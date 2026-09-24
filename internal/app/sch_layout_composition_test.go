package app

import (
	"strings"
	"testing"
)

func TestPlRotateUsesMeasuredDesignatorPose(t *testing.T) {
	c := powerLayoutPlacement{Designator: "R5", X: 100, Y: 200, BBox: layoutBBox{89.5, 195.5, 110.5, 204.5},
		TextBBoxes:           []layoutBBox{{90, 205, 98, 213}},
		TextBBoxesByRotation: map[string][]layoutBBox{"180": {{-10, 5, -2, 13}}}}
	got := plRotate(c, 2)
	if got.TextBBoxes[0] != (layoutBBox{90, 205, 98, 213}) {
		t.Fatalf("measured 180 pose ignored: %+v", got.TextBBoxes)
	}
	rigid := plRotate(c, 1) // no measured 90 pose: rigid estimate remains
	if rigid.TextBBoxes[0] != (layoutBBox{87, 190, 95, 198}) {
		t.Fatalf("rigid fallback changed: %+v", rigid.TextBBoxes)
	}
	moved := plTranslate(c, 50, 0)
	if back := plRotate(moved, 2); back.TextBBoxes[0] != (layoutBBox{140, 205, 148, 213}) {
		t.Fatalf("relative pose must follow the anchor: %+v", back.TextBBoxes)
	}
}

func TestLayoutCompositionMapsRolesAndRefusesDrift(t *testing.T) {
	pins := []powerLayoutPin{{Number: "1", Name: "A", Net: "SIG"}, {Number: "2", Name: "B", Net: "GND"}}
	place := func(ref string) powerLayoutPlacement {
		return powerLayoutPlacement{Designator: ref, BBox: layoutBBox{0, 0, 10, 10}, Pins: append([]powerLayoutPin(nil), pins...)}
	}
	in := schLayoutCompositionInput{ProjectID: "p", DocumentID: "d",
		Source: SchematicZonesInput{NetPolicies: map[string]string{"SIG": "direct", "GND": "local_ground"},
			Components: []SchematicLayoutComponent{{ID: "core", Measurement: place("U1")}, {ID: "per", Measurement: place("R1")}},
			Zones:      []SchematicZone{{ID: "Z", CoreComponentID: "core", ComponentIDs: []string{"core", "per"}}}},
		Devices: []schLayoutCompositionDevice{{ID: "core", LibraryUUID: "l", UUID: strings.Repeat("a", 32)}, {ID: "per", LibraryUUID: "l", DeviceUUID: strings.Repeat("b", 32)}},
		Page: SchematicRenderInput{Sheet: &SchematicRenderSheet{Bounds: layoutBBox{0, 0, 100, 100}, Border: layoutBBox{10, 10, 90, 90}, Keepouts: []SchematicBox{}},
			Zones: []SchematicRenderZone{{ID: "Z", Title: "Z", CoreComponentID: "core", Layout: &SchematicLayoutResult{
				Placements: []powerLayoutPlacement{place("U1"), place("R1")}, ComponentIDs: map[string]string{"U1": "core", "R1": "per"},
				PinStates: map[string]map[string]string{}}}}}}
	out, err := buildSchLayoutComposition(in)
	if err != nil {
		t.Fatal(err)
	}
	roles := map[string]string{}
	for _, n := range out.Connectivity.Nets {
		roles[n.Name] = n.Role
	}
	if roles["GND"] != "ground" || roles["SIG"] != "" || len(out.Connectivity.Connections) != 4 || out.Connectivity.Modules[0].CoreComponents[0] != "core" {
		t.Fatalf("bad IR: %+v", out.Connectivity)
	}
	in.Page.Zones[0].Layout.Placements[1].Pins[0].Net = "OTHER"
	if _, err := buildSchLayoutComposition(in); err == nil {
		t.Fatal("pin net drift between source and page accepted")
	}
}
