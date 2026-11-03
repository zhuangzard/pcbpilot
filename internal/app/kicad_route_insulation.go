package app

// The router has to know the insulation pairs. KiCad's rule file carries them,
// but the DSN gives each class one clearance, so without this fastroute lays
// copper across a pair and only the final DRC notices.

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var (
	reDSNClassName      = regexp.MustCompile(`^\(class\s+("[^"]*"|[^\s()]+)`)
	reDSNToken          = regexp.MustCompile(`"[^"]*"|[^\s()]+`)
	reDSNClassClearance = regexp.MustCompile(`\(clearance\s+([0-9.]+)\)`)
)

// dsnFindAll returns where each "(" + word + delimiter starts outside quotes.
func dsnFindAll(dsn, word string) []int {
	var at []int
	inQ := false
	for i := 0; i < len(dsn); i++ {
		switch dsn[i] {
		case '"':
			inQ = !inQ
		case '(':
			if !inQ && strings.HasPrefix(dsn[i+1:], word) {
				if j := i + 1 + len(word); j < len(dsn) && strings.ContainsRune(" \t\r\n()", rune(dsn[j])) {
					at = append(at, i)
				}
			}
		}
	}
	return at
}

// dsnInsulationRules adds a class_class clearance for every two DSN classes on
// the two sides of an insulation pair (a DSN class joins netclass names with
// '+'). marginUm is the margin every other DSN clearance carries; a pair rule
// never goes below either class's own clearance. No applicable pair leaves the
// DSN byte for byte; rules that cannot be written are an error, never a DSN
// that silently lacks them.
func dsnInsulationRules(dsn string, iso []map[string]any, marginUm float64) (string, int, error) {
	own := map[string]float64{}
	var names []string
	for _, at := range dsnFindAll(dsn, "class") {
		end := sexprEnd(dsn, at)
		if end < 0 {
			return dsn, 0, fmt.Errorf("unterminated class in the DSN")
		}
		m := reDSNClassName.FindStringSubmatch(dsn[at:end])
		if m == nil {
			return dsn, 0, fmt.Errorf("a DSN class without a name")
		}
		name := strings.Trim(m[1], `"`)
		names = append(names, name)
		for _, c := range reDSNClassClearance.FindAllStringSubmatch(dsn[at:end], -1) {
			if v, err := strconv.ParseFloat(c[1], 64); err == nil && v > own[name] {
				own[name] = v
			}
		}
	}
	member := func(name, class string) bool {
		for _, p := range strings.Split(strings.TrimSuffix(name, "__POUR"), "+") {
			if p == class {
				return true
			}
		}
		return false
	}
	seen := map[string]float64{}
	var order []string
	for _, p := range iso {
		mm, _ := p["clearanceMm"].(float64)
		if mm <= 0 {
			continue
		}
		ca, cb := domainClass(fmt.Sprint(p["a"])), domainClass(fmt.Sprint(p["b"]))
		for _, x := range names {
			if member(x, ca) && member(x, cb) {
				return dsn, 0, fmt.Errorf("DSN class %s is on both sides of insulation pair %v|%v", x, p["a"], p["b"])
			}
			for _, y := range names {
				if x >= y || !(member(x, ca) && member(y, cb) || member(x, cb) && member(y, ca)) {
					continue
				}
				key := x + "\x00" + y
				um := mm*1000 + marginUm
				if own[x] > um {
					um = own[x]
				}
				if own[y] > um {
					um = own[y]
				}
				if old, ok := seen[key]; !ok {
					order = append(order, key)
					seen[key] = um
				} else if um > old {
					seen[key] = um
				}
			}
		}
	}
	if len(order) == 0 {
		return dsn, 0, nil
	}
	nets := dsnFindAll(dsn, "network")
	if len(nets) != 1 {
		return dsn, 0, fmt.Errorf("%d network sections in the DSN", len(nets))
	}
	end := sexprEnd(dsn, nets[0])
	if end < 0 {
		return dsn, 0, fmt.Errorf("unterminated network section in the DSN")
	}
	var b strings.Builder
	for _, key := range order {
		pair := strings.SplitN(key, "\x00", 2)
		fmt.Fprintf(&b, "    (class_class (classes %q %q) (rule (clearance %g)))\n", pair[0], pair[1], seen[key])
	}
	return dsn[:end-1] + b.String() + dsn[end-1:], len(order), nil
}

// routerArgManaged names the fastroute options pcbpilot owns: --router-arg must
// not move the files it reads back or the session a round resumes from.
var routerArgManaged = []string{"-de", "-do", "-mp", "--report", "--initial-session", "--diagnose", "--pairs", "--tune", "--max-time", "--multi-start", "--no-neckdown-classes", "--router.job_timeout", "--router.autorouter.max_threads", "--router.optimizer.max_threads", "--router.optimizer.enabled"}

func checkRouterArgs(args []string) error {
	for _, a := range args {
		name := a
		if i := strings.Index(a, "="); i >= 0 {
			name = a[:i]
		}
		for _, m := range routerArgManaged {
			if name == m {
				return fmt.Errorf("--router-arg %s: pcbpilot sets this one itself", a)
			}
		}
		if !strings.HasPrefix(a, "-") {
			return fmt.Errorf("--router-arg %q: give the option and its value as one --name=value", a)
		}
	}
	return nil
}

// dsnIsolateNets gives each named net a DSN class of its own, a copy of its
// class under the name <class>__POUR, so a router option that leaves classes
// unrouted can leave out those nets and not the ones that share their class.
// It returns the DSN and the classes that hold only the named nets, sorted. A
// named net with no class in the DSN is not routed anyway and is skipped.
func dsnIsolateNets(dsn string, nets []string) (string, []string, error) {
	want := map[string]bool{}
	for _, n := range nets {
		want[n] = true
	}
	var out strings.Builder
	var classes []string
	last := 0
	for _, at := range dsnFindAll(dsn, "class") {
		end := sexprEnd(dsn, at)
		if end < 0 {
			return dsn, nil, fmt.Errorf("unterminated class in the DSN")
		}
		text := dsn[at:end]
		m := reDSNClassName.FindStringSubmatchIndex(text)
		if m == nil {
			return dsn, nil, fmt.Errorf("a DSN class without a name")
		}
		name := strings.Trim(text[m[2]:m[3]], `"`)
		// The class's nets run from its name to its first sub-list.
		listEnd := m[3]
		for listEnd < len(text) && text[listEnd] != '(' {
			if text[listEnd] == '"' {
				q := strings.IndexByte(text[listEnd+1:], '"')
				if q < 0 {
					return dsn, nil, fmt.Errorf("unterminated name in DSN class %s", name)
				}
				listEnd += q + 1
			}
			listEnd++
		}
		var keep, move []string
		for _, tok := range reDSNToken.FindAllString(text[m[3]:listEnd], -1) {
			if want[strings.Trim(tok, `"`)] {
				move = append(move, tok)
			} else {
				keep = append(keep, tok)
			}
		}
		switch {
		case len(move) == 0:
			continue
		case len(keep) == 0:
			classes = append(classes, name)
			continue
		}
		copyName := name + "__POUR"
		classes = append(classes, copyName)
		out.WriteString(dsn[last:at])
		fmt.Fprintf(&out, "%s %s\n      %s\n    (class %q %s\n      %s", text[:m[3]], strings.Join(keep, " "), text[listEnd:], copyName, strings.Join(move, " "), text[listEnd:])
		last = end
	}
	out.WriteString(dsn[last:])
	sort.Strings(classes)
	return out.String(), classes, nil
}

// largeBoardConnections: from here on `kicad route` uses the large-board
// profile. Below it nothing changes.
const largeBoardConnections = 2000

// useLargeBoardProfile reads --large-board: auto, on or off.
func useLargeBoardProfile(mode string, connections int) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	}
	return connections >= largeBoardConnections
}
