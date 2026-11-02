package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	if ri.Completed == nil || *ri.Completed != 97 {
		t.Errorf("Completed = %v", ri.Completed)
	}
	if ri.Unrouted == nil || *ri.Unrouted != 3 {
		t.Errorf("Unrouted = %v", ri.Unrouted)
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
	if ri.Completed != nil || ri.Unrouted != nil || !ri.Partial {
		t.Errorf("%+v", ri)
	}
}

// Top-level counts appear only when known: never completed:0, unrouted:-1, or
// everything completed because a partial result last counted 0.
func TestRouteInstrumentationTopLevelJSON(t *testing.T) {
	for _, c := range []struct {
		name  string
		res   *routeResult
		conns int
		want  []string
		not   []string
	}{
		{"complete", &routeResult{}, 50, []string{`"connections":50`, `"completed":50`, `"unrouted":0`}, []string{`"partial"`}},
		{"unrouted", &routeResult{UnroutedCount: 3}, 50, []string{`"completed":47`, `"unrouted":3`}, nil},
		{"partial, unknown", &routeResult{UnroutedCount: -1, Partial: true}, 50, []string{`"connections":50`, `"partial":true`}, []string{`"completed"`, `"unrouted"`}},
		{"partial at 0", &routeResult{Partial: true}, 50, []string{`"partial":true`}, []string{`"completed"`, `"unrouted"`}},
		{"partial with a count", &routeResult{UnroutedCount: 4, Partial: true}, 50, []string{`"partial":true`}, []string{`"completed"`, `"unrouted"`}},
		{"unknown without the flag", &routeResult{UnroutedCount: -1}, 50, nil, []string{`"completed"`, `"unrouted"`}},
		{"more unrouted than connections", &routeResult{UnroutedCount: 60}, 50, []string{`"unrouted":60`}, []string{`"completed"`}},
	} {
		b, err := json.Marshal(buildRouteInstrumentation(c.res, c.conns, "", nil, nil))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range c.want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s: no %s in %s", c.name, w, b)
			}
		}
		for _, n := range c.not {
			if strings.Contains(string(b), n) {
				t.Errorf("%s: %s in %s", c.name, n, b)
			}
		}
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
	stats := netClassBreakdown(res, classes, nil)
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
				{Number: "1", X: 25, Y: 75}, // NW
				{Number: "2", X: 75, Y: 75}, // NE
				{Number: "3", X: 25, Y: 25}, // SW
				{Number: "4", X: 75, Y: 25}, // SE
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

// netSnap has pads[net] pads on each net.
func netSnap(pads map[string]int) *boardSnapshot {
	c := boardComp{Designator: "U1"}
	for net, n := range pads {
		for range n {
			c.Pads = append(c.Pads, boardPad{Number: "x", Net: net})
		}
	}
	return &boardSnapshot{Components: []boardComp{c}}
}

// One unrouted connection in a class with 100 routed is 1 of 101, not 1 of 1;
// a class with nothing unrouted is still listed.
func TestInstrumentationBreakdownTotals(t *testing.T) {
	res := &routeResult{UnroutedCount: 1, Unrouted: []routeConn{{Net: "BIG", From: "U1.1", To: "U1.2"}}}
	classes := []kicad.NetClass{{Name: "PP1", Nets: []string{"BIG"}}, {Name: "PPD_HV", Nets: []string{"HV"}}}
	snap := netSnap(map[string]int{"BIG": 102, "HV": 3, "OTHER": 2, "LONE": 1})
	ri := buildRouteInstrumentation(res, boardConnections(snap), "", classes, snap)
	if ri.Connections != 104 || ri.Completed == nil || *ri.Completed != 103 {
		t.Fatalf("%+v", ri)
	}
	b, err := json.Marshal(ri)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`{"name":"PP1","total":101,"completed":100,"unrouted":1}`,
		`{"name":"PPD_HV","total":2,"completed":2,"unrouted":0}`,
		`{"name":"Default","total":1,"completed":1,"unrouted":0}`,
		`{"domain":"HV","total":2,"completed":2,"unrouted":0}`,
		`{"domain":"(none)","total":102,"completed":101,"unrouted":1}`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in %s", want, b)
		}
	}
}

// What is not known is absent, never a made-up number.
func TestInstrumentationBreakdownUnknownTotalsOmitted(t *testing.T) {
	classes := []kicad.NetClass{{Name: "PP1", Nets: []string{"BIG"}}, {Name: "PPD_HV", Nets: []string{"HV"}}}
	snap := netSnap(map[string]int{"BIG": 102, "HV": 3})
	listed := []routeConn{{Net: "BIG", From: "U1.1", To: "U1.2"}}
	for name, c := range map[string]struct {
		res  *routeResult
		snap *boardSnapshot
	}{
		"no snapshot":     {&routeResult{UnroutedCount: 1, Unrouted: listed}, nil},
		"partial":         {&routeResult{UnroutedCount: -1, Partial: true}, snap},
		"partial, listed": {&routeResult{UnroutedCount: 1, Partial: true, Unrouted: listed}, snap},
		"list incomplete": {&routeResult{UnroutedCount: 5, Unrouted: listed}, snap},
	} {
		b, err := json.Marshal(struct {
			C []NetClassStats
			D []DomainStats
		}{netClassBreakdown(c.res, classes, c.snap), domainBreakdown(c.res, classes, c.snap)})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "total") || strings.Contains(string(b), "completed") {
			t.Errorf("%s: %s", name, b)
		}
	}
	// Regions: a routed connection has no region, so no totals at all.
	b, err := json.Marshal(RegionStats{Region: "NW", Unrouted: 1})
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"region":"NW","unrouted":1}` {
		t.Errorf("%s", b)
	}
}

// The stage list of a real run: router time is the routing stage, not the
// import that follows it.
func TestInstrumentationTimingRollupRouterStages(t *testing.T) {
	roll := func(stages ...stageTime) (router, post float64) {
		m := timingRollup(stages, 2751)
		if m["totalSeconds"] != 2751.0 {
			t.Fatalf("%v", m)
		}
		return m["routerSeconds"].(float64), m["postProcessingSeconds"].(float64)
	}
	pre := []stageTime{{"setup (KiCad, fastroute, schematic netlist)", 5}, {"DSN export", 7}, {"DSN prepare (requirements, keep-outs)", 3}}
	post := []stageTime{{"via arrays", 10}, {"pours + zone fill + thermals", 800.5}, {"gates", 40}}
	for _, c := range []struct {
		name   string
		stages []stageTime
		router float64
		post   float64
	}{
		{"fastroute", []stageTime{{"fastroute (route)", 1819.6}, {"router fastroute (committed)", 34}}, 1819.6, 884.5},
		{"tracemaker", []stageTime{{"tracemaker (route)", 600.2}, {"router tracemaker (committed)", 12}}, 600.2, 862.5},
		{"race", []stageTime{{"routers (both)", 900}, {"router fastroute (committed)", 34}}, 900, 884.5},
		{"race, then a rerun", []stageTime{{"routers (both)", 900}, {"tracemaker (route)", 100.5}, {"router tracemaker (committed)", 12}}, 1000.5, 862.5},
	} {
		stages := append(append(append([]stageTime{}, pre...), c.stages...), post...)
		if r, p := roll(stages...); r != c.router || p != c.post {
			t.Errorf("%s: router %v post %v", c.name, r, p)
		}
	}
}

// A candidate run again is timed as a router stage of its own.
func TestRerunCandidateTimedAsRouterStage(t *testing.T) {
	r := &kicadRun{summary: map[string]any{}, stderr: io.Discard}
	c := &routeCandidate{Name: routerTracemaker, exec: func(context.Context) (*routeResult, error) {
		return &routeResult{}, nil
	}}
	if err := r.rerunCandidate(c); err != nil {
		t.Fatal(err)
	}
	if len(r.times) != 1 || !routerStage(r.times[0].Stage) || r.times[0].Stage != "tracemaker (route)" {
		t.Fatalf("%+v", r.times)
	}
}
