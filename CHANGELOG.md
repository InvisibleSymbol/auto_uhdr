# Changelog

All notable changes to this project are documented here. The format is loosely
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased] — refactor branch

A ground-up refactor for maintainability and idiomatic, modern (Go 1.24) style,
plus a new default rendering and two correctness fixes to the highlight path.

### Rendering
- `--boost-curve` (raw mode, default 0): reshapes the RAW-derived recovery from
  linear toward logarithmic. The recovered gain is remapped by `log(1+b·t)/log(1+b)`
  over its `0..--max-boost` range, a concave curve that lifts partially-clipped
  mid-highlights up toward the recovery ceiling while the top compresses. Endpoints
  are fixed (no gain stays no gain; a fully-clipped pixel still reaches the ceiling),
  so it stays a faithful RAW recovery — just weighted toward the mid-highlights.
- New default `--hdr-mode raw`: a JPEG-gated, RAW-luminance-driven boost. The
  boost magnitude comes from how much brighter the RAW is than the JPEG (per
  channel), so bright surfaces lift by their real scene luminance and clipped
  regions reconstruct genuine RAW detail, while the JPEG luma gate masks shadows
  (no dark→bright glow, no RAW shadow noise). More faithful than the previous
  synthetic display boost, which remains as `--hdr-mode highlight` (still
  golden-tested). Defaults retuned accordingly (`--strength 1.0`, `--threshold
  0.5`, and `--gainmap-scale 1` — full-res, since raw mode carries real recovered
  detail in the map; raise it for smaller files).
- Gain map: per-channel colour is neutralized inside clipped highlights, so a
  blown white sky no longer picks up a colour cast in RGB gain maps while
  coloured, unclipped highlights keep their saturation. Opt-in via `--neutralize`
  (off by default; superseded for most cases by `--chroma`).
- `--chroma` (0..1, default 0.3): dials the RGB gain-map saturation in raw mode.
  Each channel's gain is `lerp(luminanceGain, perChannelGain, chroma)` — 0 is a
  neutral boost that keeps the JPEG's exact colour (and makes the RGB map ≡ the
  single-channel one), 1 is full per-channel recovery (rebuilds clipped colour
  but pulls mid-highlights toward the flatter RAW colour). ~0.3 adds real colour
  without a jarring mid-highlight transition.
- Default gain map is now `rgb` (was `single`), so the default `--chroma 0.3`
  actually takes effect; `--gainmap single` for a smaller luminance-only map.
- `--chroma-track`: ramps `--chroma` with JPEG brightness so per-channel colour
  recovery concentrates in the clipped highlights (where the JPEG lost colour)
  and stays neutral in the midtones (where the JPEG's colour is good) — `--chroma`
  becomes the peak reached at clipping. Fixes mid-highlight graying while still
  recovering full colour in blown areas.

### Fixed
- Portrait frames (EXIF 6/8) were misaligned: LibRaw auto-rotated the RAW to the
  shot's orientation while the JPEG stays sensor-native, so highlight recovery
  pasted rotated RAW content into the wrong place (e.g. a staircase in the sky).
  The RAW now decodes sensor-native (`user_flip=0`).

### Added
- Public library API at the module root: `arw2uhdr.Convert(ctx, in, opts)` with
  `Options`, `Result`, and typed stage errors (`errors.Is`-friendly). The CLI is
  now a thin client of this package.
- `context.Context` support: conversions can be cancelled at stage boundaries.
- Structured logging via `log/slog` (replaces the ad-hoc verbose printf).
- Redesigned subcommand CLI: `convert`, `verify`, `inspect`, `batch`, `version`.
  `inspect` folds in the former `arwmeta` debug tool; `batch` is a native
  worker-pool replacement for the shell script (the script remains for
  POSIX-only environments).
- Optional vignetting correction (parsed from the ARW profile; default off
  pending on-device validation of the scale factor).
- Substantially expanded test suite (imaging, color, spline/warp, gainmap
  multichannel round-trip, Ultra HDR container offsets, stage-error mapping)
  plus benchmarks and a sample-gated end-to-end golden test.
- Project hygiene: CI workflow, `.golangci.yml`, `.editorconfig`, this changelog.

### Changed
- Debug/dev commands moved from `cmd/` to `tools/`.
- Consolidated three separate box-blur implementations into one shared,
  parallelized routine in `internal/imaging`.
- Parallelized `gainmap.Compute`/`Reconstruct` and `hdrbuild.anchorGains`.
- Modernized throughout: `min`/`max` builtins, `for range N` loops,
  `slices`/`cmp`, generic clamp helpers; removed legacy workarounds.

### Fixed
- `gainmap`: local variable shadowing the `cap` builtin.
- Hardened the JPEG marker walker in the Ultra HDR writer against malformed
  segments.
