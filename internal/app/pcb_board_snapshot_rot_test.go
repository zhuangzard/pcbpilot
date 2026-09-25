package app

import "testing"

func TestSnapRotation(t *testing.T) {
	for in, want := range map[float64]float64{-90.00000000000001: 270, 270: 270, 0: 0, -0.0000000001: 0, 359.9999999999: 0, 450: 90, 45.5: 45.5, -180: 180} {
		if got := snapRotation(in); got != want {
			t.Errorf("snapRotation(%v) = %v, want %v", in, got, want)
		}
	}
}
