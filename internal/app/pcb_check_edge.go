package app

// pcb_check_edge.go — `pcb check` copper-to-edge rule (板边安全距离).
//
// Measures every piece of copper against the board outline and the metal
// mounting holes: tracks and arcs, vias (all layers), pads, the
// MATERIALIZED poured copper (exact complex polygons, arcs flattened — the
// copper EasyEDA actually flooded, not the pour boundary) and net fills.
// An inner layer the dump carries no copper for is a negative plane: the
// host draws it to the Board Outline ↔ Copper/Plane Zone rule, so that rule
// value is its pull-back. Thresholds come from intent.json "edge" (else the
// defaults: 20 mil outer, 30 mil inner, V-cut 0.5/0.8 mm) and, per insulated
// domain, its clearance/creepage to an accessible surface. ERROR below the
// requirement. The geometry lives in pkg/pcbauto (CheckEdgeSnapshot) so pcb
// auto, pcb check and the design report measure the same way.

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// loadEdgeIntent reads an intent.json for the edge policy (nil path → nil).
func loadEdgeIntent(path string) (*pcbauto.Intent, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return pcbauto.ParseIntent(raw)
}

// addEdgeFindings runs the copper-to-edge rule on a board dump.
func addEdgeFindings(rep *pcbCheckReport, dump []byte, in *pcbauto.Intent, edgeKind string) error {
	chk, err := pcbauto.CheckEdgeSnapshot(dump, in, edgeKind)
	if err != nil {
		return fmt.Errorf("copper-to-edge: %w", err)
	}
	rep.Edge = chk
	pol := chk.Policy
	rep.Limitations = append(rep.Limitations, fmt.Sprintf("copper-to-edge (%s, %s edge): outer ≥ %.1f mil, inner ≥ %.1f mil (fab floor %.1f mil, live Board Outline rule %.1f mil)",
		pol.Source, pol.Kind, pol.LayerReq(pcbauto.LayerTop), pol.LayerReq(pcbauto.LayerInner1), pol.FabMinMil, pol.RuleMil))
	for id, d := range pol.ByDomain {
		rep.Limitations = append(rep.Limitations, fmt.Sprintf("copper-to-edge domain %s: ≥ %.1f mil to the edge and to metal mounting holes (%s insulation; engineering reference — confirm with the certification lab)", id, d.Mil, d.Insulation))
	}
	for _, l := range chk.Layers {
		if l.Measured {
			rep.Limitations = append(rep.Limitations, fmt.Sprintf("copper-to-edge layer %d (%s): min %.2f mil (%s %s), required %.1f mil", l.Layer, l.Class, l.MinMil, l.Kind, l.Net, l.RequiredMil))
		}
	}
	rep.Limitations = append(rep.Limitations, chk.Notes...)
	for _, f := range chk.Findings {
		pf := pcbCheckFinding{Type: f.Rule, Level: f.Level, Net: f.Net, Layer: f.Layer, Message: f.Message, At: &pcbXY{X: f.At.X, Y: f.At.Y}}
		if f.Item != "" {
			pf.Primitives = []string{f.Item}
		}
		rep.Findings = append(rep.Findings, pf)
		rep.Summary.CopperToEdge++
		switch f.Level {
		case "ERROR":
			rep.Summary.Errors++
			rep.Summary.Warnings++
		default:
			rep.Summary.Warnings++
		}
		rep.Summary.Total++
	}
	rep.Passed = rep.Summary.Total == 0
	return nil
}

// liveEdgeDump reads the board as `pcb dump --include-copper` would.
func liveEdgeDump(cfg *appConfig, window string) ([]byte, error) {
	snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{withRules: true, withLayers: true, withCopper: true, withFootprintHoles: true})
	if err != nil {
		return nil, err
	}
	return json.Marshal(snap)
}
