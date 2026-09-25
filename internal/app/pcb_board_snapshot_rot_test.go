package app

import "testing"

func TestSnapRotation(t *testing.T) {
	for in, want := range map[float64]float64{-90.00000000000001: 270, 270: 270, 0: 0, -0.0000000001: 0, 359.9999999999: 0, 450: 90, 45.5: 45.5, -180: 180} {
		if got := snapRotation(in); got != want {
			t.Errorf("snapRotation(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestPourOnLayer(t *testing.T) {
	if !pourOnLayer(map[string]any{"layer": 1.0}, 1) || pourOnLayer(map[string]any{"layer": 2.0}, 1) || pourOnLayer(map[string]any{}, 1) {
		t.Fatal("pour-fit --replace must match the layer, not only the net")
	}
}

// U2 (SOP-16) pin 5 on the ESP32 board: OVAL 68.7 × 22 mil; the RTS via
// (dia 24) at (1251.23, 544.92) is 7.3 mil from the rounded end, not the 3.4
// mil its bounding rectangle gives.
func TestPadSegDistOval(t *testing.T) {
	p := pcbPadP{X: 1291.9, Y: 520, W: 68.7, H: 22, Rotation: 270, Shape: "OVAL", ShapeOK: true}
	if d := padSegDist(p, 1251.23, 544.92, 1251.23, 544.92) - 12; d < 7 || d > 7.6 {
		t.Fatalf("oval edge gap %.2f, want ~7.3", d)
	}
	p.Shape = "RECT"
	if d := padSegDist(p, 1251.23, 544.92, 1251.23, 544.92) - 12; d > 3.6 {
		t.Fatalf("rect keeps the corner: %.2f", d)
	}
}

func TestMarkUnrenderedSilk(t *testing.T) {
	silk := []pcbSilkText{
		{Kind: "attribute", Key: "Designator", BBox: &pcbRect{0, 0, 10, 10}},
		{Kind: "attribute", Key: "Footprint"},
		{Kind: "string", Text: "logo"},
	}
	markUnrenderedSilk(silk)
	if silk[0].Hidden || !silk[1].Hidden || silk[2].Hidden {
		t.Fatalf("hidden flags %v %v %v", silk[0].Hidden, silk[1].Hidden, silk[2].Hidden)
	}
	old := []pcbSilkText{{Kind: "attribute", Key: "Footprint"}}
	markUnrenderedSilk(old)
	if old[0].Hidden {
		t.Fatal("without any boxes (old connector) nothing can be judged hidden")
	}
}
