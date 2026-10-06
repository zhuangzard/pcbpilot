package rules

import (
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// table is the frozen Resolver. Everything a lookup needs per (net, layer)
// is computed in Build; clearances are cached lazily (clearance.go).
type table struct {
	book     Book // deep copy taken by Build
	layers   []Layer
	layerIdx []int16 // LayerID → position in layers, -1 = unknown
	netIdx   []int32 // NetID → position in infos, -1 = default class-0 net
	infos    []netInfo
	def      netInfo // a net the Book knows nothing about
	keepouts [][]Keepout
	edge     int64
	warnings []string // collected by Build
	clrCache
}

// netInfo is the resolved rule state of one net.
type netInfo struct {
	net    geom.NetID
	class  ClassID
	intent *Intent
	prof   int32       // clearance profile (clearance.go)
	rows   []layerRule // per layer position
	vias   []ViaType
}

// layerRule is one net's resolved width, neck-down and floors on one layer.
type layerRule struct {
	width   int64
	neckMin int64
	zone    int64 // explicit NeckZone, -1 = derived from the pad
	neck    bool  // neck-down allowed
	floors  Floors
}

var _ Resolver = (*table)(nil)

// Build validates the Book and freezes it into a Resolver. Later changes to
// the Book do not affect the returned Resolver.
func (b *Book) Build() (Resolver, error) {
	b.warnings = nil
	if len(b.layers) == 0 {
		return nil, errors.New("rules: no layers")
	}
	pcb := b.scopes[scopeKey{kind: ScopePCB}]
	if pcb == nil || pcb.Width == nil || *pcb.Width <= 0 {
		return nil, errors.New("rules: the pcb scope has no track width")
	}
	if _, ok := pcb.Clearance[Generic]; !ok {
		return nil, errors.New("rules: the pcb scope has no generic clearance")
	}
	t := &table{book: b.freeze(), layers: slices.Clone(b.layers)}
	for i, l := range t.layers {
		if l.ID < 0 {
			return nil, fmt.Errorf("rules: layer %q has a negative id", l.Name)
		}
		for int(l.ID) >= len(t.layerIdx) {
			t.layerIdx = append(t.layerIdx, -1)
		}
		if t.layerIdx[l.ID] >= 0 {
			return nil, fmt.Errorf("rules: layer id %d added twice", l.ID)
		}
		t.layerIdx[l.ID] = int16(i)
	}
	byName := map[string]ViaType{}
	for _, v := range b.vias {
		if _, dup := byName[v.Name]; dup {
			return nil, fmt.Errorf("rules: via %q added twice", v.Name)
		}
		byName[v.Name] = v
	}

	var nets []geom.NetID
	seen := map[geom.NetID]bool{}
	add := func(n geom.NetID) {
		if n > 0 && !seen[n] {
			seen[n] = true
			nets = append(nets, n)
		}
	}
	for n := range b.class {
		add(n)
	}
	for k := range b.scopes {
		add(k.net)
	}
	for n := range b.intent {
		add(n)
	}
	slices.Sort(nets)
	warned := map[geom.NetID]bool{}
	var err error
	if t.def, err = t.resolveNet(0, byName, warned); err != nil {
		return nil, err
	}
	for _, n := range nets {
		info, err := t.resolveNet(n, byName, warned)
		if err != nil {
			return nil, err
		}
		for int(n) >= len(t.netIdx) {
			t.netIdx = append(t.netIdx, -1)
		}
		t.netIdx[n] = int32(len(t.infos))
		t.infos = append(t.infos, info)
		if info.intent != nil && info.intent.Mains {
			t.warnings = append(t.warnings, fmt.Sprintf("net %d: mains or hazardous voltage (%g V): review clearance and creepage by hand", n, info.intent.VoltageV))
		}
	}
	t.edge = DefaultEdge
	if b.edge != nil {
		t.edge = *b.edge
	}
	t.keepouts = make([][]Keepout, len(t.layers))
	for i, l := range t.layers {
		for _, k := range b.keepouts {
			if k.Layer == l.ID || k.Layer == geom.AllLayers {
				t.keepouts[i] = append(t.keepouts[i], k)
			}
		}
	}
	t.initProfiles()
	b.warnings = t.warnings
	return t, nil
}

// freeze deep-copies the Book.
func (b *Book) freeze() Book {
	c := Book{
		layers:   slices.Clone(b.layers),
		class:    map[geom.NetID]ClassID{},
		scopes:   map[scopeKey]*RuleSet{},
		vias:     slices.Clone(b.vias),
		keepouts: slices.Clone(b.keepouts),
		intent:   map[geom.NetID]Intent{},
		edge:     b.edge,
	}
	for k, v := range b.class {
		c.class[k] = v
	}
	for k, v := range b.scopes {
		rs := v.clone()
		c.scopes[k] = &rs
	}
	for k, v := range b.intent {
		c.intent[k] = v
	}
	for _, r := range b.regions {
		r.rs = r.rs.clone()
		c.regions = append(c.regions, r)
	}
	return c
}

// resolveNet computes a net's rows on every layer (04 §3.6 Resolve).
func (t *table) resolveNet(n geom.NetID, vias map[string]ViaType, warned map[geom.NetID]bool) (netInfo, error) {
	b := &t.book
	info := netInfo{net: n, class: b.class[n]}
	if in, ok := b.intent[n]; ok && n != 0 {
		info.intent = &in
	}
	info.rows = make([]layerRule, len(t.layers))
	for i := range t.layers {
		l := &t.layers[i]
		row, explicit := t.resolveRow(info, l)
		if explicit < row.floors.Width && !warned[n] && n != 0 {
			warned[n] = true // 04 §5: warn once per net
			t.warnings = append(t.warnings, fmt.Sprintf("net %d: rule width %d nm < intent width %d nm on %s; the intent floor applies",
				n, explicit, row.floors.Width, l.Name))
		}
		info.rows[i] = row
	}
	var err error
	info.vias, err = t.resolveVias(info, vias)
	return info, err
}

// chain merges the width scopes of a net on a layer, lowest precedence first.
func (t *table) chain(info netInfo, layer geom.LayerID) RuleSet {
	b := &t.book
	var out RuleSet
	for _, k := range []scopeKey{
		{kind: ScopePCB},
		{kind: ScopeLayer, layer: layer},
		{kind: ScopeClass, class: info.class},
		{kind: ScopeClassLayer, class: info.class, layer: layer},
		{kind: ScopeNet, net: info.net},
		{kind: ScopeNetLayer, net: info.net, layer: layer},
	} {
		if k.net == 0 && (k.kind == ScopeNet || k.kind == ScopeNetLayer) {
			continue
		}
		if rs := b.scope(k); rs != nil {
			out.merge(rs)
		}
	}
	return out
}

// intentScopes are the explicit ScopeIntent sets of a net on a layer.
func (t *table) intentScopes(n geom.NetID, layer geom.LayerID) []*RuleSet {
	if n == 0 {
		return nil
	}
	var out []*RuleSet
	for _, k := range []scopeKey{{kind: ScopeIntent, net: n, layer: geom.AllLayers}, {kind: ScopeIntent, net: n, layer: layer}} {
		if rs := t.book.scope(k); rs != nil {
			out = append(out, rs)
		}
	}
	return out
}

// resolveRow returns the row of a net on layer l and the explicit width the
// scopes gave before any floor.
func (t *table) resolveRow(info netInfo, l *Layer) (layerRule, int64) {
	rs := t.chain(info, l.ID)
	explicit := *rs.Width
	var fl Floors
	if in := info.intent; in != nil {
		fl.Width = in.widthFloor(l)
		fl.Clearance = voltageFloor(in, nil, l)
	}
	for _, s := range t.intentScopes(info.net, l.ID) {
		if s.Width != nil {
			fl.Width = max(fl.Width, *s.Width)
		}
		if v, ok := s.clr(Generic); ok {
			fl.Clearance = max(fl.Clearance, v)
		}
	}
	row := layerRule{floors: fl, zone: -1}
	row.width = max(explicit, fl.Width, l.MinWidth)
	row.neckMin = row.width // 04 §3.5: MinWidth defaults to the width
	if rs.MinWidth != nil {
		row.neckMin = *rs.MinWidth
	}
	row.neckMin = max(row.neckMin, l.MinWidth)
	if in := info.intent; in != nil {
		row.neckMin = max(row.neckMin, in.neckFloor(l))
	}
	row.neck = row.neckMin < row.width
	if in := info.intent; in != nil && (in.NoNeckDown || in.ControlledImpedance) {
		row.neck = false
	}
	if !row.neck {
		row.neckMin = row.width
	}
	if rs.NeckZone != nil {
		row.zone = min(max(*rs.NeckZone, 0), maxNeckZone)
	}
	return row, explicit
}

// resolveVias lists the net's allowed vias: use_via of the highest scope that
// sets it, in its order, or the whole catalogue. A high-current net (more
// current than one via of the first choice carries) tries the largest first.
func (t *table) resolveVias(info netInfo, byName map[string]ViaType) ([]ViaType, error) {
	b := &t.book
	var names []string
	for _, k := range []scopeKey{{kind: ScopePCB}, {kind: ScopeClass, class: info.class}, {kind: ScopeNet, net: info.net}} {
		if k.kind == ScopeNet && info.net == 0 {
			continue
		}
		if rs := b.scope(k); rs != nil && rs.UseVia != nil {
			names = rs.UseVia
		}
	}
	var out []ViaType
	if names == nil {
		out = slices.Clone(b.vias)
	}
	for _, name := range names {
		v, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("rules: net %d uses via %q, which is not in the catalogue", info.net, name)
		}
		out = append(out, v)
	}
	if in := info.intent; in != nil && len(out) > 0 && in.CurrentA > ViaAmpacity(out[0].Drill, 0, in.tempRise()) {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Pad > out[j].Pad })
	}
	return out, nil
}

// info returns the resolved state of a net.
func (t *table) info(n geom.NetID) *netInfo {
	if n > 0 && int(n) < len(t.netIdx) {
		if i := t.netIdx[n]; i >= 0 {
			return &t.infos[i]
		}
	}
	return &t.def
}

// row returns a net's row on a layer; an unknown layer is resolved on the
// spot without layer rules, fab minimum or copper data.
func (t *table) row(info *netInfo, layer geom.LayerID) layerRule {
	if layer >= 0 && int(layer) < len(t.layerIdx) {
		if i := t.layerIdx[layer]; i >= 0 {
			return info.rows[i]
		}
	}
	r, _ := t.resolveRow(*info, &Layer{ID: layer})
	return r
}

func (t *table) Layers() []Layer               { return t.layers }
func (t *table) Class(net geom.NetID) ClassID  { return t.info(net).class }
func (t *table) Vias(net geom.NetID) []ViaType { return t.info(net).vias }
func (t *table) Edge() int64                   { return t.edge }
func (t *table) Width(net geom.NetID, layer geom.LayerID) int64 {
	return t.row(t.info(net), layer).width
}

func (t *table) IntentFloors(net geom.NetID, layer geom.LayerID) Floors {
	return t.row(t.info(net), layer).floors
}

// Keepouts lists the keep-outs on a layer (AllLayers: every keep-out).
func (t *table) Keepouts(layer geom.LayerID) []Keepout {
	if layer >= 0 && int(layer) < len(t.layerIdx) {
		if i := t.layerIdx[layer]; i >= 0 {
			return t.keepouts[i]
		}
	}
	var out []Keepout
	for _, k := range t.book.keepouts {
		if layer == geom.AllLayers || k.Layer == layer || k.Layer == geom.AllLayers {
			out = append(out, k)
		}
	}
	return out
}
