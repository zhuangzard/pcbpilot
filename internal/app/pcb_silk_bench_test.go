package app

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// PCBPILOT_SILK_BENCH=a.json,b.json runs the silk planner on real board
// snapshots (kicad snapshot / pcb dump) and logs time and unresolved labels.
func TestSilkPlannerBench(t *testing.T) {
	env := os.Getenv("PCBPILOT_SILK_BENCH")
	if env == "" {
		t.Skip("set PCBPILOT_SILK_BENCH to board snapshots")
	}
	for _, f := range strings.Split(env, ",") {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var s boardSnapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			t.Fatal(err)
		}
		s.sanitizeOutline()
		opt := defaultSilkTightOpts()
		start := time.Now()
		labels, sc, _ := silkTightInput(&s, opt)
		placed, _ := planSilkTight(labels, sc, opt)
		tPlan := time.Since(start)
		un := 0
		for _, p := range placed {
			if p.How == "unresolved" {
				un++
			}
		}
		if os.Getenv("PCBPILOT_SILK_BENCH_V") != "" {
			layerOf := map[string]int{}
			for _, l := range labels {
				layerOf[l.ID] = l.Layer
			}
			c := &silkSlotCache{sc: sc, layerOf: layerOf, opt: opt, m: map[string][]silkSlot{}}
			for _, p := range placed {
				if p.How != "unresolved" {
					continue
				}
				for _, l := range labels {
					if l.ID == p.ID {
						k := opt.MinFont / l.Target
						sl := l
						sl.Len, sl.Hgt = l.Len*k, l.Hgt*k
						t.Logf("  %s own %.0fx%.0f label %.0fx%.0f: %d static slot(s), %d at %.1f mil", l.Ref, l.Own.w(), l.Own.h(), l.Len, l.Hgt, len(c.of(l)), len(c.of(sl)), opt.MinFont)
					}
				}
			}
		}
		groups, placed2, gnotes := planSilkGroups(labels, sc, placed, opt)
		if os.Getenv("PCBPILOT_SILK_BENCH_V") != "" {
			for _, n := range gnotes {
				t.Log("  group:", n)
			}
		}
		un2, hidden, shrunk := 0, 0, 0
		for _, p := range placed2 {
			if p.How == "unresolved" {
				un2++
			}
			if p.Font > 0 {
				shrunk++
			}
		}
		for _, g := range groups {
			hidden += len(g.IDs)
		}
		t.Logf("%s: %d labels, plan %.2fs, unresolved %d before groups; %d group(s) hiding %d, %d still unresolved, %d shrunk (total %.2fs)",
			f, len(labels), tPlan.Seconds(), un, len(groups), hidden, un2, shrunk, time.Since(start).Seconds())
	}
}
