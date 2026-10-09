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
