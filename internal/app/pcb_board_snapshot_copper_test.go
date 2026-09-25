package app

import "testing"

func TestBoardSnapshotSemanticHashIgnoresCaptureMetadataOnly(t *testing.T) {
	s := lpSnapshot(lpComp("u1", "U1", 100, 100, 0, lpBBox(90, 90, 110, 110)))
	s.CapturedAt = "2026-09-22T01:00:00Z"
	s.SemanticSHA256 = "old-self-hash"
	h1, err := boardSnapshotSemanticSHA256(s)
	if err != nil {
		t.Fatal(err)
	}
	s.CapturedAt = "2026-09-22T02:00:00Z"
	s.SemanticSHA256 = "another-self-hash"
	h2, err := boardSnapshotSemanticSHA256(s)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("capture metadata changed semantic hash: %s != %s", h1, h2)
	}
	s.Components[0].X++
	h3, err := boardSnapshotSemanticSHA256(s)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h2 {
		t.Fatal("component geometry/identity change did not change semantic hash")
	}
}

func TestCopperSnapshotAvailabilityDistinguishesKnownEmptyFromUnknown(t *testing.T) {
	knownEmpty := &boardSnapshot{Copper: &boardCopperSnapshot{
		Availability: map[string]string{"routing": "available", "vias": "available", "pours": "available", "poured": "available", "regions": "available", "fills": "available"},
		Lines:        []any{}, Arcs: []any{}, Vias: []any{}, Pours: []any{}, Poured: []any{}, Regions: []any{}, Fills: []any{},
	}}
	available := true
	knownEmpty.Copper.ArcsAvailable = &available
	if got := knownEmpty.Copper.Availability["poured"]; got != "available" || knownEmpty.Copper.Poured == nil {
		t.Fatalf("known-empty materialized copper lost: availability=%q list=%#v", got, knownEmpty.Copper.Poured)
	}
	unknown := &boardSnapshot{Copper: &boardCopperSnapshot{Availability: map[string]string{"poured": "unknown"}}}
	if unknown.Copper.Availability["poured"] != "unknown" {
		t.Fatalf("unknown availability=%q", unknown.Copper.Availability["poured"])
	}
}

func TestBoardSnapshotPreservesExactPadShapeAndSpecialPad(t *testing.T) {
	shape := []any{"RECT", 20.0, 12.0, 2.0}
	special := []any{"L", 1.0, 2.0}
	components := parseBoardComponents(componentsResult(map[string]any{
		"primitiveId": "u1", "designator": "U1", "layer": 1.0,
		"pads": []any{map[string]any{
			"primitiveId": "p1", "padNumber": "1", "net": "GND", "layer": 1.0,
			"x": 10.0, "y": 20.0, "width": 20.0, "height": 12.0,
			"shape": shape, "specialPad": special,
		}},
	}))
	if len(components) != 1 || len(components[0].Pads) != 1 {
		t.Fatalf("components=%+v", components)
	}
	pad := components[0].Pads[0]
	if canonicalJSON(pad.Shape) != canonicalJSON(shape) || canonicalJSON(pad.SpecialPad) != canonicalJSON(special) {
		t.Fatalf("pad geometry was dropped: shape=%#v special=%#v", pad.Shape, pad.SpecialPad)
	}
}

// Materialised pour copper is regenerated on every reload: other ids, order
// and 64 vs 64.0. contentSha256 must not move; semanticSha256 must ignore the
// new field so recorded baselines keep matching.
func TestBoardSnapshotContentHashIgnoresPourRematerialisation(t *testing.T) {
	a := &boardSnapshot{Copper: &boardCopperSnapshot{Poured: []any{
		map[string]any{"primitiveId": "p1", "net": "GND", "fills": []any{map[string]any{"id": "f1", "source": []any{64.0, 1320.0}}}},
		map[string]any{"primitiveId": "p2", "net": "+3V3", "fills": []any{}},
	}}}
	b := &boardSnapshot{Copper: &boardCopperSnapshot{Poured: []any{
		map[string]any{"primitiveId": "q2", "net": "+3V3", "fills": []any{}},
		map[string]any{"primitiveId": "q1", "net": "GND", "fills": []any{map[string]any{"id": "g9", "source": []any{64.000001, 1320.0}}}},
	}}}
	ha, _ := boardSnapshotContentSHA256(a)
	hb, _ := boardSnapshotContentSHA256(b)
	if ha != hb {
		t.Fatal("content hash moved on a pure re-materialisation")
	}
	s1, _ := boardSnapshotSemanticSHA256(a)
	a.ContentSHA256 = ha
	s2, _ := boardSnapshotSemanticSHA256(a)
	if s1 != s2 {
		t.Fatal("semantic hash must not depend on contentSha256")
	}
}
