package app

// intent_rules_plan.go — pure planner: design intent + a fresh `pcb.config.get`
// snapshot (+ PCB net names + differential-pair constraints) → the desired
// EasyEDA rule state and the exact differences to write.
//
// Mapping (all against the observed Web 3.2.203 rule schema; see
// extension/src/testdata/pcb-config-web-3.2.203.json):
//
//   - netClasses[] (+ nets[].netClass)  → native net class (pcb.net_class.create
//     / pcb.net_class.add_nets). Extra live members are preserved, never removed.
//   - class track width               → Physics.Track."PP_<class>" (copy of the
//     default rule; default/min/max per layer table) bound through netRules.
//   - class clearance                 → Spacing."Safe Spacing"."PP_<class>" (copy
//     of the default matrix; every copper×copper cell raised to ≥ clearance).
//   - class via drill/diameter        → Physics."Via Size"."PP_<class>".
//   - nets[].diffPair                 → pcb.differential_pair.create; width/gap
//     (when unambiguous) → the single global Physics."Differential Pair" rule.
//   - pairs[] (domain clearance/creepage) → unsupported: the official
//     net-by-net rule API is opaque and its schema has not been captured live.
//
// The desired values are computed from the DEFAULT rule + intent every time,
// never from a previous PP_ rule, so replaying the same intent is a no-op and a
// changed intent converges. Numeric comparison tolerates host float roundoff
// only (relative 1e-9), never an engineering tolerance.

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

const intentRulePrefix = "PP_"

// The copper object labels of the Safe Spacing matrix. Non-copper pairs
// (silk, outline, holes) keep the default rule's values.
var safeSpacingCopperLabels = map[string]bool{
	"Track": true, "SMD Pad": true, "TH Pad": true, "SMD Test Point": true, "TH Test Point": true,
	"Via": true, "Fill Region/Teardrop": true, "Copper/Plane Zone": true,
}

type intentRulesSnapshot struct {
	RuleConfiguration map[string]any `json:"ruleConfiguration"`
	Classes           []any          `json:"classes"`
	NetRules          []any          `json:"netRules"`
}

type intentLiveDiffPair struct {
	Name     string `json:"name"`
	Positive string `json:"positiveNet"`
	Negative string `json:"negativeNet"`
}

type intentPlanNote struct {
	Item   string `json:"item"`
	Detail string `json:"detail"`
	Status string `json:"status,omitempty"`
}

type intentClassPlan struct {
	Name         string   `json:"name"`
	Action       string   `json:"action"` // create | add_nets | ok | skip
	Nets         []string `json:"nets"`
	Add          []string `json:"add,omitempty"`
	ExtraLive    []string `json:"extraLiveMembers,omitempty"`
	MissingOnPcb []string `json:"missingOnPcb,omitempty"`
	TrackRule    string   `json:"trackRule,omitempty"`
	SpacingRule  string   `json:"spacingRule,omitempty"`
	ViaRule      string   `json:"viaRule,omitempty"`
}

type intentRuleChange struct {
	Path   string `json:"path"`
	Before any    `json:"before"`
	After  any    `json:"after"`
}

type intentBindChange struct {
	Class    string `json:"class"`
	Target   string `json:"target"` // netClass:<name> | net:<name>
	Category string `json:"category"`
	Before   any    `json:"before"`
	After    string `json:"after"`
}

type intentDiffPairPlan struct {
	Name         string  `json:"name"`
	Positive     string  `json:"positiveNet"`
	Negative     string  `json:"negativeNet"`
	Action       string  `json:"action"` // create | ok | skip
	ImpedanceOhm float64 `json:"impedanceOhm,omitempty"`
	WidthMil     float64 `json:"widthMil,omitempty"`
	GapMil       float64 `json:"gapMil,omitempty"`
	Note         string  `json:"note,omitempty"`
	// Interface family and its intra-pair length tolerance (intent
	// maxSkewMil, else the router's HS class table).
	Interface    string  `json:"interface,omitempty"`
	LengthTolMil float64 `json:"lengthTolMil,omitempty"`
}

// intentLengthGroupPlan is a declared length-matching group (inter-pair /
// byte lane). The host's Net Length Tolerance rule schema is not captured,
// so groups are reported for the router and reviewer, not written.
type intentLengthGroupPlan struct {
	Name      string   `json:"name"`
	Interface string   `json:"interface,omitempty"`
	Nets      []string `json:"nets"`
	Pairs     int      `json:"pairs"`
	TolMil    float64  `json:"tolMil"`
	MaxVias   int      `json:"maxVias,omitempty"`
}

type intentRulesPlan struct {
	Classes         []intentClassPlan       `json:"classes"`
	RuleChanges     []intentRuleChange      `json:"ruleChanges"`
	Bindings        []intentBindChange      `json:"bindings"`
	PendingBindings []string                `json:"pendingBindings,omitempty"`
	DiffPairs       []intentDiffPairPlan    `json:"diffPairs"`
	LengthGroups    []intentLengthGroupPlan `json:"lengthGroups,omitempty"`
	Unsupported     []intentPlanNote        `json:"unsupported"`
	Advisories      []intentPlanNote        `json:"advisories"`
	Conflicts       []intentPlanNote        `json:"conflicts"`
	PendingWrites   int                     `json:"pendingWrites"`
	// Edge is the board-edge safety distance written into the Safe Spacing
	// "Board Outline" cells (default rule and every PP_ rule).
	Edge *intentEdgePlan `json:"edge,omitempty"`

	edgePol           *pcbauto.EdgePolicy
	ruleConfiguration map[string]any // desired complete configuration
	netRules          []any          // desired complete net rules
}

func (p *intentRulesPlan) classOps() []intentClassPlan {
	var out []intentClassPlan
	for _, c := range p.Classes {
		if c.Action == "create" || c.Action == "add_nets" {
			out = append(out, c)
		}
	}
	return out
}

func (p *intentRulesPlan) diffPairCreates() []intentDiffPairPlan {
	var out []intentDiffPairPlan
	for _, d := range p.DiffPairs {
		if d.Action == "create" {
			out = append(out, d)
		}
	}
	return out
}

func (p *intentRulesPlan) finish() {
	p.PendingWrites = len(p.classOps()) + len(p.RuleChanges) + len(p.Bindings) + len(p.PendingBindings) + len(p.diffPairCreates())
	for _, s := range []*[]intentPlanNote{&p.Unsupported, &p.Advisories, &p.Conflicts} {
		if *s == nil {
			*s = []intentPlanNote{}
		}
	}
	if p.Classes == nil {
		p.Classes = []intentClassPlan{}
	}
	if p.RuleChanges == nil {
		p.RuleChanges = []intentRuleChange{}
	}
	if p.Bindings == nil {
		p.Bindings = []intentBindChange{}
	}
	if p.DiffPairs == nil {
		p.DiffPairs = []intentDiffPairPlan{}
	}
}

// intentEdgePlan is the board-edge distance the rules push writes.
type intentEdgePlan struct {
	EdgeKind string   `json:"edgeKind"`
	Source   string   `json:"source"` // intent | default
	OuterMil float64  `json:"outerMil"`
	InnerMil float64  `json:"innerMil"`
	VcutMil  float64  `json:"vcutMil,omitempty"`
	RuleMil  float64  `json:"ruleMil"` // written to the Board Outline cells of the default rule
	Cells    []string `json:"cells"`
	// Classes lists PP_ rules whose Board Outline cells exceed RuleMil (a
	// hazardous domain's distance to the accessible edge).
	Classes map[string]float64 `json:"classes,omitempty"`
	Why     []string           `json:"why,omitempty"`
}

// safeSpacingEdgeCopper are the copper objects whose Board Outline cell
// the edge distance sets (the Safe Spacing matrix has no per-layer-class
// split, so one value covers pours, tracks, pads and vias on every layer).
var safeSpacingEdgeCopper = map[string]bool{
	"Track": true, "SMD Pad": true, "TH Pad": true, "SMD Test Point": true, "TH Test Point": true,
	"Via": true, "Fill Region/Teardrop": true, "Copper/Plane Zone": true,
}

// edgeRuleMil is the Board Outline value for one Safe Spacing table: with a
// single table (the observed host schema) the larger inner/plane value —
// the matrix applies to every layer, and the inner plane is the copper that
// must pull back furthest; with per-layer tables outer keys take the outer
// value.
func edgeRuleMil(pol *pcbauto.EdgePolicy, key string, tables int) float64 {
	if tables > 1 && (key == "1" || key == "2") {
		return pol.LayerReq(pcbauto.LayerTop)
	}
	return math.Max(pol.LayerReq(pcbauto.LayerTop), pol.LayerReq(pcbauto.LayerInner1))
}

// setBoardOutlineCells raises every Board Outline × copper cell of a Safe
// Spacing rule to ≥ want(tableKey) mil; returns the touched cell labels.
func setBoardOutlineCells(rule map[string]any, want func(key string, tables int) float64) ([]string, error) {
	rows, cols := asStrSlice(rule["row"]), asStrSlice(rule["column"])
	tables, ok := rule["tables"].(map[string]any)
	if !ok || len(tables) == 0 || len(rows) == 0 || len(cols) == 0 {
		return nil, fmt.Errorf("Safe Spacing rule lacks row/column labels or tables")
	}
	unit := asString(rule["unit"])
	seen := map[string]bool{}
	var labels []string
	for _, key := range sortedKeys(tables) {
		content, ok := mnav(tables[key], "content").([]any)
		if !ok {
			return nil, fmt.Errorf("Safe Spacing table %s has no content matrix", key)
		}
		w := milToStored(want(key, len(tables)), unit)
		for i, rowRaw := range content {
			row, ok := rowRaw.([]any)
			if !ok || i >= len(rows) {
				continue
			}
			for j := range row {
				if j >= len(cols) {
					continue
				}
				other := ""
				switch {
				case rows[i] == "Board Outline" && safeSpacingEdgeCopper[cols[j]]:
					other = cols[j]
				case cols[j] == "Board Outline" && safeSpacingEdgeCopper[rows[i]]:
					other = rows[i]
				default:
					continue
				}
				v, ok := asFloatOK(row[j])
				if !ok {
					return nil, fmt.Errorf("Safe Spacing table %s cell %d,%d is not numeric", key, i, j)
				}
				row[j] = math.Max(v, w)
				if !seen[other] {
					seen[other] = true
					labels = append(labels, other)
				}
			}
		}
	}
	if len(labels) == 0 {
		return nil, fmt.Errorf("Safe Spacing matrix has no Board Outline × copper cells (labels %v)", rows)
	}
	return labels, nil
}

// planEdge raises the DEFAULT Safe Spacing rule's Board Outline cells to the
// edge distance (every PP_ rule is derived from it afterwards) and states
// the native creepage rule it would take, without enabling it.
func (p *intentRulesPlan) planEdge(rc map[string]any, in *designIntent) {
	pol := in.edgePolicy()
	p.edgePol = pol
	ep := &intentEdgePlan{EdgeKind: pol.Kind, Source: pol.Source, OuterMil: pol.LayerReq(pcbauto.LayerTop), InnerMil: pol.LayerReq(pcbauto.LayerInner1), VcutMil: pol.VcutMil,
		RuleMil: edgeRuleMil(pol, "", 1), Why: pol.Why}
	p.Edge = ep
	cat, err := ruleCategory(rc, "Spacing", "Safe Spacing")
	if err != nil {
		p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "board edge", Detail: err.Error()})
		return
	}
	name, def, err := defaultRuleOf(cat, "Spacing.Safe Spacing")
	if err != nil {
		p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "board edge", Detail: err.Error()})
		return
	}
	desired, _ := jsonClone(def).(map[string]any)
	cells, err := setBoardOutlineCells(desired, func(key string, n int) float64 { return edgeRuleMil(pol, key, n) })
	if err != nil {
		p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "board edge", Detail: err.Error()})
		return
	}
	ep.Cells = cells
	p.commitRule(cat, "Spacing.Safe Spacing", name, desired)
	p.Advisories = append(p.Advisories, intentPlanNote{Item: "board edge",
		Detail: fmt.Sprintf("Safe Spacing Board Outline × copper cells ≥ %.1f mil in the default rule %s and every PP_ rule (%s edge; the matrix is not per layer class, so the inner/plane value %.1f mil covers outer copper %.1f mil too). Pours and negative planes (内电层) are pulled back by exactly this rule.",
			ep.RuleMil, name, pol.Kind, ep.InnerMil, ep.OuterMil)})
	// Native creepage (Spacing › Creepage Distance): board-wide, net-agnostic
	// — enabling it would flag every SELV pair. Report the value only.
	maxCreep := 0.0
	for _, pr := range in.Pairs {
		maxCreep = math.Max(maxCreep, pr.CreepageMm)
	}
	for _, d := range pol.ByDomain {
		maxCreep = math.Max(maxCreep, d.CreepageMm)
	}
	if maxCreep > 0 {
		cur := "unknown"
		if cr, ok := mnav(rc, "Spacing", "Creepage Distance").(map[string]any); ok {
			for _, k := range sortedKeys(cr) {
				if v, ok := asFloatOK(mnav(cr[k], "creepageDistance")); ok {
					cur = fmt.Sprintf("%s = %g", k, v)
				}
			}
		}
		p.Advisories = append(p.Advisories, intentPlanNote{Item: "native creepage rule", Status: "advisory",
			Detail: fmt.Sprintf("Spacing › Creepage Distance would take creepageDistance %.2f mm (largest intent creepage; live %s) — NOT enabled: the host rule is board-wide and net-agnostic, so it would flag every SELV pair; pcb check --intent and pcb auto enforce the per-domain creepage instead", maxCreep, cur)})
	}
}

// classEdgeMil is the Board Outline value of a class's PP_ rule: the
// general value, raised to the domain distance of any hazardous member.
func (p *intentRulesPlan) classEdgeMil(key string, tables int, nets []string) float64 {
	if p.edgePol == nil {
		return 0
	}
	v := edgeRuleMil(p.edgePol, key, tables)
	for _, n := range nets {
		v = math.Max(v, p.edgePol.NetReq(n))
	}
	return v
}

// intentClassSpec is one class's desired membership + dimensions (mil).
type intentClassSpec struct {
	name                             string
	nets                             []string
	trackOuter, trackInner, trackMin float64
	clearance                        float64
	viaDia, viaDrill                 float64
	diffGap                          float64
}

func (s intentClassSpec) hasTrack() bool   { return s.trackOuter > 0 }
func (s intentClassSpec) hasSpacing() bool { return s.clearance > 0 }
func (s intentClassSpec) hasVia() bool     { return s.viaDia > 0 && s.viaDrill > 0 }

// buildIntentClassSpecs merges netClasses[] and nets[].netClass. A net claimed
// by two classes is a conflict (EasyEDA nets belong to one class).
func buildIntentClassSpecs(in *designIntent) ([]intentClassSpec, []intentPlanNote) {
	var conflicts []intentPlanNote
	order := []string{}
	byName := map[string]*intentNetClass{}
	members := map[string]map[string]bool{}
	add := func(class, net string) {
		if members[class] == nil {
			members[class] = map[string]bool{}
		}
		members[class][net] = true
	}
	for i := range in.NetClasses {
		c := &in.NetClasses[i]
		order = append(order, c.Name)
		byName[c.Name] = c
		for _, n := range c.Nets {
			if strings.TrimSpace(n) != "" {
				add(c.Name, n)
			}
		}
	}
	for _, name := range in.sortedNetNames() {
		cls := in.Nets[name].NetClass
		if cls == "" {
			continue
		}
		if err := validIntentName(cls); err != nil {
			conflicts = append(conflicts, intentPlanNote{Item: "net " + name, Detail: "netClass: " + err.Error()})
			continue
		}
		if _, ok := byName[cls]; !ok {
			byName[cls] = &intentNetClass{Name: cls}
			order = append(order, cls)
		}
		add(cls, name)
	}
	owner := map[string]string{}
	for _, cls := range order {
		for n := range members[cls] {
			if prev, ok := owner[n]; ok && prev != cls {
				conflicts = append(conflicts, intentPlanNote{Item: "net " + n, Detail: fmt.Sprintf("claimed by net classes %s and %s; an EasyEDA net belongs to one class", prev, cls)})
			}
			owner[n] = cls
		}
	}
	var specs []intentClassSpec
	for _, cls := range order {
		c := byName[cls]
		s := intentClassSpec{name: cls, trackOuter: c.TrackMil, trackInner: c.InnerTrackMil, trackMin: c.MinTrackMil,
			clearance: c.ClearanceMil, viaDia: c.ViaDiaMil, viaDrill: c.ViaDrillMil, diffGap: c.DiffGapMil}
		var outer, inner, clr float64
		minW := math.Inf(1)
		for n := range members[cls] {
			s.nets = append(s.nets, n)
			if np := in.Nets[n]; np != nil {
				outer = math.Max(outer, np.WidthMil.Outer)
				inner = math.Max(inner, np.WidthMil.Inner)
				clr = math.Max(clr, np.ClearanceMil)
				if np.WidthMil.Min > 0 {
					minW = math.Min(minW, np.WidthMil.Min)
				}
			}
		}
		sort.Strings(s.nets)
		if s.trackOuter == 0 {
			s.trackOuter = outer
		}
		if s.trackInner == 0 {
			s.trackInner = inner
		}
		if s.trackInner == 0 {
			s.trackInner = s.trackOuter
		}
		// The per-net current minimum (widthMil.min) is what the body of a
		// trace needs; the router still necks down to the fabrication
		// minimum for a few mil at fine-pitch pads. EasyEDA's rule cannot
		// say "only a short neck may be narrower", so the rule minimum stays
		// at the board default (fab minimum) and the class width becomes the
		// default — pushing widthMil.min as the minimum flagged every neck
		// (live 2026-09-27: 9 Track errors at 5 mil necks). Only an explicit
		// class minTrackMil overrides.
		_ = minW
		if s.clearance == 0 {
			s.clearance = clr
		}
		specs = append(specs, s)
	}
	return specs, conflicts
}

// planIntentRules is the pure core. pcbNets nil = unknown (no membership
// filtering); livePairs nil = no constraints read.
func planIntentRules(in *designIntent, snap intentRulesSnapshot, pcbNets map[string]bool, livePairs []intentLiveDiffPair) *intentRulesPlan {
	p := &intentRulesPlan{}
	defer p.finish()
	if snap.RuleConfiguration == nil || snap.Classes == nil || snap.NetRules == nil {
		p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "pcb.config.get", Detail: "ruleConfiguration, classes or netRules missing from the snapshot"})
		return p
	}
	rc, _ := jsonClone(snap.RuleConfiguration).(map[string]any)
	netRules, _ := jsonClone(snap.NetRules).([]any)
	p.ruleConfiguration, p.netRules = rc, netRules

	liveClasses := map[string][]string{}
	for _, raw := range snap.Classes {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		name := asString(m["name"])
		liveClasses[name] = asStrSlice(m["nets"])
	}
	onPcb := func(n string) bool { return pcbNets == nil || pcbNets[n] }

	specs, conflicts := buildIntentClassSpecs(in)
	p.Conflicts = append(p.Conflicts, conflicts...)
	p.planEdge(rc, in)

	for _, s := range specs {
		cp := intentClassPlan{Name: s.name}
		for _, n := range s.nets {
			if onPcb(n) {
				cp.Nets = append(cp.Nets, n)
			} else {
				cp.MissingOnPcb = append(cp.MissingOnPcb, n)
			}
		}
		if len(cp.MissingOnPcb) > 0 {
			p.Advisories = append(p.Advisories, intentPlanNote{Item: "class " + s.name, Detail: "nets not on this PCB were left out: " + strings.Join(cp.MissingOnPcb, ", ")})
		}
		if len(cp.Nets) == 0 {
			cp.Action = "skip"
			p.Classes = append(p.Classes, cp)
			continue
		}
		live, exists := liveClasses[s.name]
		liveSet := map[string]bool{}
		for _, n := range live {
			liveSet[n] = true
		}
		switch {
		case !exists:
			cp.Action = "create"
		default:
			for _, n := range cp.Nets {
				if !liveSet[n] {
					cp.Add = append(cp.Add, n)
				}
			}
			want := map[string]bool{}
			for _, n := range cp.Nets {
				want[n] = true
			}
			for _, n := range live {
				if !want[n] {
					cp.ExtraLive = append(cp.ExtraLive, n)
				}
			}
			if len(cp.Add) > 0 {
				cp.Action = "add_nets"
			} else {
				cp.Action = "ok"
			}
		}
		// A net already in ANOTHER live class cannot join this one silently.
		for _, n := range cp.Nets {
			for other, mem := range liveClasses {
				if other == s.name {
					continue
				}
				for _, m := range mem {
					if m == n {
						p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "net " + n, Detail: fmt.Sprintf("already a member of live class %s; intent wants %s — resolve membership explicitly", other, s.name)})
					}
				}
			}
		}

		// Rules (desired from default + intent).
		if s.hasTrack() {
			if name, err := p.ensureTrackRule(rc, s); err != nil {
				p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "class " + s.name + " track", Detail: err.Error()})
			} else {
				cp.TrackRule = name
			}
		}
		if s.hasSpacing() {
			if name, err := p.ensureSpacingRule(rc, s); err != nil {
				p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "class " + s.name + " clearance", Detail: err.Error()})
			} else {
				cp.SpacingRule = name
			}
		}
		if s.hasVia() {
			if name, err := p.ensureViaRule(rc, s); err != nil {
				p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "class " + s.name + " via", Detail: err.Error()})
			} else {
				cp.ViaRule = name
			}
		} else if s.viaDia > 0 || s.viaDrill > 0 {
			p.Advisories = append(p.Advisories, intentPlanNote{Item: "class " + s.name + " via", Detail: "only one of viaDiaMil/viaDrillMil given; via rule not written"})
		}
		p.bindClass(netRules, cp, exists, live)
		p.Classes = append(p.Classes, cp)
	}

	p.planDiffPairs(in, rc, specs, onPcb, livePairs)
	p.planLengthGroups(in, onPcb)

	for _, pr := range in.Pairs {
		req := []string{}
		if pr.ClearanceMm > 0 {
			req = append(req, fmt.Sprintf("clearance ≥ %.2f mm", pr.ClearanceMm))
		}
		if pr.CreepageMm > 0 {
			req = append(req, fmt.Sprintf("creepage ≥ %.2f mm", pr.CreepageMm))
		}
		if pr.SlotRequired {
			req = append(req, "slot required")
		}
		p.Unsupported = append(p.Unsupported, intentPlanNote{
			Item:   fmt.Sprintf("pair %s ↔ %s", pr.A, pr.B),
			Detail: strings.Join(req, ", ") + " — net-to-net / class-to-class clearance needs eda.pcb_Drc.overwriteNetByNetRules, whose schema is opaque and not yet captured live; enforce by layout keep-out/slot and verify with DRC",
			Status: "planned",
		})
	}
	for _, name := range in.sortedNetNames() {
		n := in.Nets[name]
		if n.ViasPerTransition > 1 {
			p.Advisories = append(p.Advisories, intentPlanNote{Item: "net " + name, Detail: fmt.Sprintf("viasPerTransition=%d has no EasyEDA rule; the router/reviewer must place that many vias per layer change", n.ViasPerTransition)})
		}
		if n.ImpedanceOhm > 0 && n.DiffPair == "" {
			p.Advisories = append(p.Advisories, intentPlanNote{Item: "net " + name, Detail: fmt.Sprintf("single-ended %.0f Ω target: EasyEDA rules store width only; width comes from widthMil", n.ImpedanceOhm)})
		}
	}
	return p
}

// ── rule builders ─────────────────────────────────────────────────────────

func ruleCategory(rc map[string]any, section, category string) (map[string]any, error) {
	sec, ok := rc[section].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("rule section %s is missing or has an unsupported schema", section)
	}
	cat, ok := sec[category].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("rule category %s.%s is missing or has an unsupported schema", section, category)
	}
	return cat, nil
}

func defaultRuleOf(cat map[string]any, where string) (string, map[string]any, error) {
	var name string
	var rule map[string]any
	n := 0
	for k, v := range cat {
		m, ok := v.(map[string]any)
		if !ok || m["isSetDefault"] != true {
			continue
		}
		name, rule = k, m
		n++
	}
	if n != 1 {
		return "", nil, fmt.Errorf("%s: need exactly one default rule to copy from, found %d", where, n)
	}
	if u := asString(rule["unit"]); u != "mm" && u != "mil" {
		return "", nil, fmt.Errorf("%s.%s: stored unit %q is unknown", where, name, u)
	}
	return name, rule, nil
}

func milToStored(mil float64, unit string) float64 {
	if unit == "mm" {
		return mil * 0.0254
	}
	return mil
}

// derivedRule copies the default rule under name and marks it non-default.
func derivedRule(def map[string]any, name string) map[string]any {
	out, _ := jsonClone(def).(map[string]any)
	out["editName"] = name
	out["isSetDefault"] = false
	return out
}

// commitRule writes desired into cat[name] when it differs (beyond host
// roundoff) from the live value and records the changes.
func (p *intentRulesPlan) commitRule(cat map[string]any, path, name string, desired map[string]any) {
	before, exists := cat[name]
	if exists && jsonEqualTol(before, desired) {
		return
	}
	if !exists {
		p.RuleChanges = append(p.RuleChanges, intentRuleChange{Path: path + "." + name, Before: nil, After: desired})
	} else {
		p.RuleChanges = append(p.RuleChanges, jsonDiff(before, desired, path+"."+name)...)
	}
	cat[name] = desired
}

func isOuterLayerKey(key string, n int) bool { return n == 1 || key == "1" || key == "2" }

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func (p *intentRulesPlan) ensureTrackRule(rc map[string]any, s intentClassSpec) (string, error) {
	cat, err := ruleCategory(rc, "Physics", "Track")
	if err != nil {
		return "", err
	}
	_, def, err := defaultRuleOf(cat, "Physics.Track")
	if err != nil {
		return "", err
	}
	name := intentRulePrefix + s.name
	unit := asString(def["unit"])
	desired := derivedRule(def, name)
	data, ok := mnav(desired, "form", "data").(map[string]any)
	if !ok || len(data) == 0 {
		return "", fmt.Errorf("Physics.Track default rule has no form.data layer table")
	}
	for _, layer := range sortedKeys(data) {
		entry, ok := data[layer].(map[string]any)
		if !ok {
			return "", fmt.Errorf("Physics.Track layer %s is not an object", layer)
		}
		for _, k := range []string{"minValue", "defaultValue", "maxValue"} {
			if _, ok := entry[k]; !ok {
				return "", fmt.Errorf("Physics.Track layer %s lacks %s", layer, k)
			}
		}
		width := s.trackInner
		if isOuterLayerKey(layer, len(data)) {
			width = s.trackOuter
		}
		dv := milToStored(width, unit)
		defMin, okMin := asFloatOK(entry["minValue"])
		if !okMin {
			return "", fmt.Errorf("Physics.Track layer %s minValue is not numeric", layer)
		}
		minV := defMin
		if s.trackMin > 0 {
			minV = milToStored(s.trackMin, unit)
		}
		minV = math.Min(minV, dv)
		entry["defaultValue"] = dv
		entry["minValue"] = minV
		if mx, ok := asFloatOK(entry["maxValue"]); ok && mx < dv {
			entry["maxValue"] = dv
		}
	}
	p.commitRule(cat, "Physics.Track", name, desired)
	return name, nil
}

func (p *intentRulesPlan) ensureSpacingRule(rc map[string]any, s intentClassSpec) (string, error) {
	cat, err := ruleCategory(rc, "Spacing", "Safe Spacing")
	if err != nil {
		return "", err
	}
	_, def, err := defaultRuleOf(cat, "Spacing.Safe Spacing")
	if err != nil {
		return "", err
	}
	name := intentRulePrefix + s.name
	unit := asString(def["unit"])
	desired := derivedRule(def, name)
	rows, cols := asStrSlice(desired["row"]), asStrSlice(desired["column"])
	if len(rows) == 0 || len(cols) == 0 {
		return "", fmt.Errorf("Safe Spacing default rule lacks row/column labels")
	}
	tables, ok := desired["tables"].(map[string]any)
	if !ok || len(tables) == 0 {
		return "", fmt.Errorf("Safe Spacing default rule has no tables")
	}
	want := milToStored(s.clearance, unit)
	touched := 0
	for _, key := range sortedKeys(tables) {
		content, ok := mnav(tables[key], "content").([]any)
		if !ok {
			return "", fmt.Errorf("Safe Spacing table %s has no content matrix", key)
		}
		for i, rowRaw := range content {
			row, ok := rowRaw.([]any)
			if !ok || i >= len(rows) {
				continue
			}
			for j := range row {
				if j >= len(cols) || !safeSpacingCopperLabels[rows[i]] || !safeSpacingCopperLabels[cols[j]] {
					continue
				}
				v, ok := asFloatOK(row[j])
				if !ok {
					return "", fmt.Errorf("Safe Spacing table %s cell %d,%d is not numeric", key, i, j)
				}
				row[j] = math.Max(v, want)
				touched++
			}
		}
	}
	if touched == 0 {
		return "", fmt.Errorf("Safe Spacing matrix has no copper×copper cells (labels %v)", rows)
	}
	if p.edgePol != nil {
		// The default already carries the general edge distance; a class with
		// hazardous members keeps its domain distance to the edge.
		if _, err := setBoardOutlineCells(desired, func(key string, n int) float64 { return p.classEdgeMil(key, n, s.nets) }); err != nil {
			return "", err
		}
		if v := p.classEdgeMil("", 1, s.nets); p.Edge != nil && v > p.Edge.RuleMil+1e-9 {
			if p.Edge.Classes == nil {
				p.Edge.Classes = map[string]float64{}
			}
			p.Edge.Classes[name] = v
		}
	}
	p.commitRule(cat, "Spacing.Safe Spacing", name, desired)
	return name, nil
}

func (p *intentRulesPlan) ensureViaRule(rc map[string]any, s intentClassSpec) (string, error) {
	cat, err := ruleCategory(rc, "Physics", "Via Size")
	if err != nil {
		return "", err
	}
	_, def, err := defaultRuleOf(cat, "Physics.Via Size")
	if err != nil {
		return "", err
	}
	name := intentRulePrefix + s.name
	unit := asString(def["unit"])
	desired := derivedRule(def, name)
	form, ok := desired["form"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("Via Size default rule has no form")
	}
	for _, side := range []struct {
		prefix string
		mil    float64
	}{{"viaOuterdiameter", s.viaDia}, {"viaInnerdiameter", s.viaDrill}} {
		v := milToStored(side.mil, unit)
		for _, k := range []string{"Min", "Default", "Max"} {
			if _, ok := form[side.prefix+k]; !ok {
				return "", fmt.Errorf("Via Size default rule lacks %s%s", side.prefix, k)
			}
		}
		form[side.prefix+"Default"] = v
		if mn, ok := asFloatOK(form[side.prefix+"Min"]); ok && mn > v {
			form[side.prefix+"Min"] = v
		}
		if mx, ok := asFloatOK(form[side.prefix+"Max"]); ok && mx < v {
			form[side.prefix+"Max"] = v
		}
	}
	for _, b := range []string{"Min", "Default", "Max"} {
		in, iok := asFloatOK(form["viaInnerdiameter"+b])
		out, ook := asFloatOK(form["viaOuterdiameter"+b])
		if iok && ook && in >= out {
			return "", fmt.Errorf("via %s: hole %.4g must be smaller than outer %.4g (%s)", b, in, out, unit)
		}
	}
	p.commitRule(cat, "Physics.Via Size", name, desired)
	return name, nil
}

// bindClass points the class entry and every member row of netRules at the
// class's rules. A missing entry for a class that will be created is pending.
func (p *intentRulesPlan) bindClass(netRules []any, cp intentClassPlan, liveExists bool, liveMembers []string) {
	cats := [][2]string{{"Track", cp.TrackRule}, {"Safe Spacing", cp.SpacingRule}, {"Via Size", cp.ViaRule}}
	hasRule := false
	for _, c := range cats {
		if c[1] != "" {
			hasRule = true
		}
	}
	if !hasRule {
		return
	}
	var entry map[string]any
	n := 0
	for _, raw := range netRules {
		m, ok := raw.(map[string]any)
		if ok && m["type"] == "netClass" && m["name"] == cp.Name {
			entry = m
			n++
		}
	}
	if !liveExists {
		p.PendingBindings = append(p.PendingBindings, fmt.Sprintf("class %s: bind %s after the class is created", cp.Name, ruleList(cats)))
		return
	}
	if n != 1 {
		p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "class " + cp.Name, Detail: fmt.Sprintf("netRules has %d entries for this class (need exactly 1); read fresh state", n)})
		return
	}
	sub, ok := entry["sub"].([]any)
	if !ok {
		p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "class " + cp.Name, Detail: "netRules class entry has no sub array"})
		return
	}
	subNames := []string{}
	targets := []map[string]any{entry}
	for _, raw := range sub {
		m, ok := raw.(map[string]any)
		if !ok || m["type"] != "net" {
			p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "class " + cp.Name, Detail: "netRules sub contains a non-net row"})
			return
		}
		subNames = append(subNames, asString(m["name"]))
		targets = append(targets, m)
	}
	want := append([]string(nil), liveMembers...)
	sort.Strings(want)
	sort.Strings(subNames)
	if strings.Join(want, "\x00") != strings.Join(subNames, "\x00") {
		p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "class " + cp.Name, Detail: "live class members and netRules children differ; read fresh state"})
		return
	}
	for _, t := range targets {
		for _, c := range cats {
			if c[1] == "" {
				continue
			}
			cur, ok := t[c[0]].(string)
			if !ok {
				p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "class " + cp.Name, Detail: fmt.Sprintf("netRules row %s lacks a string %q assignment (unsupported schema)", asString(t["name"]), c[0])})
				return
			}
			if cur == c[1] {
				continue
			}
			target := "net:" + asString(t["name"])
			if t["type"] == "netClass" {
				target = "netClass:" + cp.Name
			}
			p.Bindings = append(p.Bindings, intentBindChange{Class: cp.Name, Target: target, Category: c[0], Before: cur, After: c[1]})
			t[c[0]] = c[1]
		}
	}
	if len(cp.Add) > 0 {
		p.PendingBindings = append(p.PendingBindings, fmt.Sprintf("class %s: bind new members %s after add_nets", cp.Name, strings.Join(cp.Add, ",")))
	}
}

func ruleList(cats [][2]string) string {
	var parts []string
	for _, c := range cats {
		if c[1] != "" {
			parts = append(parts, c[0]+"="+c[1])
		}
	}
	return strings.Join(parts, ", ")
}

// ── differential pairs ────────────────────────────────────────────────────

// diffPolarity: +1 positive, -1 negative, 0 unknown.
func diffPolarity(net string) int {
	u := strings.ToUpper(net)
	for _, s := range []string{"_DP", "_P", "+", "DP", "P", "_H"} {
		if strings.HasSuffix(u, s) {
			return 1
		}
	}
	for _, s := range []string{"_DM", "_DN", "_N", "-", "DM", "DN", "N", "M", "_L"} {
		if strings.HasSuffix(u, s) {
			return -1
		}
	}
	return 0
}

func (p *intentRulesPlan) planDiffPairs(in *designIntent, rc map[string]any, specs []intentClassSpec, onPcb func(string) bool, live []intentLiveDiffPair) {
	groups := map[string][]string{}
	for _, name := range in.sortedNetNames() {
		dp := in.Nets[name].DiffPair
		if dp == "" {
			continue
		}
		// Two conventions reach here: a shared pair id on both nets
		// ("USB_D"), or each net naming its partner net (`intent derive`
		// writes USB_DM → "USB_DP", USB_DP → "USB_DM"). A mutual partner
		// reference is one pair, keyed by the common name prefix.
		if partner, ok := in.Nets[dp]; ok && partner.DiffPair == name {
			if name > dp {
				continue // the pair is added once, from the lower name
			}
			dp = diffPairKey(name, dp)
			groups[dp] = append(groups[dp], name, in.sortedPartner(name))
			continue
		}
		groups[dp] = append(groups[dp], name)
	}
	classGap := map[string]float64{}
	for _, s := range specs {
		for _, n := range s.nets {
			if s.diffGap > 0 {
				classGap[n] = s.diffGap
			}
		}
	}
	names := make([]string, 0, len(groups))
	for k := range groups {
		names = append(names, k)
	}
	sort.Strings(names)
	var widths, gaps []float64
	for _, g := range names {
		nets := groups[g]
		if err := validIntentName(g); err != nil {
			p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "diffPair " + g, Detail: err.Error()})
			continue
		}
		if len(nets) != 2 {
			p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "diffPair " + g, Detail: fmt.Sprintf("needs exactly 2 nets, intent has %d (%s)", len(nets), strings.Join(nets, ","))})
			continue
		}
		pos, neg := nets[0], nets[1]
		a, b := diffPolarity(nets[0]), diffPolarity(nets[1])
		note := ""
		switch {
		case a == -1 && b == 1:
			pos, neg = nets[1], nets[0]
		case a == 1 && b == -1:
		default:
			note = "polarity not recognisable from the names; first name taken as positive"
		}
		d := intentDiffPairPlan{Name: g, Positive: pos, Negative: neg, Note: note}
		np, nn := in.Nets[pos], in.Nets[neg]
		d.Interface = firstNonEmptyStr(np.Interface, nn.Interface)
		switch {
		case np.MaxSkewMil > 0 && nn.MaxSkewMil > 0:
			d.LengthTolMil = math.Min(np.MaxSkewMil, nn.MaxSkewMil)
		case np.MaxSkewMil > 0 || nn.MaxSkewMil > 0:
			d.LengthTolMil = math.Max(np.MaxSkewMil, nn.MaxSkewMil)
		default:
			d.LengthTolMil = pcbauto.ClassifyHSName(d.Interface, pos).MaxSkewMil
		}
		if np.Interface != "" && nn.Interface != "" && np.Interface != nn.Interface {
			p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "diffPair " + g, Detail: fmt.Sprintf("members disagree on the interface (%s: %s, %s: %s)", pos, np.Interface, neg, nn.Interface)})
		}
		d.ImpedanceOhm = math.Max(np.ImpedanceOhm, nn.ImpedanceOhm)
		if np.WidthMil.Outer > 0 && np.WidthMil.Outer == nn.WidthMil.Outer {
			d.WidthMil = np.WidthMil.Outer
		}
		d.GapMil = math.Max(np.PairGapMil, nn.PairGapMil)
		if d.GapMil == 0 {
			d.GapMil = math.Max(classGap[pos], classGap[neg])
		}
		if !onPcb(pos) || !onPcb(neg) {
			d.Action = "skip"
			d.Note = strings.TrimSpace(d.Note + " net(s) not on this PCB")
			p.DiffPairs = append(p.DiffPairs, d)
			continue
		}
		d.Action = "create"
		for _, lp := range live {
			sameNets := (lp.Positive == pos && lp.Negative == neg) || (lp.Positive == neg && lp.Negative == pos)
			if lp.Name == g {
				if lp.Positive == pos && lp.Negative == neg {
					d.Action = "ok"
				} else {
					p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "diffPair " + g, Detail: fmt.Sprintf("live pair %s binds %s/%s, intent wants %s/%s; delete it explicitly first", g, lp.Positive, lp.Negative, pos, neg)})
					d.Action = "conflict"
				}
				break
			}
			if sameNets {
				d.Action = "ok"
				d.Note = strings.TrimSpace(d.Note + " already constrained as live pair " + lp.Name)
				break
			}
		}
		if d.ImpedanceOhm > 0 {
			p.Advisories = append(p.Advisories, intentPlanNote{Item: "diffPair " + g, Detail: fmt.Sprintf("%.0f Ω target: EasyEDA stores width/gap, not impedance; width/gap must come from the stackup calculation in intent", d.ImpedanceOhm)})
		}
		if d.WidthMil > 0 {
			widths = append(widths, d.WidthMil)
			gaps = append(gaps, d.GapMil)
		}
		p.DiffPairs = append(p.DiffPairs, d)
	}
	if len(widths) == 0 {
		return
	}
	for i := range widths {
		if widths[i] != widths[0] || gaps[i] != gaps[0] {
			p.Advisories = append(p.Advisories, intentPlanNote{Item: "Differential Pair rule", Detail: "pairs disagree on width/gap; the host has ONE global differential-pair rule, left unchanged"})
			return
		}
	}
	// Intra-pair length tolerance: the router's HS class table (USB2 100 mil,
	// USB3/PCIe/HDMI 5 mil, …) — one source for the SI check and the host
	// rule; the strictest pair wins. The host default 10 mil flagged a
	// 20.9 mil USB2 mismatch the SI check accepts.
	tol := math.Inf(1)
	for _, d := range p.DiffPairs {
		if d.Action == "skip" || d.LengthTolMil <= 0 {
			continue
		}
		tol = math.Min(tol, d.LengthTolMil)
	}
	if math.IsInf(tol, 1) {
		tol = 0
	}
	if err := p.ensureDiffPairRule(rc, widths[0], gaps[0], tol); err != nil {
		p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "Differential Pair rule", Detail: err.Error()})
	}
}

func (p *intentRulesPlan) ensureDiffPairRule(rc map[string]any, widthMil, gapMil, lenTolMil float64) error {
	cat, err := ruleCategory(rc, "Physics", "Differential Pair")
	if err != nil {
		return err
	}
	name, def, err := defaultRuleOf(cat, "Physics.Differential Pair")
	if err != nil {
		return err
	}
	unit := asString(def["unit"])
	desired, _ := jsonClone(def).(map[string]any)
	set := func(table string, mil float64) error {
		data, ok := mnav(desired, "form", table, "data").(map[string]any)
		if !ok || len(data) == 0 {
			return fmt.Errorf("Differential Pair rule lacks form.%s.data", table)
		}
		v := milToStored(mil, unit)
		for _, layer := range sortedKeys(data) {
			e, ok := data[layer].(map[string]any)
			if !ok {
				return fmt.Errorf("Differential Pair %s layer %s is not an object", table, layer)
			}
			e["defaultValue"] = v
			if mn, ok := asFloatOK(e["minValue"]); ok && mn > v {
				e["minValue"] = v
			}
			if mx, ok := asFloatOK(e["maxValue"]); ok && mx < v {
				e["maxValue"] = v
			}
		}
		return nil
	}
	if err := set("strokeWidthTables", widthMil); err != nil {
		return err
	}
	if gapMil > 0 {
		if err := set("diffPairSpacingTables", gapMil); err != nil {
			return err
		}
	}
	if lenTolMil > 0 {
		// EasyEDA's own spelling of the key; a scalar in the rule unit.
		if _, ok := mnav(desired, "form", "differentailPairLenTolerMax").(float64); ok {
			mnavSet(desired, milToStored(lenTolMil, unit), "form", "differentailPairLenTolerMax")
		} else {
			p.Advisories = append(p.Advisories, intentPlanNote{Item: "Differential Pair rule", Detail: "no form.differentailPairLenTolerMax on this host; length tolerance left at the host default"})
		}
	}
	p.commitRule(cat, "Physics.Differential Pair", name, desired)
	return nil
}

// ── JSON helpers ──────────────────────────────────────────────────────────

func jsonClone(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil
	}
	return out
}

func floatsClose(a, b float64) bool {
	if a == b {
		return true
	}
	return math.Abs(a-b) <= 1e-9*math.Max(math.Abs(a), math.Abs(b))
}

// jsonEqualTol compares decoded JSON values; numbers within host roundoff.
func jsonEqualTol(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, x := range av {
			y, ok := bv[k]
			if !ok || !jsonEqualTol(x, y) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqualTol(av[i], bv[i]) {
				return false
			}
		}
		return true
	case float64:
		bv, ok := b.(float64)
		return ok && floatsClose(av, bv)
	default:
		return a == b
	}
}

// jsonDiff lists leaf differences between two decoded JSON values.
func jsonDiff(before, after any, path string) []intentRuleChange {
	bm, bok := before.(map[string]any)
	am, aok := after.(map[string]any)
	if bok && aok {
		keys := map[string]bool{}
		for k := range bm {
			keys[k] = true
		}
		for k := range am {
			keys[k] = true
		}
		ks := make([]string, 0, len(keys))
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		var out []intentRuleChange
		for _, k := range ks {
			out = append(out, jsonDiff(bm[k], am[k], path+"."+k)...)
		}
		return out
	}
	ba, bok := before.([]any)
	aa, aok := after.([]any)
	if bok && aok && len(ba) == len(aa) {
		var out []intentRuleChange
		for i := range ba {
			out = append(out, jsonDiff(ba[i], aa[i], fmt.Sprintf("%s[%d]", path, i))...)
		}
		return out
	}
	if jsonEqualTol(before, after) {
		return nil
	}
	return []intentRuleChange{{Path: path, Before: before, After: after}}
}

// sortedPartner returns the partner net named by a mutual diffPair reference.
func (in *designIntent) sortedPartner(name string) string { return in.Nets[name].DiffPair }

// diffPairKey names a pair from its two nets: the common prefix without a
// trailing separator ("USB_DM","USB_DP" → "USB_D"), else "a/b".
func diffPairKey(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	k := strings.TrimRight(a[:n], "_-")
	if k == "" {
		return a + "/" + b
	}
	return k
}

// mnavSet sets v at the key path of nested maps; the parents must exist.
func mnavSet(root map[string]any, v any, keys ...string) {
	m := root
	for _, k := range keys[:len(keys)-1] {
		next, ok := m[k].(map[string]any)
		if !ok {
			return
		}
		m = next
	}
	m[keys[len(keys)-1]] = v
}

func firstNonEmptyStr(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// planLengthGroups reports every declared length group with more than one
// matching unit (a pair counts once). Tolerances disagreeing inside a group
// are a conflict; the host rule is unsupported (schema not captured).
func (p *intentRulesPlan) planLengthGroups(in *designIntent, onPcb func(string) bool) {
	members := map[string][]string{}
	for _, name := range in.sortedNetNames() {
		if g := in.Nets[name].LengthGroup; g != "" && onPcb(name) {
			members[g] = append(members[g], name)
		}
	}
	names := make([]string, 0, len(members))
	for g := range members {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		nets := members[g]
		in1 := map[string]bool{}
		for _, n := range nets {
			in1[n] = true
		}
		units, pairs := 0, 0
		seen := map[string]bool{}
		lg := intentLengthGroupPlan{Name: g, Nets: nets}
		tols := map[float64]bool{}
		for _, n := range nets {
			np := in.Nets[n]
			if np.LengthTolMil > 0 {
				tols[np.LengthTolMil] = true
				lg.TolMil = math.Max(lg.TolMil, np.LengthTolMil)
			}
			if np.MaxVias > 0 && (lg.MaxVias == 0 || np.MaxVias < lg.MaxVias) {
				lg.MaxVias = np.MaxVias
			}
			lg.Interface = firstNonEmptyStr(lg.Interface, np.Interface)
			if seen[n] {
				continue
			}
			seen[n] = true
			units++
			if dp := np.DiffPair; dp != "" && in1[dp] {
				seen[dp] = true
				pairs++
			}
		}
		if units < 2 {
			continue // a lone pair: its intra-pair tolerance is the diff-pair rule
		}
		lg.Pairs = pairs
		if len(tols) > 1 {
			p.Conflicts = append(p.Conflicts, intentPlanNote{Item: "lengthGroup " + g, Detail: "members declare different lengthTolMil values; one group has one tolerance"})
		}
		p.LengthGroups = append(p.LengthGroups, lg)
		detail := fmt.Sprintf("%d unit(s) (%d pair(s)) matched within %.0f mil", units, pairs, lg.TolMil)
		if lg.TolMil == 0 {
			detail = fmt.Sprintf("%d unit(s) (%d pair(s)) grouped without a tolerance (reported only)", units, pairs)
		}
		p.Unsupported = append(p.Unsupported, intentPlanNote{Item: "lengthGroup " + g,
			Detail: detail + " — the host Net Length Tolerance rule schema is not captured; pcb auto --intent tunes and checks the group, verify after routing",
			Status: "planned"})
	}
}
