package specctra

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// NetRequirement is what the design intent demands of one net's copper (mil).
// OuterMil applies on TopLayer/BottomLayer, InnerMil on InnerN, MinMil is the
// narrowest neck-down allowed (MinMil >= OuterMil = no neck-down).
type NetRequirement struct {
	OuterMil     float64 `json:"outerMil"`
	InnerMil     float64 `json:"innerMil"`
	MinMil       float64 `json:"minMil"`
	ClearanceMil float64 `json:"clearanceMil"`
}

// RequirementReport says how the DSN was brought up to the requirements.
type RequirementReport struct {
	Nets       int      `json:"nets"`
	Classes    int      `json:"classesChanged"`
	NewClasses int      `json:"classesAdded"`
	Raised     []string `json:"raised,omitempty"`
	// NoNeckdown are the DSN classes whose nets allow no neck-down; pass
	// them to fastroute --no-neckdown-classes.
	NoNeckdown []string `json:"noNeckdownClasses,omitempty"`
	// MinTraceMil is the narrowest neck-down any net allows (0 = none given).
	MinTraceMil float64 `json:"minTraceMil"`
}

const reqEps = 0.005

// ApplyNetRequirements raises every DSN net class to the requirements of the
// nets it carries: rule width to the largest outer width, a layer_rule on the
// inner layers to the largest inner width, clearance to the largest
// clearance. Values already above the requirement stay. A net with a
// requirement but no class gets a class of its own. Classes whose nets all
// forbid neck-down are listed for fastroute's --no-neckdown-classes.
// Coordinates must be in mil (EasyEDA writes `(resolution mil ...)`).
func ApplyNetRequirements(dsn string, reqs map[string]NetRequirement) (string, RequirementReport, error) {
	rep := RequirementReport{Nets: len(reqs)}
	if err := requireMil(dsn); err != nil {
		return "", rep, err
	}
	inner := innerLayers(dsn)
	ns, ne, err := sectionSpan(dsn, "network")
	if err != nil {
		return "", rep, err
	}
	for _, r := range reqs {
		if r.MinMil > 0 && (rep.MinTraceMil == 0 || r.MinMil < rep.MinTraceMil) {
			rep.MinTraceMil = r.MinMil
		}
	}
	net := dsn[ns:ne]
	covered := map[string]bool{}
	var out strings.Builder
	last := 0
	for _, sp := range classSpans(net) {
		n, err := parseSexpr(net[sp[0]:sp[1]])
		if err != nil {
			return "", rep, fmt.Errorf("class at offset %d: %w", ns+sp[0], err)
		}
		atoms := n.atoms()
		if len(atoms) == 0 {
			continue
		}
		name, members := atoms[0], atoms[1:]
		var want NetRequirement
		noNeck, has := true, false
		for _, m := range members {
			covered[m] = true
			r, ok := reqs[m]
			if !ok {
				continue
			}
			has = true
			want.OuterMil = math.Max(want.OuterMil, r.OuterMil)
			want.InnerMil = math.Max(want.InnerMil, r.InnerMil)
			want.ClearanceMil = math.Max(want.ClearanceMil, r.ClearanceMil)
			if r.MinMil+reqEps < r.OuterMil {
				noNeck = false
			}
		}
		if !has {
			continue
		}
		if noNeck {
			rep.NoNeckdown = append(rep.NoNeckdown, strings.Trim(name, `"'`))
		}
		changed, notes := raiseClass(n, want, inner)
		if !changed {
			continue
		}
		rep.Classes++
		for _, s := range notes {
			rep.Raised = append(rep.Raised, strings.Trim(name, `"'`)+": "+s)
		}
		out.WriteString(net[last:sp[0]])
		out.WriteString(writeSexpr(n))
		last = sp[1]
	}
	out.WriteString(net[last:])
	body := out.String()

	// Nets with a requirement but no class get one each.
	var missing []string
	for name := range reqs {
		if !covered[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		var add strings.Builder
		for i, name := range missing {
			r := reqs[name]
			cls := fmt.Sprintf("pcbpilot_req_%d", i+1)
			n := &node{isList: true, List: []*node{{Atom: "class"}, {Atom: cls}, {Atom: name, Quoted: true}}}
			raiseClass(n, r, inner)
			add.WriteString("    " + writeSexpr(n) + "\n")
			if r.MinMil+reqEps >= r.OuterMil {
				rep.NoNeckdown = append(rep.NoNeckdown, cls)
			}
			rep.NewClasses++
		}
		end := strings.LastIndexByte(body, ')')
		body = body[:end] + add.String() + "  " + body[end:]
	}
	sort.Strings(rep.NoNeckdown)
	rep.NoNeckdown = slices.Compact(rep.NoNeckdown)
	return dsn[:ns] + body + dsn[ne:], rep, nil
}

// CheckNetRequirements re-reads the DSN and lists every net whose class
// (base rule plus inner layer_rule) is below its requirement. Empty = pass.
func CheckNetRequirements(dsn string, reqs map[string]NetRequirement) ([]string, error) {
	if err := requireMil(dsn); err != nil {
		return nil, err
	}
	inner := innerLayers(dsn)
	ns, ne, err := sectionSpan(dsn, "network")
	if err != nil {
		return nil, err
	}
	type eff struct {
		outer, clr float64
		inner      map[string]float64
	}
	byNet := map[string]eff{}
	net := dsn[ns:ne]
	for _, sp := range classSpans(net) {
		n, err := parseSexpr(net[sp[0]:sp[1]])
		if err != nil {
			return nil, err
		}
		atoms := n.atoms()
		if len(atoms) < 2 {
			continue
		}
		w, c := ruleValues(n.child("rule"))
		e := eff{outer: w, clr: c, inner: map[string]float64{}}
		for _, l := range inner {
			e.inner[l] = w
		}
		for _, lr := range n.children("layer_rule") {
			lw, _ := ruleValues(lr.child("rule"))
			for _, l := range lr.atoms() {
				if lw > 0 {
					e.inner[l] = lw
				}
			}
		}
		for _, m := range atoms[1:] {
			byNet[m] = e
		}
	}
	var short []string
	names := make([]string, 0, len(reqs))
	for n := range reqs {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		r := reqs[name]
		e, ok := byNet[name]
		if !ok {
			short = append(short, name+": no net class in the DSN")
			continue
		}
		if e.outer+reqEps < r.OuterMil {
			short = append(short, fmt.Sprintf("%s: outer width %s < %s mil", name, fnum(e.outer), fnum(r.OuterMil)))
		}
		for _, l := range inner {
			if e.inner[l]+reqEps < r.InnerMil {
				short = append(short, fmt.Sprintf("%s: %s width %s < %s mil", name, l, fnum(e.inner[l]), fnum(r.InnerMil)))
			}
		}
		if e.clr+reqEps < r.ClearanceMil {
			short = append(short, fmt.Sprintf("%s: clearance %s < %s mil", name, fnum(e.clr), fnum(r.ClearanceMil)))
		}
	}
	return short, nil
}

// raiseClass lifts a class node's rule and inner layer_rule to want; it
// reports whether anything changed and what.
func raiseClass(n *node, want NetRequirement, inner []string) (bool, []string) {
	var notes []string
	rule := n.child("rule")
	if rule == nil {
		rule = &node{isList: true, List: []*node{{Atom: "rule"}}}
		n.List = append(n.List, rule)
	}
	w, c := ruleValues(rule)
	if want.OuterMil > w+reqEps {
		setRule(rule, "width", want.OuterMil)
		notes = append(notes, fmt.Sprintf("width %s→%s", fnum(w), fnum(want.OuterMil)))
		w = want.OuterMil
	}
	if want.ClearanceMil > c+reqEps {
		setRule(rule, "clearance", want.ClearanceMil)
		notes = append(notes, fmt.Sprintf("clearance %s→%s", fnum(c), fnum(want.ClearanceMil)))
	}
	if len(inner) > 0 && want.InnerMil > w+reqEps {
		have := 0.0
		for _, lr := range n.children("layer_rule") {
			lw, _ := ruleValues(lr.child("rule"))
			if slices.Equal(lr.atoms(), inner) {
				have = lw
			}
		}
		if want.InnerMil > have+reqEps {
			var kept []*node
			for _, c := range n.List {
				if !(c.isList && c.head() == "layer_rule" && slices.Equal(c.atoms(), inner)) {
					kept = append(kept, c)
				}
			}
			lr := &node{isList: true, List: []*node{{Atom: "layer_rule"}}}
			for _, l := range inner {
				lr.List = append(lr.List, &node{Atom: l})
			}
			lr.List = append(lr.List, &node{isList: true, List: []*node{{Atom: "rule"},
				{isList: true, List: []*node{{Atom: "width"}, {Atom: fnum(want.InnerMil)}}}}})
			n.List = append(kept, lr)
			notes = append(notes, fmt.Sprintf("inner width %s→%s", fnum(math.Max(have, w)), fnum(want.InnerMil)))
		}
	}
	return len(notes) > 0, notes
}

func ruleValues(rule *node) (width, clearance float64) {
	if rule == nil {
		return 0, 0
	}
	for _, c := range rule.List[1:] {
		if !c.isList || len(c.List) < 2 {
			continue
		}
		v, err := strconv.ParseFloat(c.List[1].Atom, 64)
		if err != nil {
			continue
		}
		switch c.head() {
		case "width":
			width = v
		case "clearance":
			if len(c.List) == 2 { // a typed clearance (… (type smd_smd)) is not the class value
				clearance = v
			}
		}
	}
	return width, clearance
}

func setRule(rule *node, key string, v float64) {
	for _, c := range rule.List[1:] {
		if c.isList && c.head() == key && len(c.List) == 2 {
			c.List[1] = &node{Atom: fnum(v)}
			return
		}
	}
	rule.List = append(rule.List, &node{isList: true, List: []*node{{Atom: key}, {Atom: fnum(v)}}})
}

var reResolution = regexp.MustCompile(`\(resolution\s+(\w+)`)

func requireMil(dsn string) error {
	m := reResolution.FindStringSubmatch(dsn)
	if m == nil || strings.ToLower(m[1]) != "mil" {
		return fmt.Errorf("DSN is not in mil (resolution %v); requirements are in mil", m)
	}
	return nil
}

func innerLayers(dsn string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`\(layer\s+(Inner\d+)\s`).FindAllStringSubmatch(dsn, -1) {
		if !slices.Contains(out, m[1]) {
			out = append(out, m[1])
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(out[i], "Inner"))
		b, _ := strconv.Atoi(strings.TrimPrefix(out[j], "Inner"))
		return a < b
	})
	return out
}

// sectionSpan returns the byte span of the top-level (name ...) list.
func sectionSpan(dsn, name string) (int, int, error) {
	off := strings.IndexByte(dsn, '(') + 1
	if sp := childSpans(dsn[off:], name); len(sp) > 0 {
		return off + sp[0][0], off + sp[0][1], nil
	}
	return 0, 0, fmt.Errorf("DSN has no (%s) section", name)
}

// classSpans returns the (class ...) spans inside a (network ...) list text.
func classSpans(network string) [][2]int {
	spans := childSpans(network[1:], "class")
	for i := range spans {
		spans[i][0]++
		spans[i][1]++
	}
	return spans
}

// childSpans returns the spans of the lists with head name that sit directly
// inside text (depth 0 relative to text; text may start inside a list).
func childSpans(text, name string) [][2]int {
	var out [][2]int
	depth, start := 0, -1
	inStr := false
	for i := 0; i < len(text); i++ {
		ch := text[i]
		if inStr {
			if ch == '"' {
				inStr = false
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
		case '(':
			if depth == 0 {
				j := i + 1
				for j < len(text) && (text[j] == ' ' || text[j] == '\t') {
					j++
				}
				if strings.HasPrefix(text[j:], name) && j+len(name) < len(text) && strings.ContainsRune(" \t\r\n()", rune(text[j+len(name)])) {
					start = i
				}
			}
			depth++
		case ')':
			depth--
			if depth == 0 && start >= 0 {
				out = append(out, [2]int{start, i + 1})
				start = -1
			}
			if depth < 0 {
				return out
			}
		}
	}
	return out
}

// writeSexpr serialises a node on one line; quoted atoms keep their quotes.
func writeSexpr(n *node) string {
	if !n.isList {
		if n.Quoted {
			return strconv.Quote(n.Atom)
		}
		return n.Atom
	}
	parts := make([]string, len(n.List))
	for i, c := range n.List {
		parts[i] = writeSexpr(c)
	}
	return "(" + strings.Join(parts, " ") + ")"
}
