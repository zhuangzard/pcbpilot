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
	h := kicadPlaceHalo(b, 8, 40)
	if h["R1"] != 8 || h["U1"] != 40 {
		t.Fatalf("halo = %v, want R1 8 / U1 40", h)
	}
	h = kicadPlaceHalo(b, 0, 40)
	if _, ok := h["R1"]; ok || h["U1"] != 40 {
		t.Fatalf("halo (IC only) = %v", h)
	}
}
