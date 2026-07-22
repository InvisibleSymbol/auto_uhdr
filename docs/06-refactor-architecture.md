# Refactor architecture — pluggable pipeline stages

This note records the architecture introduced by the refactor. The behaviour (the
validated HDR look) is unchanged and pinned by `hdrbuild.TestGoldenCurveV10`; this
is purely about structure and extensibility.

## The shape

The conversion is a four-stage pipeline. Each stage is a small interface with a
default implementation, composed by a `Converter`:

```
RawDecoder        LensCorrector          HDRRenderer         UltraHDREncoder
  (LibRaw)   →   (Sony embedded    →   (highlight        →   (pure-Go gain map
                  profile + warp)       recovery + boost)      + JPEG_R container)
```

- **RawDecoder** — `Decode(ctx, arwPath) (*Image, *RawMeta, error)`
- **LensCorrector** — `Correct(ctx, img, arwPath) (*Image, error)`
- **HDRRenderer** — `Render(ctx, sdrLinear, rawLinear) (*Image, error)`
- **UltraHDREncoder** — `Encode(ctx, baseJPEG, sdrLinear, hdrLinear) (EncodeResult, error)`

The `Converter` owns the glue that is not itself a swap point: loading and
linearizing the SDR base, resizing the RAW to the JPEG grid, and the residual
registration step (toggled by `Options.Register`).

## Why this shape

The goal was extensibility along the axes that actually vary:

- **Custom raw handling** for particular images or bodies → a new `RawDecoder`.
- **An alternative Ultra HDR generator** (a native encoder, libultrahdr via cgo, or
  an ISO 21496-1 dual-write) → a new `UltraHDREncoder`. Making the encoder own both
  gain-map derivation and container assembly matches how such backends are actually
  packaged (libultrahdr does both), so swapping one in doesn't force our gain-map
  math on it.
- **A different tone-mapping** → a new `HDRRenderer`.

This is the Strategy pattern, not a repository/data-access layer — there is no
persistence to abstract; the pipeline is a stateless transform.

## Boundaries and defaults

- Interfaces and the `Converter` live in the root package `arw2uhdr` (public API).
  Stage implementations live in `internal/*`; the root package adapts them.
- `New(Options)` wires the defaults from a flat, primitive/enum `Options` surface
  (no internal option structs leak into the public API). The exported constructors
  (`NewLibRawDecoder`, `NewEmbeddedLensCorrector`, `NewHighlightRenderer`,
  `NewGoEncoder`) let a custom stage wrap and delegate to a default.
- `context.Context` is checked at every stage boundary. The LibRaw decode is a
  blocking C call and cannot be interrupted mid-flight, so cancellation is a
  between-stages guarantee, not a mid-stage one.
- Errors are wrapped in `StageError{Stage, Err}`; the CLI maps `Stage` to a stable
  exit code via `errors.As`.

## Adding a stage — worked example

```go
type doubleEncoder struct{ inner arw2uhdr.UltraHDREncoder } // e.g. also emit ISO 21496-1

func (d doubleEncoder) Encode(ctx context.Context, base []byte, sdr, hdr *arw2uhdr.Image) (arw2uhdr.EncodeResult, error) {
    r, err := d.inner.Encode(ctx, base, sdr, hdr) // delegate to the default
    if err != nil {
        return r, err
    }
    // ... augment r.Data ...
    return r, nil
}

conv := arw2uhdr.New(opts)
conv.Encoder = doubleEncoder{inner: conv.Encoder}
```

Testing follows the same seam: `arw2uhdr_test.go` drives the `Converter` with fake
decoder/corrector stages, so the orchestration is covered without LibRaw or a real
ARW.
