package app

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
)

func TestLibLayoutPreservesExplicitUnconnectedThroughComposition(t *testing.T) {
	src := libLayoutFixture()
	src.Connectivity.Components[0].Pins[2].NoConnected = false
	src.Connectivity.Components[0].Pins[2].ConnectionState = "unconnected"
	before, _ := json.Marshal(src)
	out, err := planLibLayout(src)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planSchComposition(*out)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(src)
	if string(before) != string(after) {
		t.Fatal("source state was mutated")
	}
	for _, d := range []connectivity.Document{out.Connectivity, plan.Connectivity} {
		p := d.Components[0].Pins[2]
		if p.Number != "3" || p.NoConnected || p.ConnectionState != "unconnected" {
			t.Fatalf("lost explicit open state: %+v", p)
		}
		if !reflect.DeepEqual(d.Connections, src.Connectivity.Connections) {
			t.Fatal("electrical edges changed")
		}
		foundWarning := false
		for _, issue := range d.Issues {
			if issue.Code == "unconnected-pin" && issue.ComponentID == d.Components[0].ID && issue.PinNumber == "3" {
				foundWarning = true
			}
		}
		if !foundWarning {
			t.Fatal("open pin disappeared from electrical warnings")
		}
	}
	// The blank measured pin remains a physical obstacle; no wire/marker is
	// manufactured to make its connection state appear complete.
	core := out.Modules[0].Placements[0]
	if len(core.Pins) != 3 || core.Pins[2].Net != "" {
		t.Fatal("open physical pin lost or silently wired")
	}
}

func TestLibLayoutRejectsUnknownOrConflictingUnconnected(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*libLayoutSource)
	}{
		{"missing declaration", func(s *libLayoutSource) { s.Connectivity.Components[0].Pins[2].NoConnected = false }},
		{"unsupported declaration", func(s *libLayoutSource) {
			s.Connectivity.Components[0].Pins[2].NoConnected = false
			s.Connectivity.Components[0].Pins[2].ConnectionState = "unknown"
		}},
		{"NC conflict", func(s *libLayoutSource) { s.Connectivity.Components[0].Pins[2].ConnectionState = "unconnected" }},
		{"net conflict", func(s *libLayoutSource) { s.Connectivity.Components[0].Pins[0].ConnectionState = "unconnected" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := libLayoutFixture()
			tc.edit(&src)
			if out, err := planLibLayout(src); err == nil || out != nil {
				t.Fatalf("accepted unknown or contradictory pin state: %+v %v", out, err)
			}
		})
	}
}
