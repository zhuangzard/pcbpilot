package dsn

import (
	"bufio"
	"cmp"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// sesStep is the SES coordinate step in nm: (resolution um 10) is 0.1 µm
// (spec 04 §3.8).
const sesStep = 100

// snap rounds a length in nm to SES units, halves away from zero. It is a
// pure function of the value, so endpoints that are shared in nm stay
// shared in the session.
func snap(v int64) int64 {
	if v < 0 {
		return -snap(-v)
	}
	return (v + sesStep/2) / sesStep
}

type sesPt struct{ X, Y int64 }

func snapPt(p geom.Pt) sesPt { return sesPt{snap(p.X), snap(p.Y)} }

func (a sesPt) cmp(b sesPt) int {
	if c := cmp.Compare(a.X, b.X); c != 0 {
		return c
	}
	return cmp.Compare(a.Y, b.Y)
}

// sesWire is one (wire (path …)): a polyline of constant width on one layer.
type sesWire struct {
	layer string
	width int64
	pts   []sesPt
}

type sesVia struct {
	padstack string
	at       sesPt
}

// WriteSES writes the routed copper of v as a Specctra session (spec 04
// §3.8): every Track and Via that is not Fixed. Fixed copper is never
// changed by the router, so it is left out. Joined tracks of one net, layer
// and width are written as one path; a path ends where three or more meet.
// Nets are sorted by name, wires by first point, vias by position, so the
// output is byte-deterministic. Vias that match no catalogue padstack get a
// generated one in library_out.
func WriteSES(w io.Writer, v board.View, rs rules.Resolver) error {
	layers := rs.Layers()
	layerName := func(id geom.LayerID) (string, error) {
		for _, l := range layers {
			if l.ID == id {
				return l.Name, nil
			}
		}
		return "", fmt.Errorf("dsn: unknown layer %d", id)
	}
	type wireKey struct {
		layer string
		width int64
	}
	type netCopper struct {
		segs map[wireKey][][2]sesPt
		vias []sesVia
	}
	byNet := map[geom.NetID]*netCopper{}
	newPads := map[string]board.Item{}
	var err error
	v.Items(func(it board.Item) bool {
		if it.Fixed || (it.Kind != board.Track && it.Kind != board.Via) {
			return true
		}
		nc := byNet[it.Net]
		if nc == nil {
			nc = &netCopper{segs: map[wireKey][][2]sesPt{}}
			byNet[it.Net] = nc
		}
		switch it.Kind {
		case board.Track:
			s, ok := it.Shape.(geom.Seg)
			if !ok {
				err = fmt.Errorf("dsn: track %d is not a segment", it.ID)
				return false
			}
			var ln string
			if ln, err = layerName(it.From); err != nil {
				return false
			}
			a, b := snapPt(s.A), snapPt(s.B)
			if a == b {
				return true
			}
			k := wireKey{ln, snap(2 * s.HalfW)}
			nc.segs[k] = append(nc.segs[k], [2]sesPt{a, b})
		case board.Via:
			c, ok := it.Shape.(geom.Circle)
			if !ok {
				err = fmt.Errorf("dsn: via %d is not a circle", it.ID)
				return false
			}
			name := viaPadstack(rs, it, c, len(layers))
			if name == "" {
				name = fmt.Sprintf("pcbroute_via_%d_%d_%d", snap(2*c.R), it.From, it.To)
				newPads[name] = it
			}
			nc.vias = append(nc.vias, sesVia{padstack: name, at: snapPt(c.C)})
		}
		return true
	})
	if err != nil {
		return err
	}

	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "(session pcbroute\n  (routes\n    (resolution um 10)\n")
	fmt.Fprintf(bw, "    (parser\n      (host_cad \"pcbpilot\")\n      (host_version \"engine v2\")\n    )\n")
	if len(newPads) > 0 {
		fmt.Fprintf(bw, "    (library_out\n")
		for _, name := range sortedKeys(newPads) {
			it := newPads[name]
			fmt.Fprintf(bw, "      (padstack %s\n", name)
			from, to := span(it, len(layers))
			for l := from; l <= to; l++ {
				ln, err := layerName(l)
				if err != nil {
					return err
				}
				fmt.Fprintf(bw, "        (shape (circle %s %d 0 0))\n", atom(ln), snap(2*it.Shape.(geom.Circle).R))
			}
			fmt.Fprintf(bw, "      )\n")
		}
		fmt.Fprintf(bw, "    )\n")
	}
	type namedNet struct {
		name string
		nc   *netCopper
	}
	var nets []namedNet
	for id, nc := range byNet {
		nets = append(nets, namedNet{v.NetName(id), nc})
	}
	slices.SortFunc(nets, func(a, b namedNet) int { return strings.Compare(a.name, b.name) })
	fmt.Fprintf(bw, "    (network_out\n")
	for _, n := range nets {
		var wires []sesWire
		for k, segs := range n.nc.segs {
			for _, p := range chains(segs) {
				wires = append(wires, sesWire{layer: k.layer, width: k.width, pts: p})
			}
		}
		slices.SortFunc(wires, func(a, b sesWire) int {
			if c := slices.CompareFunc(a.pts, b.pts, sesPt.cmp); c != 0 {
				return c
			}
			if c := strings.Compare(a.layer, b.layer); c != 0 {
				return c
			}
			return cmp.Compare(a.width, b.width)
		})
		slices.SortFunc(n.nc.vias, func(a, b sesVia) int {
			if c := a.at.cmp(b.at); c != 0 {
				return c
			}
			return strings.Compare(a.padstack, b.padstack)
		})
		fmt.Fprintf(bw, "      (net %s\n", atom(n.name))
		for _, wr := range wires {
			fmt.Fprintf(bw, "        (wire (path %s %d", atom(wr.layer), wr.width)
			for _, p := range wr.pts {
				fmt.Fprintf(bw, " %d %d", p.X, p.Y)
			}
			fmt.Fprintf(bw, "))\n")
		}
		for _, vi := range n.nc.vias {
			fmt.Fprintf(bw, "        (via %s %d %d)\n", atom(vi.padstack), vi.at.X, vi.at.Y)
		}
		fmt.Fprintf(bw, "      )\n")
	}
	fmt.Fprintf(bw, "    )\n  )\n)\n")
	return bw.Flush()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// span is an item's layer range with AllLayers expanded.
func span(it board.Item, n int) (geom.LayerID, geom.LayerID) {
	if it.From == geom.AllLayers || it.To == geom.AllLayers {
		return 0, geom.LayerID(n - 1)
	}
	return it.From, it.To
}

// viaPadstack names the catalogue via (the net's first, then the board's)
// whose land and span match the item, or "" if none does.
func viaPadstack(rs rules.Resolver, it board.Item, c geom.Circle, n int) string {
	from, to := span(it, n)
	for _, net := range []geom.NetID{it.Net, 0} {
		for _, vt := range rs.Vias(net) {
			if vt.Pad == 2*c.R && vt.From == from && vt.To == to {
				return vt.Name
			}
		}
	}
	return ""
}

// chains joins segments into polylines. A polyline runs through points where
// exactly two segments meet and stops elsewhere; it starts at the smallest
// such stop point, so the result does not depend on the input order.
func chains(segs [][2]sesPt) [][]sesPt {
	segs = slices.Clone(segs)
	for i, s := range segs {
		if s[1].cmp(s[0]) < 0 {
			segs[i] = [2]sesPt{s[1], s[0]}
		}
	}
	slices.SortFunc(segs, func(a, b [2]sesPt) int {
		if c := a[0].cmp(b[0]); c != 0 {
			return c
		}
		return a[1].cmp(b[1])
	})
	adj := map[sesPt][]int{}
	for i, s := range segs {
		adj[s[0]] = append(adj[s[0]], i)
		adj[s[1]] = append(adj[s[1]], i)
	}
	used := make([]bool, len(segs))
	walk := func(p sesPt, i int) []sesPt {
		out := []sesPt{p}
		for {
			used[i] = true
			q := segs[i][0]
			if q == p {
				q = segs[i][1]
			}
			out = append(out, q)
			if len(adj[q]) != 2 {
				return out
			}
			next := adj[q][0]
			if next == i {
				next = adj[q][1]
			}
			if used[next] {
				return out
			}
			p, i = q, next
		}
	}
	var stops []sesPt
	for p, ix := range adj {
		if len(ix) != 2 {
			stops = append(stops, p)
		}
	}
	slices.SortFunc(stops, sesPt.cmp)
	var out [][]sesPt
	for _, p := range stops {
		for _, i := range adj[p] {
			if !used[i] {
				out = append(out, walk(p, i))
			}
		}
	}
	for i := range segs { // closed loops
		if !used[i] {
			out = append(out, walk(segs[i][0], i))
		}
	}
	return out
}

// ReadSES reads a session's routed wires and vias back as board items (for
// round-trip tests and for loading another router's session). Net names are
// resolved through v (net IDs 1 … NumNets), layers and via padstacks through
// rs and the session's library_out. Items have ID 0; (type fix) and
// (type protect) copper is Fixed.
func ReadSES(src []byte, v board.View, rs rules.Resolver) ([]board.Item, error) {
	root, err := specctra.ParseExpr(string(src))
	if err != nil {
		return nil, fmt.Errorf("dsn: SES: %w", err)
	}
	if root.Head() != "session" {
		return nil, fmt.Errorf("dsn: not a Specctra session: top level is (%s ...)", root.Head())
	}
	routes := root.Child("routes")
	if routes == nil {
		return nil, fmt.Errorf("dsn: SES has no (routes)")
	}
	res := routes.Child("resolution")
	if res == nil || len(res.Atoms()) < 2 {
		return nil, fmt.Errorf("dsn: SES routes have no (resolution)")
	}
	unit, ok := nmPerUnit(res.Atoms()[0])
	div, err := num(res.Atoms()[1])
	if !ok || err != nil || div <= 0 {
		return nil, fmt.Errorf("dsn: SES resolution %v not supported", res.Atoms())
	}
	r := &reader{scale: unit / div, layerIdx: map[string]geom.LayerID{}, padstacks: map[string]*specctra.Expr{}}
	r.layers = rs.Layers()
	for _, l := range r.layers {
		r.layerIdx[l.Name] = l.ID
	}
	if lib := routes.Child("library_out"); lib != nil {
		for _, ps := range lib.Children("padstack") {
			if a := ps.Atoms(); len(a) > 0 {
				r.padstacks[a[0]] = ps
			}
		}
	}
	nets := map[string]geom.NetID{}
	for id := geom.NetID(1); int(id) <= v.NumNets(); id++ {
		nets[v.NetName(id)] = id
	}
	var out []board.Item
	no := routes.Child("network_out")
	if no == nil {
		return out, nil
	}
	for _, n := range no.Children("net") {
		a := n.Atoms()
		if len(a) == 0 {
			continue
		}
		net, ok := nets[a[0]]
		if !ok {
			return nil, fmt.Errorf("dsn: SES net %q is not on the board", a[0])
		}
		for _, wr := range n.Children("wire") {
			p := wr.Child("path")
			if p == nil {
				return nil, fmt.Errorf("dsn: SES net %s: wire without (path)", a[0])
			}
			layer, shapes, err := r.shape(p, r.absolute)
			if err != nil {
				return nil, fmt.Errorf("dsn: SES net %s: %w", a[0], err)
			}
			l, ok := r.layerIdx[layer]
			if !ok {
				return nil, fmt.Errorf("dsn: SES net %s: unknown layer %q", a[0], layer)
			}
			for _, s := range shapes {
				if _, isSeg := s.(geom.Seg); isSeg {
					out = append(out, board.Item{Kind: board.Track, Net: net, From: l, To: l, Shape: s, Fixed: isFixedType(wr)})
				}
			}
		}
		for _, vi := range n.Children("via") {
			va := vi.Atoms()
			if len(va) < 3 {
				return nil, fmt.Errorf("dsn: SES net %s: malformed via", a[0])
			}
			x, err1 := num(va[1])
			y, err2 := num(va[2])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("dsn: SES net %s: bad via coordinate", a[0])
			}
			it := board.Item{Kind: board.Via, Net: net, Fixed: isFixedType(vi)}
			var land int64
			if _, ok := r.padstacks[va[0]]; ok {
				if land, it.From, it.To, err = r.viaLand(va[0]); err != nil {
					return nil, fmt.Errorf("dsn: SES: %w", err)
				}
			} else if vt, ok := catalogueVia(rs, net, va[0]); ok {
				land, it.From, it.To = vt.Pad/2, vt.From, vt.To
			} else {
				return nil, fmt.Errorf("dsn: SES net %s: via padstack %q unknown", a[0], va[0])
			}
			it.Shape = geom.Circle{C: r.absolute(x, y), R: land}
			out = append(out, it)
		}
	}
	return out, nil
}

func catalogueVia(rs rules.Resolver, net geom.NetID, name string) (rules.ViaType, bool) {
	for _, n := range []geom.NetID{net, 0} {
		for _, vt := range rs.Vias(n) {
			if vt.Name == name {
				return vt, true
			}
		}
	}
	return rules.ViaType{}, false
}
