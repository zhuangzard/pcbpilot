package kicad

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DSNPrep says how PrepareDSN changed KiCad's DSN export.
type DSNPrep struct {
	Unit string `json:"unit"`
	// Renamed are composite class names ("PP1_W20,Power3V3": the net matched
	// a user netclass too) rewritten without commas, because fastroute's
	// --no-neckdown-classes is a comma-separated list. The SES carries no
	// class names, so the rename never reaches the board.
	Renamed map[string]string `json:"renamedClasses,omitempty"`
	// InnerRules are the (layer_rule <inner layers> (rule (width W))) added to
	// classes whose intent inner width exceeds the outer one: KiCad
	// netclasses have a single width, so the exporter cannot write it.
	InnerRules []string `json:"innerRules,omitempty"`
	// NoNeckdown are the DSN classes for fastroute --no-neckdown-classes:
	// every net of the class has widthMil.min = outer above the floor.
	NoNeckdown []string `json:"noNeckdownClasses,omitempty"`
	// MinTraceMil is the narrowest widthMil.min of any net (0 = none).
	MinTraceMil float64 `json:"minTraceMil"`
	// NarrowestClassMil is the narrowest rule width of any DSN class with
	// nets: a neck-down floor above it would forbid that class its own width.
	NarrowestClassMil float64 `json:"narrowestClassMil"`
	// ClearanceMarginMil was added to every clearance (see ClearanceMarginMil).
	ClearanceMarginMil float64 `json:"clearanceMarginMil"`
	// Short lists the nets whose DSN class misses their requirement (the
	// pre-route gate: routing stops when it is not empty).
	Short []string `json:"short,omitempty"`
	Nets  int      `json:"nets"`
}

const reqEps = 0.005

// ClearanceMarginMil is added to every clearance in the DSN handed to
// fastroute: KiCad's DRC measured 0.1484 mm against a 0.1501 mm netclass
// clearance on a track fastroute had placed at the rule (PicoRick, KiCad
// 10.0.7), so routing at the bare rule fails DRC by rounding. Same value as
// the EasyEDA flow's specctra.ClearanceMarginMil.
var ClearanceMarginMil = 0.2

// forbidsNeckdown matches internal/pcb/specctra: the net may not neck down
// and its full width is above the global neck-down floor.
func forbidsNeckdown(r NetRequirement, floor float64) bool {
	return r.MinMil+reqEps >= r.OuterMil && r.OuterMil > floor+reqEps
}

var (
	reResolution   = regexp.MustCompile(`\(resolution\s+(\w+)`)
	reLayerType    = regexp.MustCompile(`\(layer\s+("[^"]*"|[^\s()]+)\s*\(type\s+(\w+)`)
	reWidth        = regexp.MustCompile(`\(width\s+([0-9.eE+-]+)\s*\)`)
	reStringQuote  = regexp.MustCompile(`\(string_quote\s+"\s*\)`)
	reLayerRuleW   = regexp.MustCompile(`\(layer_rule\s[^()]*\(rule\s*\(width\s+([0-9.eE+-]+)`)
	reClearanceAny = regexp.MustCompile(`\((clear|clearance)(\s+)([0-9.]+)`)
	reClearance    = regexp.MustCompile(`\(clearance\s+([0-9.eE+-]+)\s*\)`)
)

// unitToMil is the factor from a DSN resolution unit to mil.
func unitToMil(unit string) (float64, error) {
	switch strings.ToLower(unit) {
	case "um":
		return 1 / 25.4, nil
	case "mm":
		return 1 / 0.0254, nil
	case "mil":
		return 1, nil
	case "inch":
		return 1000, nil
	case "cm":
		return 1 / 0.00254, nil
	}
	return 0, fmt.Errorf("DSN resolution unit %q not supported", unit)
}

// PrepareDSN brings a KiCad DSN export in line with the intent: composite
// class names lose their commas, inner widths become layer_rules, and every
// required net is checked against its class (width, inner width,
// clearance). classes are the bridge's netclasses (names PPn_...), reqs the
// intent per net (only nets present on the board).
func PrepareDSN(dsn string, classes []NetClass, reqs map[string]NetRequirement) (string, *DSNPrep, error) {
	prep := &DSNPrep{Renamed: map[string]string{}, Nets: len(reqs)}
	m := reResolution.FindStringSubmatch(dsn)
	if m == nil {
		return "", prep, fmt.Errorf("DSN has no (resolution ...)")
	}
	prep.Unit = m[1]
	toMil, err := unitToMil(m[1])
	if err != nil {
		return "", prep, err
	}
	if ClearanceMarginMil > 0 {
		margin := ClearanceMarginMil / toMil
		dsn = reClearanceAny.ReplaceAllStringFunc(dsn, func(m string) string {
			p := reClearanceAny.FindStringSubmatch(m)
			v, err := strconv.ParseFloat(p[3], 64)
			if err != nil {
				return m
			}
			return "(" + p[1] + p[2] + strconv.FormatFloat(math.Round((v+margin)*1000)/1000, 'f', -1, 64)
		})
		prep.ClearanceMarginMil = ClearanceMarginMil
	}
	for _, r := range reqs {
		if r.MinMil > 0 && (prep.MinTraceMil == 0 || r.MinMil < prep.MinTraceMil) {
			prep.MinTraceMil = r.MinMil
		}
	}
	// Copper layers in stack order; inner = all but the first and last.
	var layers []string
	if ss, se, err := listSpan(dsn, "structure"); err == nil {
		for _, lm := range reLayerType.FindAllStringSubmatch(dsn[ss:se], -1) {
			layers = append(layers, lm[1])
		}
	}
	var inner []string
	if len(layers) > 2 {
		inner = layers[1 : len(layers)-1]
	}
	ours := map[string]NetClass{}
	for _, c := range classes {
		ours[c.Name] = c
	}

	ns, ne, err := listSpan(dsn, "network")
	if err != nil {
		return "", prep, err
	}
	network := dsn[ns:ne]
	type eff struct {
		outer, inner, clr float64
	}
	byNet := map[string]eff{}
	var out strings.Builder
	last := 0
	for _, sp := range childSpans(network, "class") {
		text := network[sp[0]:sp[1]]
		name, members, headEnd := classHeader(text)
		if name == "" {
			continue
		}
		newName := name
		if strings.Contains(name, ",") {
			newName = strings.ReplaceAll(name, ",", "+")
			prep.Renamed[name] = newName
		}
		cls, isOurs := ours[strings.FieldsFunc(name, func(r rune) bool { return r == ',' || r == '+' })[0]]
		e := eff{}
		if wm := reWidth.FindStringSubmatch(text); wm != nil {
			e.outer, _ = strconv.ParseFloat(wm[1], 64)
			e.outer *= toMil
		}
		if cm := reClearance.FindStringSubmatch(text); cm != nil {
			e.clr, _ = strconv.ParseFloat(cm[1], 64)
			e.clr *= toMil
		}
		e.inner = e.outer
		if lm := reLayerRuleW.FindStringSubmatch(text); lm != nil {
			w, _ := strconv.ParseFloat(lm[1], 64)
			e.inner = w * toMil
		}
		body := text[headEnd:]
		if isOurs && len(inner) > 0 && cls.InnerWidthMil > e.inner+reqEps {
			w := strconv.FormatFloat(math.Round(cls.InnerWidthMil/toMil*1000)/1000, 'f', -1, 64)
			rule := fmt.Sprintf("(layer_rule %s (rule (width %s)))", strings.Join(inner, " "), w)
			end := strings.LastIndexByte(body, ')')
			body = body[:end] + "  " + rule + "\n    " + body[end:]
			prep.InnerRules = append(prep.InnerRules, newName+": "+rule)
			e.inner = cls.InnerWidthMil
		}
		if isOurs {
			no := len(cls.Nets) > 0
			for _, n := range cls.Nets {
				r, ok := reqs[n]
				if !ok || !forbidsNeckdown(r, prep.MinTraceMil) {
					no = false
				}
			}
			if no {
				prep.NoNeckdown = append(prep.NoNeckdown, newName)
			}
		}
		for _, n := range members {
			byNet[n] = e
		}
		if len(members) > 0 && e.outer > 0 && (prep.NarrowestClassMil == 0 || e.outer < prep.NarrowestClassMil) {
			prep.NarrowestClassMil = e.outer
		}
		// Only the name changes; members keep KiCad's own quoting.
		header := text[:headEnd]
		if newName != name {
			header = strings.Replace(header, name, newName, 1)
		}
		out.WriteString(network[last:sp[0]])
		out.WriteString(header)
		out.WriteString(body)
		last = sp[1]
	}
	out.WriteString(network[last:])

	names := make([]string, 0, len(reqs))
	for n := range reqs {
		names = append(names, n)
	}
	sort.Strings(names)
	f := func(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }
	for _, n := range names {
		r := reqs[n]
		e, ok := byNet[n]
		if !ok {
			prep.Short = append(prep.Short, n+": no net class in the DSN")
			continue
		}
		if e.outer+reqEps < r.OuterMil {
			prep.Short = append(prep.Short, fmt.Sprintf("%s: outer width %s < %s mil", n, f(e.outer), f(r.OuterMil)))
		}
		if len(inner) > 0 && e.inner+reqEps < r.InnerMil {
			prep.Short = append(prep.Short, fmt.Sprintf("%s: inner width %s < %s mil", n, f(e.inner), f(r.InnerMil)))
		}
		if e.clr+reqEps < r.ClearanceMil {
			prep.Short = append(prep.Short, fmt.Sprintf("%s: clearance %s < %s mil", n, f(e.clr), f(r.ClearanceMil)))
		}
	}
	sort.Strings(prep.NoNeckdown)
	return dsn[:ns] + out.String() + dsn[ne:], prep, nil
}

// classHeader parses "(class NAME m1 m2 ... (" and returns the name, the
// member nets and the offset of the first nested list.
func classHeader(text string) (string, []string, int) {
	var atoms []string
	i := strings.Index(text, "class") + len("class")
	for i < len(text) {
		c := text[i]
		switch {
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		case c == '(' || c == ')':
			if len(atoms) == 0 {
				return "", nil, i
			}
			return atoms[0], atoms[1:], i
		case c == '"':
			j := strings.IndexByte(text[i+1:], '"')
			if j < 0 {
				return "", nil, len(text)
			}
			atoms = append(atoms, text[i+1:i+1+j])
			i += j + 2
		default:
			j := i
			for j < len(text) && !strings.ContainsRune(" \t\r\n()", rune(text[j])) {
				j++
			}
			atoms = append(atoms, text[i:j])
			i = j
		}
	}
	return "", nil, len(text)
}

// listSpan returns the byte span of the first list (name ...) directly
// inside the DSN's top-level (pcb ...) list.
func listSpan(dsn, name string) (int, int, error) {
	off := strings.IndexByte(dsn, '(') + 1
	if sp := childSpans(dsn[off:], name); len(sp) > 0 {
		return off + sp[0][0], off + sp[0][1], nil
	}
	return 0, 0, fmt.Errorf("DSN has no (%s) section", name)
}

// childSpans returns the spans of the lists with head name at depth 0 of
// text, or at depth 1 when text itself starts with '(' (a list).
func childSpans(text, name string) [][2]int {
	// (string_quote ") declares the quote character; its lone quote must not
	// open a string. Masking keeps every offset.
	text = reStringQuote.ReplaceAllStringFunc(text, func(m string) string { return strings.Replace(m, `"`, "_", 1) })
	base := 0
	if strings.HasPrefix(text, "(") {
		base = 1
	}
	var out [][2]int
	depth, start := 0, -1
	inStr := false
	for i := base; i < len(text); i++ {
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
