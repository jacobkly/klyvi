package embed

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   []float32
		want []float32
	}{
		{"scales to unit length", []float32{3, 4}, []float32{0.6, 0.8}},
		{"keeps a zero vector", []float32{0, 0}, []float32{0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Normalize(tt.in)
			for i := range tt.want {
				if math.Abs(float64(got[i]-tt.want[i])) > 1e-6 {
					t.Fatalf("Normalize(%v) = %v, want %v", tt.in, got, tt.want)
				}
			}
		})
	}
}

// TestModel runs the real model when KLYVI_EMBED_MODEL names its directory.
func TestModel(t *testing.T) {
	dir := os.Getenv("KLYVI_EMBED_MODEL")
	if dir == "" || testing.Short() {
		t.Skip("set KLYVI_EMBED_MODEL to the model directory to run")
	}
	ctx := context.Background()
	m, err := Load(ctx, dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })

	embed := func(s string) []float32 {
		t.Helper()
		v, err := m.Embed(ctx, s)
		if err != nil {
			t.Fatalf("Embed: %v", err)
		}
		if len(v) != Dims {
			t.Fatalf("got %d dims, want %d", len(v), Dims)
		}
		return v
	}
	dot := func(a, b []float32) float64 {
		var s float64
		for i := range a {
			s += float64(a[i]) * float64(b[i])
		}
		return s
	}
	heist := embed("A group of thieves plan an elaborate bank heist, but a detective closes in on them.")
	robbers := embed("A crew of robbers prepares to rob a vault while the police investigate.")
	paris := embed("Two young lovers fall for each other during a summer in Paris.")
	if near, far := dot(heist, robbers), dot(heist, paris); near <= far {
		t.Errorf("heist~robbers %.3f should beat heist~paris %.3f", near, far)
	}
	if n := dot(heist, heist); math.Abs(n-1) > 1e-4 {
		t.Errorf("vector length² %.5f, want 1", n)
	}
}
