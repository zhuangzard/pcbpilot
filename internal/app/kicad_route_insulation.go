package app

// The router has to know the insulation pairs. KiCad's rule file carries them,
// but the DSN gives each class one clearance, so without this fastroute lays
// copper across a pair and only the final DRC notices.

import (
	"fmt"
	"regexp"
	"strings"
)

var reDSNClassName = regexp.MustCompile(`\(class\s+("[^"]*"|[^\s()]+)`)

// dsnInsulationRules adds a class_class clearance for every two DSN classes on
// the two sides of an insulation pair (a DSN class joins netclass names with
// '+'). marginUm is the margin every other DSN clearance carries. A DSN whose
// network section cannot be found comes back unchanged, with count 0.
func dsnInsulationRules(dsn string, iso []map[string]any, marginUm float64) (string, int) {
	var names []string
	for _, m := range reDSNClassName.FindAllStringSubmatch(dsn, -1) {
		names = append(names, strings.Trim(m[1], `"`))
	}
	member := func(name, class string) bool {
		for _, p := range strings.Split(name, "+") {
			if p == class {
				return true
			}
		}
		return false
	}
	seen := map[string]float64{}
	var order []string
	for _, p := range iso {
		a, b := fmt.Sprint(p["a"]), fmt.Sprint(p["b"])
		mm, _ := p["clearanceMm"].(float64)
		if mm <= 0 {
			continue
		}
		ca, cb := domainClass(a), domainClass(b)
		for _, x := range names {
			for _, y := range names {
				if x >= y {
					continue
				}
				if !(member(x, ca) && member(y, cb) || member(x, cb) && member(y, ca)) {
					continue
				}
				key := x + "\x00" + y
				um := mm*1000 + marginUm
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
		return dsn, 0
	}
	at := strings.Index(dsn, "(network")
	if at < 0 {
		return dsn, 0
	}
	end := sexprEnd(dsn, at)
	if end < 0 {
		return dsn, 0
	}
	var b strings.Builder
	for _, key := range order {
		pair := strings.SplitN(key, "\x00", 2)
		fmt.Fprintf(&b, "    (class_class (classes %q %q) (rule (clearance %g)))\n", pair[0], pair[1], seen[key])
	}
	return dsn[:end-1] + b.String() + dsn[end-1:], len(order)
}
