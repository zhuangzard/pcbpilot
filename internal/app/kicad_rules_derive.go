package app

// kicad_rules_derive.go — the one derivation of KiCad design rules from the
// design intent, shared by `kicad route` (stage 2, pre-route gate) and
// `kicad rules`:
//
//	intent nets      → kicadRequirements (widths, clearance, via; pair
//	                   clearance on every net of an insulation pair)
//	insulation pairs → kicadIsolation (domain classes + clearance/creepage)
//	domain edges     → kicadEdges
//	(kicad rules)    → widths raised to the current-derived width
//	                   (planRulesWidths: sim / declared current × IPC-2221/
//	                   2152 on the stackup's copper)
//
// and one bridge call (bridge netclasses) that turns it into netclasses,
// patterns and .kicad_dru rules, never below the board's own minimums.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// kicadRulesInputs is a netclasses request before the bridge sees it.
type kicadRulesInputs struct {
	Reqs  map[string]kicad.NetRequirement
	Iso   []map[string]any
	Edges []map[string]any
}

// deriveKicadRules is the shared derivation. widths (nil for kicad route
// without kicad rules) raises a net's outer / inner width; it never lowers.
func deriveKicadRules(in *designIntent, intentPath string, widths map[string]rulesWidth) kicadRulesInputs {
	reqs := kicadRequirements(in)
	for net, w := range widths {
		r, ok := reqs[net]
		if !ok {
			continue
		}
		r.OuterMil = math.Max(r.OuterMil, w.OuterMil)
		r.InnerMil = math.Max(r.InnerMil, w.InnerMil)
		reqs[net] = r
	}
	return kicadRulesInputs{Reqs: reqs, Iso: kicadIsolation(in), Edges: kicadEdges(intentPath, in)}
}

// request is the bridge request (sizes: kicad rules only).
func (ri kicadRulesInputs) request(sizes *kicad.SizeSet) kicad.NetclassRequest {
	return kicad.NetclassRequest{Nets: ri.Reqs, Isolation: ri.Iso, Edges: ri.Edges, Sizes: sizes}
}

// sha is the identity of the derivation (the request body without the
// pre-defined sizes): kicad rules records it in its .kicad_dru block and
// kicad route compares its own derivation against it.
func (ri kicadRulesInputs) sha() string {
	b, _ := json.Marshal(ri.request(nil).Body())
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// applyKicadNetclasses writes ri into a copy of board at out through the
// bridge and refuses a result where a net does not resolve to its class
// (both commands' pre-route gate). Missing intent nets are dropped from
// ri.Reqs (they are not on the board).
func applyKicadNetclasses(t rulesNetclasser, board string, ri kicadRulesInputs, sizes *kicad.SizeSet, out string) (*kicad.NetclassFull, error) {
	res, err := t.NetclassesReq(board, ri.request(sizes), out)
	if err != nil {
		return res, fmt.Errorf("netclasses: %w", err)
	}
	if len(res.Mismatched) > 0 {
		return res, fmt.Errorf("%d net(s) do not resolve to their pcbpilot netclass: %s", len(res.Mismatched), strings.Join(res.Mismatched, "; "))
	}
	for _, n := range res.MissingNets {
		delete(ri.Reqs, n)
	}
	return res, nil
}

// rulesNetclasser is the bridge call applyKicadNetclasses needs.
type rulesNetclasser interface {
	NetclassesReq(pcb string, req kicad.NetclassRequest, out string) (*kicad.NetclassFull, error)
}

// ── width basis (kicad rules) ──────────────────────────────────────────────

// rulesWidth is a current-derived width (mil).
type rulesWidth struct {
	OuterMil float64 `json:"outerMil"`
	InnerMil float64 `json:"innerMil"`
}

// rulesCopper is the copper the current rule is evaluated on.
type rulesCopper struct {
	OuterOz     float64               `json:"outerOz"`
	InnerOz     float64               `json:"innerOz"`
	TempRiseC   float64               `json:"tempRiseC"`
	Source      string                `json:"source"` // stackup | intent | kicad-default
	Stackup     []kicad.StackupCopper `json:"stackup,omitempty"`
	Assumptions []string              `json:"assumptions,omitempty"`
}

// kicadDefaultCopperMm is KiCad's default copper thickness (35 µm, 1 oz).
const kicadDefaultCopperMm = 0.035

// copperBasis: the copper weights from the board stackup (outer = the
// thinner of the two outer layers, inner = the thinnest inner layer), else
// the intent copper block, else KiCad's default; the temperature rise from
// the intent (else 10 °C). Every default is an assumption in the report.
func copperBasis(stack []kicad.StackupCopper, in *designIntent) rulesCopper {
	c := rulesCopper{Stackup: stack}
	oz := func(mm float64) float64 { return math.Round(mm/kicadDefaultCopperMm*1000) / 1000 }
	// Per-layer fallback for a layer the stackup gives no thickness: the
	// intent copper block (outer / inner weight), else KiCad's 35 µm.
	fbOuter, fbInner, fbSrc := 1.0, 1.0, "kicad-default"
	if in != nil && in.Copper != nil && in.Copper.OuterOz > 0 {
		fbOuter, fbInner, fbSrc = in.Copper.OuterOz, in.Copper.InnerOz, "intent"
		if fbInner <= 0 {
			fbInner = fbOuter
		}
	}
	fbWhy := map[string]string{"intent": "the intent copper block", "kicad-default": "KiCad's default 0.035 mm (1 oz)"}[fbSrc]
	// Every layer is resolved on its own: known thicknesses stay, only the
	// missing ones fall back; then outer = the thinner outer layer, inner =
	// the thinnest inner layer.
	outer, inner := 0.0, 0.0
	known, missing := 0, 0
	for i, l := range stack {
		isOuter := i == 0 || i == len(stack)-1
		w := oz(l.ThicknessMm)
		if l.ThicknessMm <= 0 {
			missing++
			w = fbInner
			if isOuter {
				w = fbOuter
			}
			c.Assumptions = append(c.Assumptions, fmt.Sprintf("stackup layer %s has no copper thickness: %g oz from %s", l.Layer, w, fbWhy))
		} else {
			known++
		}
		if isOuter {
			if outer == 0 || w < outer {
				outer = w
			}
		} else if inner == 0 || w < inner {
			inner = w
		}
	}
	switch {
	case len(stack) == 0:
		c.Source, outer, inner = fbSrc, fbOuter, fbInner
		c.Assumptions = append(c.Assumptions, "the board has no stackup: copper weight from "+fbWhy)
	case missing == 0:
		c.Source = "stackup"
	case known == 0:
		c.Source = fbSrc
	default:
		c.Source = "stackup+" + fbSrc
	}
	if inner == 0 {
		inner = outer
		if len(stack) == 0 {
			inner = fbInner
		}
	}
	c.OuterOz, c.InnerOz = outer, inner
	c.TempRiseC = 10
	if in != nil && in.Copper != nil && in.Copper.TempRiseC > 0 {
		c.TempRiseC = in.Copper.TempRiseC
	} else {
		c.Assumptions = append(c.Assumptions, "the intent gives no allowed temperature rise: 10 °C")
	}
	return c
}

// metricCeilMil is a width rounded up to the 0.05 mm grid (mil, 2 decimals).
func metricCeilMil(mil float64) float64 {
	if mil <= 0 {
		return 0
	}
	// Kept to 1e-6 mil so the bridge's mil → nm conversion lands exactly on
	// the grid (0.6 mm, not 0.599948 mm from a 2-decimal 23.62 mil).
	mm := math.Ceil(mil*0.0254/0.05-1e-9) * 0.05
	return math.Round(mm/0.0254*1e6) / 1e6
}

// rulesCurrent is the current a net's width is sized for.
type rulesCurrent struct {
	A        float64 `json:"a"`
	Source   string  `json:"source"` // sim | declared | intent
	Scenario string  `json:"scenario,omitempty"`
	Why      string  `json:"why"`
}

// declaredCurrents are machine-readable currents from --requirements JSON
// files ({"nets": {NET: {"peakA", "rmsA"}}}).
type declaredCurrents map[string]struct {
	PeakA float64 `json:"peakA"`
	RmsA  float64 `json:"rmsA"`
}

// loadDeclaredCurrents reads every .json requirements file's "nets"; other
// files (requirement documents) carry no machine-readable current.
func loadDeclaredCurrents(paths []string) (declaredCurrents, []string, error) {
	return declaredCurrentsFrom(paths, os.ReadFile)
}

// declaredCurrentsFrom is loadDeclaredCurrents over bytes from read (kicad
// rules passes the bytes it hashed, never a fresh read of the path).
func declaredCurrentsFrom(paths []string, read func(string) ([]byte, error)) (declaredCurrents, []string, error) {
	out := declaredCurrents{}
	var notes []string
	for _, p := range paths {
		if !strings.EqualFold(filepath.Ext(p), ".json") {
			notes = append(notes, p+": not JSON, no machine-readable current (bound to the plan by its hash only)")
			continue
		}
		b, err := read(p)
		if err != nil {
			return nil, nil, err
		}
		var f struct {
			Nets declaredCurrents `json:"nets"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", p, err)
		}
		for n, v := range f.Nets {
			if v.PeakA < 0 || v.RmsA < 0 || math.IsNaN(v.PeakA+v.RmsA) || math.IsInf(v.PeakA+v.RmsA, 0) {
				return nil, nil, fmt.Errorf("%s: net %s: negative or non-finite current", p, n)
			}
			cur := out[n]
			cur.PeakA, cur.RmsA = math.Max(cur.PeakA, v.PeakA), math.Max(cur.RmsA, v.RmsA)
			out[n] = cur
		}
	}
	return out, notes, nil
}

// rulesNetBasis is the width derivation of one intent net.
type rulesNetBasis struct {
	Net          string        `json:"net"`
	Role         string        `json:"role,omitempty"`
	Intent       intentWidth   `json:"intentWidthMil"`
	Current      *rulesCurrent `json:"current,omitempty"`
	NeedsCurrent bool          `json:"needsCurrent"`
	CurrentWidth *rulesWidth   `json:"currentWidthMil,omitempty"`
	Width        rulesWidth    `json:"requiredWidthMil"`
	Basis        []string      `json:"basis"`
	// Status: pass, cannot-check (a width that needs a current has none) or
	// fail (the current needs a width the impedance / pair forbids).
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

// jsonNumber decodes a raw JSON value that must be an actual number (not
// null, not a string): ok=false otherwise. Negative / non-finite values
// are not currents.
func jsonNumber(m json.RawMessage) (float64, bool) {
	var v *float64
	if len(m) == 0 || json.Unmarshal(m, &v) != nil || v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) || *v < 0 {
		return 0, false
	}
	return *v, true
}

// simNetCurrent is one net's current from a sim power file.
type simNetCurrent struct {
	A        float64
	Scenario string
}

// simCurrents is the per-net WORST CASE over every result of a sim power
// file (the "worst" result included): a result counts for a net only with
// an actual JSON number — its currentA, else Σsource / Σsink of its pins'
// numeric currents. Evidence and value come from the same pass, so an
// empty entry ("VCC": {}) or a null never makes an evidenced zero while
// another scenario carries 2 A. Nets without any number are absent.
func simCurrents(raw []byte) map[string]simNetCurrent {
	var f struct {
		Results []struct {
			Scenario string                                `json:"scenario"`
			Nets     map[string]map[string]json.RawMessage `json:"nets"`
		} `json:"results"`
	}
	out := map[string]simNetCurrent{}
	if json.Unmarshal(raw, &f) != nil {
		return out
	}
	for _, r := range f.Results {
		for net, fields := range r.Nets {
			a, ok := jsonNumber(fields["currentA"])
			if !ok || a == 0 {
				var pins []map[string]json.RawMessage
				if json.Unmarshal(fields["pins"], &pins) == nil {
					src, snk, any := 0.0, 0.0, false
					for _, p := range pins {
						v, vok := jsonNumber(p["currentA"])
						if !vok {
							continue
						}
						any = true
						var dir string
						_ = json.Unmarshal(p["dir"], &dir)
						switch dir {
						case "source":
							src += v
						case "sink":
							snk += v
						}
					}
					if any {
						a, ok = math.Max(math.Max(src, snk), a), true
					}
				}
			}
			if !ok {
				continue
			}
			if cur, seen := out[net]; !seen || a > cur.A {
				out[net] = simNetCurrent{A: a, Scenario: r.Scenario}
			}
		}
	}
	return out
}

// simEvidence lists the nets simCurrents gives a current for.
func simEvidence(raw []byte) map[string]bool {
	out := map[string]bool{}
	for n := range simCurrents(raw) {
		out[n] = true
	}
	return out
}

// rulesSimNet is the resolved sim current of one net: with cur (from
// simCurrents on the raw file) its worst case over every result, else the
// result pcbauto.ParseSim selected (tests); a switch node's ripple RMS
// governs when larger.
func rulesSimCurrent(sp *pcbauto.SimPower, net string, cur ...map[string]simNetCurrent) (rulesCurrent, bool) {
	if sp == nil {
		return rulesCurrent{}, false
	}
	if len(cur) > 0 && cur[0] != nil {
		c, ok := cur[0][net]
		if !ok {
			return rulesCurrent{}, false
		}
		a := c.A
		why := fmt.Sprintf("sim worst case over all results DC %.3f A (%s)", a, c.Scenario)
		if r, ok := sp.Ripple[net]; ok && r.IRmsA > a {
			a = r.IRmsA
			why += fmt.Sprintf(", ripple RMS %.3f A governs", r.IRmsA)
		}
		return rulesCurrent{A: a, Source: "sim", Scenario: c.Scenario, Why: why}, true
	}
	sn := sp.Nets[net]
	if sn == nil {
		return rulesCurrent{}, false
	}
	a := sn.CurrentA
	if a <= 0 {
		src, snk := 0.0, 0.0
		for _, p := range sn.Pins {
			switch p.Dir {
			case "source":
				src += p.CurrentA
			case "sink":
				snk += p.CurrentA
			}
		}
		a = math.Max(src, snk)
	}
	why := fmt.Sprintf("sim %s DC %.3f A", sp.Scenario, a)
	if r, ok := sp.Ripple[net]; ok && r.IRmsA > a {
		a = r.IRmsA
		why += fmt.Sprintf(", ripple RMS %.3f A governs", r.IRmsA)
	}
	scen := sp.Scenario
	if sn.Scenario != "" {
		scen += "/" + sn.Scenario
	}
	return rulesCurrent{A: a, Source: "sim", Scenario: scen, Why: why}, true
}

// planRulesWidths derives every intent net's width basis: the current is
// the larger of the sim DC current (or a switch node's ripple RMS) and a
// declared peak / RMS (requirements, intent peakA / rmsA); without either,
// the intent's own simulated or declared currentA. The width is
// max(intent widthMil, IPC-2221/2152 width of that current on the copper
// basis, rounded up to 0.05 mm). A power / ground / switch net that carries
// current but has no number (or a pulsed net without a declared peak/RMS)
// is cannot-check; its intent width (the floor) still applies. A current
// width above an impedance-controlled or paired net's width is a failure:
// the two requirements are incompatible, never silently maxed.
//
// simHas (from simEvidence on the raw sim file) says which nets the sim
// actually gives a current for; without it every net of sp counts (tests).
// An entry without a number ("VCC": {}) is no evidence, never a zero.
func planRulesWidths(in *designIntent, sp *pcbauto.SimPower, decl declaredCurrents, cu rulesCopper, simCur ...map[string]simNetCurrent) (map[string]rulesWidth, []rulesNetBasis) {
	var sc map[string]simNetCurrent
	if len(simCur) > 0 {
		sc = simCur[0]
	}
	widths := map[string]rulesWidth{}
	var out []rulesNetBasis
	for _, name := range in.sortedNetNames() {
		n := in.Nets[name]
		if n == nil || n.WidthMil.Outer <= 0 {
			continue
		}
		b := rulesNetBasis{Net: name, Role: n.Role, Intent: n.WidthMil, Status: "pass"}
		if b.Intent.Inner <= 0 {
			b.Intent.Inner = b.Intent.Outer
		}
		b.Width = rulesWidth{OuterMil: b.Intent.Outer, InnerMil: b.Intent.Inner}
		b.Basis = append(b.Basis, fmt.Sprintf("intent widthMil outer %g / inner %g / min %g mil (floor)", b.Intent.Outer, b.Intent.Inner, n.WidthMil.Min))
		powerish := n.Role == "power" || n.Role == "ground" || n.Role == "switch"
		// Every power / ground / switch net's width depends on its current:
		// it needs evidence, and an evidenced zero (a sim result, a
		// simulated / declared currentA of 0) is not a missing value.
		b.NeedsCurrent = powerish
		var cur *rulesCurrent
		evidencedZero := false
		if c, ok := rulesSimCurrent(sp, name, sc); ok {
			cur = &c
			evidencedZero = c.A <= 0
		}
		dp, dr := math.Max(n.PeakA, decl[name].PeakA), math.Max(n.RmsA, decl[name].RmsA)
		if d := math.Max(dp, dr); d > 0 && (cur == nil || d > cur.A) {
			cur = &rulesCurrent{A: d, Source: "declared", Why: fmt.Sprintf("declared peak %.3f A / RMS %.3f A", dp, dr)}
		}
		if (cur == nil || cur.A <= 0) && (n.CurrentSource == "simulated" || n.CurrentSource == "declared") {
			switch {
			case n.CurrentA > 0:
				cur = &rulesCurrent{A: n.CurrentA, Source: "intent", Why: fmt.Sprintf("intent currentA %.3f A (%s; no --sim given)", n.CurrentA, n.CurrentSource)}
			case n.currentAGiven:
				// Only an explicit 0 is an evidenced zero; an absent
				// currentA is missing evidence.
				evidencedZero = true
			}
		}
		if cur != nil && cur.A > 0 {
			evidencedZero = false
		}
		switch {
		case n.Pulsed && dp <= 0 && dr <= 0:
			b.Status, b.Detail = "cannot-check", "pulsed / inrush net without a declared peakA or rmsA: copper adequacy cannot be judged (the intent floor still applies)"
		case b.NeedsCurrent && (cur == nil || cur.A <= 0) && !evidencedZero:
			b.Status, b.Detail = "cannot-check", fmt.Sprintf("%s net without current evidence (currentA %g, source %s; no --sim result, no declared current): the width is not verified (the intent floor still applies)", n.Role, n.CurrentA, firstNonEmptyStr(n.CurrentSource, "unknown"))
		case b.NeedsCurrent && evidencedZero:
			b.Basis = append(b.Basis, "evidenced zero current (simulation / declared): the intent width governs")
		}
		if cur != nil && cur.A > 0 {
			b.Current = cur
			cw := rulesWidth{OuterMil: metricCeilMil(pcbauto.TraceWidthForCurrent(cur.A, cu.TempRiseC, cu.OuterOz, false)),
				// Inner layers: IPC-2221's internal-conductor constant.
				InnerMil: metricCeilMil(pcbauto.TraceWidthForCurrent(cur.A, cu.TempRiseC, cu.InnerOz, true))}
			b.CurrentWidth = &cw
			b.Basis = append(b.Basis, fmt.Sprintf("%s × IPC-2221 ΔT %g °C → %g mil outer (external, %g oz), %g mil inner (internal, %g oz), 0.05 mm grid", cur.Why, cu.TempRiseC, cw.OuterMil, cu.OuterOz, cw.InnerMil, cu.InnerOz))
			controlled := n.ImpedanceOhm > 0 || n.DiffPair != "" || n.Role == "diff" || n.Role == "rf" || n.Role == "hs"
			switch {
			case controlled && (cw.OuterMil > b.Intent.Outer+reqEpsMil || cw.InnerMil > b.Intent.Inner+reqEpsMil):
				// Either layer group: nothing is raised on any layer.
				b.Status = "fail"
				b.Detail = fmt.Sprintf("incompatible requirements: the current needs %g mil outer / %g mil inner, the impedance / pair width is %g / %g mil", cw.OuterMil, cw.InnerMil, b.Intent.Outer, b.Intent.Inner)
			default:
				if cw.OuterMil > b.Width.OuterMil+reqEpsMil {
					b.Width.OuterMil = cw.OuterMil
					b.Basis = append(b.Basis, fmt.Sprintf("outer: the current width %g mil governs (intent %g)", cw.OuterMil, b.Intent.Outer))
				} else {
					b.Basis = append(b.Basis, fmt.Sprintf("outer: the intent width %g mil governs (current needs %g)", b.Intent.Outer, cw.OuterMil))
				}
				if cw.InnerMil > b.Width.InnerMil+reqEpsMil {
					b.Width.InnerMil = cw.InnerMil
					b.Basis = append(b.Basis, fmt.Sprintf("inner: the current width %g mil governs (intent %g)", cw.InnerMil, b.Intent.Inner))
				}
			}
		} else if !b.NeedsCurrent {
			b.Basis = append(b.Basis, "no current to size for: the intent width governs")
		}
		if b.Status == "fail" {
			out = append(out, b)
			continue
		}
		if b.Width.OuterMil > b.Intent.Outer+reqEpsMil || b.Width.InnerMil > b.Intent.Inner+reqEpsMil {
			widths[name] = b.Width
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Net < out[j].Net })
	return widths, out
}
