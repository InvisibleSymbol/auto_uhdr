# RAW+JPEG → Ultra HDR — Research Findings

*Phase 1 of the project. This document consolidates the technical research that anchors the
architecture, CLI design, and implementation. Every non-obvious claim is sourced. Confidence is
flagged where the underlying facts are reverse-engineered rather than officially documented.*

Project constraints (decided with the user):

- **Language / packaging:** Go, single self-contained binary, using **cgo bindings** to native
  libraries rather than shelling out.
- **Lens correction source:** **Sony's embedded profile in the ARW** (so the RAW-derived image
  stays geometrically aligned to the camera JPEG).
- **HDR strategy:** the **camera JPEG is the SDR base**; the **RAW supplies the HDR** (recovered
  highlights / extra range). The gain map is computed from the two.
- **Batch:** CLI is single-pair; batching is handled by an external script.

---

## 0. The one-paragraph mental model

An Ultra HDR file *is* a normal JPEG (the SDR image the user shot) with a second, small JPEG (the
**gain map**) appended after it and tied together by MPF + XMP/ISO metadata. The gain map stores, per
pixel, `log2(HDR/SDR)` — the recipe an HDR display uses to reconstruct extra brightness from the SDR
base. So our job is: (1) decode the ARW to a linear, high-bit-depth image; (2) apply the **same lens
distortion the camera baked into its JPEG** so the two line up pixel-for-pixel; (3) develop the RAW so
its colour, white balance and base tone **match the JPEG**, differing *only* by carrying recovered
highlights above SDR white; (4) hand the JPEG (as the SDR base) and the linear HDR buffer to
**libultrahdr**, which computes the gain map and muxes the final Ultra HDR file. The encoder is the
easy part. The engineering effort is in steps 2–3: conforming the RAW development to a fixed camera
JPEG.

---

## 1. Ultra HDR format & libultrahdr (the encoder)

### 1.1 Container

- An Ultra HDR / JPEG_R file = **[primary SDR JPEG]** immediately followed by **[secondary gain-map
  JPEG]**. The primary is a fully valid baseline JPEG ending in its own EOI, so legacy viewers show
  only the SDR image and never parse the appended gain map.
- The two images are linked by an **MPF (Multi-Picture Format) APP2** index on the primary (CIPA
  DC-x 007-2009) recording each image's byte offset + length. A **GContainer XMP** packet (APP1,
  `http://ns.google.com/photos/1.0/container/`) also lists Primary + GainMap items.
- A file is *detected* as Ultra HDR by `hdrgm:Version="1.0"` in the primary's XMP (Adobe's
  `http://ns.adobe.com/hdr-gain-map/1.0/` namespace).
- The gain-map image carries the gain-map metadata (`hdrgm:*` in XMP and/or an ISO 21496-1 packet).

### 1.2 Gain-map math (encode → decode)

Working in **linear light** on a common scale, with `Ysdr`, `Yhdr` the SDR/HDR luminances:

```
pixel_gain(x)    = (Yhdr(x) + offset_hdr) / (Ysdr(x) + offset_sdr)

map_min_log2     = log2(min_content_boost)          # min_content_boost ∈ (0,1], = 1 for us
map_max_log2     = log2(max_content_boost)          # ≥ 1

log_recovery(x)  = (log2(pixel_gain(x)) - map_min_log2) / (map_max_log2 - map_min_log2)
recovery(x)      = clamp(log_recovery(x), 0, 1) ^ map_gamma
encoded(x)       = round(recovery(x) * 255)         # 8-bit gain-map pixel
```

Decode (what a player does), where `max_display_boost = HDR_white/SDR_white` of the actual display:

```
weight        = clamp((log2(max_display_boost) - hdr_capacity_min)
                       / (hdr_capacity_max - hdr_capacity_min), 0, 1)
log_boost(x)  = map_min_log2*(1 - log_recovery(x)) + map_max_log2*log_recovery(x)
HDR(x)        = (SDR(x) + offset_sdr) * exp2(log_boost(x) * weight) - offset_hdr
```

`weight` ramps 0→1 as the display's headroom grows, which is why gain maps degrade gracefully
(SDR-only display → `weight=0` → shows the base JPEG). **Reconstruction must use exponentiation, not
linear interpolation.**

Metadata parameters (XMP `hdrgm:` stores **log2** values; the libultrahdr C struct stores **linear**
boosts — do not double-convert):

| Field | Meaning | Default | Our value |
|---|---|---|---|
| `GainMapMin` | `log2(min_content_boost)` | 0.0 | ~0 (HDR ≥ SDR everywhere) |
| `GainMapMax` | `log2(max_content_boost)` | **required** | `log2(peak recovered boost)` |
| `Gamma` | `map_gamma` | 1.0 | 1.0 to start |
| `OffsetSDR` / `OffsetHDR` | ratio offsets | 0.015625 (1/64) | leave to encoder |
| `HDRCapacityMin` | `log2(min_display_boost)` | 0.0 | ~0 |
| `HDRCapacityMax` | `log2(max_display_boost)` | **required** | usually = `GainMapMax` |
| `BaseRenditionIsHDR` | is primary the HDR image? | False | **False** (base = SDR) |

### 1.3 Metadata dialects — write BOTH

- **Ultra HDR v1 (Google/Adobe):** the `hdrgm:*` XMP packet. Read by Android 14, older tooling,
  browsers.
- **ISO 21496-1:2025:** standardized binary metadata in an APP2 box; Android 15+ / newer decoders.
- **Current libultrahdr (v1.4.0) defaults to writing ISO only** (`UHDR_WRITE_ISO=TRUE`,
  `UHDR_WRITE_XMP=FALSE`). For maximum compatibility, **build with both**
  `-DUHDR_WRITE_XMP=1 -DUHDR_WRITE_ISO=1` (matches Android's "encode both" guidance). This is a
  **compile-time** decision, baked into how we build the lib.

### 1.4 The encode API we will use (cgo)

libultrahdr exposes five encode "scenarios." **Ours is API-3: raw HDR buffer + *compressed* SDR
JPEG → library computes + compresses the gain map and muxes the container.** That is exactly
"camera JPEG as SDR base + RAW-derived HDR."

Minimal C flow (flat C-linkage header `ultrahdr_api.h`, cgo-friendly):

```c
uhdr_codec_private_t* enc = uhdr_create_encoder();
uhdr_enc_set_compressed_image(enc, &sdr_jpeg, UHDR_SDR_IMG);   // camera JPEG bytes
uhdr_enc_set_raw_image(enc, &hdr_raw, UHDR_HDR_IMG);           // linear rgbaf16
uhdr_enc_set_quality(enc, 90, UHDR_GAIN_MAP_IMG);
uhdr_enc_set_gainmap_scale_factor(enc, 4);                     // downsample gain map ~1/4/dim
uhdr_encode(enc);
uhdr_compressed_image_t* out = uhdr_get_encoded_stream(enc);   // copy out->data before release
uhdr_release_encoder(enc);
```

Relevant types/enums:

- `uhdr_raw_image_t{ fmt, cg, ct, range, w, h, planes[3], stride[3] }`.
- HDR input format for a develop-from-RAW float pipeline: **`UHDR_IMG_FMT_64bppRGBAHalfFloat`
  (rgbaf16)** tagged **`ct = UHDR_CT_LINEAR`**. (Alternatives: P010 HLG/PQ, RGBA1010102 — avoid the
  YUV/chroma pitfalls; float linear is what our developer naturally produces.)
- Gamut `uhdr_color_gamut_t`: `UHDR_CG_BT_709(0)`, `UHDR_CG_DISPLAY_P3(1)`, `UHDR_CG_BT_2100(2)`.
- Transfer `uhdr_color_transfer_t`: `UHDR_CT_LINEAR(0)`, `HLG(1)`, `PQ(2)`, `SRGB(3)`.
- Setters worth knowing: `uhdr_enc_set_quality`, `_set_using_multi_channel_gainmap`,
  `_set_gainmap_scale_factor`, `_set_gainmap_gamma`, `_set_min_max_content_boost`,
  `_set_target_display_peak_brightness`, `_set_output_format` (JPG/HEIF/AVIF), `_set_preset`
  (REALTIME | BEST_QUALITY).
- Defaults: base/gain-map **quality 95/95**; gain-map **scale factor 1** (full-res — we should set 4
  to follow the spec and shrink the file).

### 1.5 Build / link

- Deps: **libjpeg-turbo**. Build with CMake+Ninja; installs `libuhdr.so`/`.a`, `ultrahdr_api.h`,
  and a `libuhdr.pc` pkg-config file → `#cgo pkg-config: libuhdr`.
- **No maintained Go/cgo binding exists** (only JNI/Java in-repo, plus Rust/Python/C# third-party).
  We write our own thin cgo wrapper — the API is flat POD structs + int enums, so it's mechanical.

### 1.6 Gotchas

- SDR base and HDR raw represent the same scene and (per the per-pixel ratio) should be the **same
  width/height**. The **gain map itself** may be lower resolution (that's the `scale_factor`).
- Set `cg`/`ct`/`range` correctly on *every* buffer — the encoder trusts the tags literally; a wrong
  transfer tag is the #1 way to produce a broken gain map.
- Start with a **single-channel** (luminance) gain map (smaller, the common default); 3-channel
  preserves per-channel colour boosts but amplifies any SDR/HDR mismatch and noise.
- Pin a libultrahdr version; confirm the rgbaf16+linear path on that version.

---

## 2. Decoding the ARW (LibRaw via cgo)

### 2.1 Pipeline

Use LibRaw's **C API** (`libraw_c_api.h` — cgo-friendly; the primary API is C++ and should not be
called from cgo directly). Handle is an opaque `libraw_data_t*`:

```
libraw_init(0) → set params → libraw_open_file → libraw_unpack
  → libraw_dcraw_process → libraw_dcraw_make_mem_image → (copy) → libraw_dcraw_clear_mem
  → libraw_recycle (reuse) / libraw_close
```

Skip `raw2image()` (legacy). `dcraw_make_mem_image` returns `libraw_processed_image_t{ type, height,
width, colors, bits, data_size, data[] }` — interleaved RGB, `bits` = 8 or 16 (host byte order),
`data_size` authoritative. Copy out with `C.GoBytes` before `clear_mem`.

### 2.2 Params for a linear, controllable develop

Set directly on the handle (`lr->params.*`):

| Field | Setting | Why |
|---|---|---|
| `output_bps` | `16` | high bit depth for HDR headroom |
| `gamm[0]=gamm[1]=1.0` | linear transfer | we want scene-linear, not BT.709 |
| `no_auto_bright` | `1` | reproducible; no histogram stretch |
| `use_camera_wb` | `1` | **match the JPEG's as-shot WB** |
| `output_color` | `1` (sRGB) or `2` (Adobe) / native | match JPEG primaries (see §4) |
| `highlight` | `0` clip … `2` blend … `3–9` reconstruct | highlight recovery control |
| `user_qual` | `3` AHD / `4` DCB / `11` DHT … | demosaic quality |
| `half_size` | `0` | full res (1 = fast preview) |

### 2.3 Embedded preview vs. the JPEG file

LibRaw can extract the ARW's embedded JPEG (`unpack_thumb` → `dcraw_make_mem_thumb`, returns a
ready-to-save JPEG). **But Sony's embedded preview is usually reduced resolution (~1616×1080), not
full-res.** Since the user is providing the paired full-resolution JPEG, **use that file as the SDR
base**; treat the embedded preview only as a fallback/diagnostic. Check
`imgdata.thumbnail.twidth/theight` at runtime.

### 2.4 What LibRaw does *not* do (critical)

- **LibRaw applies no geometric lens correction** — maintainer: *"There is no barrel correction code
  in LibRaw postprocessing… Use lensfun."* So distortion/CA warp is **our** job.
- **LibRaw does not parse Sony's embedded correction coefficients.** It gives lens *identity*
  (`imgdata.lens`), exposure (`imgdata.other`), colour matrices + black/white levels
  (`imgdata.color`), but not the `DistortionCorrParams` arrays. We read those separately (see §3).

### 2.5 Build / threading

- `apt install libraw-dev` → headers in `/usr/include/libraw/`, pkg-config modules **`libraw`** and
  **`libraw_r`** (reentrant). For concurrent decode (a goroutine worker pool, one `libraw_data_t*`
  per goroutine) **link the reentrant build**: `#cgo pkg-config: libraw_r`. A single LibRaw instance
  is **not** thread-safe; never share one across goroutines.
- Existing Go bindings (`enricod/golibraw`, `inokone/golibraw` GPL-3.0; `mrht-srproject/librawgo`
  MIT SWIG) are thin and mostly unmaintained — good references, but we write our own cgo layer over
  `libraw_c_api.h` (avoids GPL and the C++ ABI problem; ~15 functions).

---

## 3. Sony embedded lens correction (the alignment key)

**Why this matters:** the camera **applies distortion correction to its JPEG but never to the raw
pixels.** So a straight LibRaw develop is *geometrically misaligned* with the JPEG (barrel/pincushion
mismatch, worst at the corners). Since the gain map is a per-pixel ratio, misalignment → edge halos
and fringing. To align, **we must replicate the camera's distortion warp on the RAW-derived image.**

### 3.1 The tags (read via a maker-note / SubIFD parser)

| Correction | Tag | Format | Knots |
|---|---|---|---|
| Distortion | `DistortionCorrParams` | int16s[16] | 16 (FF) / 11 (APS-C) |
| Chromatic aberration | `ChromaticAberrationCorrParams` | int16s[32] | 16 red + 16 blue |
| Vignetting | `VignettingCorrParams` (`LightFalloffParams`) | int16s[16] | 16 / 11 |
| Knot count selector | `DistortionCorrParamsNumber` | int | 11 (APS-C) or 16 (FF) |

Two storage locations for the same arrays:

1. **Sony MakerNote 0x9405 (`Tag9405a`/`b`)** — *enciphered* by a byte-substitution cipher; needs
   ExifTool's `ProcessEnciphered` logic to decode.
2. **SR2 private / Exif SubIFD** — an **un-enciphered** copy, surfaced by exiv2 as
   `Exif.SubImage1.DistortionCorrParams` etc. (raw TIFF tag 0x7037 in the Exif SubIFD; array is
   *count-prefixed*: first value = 16 or 11, then that many coefficients).

**Design implication:** the SubIFD copy is a plain TIFF/EXIF tag and **not enciphered**, so a
**pure-Go TIFF/EXIF walk can read `DistortionCorrParams` directly** without porting the cipher — the
cleanest path for a self-contained binary. (Fallback: shell out to exiftool/exiv2, or port the cipher
+ offset tables from ExifTool's `Sony.pm`.) *Confidence: the un-enciphered SubIFD location is
documented via exiv2/variousphotography; verify tag offsets against real sample files.*

### 3.2 Coefficient scaling *(reverse-engineered, cross-validated by darktable + uchrisu + fdw)*

All signed 16-bit, 16 (or 11) equi-spaced radial knots from centre to corner:

```
distortion:   g_d(r)  = 1 + v_d(r)  / 2^14
CA (red):     g_r(r)  = 1 + v_cr(r) / 2^21       # relative to green
CA (blue):    g_b(r)  = 1 + v_cb(r) / 2^21
vignetting:   illum(r) = 1 - v_v(r) / 2^14        # relative illumination (brightness only)
```

CA is expressed **relative to green** and **multiplies** the distortion factor:
`fpr = (v_cr·2^-21 + 1)·g_d`, `fpb = (v_cb·2^-21 + 1)·g_d`; green plane = distortion only.

### 3.3 The warp

Centre `C`, output pixel `X`, `r = ‖X−C‖`, `rmax` = centre-to-corner. Knot `i` sits at
`r_i = i·rmax/(n−1)`. Interpolate (cubic/spline) the scaled knots to get `g_d(r)`. Sony *stretches*
rather than crops, so **autoscale**: normalize by the max factor over the frame so nothing exceeds 1,
then inverse-map output → source:

```
s          = max over frame of g_d(‖X−C‖)
source(X)  = C + (X−C) · g_d(r) / s        # sample the demosaiced raw here (bilinear)
```

Per-channel for CA: warp G with `g_d/s`, R with `g_d/s·(1+v_cr/2^21)`, B with `g_d/s·(1+v_cb/2^21)`.

### 3.4 What's baked where (drives what we must replicate)

- **Distortion (geometry): in JPEG, NOT in raw → MUST replicate.** (Unambiguous, multiple sources.)
- **Lateral CA (geometry, per-channel): body-dependent** — some bodies bake partial CA into the raw.
  For channel-level alignment apply the CA curve; validate per body. Secondary to distortion.
- **Vignetting (brightness only): often already baked into the raw.** Never affects geometry; only
  re-apply if matching the JPEG's brightness and the raw doesn't already have it (else double
  correction). Irrelevant to alignment.
- **APS-C crop mode:** 11 knots and a different normalization; Sony *divides* by the crop scale.
  Read `DistortionCorrParamsNumber` to detect crop mode and replicate exactly.

### 3.5 Reference implementation to cross-check against

**darktable** `src/iop/lens.cc` ("lens correction" → "embedded metadata" method), parsing in
`src/common/exif.cc`; and uchrisu's `lf_fitexif_se.py` (`calc_distortion` / `calc_tca` /
`calc_vignetting`). RawTherapee's "Automatic distortion" button does the same alignment by *matching
the raw to the JPEG image* rather than reading coefficients — a useful fallback idea.

---

## 4. HDR alignment & colour management (where quality is won or lost)

### 4.1 The invariant

libultrahdr normalizes both images to a common nit scale (`kSdrWhiteNits = 203`). For a **linear**
HDR buffer, SDR and HDR are scaled by the *same* factor, so the stored gain is literally
`log2(HDR_linear / SDR_linear)`. Therefore **where HDR == SDR, gain = 0**; positive gain appears
*only* where the RAW genuinely has more range. Every non-range difference (WB, hue, base tone,
exposure, registration) is baked into the ratio as an artifact.

### 4.2 Conform the RAW development to the JPEG

Because the SDR is a fixed camera JPEG we cannot re-render, we conform the RAW to it:

1. **White balance:** apply the camera's **as-shot WB** (the same the JPEG used — LibRaw
   `use_camera_wb=1` / EXIF `AsShotNeutral`). Never re-auto-WB.
2. **Primaries/colour:** develop into the JPEG's working space (sRGB, or Display-P3 if its ICC says
   so) using the camera colour matrix so hues line up. No creative HSL/saturation the JPEG didn't do.
3. **Base tone:** match the JPEG's tone through the SDR range (0…diffuse white), **then keep going
   above 1.0** for recovered highlights instead of rolling off/clipping.
4. **Exposure:** match mid-gray / diffuse white so a midtone patch has the same linear value in both.

Ideal: the SDR JPEG and HDR are *the same edit rendered twice*, differing only in headroom. (This is
how Adobe Camera Raw, `dt_ultrahdr`, and Google's pipeline are structured.)

### 4.3 Highlight recovery

Work in **extended-range linear where 1.0 = SDR diffuse white** (the JPEG's clip point). The JPEG
rolls off + hard-clips highlights at 255 and is 8-bit; the RAW retains those scene luminances as
values >1.0. Set exposure so diffuse white = 1.0, **do not clip above 1.0**, carry highlight detail
as linear 1.0…N (N = chosen peak boost, e.g. 4×–16× ≈ +2…+4 stops). A highlight at linear 4.0 →
`log2(4) = +2` stops of gain; flat midtones → gain ≈ 0. `GainMapMax = log2(N)`; keep it honest to the
actual recovered range.

### 4.4 Colour spaces to standardize on

- **SDR base:** read the JPEG's embedded ICC; sRGB if none (near-universal). libultrahdr can take the
  compressed JPEG directly and linearize with the piecewise-sRGB curve.
- **HDR intent:** **Linear** transfer in the **same primaries as the SDR base** (Display-P3 is the
  natural, low-surprise choice and `ultrahdr_app`'s default HDR gamut; BT.2100 for widest gamut).
  Deliver as **rgbaf16 linear**. Prefer Linear over PQ/HLG for a develop-from-RAW pipeline (no
  round-trip; matches the developer's native output).
- Luminance weights by primaries: sRGB/709 `(0.2126,0.7152,0.0722)`, P3 `(0.2290,0.6917,0.0793)`,
  BT.2100 `(0.2627,0.6780,0.0593)`.

### 4.5 Compute the gain map ourselves, or let libultrahdr do it?

**Recommendation: let libultrahdr compute it (API-3).** It already implements — and keeps in sync
with the spec/ISO standard — sRGB linearization, primaries handling, the common-nit normalization,
min/max-boost selection, log/gamma/8-bit encoding, downsampling, MPF container, and dual
(XMP + ISO) metadata. Re-implementing that in Go is a large surface for subtle colour bugs with no
quality upside, and libultrahdr is what every consumer decoder is validated against. Reserve
"compute it ourselves" for a genuine need (bespoke roll-off, custom offsets, or a future pure-Go
build with no native dep).

### 4.6 Prior art worth reading

`Mikayex/dt_ultrahdr` (single edit → SDR + Rec.2020-linear EXR → rgbaf16 → `ultrahdr_app`) is the
closest architecture, except it renders the SDR too instead of reusing a camera JPEG. darktable's
Lua `contrib/ultrahdr`, Adobe Camera Raw's gain-map export, pixls.us "Manual creation of UltraHDR
images", and a Canon-community CR3→UltraHDR thread all confirm the same rule: **SDR and HDR are the
same scene/edit, colorimetrically matched, differing only in dynamic range.**

---

## 5. Synthesized architecture implications (feeds Phase 2 design)

1. **Native deps (3):** LibRaw (`libraw_r`), libultrahdr (built with XMP+ISO), libjpeg-turbo
   (libultrahdr's dep; also handy for us). All linked via cgo/pkg-config → one static-ish binary.
2. **Sony metadata:** prefer a **pure-Go EXIF/TIFF reader** for the *un-enciphered SubIFD* copy of
   `DistortionCorrParams`/`ChromaticAberrationCorrParams`/`VignettingCorrParams`
   (+`DistortionCorrParamsNumber`), avoiding a 4th native dep and the cipher. Fallback to
   exiftool/exiv2 if a body only exposes the enciphered maker-note copy.
3. **Pipeline stages:**
   `decode ARW (LibRaw, linear 16-bit)` → `apply Sony distortion (+CA) warp to align to JPEG` →
   `conform WB/primaries/tone to JPEG, extend highlights >1.0` → `to linear rgbaf16 in SDR
   primaries` → `libultrahdr API-3 with the JPEG as SDR base` → `write Ultra HDR`.
4. **The two hard, quality-critical stages are the warp (§3) and the colour-conform (§4).** The
   encode (§1) and decode (§2) are mechanical. Budget engineering + verification accordingly.
5. **Alignment must be verified empirically** (gray card / difference image between developed RAW and
   JPEG) — the reverse-engineered warp scaling and the "what's baked into the raw" facts are
   body-dependent and need a real ARW+JPEG pair to confirm.

## 6. Open questions to resolve before/while coding

- Does the target camera body expose `DistortionCorrParams` in the un-enciphered SubIFD, or only in
  the enciphered maker note? (Determines pure-Go vs. exiftool for metadata.) — **needs a sample ARW.**
- Is lateral CA already baked into this body's raw (apply full curve vs. residual vs. none)? — sample.
- Full-frame vs. APS-C / crop-mode captures (knot count + normalization). — sample.
- Is the paired JPEG sRGB or Display-P3 (its ICC)? Sets the pipeline primaries.
- Confirm libultrahdr version + the rgbaf16/linear encode path on it; pin it.

---

### Consolidated sources

**Ultra HDR / libultrahdr:** Android Ultra HDR v1.1 spec
<https://developer.android.com/media/platform/hdr-image-format>; libultrahdr repo/README/`ultrahdr_api.h`/`CMakeLists.txt`/`docs/building.md`
<https://github.com/google/libultrahdr>; ISO 21496-1:2025 <https://www.iso.org/standard/86775.html>.

**LibRaw:** C API <https://www.libraw.org/docs/API-C.html>; C++ API
<https://www.libraw.org/docs/API-CXX.html>; data structures
<https://www.libraw.org/docs/API-datastruct-eng.html>; `libraw_types.h`
<https://raw.githubusercontent.com/LibRaw/LibRaw/master/libraw/libraw_types.h>; thread safety
<https://www.libraw.org/node/2696>; "no barrel correction" <https://www.libraw.org/node/2160>;
bindings <https://github.com/enricod/golibraw>, <https://pkg.go.dev/github.com/inokone/golibraw>,
<https://pkg.go.dev/github.com/mrht-srproject/librawgo>.

**Sony lens correction:** ExifTool `Sony.pm`
<https://github.com/exiftool/exiftool/blob/master/lib/Image/ExifTool/Sony.pm> & tag names
<https://exiftool.org/TagNames/Sony.html>; exiv2 Sony tags <https://exiv2.org/tags-sony.html>;
Stannum warp math <https://stannum.io/blog/0PwljB>; pixls.us CA model
<https://discuss.pixls.us/t/sony-raw-chromatic-aberration-correction-model/21153>; uchrisu
`lf_fitexif` <https://github.com/uchrisu/lf_fitexif>; variousphotography SR2 storage
<https://variousphotography.wordpress.com/2015/05/17/summary-sonys-arw-v2-3-1-embedded-lens-correction-data/>;
darktable issue #15647 <https://github.com/darktable-org/darktable/issues/15647>; RawPedia geometry
<https://rawpedia.rawtherapee.com/Lens/Geometry>; diglloyd baked-in corrections
<https://diglloyd.com/blog/2015/20150527_0544-Sony-lens-corrections.html>.

**HDR alignment / colour:** libultrahdr `gainmapmath.cpp` / `jpegr.cpp`
<https://github.com/google/libultrahdr>; Adobe gain map <https://helpx.adobe.com/camera-raw/using/gain-map.html>;
Greg Benz <https://gregbenzphotography.com/hdr-photos/iso-21496-1-gain-maps-share-hdr-photos/>;
`Mikayex/dt_ultrahdr` <https://github.com/Mikayex/dt_ultrahdr>; darktable `contrib/ultrahdr`
<https://docs.darktable.org/lua/stable/lua.scripts.manual/scripts/contrib/ultrahdr/>; pixls.us manual
UltraHDR <https://discuss.pixls.us/t/manual-creation-of-ultrahdr-images/45004>.
