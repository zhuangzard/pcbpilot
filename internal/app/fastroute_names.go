package app

// fastroute_names.go — net names fastroute's own text files cannot carry.
//
// fastroute's --pairs and --tune files are split on whitespace with no
// quoting ("pair A B gap=…": a quoted or backslash-escaped name with a space
// is still split — `unknown option 'USB'`, observed with fastroute 0.1.14),
// and a tune net line treats * as a wildcard. KiCad hierarchical net names
// carry sheet titles verbatim ("/CAN-FD, RS-422, USB service/FT_DM", auto
// pages " (1/2)"). Such a net is renamed for fastroute only: in the DSN its
// quoted name becomes a plain alias token (pcbpilot_net_N), the pairs / tune
// files name the alias, and the session fastroute writes gets the real
// names back before it is imported (unaliasSession). The board, KiCad's SES
// import and every report keep the real name.

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

// fastrouteNetAliases maps the unsafe names of one DSN to aliases.
type fastrouteNetAliases struct {
	toAlias map[string]string
	toName  map[string]string
}

// newFastrouteNetAliases picks an alias for every unsafe name in names that
// is not already a token of dsn.
func newFastrouteNetAliases(names []string, dsn string) *fastrouteNetAliases {
	a := &fastrouteNetAliases{toAlias: map[string]string{}, toName: map[string]string{}}
	sort.Strings(names)
	i := 0
	for _, n := range names {
		if fastrouteSafeName(n) || a.toAlias[n] != "" {
			continue
		}
		var al string
		for {
			i++
			al = fmt.Sprintf("pcbpilot_net_%d", i)
			if !regexp.MustCompile(`(^|[\s()"])` + al + `($|[\s()"])`).MatchString(dsn) {
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

// apply renames the aliased nets in DSN text: every quoted occurrence of
// the name (KiCad's DSN quotes any name with a space, a parenthesis or a
// quote character) becomes the alias. It fails when a name is not found.
func (a *fastrouteNetAliases) apply(dsn string) (string, error) {
	if a.empty() {
		return dsn, nil
	}
	for _, n := range sortedKeysOf(a.toAlias) {
		q := `"` + n + `"`
		if !strings.Contains(dsn, "(net "+q) {
			return "", fmt.Errorf("net %q is not in the DSN as a quoted net name", n)
		}
		dsn = strings.ReplaceAll(dsn, q, a.toAlias[n])
	}
	return dsn, nil
}

var (
	reSesToken       = regexp.MustCompile(`pcbpilot_net_\d+`)
	reSesQuotedToken = regexp.MustCompile(`"pcbpilot_net_\d+"|pcbpilot_net_\d+`)
)

// unaliasSession writes src with the real net names (quoted) to dst.
func (a *fastrouteNetAliases) unaliasSession(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	// fastroute writes the alias bare or quoted (0.1.14: (net "pcbpilot_net_1").
	out := reSesQuotedToken.ReplaceAllStringFunc(string(b), func(t string) string {
		if n := a.toName[strings.Trim(t, `"`)]; n != "" {
			return `"` + n + `"`
		}
		return t
	})
	return os.WriteFile(dst, []byte(out), 0o644)
}

// realText replaces aliases in a free-text line (report lists).
func (a *fastrouteNetAliases) realText(s string) string {
	if a.empty() {
		return s
	}
	return reSesToken.ReplaceAllStringFunc(s, a.real)
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
