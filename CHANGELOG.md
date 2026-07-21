# Changelog

All notable changes to this project are documented here. The format is loosely
based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/).

## [Unreleased] — refactor branch

A ground-up refactor for maintainability and idiomatic, modern (Go 1.24) style.
No change to the validated HDR look: the tuned rendition math is preserved and
now locked by a golden numeric test.

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
