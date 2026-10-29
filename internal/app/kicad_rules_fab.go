package app

// kicad_rules_fab.go — what kicad rules checks against manufacturing:
//
//   - fab-contract (--contract, the order contract kicad fab --preflight
//     reads): the board's design-rule minimums (track, clearance, through
//     hole, via annular ring, via diameter) are raised to the contract's
//     limits on the stage copy before the derivation, so every planned via
//     is floored at the contract annular ring too and --apply publishes
//     the raised minimums; what still allows less than the contract (a
//     user .kicad_dru rule, a user netclass) is listed. --check judges the
//     board's own rules against the contract.
//   - footprint-pad-clearance: the clearance KiCad will require between
//     two pads of one footprint (the larger of the two pads' clearances:
//     a pad / footprint local override, else the net's class clearance)
//     against the gap the footprint has between them. A 0.5 mm pitch part
//     has 0.15–0.2 mm gaps: a class clearance above that is a DRC error on
//     every instance that no placement fixes. Reported per footprint and
//     class with the fix; pcbpilot never lowers a clearance silently.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// contractRuleKeys: the .kicad_pro board.design_settings.rules keys a
// contract limit raises (mm).
type contractRaise struct {
	Key     string  `json:"key"`
	FromMm  float64 `json:"fromMm"`
	ToMm    float64 `json:"toMm"`
	Contact string  `json:"contract"`
}

// raiseProjectToContract raises the .kicad_pro design-rule minimums below
// the contract's limits (never lowers one). It returns the new bytes and
// what it raised.
func raiseProjectToContract(pro []byte, c *kicad.FabContract) ([]byte, []contractRaise, error) {
	dec := json.NewDecoder(bytes.NewReader(pro))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return nil, nil, fmt.Errorf(".kicad_pro: %w", err)
	}
	obj := func(parent map[string]any, k string) map[string]any {
		v, _ := parent[k].(map[string]any)
		if v == nil {
			v = map[string]any{}
			parent[k] = v
		}
		return v
	}
	rules := obj(obj(obj(m, "board"), "design_settings"), "rules")
	num := func(k string) float64 {
		switch v := rules[k].(type) {
		case json.Number:
			f, _ := v.Float64()
			return f
		case float64:
			return v
		}
		return 0
	}
	l := c.Limits
	want := []struct {
		key, from string
		mm        float64
	}{
		{"min_track_width", "limits.minTrackMm", l.MinTrackMm},
		{"min_clearance", "limits.minSpaceMm", l.MinSpaceMm},
		{"min_through_hole_diameter", "limits.minDrillMm", l.MinDrillMm},
		{"min_via_annular_width", "limits.minAnnularMm", l.MinAnnularMm},
	}
	if l.MinDrillMm > 0 && l.MinAnnularMm > 0 {
		want = append(want, struct {
			key, from string
			mm        float64
		}{"min_via_diameter", "limits.minDrillMm + 2 × limits.minAnnularMm", l.MinDrillMm + 2*l.MinAnnularMm})
	}
	if micro, _ := rules["allow_microvias"].(bool); micro && l.MinDrillMm > 0 {
		want = append(want, struct {
			key, from string
			mm        float64
		}{"min_microvia_drill", "limits.minDrillMm", l.MinDrillMm})
	}
	var raised []contractRaise
	for _, w := range want {
		w.mm = math.Round(w.mm*1e6) / 1e6
		if w.mm <= 0 {
			continue
		}
		if cur := num(w.key); cur+1e-9 < w.mm {
			rules[w.key] = json.Number(fmt.Sprintf("%g", w.mm))
			raised = append(raised, contractRaise{Key: w.key, FromMm: cur, ToMm: w.mm, Contact: w.from})
		}
	}
	if len(raised) == 0 {
		return pro, nil, nil
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	return append(b, '\n'), raised, nil
}

// applyContract raises the staged input project to the contract (plan /
// apply; --check leaves the board's rules as they are and judges them).
func (r *rulesRun) applyContract() int {
	if r.o.contract == "" {
		return 0
	}
	c, err := kicad.ParseFabContract(r.data[r.o.contract], r.o.contract)
	if err != nil {
		return r.fail(errRulesInput, r.o.contract, err.Error(), "fix the order contract (the one kicad fab --preflight --contract reads)", 1)
	}
	r.contract = c
	if r.o.check {
		// --check judges the board's own rules: an unraised copy.
		dir := filepath.Join(r.stage, "orig")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return r.fileErr(err)
		}
		r.stInOrig = filepath.Join(dir, filepath.Base(r.o.pcb))
		for _, f := range []string{r.o.pcb, r.pro, r.dru} {
			if b, ok := r.data[f]; ok && b != nil {
				if err := os.WriteFile(boardSibling(r.stInOrig, filepath.Ext(f)), b, 0o644); err != nil {
					return r.fileErr(err)
				}
			}
		}
	}
	stPro := boardSibling(r.stIn, ".kicad_pro")
	pro, err := os.ReadFile(stPro)
	if err != nil {
		r.contractNote = append(r.contractNote, "the board has no .kicad_pro: the contract minimums cannot be written as board rules")
		return 0
	}
	nb, raised, err := raiseProjectToContract(pro, c)
	if err != nil {
		return r.fail(errRulesInput, r.pro, err.Error(), "", 1)
	}
	r.contractRaised = raised
	if len(raised) > 0 {
		if err := os.WriteFile(stPro, nb, 0o644); err != nil {
			return r.fileErr(err)
		}
	}
	return 0
}

// contractCheck: the effective manufacturing minimums (the files kicad fab
// --preflight reads) against the contract. Plan / apply judge the staged
// result (after the raise), --check the board as it is.
func (r *rulesRun) contractCheck() {
	if r.o.contract == "" || r.contract == nil {
		return
	}
	chk := rulesCheck{Name: "fab-contract", Status: gate.Pass}
	for _, x := range r.contractRaised {
		chk.Info = append(chk.Info, fmt.Sprintf(".kicad_pro %s raised %g → %g mm (contract %s)", x.Key, x.FromMm, x.ToMm, x.Contact))
	}
	chk.Info = append(chk.Info, r.contractNote...)
	board, what := r.staged, "the planned rules"
	if r.o.check {
		board, what = r.stInOrig, "the board's rules"
	}
	eff, err := kicad.ReadEffectiveRules(board)
	if err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "effective rules: "+err.Error()
		r.rep.check(chk)
		return
	}
	for _, e := range eff.Items() {
		lim := r.contract.Limit(e.Item)
		if lim <= 0 || e.MinMm == nil || *e.MinMm+1e-9 >= lim {
			continue
		}
		var below []string
		for _, s := range e.Sources {
			if s.Mm+1e-9 < lim {
				below = append(below, fmt.Sprintf("%s %s = %g mm", s.Source, s.Where, s.Mm))
			}
		}
		chk.Items = append(chk.Items, fmt.Sprintf("%s: %s allow %g mm < contract %s %g mm (%s)", e.Item, what, *e.MinMm, ruleContractKey[e.Item], lim, strings.Join(below, "; ")))
	}
	switch {
	case len(chk.Items) == 0:
		chk.Detail = fmt.Sprintf("%s meet the contract limits (%s, kicad fab --preflight's min-track / min-space / min-drill / min-annular)", what, r.contract.Vendor)
		if len(r.contractRaised) > 0 {
			chk.Detail += fmt.Sprintf("; %d board minimum(s) raised to the contract", len(r.contractRaised))
		}
	case r.o.check:
		chk.Status = gate.Fail
		chk.Detail = fmt.Sprintf("%d item(s) below the contract: kicad fab --preflight will fail them (fix: kicad rules --apply --contract raises the board minimums; raise a user rule / class yourself — never lower the contract)", len(chk.Items))
	default:
		// The raise cannot touch user rules and classes: reported, the
		// apply still writes what it can.
		chk.Status, chk.Advisory = gate.Warn, true
		chk.Detail = fmt.Sprintf("%d item(s) still below the contract after the raise (a user .kicad_dru rule or netclass pcbpilot does not change): kicad fab --preflight will fail them (fix: raise them yourself; never lower the contract)", len(chk.Items))
	}
	r.rep.check(chk)
}

// ── footprint pad gaps ─────────────────────────────────────────────────────

// padClearance is the footprint-pad-clearance check.
func (r *rulesRun) padClearance() {
	chk := rulesCheck{Name: "footprint-pad-clearance", Status: gate.Pass, Advisory: true}
	rb := r.rbStaged
	what := "planned"
	if rb == nil || r.o.check {
		rb, what = r.rb0, "effective"
	}
	if rb == nil || r.snap == nil {
		chk.Status, chk.Advisory, chk.Detail = gate.CannotCheck, false, "no readback of the net clearances"
		r.rep.check(chk)
		return
	}
	// One line per footprint and required clearance: the smallest gap (with
	// its pads), the classes that require it and every part it occurs on.
	type group struct {
		gap     int64 // nm
		pads    string
		classes []string
		refs    []string
	}
	type gkey struct {
		fp   string
		need int64
	}
	groups := map[gkey]*group{}
	type thermalHit struct {
		ep, net string
		vias    int
		refs    []string
	}
	thermal := map[[2]string]*thermalHit{}
	unknown := map[[2]string][]string{} // footprint, pads → parts
	pairs := 0
	for _, c := range r.snap.Components {
		for i := 0; i < len(c.Pads); i++ {
			a := c.Pads[i]
			for j := i + 1; j < len(c.Pads); j++ {
				b := c.Pads[j]
				// A pad without a net (an unnamed thermal via in an exposed
				// pad) is checked against every netted pad, as KiCad does.
				if b.Net == a.Net || !padLayersMeet(a.Layer, b.Layer) {
					continue
				}
				gapMil, exact := padShapeGap(padShapeOf(a.X, a.Y, a.W, a.H, a.Rotation, a.Shape), padShapeOf(b.X, b.Y, b.W, b.H, b.Rotation, b.Shape))
				if gapMil <= 0 && !exact {
					// Bounding boxes overlap and a shape is not modelled
					// (a custom pad): the gap is unknown — never a pass.
					pairs++
					id := [2]string{c.Footprint, padPair(a.Number, b.Number)}
					if !rulesHasStr(unknown[id], c.Designator) {
						unknown[id] = append(unknown[id], c.Designator)
					}
					continue
				}
				if gapMil <= 0 {
					// Overlapping pads of two nets: a net tie (skipped) —
					// unless one has no net: an unnamed thermal via inside
					// an exposed pad is a different "net" to KiCad and
					// flagged on every instance.
					if (a.Net == "") == (b.Net == "") {
						continue
					}
					pairs++
					ep := a
					if a.Net == "" {
						ep = b
					}
					id := [2]string{c.Footprint, ep.Number}
					g := thermal[id]
					if g == nil {
						g = &thermalHit{ep: ep.Number, net: ep.Net}
						thermal[id] = g
					}
					g.vias++
					if !rulesHasStr(g.refs, c.Designator) {
						g.refs = append(g.refs, c.Designator)
					}
					continue
				}
				pairs++
				ca, clsA := r.padNeed(rb, c.LocalClearance, a.LocalClearance, a.Net)
				cb, clsB := r.padNeed(rb, c.LocalClearance, b.LocalClearance, b.Net)
				need, cls := ca, clsA
				if cb > ca {
					need, cls = cb, clsB
				}
				// µm: the snapshot's mil rounding must not invent a gap.
				gap := int64(math.Round(gapMil*nmPerMil/1000)) * 1000
				if gap >= need {
					continue
				}
				k := gkey{c.Footprint, need}
				g := groups[k]
				if g == nil {
					g = &group{gap: gap, pads: padPair(a.Number, b.Number)}
					groups[k] = g
				} else if gap < g.gap {
					g.gap, g.pads = gap, padPair(a.Number, b.Number)
				}
				if !rulesHasStr(g.classes, cls) {
					g.classes = append(g.classes, cls)
				}
				if !rulesHasStr(g.refs, c.Designator) {
					g.refs = append(g.refs, c.Designator)
				}
			}
		}
	}
	keys := make([]gkey, 0, len(groups))
	for k := range groups {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].fp != keys[j].fp {
			return keys[i].fp < keys[j].fp
		}
		return keys[i].need < keys[j].need
	})
	parts := map[string]bool{}
	for _, k := range keys {
		g := groups[k]
		sort.Strings(g.refs)
		sort.Strings(g.classes)
		for _, ref := range g.refs {
			parts[ref] = true
		}
		refs, more := g.refs, ""
		if len(refs) > 8 {
			more = fmt.Sprintf(" (+%d more)", len(refs)-8)
			refs = refs[:8]
		}
		chk.Items = append(chk.Items, fmt.Sprintf("%s: pad gap %s (%s) < clearance %s (%s) on %s%s — fix: a footprint-local clearance override ≤ %s on these footprints, or a lower clearance for that class (or a .kicad_dru rule for the footprint); pcbpilot does not lower it",
			emptyAs(k.fp, "(no library id)"), kicad.MmOfNm(g.gap), g.pads, kicad.MmOfNm(k.need), strings.Join(g.classes, "; "), strings.Join(refs, ", "), more, kicad.MmOfNm(g.gap)))
	}
	tids := make([][2]string, 0, len(thermal))
	for id := range thermal {
		tids = append(tids, id)
	}
	sort.Slice(tids, func(i, j int) bool { return tids[i][0]+"|"+tids[i][1] < tids[j][0]+"|"+tids[j][1] })
	for _, id := range tids {
		g := thermal[id]
		sort.Strings(g.refs)
		for _, ref := range g.refs {
			parts[ref] = true
		}
		refs, more := g.refs, ""
		if len(refs) > 8 {
			more = fmt.Sprintf(" (+%d more)", len(refs)-8)
			refs = refs[:8]
		}
		chk.Items = append(chk.Items, fmt.Sprintf("%s: pad(s) without a net overlap pad %s (%s) — unnamed thermal vias in the exposed pad; KiCad reports them as clearance errors to the pad, on %s%s — fix: give the thermal-via pads the exposed pad's number (same net) in the footprint; pcbpilot does not change footprints",
			emptyAs(id[0], "(no library id)"), emptyAs(g.ep, "(unnamed)"), g.net, strings.Join(refs, ", "), more))
	}
	uids := make([][2]string, 0, len(unknown))
	for id := range unknown {
		uids = append(uids, id)
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i][0]+"|"+uids[i][1] < uids[j][0]+"|"+uids[j][1] })
	for _, id := range uids {
		refs := unknown[id]
		sort.Strings(refs)
		for _, ref := range refs {
			parts[ref] = true
		}
		more := ""
		if len(refs) > 8 {
			more, refs = fmt.Sprintf(" (+%d more)", len(refs)-8), refs[:8]
		}
		chk.Items = append(chk.Items, fmt.Sprintf("%s: %s not judged — a custom pad shape whose bounding box overlaps the other pad, on %s%s: check the gap against the clearance in KiCad's DRC",
			emptyAs(id[0], "(no library id)"), id[1], strings.Join(refs, ", "), more))
	}
	if len(chk.Items) > 0 {
		chk.Status = gate.Warn
		chk.Detail = fmt.Sprintf("%d footprint(s) / clearance(s) on %d part(s) have pads closer than the %s clearance between them: KiCad DRC reports each as a clearance error no placement can fix (custom .kicad_dru clearance rules are not evaluated here; DRC is the judge)", len(chk.Items), len(parts), what)
	} else {
		chk.Detail = fmt.Sprintf("%d pad pair(s) of different nets inside footprints: every gap meets the %s clearance", pairs, what)
	}
	r.rep.check(chk)
}

// padNeed is one pad's clearance (nm) and where it comes from: its own
// local override, else the footprint's, else its net's class clearance
// (the class name KiCad resolves, composite "PP1_W8,Default" included).
func (r *rulesRun) padNeed(rb *kicad.RulesReadback, fpLocal, padLocal *float64, net string) (int64, string) {
	if padLocal != nil {
		return int64(math.Round(*padLocal * nmPerMil)), "pad local clearance"
	}
	if fpLocal != nil {
		return int64(math.Round(*fpLocal * nmPerMil)), "footprint local clearance"
	}
	if net == "" {
		return rb.Default.Clearance, "class Default (pad without a net)"
	}
	eff := rb.Nets[net]
	cls := eff.Class
	if cls == "" {
		cls = "Default"
	}
	return eff.Clearance, "class " + cls
}

// padLayersMeet: two pads share a copper layer (1 F.Cu, 2 B.Cu, 12 through).
func padLayersMeet(a, b int) bool { return a == b || a == 12 || b == 12 }

func emptyAs(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

// padPair names two pads ("pads 2/3"; an unnamed pad is "(unnamed)").
func padPair(a, b string) string {
	return "pads " + emptyAs(a, "(unnamed)") + "/" + emptyAs(b, "(unnamed)")
}
