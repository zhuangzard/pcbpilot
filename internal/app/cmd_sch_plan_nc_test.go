package app

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

func ncPlanFixture() (connectivity.Document, connectivity.Document) {
	a, _ := planFixtures()
	a.Components[0].Pins[0].NoConnected = true
	a.Components[0].Pins[1].NoConnected = true
	b := cloneSchPlanState(a)
	b.Components[0].Pins[0].NoConnected = false
	b.Connections = []connectivity.Connection{{ComponentID: "c", PinNumber: "1", NetID: "n", Kind: "ground"}}
	return a, b
}

func TestPlanNCTransitionHasIndependentGuardedStages(t *testing.T) {
	a, b := ncPlanFixture()
	b.Components[0].Pins[1].NoConnected = false
	b.Connections = append(b.Connections, connectivity.Connection{ComponentID: "c", PinNumber: "2", NetID: "n", Kind: "power"})
	originalA, _ := json.Marshal(a)
	originalB, _ := json.Marshal(b)
	pb, err := buildSchPlan(a, b)
	if err != nil {
		t.Fatal(err)
	}
	if !pb.RequireFullExecution || len(pb.Steps) != 11 || *pb.Defaults.Retry != 0 || *pb.Defaults.ContinueOnError {
		t.Fatalf("unsafe generated queue: %+v", pb)
	}
	if errs := preflight(pb, nil); len(errs) != 0 {
		t.Fatal(errs)
	}
	var states [][]string
	var ncPins []string
	for _, step := range pb.Steps {
		if step.ExpectedConnectivity != nil {
			d := step.ExpectedConnectivity
			if err := d.Validate(); err != nil {
				t.Fatalf("invalid intermediate guard: %v", err)
			}
			var pins []string
			for _, pin := range d.Components[0].Pins {
				s := "connected"
				if pin.NoConnected {
					s = "NC"
				} else if pin.ConnectionState != "" {
					s = pin.ConnectionState
				}
				pins = append(pins, s)
			}
			states = append(states, pins)
		}
		if step.Action == "schematic.pin.set_no_connect" {
			pins, ok := step.Payload["pins"].([]string)
			if !ok || len(pins) != 1 || step.Payload["designator"] != "U1" || step.Payload["noConnected"] != false || step.Assert["$.notApplied"] != "len==0" {
				t.Fatalf("NC clear must identify exactly its pin and verify persistence: %+v", step)
			}
			ncPins = append(ncPins, pins[0])
		}
		if step.Run == "sch autoconnect" {
			for k, v := range map[string]any{"strict": true, "offset-min": 10, "offset-max": 80, "offset-step": 5, "offset-cap": 300} {
				if step.Flags[k] != v {
					t.Fatalf("unsafe/missing autoconnect flag %s: %+v", k, step.Flags)
				}
			}
		}
	}
	want := [][]string{{"NC", "NC"}, {"unconnected", "NC"}, {"connected", "NC"}, {"connected", "unconnected"}, {"connected", "connected"}, {"connected", "connected"}}
	if !reflect.DeepEqual(states, want) || !reflect.DeepEqual(ncPins, []string{"1", "2"}) {
		t.Fatalf("NC stages aliased or incomplete: %v / %v", states, ncPins)
	}
	afterA, _ := json.Marshal(a)
	afterB, _ := json.Marshal(b)
	if !bytes.Equal(originalA, afterA) || !bytes.Equal(originalB, afterB) {
		t.Fatal("compilation mutated caller pin/issue slices")
	}
}

func TestPlanNCTransitionRejectsUnpairedAndConflictingEdits(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*connectivity.Document, *connectivity.Document)
	}{
		{"isolated clear", func(a, b *connectivity.Document) { b.Connections = nil }},
		{"another pin cleared", func(a, b *connectivity.Document) { b.Components[0].Pins[1].NoConnected = false }},
		{"wire is not marker", func(a, b *connectivity.Document) { b.Connections[0].Kind = "wire" }},
		{"unspecified marker", func(a, b *connectivity.Document) { b.Connections[0].Kind = "" }},
		{"unsupported marker alias", func(a, b *connectivity.Document) { b.Connections[0].Kind = "gnd" }},
		{"target NC plus edge", func(a, b *connectivity.Document) { b.Components[0].Pins[0].NoConnected = true }},
		{"baseline NC plus edge", func(a, b *connectivity.Document) {
			a.Connections = append([]connectivity.Connection(nil), b.Connections...)
		}},
		{"target open plus edge", func(a, b *connectivity.Document) { b.Components[0].Pins[0].ConnectionState = "unconnected" }},
		{"pin renamed", func(a, b *connectivity.Document) { b.Components[0].Pins[0].Name = "different" }},
		{"pin moved", func(a, b *connectivity.Document) { b.Components[0].Pins[0].X = 5 }},
		{"isolated NC added", func(a, b *connectivity.Document) {
			a.Components[0].Pins[1].NoConnected = false
			b.Components[0].Pins[1].NoConnected = true
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := ncPlanFixture()
			tc.edit(&a, &b)
			if _, err := buildSchPlan(a, b); err == nil {
				t.Fatal("unsupported NC change compiled")
			}
		})
	}
}

func TestPlanNCTransitionCannotSkipOrRetarget(t *testing.T) {
	a, b := ncPlanFixture()
	pb, err := buildSchPlan(a, b)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(pb)
	path := filepath.Join(t.TempDir(), "nc-apply.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--resume"}, {"--from", "clear-nc-002"}, {"--to", "connect-004"}, {"--doc", "different"}, {"--project", "different"}} {
		var out, errs bytes.Buffer
		argv := append([]string{"sch", "apply", path, "--dry-run"}, args...)
		if code := Run(argv, &out, &errs); code == 0 || !(strings.Contains(errs.String(), "full execution") || strings.Contains(errs.String(), "guarded plan target")) {
			t.Fatalf("%v must stop offline: code=%d %s", args, code, errs.String())
		}
	}
}

func TestPlanNCIntermediateGuardStopsAfterUnpersistedClear(t *testing.T) {
	a, b := ncPlanFixture()
	pb, err := buildSchPlan(a, b)
	if err != nil {
		t.Fatal(err)
	}
	cfg, daemon, closeServer := newAutolayoutTestDaemon(t, func(_ int, c autolayoutTestCall) string {
		result := `{}`
		switch c.Action {
		case "document.current":
			result = `{"uuid":"d"}`
		case "schematic.pages.list":
			result = `{"pages":[{"uuid":"d","name":"d"}]}`
		case "pcb.documents.list":
			result = `{"pcbs":[]}`
		case "schematic.components.list":
			// doc switch now requires the target page's primitive inventory to
			// settle before reporting success.
			result = `{"count":1}`
		case "schematic.read":
			// The platform claims the mutation succeeded, but both pins remain NC.
			result = `{"components":[{"componentType":"part","designator":"U1","pins":[{"number":"1","net":"","noConnected":true},{"number":"2","net":"","noConnected":true}]}]}`
		case "schematic.pin.set_no_connect":
			result = `{"notApplied":[]}`
		}
		return `{"ok":true,"context":{"projectUuid":"p","documentUuid":"d","documentType":"schematic"},"result":` + result + `}`
	})
	defer closeServer()
	r := &applyRunner{cfg: cfg, pb: pb, stdout: io.Discard, stderr: io.Discard, window: "w1", journalPath: filepath.Join(t.TempDir(), "journal.jsonl"), toIdx: len(pb.Steps) - 1, vars: map[string]string{}}
	if err := r.execute(); err == nil {
		t.Fatal("unpersisted NC clear passed its intermediate guard")
	}
	reads, clears := 0, 0
	for _, call := range daemon.snapshot() {
		switch call.Action {
		case "schematic.read":
			reads++
		case "schematic.pin.set_no_connect":
			clears++
		case "schematic.power.connect_pin", "schematic.save":
			t.Fatalf("continued writing after failed intermediate guard: %s", call.Action)
		}
	}
	if reads != 2 || clears != 1 {
		t.Fatalf("test must reach the NC mutation and stop at next guard: reads=%d clears=%d", reads, clears)
	}
}

func TestPlanMarkerKindsAlsoPassDesignDiff(t *testing.T) {
	for _, kind := range []string{"power", "ground", "net_port_in", "net_port_out", "net_port_bi"} {
		t.Run(kind, func(t *testing.T) {
			a, b := ncPlanFixture()
			b.Connections[0].Kind = kind
			b.Components[0].Device = connectivity.Device{UUID: strings.Repeat("a", 32), LibraryUUID: "library"}
			a.Components[0].Device = b.Components[0].Device
			if _, err := buildSchPlan(a, b); err != nil {
				t.Fatal(err)
			}
			if _, err := connectivity.CompareDesign(b, b); err != nil {
				t.Fatalf("plan and design-diff disagree on explicit marker %s: %v", kind, err)
			}
		})
	}
}

func TestAutoconnectOffsetCapValidatesBeforeDispatch(t *testing.T) {
	for _, cap := range []string{"0", "-1", "NaN", "+Inf", "5"} {
		var out, errs bytes.Buffer
		code := Run([]string{"sch", "autoconnect", "--pin", "U1:1", "--net", "GND", "--kind", "ground", "--offset-min", "10", "--offset-cap", cap}, &out, &errs)
		if code == 0 || !strings.Contains(errs.String(), "--offset-cap must") {
			t.Fatalf("invalid offset cap must fail before dispatch: %s: %d %s", cap, code, errs.String())
		}
	}
}
