package app

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// The gates read only routeResult: a backend other than fastroute (one that
// writes the board directly, no SES, no fastroute report) is judged the same.
func TestRouteResultSeam(t *testing.T) {
	r := &routeResult{Router: "other", Version: "1.0", Board: "routed.kicad_pcb", UnroutedCount: 1,
		Unrouted: []routeConn{{Net: "GND", From: "C2.2", To: "U8.115"}}, Blocked: []routeConn{{Net: "GND", From: "C2.2", To: "U8.115"}}}
	g := routeCompleteGate(r)
	if g.Pass || !strings.Contains(g.Detail, "other result: 1 unrouted") || !strings.Contains(strings.Join(g.Items, "\n"), "blocked by geometry (move the part or add an escape; rerouting cannot fix it): GND C2.2–U8.115") {
		t.Fatalf("gate %+v", g)
	}
	// KiCad: the unrouted connection on a poured net with 0 unconnected items passes.
	snap := &boardSnapshot{Copper: &boardCopperSnapshot{Poured: []any{map[string]any{"net": "GND"}}}}
	kg := kicadRouteCompleteGate(r, &kicad.DRCReport{Counts: map[string]int{}}, snap)
	if !kg.Pass || !strings.Contains(strings.Join(kg.Info, ""), "GND C2.2–U8.115: unrouted by other, joined by the GND pour") {
		t.Fatalf("kicad gate %+v", kg)
	}
	// The summary keeps it as JSON (summary.json round trip).
	b, _ := json.Marshal(map[string]any{"routeResult": r})
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	if got := decodeRouteResult(m["routeResult"]); got == nil || got.Router != "other" || len(got.Blocked) != 1 {
		t.Fatalf("decoded %+v", got)
	}
}

func TestRouteCompleteBelowMinWidthFails(t *testing.T) {
	r := &routeResult{Router: "tracemaker", BelowMinWidthNets: 2}
	g := routeCompleteGate(r)
	if g.Pass || !strings.Contains(g.Detail, "below the minimum width") {
		t.Fatalf("gate %+v", g)
	}
}

// Pour credit: each of the four preconditions, and the DRC backstop.
func TestKicadRouteCompletePourCreditPreconditions(t *testing.T) {
	poured := &boardSnapshot{Copper: &boardCopperSnapshot{Poured: []any{map[string]any{"net": "GND"}}}}
	clean := &kicad.DRCReport{Counts: map[string]int{}}
	mk := func() *routeResult {
		return &routeResult{Router: "tracemaker", UnroutedCount: 1, Unrouted: []routeConn{{Net: "GND", From: "C2.2", To: "U8.115"}}}
	}
	if g := kicadRouteCompleteGate(mk(), clean, poured); !g.Pass || len(g.Info) != 1 || !strings.Contains(g.Info[0], "POUR-CREDITED") || !strings.Contains(g.Detail, "credited to a pour") {
		t.Fatalf("base case must pass and say so: %+v", g)
	}
	// 1. fixable violations
	r := mk()
	r.Fixable = 1
	if g := kicadRouteCompleteGate(r, clean, poured); g.Pass {
		t.Fatalf("Fixable != 0 credited: %+v", g)
	}
	// 1b. a track below the minimum width
	r = mk()
	r.BelowMinWidthNets = 1
	if g := kicadRouteCompleteGate(r, clean, poured); g.Pass {
		t.Fatalf("below-min-width credited: %+v", g)
	}
	// 2. counters that do not agree with the list
	r = mk()
	r.UnroutedCount = 3
	if g := kicadRouteCompleteGate(r, clean, poured); g.Pass {
		t.Fatalf("inconsistent counts credited: %+v", g)
	}
	// 3. an unrouted connection whose net is not poured
	r = mk()
	r.Unrouted = append(r.Unrouted, routeConn{Net: "+3V3", From: "U1.1", To: "C1.1"})
	r.UnroutedCount = 2
	if g := kicadRouteCompleteGate(r, clean, poured); g.Pass {
		t.Fatalf("unpoured net credited: %+v", g)
	}
	// 4. KiCad DRC reports unconnected items; and no DRC report at all
	if g := kicadRouteCompleteGate(mk(), &kicad.DRCReport{Counts: map[string]int{"unconnected_items": 1}}, poured); g.Pass {
		t.Fatalf("unconnected_items credited: %+v", g)
	}
	if g := kicadRouteCompleteGate(mk(), nil, poured); g.Pass {
		t.Fatalf("credited without a DRC report: %+v", g)
	}
	if g := kicadRouteCompleteGate(mk(), clean, &boardSnapshot{}); g.Pass {
		t.Fatalf("credited without copper data: %+v", g)
	}
	// Independent backstop: the router says all routed, KiCad disagrees.
	all := &routeResult{Router: "tracemaker"}
	if g := kicadRouteCompleteGate(all, &kicad.DRCReport{Counts: map[string]int{"unconnected_items": 2}}, poured); g.Pass || !strings.Contains(g.Detail, "unconnected") {
		t.Fatalf("router-claimed-complete with unconnected items passed: %+v", g)
	}
	if g := kicadRouteCompleteGate(all, clean, poured); !g.Pass {
		t.Fatalf("complete and clean must pass: %+v", g)
	}
}

func TestTracemakerReportConsistency(t *testing.T) {
	type u = struct {
		Net     string `json:"net"`
		From    string `json:"from"`
		To      string `json:"to"`
		Blocked bool   `json:"blocked"`
		Reason  string `json:"reason"`
	}
	cases := []struct {
		name string
		r    tmReport
		ok   bool
	}{
		{"all routed", tmReport{Connections: 10, Routed: 10}, true},
		{"routed==connections but blocked>0", tmReport{Connections: 10, Routed: 10, Blocked: 1}, false},
		{"routed==connections but unrouted listed", tmReport{Connections: 10, Routed: 10, Unrouted: []u{{Net: "A"}}}, false},
		{"routed>connections", tmReport{Connections: 10, Routed: 11}, false},
		{"blocked without list", tmReport{Connections: 10, Routed: 9, Blocked: 1}, false},
		{"more unrouted entries than open connections", tmReport{Connections: 10, Routed: 9, Unrouted: []u{{Net: "A"}, {Net: "B"}}}, false},
		{"one open, blocked, listed", tmReport{Connections: 10, Routed: 9, Blocked: 1, Unrouted: []u{{Net: "A", Blocked: true}}}, true},
		{"negative", tmReport{Connections: 10, Routed: -1}, false},
	}
	for _, c := range cases {
		if err := c.r.consistent(); (err == nil) != c.ok {
			t.Errorf("%s: err=%v, want ok=%v", c.name, err, c.ok)
		}
	}
	// open connections the report counts but does not list still count
	res := tracemakerResult(&tmReport{Connections: 10, Routed: 7, Unrouted: []u{{Net: "A"}}, TracksBelowMinWidthNets: 1}, nil, "", "", "b", "r")
	if res.UnroutedCount != 3 || res.BelowMinWidthNets != 1 {
		t.Fatalf("result %+v", res)
	}
}
