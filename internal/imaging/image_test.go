package imaging

import (
	"math"
	"testing"
)

func TestSampleBilinearExactAndInterp(t *testing.T) {
	im := New(2, 1)
	im.Set(0, 0, 0, 0, 0)
	im.Set(1, 0, 1, 1, 1)
	if r, _, _ := im.SampleBilinear(0, 0); r != 0 {
		t.Errorf("left sample=%v want 0", r)
	}
	if r, _, _ := im.SampleBilinear(1, 0); r != 1 {
		t.Errorf("right sample=%v want 1", r)
	}
	if r, _, _ := im.SampleBilinear(0.5, 0); math.Abs(float64(r)-0.5) > 1e-6 {
		t.Errorf("mid sample=%v want 0.5", r)
	}
}

func TestSampleBilinearClampsBorder(t *testing.T) {
	im := New(4, 4)
	im.Set(0, 0, 0.7, 0.7, 0.7)
	if r, _, _ := im.SampleBilinear(-5, -5); r != 0.7 {
		t.Errorf("out-of-range clamp=%v want 0.7", r)
	}
}

func TestResizeIdentity(t *testing.T) {
	im := New(8, 6)
	for i := range im.Pix {
		im.Pix[i] = float32(i%7) / 7
	}
	out := im.Resize(8, 6)
	for i := range im.Pix {
		if math.Abs(float64(out.Pix[i]-im.Pix[i])) > 1e-6 {
			t.Fatalf("identity resize changed pixel %d: %v vs %v", i, out.Pix[i], im.Pix[i])
		}
	}
}

func TestClone(t *testing.T) {
	im := New(3, 3)
	im.Pix[4] = 0.5
	c := im.Clone()
	c.Pix[4] = 0.9
	if im.Pix[4] != 0.5 {
		t.Error("Clone did not deep-copy")
	}
}

func TestBoxBlurUniformIsInvariant(t *testing.T) {
	w, h := 20, 15
	src := make([]float64, w*h)
	for i := range src {
		src[i] = 0.42
	}
	out := BoxBlurF(src, w, h, 3, 2)
	for i, v := range out {
		if math.Abs(v-0.42) > 1e-9 {
			t.Fatalf("uniform blur drifted at %d: %v", i, v)
		}
	}
}

func TestBoxBlurConservesMeanAndSpreads(t *testing.T) {
	w, h := 21, 21
	src := make([]float64, w*h)
	src[10*w+10] = 1 // central impulse
	out := BoxBlurF(src, w, h, 2, 1)
	// center should have spread out (strictly less than 1)
	if out[10*w+10] >= 1 {
		t.Errorf("impulse did not spread: center=%v", out[10*w+10])
	}
	// a single separable box blur conserves total mass in the interior
	var sum float64
	for _, v := range out {
		sum += v
	}
	if math.Abs(sum-1) > 1e-6 {
		t.Errorf("blur did not conserve mass: sum=%v want 1", sum)
	}
}

func TestBoxBlurZeroRadiusCopies(t *testing.T) {
	src := []float64{1, 2, 3, 4}
	out := BoxBlurF(src, 2, 2, 0, 1)
	out[0] = 99
	if src[0] != 1 {
		t.Error("zero-radius blur must return an independent copy")
	}
}

func BenchmarkBoxBlur(b *testing.B) {
	w, h := 512, 512
	src := make([]float64, w*h)
	for i := range src {
		src[i] = float64(i%255) / 255
	}
	b.ResetTimer()
	for range b.N {
		_ = BoxBlurF(src, w, h, 4, 3)
	}
}
