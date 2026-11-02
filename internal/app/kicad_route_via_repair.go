package app

// kicad_route_via_repair.go — growing a via to its class via, only as an
// explicit repair: per via, in one transaction, followed by a geometry check
// before the write and KiCad DRC after it. A via that fails keeps its size
// and carries its evidence. Nothing is ripped up and nothing loops: at most
// one retry without the vias DRC rejected, otherwise the board is untouched.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

type viaGrow struct {
	v               routeVia
	classDia, drill int64 // nm
}

// viaRepairResult: grown via ids; evidence for every via left as it was; info
// for every via that was grown.
type viaRepairResult struct {
	grown    []string
	evidence []string
	info     []string
}

// viaGrowCandidates: the router vias shrinking cannot bring to their class via.
func viaGrowCandidates(vias []routeVia, old map[string]bool, eff func(string) (int64, int64, bool)) []viaGrow {
	var out []viaGrow
	for _, v := range vias {
		if old[v.ID] {
			continue
		}
		cd, ch, ok := eff(v.Net)
		if !ok || cd <= 0 || ch <= 0 || ch >= cd {
			continue
		}
		if viaNotReachable(int64(math.Round(v.Dia*nmPerMil)), int64(math.Round(v.Drill*nmPerMil)), cd, ch) {
			out = append(out, viaGrow{v: v, classDia: cd, drill: ch})
		}
	}
	return out
}

func (g viaGrow) describe() string {
	return fmt.Sprintf("%s via %s: router via %.2f/%.2f mil cannot be shrunk to the class via %.2f/%.2f mil", g.v.Net, short12(g.v.ID), g.v.Dia, g.v.Drill, float64(g.classDia)/nmPerMil, float64(g.drill)/nmPerMil)
}

// viaGrowConflict is why a grown via would break clearance on the board, ""
// when nothing does; cannotCheck says a rule or a size to judge it is missing.
// Pads are taken at their circumscribed circle: conservative, DRC has the rest.
// grownAt holds the diameters the other candidates will have.
func viaGrowConflict(g viaGrow, tracks []specctra.Track, vias []widenVia, pads []boardPad, rb *kicad.RulesReadback, grownAt map[[2]float64]float64) (why string, cannotCheck bool) {
	own, ok := rb.Nets[g.v.Net]
	if !ok || own.Clearance <= 0 {
		return fmt.Sprintf("no clearance rule resolved for net %s", g.v.Net), true
	}
	need := func(net string) float64 {
		c := own.Clearance
		if e, ok := rb.Nets[net]; ok && e.Clearance > c {
			c = e.Clearance
		}
		if rb.Minimums.Clearance > c {
			c = rb.Minimums.Clearance
		}
		return float64(c) / nmPerMil
	}
	r := float64(g.classDia) / nmPerMil / 2
	for _, t := range tracks {
		if t.Net == g.v.Net {
			continue
		}
		if gap := pointSegDist(t.X1, t.Y1, t.X2, t.Y2, g.v.X, g.v.Y) - t.Width/2 - r; gap < need(t.Net) {
			return fmt.Sprintf("%.2f mil to a track of %s on layer %d, needs %.2f", gap, t.Net, t.Layer, need(t.Net)), false
		}
	}
	for _, v := range vias {
		if v.Net == g.v.Net {
			continue
		}
		d := v.Diameter
		if o, ok := grownAt[[2]float64{v.X, v.Y}]; ok {
			d = o
		}
		if gap := math.Hypot(v.X-g.v.X, v.Y-g.v.Y) - d/2 - r; gap < need(v.Net) {
			return fmt.Sprintf("%.2f mil to a via of %s, needs %.2f", gap, v.Net, need(v.Net)), false
		}
	}
	for _, p := range pads {
		if p.Net == g.v.Net {
			continue
		}
		if p.W <= 0 || p.H <= 0 {
			if math.Hypot(p.X-g.v.X, p.Y-g.v.Y) < 200 {
				return fmt.Sprintf("pad %s of net %s nearby has no size", p.Number, p.Net), true
			}
			continue
		}
		if gap := math.Hypot(p.X-g.v.X, p.Y-g.v.Y) - math.Hypot(p.W, p.H)/2 - r; gap < need(p.Net) {
			return fmt.Sprintf("%.2f mil to a pad of %s (circumscribed), needs %.2f", gap, p.Net, need(p.Net)), false
		}
	}
	return "", false
}

// repairRouterVias grows the candidates that pass the geometry check, runs DRC
// on the result, and retries once without the vias DRC names. It returns the
// board the result describes: the input board unless a transaction passed.
func repairRouterVias(t routerViaTools, board string, cands []viaGrow, rb *kicad.RulesReadback, next func(string) string, stats map[string]any) (string, viaRepairResult) {
	var res viaRepairResult
	reject := func(g viaGrow, why string) {
		res.evidence = append(res.evidence, g.describe()+"; repair rejected: "+why)
	}
	allReject := func(gs []viaGrow, why string) {
		for _, g := range gs {
			reject(g, why)
		}
	}
	defer func() {
		sort.Strings(res.evidence)
		stats["grownToClass"], stats["repairRejected"] = len(res.grown), len(res.evidence)
	}()
	raw, err := t.Snapshot(board)
	if err != nil {
		allReject(cands, "cannot check: board snapshot: "+err.Error())
		return board, res
	}
	var s boardSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		allReject(cands, "cannot check: decode the board snapshot: "+err.Error())
		return board, res
	}
	tracks, vias, pads, err := copperOf(&s)
	if err != nil {
		allReject(cands, "cannot check: board copper: "+err.Error())
		return board, res
	}
	grownAt := map[[2]float64]float64{}
	for _, g := range cands {
		grownAt[[2]float64{g.v.X, g.v.Y}] = float64(g.classDia) / nmPerMil
	}
	var pass []viaGrow
	for _, g := range cands {
		if why, unknown := viaGrowConflict(g, tracks, vias, pads, rb, grownAt); why != "" {
			if unknown {
				why = "cannot check: " + why
			}
			reject(g, why)
			continue
		}
		pass = append(pass, g)
	}
	// One transaction, one retry: each writes a new board from the input.
	for try := 0; try < 2 && len(pass) > 0; try++ {
		out := next("vias-repair")
		ops := make([]map[string]any, 0, len(pass))
		for _, g := range pass {
			ops = append(ops, map[string]any{"id": g.v.ID, "diameter": float64(g.classDia) / nmPerMil, "drill": float64(g.drill) / nmPerMil})
		}
		if _, err := t.Edit(board, map[string]any{"setVia": ops}, out); err != nil {
			allReject(pass, "the resize could not be written: "+err.Error())
			return board, res
		}
		rep, err := t.DRC(out, out+".drc.json")
		if err != nil || rep == nil {
			allReject(pass, fmt.Sprintf("cannot check: DRC of the repaired board: %v", err))
			return board, res
		}
		bad := viasNamedByDRC(rep, pass)
		if len(bad) == 0 {
			for _, g := range pass {
				res.grown = append(res.grown, g.v.ID)
				res.info = append(res.info, fmt.Sprintf("%s via %s grown to its class via %.2f/%.2f mil", g.v.Net, short12(g.v.ID), float64(g.classDia)/nmPerMil, float64(g.drill)/nmPerMil))
			}
			return out, res
		}
		var keep []viaGrow
		for _, g := range pass {
			if why, ok := bad[g.v.ID]; ok {
				reject(g, "DRC: "+why)
			} else {
				keep = append(keep, g)
			}
		}
		pass = keep
	}
	allReject(pass, "DRC of the retried transaction still names a grown via: transaction dropped")
	return board, res
}

// viasNamedByDRC: the candidates a design-rule violation names (rule: message).
func viasNamedByDRC(rep *kicad.DRCReport, gs []viaGrow) map[string]string {
	ids := map[string]bool{}
	for _, g := range gs {
		ids[g.v.ID] = true
	}
	out := map[string]string{}
	for _, v := range rep.Violations {
		if strings.HasPrefix(v.Rule, "unconnected") || v.Section == "unconnected_items" {
			continue
		}
		for _, o := range v.Objs {
			if ids[o] {
				out[o] = v.Rule + ": " + v.Message
			}
		}
	}
	return out
}
