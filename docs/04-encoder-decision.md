# Encoder path decision — pure-Go gain map (with libultrahdr as a future swap-in)

## Context
The design (`02-design.md` §4/§5) chose libultrahdr via cgo for the Ultra HDR encode, with
"compute the gain map ourselves" as the documented alternative (§5.3). In this build environment:

- libultrahdr is **not** in apt (checked: no `libuhdr*/libultrahdr*` packages).
- The sandbox egress allowlist blocks `github.com`/`codeload.github.com` (release tarball → 403) and
  the gist host; only `raw.githubusercontent.com` is reachable (single files, HTTP 200). Building
  libultrahdr from source would mean fetching 50+ files plus its `third_party/image_io` dependency
  one-by-one — fragile and version-brittle.

## Decision
Implement the **gain-map computation and the Ultra HDR (JPEG_R) container writer in pure Go** now.

Rationale:
1. Fully implementable and testable in this environment — no blocked dependency.
2. Gives first-class control over **single-channel vs RGB (3-channel) gain maps** — the user's stated
   goal — rather than a single library toggle.
3. Produces a real, inspectable Ultra HDR file this run for validation and visualization.
4. Keeps the same package boundary (`internal/ultrahdr.Encode(sdrJPEG, hdrLinear, opts)`), so a
   libultrahdr **cgo backend can be swapped in later** unchanged if we obtain the library (e.g. the
   user adds `github.com` to the egress allowlist, or provides a prebuilt `libuhdr`).

## Interop target
Follow the **Android Ultra HDR v1.1 / Adobe `hdrgm` XMP** structure (the original, widely-read
variant: Android 14+, Chrome, Photos):

- Primary SDR JPEG carries: APP1 XMP with a GContainer directory (Primary + GainMap items, GainMap
  `Length` in bytes) and `hdrgm:Version="1.0"`; APP2 MPF index with the two images' offsets/sizes.
- The gain-map JPEG is appended after the primary's EOI, carrying its own APP1 XMP with the `hdrgm`
  metadata (GainMapMin/Max, Gamma, OffsetSDR/HDR, HDRCapacityMin/Max, BaseRenditionIsHDR).

ISO 21496-1 binary metadata is a later addition (dual-write for Android 15+); the XMP path is
implemented first and is sufficient for broad viewer recognition. Validation: verify structure
(markers, XMP, MPF offsets, appended gain map) and round-trip reconstruct the HDR to check the math.
