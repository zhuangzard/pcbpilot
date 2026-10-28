package app

// kicad_rules_block.go — the .kicad_dru block `kicad rules` owns, and the
// check `kicad route` makes against it.
//
// The block sits after the block bridge netclasses writes (the "route
// block"): KiCad applies the last matching rule, so the route block (whose
// inner rule may only carry the board minimum) can never weaken these
// floors. A route run re-appends its block at the end; kicad route then
// moves this block back behind it (rulesStamp matched). It carries:
//
//	# request-sha256 <hex>    the shared derivation it was made from
//	# sizes-owned tracks=<nm,…> vias=<dia:drill,…>
//	(rule "pcbpilot-rules <class> outer" (layer outer) … track_width min/opt)
//	(rule "pcbpilot-rules <class> inner" (layer inner) … track_width min/opt)
//
// A netclass width is a routing default, not a minimum; these rules make the
// floor a DRC rule on every layer.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// rulesStamp is what a rules block records.
type rulesStamp struct {
	RequestSHA string
	Owned      kicad.SizeSet
}

// parseRulesStamp reads the stamp of the rules block of a .kicad_dru text
// (ok=false: no rules block).
func parseRulesStamp(dru string) (rulesStamp, bool) {
	_, blk, _, err := kicad.SplitDRU(dru)
	if err != nil || blk == "" {
		return rulesStamp{}, false
	}
	var st rulesStamp
	for _, l := range strings.Split(blk, "\n") {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(l), "#"))
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "request-sha256":
			st.RequestSHA = f[1]
		case "sizes-owned":
			for _, kv := range f[1:] {
				k, v, _ := strings.Cut(kv, "=")
				for _, item := range strings.Split(v, ",") {
					if item == "" {
						continue
					}
					switch k {
					case "tracks":
						if n, err := strconv.ParseInt(item, 10, 64); err == nil {
							st.Owned.Tracks = append(st.Owned.Tracks, n)
						}
					case "vias":
						a, b, _ := strings.Cut(item, ":")
						d, e1 := strconv.ParseInt(a, 10, 64)
						r, e2 := strconv.ParseInt(b, 10, 64)
						if e1 == nil && e2 == nil {
							st.Owned.Vias = append(st.Owned.Vias, [2]int64{d, r})
						}
					}
				}
			}
		}
	}
	return st, st.RequestSHA != ""
}

// rulesBlockRule is one rule of the rules block.
type rulesBlockRule struct {
	Name  string `json:"name"`
	Class string `json:"class"`
	Layer string `json:"layer"` // outer | inner
	MinNm int64  `json:"minNm"`
	OptNm int64  `json:"optNm"`
	// MaxNm: a user rule's track_width maximum carried over (0 = none):
	// KiCad's last match replaces the whole constraint, so the owned rule
	// keeps the user's max instead of dropping it.
	MaxNm   int64  `json:"maxNm,omitempty"`
	MaxFrom string `json:"maxFrom,omitempty"`
	Comment string `json:"why"`
}

// rulesBlockRules are the track_width rules for the written classes: the
// floor is the class's intent minimum (the neck-down limit) when it has
// one, else the layer's full width; opt is the layer's width. Inner rules
// only on boards with inner layers.
func rulesBlockRules(classes []kicad.NetclassClassNM, copperLayers int, maxes ...trackMaxes) []rulesBlockRule {
	var mx trackMaxes
	if len(maxes) > 0 {
		mx = maxes[0]
	}
	var out []rulesBlockRule
	for _, c := range classes {
		if !strings.HasPrefix(c.Name, "PP") || strings.HasPrefix(c.Name, "PPD_") || strings.HasPrefix(c.Name, "PPF_") {
			continue
		}
		q := c.NM
		floor := func(w int64) (int64, string) {
			if q.Min > 0 && q.Min < w {
				return q.Min, "intent minimum (neck-down limit)"
			}
			return w, "full width (the intent allows no neck-down)"
		}
		mn, why := floor(q.Track)
		m := mx.get(c.Name, "outer")
		out = append(out, rulesBlockRule{Name: kicad.DRURulesPrefix + c.Name + " outer", Class: c.Name, Layer: "outer", MinNm: mn, OptNm: q.Track, MaxNm: m.Nm, MaxFrom: m.Rule, Comment: why})
		if copperLayers > 2 {
			inner := q.Inner
			if inner <= 0 {
				inner = q.Track
			}
			mn, why := floor(inner)
			m := mx.get(c.Name, "inner")
			out = append(out, rulesBlockRule{Name: kicad.DRURulesPrefix + c.Name + " inner", Class: c.Name, Layer: "inner", MinNm: mn, OptNm: inner, MaxNm: m.Nm, MaxFrom: m.Rule, Comment: why})
		}
	}
	return out
}

// rulesBlockText renders the block (with markers, ending in a newline).
// It carries no plan revision (which hashes the input .kicad_dru): the
// same derivation must give the same bytes, so re-applying is a no-op.
func rulesBlockText(rules []rulesBlockRule, requestSHA string, owned kicad.SizeSet) string {
	var b strings.Builder
	b.WriteString(kicad.DRURulesBegin + "\n")
	b.WriteString("# owner: pcbpilot kicad rules (rules named \"" + kicad.DRURulesPrefix + "...\"); re-applying replaces this block\n")
	fmt.Fprintf(&b, "# request-sha256 %s\n", requestSHA)
	var ts, vs []string
	for _, t := range owned.Tracks {
		ts = append(ts, strconv.FormatInt(t, 10))
	}
	for _, v := range owned.Vias {
		vs = append(vs, fmt.Sprintf("%d:%d", v[0], v[1]))
	}
	fmt.Fprintf(&b, "# sizes-owned tracks=%s vias=%s\n", strings.Join(ts, ","), strings.Join(vs, ","))
	for _, r := range rules {
		mx := ""
		if r.MaxNm > 0 {
			mx = " (max " + kicad.MmOfNm(r.MaxNm) + ")"
		}
		fmt.Fprintf(&b, "(rule \"%s\"\n  (layer %s)\n  (condition \"A.hasNetclass('%s')\")\n  (constraint track_width (min %s) (opt %s)%s))\n",
			r.Name, r.Layer, r.Class, kicad.MmOfNm(r.MinNm), kicad.MmOfNm(r.OptNm), mx)
	}
	b.WriteString(kicad.DRURulesEnd + "\n")
	return b.String()
}

// placeRulesBlock puts block into a .kicad_dru text: the old rules block is
// removed and the new one goes last, after the user part and the route
// block (the last matching rule wins in KiCad).
func placeRulesBlock(dru, block string) (string, error) {
	// LF only: KiCad's Python writes the route block in text mode (CRLF on
	// Windows); the same rules must give the same bytes on every platform.
	dru = strings.ReplaceAll(dru, "\r\n", "\n")
	user, _, route, err := kicad.SplitDRU(dru)
	if err != nil {
		return "", err
	}
	text := strings.TrimRight(user, "\n")
	if text != "" {
		text += "\n"
	}
	if !strings.Contains(text, "(version") {
		text = "(version 1)\n" + text
	}
	return text + route + block, nil
}

// rulesTrackFloor resolves the track_width minimum KiCad applies to class
// cls on one copper layer ("F.Cu", "In2.Cu", "B.Cu"; "outer" / "inner"
// stand for F.Cu / In1.Cu) from a parsed .kicad_dru: a rule applies when
// it has no (layer …), names that layer, or names its group (outer =
// F.Cu / B.Cu, inner = InN.Cu); the last
// matching rule wins — a pcbpilot-owned rule whose condition is exactly
// A.hasNetclass('cls'), or any user rule not provably disjoint from names
// (cls, its nets: druDisjoint); a user rule without a comparable minimum
// resolves to 0 (unknown). 0 = none.
func rulesTrackFloor(rules []kicad.DRURule, cls, layer string, names ...map[string]bool) (int64, string) {
	f := rulesTrackResolve(rules, cls, layer, names...)
	return f.Min, f.Rule
}

// trackFloor is a resolved track_width minimum.
type trackFloor struct {
	// Min / Rule: the worst (lowest) minimum KiCad may apply and the rule
	// giving it; 0 = none.
	Min  int64
	Rule string
	// Possible: Min comes from a rule that only possibly matches the net (a
	// property or wildcard condition), not one that provably does.
	Possible bool
	// Unknown: a rule that may apply has no comparable minimum.
	Unknown bool
}

// rulesTrackResolve is KiCad's last-match resolution with uncertainty: a
// rule DEFINITELY matches the net (a pcbpilot-owned rule on exactly
// A.hasNetclass('cls'), or a user rule whose condition is only literal
// net / class terms all naming the net) or POSSIBLY matches it (any user
// rule not provably disjoint). The effective minimum is the last definite
// match, or any possible match after it — so the result is the lowest of
// those: a possibly matching stronger rule never erases a weaker one.
func rulesTrackResolve(rules []kicad.DRURule, cls, layer string, names ...map[string]bool) trackFloor {
	cond := "A.hasNetclass('" + cls + "')"
	nm := map[string]bool{cls: true}
	for _, m := range names {
		for k := range m {
			nm[k] = true
		}
	}
	switch layer {
	case "outer":
		layer = "F.Cu"
	case "inner":
		layer = "In1.Cu"
	}
	type cand struct {
		min      int64
		rule     string
		possible bool
		unknown  bool
	}
	var cands []cand // since the last definite match, that match first
	for _, r := range rules {
		if !ruleOnLayer(r.Layer, layer) {
			continue
		}
		definite := false
		switch {
		case r.Block != "":
			if r.Condition != cond {
				continue
			}
			definite = true
		case len(names) == 0 || druDisjoint(r.Condition, nm):
			continue
		default:
			definite = druDefinite(r.Condition, nm)
		}
		for _, c := range r.Constraints {
			if c.Type != "track_width" {
				continue
			}
			k := cand{rule: r.Name, possible: !definite}
			switch {
			case c.Min != nil:
				k.min = *c.Min
			case r.Block == "":
				k.unknown = true // a user rule without a comparable minimum
			default:
				continue // an owned rule without a minimum sets none
			}
			if definite {
				cands = []cand{k}
			} else {
				cands = append(cands, k)
			}
		}
	}
	var f trackFloor
	for _, k := range cands {
		if k.unknown {
			f.Unknown = true
			continue
		}
		if f.Rule == "" || k.min < f.Min {
			f.Min, f.Rule, f.Possible = k.min, k.rule, k.possible
		}
	}
	if f.Unknown && f.Rule == "" {
		f.Rule = cands[len(cands)-1].rule
	}
	return f
}

var reDefiniteTerm = regexp.MustCompile(`^A\.(?:(?:NetName|NetClass)\s*==\s*'([^']*)'|hasNetclass\('([^']*)'\)|(?:NetName|NetClass)\s*==\s*"([^"]*)")$`)

// druDefinite: a unary condition certainly matches an item named in names
// — empty, or one '||' alternative made only of A literal net / class
// terms (no wildcard) that all name it. Anything else is only possible.
func druDefinite(cond string, names map[string]bool) bool {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true
	}
	trim := func(s string) string {
		s = strings.TrimSpace(s)
		for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") && balanced(s[1:len(s)-1]) {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
		return s
	}
	for _, alt := range splitTop(trim(cond), "||") {
		ok := true
		for _, term := range splitTop(trim(alt), "&&") {
			m := reDefiniteTerm.FindStringSubmatch(trim(term))
			if m == nil {
				ok = false
				break
			}
			lit := m[1] + m[2] + m[3]
			if strings.ContainsAny(lit, "*?") || !names[lit] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// trackMax is a carried user maximum: the strictest max of the user rules
// that may govern a class on a layer group.
type trackMax struct {
	Nm   int64
	Rule string
}

// trackMaxes: class → layer group → carried max.
type trackMaxes map[string]map[string]trackMax

func (m trackMaxes) get(cls, group string) trackMax {
	if m == nil {
		return trackMax{}
	}
	return m[cls][group]
}

// maxConflict is a user track_width maximum the plan cannot keep.
type maxConflict struct {
	Rule   string
	Class  string
	Group  string
	PlanNm int64
	MaxNm  int64
	Nets   []string
	Detail string
}

// trackMaxCarry: for every pcbpilot width class and layer group, the user
// rules (outside pcbpilot's blocks, not replaced) whose track_width has a
// max and that may govern one of the class's nets on that group. Their
// strictest max is carried into the class's owned rule when the plan fits
// under it (floor and width ≤ max); a plan above it, or a max pcbpilot
// cannot compare, is a conflict naming the rule. identity gives a net's
// complete name set (net, classes, planned PP / PPD classes).
func trackMaxCarry(rules []kicad.DRURule, classes []kicad.NetclassClassNM, copperLayers int, identity func(net string) map[string]bool, replaced map[string]bool) (trackMaxes, []maxConflict) {
	out := trackMaxes{}
	var bad []maxConflict
	groups := []string{"outer"}
	if copperLayers > 2 {
		groups = append(groups, "inner")
	}
	for _, c := range classes {
		if !reGeneratedClass.MatchString(c.Name) || strings.HasPrefix(c.Name, "PPD_") || strings.HasPrefix(c.Name, "PPF_") {
			continue
		}
		for _, g := range groups {
			plan := c.NM.Track
			if g == "inner" && c.NM.Inner > 0 {
				plan = c.NM.Inner
			}
			for _, u := range rules {
				if u.Block != "" || replaced[u.Name] {
					continue
				}
				on := false
				for _, l := range ruleLayers(u.Layer, copperLayers) {
					on = on || l == g
				}
				if !on {
					continue
				}
				var nets []string
				for _, n := range c.Nets {
					if !druDisjoint(u.Condition, identity(n)) {
						nets = append(nets, n)
					}
				}
				if len(nets) == 0 {
					continue
				}
				for _, k := range u.Constraints {
					if k.Type != "track_width" || k.Max == nil && !k.Unparsed {
						continue
					}
					if k.Max == nil {
						continue // an uncomparable value: the minimum check reports it
					}
					if plan > *k.Max || expectedFloor(c.NM, g) > *k.Max {
						bad = append(bad, maxConflict{Rule: u.Name, Class: c.Name, Group: g, PlanNm: plan, MaxNm: *k.Max, Nets: nets,
							Detail: fmt.Sprintf("the user rule limits track_width to max %s on %s layers for %s; the plan needs %s (class %s): incompatible requirements", kicad.MmOfNm(*k.Max), g, strings.Join(nets, ", "), kicad.MmOfNm(plan), c.Name)})
						continue
					}
					if cur, ok := out[c.Name][g]; !ok || *k.Max < cur.Nm {
						if out[c.Name] == nil {
							out[c.Name] = map[string]trackMax{}
						}
						out[c.Name][g] = trackMax{Nm: *k.Max, Rule: u.Name}
					}
				}
			}
		}
	}
	return out, bad
}

// rulesTrackMaxResolve is the track_width maximum KiCad applies to class cls
// on one copper layer, with the definite / possible semantics of
// rulesTrackResolve: the last definite match's max (0 = that rule sets
// none) and every possible match's after it. Strict is the tightest max of
// those (for judging copper: a segment above a definite strict max fails,
// above a possible one is cannot-check); Weak is the loosest (for judging
// that a user max survives: 0 = some candidate sets no max).
type trackMaxResult struct {
	Strict, Weak                      int64
	StrictRule, WeakRule              string
	StrictPossible, WeakPossible, Any bool
}

func rulesTrackMaxResolve(rules []kicad.DRURule, cls, layer string, names map[string]bool) trackMaxResult {
	cond := "A.hasNetclass('" + cls + "')"
	nm := map[string]bool{cls: true}
	for k := range names {
		nm[k] = true
	}
	type cand struct {
		max      int64
		rule     string
		possible bool
	}
	var cands []cand
	for _, r := range rules {
		if !ruleOnLayer(r.Layer, layer) {
			continue
		}
		definite := false
		switch {
		case r.Block != "":
			if r.Condition != cond {
				continue
			}
			definite = true
		case druDisjoint(r.Condition, nm):
			continue
		default:
			definite = druDefinite(r.Condition, nm)
		}
		for _, c := range r.Constraints {
			if c.Type != "track_width" {
				continue
			}
			k := cand{rule: r.Name, possible: !definite}
			if c.Max != nil {
				k.max = *c.Max
			}
			if definite {
				cands = []cand{k}
			} else {
				cands = append(cands, k)
			}
		}
	}
	var res trackMaxResult
	for i, k := range cands {
		res.Any = true
		if k.max > 0 && (res.Strict == 0 || k.max < res.Strict) {
			res.Strict, res.StrictRule, res.StrictPossible = k.max, k.rule, k.possible
		}
		if i == 0 || res.Weak != 0 && (k.max == 0 || k.max > res.Weak) {
			res.Weak, res.WeakRule, res.WeakPossible = k.max, k.rule, k.possible
		}
	}
	return res
}

// ruleOnLayer: a rule's (layer …) covers the copper layer.
func ruleOnLayer(ruleLayer, layer string) bool {
	switch ruleLayer {
	case "", layer:
		return true
	case "outer":
		return layer == "F.Cu" || layer == "B.Cu"
	case "inner":
		return reInnerLayer.MatchString(layer)
	}
	return false
}

// layerGroup is a copper layer's group (outer | inner).
func layerGroup(layer string) string {
	if reInnerLayer.MatchString(layer) {
		return "inner"
	}
	return "outer"
}

// copperLayerNames are a board's enabled copper layers in stack order.
func copperLayerNames(n int) []string {
	out := []string{"F.Cu"}
	for i := 1; i <= n-2; i++ {
		out = append(out, fmt.Sprintf("In%d.Cu", i))
	}
	return append(out, "B.Cu")
}

// verifyRulesBlock: the rules block of the .kicad_dru next to pcb is
// byte for byte the block kicad rules writes for classes (the floored
// classes the netclasses step just produced) with the block's own stamp; a
// missing or invalid stamp, a body edited by hand, or floors from classes
// that have changed since (a widened board minimum, a user / Default
// class) is RULES_STALE.
func verifyRulesBlock(pcb string, classes []kicad.NetclassClassNM, copperLayers int, maxes ...trackMaxes) error {
	stale := func(why string) error {
		return fmt.Errorf("RULES_STALE: the board's kicad rules block %s; rerun `pcbpilot kicad rules --apply` with the same inputs, then route", why)
	}
	b, err := os.ReadFile(strings.TrimSuffix(pcb, ".kicad_pcb") + ".kicad_dru")
	if err != nil {
		return stale("cannot be read: " + err.Error())
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	_, blk, _, err := kicad.SplitDRU(text)
	if err != nil {
		return stale("is malformed: " + err.Error())
	}
	st, ok := parseRulesStamp(text)
	if blk == "" || !ok || !validRequestSHA(st.RequestSHA) {
		return stale("has no valid stamp (request-sha256)")
	}
	if want := rulesBlockText(rulesBlockRules(classes, copperLayers, maxes...), st.RequestSHA, st.Owned); blk != want {
		return stale("no longer matches the classes derived now (a board minimum or a user / Default class changed after kicad rules --apply, or the block was edited)")
	}
	return nil
}

var reSHA256 = regexp.MustCompile(`^[0-9a-f]{64}$`)

func validRequestSHA(s string) bool { return reSHA256.MatchString(s) }

// routeTrackMaxes recomputes kicad rules' carried user maximums for route's
// block check: the user rules of the classed board's .kicad_dru and each
// net's identity from the input board (its classes after the apply: user,
// PP and PPD) plus the classes just derived.
func (r *kicadRun) routeTrackMaxes(classed string, full *kicad.NetclassFull, copperLayers int) trackMaxes {
	b, err := os.ReadFile(strings.TrimSuffix(classed, ".kicad_pcb") + ".kicad_dru")
	if err != nil {
		return nil
	}
	rules, err := kicad.ParseDRU(strings.ReplaceAll(string(b), "\r\n", "\n"))
	if err != nil {
		return nil
	}
	var snap struct {
		NetClassOf map[string]string `json:"netClassOf"`
	}
	_ = json.Unmarshal(r.inRaw, &snap)
	identity := func(net string) map[string]bool {
		m := map[string]bool{net: true}
		for _, c := range strings.Split(snap.NetClassOf[net], ",") {
			if c != "" {
				m[c] = true
			}
		}
		for _, c := range full.ClassesNM {
			if rulesHasStr(c.Nets, net) {
				m[c.Name] = true
			}
		}
		for dom, nets := range full.Domains {
			if rulesHasStr(nets, net) {
				m["PPD_"+dom] = true
			}
		}
		return m
	}
	mx, _ := trackMaxCarry(rules, full.ClassesNM, copperLayers, identity, nil)
	return mx
}

// moveRulesBlockLast re-places the rules block of the .kicad_dru next to
// pcb behind the route block (kicad route after its netclasses step).
func moveRulesBlockLast(pcb string) error {
	path := strings.TrimSuffix(pcb, ".kicad_pcb") + ".kicad_dru"
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	_, blk, _, err := kicad.SplitDRU(strings.ReplaceAll(string(b), "\r\n", "\n"))
	if err != nil || blk == "" {
		return err
	}
	text, err := placeRulesBlock(string(b), blk)
	if err != nil {
		return err
	}
	return os.WriteFile(path, []byte(text), 0o644)
}

// rulesStampCheck is kicad route's check of rules applied by kicad rules:
// when the input board's .kicad_dru has a rules block, route derives the
// rules again with kicad rules' width basis on route's own inputs (intent,
// --sim, --requirements, the board stackup); the request must be the one
// the block was made from, else the rules are stale and route stops. With
// no rules block nothing changes (route's own derivation, as before).
func (r *kicadRun) rulesStamp(ri kicadRulesInputs, inSnap *boardSnapshot) (kicadRulesInputs, error) {
	data, err := os.ReadFile(strings.TrimSuffix(r.o.pcb, ".kicad_pcb") + ".kicad_dru")
	if err != nil {
		return ri, nil
	}
	// A rules block is present or not by its markers; a present block must
	// carry a valid stamp (and pass verifyRulesBlock after the netclasses).
	_, blk, _, err := kicad.SplitDRU(strings.ReplaceAll(string(data), "\r\n", "\n"))
	if err != nil {
		return ri, fmt.Errorf("RULES_STALE: the board's .kicad_dru is malformed (%v); rerun `pcbpilot kicad rules --apply` with the same inputs, then route", err)
	}
	if blk == "" {
		return ri, nil
	}
	st, ok := parseRulesStamp(string(data))
	if !ok || !validRequestSHA(st.RequestSHA) {
		r.summary["kicadRules"] = map[string]any{"stale": true, "appliedRequestSha256": st.RequestSHA}
		return ri, fmt.Errorf("RULES_STALE: the board's kicad rules block has no valid request-sha256 stamp; rerun `pcbpilot kicad rules --pcb %s --intent %s --apply` with the same inputs, then route", r.o.pcb, r.o.intent)
	}
	board, err := os.ReadFile(r.o.pcb)
	if err != nil {
		return ri, err
	}
	stack, _ := kicad.ReadStackupCopper(board)
	var sp *pcbauto.SimPower
	var simHas map[string]bool
	if r.o.sim != "" {
		raw, err := os.ReadFile(r.o.sim)
		if err != nil {
			return ri, err
		}
		if sp, err = pcbauto.ParseSim(raw); err != nil {
			return ri, err
		}
		simHas = simEvidence(raw)
	}
	decl, _, err := loadDeclaredCurrents(r.o.requirements)
	if err != nil {
		return ri, err
	}
	widths, _ := planRulesWidths(r.in, sp, decl, copperBasis(stack, r.in), simHas)
	ri2 := deriveKicadRules(r.in, r.o.intent, widths)
	sum := map[string]any{"requestSha256": ri2.sha(), "appliedRequestSha256": st.RequestSHA}
	r.summary["kicadRules"] = sum
	if ri2.sha() != st.RequestSHA {
		sum["stale"] = true
		return ri, fmt.Errorf("RULES_STALE: the board carries rules from `kicad rules` (derivation %s) that no longer match the intent, --sim, --requirements and stackup given to route (derivation %s); rerun `pcbpilot kicad rules --pcb %s --intent %s --apply` with the same inputs, then route", short12(st.RequestSHA), short12(ri2.sha()), r.o.pcb, r.o.intent)
	}
	fmt.Fprintf(r.stderr, "rules: the board's kicad rules (derivation %s) match route's derivation\n", short12(st.RequestSHA))
	return ri2, nil
}

// viaCountNeed is the vias per layer transition a current needs at drill
// (mil): ceil(I / one via's ampacity).
func viaCountNeed(a, drillMil, tempRise float64) (int, float64) {
	per := pcbauto.ViaAmpacity(drillMil, pcbauto.DefaultViaPlatingMil, tempRise)
	if per <= 0 || a <= 0 {
		return 0, per
	}
	return int(math.Ceil(a/per - 1e-9)), per
}

func sortedInt64(v []int64) []int64 {
	out := append([]int64(nil), v...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// expectedFloor is the track_width minimum a class must resolve to on a
// layer kind, from its values alone (never from the generated rules): the
// intent minimum (the neck-down limit) when below the layer width, else
// the layer width.
func expectedFloor(q kicad.ClassNM, layer string) int64 {
	w := q.Track
	if layer == "inner" && q.Inner > 0 {
		w = q.Inner
	}
	if q.Min > 0 && q.Min < w {
		return q.Min
	}
	return w
}
