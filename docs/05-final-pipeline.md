# Final pipeline notes — the HDR curve, and what we learned getting there

*Phase 3 wrap-up. Records the settled HDR-derivation design and the artifact→fix history, so the
reasoning isn't lost. All findings validated on-device (RX100M7 files, HDR phone display).*

## The settled design (v10)

Per-pixel, with `Y` = SDR (JPEG) luma, linearized:

1. **Content**: `content = sdr` everywhere, except a clip-gated splice
   `content = mix(sdr, max(sdr, k·raw), smoothstep(0.90, 0.97, Y))` — RAW detail replaces JPEG
   content **only where the JPEG is clipped** and has nothing to lose. (k = per-channel midtone
   anchor, median-based.)
2. **Boost**: `boost = 2^(Strength · smoothstep(T, min(T+W, 1), Y))` — **monotonic** in luma,
   ramping from threshold `T` over fixed width `W` (default 0.35) to a plateau.
3. **Ceiling**: `hdr = softShoulder(content · boost)` — log-domain tanh shoulder below
   `max-boost`, compressing instead of clipping.

Gain map = `log2((hdr+ε)/(sdr+ε))` per hdrgm math, single or per-channel.

## Artifact history (each found by the user on a real HDR display)

| symptom | root cause | fix |
|---|---|---|
| gain map offset growing toward corners | naive resize between LibRaw grid (5496×3672) and JPEG grid (5472×3648) applied anisotropic scale | registration stage: block-match + robust affine fit (15 px → <1 px) |
| punchy settings clip highlight detail | boost stacked on recovery and hit a hard `min()` ceiling | soft log-domain shoulder |
| gray outline around everything bright | the "taper" (boost fading to zero before the recovery zone) carved a gain valley into every bright-object gradient | **remove the taper**; keep boost monotonic — the shoulder alone handles the ceiling |
| cyan/stained mottling on bright signs | recovery gate at luma 0.45 blended partially-clipped RAW (hue-stained demosaic) into areas where the JPEG was valid | clip-gate the recovery (0.90→0.97): RAW only replaces clipped data |
| threshold dial looked inert | ramp spanned threshold→white, so lowering it stretched the ramp thinner instead of moving boost into midtones | fixed-width ramp (threshold → threshold+0.35) |

Dead ends worth remembering: gain-map blurring (kills real detail with the ringing), SDR-guided
filtering of recovery gain (helps mottling but softens legit detail — kept as expert knob,
default off), `highlight=0` decode (clean but discards partial-channel recovery), reliability
masks keyed on clip ceiling (blend-mode decode hides clipping below the ceiling).

Misdiagnosis to not repeat: the KOMIYAKI sign's outlined lettering was real content, not
sharpening halos — that wrong call led to the luma-0.45 recovery gate and its mottling.

## Performance

Parallelized (warp, resize, register-apply, linearize, hdr-build, gain-map loops) across cores:
**~10 s per 20 MP image** (from ~60–90 s single-threaded). Batch: 7 pairs in 62 s at `-j 2`
(memory-bound: ~1.5 GB/job peak).

## Verified end-to-end (all 7 sample pairs)

Structure (MPF 2-image + GContainer + hdrgm, exiftool-independent), `--verify` self-check,
EXIF orientation preserved on portrait shots (pipeline is stored-pixel-space; gain-map/SDR phase
shift (0,0) on orientation-6 and -8 files), on-device HDR rendering confirmed by the user
through v10.
