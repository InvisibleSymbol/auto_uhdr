# arw2uhdr — Architecture & Design

*Phase 2. Builds directly on `01-research-findings.md`. Defines the module layout, the cgo
boundaries, the pipeline data-flow, the HDR-derivation strategy, the CLI contract, the external
batch script, the build/distribution story, and the verification plan. Concrete enough to implement
against in Phase 3.*

Working name for the tool/binary: **`arw2uhdr`** (Go module `github.com/invis/arw2uhdr`).
Rename freely — it's referenced in one place (`go.mod`) plus the CLI usage string.

---

## 1. Goals & non-goals

**Goals**
- One command: `arw2uhdr photo.ARW photo.JPG` → `photo_uhdr.jpg` (a valid Ultra HDR file).
- Self-contained binary (cgo-linked LibRaw + libultrahdr + libjpeg-turbo); no runtime shell-outs on
  the happy path.
- RAW-derived HDR **geometrically aligned** to the camera JPEG using Sony's embedded lens profile.
- Camera JPEG is the SDR base, untouched; RAW supplies recovered highlights.
- Deterministic, scriptable, machine-readable status for batch use.

**Non-goals (v1)**
- Not a general RAW editor (no creative grading UI, no sliders beyond what the pipeline needs).
- Not a re-encoder of the SDR JPEG (we preserve the user's exact JPEG bytes as the base).
- Batch orchestration itself lives in the external script, not the binary (binary stays single-pair).
- Non-Sony RAW formats are out of scope for v1 (architecture leaves room; see §11).

---

## 2. High-level pipeline

```
  photo.ARW ─┐
             │  (1) decode: LibRaw → linear 16-bit RGB, camera WB, JPEG primaries
             ▼
        RAW linear image (sensor geometry, NOT lens-corrected)
             │  (2) read Sony correction params (pure-Go EXIF/SubIFD)
             │  (3) apply distortion (+CA) warp  → geometry matches the JPEG
             ▼
        RAW linear image, ALIGNED to JPEG frame + resolution
             │
  photo.JPG ─┤  (0) parsed in parallel: bytes (SDR base), dims, ICC/primaries, EXIF WB, orientation
             ▼
             │  (4) build HDR intent buffer:
             │      - anchor exposure so JPEG diffuse-white = linear 1.0
             │      - default mode: JPEG-anchored highlight recovery (see §5)
             │      - carry recovered highlights linearly above 1.0
             │  (5) → linear RGBA float16, in the SDR's primaries
             ▼
        HDR raw buffer (rgbaf16, UHDR_CT_LINEAR, cg = SDR primaries)
             │  (6) libultrahdr API-3: SDR = original JPEG bytes, HDR = rgbaf16
             ▼
        photo_uhdr.jpg   (SDR JPEG + gain map + MPF + XMP&ISO metadata)
```

Stages (2)+(3) and (4) are the quality-critical, novel work. Stages (1) and (6) are mechanical
library calls.

---

## 3. Module / package layout

```
arw2uhdr/
├── go.mod
├── cmd/
│   └── arw2uhdr/
│       └── main.go            # CLI: flag parsing, wiring, exit codes, --json output
├── internal/
│   ├── pipeline/
│   │   └── pipeline.go        # Process(arw, jpg, Opts) (Result, error); orchestrates stages
│   ├── raw/                   # cgo → LibRaw
│   │   ├── libraw.go          # cgo wrapper over libraw_c_api.h
│   │   └── decode.go          # Decode(path, DecodeOpts) (*imaging.Image, Meta, error)
│   ├── sonylens/             # PURE GO
│   │   ├── params.go          # read DistortionCorrParams/CA/Vignetting from ARW (EXIF SubIFD)
│   │   ├── cipher.go          # (optional) ExifTool-style decipher for enciphered maker note
│   │   └── warp.go            # distortion + CA warp (the geometric aligner)
│   ├── sdrjpeg/              # PURE GO
│   │   └── jpeg.go            # read bytes, dims, ICC→primaries, EXIF WB, orientation
│   ├── color/                # PURE GO
│   │   ├── transfer.go        # sRGB OETF/EOTF, linearization
│   │   ├── primaries.go       # sRGB/P3/BT2100 matrices, gamut convert, luma weights
│   │   └── tone.go            # exposure anchoring / tone matching helpers
│   ├── hdrbuild/             # PURE GO
│   │   └── hdrbuild.go        # build the linear HDR buffer (the two modes, §5)
│   ├── ultrahdr/             # cgo → libultrahdr
│   │   └── encode.go          # Encode(sdrJPEG []byte, hdr *imaging.Image, Opts) ([]byte, error)
│   ├── imaging/              # PURE GO
│   │   ├── image.go           # float32 RGBA image type, alloc, subsample
│   │   └── resample.go        # bilinear/bicubic sampling, resize
│   └── verify/               # PURE GO (+ optional cgo decode)
│       └── verify.go          # alignment diff, output validity, round-trip check
└── scripts/
    └── batch.sh               # external batch driver (pair discovery + parallel invocation)
```

Design rules:
- **cgo is quarantined** to exactly two packages (`raw`, `ultrahdr`) plus optional decode in
  `verify`. Everything else is pure Go and unit-testable without native libs. This keeps the
  quality-critical warp/colour code portable and fast to iterate.
- `imaging.Image` is the lingua franca between stages: `struct{ W, H int; Pix []float32; // RGBA,
  linear; Space color.Primaries }`. Float32 in memory; convert to float16 only at the libultrahdr
  boundary.

---

## 4. cgo boundaries (the two native wrappers)

### 4.1 `internal/raw` — LibRaw

```go
/*
#cgo pkg-config: libraw_r          // reentrant build: safe with one handle per goroutine
#include <libraw/libraw.h>
#include <stdlib.h>
*/
import "C"
```

`Decode(path string, o DecodeOpts) (*imaging.Image, Meta, error)`:
1. `h := C.libraw_init(0)`; defer `libraw_close(h)`.
2. Set params on `h.params`: `output_bps=16`, `gamm[0]=gamm[1]=1.0` (linear),
   `no_auto_bright=1`, `use_camera_wb=1` (unless overridden), `output_color` = mapped from the
   JPEG's primaries (1=sRGB / 2=Adobe / 8=Rec2020 if available), `user_qual` = demosaic choice,
   `highlight` = recovery choice.
3. `open_file → unpack → dcraw_process → dcraw_make_mem_image`.
4. Copy the interleaved 16-bit buffer out with `C.GoBytes`, convert to `imaging.Image` (float32,
   normalized so 16-bit max → a known reference; keep values linear).
5. Also capture `Meta`: make/model, as-shot WB multipliers, black/white levels, `imgdata.sizes`
   (raw vs visible dims, margins, `flip`), lens id.
6. `dcraw_clear_mem`; `recycle`.

`Meta.Flip` and the visible-area margins are needed so the warped RAW is oriented and cropped to
match the JPEG.

### 4.2 `internal/ultrahdr` — libultrahdr (API-3)

```go
/*
#cgo pkg-config: libuhdr
#include <ultrahdr_api.h>
#include <stdlib.h>
*/
import "C"
```

`Encode(sdrJPEG []byte, hdr *imaging.Image, o EncodeOpts) ([]byte, error)`:
1. `enc := C.uhdr_create_encoder()`; defer release.
2. Fill a `uhdr_compressed_image_t` pointing at `sdrJPEG` (cg from JPEG primaries, ct=SRGB,
   full range) → `uhdr_enc_set_compressed_image(enc, &c, UHDR_SDR_IMG)`.
3. Convert `hdr` to **float16 RGBA**, fill `uhdr_raw_image_t{ fmt:64bppRGBAHalfFloat, ct:LINEAR,
   cg: <SDR primaries>, range:FULL, w,h,planes[0],stride[0] }` →
   `uhdr_enc_set_raw_image(enc, &r, UHDR_HDR_IMG)`.
4. `uhdr_enc_set_quality(enc, o.GainMapQuality, UHDR_GAIN_MAP_IMG)`;
   `uhdr_enc_set_gainmap_scale_factor(enc, o.GainMapScale)`;
   optional `uhdr_enc_set_using_multi_channel_gainmap`, `_set_min_max_content_boost`,
   `_set_gainmap_gamma`.
5. `uhdr_encode(enc)`; `out := uhdr_get_encoded_stream(enc)`; **copy** `out.data[:out.data_sz]` into a
   Go `[]byte` before releasing the encoder.

Pin a known-good libultrahdr version and build it with `-DUHDR_WRITE_XMP=1 -DUHDR_WRITE_ISO=1` so the
output carries both metadata dialects.

---

## 5. HDR-derivation strategy (the heart of it)

The gain map encodes `log2(HDR_linear / SDR_linear)` per pixel, and libultrahdr linearizes the JPEG
itself. So the design question is: *what HDR_linear buffer do we hand it?* Two modes, default =
`highlight`.

### 5.1 Mode `highlight` (default) — JPEG-anchored highlight recovery

Rationale: our SDR is a **fixed** camera JPEG with a baked-in S-curve we can't reproduce exactly.
Trying to tone-match a full RAW develop to it risks midtone/shadow ratio errors (casts, tone
reversals — see findings §4.1). Instead we make the HDR **equal to the JPEG through the SDR range**
and only diverge where the JPEG has clipped/rolled off highlights, splicing in RAW-recovered detail
mapped above linear 1.0. Result: gain ≈ 0 across shadows/midtones (no artifacts), positive gain only
in highlights — exactly "recover the blown highlights using the RAW," which is the stated intent.

Steps:
1. Linearize the JPEG: `sdrLin = sRGB_EOTF(jpeg_rgb)`, values in [0,1], where **1.0 = SDR diffuse
   white**.
2. Anchor the RAW exposure: find scale `k` so that RAW linear matches `sdrLin` in a well-exposed,
   non-clipped, non-shadow luminance band (robust regression over midtone pixels, both images
   aligned). Now `rawLin*k` shares the JPEG's white point; highlights the JPEG clipped sit at
   `rawLin*k > 1.0`.
3. Build a **highlight blend weight** `w(x) = smoothstep(t0, t1, jpegLuma(x))` that ramps 0→1 as the
   JPEG approaches clipping (e.g. `t0=0.85`, `t1=0.98`), so only near-clipped regions take RAW data.
4. `hdrLin(x) = (1-w)*sdrLin(x) + w * max(sdrLin(x), rawLin(x)*k)`. Below the highlight band this is
   exactly `sdrLin` (gain 0); in highlights it lifts to the RAW value (>1.0).
5. Clamp the peak to `--max-boost` (converted to linear); this sets `GainMapMax = log2(peak)`.
6. Optional symmetric shadow lift from RAW (`--shadow-recovery`), off by default.

This mode needs the RAW **aligned** (stage 3) and **exposure-anchored**, but not tone-matched —
which is why it's robust. It's the recommended default.

### 5.2 Mode `develop` — full linear RAW as HDR

For users who want a richer, "creative" HDR (brighter midtones, deeper shadows), matching how
`dt_ultrahdr`/ACR operate: develop the whole RAW to linear, WB + primaries matched to the JPEG,
exposure-anchored so diffuse white = 1.0, **not** tone-mapped (kept linear), highlights carried >1.0.
Hand that entire buffer to libultrahdr and let it compute the full gain map. More dynamic-range
"pop," but the HDR won't perfectly track the JPEG's look in midtones (a deliberate creative choice).

### 5.3 Why not "compute the gain map ourselves"

We let libultrahdr compute it in both modes (findings §4.5): it already implements the spec-correct
sRGB linearization, primaries/luma handling, common-nit normalization, min/max-boost selection,
log/gamma/8-bit encode, downsample, MPF container, and dual metadata — all validated against consumer
decoders. We only shape the **HDR buffer**; that's where our value-add (alignment + highlight
recovery) lives.

---

## 6. Geometric alignment (stage 3) in detail

Sequence inside `sonylens`:
1. `params.go` reads `DistortionCorrParams`, `ChromaticAberrationCorrParams`,
   `VignettingCorrParams`, `DistortionCorrParamsNumber` from the ARW. **Primary path: pure-Go walk
   of the Exif SubIFD** (un-enciphered copy, count-prefixed arrays). If absent, **fallback**: decipher
   the maker-note copy (`cipher.go`) or, if `--exiftool-fallback`, shell out to `exiftool -b -j`.
2. `warp.go` builds radial spline `g_d(r)` from the 16/11 distortion knots (scale `2^-14`, knots
   equi-spaced centre→corner), computes the autoscale `s`, and inverse-maps output→source:
   `source(X) = C + (X−C)·g_d(r)/s`. Per-channel CA adds `(1 + v_cr/2^21)` / `(1 + v_cb/2^21)` factors
   to R/B. Bilinear (default) or bicubic sampling of the RAW linear image.
3. Orientation & framing: apply `Meta.Flip` and crop the RAW visible area so the warped result has
   the **same orientation, aspect, and pixel dimensions as the JPEG**; resample to the JPEG's exact
   W×H so subsequent per-pixel ops line up.
4. `--lens-correction` controls scope: `distortion+ca` (default), `distortion`, `off` (debug),
   `auto` (apply only what `DistortionCorrParamsPresent`/`DistortionCorrection` flags indicate the
   camera applied). Vignetting is brightness-only and off by default for alignment (add
   `--vignetting` to also match the JPEG's shading if the raw isn't already shaded).
5. **Fine-registration safety net:** after the analytic warp, estimate a residual global shift by
   phase correlation between the developed-RAW luma and the JPEG luma; if it exceeds a threshold,
   apply the sub-pixel shift (and warn). Guards against per-body normalization quirks flagged in the
   research (APS-C crop scale, knot-position convention).

Everything here is pure Go and unit-testable with synthetic checkerboard/grid inputs and, later, a
real pair.

---

## 7. CLI contract

```
arw2uhdr [flags] <input.arw> [input.jpg]

Positional:
  input.arw            Sony RAW file (required)
  input.jpg            Paired SDR JPEG. If omitted, inferred from the ARW basename
                       (e.g. DSC01234.ARW → DSC01234.JPG, case-insensitive).

Output:
  -o, --output PATH    Output Ultra HDR JPEG (default: <jpgbase>_uhdr.jpg)
      --skip-existing  Exit 0 without work if the output already exists (batch idempotency)

HDR:
      --hdr-mode MODE       highlight (default) | develop
      --max-boost STOPS     Peak highlight boost cap in stops (default 3.0 ≈ 8x). 0 = auto
      --shadow-recovery     Also lift shadows from RAW (highlight mode; default off)
      --highlight-blend A,B JPEG-luma ramp for highlight splice (default 0.85,0.98)

Gain map:
      --gainmap-quality N   JPEG quality of the gain map, 1-100 (default 90)
      --gainmap-scale N     Downsample factor per dimension (default 4)
      --multi-channel       3-channel gain map (default: single-channel luminance)

RAW develop:
      --demosaic ALG        ahd (default) | dcb | dht | vng | ppg
      --highlights MODE     clip | blend | reconstruct (LibRaw highlight handling; default blend)
      --wb MODE             camera (default) | as-shot | auto
      --primaries P         auto (default, from JPEG ICC) | srgb | p3 | bt2100

Lens correction:
      --lens-correction S   distortion+ca (default) | distortion | auto | off
      --vignetting          Also apply Sony vignetting (default off)
      --exiftool-fallback   Use exiftool if the pure-Go metadata read fails

Diagnostics / control:
      --verify              Run post-encode validity + alignment checks, print a report
      --dump-intermediate D Write intermediate TIFFs (aligned RAW, HDR preview) to dir D
      --json                Emit a single machine-readable JSON status line on stdout
  -v, --verbose             Verbose logging to stderr
  -q, --quiet               Suppress non-error output
      --version
```

**Exit codes** (for the batch script): `0` success, `2` bad usage/args, `3` input read/parse error,
`4` RAW decode error, `5` lens-metadata missing (and no fallback), `6` alignment failed threshold,
`7` encode error, `8` output write error. `--json` emits e.g.
`{"status":"ok","input_arw":"…","input_jpg":"…","output":"…","max_boost_stops":2.7,
"gainmap_bytes":48213,"residual_shift_px":0.4,"elapsed_ms":1830}` (or `"status":"error","code":6,
"message":"…"`).

Flag library: stdlib `flag` is enough, but `spf13/pflag` gives the GNU `--long`/`-s` style shown
above with almost no cost. Pick `pflag` (single small dep) for ergonomics; no subcommands needed.

---

## 8. External batch script (`scripts/batch.sh`)

Contract: discover ARW+JPEG pairs under one or more directories and invoke `arw2uhdr` once per pair,
in parallel, with robust logging — the binary stays single-pair.

```bash
#!/usr/bin/env bash
# Usage: batch.sh [-j N] [-o OUTDIR] [--skip-existing] [--] SRC [SRC...]
#   Finds *.ARW with a sibling *.JPG (same basename) and converts each to Ultra HDR.
set -euo pipefail
JOBS="${JOBS:-$(nproc)}"; OUTDIR=""; EXTRA=(); BIN="${ARW2UHDR:-arw2uhdr}"
# ...arg parsing (getopts) sets JOBS, OUTDIR, EXTRA(+=--skip-existing), SRC dirs...

# 1) discover pairs → NUL-delimited "arw\tjpg" lines
find "${SRCS[@]}" -type f \( -iname '*.arw' \) -print0 |
while IFS= read -r -d '' arw; do
  base="${arw%.*}"
  for ext in JPG jpg JPEG jpeg; do
    [[ -f "$base.$ext" ]] && { printf '%s\t%s\0' "$arw" "$base.$ext"; break; }
  done
done |
# 2) fan out over cores; one arw2uhdr per pair; per-pair log line
xargs -0 -P "$JOBS" -I{} bash -c '
  IFS=$'"'"'\t'"'"' read -r arw jpg <<< "$1"
  out="${OUTDIR:+$OUTDIR/}$(basename "${jpg%.*}")_uhdr.jpg"
  if "'"$BIN"'" --json ${EXTRA[*]:-} -o "$out" "$arw" "$jpg"; then
     echo "OK   $arw"
  else echo "FAIL $arw (code $?)" >&2; fi
' _ {}
```

Notes: pairing is basename-based, case-insensitive extension; `--skip-existing` makes reruns cheap;
`-P N` parallelism (each `arw2uhdr` uses `libraw_r`, safe as separate processes); failures logged,
not fatal (so one bad file doesn't abort a 2,000-image night run). A `--json` line per invocation
lets a wrapper aggregate stats. Ship a GNU `parallel` variant in comments for users who prefer it.

---

## 9. Build & distribution ("self-contained binary")

cgo means we depend on LibRaw, libultrahdr, libjpeg-turbo (+ their transitive deps: lcms2, zlib).
Options, in order of preference:

1. **Static-linked release binary via a build container.** A `Dockerfile` builds libjpeg-turbo,
   LibRaw (`libraw_r`), and libultrahdr (`-DUHDR_WRITE_XMP=1 -DUHDR_WRITE_ISO=1`,
   `-DBUILD_SHARED_LIBS=OFF`) as static archives, then `CGO_ENABLED=1 go build` with
   `-ldflags '-linkmode external -extldflags "-static"'` (musl toolchain to avoid glibc static
   pitfalls). Produces a single portable Linux binary. This is the "self-contained" deliverable.
2. **Dynamic link + documented deps** for local dev (`apt install libraw-dev libjpeg-turbo-dev` +
   built libuhdr). Faster iteration; what we'll use during implementation.
3. A `Makefile` wraps both (`make dev`, `make static`, `make deps`), and pins library versions in a
   `deps.env` for reproducibility.

macOS/Windows are secondary; the same three libs build there (Homebrew/vcpkg), but v1 targets Linux
static + Linux/macOS dynamic.

---

## 10. Verification plan (`--verify` + tests)

Correctness is checked at three levels:

1. **Unit (pure Go, no native libs):** colour transfer round-trips (sRGB EOTF∘OETF ≈ identity),
   primaries matrix inversions, the distortion warp on synthetic grids (a known barrel curve must
   invert to straight lines), CA channel-scale on a synthetic radial target, HDR-build math
   (gain 0 where HDR=SDR, `log2(peak)` at a set highlight).
2. **Integration (needs the native libs, plus a real ARW+JPEG pair):**
   - *Alignment:* phase-correlation residual between developed-aligned RAW luma and JPEG luma below
     ~1 px; a difference image written by `--dump-intermediate` for eyeballing.
   - *Output validity:* re-open the result (libultrahdr decode API or `ultrahdr_app -m 1`), assert a
     gain map is present, MPF markers exist, and `hdrgm`/ISO metadata parse; confirm a legacy JPEG
     decoder still shows the SDR image.
   - *Round-trip:* reconstruct HDR at `max_display_boost = peak` from (SDR + gain map) and compare to
     the input HDR buffer (bounded error).
3. **Visual spot check:** on an HDR-capable viewer (Chrome, Android, macOS Preview) the highlights
   should gain detail while the SDR view is unchanged. (Manual; documented in a checklist.)

For high-stakes correctness, the alignment and round-trip checks will be run via a subagent/CI step
on the sample pair once provided.

---

## 11. Extensibility notes

- **Other RAW brands:** the warp/colour/HDR packages are brand-agnostic; only `raw` (LibRaw already
  decodes most brands) and `sonylens` (Sony-specific metadata) are brand-coupled. A `lens` interface
  (`CorrectionParams(meta) (Warp, error)`) lets us add Canon/Nikon/Fuji embedded profiles or a
  lensfun-backed provider later.
- **AVIF/HEIF gain-map output:** libultrahdr supports `uhdr_enc_set_output_format`; expose a
  `--container` flag in a later version (JPEG stays the interoperable default).
- **Pure-Go build:** if a native-free binary is ever wanted, swap `ultrahdr` for a Go gain-map
  computation + container writer and `raw` for a Go ARW decoder — large effort, kept behind the same
  package interfaces.

---

## 12. Phase 3 implementation order (proposed)

1. Scaffolding: `go.mod`, `imaging.Image`, CLI skeleton, `Makefile`, dev-deps install script.
2. `sdrjpeg` (pure Go) — parse JPEG dims/ICC/EXIF-WB/orientation. Testable immediately.
3. `raw` cgo wrapper — decode ARW to linear float; verify against a known TIFF develop.
4. `sonylens/params` (pure Go) — read the correction arrays; dump + sanity-check on a sample.
5. `color` + `sonylens/warp` — the aligner; unit tests on synthetic grids, then the sample pair.
6. `hdrbuild` — highlight mode first, then develop mode.
7. `ultrahdr` cgo wrapper — API-3 encode; produce a first real Ultra HDR file.
8. `verify` + `--verify`; wire `pipeline` end-to-end; `--json`.
9. `scripts/batch.sh`; static build container; docs/README.
10. End-to-end verification on the sample pair; iterate on alignment/exposure anchoring.

Steps 2, 4, 5, 6, 8, 9 are pure Go and can be built/tested before the native libs are wired; steps 3
and 7 are the cgo bring-up. A **real ARW+JPEG sample pair** unblocks steps 3–5 verification and step
10 — the main external dependency.
