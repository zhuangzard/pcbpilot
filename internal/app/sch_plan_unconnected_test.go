package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

func explicitOpenPlanFixture(t *testing.T) (connectivity.Document, connectivity.Document) {
	t.Helper()
	a, _ := planFixtures()
	for i := range a.Components[0].Pins {
		a.Components[0].Pins[i].ConnectionState = "unconnected"
	}
	var b connectivity.Document
	raw, _ := json.Marshal(a)
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	b.Components[0].Pins[0].ConnectionState = ""
	b.Connections = []connectivity.Connection{{ComponentID: "c", PinNumber: "1", NetID: "n", Kind: "ground"}}
	return a, b
}

func TestPlanConnectsExplicitOpenPinsWithIndependentCheckpoints(t *testing.T) {
	a, b := explicitOpenPlanFixture(t)
	b.Components[0].Pins[1].ConnectionState = ""
	b.Connections = append(b.Connections, connectivity.Connection{ComponentID: "c", PinNumber: "2", NetID: "n", Kind: "ground"})
	before, _ := json.Marshal(a)
	pb, err := buildSchPlan(a, b)
	if err != nil {
		t.Fatal(err)
	}
	var states [][]string
	for _, step := range pb.Steps {
		if step.ExpectedConnectivity == nil {
			continue
		}
		d := step.ExpectedConnectivity
		if err := d.Validate(); err != nil {
			t.Fatalf("invalid intermediate expected state: %v", err)
		}
		states = append(states, []string{d.Components[0].Pins[0].ConnectionState, d.Components[0].Pins[1].ConnectionState})
	}
	want := [][]string{{"unconnected", "unconnected"}, {"", "unconnected"}, {"", ""}, {"", ""}}
	if !reflect.DeepEqual(states, want) {
		t.Fatalf("checkpoints aliased or state not advanced with its edge: %+v", states)
	}
	after, _ := json.Marshal(a)
	// Validate appends diagnostic issues only to checkpoint copies, never to
	// the original source. Component/pin slices must also remain unchanged.
	if string(before) != string(after) {
		t.Fatal("source open-pin state changed while compiling")
	}
}

func TestPlanOpenTransitionRemainsBounded(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*connectivity.Document)
	}{
		{"cleared without edge", func(d *connectivity.Document) { d.Components[0].Pins[1].ConnectionState = "" }},
		{"changed pin name", func(d *connectivity.Document) { d.Components[0].Pins[0].Name = "changed" }},
		{"changed geometry", func(d *connectivity.Document) { d.Components[0].Pins[0].X = 5 }},
		{"changed NC", func(d *connectivity.Document) { d.Components[0].Pins[0].NoConnected = true }},
		{"unsupported edge", func(d *connectivity.Document) { d.Connections[0].Kind = "wire" }},
		{"remaining open declaration", func(d *connectivity.Document) { d.Components[0].Pins[0].ConnectionState = "unconnected" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := explicitOpenPlanFixture(t)
			tc.edit(&b)
			if _, err := buildSchPlan(a, b); err == nil {
				t.Fatal("unsupported open-state edit compiled")
			}
		})
	}
}

func TestConnectivityCheckpointRequiresExplicitOpenEvidenceAndNC(t *testing.T) {
	a, _ := explicitOpenPlanFixture(t)
	for _, tc := range []struct {
		name string
		pin  string
		pass bool
	}{
		{"explicit empty and false", `{"number":"1","net":"","noConnected":false}`, true},
		{"missing NC evidence", `{"number":"1","net":""}`, false},
		{"null NC evidence", `{"number":"1","net":"","noConnected":null}`, false},
		{"null net evidence", `{"number":"1","net":null,"noConnected":false}`, false},
		{"became NC", `{"number":"1","net":"","noConnected":true}`, false},
		{"became connected", `{"number":"1","net":"GND","noConnected":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var live map[string]any
			raw := `{"components":[{"componentType":"part","designator":"U1","pins":[` + tc.pin + `,{"number":"2","net":"","noConnected":false}]}]}`
			if err := json.Unmarshal([]byte(raw), &live); err != nil {
				t.Fatal(err)
			}
			err := compareObserved(a, live)
			if (err == nil) != tc.pass {
				t.Fatalf("wrong evidence result: %v", err)
			}
		})
	}
}

func TestExplicitOpenStillFailsStrictElectricalGate(t *testing.T) {
	rep := checkReport{Findings: []checkFinding{{Type: "floating-pin", Level: "warn", Designator: "U1", Pins: []string{"1"}}}}
	_, _, reasons := gradeGateCheckFindings(rep, true)
	if len(reasons) == 0 {
		t.Fatal("an open pin must still block strict electrical acceptance")
	}
	source := composeFixture(2)
	source.Connectivity.Components[0].Pins[2].NoConnected = false
	source.Connectivity.Components[0].Pins[2].ConnectionState = "unconnected"
	plan, err := planSchComposition(source)
	if err != nil {
		t.Fatal(err)
	}
	_, live := composeApplyFixture(t, false)
	pb, err := schCompositionPlaybook(plan, composeApplyBytes(t, live), true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, step := range pb.Steps {
		if step.Run == "sch gate" && step.Flags["strict"] == true {
			found = true
		}
	}
	if !found {
		t.Fatal("open-state support bypassed the electrical gate")
	}
}
