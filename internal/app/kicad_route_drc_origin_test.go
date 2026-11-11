package app

import (
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func TestDRCOrigins(t *testing.T) {
	v := func(rule string, items ...string) kicad.Violation {
		o := kicad.Violation{Rule: rule, Section: "violations"}
		for _, d := range items {
			o.Items = append(o.Items, kicad.DRCItem{Description: d})
		}
		return o
	}
	got := drcOrigins([]kicad.Violation{
		v("clearance", "Pad 1 [GND] of U1 on F.Cu", "Pad 2 [VCC] of U1 on F.Cu"),
		v("hole_clearance", "Pad A1 [GND] of J1 on F.Cu", "NPTH pad of J1"),
		v("clearance", "Pad 1 [GND] of U1 on F.Cu", "Pad 2 [VCC] of U2 on F.Cu"),
		v("clearance", "Track [A] on In1.Cu, length 1 mm", "Track [B] on In1.Cu, length 2 mm"),
		v("clearance", "Track [A] on F.Cu, length 1 mm", "Pad 1 [B] of U1 on F.Cu"),
		v("copper_edge_clearance", "Segment on Edge.Cuts", "PTH pad 3 [CHASSIS] of J2"),
		v("copper_edge_clearance", "Segment on Edge.Cuts", "Track [A] on In4.Cu, length 1 mm"),
		// A net named like a footprint must not read as one.
		v("clearance", "Track [X of U9] on F.Cu, length 1 mm", "Track [Y of U9] on F.Cu, length 1 mm"),
		{Rule: "unconnected_items", Section: "unconnected_items", Items: []kicad.DRCItem{{Description: "Pad 1 of U1"}, {Description: "Pad 2 of U1"}}},
	})
	if got.FootprintInternal != 2 || got.EdgePad != 1 || got.Other != 5 {
		t.Fatalf("got %+v", got)
	}
	if got.Footprints["U1"] != 1 || got.Footprints["J1"] != 1 || got.Footprints["J2"] != 1 || len(got.Footprints) != 3 {
		t.Fatalf("footprints %v", got.Footprints)
	}
}
