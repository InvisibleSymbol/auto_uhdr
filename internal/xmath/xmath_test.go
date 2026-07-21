package xmath

import (
	"math"
	"testing"
)

func TestClamp(t *testing.T) {
	if got := Clamp(5, 0, 10); got != 5 {
		t.Errorf("Clamp(5,0,10)=%d", got)
	}
	if got := Clamp(-1, 0, 10); got != 0 {
		t.Errorf("Clamp(-1,0,10)=%d", got)
	}
	if got := Clamp(99, 0, 10); got != 10 {
		t.Errorf("Clamp(99,0,10)=%d", got)
	}
	if got := Clamp01(1.5); got != 1 {
		t.Errorf("Clamp01(1.5)=%v", got)
	}
	if got := Clamp01(float32(-0.2)); got != 0 {
		t.Errorf("Clamp01(-0.2)=%v", got)
	}
}

func TestLerp(t *testing.T) {
	if got := Lerp(0.0, 10.0, 0.25); got != 2.5 {
		t.Errorf("Lerp=%v want 2.5", got)
	}
}

func TestSmoothstep(t *testing.T) {
	if got := Smoothstep(0, 1, -1); got != 0 {
		t.Errorf("below range=%v", got)
	}
	if got := Smoothstep(0, 1, 2); got != 1 {
		t.Errorf("above range=%v", got)
	}
	if got := Smoothstep(0, 1, 0.5); math.Abs(got-0.5) > 1e-12 {
		t.Errorf("midpoint=%v want 0.5", got)
	}
	// monotonic non-decreasing
	prev := -1.0
	for i := range 101 {
		x := float64(i) / 100
		v := Smoothstep(0.2, 0.8, x)
		if v < prev-1e-12 {
			t.Fatalf("not monotonic at x=%v (%v < %v)", x, v, prev)
		}
		prev = v
	}
	// degenerate span behaves as a step at a
	if got := Smoothstep(0.5, 0.5, 0.4); got != 0 {
		t.Errorf("degenerate below=%v", got)
	}
	if got := Smoothstep(0.5, 0.5, 0.6); got != 1 {
		t.Errorf("degenerate above=%v", got)
	}
}
