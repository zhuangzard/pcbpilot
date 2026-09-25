package app

import (
	"testing"
)

func TestMacroBuildAndExpandIsRigidWithMeasuredText(t *testing.T) {
	r90 := 0.0
	xtal := powerLayoutPlacement{Designator: "X1", X: 0, Y: 0, BBox: layoutBBox{-10.5, -5.5, 10.5, 5.5},
		TextBBoxes:           []layoutBBox{{-5, 8, 3, 16}},
		TextBBoxesByRotation: map[string][]layoutBBox{"0": {{-5, 8, 3, 16}}, "90": {{12, -4, 20, 4}}, "180": {{-5, 8, 3, 16}}, "270": {{12, -4, 20, 4}}},
		Pins:                 []powerLayoutPin{{Number: "1", Name: "1", Net: "OSC_IN", X: -20, Y: 0, Rotation: ptrFloat(180)}, {Number: "2", Name: "2", Net: "OSC_OUT", X: 20, Y: 0, Rotation: &r90}}}
	child := &SchematicLayoutResult{ComponentIDs: map[string]string{"X1": "X1"}, PinStates: map[string]map[string]string{},
		Placements: []powerLayoutPlacement{xtal},
		Flags: []powerLayoutFlag{{Net: "OSC_IN", Kind: "net_port_bi", PinX: -20, PinY: 0, Direction: "left", Offset: 10},
			{Net: "OSC_OUT", Kind: "net_port_bi", PinX: 20, PinY: 0, Direction: "right", Offset: 10}}}
	m, err := buildSchematicMacro(SchematicZone{ID: "XTAL", CoreComponentID: "X1"}, child, map[string]bool{"OSC_IN": true, "OSC_OUT": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.part.Measurement.Pins) != 2 || len(m.part.AllowedRotations) != 4 || m.part.Measurement.TextBBoxesByRotation["90"] == nil {
		t.Fatalf("macro part incomplete: %+v", m.part)
	}
	// Place the macro at (100,50) turned 90 degrees and expand.
	placed := plTranslate(plRotate(m.part.Measurement, 1), 100, 50)
	parent := &SchematicLayoutResult{ComponentIDs: map[string]string{"XTAL": macroComponentID("XTAL")}, PinStates: map[string]map[string]string{},
		Placements: []powerLayoutPlacement{placed}}
	// The parent names the macro ports where its wires would arrive.
	for _, q := range placed.Pins {
		side, _ := libPinSide(q, placed.BBox)
		parent.Flags = append(parent.Flags, powerLayoutFlag{Net: q.Net, Kind: "net_port_bi", PinX: q.X, PinY: q.Y, Direction: side, Offset: 10})
	}
	if err := m.expand(parent); err != nil {
		t.Fatal(err)
	}
	if len(parent.Placements) != 1 || parent.Placements[0].Designator != "X1" || parent.ComponentIDs["X1"] != "X1" {
		t.Fatalf("expansion lost the child: %+v", parent.Placements)
	}
	got := parent.Placements[0]
	if got.X != 100 || got.Y != 50 || got.Rotation != 90 {
		t.Fatalf("child anchor/rotation not rigid: %+v", got)
	}
	if got.TextBBoxes[0] != (layoutBBox{112, 46, 120, 54}) {
		t.Fatalf("designator must use the measured 90-degree pose: %+v", got.TextBBoxes)
	}
	if len(parent.Flags) != 2 || len(parent.Wires) != 2 {
		t.Fatalf("child port flags must be dropped (only the parent's 2 remain) and stems restored: flags %d wires %d", len(parent.Flags), len(parent.Wires))
	}
}

func ptrFloat(v float64) *float64 { return &v }
