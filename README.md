# arw2uhdr

Convert Sony **ARW + JPEG** pairs into **Ultra HDR** (JPEG_R) images — the camera JPEG stays
byte-for-byte as the SDR base, the RAW supplies lens-corrected highlight recovery, and a gain map
makes highlights pop on HDR displays. Fully backward-compatible: on SDR screens the file looks
exactly like the original JPEG.

Built for and validated against the Sony RX100M7; the architecture (LibRaw decode + count-driven
Sony lens metadata parsing) extends to other Sony bodies.

## How it works

1. **RAW decode** — LibRaw (cgo), scene-linear 16-bit, camera white balance, highlight blending.
2. **Lens correction** — parses Sony's embedded distortion/CA correction (`DistortionCorrParams`
   etc.) from the ARW's plaintext SubIFD in pure Go (no exiftool, no maker-note decryption) and
   replicates the camera's radial warp so the RAW aligns pixel-for-pixel with the JPEG, which the
   camera corrected in-body but the RAW is not.
3. **Registration** — estimates and removes the residual anisotropic scale/shift between LibRaw's
   decode grid and the JPEG (block matching + robust fit). Corner error drops ~15 px → <1 px.
4. **HDR rendition** — the JPEG is the reference everywhere it has valid data. RAW content is
   spliced in only where the JPEG is clipped (luma 0.90→0.97). A **monotonic** display boost
   (threshold → threshold+ramp-width, plateauing at full strength) adds pop; a soft log-domain
   shoulder compresses near the ceiling instead of clipping.
5. **Gain map + container** — single- or per-channel (RGB) gain map, encoded per the Adobe/Google
   `hdrgm` math, assembled into a JPEG_R container (MPF index + GContainer XMP) in pure Go.

## Build

Requires Go ≥ 1.22 and LibRaw headers (`apt install libraw-dev` / `brew install libraw`).

```
make            # builds bin/arw2uhdr
make test       # unit tests (no samples needed; sample-based tests skip if absent)
```

## Usage

```
arw2uhdr photo.ARW                    # pairs with photo.JPG automatically
arw2uhdr photo.ARW photo.JPG -o out_uhdr.jpg
arw2uhdr --gainmap rgb photo.ARW      # per-channel gain map (colored lights stay saturated)
arw2uhdr --strength 2 --threshold 0.2 photo.ARW   # more pop, reaching deeper into midtones
arw2uhdr --strength 0 photo.ARW       # pure highlight recovery, no display boost
arw2uhdr --verify --json photo.ARW    # machine-readable result + structural self-check
```

Key dials:

| flag | default | meaning |
|---|---|---|
| `--strength` | 1.5 | display boost in stops at the plateau (0 = recovery only) |
| `--threshold` | 0.3 | SDR luma where the boost ramp begins (lower = more of the image) |
| `--ramp-width` | 0.35 | luma span over which boost reaches full strength |
| `--max-boost` | 3.0 | total-boost ceiling in stops (soft shoulder) |
| `--gainmap` | single | `single` (luminance) or `rgb` (per-channel color) |
| `--gainmap-scale` | 4 | gain-map downsample factor (1 = full resolution) |

Exit codes: `0` ok · `2` usage · `3` input · `4` raw decode · `5` lens metadata · `7` encode · `8` write.

## Batch

```
scripts/batch.sh -j 2 -o outdir /path/to/photos
EXTRA="--gainmap rgb --strength 2" scripts/batch.sh -j 4 -s -o outdir dir1 dir2
```

Pairs by basename (`DSC01234.ARW` ↔ `DSC01234.JPG`), follows symlinks, skips-existing with `-s`,
logs failures without aborting the run. Each job needs ~1.5 GB RAM at 20 MP; scale `-j` accordingly.

~10 s per 20 MP image on a modern multi-core machine.

## Notes & limitations

- The paired JPEG must be the full-resolution camera JPEG (shoot RAW+JPEG). The ~1616×1080
  preview embedded in the ARW is too small to serve as the SDR base.
- Output is the original-JPEG variant of Ultra HDR v1 (`hdrgm` XMP + MPF), read by Android 14+,
  Chrome, Google Photos, and most gain-map-aware viewers. ISO 21496-1 dual-write is a possible
  future addition.
- Lens correction uses Sony's embedded profile (11/16-knot radial splines, reverse-engineered
  scaling validated against real files). Verified on RX100M7; other bodies should work via the
  count-prefix-driven parser but are untested.
- EXIF orientation (portrait shots) is preserved end-to-end; the pipeline works in stored-pixel
  space.
