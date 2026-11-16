package app

// kicad_rules_keepstricter.go — `kicad rules --keep-stricter`: resolve a
// conflict between a user .kicad_dru rule and a generated one by ORDER
// instead of removing the user's rule (--replace).
//
// KiCad gives a later rule priority over an earlier one of the same
// constraint type wherever both conditions match. The generated clearance /
// creepage / edge_clearance minimums live in the route block (written by
// bridge netclasses, after the user's rules), so a generated pair rule
// overrides a user rule that asks for MORE on the same items. With
// --keep-stricter such a generated minimum is written in a second owned
// block, the head block, placed right after (version …) and before every
// user rule: the user's rule, now later, wins wherever both match, and the
// generated minimum still applies wherever only it matches. A generated
// minimum stricter than every user rule it may overlap stays where it was
// (last). Net effect: wherever both match, the stricter value applies. No
// user rule text is changed or removed.
//
// Limits, stated rather than hidden:
//   - two rules overlap where both conditions match; pcbpilot can only
//     PROVE non-overlap from literal net / netclass terms (druDisjointPair),
//     so everything else counts as overlapping;
//   - a generated rule that would have to sit after one user rule (weaker
//     than it) and before another (stricter than it) stays last, and the
//     stricter user minimum is written again right after it, limited to the
//     items both match (a "carry" rule in the route block: the user rule's
//     condition AND the generated rule's). Every user rule that follows the
//     first stricter one in the file and may match is carried the same way,
//     in file order, with the larger of its minimum and the generated one —
//     so among the items the generated rule governs the user's own rule
//     order still decides, and nothing is lowered. A user condition or
//     layer that cannot be written inside a condition string is not
//     carried: that conflict stays and blocks;
//   - a user value that is not a plain length, a user rule without a
//     minimum, or one whose (severity …) is not error cannot be compared;
//   - a user clearance rule below a planned netclass clearance cannot be
//     fixed by order (a custom rule replaces the netclass value wherever it
//     matches);
//   - track_width rules are not reordered (they carry opt / max values and
//     have their own carry-over, trackMaxCarry).

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// keepRef names a user rule and its minimum.
type keepRef struct {
	Rule  string `json:"rule"`
	MinNm int64  `json:"minNm"`
}

// keepMove is one generated constraint written in the head block.
type keepMove struct {
	HeadRule  string    `json:"headRule"`
	Generated string    `json:"generatedRule"`
	Type      string    `json:"constraint"`
	MinNm     int64     `json:"minNm"`
	YieldsTo  []keepRef `json:"yieldsTo"` // the user rules that win over it where both match
}

// keepStay is one generated constraint that stays last: at least as strict
// as the user rules listed, which it overrides where both match.
type keepStay struct {
	Generated string    `json:"generatedRule"`
	Type      string    `json:"constraint"`
	MinNm     int64     `json:"minNm"`
	Over      []keepRef `json:"winsOver"`
}

// keepCarry is one user minimum written again after a generated rule that
// must stay last: a route-block rule on (user condition) && (generated
// condition) with the larger of the two minimums.
type keepCarry struct {
	Rule      string `json:"rule"`
	Generated string `json:"generatedRule"`
	Type      string `json:"constraint"`
	MinNm     int64  `json:"minNm"`
	UserRule  string `json:"userRule"`
	UserMinNm int64  `json:"userMinNm"`
	Condition string `json:"userCondition,omitempty"`
	Layer     string `json:"layer,omitempty"`
}

// keepCarryPrefix starts the name of every carry rule.
const keepCarryPrefix = kicad.DRURoutePrefix + "carry "

func keepCarryName(gen, typ string, n int) string {
	return fmt.Sprintf("%s%s %s %d", keepCarryPrefix, typ, strings.TrimPrefix(gen, kicad.DRURoutePrefix), n)
}

// keepCarryCondition is the condition of a carry rule.
func keepCarryCondition(user, gen string) string {
	if strings.TrimSpace(user) == "" {
		return gen
	}
	return "(" + user + ") && (" + gen + ")"
}

// keepCarriable: the user rule's condition and layer can be written again
// inside a generated rule; else why not.
func keepCarriable(u kicad.DRURule) (bool, string) {
	for _, v := range []struct{ what, text string }{{"condition", u.Condition}, {"layer", u.Layer}} {
		if strings.ContainsAny(v.text, "\"\\\n\r") {
			return false, "its " + v.what + " holds a double quote, a backslash or a line break and cannot be written again inside a generated rule"
		}
	}
	return true, ""
}

// keepUnresolved is a user rule keep-stricter could not order.
type keepUnresolved struct {
	UserRule  string `json:"userRule"`
	Generated string `json:"generatedRule,omitempty"`
	Type      string `json:"constraint"`
	Why       string `json:"why"`
}

// keepStricterReport is what --keep-stricter did (report "keepStricter").
type keepStricterReport struct {
	Head       []keepMove       `json:"headBlock"`
	Tail       []keepStay       `json:"generatedStaysLast"`
	Carried    []keepCarry      `json:"carriedAfter"`
	Unresolved []keepUnresolved `json:"unresolved"`
}

// keepGen is one generated clearance / creepage / edge_clearance minimum.
type keepGen struct {
	rule, typ string
	min       int64
	iso       bool   // an insulation pair rule (a, b); else an edge rule (a)
	a, b      string // domain ids (the class name without PPD_)
}

// keepEnv is what the overlap tests need to know about the plan.
type keepEnv struct {
	domain func(dom string) map[string]bool // every name of a domain's nets
	nets   func(dom string) []string
}

// keepKey identifies one constraint of one user rule (DRURule.Index, the
// constraint's position).
type keepKey struct{ rule, con int }

// keepVerdict is the analysis of one user constraint.
type keepVerdict struct {
	// ok: with the plan's order nothing the user rule asks for is lowered
	// where a generated rule may also match.
	ok bool
	// relation: stricter | weaker | equal | mixed | unknown | "" (no
	// generated rule of the type may overlap it).
	relation string
	notes    []string
}

// keepPlan is the analysis of a .kicad_dru's user rules against the
// generated minimums.
type keepPlan struct {
	rep      keepStricterReport
	verdicts map[keepKey]keepVerdict
}

// keepTypes are the constraint types keep-stricter orders.
var keepTypes = map[string]bool{"clearance": true, "creepage": true, "edge_clearance": true}

// keepHeadName is the head-block name of a generated constraint.
func keepHeadName(gen, typ string) string {
	return kicad.DRUHeadPrefix + strings.TrimPrefix(gen, kicad.DRURoutePrefix) + " " + typ
}

// keepHeadOrigin reverses keepHeadName.
func keepHeadOrigin(name string) (gen, typ string, ok bool) {
	rest, found := strings.CutPrefix(name, kicad.DRUHeadPrefix)
	i := strings.LastIndex(rest, " ")
	if !found || i <= 0 {
		return "", "", false
	}
	return kicad.DRURoutePrefix + rest[:i], rest[i+1:], true
}

// keepGenerated lists the generated minimums of a parsed .kicad_dru: the
// route block's insulation pair and edge rules, plus the head block's (a
// constraint the route block carries again — right after bridge netclasses
// rewrote it — counts once, with the route block's value).
func keepGenerated(rules []kicad.DRURule) []keepGen {
	var out []keepGen
	seen := map[[2]string]bool{}
	add := func(name string, cons []kicad.DRUConstraint) {
		var k keepGen
		switch {
		case strings.HasPrefix(name, "pcbpilot isolation "):
			a, b, ok := strings.Cut(strings.TrimPrefix(name, "pcbpilot isolation "), "|")
			if !ok {
				return
			}
			k = keepGen{iso: true, a: strings.TrimPrefix(a, "PPD_"), b: strings.TrimPrefix(b, "PPD_")}
		case strings.HasPrefix(name, "pcbpilot edge "):
			k = keepGen{a: strings.TrimPrefix(strings.TrimPrefix(name, "pcbpilot edge "), "PPD_")}
		default:
			return
		}
		for _, c := range cons {
			if c.Min == nil || c.Unparsed || seen[[2]string{name, c.Type}] {
				continue
			}
			if k.iso && (c.Type == "clearance" || c.Type == "creepage") || !k.iso && c.Type == "edge_clearance" {
				g := k
				g.rule, g.typ, g.min = name, c.Type, *c.Min
				seen[[2]string{name, c.Type}] = true
				out = append(out, g)
			}
		}
	}
	for _, g := range rules {
		if g.Block == "route" {
			add(g.Name, g.Constraints)
		}
	}
	for _, g := range rules {
		if g.Block != "head" {
			continue
		}
		if name, typ, ok := keepHeadOrigin(g.Name); ok {
			var cons []kicad.DRUConstraint
			for _, c := range g.Constraints {
				if c.Type == typ {
					cons = append(cons, c)
				}
			}
			add(name, cons)
		}
	}
	return out
}

// keepUsers are the user rules of a parsed .kicad_dru (outside pcbpilot's
// blocks, not replaced, not carrying a pcbpilot name).
func keepUsers(rules []kicad.DRURule, replaced map[string]bool) []kicad.DRURule {
	var out []kicad.DRURule
	for _, u := range rules {
		if u.Block != "" || replaced[u.Name] || strings.HasPrefix(u.Name, kicad.DRURulesPrefix) || strings.HasPrefix(u.Name, kicad.DRURoutePrefix) {
			continue
		}
		out = append(out, u)
	}
	return out
}

// keepComparable: a user constraint has a minimum pcbpilot can compare and
// its rule reports errors; else why not.
func keepComparable(u kicad.DRURule, c kicad.DRUConstraint) (bool, string) {
	switch {
	case c.Unparsed:
		return false, "its value is not a plain length (an expression or an unknown unit)"
	case c.Min == nil:
		return false, "it sets no minimum"
	case u.Severity != "" && u.Severity != "error":
		return false, fmt.Sprintf("its severity is %q: where it wins, a violation of the generated minimum would no longer be an error", u.Severity)
	}
	return true, ""
}

func (e keepEnv) overlaps(u kicad.DRURule, g keepGen) bool {
	if g.iso {
		return !druDisjointPair(u.Condition, e.domain(g.a), e.domain(g.b))
	}
	return !druDisjoint(u.Condition, e.domain(g.a))
}

// genOverlap: two generated rules of one type may govern the same items
// (their domains share a net).
func (e keepEnv) genOverlap(x, y keepGen) bool {
	if x.typ != y.typ || x.iso != y.iso {
		return false
	}
	share := func(p, q string) bool {
		if p == q {
			return true
		}
		for _, n := range e.nets(p) {
			if rulesHasStr(e.nets(q), n) {
				return true
			}
		}
		return false
	}
	if !x.iso {
		return share(x.a, y.a)
	}
	return share(x.a, y.a) && share(x.b, y.b) || share(x.a, y.b) && share(x.b, y.a)
}

// keepStricterAnalyse orders every generated minimum against the user
// constraints of its type that may overlap it. A generated minimum goes to
// the head block only when every overlapping user constraint is comparable
// and at least as strict, one of them strictly, and no weaker generated
// rule that may match the same items stays behind it; otherwise it stays
// last. A user constraint is ok when every generated minimum it may overlap
// either sits before it or is at least as strict, or the user minimum is
// carried after it (keepCarry).
func keepStricterAnalyse(users []kicad.DRURule, gens []keepGen, env keepEnv) *keepPlan {
	p := &keepPlan{verdicts: map[keepKey]keepVerdict{}}
	p.rep.Head, p.rep.Tail, p.rep.Unresolved, p.rep.Carried = []keepMove{}, []keepStay{}, []keepUnresolved{}, []keepCarry{}
	type link struct {
		u          kicad.DRURule
		con        int
		min        int64
		comparable bool
		whyNot     string
	}
	links := make([][]link, len(gens))
	for _, u := range users {
		for j, c := range u.Constraints {
			if !keepTypes[c.Type] {
				continue
			}
			p.verdicts[keepKey{u.Index, j}] = keepVerdict{ok: true}
			for gi, g := range gens {
				if g.typ != c.Type || !env.overlaps(u, g) {
					continue
				}
				l := link{u: u, con: j}
				if l.comparable, l.whyNot = keepComparable(u, c); c.Min != nil && !c.Unparsed {
					l.min = *c.Min
				}
				links[gi] = append(links[gi], l)
			}
		}
	}
	mm := kicad.MmOfNm
	// Per generated minimum: what keeps it from going before the user's rules.
	head := make([]bool, len(gens))
	blockers := make([][]string, len(gens))
	for gi, g := range gens {
		stricter := false
		for _, l := range links[gi] {
			switch {
			case !l.comparable && (l.min <= g.min || l.u.Constraints[l.con].Min == nil || l.u.Constraints[l.con].Unparsed):
				if l.u.Constraints[l.con].Min == nil || l.u.Constraints[l.con].Unparsed {
					blockers[gi] = append(blockers[gi], fmt.Sprintf("the user rule %q may match the same items and cannot be compared (%s)", l.u.Name, l.whyNot))
				} else {
					// Behind it the generated minimum would be lost where
					// both match: it stays last, as without --keep-stricter.
					blockers[gi] = append(blockers[gi], fmt.Sprintf("the user rule %q (min %s) is no stricter and %s, so the generated rule must stay after it", l.u.Name, mm(l.min), l.whyNot))
				}
			case !l.comparable:
				blockers[gi] = append(blockers[gi], fmt.Sprintf("the user rule %q is stricter (min %s) but %s", l.u.Name, mm(l.min), l.whyNot))
			case l.min < g.min:
				blockers[gi] = append(blockers[gi], fmt.Sprintf("the user rule %q is weaker (min %s < %s), so the generated rule must stay after it", l.u.Name, mm(l.min), mm(g.min)))
			case l.min > g.min:
				stricter = true
			}
		}
		head[gi] = stricter && len(blockers[gi]) == 0
	}
	// A weaker generated rule that stays last and may match the same items
	// would win over a moved one: the move is off (until stable — a rule
	// that stays may in turn hold back another).
	for changed := true; changed; {
		changed = false
		for gi, g := range gens {
			if !head[gi] {
				continue
			}
			for hi, h := range gens {
				if hi != gi && !head[hi] && h.min < g.min && env.genOverlap(g, h) {
					head[gi], changed = false, true
					blockers[gi] = append(blockers[gi], fmt.Sprintf("the generated rule %q (min %s) may match the same items and stays last: behind it, it would win over this one", h.rule, mm(h.min)))
					break
				}
			}
		}
	}
	// A generated minimum that stays last although a comparable user rule
	// is stricter: the user rules from the first stricter one on (file
	// order) are written again after it.
	carried := make([]map[int]string, len(gens))
	noCarry := make([]string, len(gens))
	for gi, g := range gens {
		first := -1
		for i, l := range links[gi] {
			unknown := l.u.Constraints[l.con].Min == nil || l.u.Constraints[l.con].Unparsed
			if unknown || !l.comparable && l.min > g.min {
				// Not comparable and not provably weaker: no order is known.
				first, noCarry[gi] = -1, fmt.Sprintf("the user rule %q cannot be compared, so no rule order after the generated rule is known", l.u.Name)
				break
			}
			if first < 0 && l.comparable && l.min > g.min {
				first = i
			}
		}
		if head[gi] || first < 0 {
			continue
		}
		for _, l := range links[gi][first:] {
			if ok, why := keepCarriable(l.u); !ok {
				first, noCarry[gi] = -1, fmt.Sprintf("the user rule %q cannot be carried after the generated rule (%s)", l.u.Name, why)
				break
			}
		}
		if first < 0 {
			continue
		}
		// A carry rule is after every generated rule: where another
		// generated rule with a larger minimum may match too, the carry
		// would lower that one.
		for _, l := range links[gi][first:] {
			for hi, h := range gens {
				if hi != gi && h.typ == g.typ && h.min > max(l.min, g.min) && env.genOverlap(g, h) {
					first, noCarry[gi] = -1, fmt.Sprintf("the user rule %q cannot be carried after the generated rule: the generated rule %q (min %s) may match the same items and the carried minimum (%s) would lower it", l.u.Name, h.rule, mm(h.min), mm(max(l.min, g.min)))
					break
				}
			}
			if first < 0 {
				break
			}
		}
		if first < 0 {
			continue
		}
		carried[gi] = map[int]string{}
		for i, l := range links[gi][first:] {
			c := keepCarry{Rule: keepCarryName(g.rule, g.typ, i+1), Generated: g.rule, Type: g.typ, MinNm: max(l.min, g.min),
				UserRule: l.u.Name, UserMinNm: l.min, Condition: l.u.Condition, Layer: l.u.Layer}
			carried[gi][first+i] = c.Rule
			p.rep.Carried = append(p.rep.Carried, c)
		}
	}
	rel := map[keepKey]map[string]bool{}
	note := func(l link, relation, text string, ok bool) {
		k := keepKey{l.u.Index, l.con}
		v := p.verdicts[k]
		v.ok = v.ok && ok
		v.notes = append(v.notes, text)
		p.verdicts[k] = v
		if rel[k] == nil {
			rel[k] = map[string]bool{}
		}
		rel[k][relation] = true
	}
	for gi, g := range gens {
		what := fmt.Sprintf("the generated %q (%s min %s)", g.rule, g.typ, mm(g.min))
		var yields, over []keepRef
		for li, l := range links[gi] {
			ref := keepRef{Rule: l.u.Name, MinNm: l.min}
			unknown := l.u.Constraints[l.con].Min == nil || l.u.Constraints[l.con].Unparsed
			switch {
			case unknown:
				why := fmt.Sprintf("cannot be compared with %s: %s", what, l.whyNot)
				note(l, "unknown", why+"; --keep-stricter cannot order them", false)
				p.rep.Unresolved = append(p.rep.Unresolved, keepUnresolved{UserRule: l.u.Name, Generated: g.rule, Type: g.typ, Why: why})
			case l.min == g.min:
				over = append(over, ref)
				if head[gi] {
					yields, over = append(yields, ref), over[:len(over)-1]
				}
				note(l, "equal", fmt.Sprintf("the user rule (min %s) equals %s: either order gives the same value", mm(l.min), what), true)
			case l.min < g.min:
				over = append(over, ref)
				note(l, "weaker", fmt.Sprintf("the user rule (min %s) is weaker than %s: the generated rule stays after it and wins where both match (--keep-stricter accepts this: nothing is lowered)", mm(l.min), what), true)
			case head[gi]:
				yields = append(yields, ref)
				note(l, "stricter", fmt.Sprintf("the user rule (min %s) is stricter than %s: --keep-stricter writes the generated minimum before the user's rules (head block rule %q), so the user rule wins where both match", mm(l.min), what, keepHeadName(g.rule, g.typ)), true)
			case carried[gi][li] != "":
				note(l, "stricter", fmt.Sprintf("the user rule (min %s) is stricter than %s, which must stay last (%s): --keep-stricter writes the user's minimum again after it, limited to the items both rules match (rule %q), so the stricter value applies there", mm(l.min), what, strings.Join(blockers[gi], "; "), carried[gi][li]), true)
			default:
				because := blockers[gi]
				if noCarry[gi] != "" {
					because = append(append([]string(nil), because...), noCarry[gi])
				}
				why := fmt.Sprintf("the user rule (min %s) is stricter than %s, but --keep-stricter cannot order them: %s", mm(l.min), what, strings.Join(because, "; "))
				note(l, "stricter", why, false)
				p.rep.Unresolved = append(p.rep.Unresolved, keepUnresolved{UserRule: l.u.Name, Generated: g.rule, Type: g.typ, Why: why})
			}
		}
		switch {
		case head[gi]:
			p.rep.Head = append(p.rep.Head, keepMove{HeadRule: keepHeadName(g.rule, g.typ), Generated: g.rule, Type: g.typ, MinNm: g.min, YieldsTo: yields})
		case len(over) > 0:
			p.rep.Tail = append(p.rep.Tail, keepStay{Generated: g.rule, Type: g.typ, MinNm: g.min, Over: over})
		}
	}
	for k, v := range p.verdicts {
		switch m := rel[k]; {
		case len(m) == 0:
		case m["unknown"]:
			v.relation = "unknown"
		case len(m) > 1:
			v.relation = "mixed"
		default:
			for r := range m {
				v.relation = r
			}
		}
		p.verdicts[k] = v
	}
	sort.SliceStable(p.rep.Unresolved, func(i, j int) bool { return p.rep.Unresolved[i].UserRule < p.rep.Unresolved[j].UserRule })
	// In the head block the later rule wins too: the stricter minimum goes
	// last, so one moved rule never lowers another where both match.
	sort.SliceStable(p.rep.Head, func(i, j int) bool { return p.rep.Head[i].MinNm < p.rep.Head[j].MinNm })
	return p
}

// ── the head block in the .kicad_dru text ───────────────────────────────────

var reDRUVersionLine = regexp.MustCompile(`(?m)^[ \t]*\(version[ \t]+\d+\)[ \t]*\n`)

// insertHeadBlock puts a head block (with markers, ending in a newline)
// right after the (version …) line: before every user rule.
func insertHeadBlock(text, head string) (string, error) {
	if loc := reDRUVersionLine.FindStringIndex(text); loc != nil {
		return text[:loc[1]] + head + text[loc[1]:], nil
	}
	if !strings.Contains(text, "(version") {
		return "(version 1)\n" + head + text, nil
	}
	return "", fmt.Errorf("custom rules: (version …) is not on a line of its own, so the keep-stricter head block cannot be placed before the user's rules without changing their text")
}

// druTopForms are the spans of the top-level lists of s (# comment lines
// skipped).
func druTopForms(s string) [][2]int {
	var out [][2]int
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '#':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case '(':
			end, _ := sexprSpan(s, i)
			if end < 0 {
				return out
			}
			out = append(out, [2]int{i, end})
			i = end - 1
		}
	}
	return out
}

// druChildren are the spans of the lists directly inside the list f.
func druChildren(f string) [][2]int {
	var out [][2]int
	for i := 1; i < len(f)-1; i++ {
		switch f[i] {
		case '"':
			for i++; i < len(f) && f[i] != '"'; i++ {
				if f[i] == '\\' {
					i++
				}
			}
		case '(':
			end, _ := sexprSpan(f, i)
			if end < 0 {
				return out
			}
			out = append(out, [2]int{i, end})
			i = end - 1
		}
	}
	return out
}

// dropCarryRules removes every carry rule from a route block body.
func dropCarryRules(body string) string {
	var nb strings.Builder
	last := 0
	for _, f := range druTopForms(body) {
		if _, name := sexprSpan(body[f[0]:f[1]], 0); !strings.HasPrefix(name, keepCarryPrefix) {
			continue
		}
		start := f[0]
		if start > last && body[start-1] == '\n' {
			start--
		}
		nb.WriteString(body[last:start])
		last = f[1]
	}
	nb.WriteString(body[last:])
	return nb.String()
}

// keepCarryText is the text of one carry rule; gen is the generated rule's
// condition as written.
func keepCarryText(c keepCarry, gen string) string {
	var b strings.Builder
	b.WriteString("(rule \"" + c.Rule + "\"\n")
	switch c.Layer {
	case "":
	case "outer", "inner":
		b.WriteString("  (layer " + c.Layer + ")\n")
	default:
		b.WriteString("  (layer \"" + c.Layer + "\")\n")
	}
	b.WriteString("  (condition \"" + keepCarryCondition(c.Condition, gen) + "\")\n")
	b.WriteString("  (constraint " + c.Type + " (min " + kicad.MmOfNm(c.MinNm) + ")))")
	return b.String()
}

// keepHeadBlock rewrites a .kicad_dru text for the moves and the carries:
// an existing head block and the carry rules of an earlier run are removed;
// each moved constraint leaves its rule in the route block (a rule left
// without a constraint is dropped) and is written, with the rule's
// condition and its own text, as a rule of a new head block right after
// (version …); each carry rule is written right after the generated rule
// it belongs to. The head block also lists the carries (it is the mark
// that keep-stricter is in force). Neither: the text without a head block.
// The user part is not touched: cutting the head block out again gives it
// back byte for byte.
func keepHeadBlock(text string, moves []keepMove, carries []keepCarry) (string, error) {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text, _, err := kicad.SplitDRUHead(text)
	if err != nil {
		return text, err
	}
	bi := strings.Index(text, kicad.DRURouteBegin)
	ej := -1
	if bi >= 0 {
		ej = strings.Index(text[bi:], kicad.DRURouteEnd)
	}
	if ej < 0 {
		if len(moves) == 0 && len(carries) == 0 {
			return text, nil
		}
		return "", fmt.Errorf("custom rules: no route block to take the keep-stricter rules from")
	}
	b0, b1 := bi+len(kicad.DRURouteBegin), bi+ej
	body := dropCarryRules(text[b0:b1])
	if len(moves) == 0 && len(carries) == 0 {
		return text[:b0] + body + text[b1:], nil
	}
	want := map[string]map[string]int{}
	for i, m := range moves {
		if want[m.Generated] == nil {
			want[m.Generated] = map[string]int{}
		}
		want[m.Generated][m.Type] = i
	}
	after := map[string][]int{}
	for i, c := range carries {
		after[c.Generated] = append(after[c.Generated], i)
	}
	got := make([]string, len(moves))
	placed := make([]bool, len(carries))
	var nb strings.Builder
	last := 0
	for _, f := range druTopForms(body) {
		form := body[f[0]:f[1]]
		_, name := sexprSpan(form, 0)
		types, cs := want[name], after[name]
		if !strings.HasPrefix(form, "(rule") || types == nil && cs == nil {
			continue
		}
		kids := druChildren(form)
		cond := ""
		for _, k := range kids {
			if kt := form[k[0]:k[1]]; strings.HasPrefix(kt, "(condition") {
				cond = kt
			}
		}
		var keep []string
		hits, left := 0, 0
		has := map[string]bool{}
		for _, k := range kids {
			kt := form[k[0]:k[1]]
			if fields := strings.Fields(kt); strings.HasPrefix(kt, "(constraint") && len(fields) > 1 {
				typ := strings.TrimRight(fields[1], ")")
				if mi, ok := types[typ]; ok && got[mi] == "" {
					got[mi] = "(rule \"" + moves[mi].HeadRule + "\"\n  " + cond + "\n  " + kt + ")"
					hits++
					continue
				}
				has[typ] = true
				left++
			}
			keep = append(keep, kt)
		}
		if hits == 0 && cs == nil {
			continue
		}
		nb.WriteString(body[last:f[0]])
		last = f[1]
		switch {
		case hits == 0:
			nb.WriteString(form)
		case left == 0:
			if last < len(body) && body[last] == '\n' {
				last++
			}
			continue
		default:
			nb.WriteString(strings.TrimRight(form[:kids[0][0]], " \t\n") + "\n  " + strings.Join(keep, "\n  ") + ")")
		}
		q0, q1 := strings.Index(cond, "\""), strings.LastIndex(cond, "\"")
		for _, ci := range cs {
			// The generated minimum the carry follows must still be here.
			if q1 <= q0 || !has[carries[ci].Type] {
				continue
			}
			nb.WriteString("\n" + keepCarryText(carries[ci], cond[q0+1:q1]))
			placed[ci] = true
		}
	}
	nb.WriteString(body[last:])
	oneLine := strings.NewReplacer("\n", " ", "\r", " ")
	var hb strings.Builder
	hb.WriteString(kicad.DRUHeadBegin + "\n")
	hb.WriteString("# owner: pcbpilot kicad rules --keep-stricter (rules named \"" + kicad.DRUHeadPrefix + "...\"); re-applying replaces this block\n")
	hb.WriteString("# These generated minimums sit BEFORE the user's rules: KiCad gives a later rule priority, so the stricter\n")
	hb.WriteString("# user rule named with each one wins wherever both match; the minimum still applies everywhere else.\n")
	for i, m := range moves {
		if got[i] == "" {
			return "", fmt.Errorf("custom rules: the route block has no %s constraint in rule %q to place before the user's rules", m.Type, m.Generated)
		}
		var ys []string
		for _, y := range m.YieldsTo {
			ys = append(ys, fmt.Sprintf("%q (min %s)", oneLine.Replace(y.Rule), kicad.MmOfNm(y.MinNm)))
		}
		fmt.Fprintf(&hb, "# %s of %q (min %s) yields to the user's %s\n", m.Type, m.Generated, kicad.MmOfNm(m.MinNm), strings.Join(ys, ", "))
		hb.WriteString(got[i] + "\n")
	}
	for i, c := range carries {
		if !placed[i] {
			return "", fmt.Errorf("custom rules: the route block has no %s constraint in rule %q to write the user's rule %q after", c.Type, c.Generated, c.UserRule)
		}
		fmt.Fprintf(&hb, "# %s of %q stays last; the user's %q (min %s) is written again after it as %q (min %s, where both match)\n",
			c.Type, c.Generated, oneLine.Replace(c.UserRule), kicad.MmOfNm(c.UserMinNm), c.Rule, kicad.MmOfNm(c.MinNm))
	}
	hb.WriteString(kicad.DRUHeadEnd + "\n")
	return insertHeadBlock(text[:b0]+nb.String()+text[b1:], hb.String())
}

// ── kicad rules ─────────────────────────────────────────────────────────────

// keepActive: this run orders by strictness — --keep-stricter, or a --check
// of a board that already carries a head block (the board is judged as it
// is).
func (r *rulesRun) keepActive() bool {
	if r.o.keepStricter {
		return true
	}
	if !r.o.check {
		return false
	}
	_, head, err := kicad.SplitDRUHead(string(r.data[r.dru]))
	return err == nil && head != ""
}

func (r *rulesRun) keepEnv() keepEnv {
	return keepEnv{domain: r.domainIdentity, nets: func(dom string) []string { return r.full.Domains[dom] }}
}

// keepAnalyse runs the analysis for this plan: the user rules as read
// (without the ones --replace removes) against the generated minimums of
// the staged result.
func (r *rulesRun) keepAnalyse(userRules []kicad.DRURule, replaced map[string]bool) {
	var gens []keepGen
	if b, err := os.ReadFile(boardSibling(r.staged, ".kicad_dru")); err == nil {
		if staged, err := kicad.ParseDRU(strings.ReplaceAll(string(b), "\r\n", "\n")); err == nil {
			gens = keepGenerated(staged)
		}
	}
	r.keep = keepStricterAnalyse(keepUsers(userRules, replaced), gens, r.keepEnv())
	if !r.keepActive() {
		return
	}
	rep := r.keep.rep
	r.rep.KeepStricter = &rep
	mm := kicad.MmOfNm
	refs := func(v []keepRef) string {
		var s []string
		for _, y := range v {
			s = append(s, fmt.Sprintf("%q (min %s)", y.Rule, mm(y.MinNm)))
		}
		return strings.Join(s, ", ")
	}
	for _, m := range rep.Head {
		fmt.Fprintf(r.stderr, "keep-stricter: %s min %s of generated rule %q goes before the user's rules (head block rule %q); wins where both match: user rule %s\n", m.Type, mm(m.MinNm), m.Generated, m.HeadRule, refs(m.YieldsTo))
	}
	for _, s := range rep.Tail {
		fmt.Fprintf(r.stderr, "keep-stricter: %s min %s of generated rule %q stays last; it is at least as strict as and wins over user rule %s\n", s.Type, mm(s.MinNm), s.Generated, refs(s.Over))
	}
	for _, c := range rep.Carried {
		fmt.Fprintf(r.stderr, "keep-stricter: generated rule %q stays last; %s of user rule %q (min %s) is written again after it as rule %q (min %s, where both match)\n", c.Generated, c.Type, c.UserRule, mm(c.UserMinNm), c.Rule, mm(c.MinNm))
	}
	for _, u := range rep.Unresolved {
		fmt.Fprintf(r.stderr, "keep-stricter: NOT resolved, user rule %q: %s\n", u.UserRule, u.Why)
	}
}

// clearanceBelowClass lists the planned nets a user clearance constraint
// may cover whose planned class clearance is above it.
func (r *rulesRun) clearanceBelowClass(u kicad.DRURule, min int64) []string {
	var out []string
	for _, e := range r.rep.Nets {
		cls, _ := r.classOf(e.Net)
		if min < cls.NM.Clearance && !druDisjointFor(u.Condition, r.netNames(e), true) && !druNeverGoverns(u.Condition, r.item(e.Net, nil)) {
			out = append(out, fmt.Sprintf("%s (%s)", e.Net, kicad.MmOfNm(cls.NM.Clearance)))
		}
	}
	return out
}

// keepNote is the keep-stricter judgement of one conflicting user
// constraint: the text for the conflict, whether the plan's order resolves
// it, and how the user value relates to the generated one(s).
func (r *rulesRun) keepNote(u kicad.DRURule, j int, c kicad.DRUConstraint) (notes []string, ok bool, relation string) {
	if r.keep == nil {
		return nil, false, ""
	}
	if !keepTypes[c.Type] {
		if r.keepActive() {
			notes = append(notes, "--keep-stricter does not reorder "+c.Type+" rules: this conflict stays")
		}
		return notes, false, ""
	}
	ok = true
	if c.Type == "clearance" {
		if cmp, why := keepComparable(u, c); !cmp {
			ok = false
			notes = append(notes, "it cannot be compared with the planned class clearances ("+why+")")
		} else if low := r.clearanceBelowClass(u, *c.Min); len(low) > 0 {
			ok = false
			notes = append(notes, fmt.Sprintf("the user rule (min %s) is weaker than the planned class clearance of %s: a custom rule replaces the netclass clearance wherever it matches and no rule order changes that, so --keep-stricter cannot resolve it", kicad.MmOfNm(*c.Min), strings.Join(low, ", ")))
		}
	}
	v, found := r.keep.verdicts[keepKey{u.Index, j}]
	if !found {
		return notes, false, ""
	}
	if len(v.notes) == 0 {
		v.notes = []string{"no generated " + c.Type + " rule may match the same items: the user rule stays in force as it is"}
	}
	return append(notes, v.notes...), ok && v.ok, v.relation
}

// ── kicad route ─────────────────────────────────────────────────────────────

// reapplyKeepStricter keeps a board's head block in force after route's
// netclasses step: bridge netclasses re-appends the complete route block
// (with the minimums the head block carries), so the plan is computed again
// from the user rules and the generated minimums as they are now, and the
// moved constraints leave the route block again and the carry rules are
// written into it again. A head block that this
// no longer reproduces (a user rule changed, a derived value changed), or
// a user rule that can no longer be ordered, is RULES_STALE: route never
// decides a rule order kicad rules did not report. No head block: nothing.
func (r *kicadRun) reapplyKeepStricter(classed string, full *kicad.NetclassFull) error {
	path := strings.TrimSuffix(classed, ".kicad_pcb") + ".kicad_dru"
	stale := func(why string) error {
		return fmt.Errorf("RULES_STALE: the board's keep-stricter head block %s; rerun `pcbpilot kicad rules --apply --keep-stricter` with the same inputs, then route", why)
	}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // no rules file: no head block
	}
	if err != nil {
		// Unreadable is not "no head block": the generated rules would then
		// stay behind the user's stricter ones without a word.
		return stale("cannot be checked (" + err.Error() + ")")
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	_, head, err := kicad.SplitDRUHead(text)
	if err != nil {
		return stale("is malformed: " + err.Error())
	}
	if head == "" {
		return nil
	}
	rules, err := kicad.ParseDRU(text)
	if err != nil {
		return stale("cannot be read: " + err.Error())
	}
	identity := r.routeIdentity(full)
	env := keepEnv{nets: func(dom string) []string { return full.Domains[dom] }, domain: func(dom string) map[string]bool {
		m := map[string]bool{"PPD_" + dom: true}
		for _, n := range full.Domains[dom] {
			for k := range identity(n) {
				m[k] = true
			}
		}
		return m
	}}
	plan := keepStricterAnalyse(keepUsers(rules, nil), keepGenerated(rules), env)
	if len(plan.rep.Unresolved) > 0 {
		u := plan.rep.Unresolved[0]
		return stale(fmt.Sprintf("no longer orders the user's rules (user rule %q: %s)", u.UserRule, u.Why))
	}
	out, err := keepHeadBlock(text, plan.rep.Head, plan.rep.Carried)
	if err != nil {
		return stale("cannot be rewritten: " + err.Error())
	}
	if _, now, _ := kicad.SplitDRUHead(out); now != head {
		return stale("is not the one the user rules and the derived minimums give now (a user rule or a derived clearance changed after kicad rules --apply, or the block was edited)")
	}
	return os.WriteFile(path, []byte(out), 0o644)
}
