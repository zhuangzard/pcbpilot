package specctra

import (
	"fmt"
	"slices"
	"strings"
)

// LockReport says what LockWiring froze.
type LockReport struct {
	Nets     []string `json:"nets"`
	Segments int      `json:"segments"`
	Vias     int      `json:"vias"`
}

// LockWiring writes the routed copper of the nets in lock (case-insensitive)
// from a session into the DSN's wiring as (type fix): a router started on the
// result treats it as immovable obstacle copper and only routes what is left
// (layered routing, v0.9.2 E4). Coordinates and widths of w are mil; milToUnit
// converts them to the DSN's resolution unit. layers are the DSN's copper
// layer names; a segment on any other layer is an error, not skipped.
func LockWiring(src string, w *Wiring, lock []string, layers []string, milToUnit float64) (string, LockReport, error) {
	rep := LockReport{}
	want := map[string]bool{}
	for _, n := range lock {
		want[strings.ToUpper(n)] = true
	}
	seen := map[string]bool{}
	var b strings.Builder
	for i, s := range w.Segments {
		if !want[strings.ToUpper(s.Net)] {
			continue
		}
		if !slices.Contains(layers, s.Layer) {
			return "", rep, fmt.Errorf("segment %d (%s): layer %q is not one of %v", i, s.Net, s.Layer, layers)
		}
		fmt.Fprintf(&b, "    (wire (path %s %s %s %s %s %s) (net %s) (type fix))\n", s.Layer, fnum(s.WidthMil*milToUnit),
			fnum(s.A[0]*milToUnit), fnum(s.A[1]*milToUnit), fnum(s.B[0]*milToUnit), fnum(s.B[1]*milToUnit), quoteIfNeeded(s.Net))
		rep.Segments++
		seen[strings.ToUpper(s.Net)] = true
	}
	for _, v := range w.Vias {
		if !want[strings.ToUpper(v.Net)] {
			continue
		}
		if v.Padstack == "" {
			return "", rep, fmt.Errorf("via of %s at %v has no padstack name", v.Net, v.At)
		}
		fmt.Fprintf(&b, "    (via %s %s %s (net %s) (type fix))\n", quoteIfNeeded(v.Padstack), fnum(v.At[0]*milToUnit), fnum(v.At[1]*milToUnit), quoteIfNeeded(v.Net))
		rep.Vias++
		seen[strings.ToUpper(v.Net)] = true
	}
	for n := range seen {
		rep.Nets = append(rep.Nets, n)
	}
	slices.Sort(rep.Nets)
	if rep.Segments+rep.Vias == 0 {
		return src, rep, nil
	}
	out, err := appendWiring(src, b.String())
	return out, rep, err
}
