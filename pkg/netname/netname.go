// Package netname is pcbpilot's one net-identity rule, shared by every
// producer and consumer that matches schematic nets with board nets
// (kicad netlist, kicad snapshot, signoff, sim post-layout, pad-net-diff).
//
// The identity of a net is KiCad's own full name in KiCad's escaped form —
// exactly what a .kicad_pcb (and so `kicad snapshot`) carries:
//
//	"/Sheet/Sub/NAME"  a sheet-local label: the sheet path, then the label
//	"/NAME"            a local label on the root sheet
//	"NAME"             a global label or power net; also "Net-(R1-Pad2)"
//
// Inside a name every '/' that is not a sheet-path separator is written
// "{slash}" (KiCad label "VPP/MCLR" on the root sheet = "/VPP{slash}MCLR").
// So in the escaped form the last literal '/' always ends the sheet path,
// and a name is never cut anywhere else. kicad-cli's XML netlist writes
// names unescaped, so "/pic_sockets/VCC_PIC" may be the sub-sheet label
// "VCC_PIC" or the root label "pic_sockets/VCC_PIC": Candidates lists the
// readings and the caller picks one from evidence (internal/kicad: the
// labels of each sheet file, the sheets of the net's pins) — never by
// guessing; an undecidable name is an error.
//
// The short (display) name — Local — is the name without its sheet path,
// unescaped ("VPP/MCLR"); it is only a display field. A display name, or a
// name from an older document, resolves to an identity through Resolver:
// the exact identity, else the one identity whose display name it is;
// several such identities are Ambiguous, never a pick.
//
// "unconnected-(…)" is KiCad's name for a single-pin net with no name: it
// is no net at all (IsUnconnected) on either side of a comparison.
package netname

import (
	"sort"
	"strings"
)

// UnconnectedPrefix starts KiCad's name of a lone unnamed pin's net.
const UnconnectedPrefix = "unconnected-("

// IsUnconnected reports a KiCad "unconnected-(…)" net (no net).
func IsUnconnected(n string) bool { return strings.HasPrefix(n, UnconnectedPrefix) }

// Escape writes a name part in KiCad's net-name form: '/' → "{slash}",
// line breaks dropped. An already escaped part is unchanged.
func Escape(s string) string {
	if !strings.ContainsAny(s, "/\r\n") {
		return s
	}
	return strings.NewReplacer("/", "{slash}", "\r", "", "\n", "").Replace(s)
}

// Unescape is the inverse of Escape.
func Unescape(s string) string { return strings.ReplaceAll(s, "{slash}", "/") }

// Candidates lists the sheet paths a net name as kicad-cli's XML writes it
// (unescaped) can start with, given the design's sheet paths ("/",
// "/Sub/", …; each ends with '/'), shortest first: the root "/" for every
// name starting with '/', plus each longer sheet path it starts with. A
// name not starting with '/' (global, power, "Net-(…)") has none. One
// candidate = the reading is certain; several = evidence must decide.
func Candidates(name string, sheets []string) []string {
	if !strings.HasPrefix(name, "/") {
		return nil
	}
	out := []string{"/"}
	for _, s := range sheets {
		if s != "/" && strings.HasSuffix(s, "/") && strings.HasPrefix(name, s) && len(name) > len(s) {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return len(out[i]) < len(out[j]) })
	return out
}

// Join is the identity of an XML (unescaped) net name read with sheet path
// prefix ("" for a name without one): the prefix, then the rest escaped.
// The prefix is used as given: a sheet path whose sheet names hold '/'
// must be escaped first (EscapedSheetPaths, JoinEscaped).
func Join(prefix, name string) string { return prefix + Escape(name[len(prefix):]) }

// JoinEscaped is Join with the sheet path prefix written as escPrefix,
// its escaped form ("/I{slash}O/" for the XML path "/I/O/").
func JoinEscaped(prefix, escPrefix, name string) string {
	return escPrefix + Escape(name[len(prefix):])
}

// Sheet is one sheet of a kicadxml netlist: its path as written there
// (unescaped, "/I/O (1/2)/") and its UUID path ("/u1/").
type Sheet struct{ Name, Tstamps string }

// EscapedSheetPaths maps each sheet path as kicad-cli's XML writes it to
// KiCad's escaped form, the one the board's net names carry: every '/'
// inside a sheet name is "{slash}" ("/I/O (1/2)/" → "/I{slash}O
// (1{slash}2)/"; KiCad 10.0.6, also what its kicadsexpr netlist writes).
// Where one sheet name ends is told by the UUID path, never guessed: a
// sheet's parent is the sheet whose UUID path drops the last UUID, so its
// own name is the rest of its path. A sheet whose parent is not listed
// keeps its path as written. A path that two sheets write identically but
// escape differently maps to "" (ambiguous).
func EscapedSheetPaths(sheets []Sheet) map[string]string {
	byTs := map[string]string{"/": "/"}
	for _, s := range sheets {
		if s.Tstamps != "" {
			byTs[s.Tstamps] = s.Name
		}
	}
	memo := map[string]string{"/": "/"}
	var esc func(ts string, depth int) (string, bool)
	esc = func(ts string, depth int) (string, bool) {
		if e, ok := memo[ts]; ok {
			return e, true
		}
		name, ok := byTs[ts]
		if !ok || depth > 64 || !strings.HasPrefix(ts, "/") || !strings.HasSuffix(ts, "/") || !strings.HasSuffix(name, "/") {
			return "", false
		}
		trim := strings.TrimSuffix(ts, "/")
		pts := trim[:strings.LastIndex(trim, "/")+1]
		pname, ok := byTs[pts]
		if !ok || !strings.HasPrefix(name, pname) || len(name) <= len(pname)+1 {
			return "", false
		}
		pesc, ok := esc(pts, depth+1)
		if !ok {
			return "", false
		}
		e := pesc + Escape(name[len(pname):len(name)-1]) + "/"
		memo[ts] = e
		return e, true
	}
	out := map[string]string{"/": "/"}
	for _, s := range sheets {
		if s.Name == "" || s.Name == "/" {
			continue
		}
		e, ok := esc(s.Tstamps, 0)
		if !ok {
			e = s.Name
		}
		if prev, seen := out[s.Name]; seen && prev != e {
			e = ""
		}
		out[s.Name] = e
	}
	return out
}

// SheetPath is the sheet path of an identity ("" for a global name).
func SheetPath(id string) string {
	if !strings.HasPrefix(id, "/") {
		return ""
	}
	return id[:strings.LastIndex(id, "/")+1]
}

// Local is the display name of an identity: the sheet path dropped and
// "{slash}" read back as '/'.
func Local(id string) string { return Unescape(id[len(SheetPath(id)):]) }

// Display maps each identity of a design to its short name: Local when
// that name resolves (Resolver over the same identities) to this identity
// alone — not another net's identity ("/{slash}a{slash}X" has Local "/a/X",
// the identity of another net), not an alias shared with another net —
// else the identity itself. So no display name equals another net's
// identity or display name, and each resolves back to its own net.
func Display(ids []string) map[string]string {
	r := NewResolver(ids)
	out := map[string]string{}
	for _, id := range ids {
		out[id] = id
		if l := Local(id); l != "" && l != id {
			if got, m := r.Resolve(l); m == Found && got == id {
				out[id] = l
			}
		}
	}
	return out
}

// Resolver turns any name of a net — its identity, its display name, or
// its unescaped full name (older documents) — into the identity among a
// known set (a board's nets, a design's nets).
type Resolver struct {
	ids   map[string]bool
	alias map[string][]string // display / unescaped full name → identities
}

// NewResolver indexes the identities ids ("" and unconnected nets skipped).
func NewResolver(ids []string) *Resolver {
	r := &Resolver{ids: map[string]bool{}, alias: map[string][]string{}}
	for _, id := range ids {
		if id == "" || IsUnconnected(id) || r.ids[id] {
			continue
		}
		r.ids[id] = true
	}
	for id := range r.ids {
		seen := map[string]bool{}
		for _, a := range []string{Local(id), Unescape(id)} {
			if a != id && !seen[a] {
				seen[a] = true
				r.alias[a] = append(r.alias[a], id)
			}
		}
	}
	for _, v := range r.alias {
		sort.Strings(v)
	}
	return r
}

// Match is how a name resolved.
type Match int

const (
	Unknown   Match = iota // no identity carries the name
	Found                  // the identity itself, or the one carrying the alias
	Ambiguous              // several identities carry it (Candidates lists them)
)

// Resolve returns the identity of name and Found; name and Unknown when no
// identity carries it; name and Ambiguous when several do (two sheets with
// the same local label): never guessed, and never to be looked up
// elsewhere as if unknown.
func (r *Resolver) Resolve(name string) (string, Match) {
	if r.ids[name] {
		return name, Found
	}
	switch v := r.alias[name]; len(v) {
	case 0:
		return name, Unknown
	case 1:
		return v[0], Found
	}
	return name, Ambiguous
}

// Candidates lists the identities an ambiguous name could be.
func (r *Resolver) Candidates(name string) []string { return append([]string(nil), r.alias[name]...) }

// Has reports whether id is one of the identities.
func (r *Resolver) Has(id string) bool { return r.ids[id] }
