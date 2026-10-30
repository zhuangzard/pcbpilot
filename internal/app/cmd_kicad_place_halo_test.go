package app

import (
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

func TestKicadPlaceHalo(t *testing.T) {
	pads := func(n int) []*pcbauto.Pad {
		out := make([]*pcbauto.Pad, n)
		for i := range out {
			out[i] = &pcbauto.Pad{}
		}
		return out
	}
	b := &pcbauto.Board{Parts: []*pcbauto.Part{
		{Ref: "R1", Pads: pads(2)},
		{Ref: "U1", Pads: pads(16)},
	}}
	h := kicadPlaceHalo(b, 8, 40, true)
	if h["R1"] != 8 || h["U1"] != 40 {
		t.Fatalf("halo = %v, want R1 8 / U1 40", h)
	}
	h = kicadPlaceHalo(b, 0, 40, true)
	if _, ok := h["R1"]; ok || h["U1"] != 40 {
		t.Fatalf("halo (IC only) = %v", h)
	}
	// --halo-mil 8 --halo-ic-mil 0: the explicit 0 replaces the halo on ICs.
	h = kicadPlaceHalo(b, 8, 0, true)
	if _, ok := h["U1"]; ok || h["R1"] != 8 {
		t.Fatalf("halo (explicit IC 0) = %v", h)
	}
	// --halo-mil 8 alone: ICs get the general halo.
	h = kicadPlaceHalo(b, 8, 0, false)
	if h["U1"] != 8 || h["R1"] != 8 {
		t.Fatalf("halo (no IC flag) = %v", h)
	}
}
