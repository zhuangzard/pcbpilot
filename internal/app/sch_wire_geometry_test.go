package app

import (
	"reflect"
	"testing"
)

func TestSchWireGeometryAdapterPreservesEvidenceAndBlocks(t *testing.T) {
	result := map[string]any{"components": []any{map[string]any{
		"componentType": "part", "designator": "U1", "primitiveId": "u1",
		"bbox":          map[string]any{"minX": 0.0, "minY": 0.0, "maxX": 20.0, "maxY": 20.0},
		"pinsAvailable": true, "pins": []any{map[string]any{"pinNumber": "1", "x": 30.0, "y": 10.0, "rotation": 0.0}},
	}}, "wires": "original observation must remain intact"}
	fs := schWireGeometryFindings(result, []schGroupWire{{ID: "stable-wire-id", Points: []float64{10, 10, 30, 10}}})
	if len(fs) != 2 {
		t.Fatalf("expected direction and penetration: %+v", fs)
	}
	for _, f := range fs {
		if f.WirePrimitiveId != "stable-wire-id" || f.Designator != "U1" || f.PrimitiveId != "u1" || f.Message == "" {
			t.Fatalf("lost evidence: %+v", f)
		}
		if !checkLevelBlocks(f.Level, false) {
			t.Fatalf("hard errors cannot depend on strict: %+v", f)
		}
	}
	if result["wires"] != "original observation must remain intact" {
		t.Fatal("adapter modified original raw snapshot")
	}
}

func TestSchWireGeometryAdapterDoesNotInventMergedTreeSegments(t *testing.T) {
	result := map[string]any{"components": []any{map[string]any{
		"componentType": "part", "designator": "U1", "primitiveId": "u1",
		"bbox":          map[string]any{"minX": 0.0, "minY": 0.0, "maxX": 20.0, "maxY": 20.0},
		"pinsAvailable": true, "pins": []any{},
	}}}
	// Official merged-tree array has two independent horizontal segments,
	// not the fictitious (30,-10)→(-10,30) diagonal through U1.
	wires := []schGroupWire{{ID: "merged", Points: []float64{-10, -10, 30, -10, -10, 30, 30, 30}}}
	if fs := schWireGeometryFindings(result, wires); len(fs) != 0 {
		t.Fatalf("fabricated inter-segment edge: %+v", fs)
	}
}

// The connector and the Go-side geometry guard now share one severity vocabulary.
// Geometry findings were emitted as uppercase ERROR while connector rules use
// lowercase; callers filtering level=="error" (issue #299's report) missed them.
func TestSchWireGeometryAdapterNormalizesLevelCase(t *testing.T) {
	result := map[string]any{"components": []any{map[string]any{
		"componentType": "part", "designator": "U1", "primitiveId": "u1",
		"bbox":          map[string]any{"minX": 0.0, "minY": 0.0, "maxX": 20.0, "maxY": 20.0},
		"pinsAvailable": true, "pins": []any{map[string]any{"pinNumber": "1", "x": 30.0, "y": 10.0, "rotation": 0.0}},
	}}}
	fs := schWireGeometryFindings(result, []schGroupWire{{ID: "w", Points: []float64{10, 10, 30, 10}}})
	if len(fs) != 2 {
		t.Fatalf("expected two geometry findings: %+v", fs)
	}
	for _, f := range fs {
		if f.Level != "error" {
			t.Fatalf("level %q must be normalized to lowercase error: %+v", f.Level, f)
		}
	}
}

func TestZeroLengthIdentity(t *testing.T) {
	at := &checkPoint{X: 1.5, Y: 2.25}
	for _, tc := range []struct {
		name string
		f    checkFinding
		want string
	}{
		{"wire id", checkFinding{Type: "zero-length-wire", WirePrimitiveId: "w1"}, "id:w1"},
		{"coordinate fallback", checkFinding{Type: "zero-length-wire", At: at}, "at:1.5000,2.2500"},
		{"other rule", checkFinding{Type: "wire-crossing", WirePrimitiveId: "w1"}, ""},
		{"no identity", checkFinding{Type: "zero-length-wire"}, ""},
	} {
		if got := zeroLengthIdentity(tc.f); got != tc.want {
			t.Errorf("%s: identity=%q, want %q", tc.name, got, tc.want)
		}
	}
}

// One zero-length wire is reported once — matched by primitive id or (when the
// id is missing) by anchor — and the surviving copy carries the highest
// severity with its own evidence. Other rules that share an id stay.
func TestMergeDuplicateZeroLengthFindingsKeepsHighestSeverity(t *testing.T) {
	base := []checkFinding{
		{Type: "zero-length-wire", Level: "warn", WirePrimitiveId: "w1", Message: "connector"},
		{Type: "zero-length-wire", Level: "warn", At: &checkPoint{X: 5, Y: 6}},
		{Type: "wire-crossing", Level: "error", At: &checkPoint{X: 5, Y: 6}},
		{Type: "zero-length-wire", Level: "error", WirePrimitiveId: "w3", Message: "connector"},
	}
	extra := []checkFinding{
		{Type: "zero-length-wire", Level: "error", WirePrimitiveId: "w1", Message: "guard"}, // same primitive, more severe
		{Type: "zero-length-wire", Level: "error", At: &checkPoint{X: 5, Y: 6}},             // same coordinate, more severe
		{Type: "zero-length-wire", Level: "warn", WirePrimitiveId: "w2"},                    // new primitive
		{Type: "zero-length-wire", Level: "error", WirePrimitiveId: "w2", Message: "guard"}, // repeats it, more severe
		{Type: "wire-through-body", Level: "error", WirePrimitiveId: "w1"},                  // other rule, same id
		{Type: "zero-length-wire", Level: "warn", WirePrimitiveId: "w3", Message: "guard"},  // less severe: existing wins
	}
	snapshot := append([]checkFinding(nil), base...)
	merged, added := mergeDuplicateZeroLengthFindings(base, extra)
	if !reflect.DeepEqual(base, snapshot) {
		t.Fatalf("input slice was modified: %+v", base)
	}
	wantMerged := []checkFinding{extra[0], extra[1], base[2], base[3]}
	if !reflect.DeepEqual(merged, wantMerged) {
		t.Fatalf("merged = %+v, want %+v", merged, wantMerged)
	}
	wantAdded := []checkFinding{extra[3], extra[4]}
	if !reflect.DeepEqual(added, wantAdded) {
		t.Fatalf("added = %+v, want %+v", added, wantAdded)
	}
	if _, all := mergeDuplicateZeroLengthFindings(nil, extra[4:]); !reflect.DeepEqual(all, extra[4:]) {
		t.Fatalf("empty existing must keep every geometry finding: %+v", all)
	}
}

// WARN from the connector plus ERROR from the geometry guard for the same
// primitive is ONE blocking finding, and the report fails even without --strict.
func TestZeroLengthWarnPlusErrorIsOneBlockingFinding(t *testing.T) {
	rep := checkReport{Findings: []checkFinding{{Type: "zero-length-wire", Level: "warn", WirePrimitiveId: "w1"}}}
	if blockingCheckFindings(rep, false) != 0 {
		t.Fatal("precondition: a lone WARN does not block without strict")
	}
	var added []checkFinding
	rep.Findings, added = mergeDuplicateZeroLengthFindings(rep.Findings,
		[]checkFinding{{Type: "zero-length-wire", Level: "error", WirePrimitiveId: "w1"}})
	rep.Findings = append(rep.Findings, added...)
	rep.Summary.Total = len(rep.Findings)
	rep.Passed = len(rep.Findings) == 0
	if len(rep.Findings) != 1 || rep.Findings[0].Level != "error" {
		t.Fatalf("findings = %+v, want exactly one ERROR", rep.Findings)
	}
	if rep.Passed || rep.Summary.Total != 1 || blockingCheckFindings(rep, false) != 1 {
		t.Fatalf("report must fail with one blocking finding: passed=%v total=%d blocking=%d",
			rep.Passed, rep.Summary.Total, blockingCheckFindings(rep, false))
	}
}

// A duplicate whose first occurrence came from the geometry findings must be
// looked up in the added slice, never by that index in the existing one.
func TestMergeDuplicateZeroLengthFindingsIndexesAddedSlice(t *testing.T) {
	warn := checkFinding{Type: "zero-length-wire", Level: "warn", WirePrimitiveId: "w1", Message: "first"}
	fail := checkFinding{Type: "zero-length-wire", Level: "error", WirePrimitiveId: "w1", Message: "second"}

	// Nothing reported by the connector at all.
	merged, added := mergeDuplicateZeroLengthFindings(nil, []checkFinding{warn, fail})
	if len(merged) != 0 || !reflect.DeepEqual(added, []checkFinding{fail}) {
		t.Fatalf("empty existing: merged=%+v added=%+v, want one error in added", merged, added)
	}
	merged, added = mergeDuplicateZeroLengthFindings([]checkFinding{}, []checkFinding{fail, warn})
	if len(merged) != 0 || !reflect.DeepEqual(added, []checkFinding{fail}) {
		t.Fatalf("empty existing, error first: merged=%+v added=%+v", merged, added)
	}

	// The duplicate sits at an added index past the end of existing.
	other := checkFinding{Type: "wire-crossing", Level: "error", WirePrimitiveId: "x"}
	pad1 := checkFinding{Type: "wire-through-body", Level: "error", WirePrimitiveId: "a"}
	pad2 := checkFinding{Type: "wire-through-body", Level: "error", WirePrimitiveId: "b"}
	existing := []checkFinding{other}
	merged, added = mergeDuplicateZeroLengthFindings(existing, []checkFinding{pad1, pad2, warn, fail})
	if !reflect.DeepEqual(merged, existing) {
		t.Fatalf("existing must be untouched: %+v", merged)
	}
	if want := []checkFinding{pad1, pad2, fail}; !reflect.DeepEqual(added, want) {
		t.Fatalf("added = %+v, want %+v", added, want)
	}

	// An added index that is in range for existing must still not rewrite it.
	existing = []checkFinding{other, pad1}
	merged, added = mergeDuplicateZeroLengthFindings(existing, []checkFinding{warn, fail})
	if !reflect.DeepEqual(merged, existing) || !reflect.DeepEqual(added, []checkFinding{fail}) {
		t.Fatalf("in-range added index: merged=%+v added=%+v", merged, added)
	}
}
