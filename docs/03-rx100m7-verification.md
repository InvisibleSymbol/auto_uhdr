# RX100M7 Verification — Findings Against Real Sample ARWs

*Phase 1b. Seven real `.ARW` files from the target camera (Sony DSC-RX100M7A) were analyzed to
verify — and in two places correct — the assumptions in `01-research-findings.md` before writing
code. Every claim here is measured from the actual files, not from documentation.*

Samples (all 20 MP, 3:2, `ColorSpace=sRGB`, full raw `5504×3672`, output `5472×3648`):

| File | Focal (real / 35mm-eq) | Zoom | Scene | Notable HDR content |
|---|---|---|---|---|
| DSC00468 | 72 mm / 200 mm | 100% | pink blossom, overcast | blown white sky |
| DSC01062 | 9 mm / 24 mm | 0% | Osaka street food, night | neon highlights |
| DSC01063 | 9 mm / 24 mm | 0% | Kobe alley (portrait) | bright sky |
| DSC01220 | 9 mm / 24 mm | 0% | night street | neon / lanterns |
| DSC01617 | 9 mm / 24 mm | 0% | Osaka街 night (used for warp test) | neon, deep shadows |
| DSC01885 | 35.3 mm / 98 mm | 63% | Skytree at night (portrait) | blown tower top |
| DSC02028 | 55.7 mm / 155 mm | 81% | Kitano shrine, harsh sun | clipped sun/sky |

Good spread of focal lengths (both zoom extremes) and genuine highlight-clipping scenes — an ideal
test set.

---

## 1. Confirmed assumptions ✅

- **Camera decodes cleanly in LibRaw 0.21.2** as `Sony DSC-RX100M7A` (ID `0x194`).
- **Lens-correction params live in the *un-enciphered* TIFF SubIFD** — present under both `[SubIFD]`
  and `[SR2SubIFD]`, readable by exiftool/exiv2 with no deciphering. This confirms the design's
  primary path: **a pure-Go TIFF/EXIF walk can read them directly; no ExifTool dependency and no
  maker-note cipher port needed.**
- **Distortion is applied to the JPEG but NOT to the raw** — verified visually (see §4): the
  uncorrected raw decode has a wider FOV and visible barrel bulge; the camera preview is pulled
  straight and cropped. This is the entire justification for the warp stage, now proven on the target
  camera.
- **Params vary correctly with focal length**: at 9 mm (wide) distortion is strong barrel
  (`+1136 … −2134`); at 35–72 mm it's mild (`−683 … 0`). All four 9 mm frames share identical
  distortion params, confirming the correction is a pure function of zoom position.
- **JPEG colour space is sRGB** → pipeline standardises on sRGB / BT.709 primaries, as designed.
- **As-shot WB is available** as `WB_RGGBLevels` (e.g. `2320 1024 1024 1700` = R,G1,G2,B) for
  matching the RAW develop to the JPEG.

## 2. Corrections to the research (body-specific) ⚠️

The generic full-frame/APS-C "16 or 11 knots, CA = 16+16=32" from `01-research-findings.md` is **not**
what this body emits. Measured on all 7 files:

| Correction | Count prefix | Layout (this body) | Example (DSC01617, 9 mm) |
|---|---|---|---|
| `DistortionCorrParams` | **11** | 11 radial knots + 5 pad zeros | `1136 1066 901 641 311 −69 −485 −922 −1364 −1771 −2134` |
| `ChromaticAberrationCorrParams` | **22** | **11 red knots + 11 blue knots** + 10 pad | red `0 −1152 … −1920`, blue `0 −1408 … −1408` |
| `VignettingCorrParams` | **16** | 16 radial knots | `0 27 15 51 … 6897` |

So the parser must be **count-prefix driven**, not fixed-width: read the leading count, then split
CA as `n_red = n_blue = count/2`. Distortion and CA use **11** knots here while vignetting uses
**16** — do not hard-code a single knot count. (The design already anticipated reading
`DistortionCorrParamsNumber`; this makes it mandatory and extends it to CA.)

## 3. Embedded preview is low-res (1616×1080) ⚠️

Every file's embedded `PreviewImage` is **1616×1080** — the reduced-resolution SOOC JPEG, ~1/3 linear
of full res, confirming the research caveat. Implications:

- For a **full-resolution** Ultra HDR the user must shoot **RAW+JPEG** and supply the full-res
  camera JPEG as the SDR base. (This matches the tool's stated input contract.)
- The embedded 1616×1080 preview is nonetheless the camera's *fully corrected* rendering, so it is a
  perfect **alignment reference** and lets us build and validate the entire pipeline now, at preview
  resolution, without the user uploading JPEGs. Full-res is a drop-in once a real JPEG is provided.

## 4. Distortion warp math — VALIDATED on real pixels ✅

The single highest-risk piece (the reverse-engineered warp) was prototyped in Python and scored
against the camera's own corrected preview (DSC01617, 9 mm, strongest distortion).

Model tested: knots equi-spaced centre→edge, `g_d(r) = 1 + v(r)/2¹⁴` (cubic-spline interpolated),
inverse map `source = C + (X−C)·g_d(r)/s`, radius normalised so the last knot sits at radius `rmax`.
Swept the normalisation reference and the autoscale, scored by edge-map normalised
cross-correlation (NCC) after phase-correlation shift removal:

```
norm     scale    edgeNCC
diag     none     0.7217   ← winner (half-diagonal normalisation)
width    none     0.577
(none/uncorrected)0.372    ← baseline
height   none     0.348
```

- **Normalisation = half-diagonal** (`rmax = √((W/2)²+(H/2)²)`) is clearly correct; height/width
  conventions score far worse.
- Applying the correction **lifts edge alignment 0.37 → 0.72 (+94%)** with **zero residual shift** —
  the warp direction, the `2⁻¹⁴` scale, and the knot layout are right.
- A global crop-scale search peaks at **≈0.99**, i.e. this body applies the correction with
  **almost no additional crop** (not the aggressive autoscale seen on some bodies). The exact crop
  convention will be finalised against a real full-res JPEG; a phase-correlation/scale safety-net
  (already in the design) covers residual per-body quirks.
- The 0.72 ceiling (not ~0.9) is dominated by **rendering differences** (camera JPEG sharpening/tone
  vs. dcraw AHD demosaic) in the edge maps, not by geometric misalignment — the edge overlay shows
  the corners collapsing from red/green fringing to yellow.

Artifacts written: `work/DSC01617_geo_compare.png` (FOV/geometry), `work/DSC01617_warp_result.png`
(before/after edge overlay). Prototype: `proto/warp_validate.py`.

## 5. Net effect on the plan

- **Metadata reader:** pure-Go SubIFD walk, **count-prefix driven**, splitting CA into red/blue by
  half the count. No cipher, no exiftool on the happy path. (`--exiftool-fallback` retained for
  safety.)
- **Warp:** implement exactly the validated model (half-diagonal normalisation, `g_d=1+v/2¹⁴`,
  inverse map, cubic-spline knots, near-unity crop) with the scale/shift refinement as the safety
  net. Per-channel CA uses the 11 red / 11 blue knots at `2⁻²¹` on top of `g_d`.
- **SDR base:** default to the supplied full-res JPEG; fall back to the 1616×1080 embedded preview
  when no JPEG is given (produces a valid but reduced-res Ultra HDR — useful for RAW-only shots and
  for our own testing).
- **Open items now closed:** un-enciphered availability ✅, knot counts ✅ (11/22/16), distortion-in-
  JPEG-not-raw ✅, warp normalisation ✅, colour space ✅ (sRGB), preview resolution ✅ (1616×1080).
- **Still to finalise with a real full-res JPEG:** exact crop factor and whether lateral CA is
  partly baked into this body's raw (apply full vs. residual). Neither blocks implementation.
