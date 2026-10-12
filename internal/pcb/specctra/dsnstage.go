package specctra

import (
	"fmt"
	"strings"
)

// StageSplit is SplitClassesForStage's result.
type StageSplit struct {
	// FirstClasses are the new classes holding the first-stage nets; every one
	// copies the circuit and rule of the class its nets came from.
	FirstClasses []string `json:"firstClasses"`
	// RestClasses are the classes left holding the other nets.
	RestClasses []string `json:"restClasses"`
	// Missing are first-stage nets no class lists (they stay in kicad_default
	// style implicit classes and cannot be staged).
	Missing []string `json:"missing,omitempty"`
}

// StageSuffix names the first-stage copy of a class.
const StageSuffix = "__first"

// SplitClassesForStage rewrites the DSN's (network) so every net of first moves
// into a copy "<class>__first" of its class (same circuit and rule). A router
// told to ignore the returned RestClasses then routes only the first-stage
// nets (stage 1); after LockWiring froze that copper, an unmodified run routes
// the rest (stage 2). The nets, pins and rules are untouched — only the class
// membership changes — so nothing about the design moves.
func SplitClassesForStage(src string, first []string) (string, StageSplit, error) {
	var rep StageSplit
	want := map[string]bool{}
	for _, n := range first {
		want[strings.ToUpper(strings.Trim(n, `"`))] = true
	}
	found := map[string]bool{}
	var out strings.Builder
	rest := map[string]bool{}
	i := 0
	for i < len(src) {
		j := strings.Index(src[i:], "(class ")
		if j < 0 {
			out.WriteString(src[i:])
			break
		}
		start := i + j
		end, err := matchParen(src, start)
		if err != nil {
			return "", rep, err
		}
		out.WriteString(src[i:start])
		block := src[start : end+1]
		nl, body, ok := splitClassBlock(block)
		if !ok {
			out.WriteString(block)
			i = end + 1
			continue
		}
		name, nets := nl[0], nl[1:]
		var sel, keep []string
		for _, n := range nets {
			if want[strings.ToUpper(strings.Trim(n, `"`))] {
				sel = append(sel, n)
				found[strings.ToUpper(strings.Trim(n, `"`))] = true
			} else {
				keep = append(keep, n)
			}
		}
		indent := lineIndent(src, start)
		emit := func(nm string, ns []string) {
			fmt.Fprintf(&out, "(class %s %s\n%s  %s", quoteIfNeeded(nm), strings.Join(ns, " "), indent, body)
		}
		switch {
		case len(sel) == 0:
			out.WriteString(block)
			rest[name] = true // kicad_default too: it holds every net no class lists
		default:
			if len(keep) > 0 {
				emit(name, keep)
				out.WriteString("\n" + indent)
				rest[name] = true
			}
			fn := name + StageSuffix
			emit(fn, sel)
			rep.FirstClasses = append(rep.FirstClasses, fn)
		}
		i = end + 1
	}
	for n := range want {
		if !found[n] {
			rep.Missing = append(rep.Missing, n)
		}
	}
	for c := range rest {
		rep.RestClasses = append(rep.RestClasses, c)
	}
	sortStrings(rep.RestClasses)
	sortStrings(rep.Missing)
	if len(rep.FirstClasses) == 0 {
		return "", rep, fmt.Errorf("none of the first-stage nets is listed in a (class ...) of the DSN")
	}
	return out.String(), rep, nil
}

// matchParen returns the index of the ')' closing the '(' at start, skipping
// quoted strings.
func matchParen(s string, start int) (int, error) {
	depth, inQ := 0, false
	for k := start; k < len(s); k++ {
		c := s[k]
		switch {
		case inQ:
			if c == '"' {
				inQ = false
			}
		case c == '"':
			inQ = true
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return k, nil
			}
		}
	}
	return 0, fmt.Errorf("unbalanced parenthesis at offset %d", start)
}

// splitClassBlock splits "(class NAME net… (circuit…)(rule…))" into the
// leading atoms (name first) and the text from the first '(' child to the
// block's closing ')' inclusive.
func splitClassBlock(block string) (atoms []string, body string, ok bool) {
	s := strings.TrimPrefix(block, "(class")
	k, inQ := 0, false
	var tok strings.Builder
	flush := func() {
		if tok.Len() > 0 {
			atoms = append(atoms, tok.String())
			tok.Reset()
		}
	}
	for ; k < len(s); k++ {
		c := s[k]
		if inQ {
			tok.WriteByte(c)
			if c == '"' {
				inQ = false
			}
			continue
		}
		switch {
		case c == '"':
			inQ = true
			tok.WriteByte(c)
		case c == '(':
			flush()
			return atoms, strings.TrimLeft(s[k:], " \t\r\n") + "", len(atoms) >= 1
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			flush()
		default:
			tok.WriteByte(c)
		}
	}
	return nil, "", false
}

func lineIndent(s string, at int) string {
	ls := strings.LastIndexByte(s[:at], '\n') + 1
	return s[ls:at]
}

func sortStrings(x []string) {
	for i := 1; i < len(x); i++ {
		for j := i; j > 0 && x[j] < x[j-1]; j-- {
			x[j], x[j-1] = x[j-1], x[j]
		}
	}
}
