package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func TestBuildRouteInstrumentationNilResult(t *testing.T) {
	if ri := buildRouteInstrumentation(nil, 100, "", nil, nil); ri != nil {
		t.Fatal("expected nil for nil result")
	}
}

func TestBuildRouteInstrumentationBasic(t *testing.T) {
	res := &routeResult{
		Router:        "fastroute",
		Version:       "0.1.14",
		UnroutedCount: 3,
		Seconds:       45.2,
		Unrouted: []routeConn{
			{Net: "GND", From: "U1.2", To: "C1.1"},
			{Net: "VCC", From: "U1.1", To: "R1.2"},
			{Net: "GND", From: "U1.3", To: "C2.1"},
		},
	}
	classes := []kicad.NetClass{
		{Name: "PP1_W10", Nets: []string{"GND"}},
		{Name: "PP2_W20", Nets: []string{"VCC"}},
	}
	ri := buildRouteInstrumentation(res, 100, "", classes, nil)
	if ri == nil {
		t.Fatal("expected non-nil")
	}
	if ri.Connections != 100 {
		t.Errorf("Connections = %d", ri.Connections)
	}
	if ri.Completed != 97 {
		t.Errorf("Completed = %d", ri.Completed)
	}
	if ri.Unrouted != 3 {
		t.Errorf("Unrouted = %d", ri.Unrouted)
	}
	if ri.RouterVersion != "0.1.14" {
		t.Errorf("RouterVersion = %q", ri.RouterVersion)
	}
	if ri.RouterWallTimeS != 45.2 {
		t.Errorf("RouterWallTimeS = %v", ri.RouterWallTimeS)
	}
}

func TestBuildRouteInstrumentationPartial(t *testing.T) {
	// Partial: UnroutedCount = -1 (unknown).
	res := &routeResult{
		Router:        "fastroute",
		UnroutedCount: -1,
		Partial:       true,
		Seconds:       120,
	}
	ri := buildRouteInstrumentation(res, 50, "", nil, nil)
	if ri.Completed != 0 {
		t.Errorf("Completed should be 0 for unknown unrouted, got %d", ri.Completed)
	}
}

func TestNetClassBreakdown(t *testing.T) {
	res := &routeResult{
		Unrouted: []routeConn{
			{Net: "GND", From: "U1.2", To: "C1.1"},
			{Net: "GND", From: "U1.3", To: "C2.1"},
			{Net: "VCC", From: "U1.1", To: "R1.2"},
			{Net: "SIG", From: "U1.4", To: "R2.1"},
		},
	}
	classes := []kicad.NetClass{
		{Name: "PP1_W10", Nets: []string{"GND"}},
		{Name: "PP2_W20", Nets: []string{"VCC"}},
	}
	stats := netClassBreakdown(res, classes)
	if len(stats) == 0 {
		t.Fatal("expected net class stats")
	}
	byName := map[string]NetClassStats{}
	for _, s := range stats {
		byName[s.Name] = s
	}
	if s, ok := byName["PP1_W10"]; !ok || s.Unrouted != 2 {
		t.Errorf("PP1_W10: %+v", byName["PP1_W10"])
	}
	if s, ok := byName["PP2_W20"]; !ok || s.Unrouted != 1 {
		t.Errorf("PP2_W20: %+v", byName["PP2_W20"])
	}
	if s, ok := byName["Default"]; !ok || s.Unrouted != 1 {
		t.Errorf("Default: %+v", byName["Default"])
	}
}

func TestRegionBreakdown(t *testing.T) {
	snap := &boardSnapshot{
		Outline: &boardOutline{BBox: layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}},
		Components: []boardComp{
			{Designator: "U1", Pads: []boardPad{
				{Number: "1", X: 25, Y: 75},  // NW
				{Number: "2", X: 75, Y: 75},  // NE
				{Number: "3", X: 25, Y: 25},  // SW
				{Number: "4", X: 75, Y: 25},  // SE
			}},
		},
	}
	res := &routeResult{
		Unrouted: []routeConn{
			{Net: "A", From: "U1.1", To: "U1.2"}, // from is NW
			{Net: "B", From: "U1.3", To: "U1.4"}, // from is SW
			{Net: "C", From: "U1.4", To: "U1.1"}, // from is SE
		},
	}
	stats := regionBreakdown(res, snap)
	byRegion := map[string]RegionStats{}
	for _, s := range stats {
		byRegion[s.Region] = s
	}
	if s := byRegion["NW"]; s.Unrouted != 1 {
		t.Errorf("NW unrouted = %d, want 1", s.Unrouted)
	}
	if s := byRegion["SW"]; s.Unrouted != 1 {
		t.Errorf("SW unrouted = %d, want 1", s.Unrouted)
	}
	if s := byRegion["SE"]; s.Unrouted != 1 {
		t.Errorf("SE unrouted = %d, want 1", s.Unrouted)
	}
}

func TestFileSHA256(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "test.dsn")
	if err := os.WriteFile(p, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := filePathSHA256(p)
	if got == "" {
		t.Fatal("expected non-empty SHA")
	}
	// Same content → same SHA.
	p2 := filepath.Join(dir, "test2.dsn")
	if err := os.WriteFile(p2, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if filePathSHA256(p2) != got {
		t.Error("same content should produce same SHA")
	}
	// Missing file → empty.
	if filePathSHA256(filepath.Join(dir, "nope")) != "" {
		t.Error("missing file should return empty SHA")
	}
}
