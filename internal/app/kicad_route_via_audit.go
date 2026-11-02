package app

// kicad_route_via_audit.go — the via-audit gate: every via of the FINAL board
// (after all geometry stages) with its net, resolved class, span, size,
// provenance and violated constraint. Read-only.
//
// Manufacturing legality (annular ring, drill, diameter vs the board's and the
// contract's minimums) is kept apart from preferred-size conformance (the
// net's class via). Exceptions are declared policy (--via-policy), reported,
// never silent: route's own intent escapes are a built-in neckdown-escape
// exception to preferred size; nothing exempts a via from legality except a
// declared non-through kind with its own explicit minimums. Unknown provenance
// is no exemption. Vias already on the input board are judged but only their
// legality fails the gate. A custom rule on via size that cannot be resolved
// to a number for every via makes the gate cannot-check.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// Provenance of a via on the final board.
const (
	viaFromInput      = "input"
	viaFromRouter     = "router"
	viaFromTracemaker = "tracemaker"
	viaFromRepair     = "router-repaired"
	viaFromEscape     = "escape"
	viaFromArray      = "via-array"
	viaFromUnknown    = "unknown"
)

// viaProvenance records one routed candidate's vias against the input board:
// the board's own, those its backend created (created names it, viaFromRouter
// when empty) and those repaired. A via that could not be told is in none and
// stays unknown. The methods accept nil.
type viaProvenance struct {
	created              string
	input, router, grown map[string]bool
}

// record: input holds the board's own vias on the routed board; skip those not
// to be counted as created either (input and the ones that could not be told).
func (p *viaProvenance) record(input, skip map[string]bool, after []routeVia) {
	if p == nil {
		return
	}
	if p.input == nil {
		p.input, p.router, p.grown = map[string]bool{}, map[string]bool{}, map[string]bool{}
	}
	for id := range input {
		p.input[id] = true
	}
	for _, v := range after {
		if !skip[v.ID] {
			p.router[v.ID] = true
		}
	}
}

func (p *viaProvenance) createdBy() string {
	if p.created != "" {
		return p.created
	}
	return viaFromRouter
}

func (p *viaProvenance) recordGrown(ids []string) {
	if p == nil {
		return
	}
	for _, id := range ids {
		p.grown[id] = true
	}
}

// viaPolicy is the --via-policy file.
type viaPolicy struct {
	Contract   string         `json:"contract,omitempty"` // fab contract file, relative to the policy
	Exceptions []viaException `json:"exceptions,omitempty"`
}

// viaException declares one kind of via as an exception. neckdown-escape and
// stitching exempt from preferred size only (Nets: path.Match patterns, needed);
// microvia and blind-buried cover non-through vias and must state their minimums.
type viaException struct {
	Kind         string   `json:"kind"`
	Nets         []string `json:"nets,omitempty"`
	Reason       string   `json:"reason"`
	MinDrillMm   float64  `json:"minDrillMm,omitempty"`
	MinAnnularMm float64  `json:"minAnnularMm,omitempty"`
}

func (e viaException) validate() error {
	switch e.Kind {
	case "neckdown-escape", "stitching":
		if len(e.Nets) == 0 {
			return fmt.Errorf("%s exception without nets", e.Kind)
		}
	case "microvia", "blind-buried":
		if e.MinDrillMm <= 0 || e.MinAnnularMm <= 0 {
			return fmt.Errorf("%s exception needs minDrillMm and minAnnularMm", e.Kind)
		}
	default:
		return fmt.Errorf("unknown via exception kind %q", e.Kind)
	}
	if strings.TrimSpace(e.Reason) == "" {
		return fmt.Errorf("%s exception without a reason", e.Kind)
	}
	for _, n := range e.Nets {
		if _, err := path.Match(n, ""); err != nil {
			return fmt.Errorf("%s exception net pattern %q: %w", e.Kind, n, err)
		}
	}
	return nil
}

func (e viaException) covers(net string) bool {
	if len(e.Nets) == 0 {
		return true
	}
	for _, n := range e.Nets {
		if ok, _ := path.Match(n, net); ok {
			return true
		}
	}
	return false
}

// viaLimits are the manufacturing minimums for through vias (nm).
type viaLimits struct {
	drill, annular, diameter int64
}

// viaAuditRow is one via of the final board.
type viaAuditRow struct {
	ID         string   `json:"id"`
	Net        string   `json:"net"`
	Class      string   `json:"class,omitempty"`
	Span       string   `json:"span"` // through | non-through | unknown
	FromLayer  int      `json:"fromLayer"`
	ToLayer    int      `json:"toLayer"`
	DiameterMm float64  `json:"diameterMm"`
	DrillMm    float64  `json:"drillMm"`
	AnnularMm  float64  `json:"annularMm"`
	Provenance string   `json:"provenance"`
	Legal      string   `json:"legal"`     // yes | no | cannot-check
	Preferred  string   `json:"preferred"` // conforms | differs | unknown
	Violations []string `json:"violations,omitempty"`
	Exception  string   `json:"exception,omitempty"`
}

// viaAudit is the verdict over all rows.
type viaAudit struct {
	Rows        []viaAuditRow `json:"vias"`
	Illegal     []string      `json:"illegal,omitempty"`
	NonConform  []string      `json:"nonConforming,omitempty"`
	CannotCheck []string      `json:"cannotCheck,omitempty"`
	Excepted    []string      `json:"excepted,omitempty"`
	PreExisting []string      `json:"preExistingOffSize,omitempty"`
}

// viaMM converts mil to mm, to the nm.
func viaMM(mil float64) float64 { return math.Round(mil*nmPerMil) / 1e6 }

// auditVias judges every via. Pure: all inputs are read by the caller.
func auditVias(vias []routeVia, rb *kicad.RulesReadback, lim viaLimits, prov func(routeVia) string, pol viaPolicy) viaAudit {
	var a viaAudit
	for _, v := range vias {
		dia, drill := int64(math.Round(v.Dia*nmPerMil)), int64(math.Round(v.Drill*nmPerMil))
		e, resolved := rb.Nets[v.Net]
		row := viaAuditRow{ID: v.ID, Net: v.Net, Class: e.Class, FromLayer: v.From, ToLayer: v.To, DiameterMm: viaMM(v.Dia), DrillMm: viaMM(v.Drill),
			AnnularMm: viaMM((v.Dia - v.Drill) / 2), Provenance: prov(v), Legal: "yes", Span: viaSpan(v)}
		tag := fmt.Sprintf("%s via %s at (%.1f, %.1f) mil, %s", v.Net, short12(v.ID), v.X, v.Y, row.Provenance)
		add := func(list *[]string, msg string) {
			row.Violations = append(row.Violations, msg)
			*list = append(*list, tag+": "+msg)
		}

		// Legality: only a confirmed through via is judged by the board's
		// limits. Any other span needs declared minimums, and a via of unknown
		// origin gets no declaration.
		use := lim
		if row.Span != "through" {
			var ex *viaException
			if row.Provenance != viaFromUnknown {
				ex = findViaException(pol, v.Net, "microvia", "blind-buried")
			}
			switch {
			case ex != nil:
				row.Exception = ex.Kind + ": " + ex.Reason
				a.Excepted = append(a.Excepted, tag+": "+row.Exception)
				use = viaLimits{drill: int64(ex.MinDrillMm * 1e6), annular: int64(ex.MinAnnularMm * 1e6)}
			case row.Provenance == viaFromUnknown:
				row.Legal = "cannot-check"
				add(&a.CannotCheck, fmt.Sprintf("%s via (layers %d-%d) of unknown provenance: no exception applies", row.Span, v.From, v.To))
			case row.Span == "unknown":
				row.Legal = "cannot-check"
				add(&a.CannotCheck, fmt.Sprintf("via span unknown (layers %d-%d), not confirmed through, with no declared microvia / blind-buried minimums", v.From, v.To))
			default:
				row.Legal = "cannot-check"
				add(&a.CannotCheck, fmt.Sprintf("non-through via (layers %d-%d) with no declared microvia / blind-buried minimums", v.From, v.To))
			}
		}
		// A drill that leaves no copper is illegal on any span; the minimums
		// need limits that apply to this via. Same tolerance as the sizing step.
		if drill >= dia {
			add(&a.Illegal, fmt.Sprintf("drill %.3f mm is not smaller than the diameter %.3f mm", row.DrillMm, row.DiameterMm))
			row.Legal = "no"
		} else if row.Legal == "yes" {
			for _, c := range []struct {
				what     string
				val, min int64
			}{{"annular ring", (dia - drill) / 2, use.annular}, {"drill", drill, use.drill}, {"diameter", dia, use.diameter}} {
				if c.min > 0 && c.val+viaExactTolNM < c.min {
					add(&a.Illegal, fmt.Sprintf("%s %.3f mm below the minimum %.3f mm", c.what, float64(c.val)/1e6, float64(c.min)/1e6))
					row.Legal = "no"
				}
			}
		}

		// Preferred size: the net's class via.
		switch {
		case !resolved || e.ViaDiameter <= 0 || e.ViaDrill <= 0:
			row.Preferred = "unknown"
			if row.Provenance != viaFromInput {
				add(&a.CannotCheck, "the net resolves to no class via")
			}
		case absNM(dia-e.ViaDiameter) <= viaExactTolNM && absNM(drill-e.ViaDrill) <= viaExactTolNM:
			row.Preferred = "conforms"
		default:
			row.Preferred = "differs"
			msg := fmt.Sprintf("%.4f/%.4f mm differs from class %s via %.4f/%.4f mm", row.DiameterMm, row.DrillMm, e.Class, float64(e.ViaDiameter)/1e6, float64(e.ViaDrill)/1e6)
			var ex *viaException
			if row.Provenance != viaFromUnknown {
				ex = findViaException(pol, v.Net, "neckdown-escape", "stitching")
			}
			switch {
			case row.Provenance == viaFromInput:
				row.Violations = append(row.Violations, msg)
				a.PreExisting = append(a.PreExisting, tag+": "+msg)
			case ex != nil:
				row.Exception = ex.Kind + ": " + ex.Reason
				a.Excepted = append(a.Excepted, tag+": "+msg+" ("+row.Exception+")")
			case row.Provenance == viaFromEscape:
				row.Exception = "neckdown-escape: route's intent escape uses the Default via"
				a.Excepted = append(a.Excepted, tag+": "+msg+" ("+row.Exception+")")
			default:
				add(&a.NonConform, msg)
			}
		}
		a.Rows = append(a.Rows, row)
	}
	for _, l := range [][]string{a.Illegal, a.NonConform, a.CannotCheck, a.Excepted, a.PreExisting} {
		sort.Strings(l)
	}
	return a
}

func absNM(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// viaSpan: fromLayer/toLayer are the bridge's layer ids (F.Cu 1, B.Cu 2).
func viaSpan(v routeVia) string {
	switch {
	case v.From == 0 || v.To == 0:
		return "unknown"
	case (v.From == 1 && v.To == 2) || (v.From == 2 && v.To == 1):
		return "through"
	}
	return "non-through"
}

func findViaException(pol viaPolicy, net string, kinds ...string) *viaException {
	for i := range pol.Exceptions {
		e := &pol.Exceptions[i]
		for _, k := range kinds {
			if e.Kind == k && e.covers(net) {
				return e
			}
		}
	}
	return nil
}

// viaLimitsFromDRU raises lim by the custom rules on via size. A rule with a
// condition or a value that is not a number cannot be resolved per via.
func viaLimitsFromDRU(rules []kicad.DRURule, lim *viaLimits) (unresolved []string) {
	for _, u := range rules {
		for _, c := range u.Constraints {
			var field *int64
			switch c.Type {
			case "via_diameter":
				field = &lim.diameter
			case "hole_size":
				field = &lim.drill
			case "annular_width":
				field = &lim.annular
			default:
				continue
			}
			if c.Unparsed || c.Min == nil || u.Condition != "" {
				unresolved = append(unresolved, fmt.Sprintf("custom rule %q sets %s (condition %q): cannot be resolved per via", u.Name, c.Type, u.Condition))
				continue
			}
			if *c.Min > *field {
				*field = *c.Min
			}
		}
	}
	return unresolved
}

// viaAuditPolicy reads --via-policy and raises lim by its contract.
func viaAuditPolicy(file string, lim *viaLimits) (viaPolicy, error) {
	var pol viaPolicy
	if file == "" {
		return pol, nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return pol, err
	}
	if err := json.Unmarshal(b, &pol); err != nil {
		return pol, err
	}
	for _, e := range pol.Exceptions {
		if err := e.validate(); err != nil {
			return pol, err
		}
	}
	if pol.Contract != "" {
		cp := pol.Contract
		if !filepath.IsAbs(cp) {
			cp = filepath.Join(filepath.Dir(file), cp)
		}
		c, err := kicad.LoadFabContract(cp)
		if err != nil {
			return pol, err
		}
		lim.drill = max(lim.drill, int64(c.Limits.MinDrillMm*1e6))
		lim.annular = max(lim.annular, int64(c.Limits.MinAnnularMm*1e6))
	}
	return pol, nil
}

// viaAuditGate audits the final board and writes via-audit.json.
func (r *kicadRun) viaAuditGate(routed string, snap *boardSnapshot) gateResult {
	g := gateResult{Gate: "via-audit"}
	cannot := func(what string, err error) gateResult {
		g.CannotCheck = true
		g.Detail = "cannot check: " + what + ": " + err.Error()
		r.summary["viaAudit"] = map[string]any{"error": g.Detail}
		return g
	}
	vias, err := snapshotVias(snap)
	if err != nil {
		return cannot("the final board's vias", err)
	}
	rb, err := r.kt.RulesRead(routed)
	if err != nil {
		return cannot("KiCad's class readback", err)
	}
	lim := viaLimits{drill: rb.Minimums.ViaDrill, annular: rb.Minimums.ViaAnnular, diameter: rb.Minimums.ViaDiameter}
	pol, err := viaAuditPolicy(r.o.viaPolicy, &lim)
	if err != nil {
		return cannot("--via-policy", err)
	}
	var unresolved []string
	if b, err := os.ReadFile(boardSibling(routed, ".kicad_dru")); err == nil {
		rules, err := kicad.ParseDRU(string(b))
		if err != nil {
			return cannot("the custom rules", err)
		}
		unresolved = viaLimitsFromDRU(rules, &lim)
	} else if !os.IsNotExist(err) {
		return cannot("the custom rules", err)
	}
	a := auditVias(vias, rb, lim, r.viaProvenanceOf(), pol)
	a.CannotCheck = append(a.CannotCheck, unresolved...)
	file := filepath.Join(r.o.outDir, "via-audit.json")
	werr := writeJSONFile(file, a)
	r.summary["viaAudit"] = map[string]any{"file": file, "vias": len(a.Rows), "illegal": len(a.Illegal), "nonConforming": len(a.NonConform),
		"cannotCheck": len(a.CannotCheck), "excepted": len(a.Excepted), "preExistingOffSize": len(a.PreExisting)}
	viaAuditVerdict(&g, a)
	if werr != nil {
		it := "via-audit.json could not be written: " + werr.Error()
		g.Pass, g.CannotCheck = false, len(a.Illegal)+len(a.NonConform) == 0
		g.Items, g.Unchecked = append(g.Items, it), append(g.Unchecked, it)
	}
	return g
}

// viaAuditVerdict fills the gate from the audit.
func viaAuditVerdict(g *gateResult, a viaAudit) {
	g.Items = append(append(append([]string{}, a.Illegal...), a.NonConform...), a.CannotCheck...)
	g.Info = append(append([]string{}, a.Excepted...), a.PreExisting...)
	g.Pass = len(g.Items) == 0
	g.Unchecked = append([]string{}, a.CannotCheck...)
	g.CannotCheck = len(a.CannotCheck) > 0 && len(a.Illegal)+len(a.NonConform) == 0
	switch {
	case len(a.Illegal)+len(a.NonConform) > 0:
		g.Detail = fmt.Sprintf("%d of %d via(s) break a manufacturing minimum, %d off their class size, %d cannot be checked", len(a.Illegal), len(a.Rows), len(a.NonConform), len(a.CannotCheck))
	case len(a.CannotCheck) > 0:
		g.Detail = fmt.Sprintf("cannot check: %d item(s) over %d via(s)", len(a.CannotCheck), len(a.Rows))
	default:
		g.Detail = fmt.Sprintf("%d via(s) legal and at their class size (%d declared exception(s), %d pre-existing off size)", len(a.Rows), len(a.Excepted), len(a.PreExisting))
	}
}

// viaProvenanceOf names where each via came from: the ids the candidate's
// reconciliation saw,
// then route's escapes and via arrays by net and position.
func (r *kicadRun) viaProvenanceOf() func(routeVia) string {
	type at struct {
		net  string
		x, y float64
		from string
	}
	var placed []at
	for _, e := range r.escapes {
		if e.Via {
			placed = append(placed, at{e.Net, e.To[0], e.To[1], viaFromEscape})
		}
	}
	if p, ok := r.summary["viaArrays"].(*viaArrayPlan); ok && p != nil {
		for _, v := range p.Vias {
			placed = append(placed, at{v.Net, v.X, v.Y, viaFromArray})
		}
	}
	return func(v routeVia) string {
		if p := r.viaProv; p != nil {
			switch {
			case p.grown[v.ID]:
				return viaFromRepair
			case p.router[v.ID]:
				return p.createdBy()
			case p.input[v.ID]:
				return viaFromInput
			}
		}
		for _, q := range placed {
			if q.net == v.Net && math.Hypot(q.x-v.X, q.y-v.Y) <= specctra.MatchTolMil {
				return q.from
			}
		}
		return viaFromUnknown
	}
}
