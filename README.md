# arw2uhdr

Convert Sony **ARW + JPEG** pairs into **Ultra HDR** (JPEG_R) images — the camera JPEG stays
byte-for-byte as the SDR base, the RAW supplies lens-corrected highlight recovery, and a gain map
makes highlights pop on HDR displays. Fully backward-compatible: on SDR screens the file looks
exactly like the original JPEG.

Built for and validated against the Sony RX100M7; the architecture (LibRaw decode + count-driven
Sony lens-metadata parsing) extends to other Sony bodies.

## How it works

The conversion is a pipeline of four stages:

1. **RAW decode** — LibRaw (cgo), scene-linear 16-bit, camera white balance, highlight blending,
   decoded sensor-native (`user_flip=0`) so it stays in the same orientation as the stored JPEG.
2. **Lens correction** — parses Sony's embedded distortion/CA correction (`DistortionCorrParams`
   etc.) from the ARW's plaintext SubIFD in pure Go (no exiftool, no maker-note decryption) and
   replicates the camera's radial warp so the RAW aligns pixel-for-pixel with the JPEG, plus a
   residual registration (block matching + robust fit) that removes the small scale/framing
   mismatch between LibRaw's decode grid and the JPEG (corner error ~15 px → <1 px).
3. **HDR rendition** — the default `raw` mode gates on the JPEG (its luma masks the shadows) and
   takes the boost *magnitude* from the RAW: per channel, `gain = log2(raw·k / jpeg)`, so bright
   surfaces lift by their true scene luminance and clipped regions reconstruct genuine RAW detail
   (sharp, for free), while shadows get no boost (no dark→bright glow, no RAW noise). A soft
   log-domain shoulder compresses near the ceiling. (`highlight` and `develop` modes remain.)
4. **Gain map + container** — single- or per-channel (RGB) gain map, encoded per the Adobe/Google
   `hdrgm` math, with per-channel colour neutralized inside clipped highlights (a blown white sky
   stays neutral; coloured unclipped highlights keep their saturation). Assembled into a JPEG_R
   container (MPF index + GContainer XMP) in pure Go.

Each stage is a Go interface with a default implementation, so any of them can be swapped — see
[Extending](#extending-the-pipeline).

## Build

Requires Go ≥ 1.24 and LibRaw headers (`apt install libraw-dev` / `brew install libraw`).

```
make            # builds bin/arw2uhdr
make tools      # builds the debug/dev tools under tools/
make test       # unit tests (no samples needed; sample-based tests skip if absent)
make check      # gofmt + vet + test
```

## CLI

```
arw2uhdr convert [flags] <input.arw> [input.jpg]   convert a pair (default command)
arw2uhdr batch   [flags] <dir|file>...             convert every paired ARW under paths
arw2uhdr verify  <file.jpg>                         check a file's Ultra HDR structure
arw2uhdr inspect <file.arw>                         print the embedded Sony lens profile
arw2uhdr version
```

`convert` is the default command, so `arw2uhdr photo.arw` also works. If the JPEG is omitted it is
inferred from the ARW basename.

```
arw2uhdr convert photo.ARW                    # pairs with photo.JPG automatically (raw mode)
arw2uhdr convert --gainmap rgb photo.ARW      # per-channel gain map (coloured lights stay saturated)
arw2uhdr convert --strength 1.5 photo.ARW     # push the RAW-driven lift harder
arw2uhdr convert --hdr-mode highlight --strength 2 photo.ARW   # older synthetic-boost look
arw2uhdr convert --gainmap-scale 4 photo.ARW  # coarser gain map, smaller file
arw2uhdr convert --verify --json photo.ARW    # machine-readable result + structural self-check
arw2uhdr batch -j 2 -o out/ ~/Photos          # native parallel batch
```

Key `convert`/`batch` flags:

| flag | default | meaning |
|---|---|---|
| `--hdr-mode` | raw | `raw` (RAW-luminance-driven, JPEG-gated), `highlight` (synthetic boost), or `develop` |
| `--strength` | 1.0 | multiplier on the RAW gain (raw mode); stops of synthetic boost (highlight mode) |
| `--threshold` | 0.5 | JPEG-luma gate below which nothing is boosted (masks shadows) |
| `--ramp-width` | 0.35 | luma span over which the gate opens fully |
| `--max-boost` | 3.0 | total-boost ceiling in stops (soft shoulder) |
| `--chroma` | 0.3 | raw-mode RGB gain saturation, 0..1 (0 = neutral/≡single-channel, 1 = full per-channel colour recovery) |
| `--chroma-track` | off | ramp `--chroma` with JPEG brightness: neutral midtones (no graying), peak colour in clipped highlights |
| `--gainmap` | rgb | `rgb` (per-channel colour; needed for `--chroma` to matter) or `single` (luminance) |
| `--gainmap-scale` | 1 | gain-map downsample factor (1 = full res; raise to 2/4 for smaller files) |
| `--lens` | distortion+ca | `distortion+ca`, `distortion`, or `off` |
| `--vignetting` | off | experimental radial brightness correction (unvalidated scale) |
| `--no-register` | — | skip residual registration (debug) |

Exit codes (a stable scripting contract): `0` ok · `2` usage · `3` input · `4` raw decode ·
`5` lens metadata · `6` render · `7` encode · `8` write.

`batch` needs ~1.5 GB RAM per job at 20 MP; scale `-j` accordingly. ~10 s per 20 MP image on a
modern multi-core machine. A POSIX shell equivalent lives at `scripts/batch.sh` for environments
without the Go binary's native batch.

## Library

```go
import "github.com/invis/arw2uhdr"

res, err := arw2uhdr.Convert(ctx, arw2uhdr.Input{
    ARW:    "photo.ARW",
    JPEG:   "photo.JPG",
    Output: "photo_uhdr.jpg",
}, arw2uhdr.DefaultOptions())
```

`Convert` is a one-shot convenience over `New(opts).Convert(ctx, in)`. The context is honoured at
every stage boundary, so long batches cancel promptly.

### Extending the pipeline

`New` wires four swappable stages onto a `Converter`. Replace any of them to plug in custom
behaviour — e.g. a different raw handler, or an alternative Ultra HDR generator (a native encoder,
libultrahdr, ISO 21496-1):

```go
type RawDecoder interface {
    Decode(ctx context.Context, arwPath string) (*Image, *RawMeta, error)
}
type LensCorrector interface {
    Correct(ctx context.Context, img *Image, arwPath string) (*Image, error)
}
type HDRRenderer interface {
    Render(ctx context.Context, sdrLinear, rawLinear *Image) (*Image, error)
}
type UltraHDREncoder interface {
    Encode(ctx context.Context, baseJPEG []byte, sdrLinear, hdrLinear *Image) (EncodeResult, error)
}

conv := arw2uhdr.New(opts)
conv.Encoder = myLibUltraHDREncoder{}   // keep the rest of the pipeline
res, err := conv.Convert(ctx, in)
```

The default implementations are exported constructors (`NewLibRawDecoder`, `NewEmbeddedLensCorrector`,
`NewHighlightRenderer`, `NewGoEncoder`) so a custom stage can wrap and delegate to them.

## Repository layout

```
.                    library API: Converter, stages, Options, errors
cmd/arw2uhdr         the CLI (thin dispatcher over internal/cli)
internal/
  cli                command implementations (testable)
  raw                LibRaw cgo decode
  sonylens           ARW lens-metadata parse + geometric/photometric warp
  register           residual affine registration
  hdrbuild           HDR-linear rendition
  gainmap            gain-map compute / reconstruct
  ultrahdr           JPEG_R container writer + verify
  imaging            float32 RGB image, parallel loops, box blur
  color              sRGB transfer functions
  xmath              shared numeric helpers (clamp, smoothstep, lerp)
tools/               debug/dev commands (warptest, gmtest, hdrcollage, …)
docs/                research notes, design, and the refactor architecture
```

## Notes & limitations

- The paired JPEG must be the full-resolution camera JPEG (shoot RAW+JPEG). The ~1616×1080
  preview embedded in the ARW is too small to serve as the SDR base.
- Output is the original-JPEG variant of Ultra HDR v1 (`hdrgm` XMP + MPF), read by Android 14+,
  Chrome, Google Photos, and most gain-map-aware viewers. ISO 21496-1 dual-write is a possible
  future addition (implementable as a custom `UltraHDREncoder`).
- Lens correction uses Sony's embedded profile (radial splines, reverse-engineered scaling
  validated against real files). Verified on RX100M7; other bodies should work via the
  count-prefix-driven parser but are untested. Vignetting is parsed and available behind
  `--vignetting`, but its knot scale is not yet validated, so it is off by default.
- EXIF orientation (portrait shots) is preserved end-to-end; the pipeline works in stored-pixel
  space.

## CI

`ci/github-actions-ci.yml` runs gofmt/vet/`test -race`/build with LibRaw installed. Move it to
`.github/workflows/ci.yml` to activate (the token used to push this branch lacked the `workflow`
scope).
