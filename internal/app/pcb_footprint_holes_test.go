package app

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

// Real footprint source: J2 (USB-C-SMD_TYPE-C-16PIN, instance
// 10066e9574936036) cut DOCHEAD→next DOCHEAD from the ceshi E2E project
// archive (artifacts/ceshi-e2e-20260925/ceshi-e2e-final.epro2, EasyEDA V3
// 3.2.149). Its two e45/e46 MULTI-layer FILLs are the NPTH locating holes
// native DRC reported as "Slot Region" on 2026-09-25.
func readJ2FootprintSource(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/footprint-usbc16-10066e9574936036.epru")
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// J2 as the live `pcb dump` saw it (v3-base.json): anchor, rotation, pads.
func j2Comp() boardComp {
	c := boardComp{ID: "0998e91872a558c4", Designator: "J2", Layer: 1, X: 1053.16, Y: 167.97, Rotation: 0, FootprintUUID: "10066e9574936036"}
	for _, p := range []struct {
		id, num string
		x, y    float64
	}{
		{"e13", "A1B12", 926.2, 268.3}, {"e14", "B1A12", 1180.1, 268.3}, {"e15", "A4B9", 956.3, 268.3},
		{"e16", "B4A9", 1150, 268.3}, {"e17", "B8", 984.3, 268.3}, {"e18", "A5", 1004, 268.3},
		{"e19", "B7", 1023.6, 268.3}, {"e20", "A6", 1043.3, 268.3}, {"e21", "B5", 1122.1, 268.3},
		{"e22", "A8", 1102.4, 268.3}, {"e23", "B6", 1082.7, 268.3}, {"e24", "A7", 1063, 268.3},
		{"e25", "8", 853.6, 232.2}, {"e26", "9", 1252.8, 232.2}, {"e27", "10", 853.6, 67.7}, {"e28", "11", 1252.8, 67.7},
	} {
		c.Pads = append(c.Pads, boardPad{ID: c.ID + p.id, Number: p.num, X: p.x, Y: p.y, Layer: 1})
	}
	return c
}

func TestParseFootprintSourceHolesRealUSBC(t *testing.T) {
	holes, pads, problems, err := parseFootprintSourceHoles(readJ2FootprintSource(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
	if len(pads) != 16 {
		t.Fatalf("want 16 pads, got %d", len(pads))
	}
	// Only the two MULTI-layer FILLs — the 16 layer-50 pin-soldering FILLs and
	// the layer-49 marking dot are not holes.
	if len(holes) != 2 {
		t.Fatalf("want 2 NPTH FILLs, got %d: %+v", len(holes), holes)
	}
	for i, want := range []struct {
		id   string
		x, y float64
	}{{"e45", -113.78, 43.484}, {"e46", 113.78, 43.484}} {
		h := holes[i]
		if h.ID != want.id || !h.Circle || h.C != [2]float64{want.x, want.y} || h.R != 14.764 {
			t.Fatalf("hole %d = %+v, want %s circle at (%v,%v) r 14.764 (Ø0.75mm)", i, h, want.id, want.x, want.y)
		}
	}
}

func TestPlaceFootprintHolesPadVerified(t *testing.T) {
	holes, pads, _, err := parseFootprintSourceHoles(readJ2FootprintSource(t))
	if err != nil {
		t.Fatal(err)
	}
	got, verified := placeFootprintHoles(j2Comp(), holes, pads)
	if !verified {
		t.Fatal("J2 transform not verified by its own pads")
	}
	want := [][2]float64{{939.38, 211.45}, {1166.94, 211.45}}
	for i, h := range got {
		if h.Shape != "circle" || h.Dia != 29.53 || h.Transform != "pad-verified" || h.Owner != "J2" ||
			math.Abs(h.X-want[i][0]) > 0.02 || math.Abs(h.Y-want[i][1]) > 0.02 {
			t.Fatalf("hole %d = %+v, want circle Ø29.53 at %v", i, h, want[i])
		}
	}

	// The same footprint turned 90° and on the bottom side: the transform is
	// chosen by the pads, and the holes follow it.
	for _, tc := range []struct {
		name   string
		rot    float64
		layer  int
		mirror bool
	}{{"rot90", 90, 1, false}, {"rot270", 270, 1, false}, {"bottom-rot0", 0, 2, true}, {"bottom-rot90", 90, 2, true}} {
		c := j2Comp()
		c.Rotation, c.Layer = tc.rot, tc.layer
		tr := fpTransform{c.X, c.Y, tc.rot, tc.mirror}
		for i := range c.Pads {
			for _, lp := range pads {
				if c.ID+lp.ID == c.Pads[i].ID {
					q := tr.apply([2]float64{lp.X, lp.Y})
					c.Pads[i].X, c.Pads[i].Y = round2(q[0]), round2(q[1])
				}
			}
		}
		got, verified := placeFootprintHoles(c, holes, pads)
		if !verified {
			t.Fatalf("%s: not verified", tc.name)
		}
		for i, h := range got {
			q := tr.apply(holes[i].C)
			if math.Hypot(h.X-q[0], h.Y-q[1]) > 0.02 {
				t.Fatalf("%s: hole %d at (%v,%v), want %v", tc.name, i, h.X, h.Y, q)
			}
		}
	}

	// No pad evidence → assumed transform, and the caller is told.
	c := j2Comp()
	c.Pads = nil
	if _, verified := placeFootprintHoles(c, holes, pads); verified {
		t.Fatal("a pad-less component cannot verify the transform")
	}
	_, notes := footprintHolesFromSources([]boardComp{c}, map[string]string{c.FootprintUUID: readJ2FootprintSource(t)})
	if len(notes) != 1 || !strings.Contains(notes[0], "assumed transform") {
		t.Fatalf("assumed transform must be reported: %v", notes)
	}
}

func TestParseFillPathForms(t *testing.T) {
	dec := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// Nested circle (another footprint version writes [["CIRCLE",…]]).
	if sh, err := parseFillPath(dec(`[["CIRCLE",1,2,3]]`)); err != nil || !sh.Circle || sh.R != 3 {
		t.Fatalf("nested circle: %+v %v", sh, err)
	}
	// Rectangular slot polygon, nested with an inner contour.
	sh, err := parseFillPath(dec(`[[0,0,"L",40,0,40,10,0,10,0,0],[5,2,"L",6,2,6,3]]`))
	if err != nil || sh.Circle || len(sh.Poly) != 4 || sh.Envelope {
		t.Fatalf("rect slot: %+v %v", sh, err)
	}
	// Oblong slot with ARC ends: a convex envelope covering both semicircles.
	sh, err = parseFillPath(dec(`[0,-5,"L",20,-5,"ARC",180,20,5,"L",0,5,"ARC",180,0,-5]`))
	if err != nil || !sh.Envelope {
		t.Fatalf("arc slot: %+v %v", sh, err)
	}
	minX, maxX := math.Inf(1), math.Inf(-1)
	for _, p := range sh.Poly {
		minX, maxX = math.Min(minX, p[0]), math.Max(maxX, p[0])
	}
	if minX > -4.9 || maxX < 24.9 {
		t.Fatalf("arc envelope misses the rounded ends: x %.2f..%.2f", minX, maxX)
	}
	if _, err := parseFillPath(dec(`["SPLINE",1,2]`)); err == nil {
		t.Fatal("unknown command must be reported, not dropped")
	}
}

// The live CC1 copper the 2026-09-25 board carried into J2's e46 hole (native
// DRC: track 11.7 mil, track 0 mil ×2, via 0 mil — ≥ 11.8 required).
func TestFindFootprintHoleClearanceMatchesNativeDRC(t *testing.T) {
	holes, pads, _, err := parseFootprintSourceHoles(readJ2FootprintSource(t))
	if err != nil {
		t.Fatal(err)
	}
	placed, _ := placeFootprintHoles(j2Comp(), holes, pads)
	tracks := []pcbTrack{
		{ID: "e214b72f48190936", Net: "CC1", Layer: 1, X1: 1045.57, Y1: 199.91, X2: 1137.62, Y2: 199.91, Width: 10},
		{ID: "72b783af45938d8e", Net: "CC1", Layer: 1, X1: 1137.62, Y1: 199.91, X2: 1157.75, Y2: 220.04, Width: 10},
		{ID: "2a4a41d588b8222d", Net: "CC1", Layer: 2, X1: 1157.75, Y1: 220.04, X2: 1157.75, Y2: 257.44, Width: 10},
		{ID: "ok", Net: "GND", Layer: 1, X1: 900, Y1: 150, X2: 1200, Y2: 150, Width: 10},
	}
	vias := []pcbViaP{{ID: "b43b17f1618cc370", Net: "CC1", X: 1157.75, Y: 220.04, Dia: 24, Hole: 12}}
	otherPad := pcbPadP{ID: "x", Designator: "R9", Number: "1", Net: "N", X: 1166.94, Y: 240, W: 20, H: 20}
	ownPad := pcbPadP{ID: "y", Designator: "J2", Number: "B1A12", Net: "GND", X: 1180.1, Y: 268.3, W: 21.7, H: 57.1}
	clr, _ := slotClearance(pcbRules{clearanceMil: 5.98})
	fs := findFootprintHoleClearance(placed, tracks, vias, []pcbPadP{otherPad, ownPad}, clr)
	got := map[string]bool{}
	for _, f := range fs {
		if f.Level != "ERROR" || f.Type != "footprint-hole-clearance" {
			t.Fatalf("finding %+v", f)
		}
		got[f.Primitives[0]] = true
	}
	for _, id := range []string{"e214b72f48190936", "72b783af45938d8e", "2a4a41d588b8222d", "b43b17f1618cc370", "x"} {
		if !got[id] {
			t.Errorf("%s not flagged (native DRC fails it)", id)
		}
	}
	if got["ok"] || got["y"] {
		t.Errorf("false positive: %v", got)
	}
	if len(fs) != 5 {
		t.Errorf("want 5 findings, got %d: %+v", len(fs), fs)
	}
	var rep pcbCheckReport
	reportFootprintHoles(&rep, placed, tracks, vias, nil, pcbRules{clearanceMil: 5.98}, nil)
	if rep.Passed || rep.Summary.FootprintHoleClearance != 4 || rep.Summary.Errors != 4 {
		t.Fatalf("report summary %+v", rep.Summary)
	}
}

func TestSnapshotCarriesFootprintRefAndKeepsSemanticBaseline(t *testing.T) {
	res := map[string]any{"components": []any{map[string]any{
		"primitiveId": "0998e91872a558c4", "designator": "J2", "layer": 1.0, "x": 1053.16, "y": 167.97, "rotation": 0.0,
		"footprint": map[string]any{"uuid": "10066e9574936036", "libraryUuid": "lib"},
	}}}
	comps := parseBoardComponents(res)
	if comps[0].FootprintUUID != "10066e9574936036" {
		t.Fatalf("footprint uuid dropped: %+v", comps[0])
	}
	snap := &boardSnapshot{Components: comps, Rules: &boardRules{ClearanceMil: 6}}
	withHoles := *snap
	withHoles.FootprintHoles = []boardFootprintHole{{Owner: "J2", SourceID: "e45", Shape: "circle", X: 1, Y: 2, Dia: 3}}
	withHoles.Rules = &boardRules{ClearanceMil: 6, SlotClearanceMil: 11.81}
	plain := *snap
	plain.Components = append([]boardComp(nil), comps...)
	plain.Components[0].FootprintUUID = ""
	a, _ := boardSnapshotSemanticSHA256(&withHoles)
	b, _ := boardSnapshotSemanticSHA256(&plain)
	if a != b {
		t.Fatal("footprint hole data moved the semantic (module-check) baseline")
	}
}

func TestParsePcbRulesSlotRegionClearance(t *testing.T) {
	res := map[string]any{"rules": map[string]any{"config": map[string]any{"Spacing": map[string]any{"Safe Spacing": map[string]any{
		"copperThickness1oz": map[string]any{
			"row": []any{"Track", "SMD Pad", "Slot Region"},
			"tables": map[string]any{"1": map[string]any{"content": []any{
				[]any{0.1016}, []any{0.1524, 0.1524}, []any{0.3, 0.3, 0.3},
			}}},
		},
	}}}}}
	if r := parsePcbRules(res); math.Abs(r.slotClearanceMil-11.81) > 0.01 {
		t.Fatalf("slot clearance %.2f, want 11.81", r.slotClearanceMil)
	}
	if clr, why := slotClearance(pcbRules{clearanceMil: 6}); clr != defaultSlotClearanceMil || !strings.Contains(why, "default") {
		t.Fatalf("default slot clearance %v (%s)", clr, why)
	}
}
