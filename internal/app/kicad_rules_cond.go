package app

import (
	"regexp"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// druItem is one board net as a custom-rule condition sees it: its name,
// every name it may answer to (druDisjoint's identity) and the classes it
// is in for certain.
type druItem struct {
	net  string
	may  map[string]bool
	sure map[string]bool
}

// tri is a three-valued truth: a condition term is false, true, or not
// known from the names alone.
type tri int8

const (
	triFalse   tri = -1
	triUnknown tri = 0
	triTrue    tri = 1
)

var (
	reCondCompare = regexp.MustCompile(`^([AB])\.(NetName|NetClass)\s*(==|!=)\s*(?:'([^']*)'|"([^"]*)")$`)
	reCondHas     = regexp.MustCompile(`^([AB])\.hasNetclass\(\s*(?:'([^']*)'|"([^"]*)")\s*\)$`)
)

func druTrim(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") && balanced(s[1:len(s)-1]) {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}

// druEval evaluates a rule condition with A and B bound to a and b (nil:
// any item). '||', '&&', '!' and parentheses are followed; a net name or
// netclass test against a literal is decided where the item's names
// decide it; every other term (a property, a function, a wildcard) is
// unknown. The result is false only when no item so bound can match.
func druEval(cond string, a, b *druItem) tri {
	s := druTrim(cond)
	if s == "" {
		return triUnknown
	}
	if alts := splitTop(s, "||"); len(alts) > 1 {
		out := triFalse
		for _, x := range alts {
			out = max(out, druEval(x, a, b))
		}
		return out
	}
	if terms := splitTop(s, "&&"); len(terms) > 1 {
		out := triTrue
		for _, x := range terms {
			out = min(out, druEval(x, a, b))
		}
		return out
	}
	if strings.HasPrefix(s, "!") {
		rest := strings.TrimSpace(s[1:])
		if reCondHas.MatchString(rest) || strings.HasPrefix(rest, "(") && strings.HasSuffix(rest, ")") && balanced(rest[1:len(rest)-1]) {
			return -druEval(rest, a, b)
		}
		return triUnknown
	}
	pick := func(op string) *druItem {
		if op == "A" {
			return a
		}
		return b
	}
	if m := reCondHas.FindStringSubmatch(s); m != nil {
		it, lit := pick(m[1]), m[2]+m[3]
		switch {
		case it == nil || lit == "" || strings.ContainsAny(lit, "*?"):
			return triUnknown
		case it.sure[lit]:
			return triTrue
		case it.may[lit]:
			return triUnknown
		}
		return triFalse
	}
	if m := reCondCompare.FindStringSubmatch(s); m != nil {
		it, lit := pick(m[1]), m[4]+m[5]
		if it == nil || strings.ContainsAny(lit, "*?,") {
			return triUnknown
		}
		eq := triFalse
		switch {
		case m[2] == "NetName" && lit == it.net:
			eq = triTrue
		case lit != "" && it.may[lit]:
			eq = triUnknown
		case lit == "" && m[2] == "NetClass":
			eq = triUnknown
		}
		if m[3] == "!=" {
			return -eq
		}
		return eq
	}
	return triUnknown
}

// druNeverGoverns: a two-item condition provably matches no pair with it
// on either side (KiCad evaluates a pair in both orders).
func druNeverGoverns(cond string, it *druItem) bool {
	return druEval(cond, it, nil) == triFalse && druEval(cond, nil, it) == triFalse
}

// item is the net as conditions see it. A class is certain when every
// readback at hand has the net in it and it is no class of pcbpilot's own
// (those are rewritten by the plan).
func (r *rulesRun) item(net string, rb *kicad.RulesReadback) *druItem {
	it := &druItem{net: net, may: r.identity(net, rb), sure: map[string]bool{}}
	first := true
	for _, src := range []*kicad.RulesReadback{r.rb0, rb, r.rbStaged} {
		if src == nil {
			continue
		}
		in := map[string]bool{}
		for _, c := range strings.Split(src.Nets[net].Class, ",") {
			if c != "" && !reOwnedClass.MatchString(c) {
				in[c] = true
			}
		}
		if first {
			it.sure, first = in, false
			continue
		}
		for c := range it.sure {
			if !in[c] {
				delete(it.sure, c)
			}
		}
	}
	return it
}
