package sonylens

// cubicSpline is a natural cubic spline over strictly increasing xs.
// Used to interpolate the radial correction curves between the discrete knots.
type cubicSpline struct {
	xs, ys, y2 []float64
}

func newCubicSpline(xs, ys []float64) *cubicSpline {
	n := len(xs)
	y2 := make([]float64, n)
	if n < 3 {
		return &cubicSpline{xs: xs, ys: ys, y2: y2} // linear fallback
	}
	u := make([]float64, n)
	// natural boundary conditions: y2[0]=y2[n-1]=0
	for i := 1; i < n-1; i++ {
		sig := (xs[i] - xs[i-1]) / (xs[i+1] - xs[i-1])
		p := sig*y2[i-1] + 2.0
		y2[i] = (sig - 1.0) / p
		u[i] = (ys[i+1]-ys[i])/(xs[i+1]-xs[i]) - (ys[i]-ys[i-1])/(xs[i]-xs[i-1])
		u[i] = (6.0*u[i]/(xs[i+1]-xs[i-1]) - sig*u[i-1]) / p
	}
	for k := n - 2; k >= 0; k-- {
		y2[k] = y2[k]*y2[k+1] + u[k]
	}
	return &cubicSpline{xs: xs, ys: ys, y2: y2}
}

// eval returns the interpolated value at x (clamped to the knot range).
func (s *cubicSpline) eval(x float64) float64 {
	n := len(s.xs)
	if n == 0 {
		return 0
	}
	if x <= s.xs[0] {
		return s.ys[0]
	}
	if x >= s.xs[n-1] {
		return s.ys[n-1]
	}
	// binary search for the interval
	lo, hi := 0, n-1
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if s.xs[mid] > x {
			hi = mid
		} else {
			lo = mid
		}
	}
	h := s.xs[hi] - s.xs[lo]
	if h == 0 {
		return s.ys[lo]
	}
	a := (s.xs[hi] - x) / h
	b := (x - s.xs[lo]) / h
	return a*s.ys[lo] + b*s.ys[hi] +
		((a*a*a-a)*s.y2[lo]+(b*b*b-b)*s.y2[hi])*(h*h)/6.0
}
