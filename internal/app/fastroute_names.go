package app

// fastroute_names.go — net names fastroute's own text files cannot carry.
//
// fastroute's --pairs and --tune files are split on whitespace with no
// quoting ("pair A B gap=…": a quoted or backslash-escaped name with a space
// is still split — `unknown option 'USB'`, observed with fastroute 0.1.14),
// and a tune net line treats * as a wildcard. KiCad hierarchical net names
// carry sheet titles verbatim ("/CAN-FD, RS-422, USB service/FT_DM", auto
// pages " (1/2)"). Such a net is renamed for fastroute only: in its copy of
// the DSN the net's identifier token becomes a bare alias (pcbpilot_net_N),
// the pairs / tune files name the alias, and in the session fastroute
// writes every net identifier token that is the alias (bare or quoted)
// gets back the net's original DSN token, byte for byte. The board, KiCad's
// SES import and every report keep the real name.
//
// DSN / SES tokens (KiCad's Specctra export, string_quote "): a quoted
// atom runs to the next " with no escapes — KiCad writes an embedded " as
// two apostrophes and maps it back on SES import (KiCad 10.0.6, verified); a backslash
// is literal; a name without space, parenthesis or quote may be bare. Only
// whole atoms in net identifier positions are touched: the atom after a
// (net head, and the net atoms of a (class NAME net net …) list.

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// fastrouteSafeName: the name is one token of a fastroute pairs / tune file.
func fastrouteSafeName(n string) bool {
	return n != "" && !strings.ContainsAny(n, " \t\r\n\"'=#*?\\") && n == strings.TrimSpace(n)
}

// dsnAtom is one atom of a DSN / SES text: [start, end) of its raw text.
type dsnAtom struct {
	start, end int
	raw        string
	netID      bool // a net identifier position
}

// dsnDecode is an atom's value: a quoted atom without its quotes and with
// KiCad's two apostrophes for an embedded quote mapped back.
func dsnDecode(raw string) string {
	if len(raw) >= 2 && raw[0] == '"' && raw[len(raw)-1] == '"' {
		return strings.ReplaceAll(raw[1:len(raw)-1], "''", `"`)
	}
	return raw
}

// dsnAtoms tokenises a Specctra text (quote char ").
func dsnAtoms(text string) []dsnAtom {
	type frame struct {
		head  string
		atoms int // atoms seen in this list (the head is atom 0)
	}
	var out []dsnAtom
	var stack []frame
	i := 0
	for i < len(text) {
		c := text[i]
		switch {
		case c == '(':
			stack = append(stack, frame{})
			i++
		case c == ')':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			i++
		case c == ' ' || c == '\t' || c == '\r' || c == '\n':
			i++
		default:
			start := i
			if len(stack) > 0 && stack[len(stack)-1].head == "string_quote" {
				// (string_quote ") — the quote character itself.
				i++
			} else if c == '"' {
				if j := strings.IndexByte(text[i+1:], '"'); j < 0 {
					i = len(text)
				} else {
					i += j + 2
				}
			} else {
				for i < len(text) && !strings.ContainsRune("() \t\r\n", rune(text[i])) {
					i++
				}
			}
			a := dsnAtom{start: start, end: i, raw: text[start:i]}
			if len(stack) > 0 {
				f := &stack[len(stack)-1]
				if f.atoms == 0 {
					f.head = a.raw
				}
				a.netID = f.head == "net" && f.atoms == 1 || f.head == "class" && f.atoms >= 2
				f.atoms++
			}
			out = append(out, a)
		}
	}
	return out
}

// dsnReplaceNetIDs rewrites the net identifier atoms: repl returns the new
// raw text and true to replace one.
func dsnReplaceNetIDs(text string, repl func(raw, value string) (string, bool)) string {
	var b strings.Builder
	last := 0
	for _, a := range dsnAtoms(text) {
		if !a.netID {
			continue
		}
		if nr, ok := repl(a.raw, dsnDecode(a.raw)); ok {
			b.WriteString(text[last:a.start])
			b.WriteString(nr)
			last = a.end
		}
	}
	b.WriteString(text[last:])
	return b.String()
}

// fastrouteNetAliases maps the unsafe names of one DSN to aliases.
type fastrouteNetAliases struct {
	toAlias map[string]string
	toName  map[string]string
	// raw: alias → the net's identifier token as the DSN wrote it.
	raw map[string]string
}

// newFastrouteNetAliases picks an alias for every unsafe name in names;
// an alias never equals any atom of dsn.
func newFastrouteNetAliases(names []string, dsn string) *fastrouteNetAliases {
	a := &fastrouteNetAliases{toAlias: map[string]string{}, toName: map[string]string{}, raw: map[string]string{}}
	used := map[string]bool{}
	for _, at := range dsnAtoms(dsn) {
		used[dsnDecode(at.raw)] = true
	}
	names = append([]string(nil), names...) // never reorder the caller's slice
	sort.Strings(names)
	i := 0
	for _, n := range names {
		if fastrouteSafeName(n) || a.toAlias[n] != "" {
			continue
		}
		var al string
		for {
			i++
			if al = fmt.Sprintf("pcbpilot_net_%d", i); !used[al] {
				break
			}
		}
		a.toAlias[n], a.toName[al] = al, n
	}
	return a
}

func (a *fastrouteNetAliases) empty() bool { return a == nil || len(a.toAlias) == 0 }

// name is the token fastroute's files use for net n.
func (a *fastrouteNetAliases) name(n string) string {
	if a != nil {
		if al := a.toAlias[n]; al != "" {
			return al
		}
	}
	return n
}

// real is the net an alias stands for (n itself otherwise).
func (a *fastrouteNetAliases) real(n string) string {
	if a != nil {
		if r := a.toName[n]; r != "" {
			return r
		}
	}
	return n
}

// apply renames the aliased nets' identifier tokens in DSN text and keeps
// each net's first original token for unaliasSession. It fails when an
// aliased net has no identifier token in the DSN.
func (a *fastrouteNetAliases) apply(dsn string) (string, error) {
	if a.empty() {
		return dsn, nil
	}
	seen := map[string]bool{}
	out := dsnReplaceNetIDs(dsn, func(raw, value string) (string, bool) {
		al := a.toAlias[value]
		if al == "" {
			return "", false
		}
		if !seen[al] {
			a.raw[al] = raw // the first spelling (the network section)
		}
		seen[al] = true
		return al, true
	})
	for _, n := range sortedKeysOf(a.toAlias) {
		if !seen[a.toAlias[n]] {
			return "", fmt.Errorf("net %q has no net identifier in the DSN", n)
		}
	}
	return out, nil
}

// unaliasSession writes src to dst with every alias net identifier (bare
// or quoted) replaced by the net's original DSN token.
func (a *fastrouteNetAliases) unaliasSession(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	out := dsnReplaceNetIDs(string(b), func(raw, value string) (string, bool) {
		if a.toName[value] == "" {
			return "", false
		}
		if r := a.raw[value]; r != "" {
			return r, true
		}
		return `"` + strings.ReplaceAll(a.toName[value], `"`, "''") + `"`, true
	})
	return os.WriteFile(dst, []byte(out), 0o644)
}

var reAliasWord = regexp.MustCompile(`pcbpilot_net_\d+[A-Za-z0-9_]*`)

// realText replaces whole alias words in a free-text line (report lists).
func (a *fastrouteNetAliases) realText(s string) string {
	if a.empty() {
		return s
	}
	return reAliasWord.ReplaceAllStringFunc(s, a.real)
}

// intentRouterNets are the nets intentPairsAndTune names.
func intentRouterNets(in *designIntent) []string {
	var out []string
	for _, name := range in.sortedNetNames() {
		n := in.Nets[name]
		if n.DiffPair != "" && in.Nets[n.DiffPair] != nil {
			out = append(out, name, n.DiffPair)
		}
		if n.LengthGroup != "" {
			out = append(out, name)
		}
	}
	return out
}
