// Package register estimates and applies a residual affine (anisotropic scale + translation)
// that aligns a moving image (lens-corrected RAW) to a reference (the camera JPEG). This absorbs
// the small scale/framing mismatch between LibRaw's decode grid and the JPEG that otherwise shows
// up as a gain-map offset growing toward the edges.
package register

import (
	"math"
	"sort"

	"github.com/invis/arw2uhdr/internal/imaging"
)

// Affine maps an output (reference) coordinate to a sample coordinate in the moving image:
//
//	sample_x = Sx*x + OxFrac*W ;  sample_y = Sy*y + OyFrac*H
//
// Sx,Sy are dimensionless; OxFrac,OyFrac are fractions of width/height (resolution-independent).
type Affine struct {
	Sx, Sy         float64
	OxFrac, OyFrac float64
}

// Identity returns a no-op transform.
func Identity() Affine { return Affine{Sx: 1, Sy: 1} }

// Apply resamples mov into a W×H reference-frame image using the affine.
func (a Affine) Apply(mov *imaging.Image, W, H int) *imaging.Image {
	out := imaging.New(W, H)
	oxf := a.OxFrac * float64(W)
	oyf := a.OyFrac * float64(H)
	imaging.ParallelRows(H, func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			sy := a.Sy*float64(y) + oyf
			for x := 0; x < W; x++ {
				sx := a.Sx*float64(x) + oxf
				r, g, b := mov.SampleBilinear(sx, sy)
				out.Set(x, y, r, g, b)
			}
		}
	})
	return out
}

// Options tunes the estimator.
type Options struct {
	WorkWidth    int // downsample width for estimation (default 1400)
	Patch        int // patch size (default 40)
	SearchRad    int // search radius in working px (default 8)
	GridX, GridY int // grid of patches (default 20x13)
}

func def(o Options) Options {
	if o.WorkWidth == 0 {
		o.WorkWidth = 1400
	}
	if o.Patch == 0 {
		o.Patch = 40
	}
	if o.SearchRad == 0 {
		o.SearchRad = 8
	}
	if o.GridX == 0 {
		o.GridX = 20
	}
	if o.GridY == 0 {
		o.GridY = 13
	}
	return o
}

// gradMag returns the gradient-magnitude of luma (tone-robust for matching linear vs gamma images).
func gradMag(im *imaging.Image) []float64 {
	W, H := im.W, im.H
	lu := make([]float64, W*H)
	for i := 0; i < W*H; i++ {
		lu[i] = 0.2126*float64(im.Pix[i*3]) + 0.7152*float64(im.Pix[i*3+1]) + 0.0722*float64(im.Pix[i*3+2])
	}
	g := make([]float64, W*H)
	for y := 1; y < H-1; y++ {
		for x := 1; x < W-1; x++ {
			gx := lu[y*W+x+1] - lu[y*W+x-1]
			gy := lu[(y+1)*W+x] - lu[(y-1)*W+x]
			g[y*W+x] = math.Hypot(gx, gy)
		}
	}
	return g
}

type sample struct{ x, y, dx, dy float64 }

// ncc computes zero-mean normalized cross-correlation of two equal-size patches.
func ncc(a, b []float64) float64 {
	var ma, mb float64
	n := float64(len(a))
	for i := range a {
		ma += a[i]
		mb += b[i]
	}
	ma /= n
	mb /= n
	var num, da, db float64
	for i := range a {
		xa := a[i] - ma
		xb := b[i] - mb
		num += xa * xb
		da += xa * xa
		db += xb * xb
	}
	if da < 1e-9 || db < 1e-9 {
		return 0
	}
	return num / math.Sqrt(da*db)
}

// Estimate computes the affine aligning mov to ref (both must be the same dimensions).
func Estimate(ref, mov *imaging.Image, opts Options) Affine {
	o := def(opts)
	// downsample to working width
	ww := o.WorkWidth
	if ww > ref.W {
		ww = ref.W
	}
	wh := ref.H * ww / ref.W
	r := ref.Resize(ww, wh)
	m := mov.Resize(ww, wh)
	gr := gradMag(r)
	gm := gradMag(m)
	P := o.Patch
	R := o.SearchRad
	half := P / 2

	patchAt := func(g []float64, cx, cy int) []float64 {
		p := make([]float64, P*P)
		k := 0
		for dy := -half; dy < half; dy++ {
			yy := cy + dy
			for dx := -half; dx < half; dx++ {
				xx := cx + dx
				if xx >= 0 && xx < ww && yy >= 0 && yy < wh {
					p[k] = g[yy*ww+xx]
				}
				k++
			}
		}
		return p
	}

	var samples []sample
	margin := half + R + 1
	for gy := 0; gy < o.GridY; gy++ {
		cy := margin + (wh-2*margin)*gy/(o.GridY-1)
		for gx := 0; gx < o.GridX; gx++ {
			cx := margin + (ww-2*margin)*gx/(o.GridX-1)
			refP := patchAt(gr, cx, cy)
			// texture gate
			var s float64
			for _, v := range refP {
				s += v
			}
			if s/float64(len(refP)) < 0.01 {
				continue
			}
			// integer search for best NCC
			best := -2.0
			var bsx, bsy int
			nccAt := map[[2]int]float64{}
			for sy := -R; sy <= R; sy++ {
				for sx := -R; sx <= R; sx++ {
					movP := patchAt(gm, cx+sx, cy+sy)
					c := ncc(refP, movP)
					nccAt[[2]int{sx, sy}] = c
					if c > best {
						best = c
						bsx, bsy = sx, sy
					}
				}
			}
			if best < 0.3 {
				continue
			}
			// parabolic subpixel refine (guard edges of search window)
			fx := parabola(nccAt[[2]int{bsx - 1, bsy}], best, nccAt[[2]int{bsx + 1, bsy}])
			fy := parabola(nccAt[[2]int{bsx, bsy - 1}], best, nccAt[[2]int{bsx, bsy + 1}])
			// content matching ref(cx,cy) is at (cx+sx, cy+sy) in mov => sample offset
			samples = append(samples, sample{
				x: float64(cx), y: float64(cy),
				dx: float64(bsx) + fx, dy: float64(bsy) + fy,
			})
		}
	}
	if len(samples) < 8 {
		return Identity()
	}
	// robust fit: sample_x = Sx*x + Ox  where sample_x = x + dx  => (x+dx) = Sx*x+Ox
	sx, ox := robustLine(samples, func(s sample) (float64, float64) { return s.x, s.x + s.dx })
	sy, oy := robustLine(samples, func(s sample) (float64, float64) { return s.y, s.y + s.dy })
	return Affine{Sx: sx, Sy: sy, OxFrac: ox / float64(ww), OyFrac: oy / float64(wh)}
}

func parabola(vm, v0, vp float64) float64 {
	den := vm - 2*v0 + vp
	if math.Abs(den) < 1e-9 {
		return 0
	}
	f := 0.5 * (vm - vp) / den
	if f > 1 || f < -1 {
		return 0
	}
	return f
}

// robustLine fits y = a*x + b with one round of MAD-based outlier rejection.
func robustLine(s []sample, sel func(sample) (float64, float64)) (a, b float64) {
	fit := func(idx []int) (float64, float64) {
		var sx, sy, sxx, sxy float64
		n := float64(len(idx))
		for _, i := range idx {
			x, y := sel(s[i])
			sx += x
			sy += y
			sxx += x * x
			sxy += x * y
		}
		den := n*sxx - sx*sx
		if math.Abs(den) < 1e-9 {
			return 1, 0
		}
		a := (n*sxy - sx*sy) / den
		b := (sy - a*sx) / n
		return a, b
	}
	all := make([]int, len(s))
	for i := range s {
		all[i] = i
	}
	a, b = fit(all)
	// residuals
	res := make([]float64, len(s))
	for i := range s {
		x, y := sel(s[i])
		res[i] = math.Abs(y - (a*x + b))
	}
	med := median(res)
	keep := all[:0:0]
	for i := range s {
		if res[i] <= 3*med+1e-6 {
			keep = append(keep, i)
		}
	}
	if len(keep) >= 8 {
		a, b = fit(keep)
	}
	return a, b
}

func median(v []float64) float64 {
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	if len(c) == 0 {
		return 0
	}
	return c[len(c)/2]
}
