package app

import (
	"strings"
	"testing"
)

// A connector J1 (6 signal pins + VDD + GND) with one pull-up per signal pin,
// a decoupling cap, and an unrelated key: exercises every zoning rule.
func zonesDeriveFixture() ([]zonesDerivePart, map[string]any, map[string]any) {
	comps, desigs := []any{}, []any{}
	add := func(ref string, x float64, pins [][2]string) {
		ps := []any{}
		for i, p := range pins {
			ps = append(ps, map[string]any{"pinNumber": p[0], "pinName": p[1], "x": x - 10, "y": float64(i * 10), "rotation": 180.0})
		}
		comps = append(comps, map[string]any{"componentType": "part", "designator": ref, "primitiveId": "p-" + ref, "x": x, "y": 0.0, "rotation": 0.0, "mirror": false,
			"bbox": map[string]any{"minX": x - 5, "minY": -5.0, "maxX": x + 5, "maxY": 80.0}, "pins": ps})
		desigs = append(desigs, map[string]any{"parentId": "p-" + ref, "value": ref, "bbox": map[string]any{"minX": x, "minY": 90.0, "maxX": x + 8, "maxY": 98.0}})
	}
	add("J1", 0, [][2]string{{"1", "D0"}, {"2", "D1"}, {"3", "D2"}, {"4", "D3"}, {"5", "VDD"}, {"6", "GND"}, {"A1B2", "SHIELD"}, {"8", "SPARE"}})
	intent := []zonesDerivePart{{ID: "J1", Designator: "J1", FunctionalZone: "card", Pins: []zonesDerivePin{
		{Terminal: "D0", Net: "S0"}, {Terminal: "D1", Net: "S1"}, {Terminal: "3", Net: "S2"}, {Terminal: "4", Net: "S3"},
		{Terminal: "VDD", Net: "+3V3"}, {Terminal: "GND", Net: "GND"}, {Terminal: "A1/B2", Net: "GND"}}}}
	for i, s := range []string{"S0", "S1", "S2", "S3"} {
		ref := "R" + string(rune('1'+i))
		add(ref, float64(100+i*50), [][2]string{{"1", "1"}, {"2", "2"}})
		intent = append(intent, zonesDerivePart{ID: ref, Designator: ref, FunctionalZone: "card", Pins: []zonesDerivePin{{Terminal: "1", Net: s}, {Terminal: "2", Net: "+3V3"}}})
		add("U"+string(rune('1'+i)), float64(600+i*50), [][2]string{{"1", "IN"}})
		intent = append(intent, zonesDerivePart{ID: "U" + string(rune('1'+i)), Designator: "U" + string(rune('1'+i)), FunctionalZone: "mcu", Pins: []zonesDerivePin{{Terminal: "IN", Net: s}}})
	}
	add("C1", 400, [][2]string{{"1", "1"}, {"2", "2"}})
	intent = append(intent, zonesDerivePart{ID: "C1", Designator: "C1", FunctionalZone: "card", Pins: []zonesDerivePin{{Terminal: "1", Net: "+3V3"}, {Terminal: "2", Net: "GND"}}})
	add("SW1", 500, [][2]string{{"1", "1"}, {"2", "2"}})
	intent = append(intent, zonesDerivePart{ID: "SW1", Designator: "SW1", FunctionalZone: "card", Pins: []zonesDerivePin{{Terminal: "1", Net: "KEY"}, {Terminal: "2", Net: "GND"}}})
	return intent, map[string]any{"result": map[string]any{"components": comps}}, map[string]any{"designators": desigs}
}

func TestZonesDeriveRules(t *testing.T) {
	intent, list, desig := zonesDeriveFixture()
	opts := zonesDeriveOptions{Power: map[string]bool{"+3V3": true}, Ground: map[string]bool{"GND": true}, ArrayMin: 4, Spacing: 10, MaxCandidates: 1000}
	src, rep, err := deriveSchematicZones(intent, list, desig, nil, opts)
	if err != nil {
		t.Fatal(err)
	}
	zones := map[string]SchematicZone{}
	for _, z := range src.Zones {
		zones[z.ID] = z
	}
	if got := strings.Join(zones["CARD"].ComponentIDs, ","); got != "J1,C1" {
		t.Fatalf("card zone keeps core + rail-only decoupling, got %s", got)
	}
	if got := strings.Join(zones["CARD_ARRAY"].ComponentIDs, ","); got != "R1,R2,R3,R4" {
		t.Fatalf("dense connector array, got %s", got)
	}
	if z, ok := zones["CARD_2"]; !ok || z.CoreComponentID != "SW1" {
		t.Fatalf("detached key must be its own zone: %+v", zones)
	}
	if src.NetPolicies["S0"] != "module_port" || src.NetPolicies["+3V3"] != "local_power" || src.NetPolicies["GND"] != "local_ground" || src.NetPolicies["KEY"] != "module_port" {
		t.Fatalf("policies %v", src.NetPolicies)
	}
	var j1 SchematicLayoutComponent
	for _, c := range src.Components {
		if c.ID == "J1" {
			j1 = c
		}
	}
	if j1.PinStates["8"] != "unconnected" || j1.Measurement.Pins[6].Net != "GND" || rep.OpenPins["J1"][0] != "8" {
		t.Fatalf("unmentioned pin must be an explicit open, slash terminal must map: %+v %v", j1.PinStates, rep.OpenPins)
	}
	for _, c := range src.Components {
		if c.ID == "R1" && len(c.AllowedRotations) != 4 {
			t.Fatal("two-pin parts default to four rotations")
		}
	}
}

func TestZonesDeriveRefusesGuesses(t *testing.T) {
	intent, list, desig := zonesDeriveFixture()
	opts := zonesDeriveOptions{Power: map[string]bool{}, Ground: map[string]bool{"GND": true}, ArrayMin: 4}
	bad := append([]zonesDerivePart(nil), intent...)
	bad[0].Pins = append([]zonesDerivePin(nil), bad[0].Pins...)
	bad[0].Pins[0] = zonesDerivePin{Terminal: "D0", Pin: "1", Net: "S0"}
	if _, _, err := deriveSchematicZones(bad, list, desig, nil, opts); err == nil || !strings.Contains(err.Error(), "source") {
		t.Fatalf("override without source accepted: %v", err)
	}
	bad[0].Pins[0] = zonesDerivePin{Terminal: "NOPE", Net: "S0"}
	if _, _, err := deriveSchematicZones(bad, list, desig, nil, opts); err == nil {
		t.Fatal("unmappable terminal accepted")
	}
	bad[0].Pins[0] = zonesDerivePin{Terminal: "D0", Net: "S0"}
	bad[0].Pins = append(bad[0].Pins, zonesDerivePin{Terminal: "1", Net: "X"})
	if _, _, err := deriveSchematicZones(bad, list, desig, nil, opts); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("double-mapped pin accepted: %v", err)
	}
}
