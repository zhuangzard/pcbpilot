package dsn

import (
	"errors"
	"fmt"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// The board and rules implementations arrive with M4 and M2; these recording
// fakes stand in for them.

var errFake = errors.New("fake builder")

// recBoard records board.Builder calls and doubles as a board.View.
type recBoard struct {
	nets  []string
	items []board.Item
}

func (b *recBoard) AddNet(name string) geom.NetID {
	b.nets = append(b.nets, name)
	return geom.NetID(len(b.nets))
}

func (b *recBoard) AddItem(it board.Item) board.ItemID {
	it.ID = board.ItemID(len(b.items) + 1)
	b.items = append(b.items, it)
	return it.ID
}

func (b *recBoard) Build(rules.Resolver) (board.DB, error) { return nil, errFake }

func (b *recBoard) NumNets() int                { return len(b.nets) }
func (b *recBoard) NetName(n geom.NetID) string { return b.nets[n-1] }
func (b *recBoard) Item(id board.ItemID) (board.Item, bool) {
	if id == 0 || int(id) > len(b.items) {
		return board.Item{}, false
	}
	return b.items[id-1], true
}
func (b *recBoard) Items(fn func(board.Item) bool) {
	for _, it := range b.items {
		if !fn(it) {
			return
		}
	}
}
func (b *recBoard) Query(geom.Rect, geom.LayerID, func(board.Item) bool) {}
func (b *recBoard) Connections() []board.Connection                      { return nil }
func (b *recBoard) Connection(board.ConnID) (board.Connection, bool) {
	return board.Connection{}, false
}
func (b *recBoard) Connected(a, c board.ItemID) bool { return false }

type setCall struct {
	s  rules.Scope
	rs rules.RuleSet
}

// recRules records rules.Builder calls and answers the few Resolver queries
// the SES code needs.
type recRules struct {
	layers   []rules.Layer
	class    map[geom.NetID]rules.ClassID
	sets     []setCall
	vias     []rules.ViaType
	keepouts []rules.Keepout
}

func (r *recRules) AddLayer(l rules.Layer) { r.layers = append(r.layers, l) }
func (r *recRules) SetClass(n geom.NetID, c rules.ClassID) {
	if r.class == nil {
		r.class = map[geom.NetID]rules.ClassID{}
	}
	r.class[n] = c
}
func (r *recRules) Set(s rules.Scope, rs rules.RuleSet) { r.sets = append(r.sets, setCall{s, rs}) }
func (r *recRules) AddVia(v rules.ViaType)              { r.vias = append(r.vias, v) }
func (r *recRules) AddKeepout(k rules.Keepout)          { r.keepouts = append(r.keepouts, k) }
func (r *recRules) Build() (rules.Resolver, error)      { return nil, errFake }

func (r *recRules) Layers() []rules.Layer                                   { return r.layers }
func (r *recRules) Class(n geom.NetID) rules.ClassID                        { return r.class[n] }
func (r *recRules) Width(geom.NetID, geom.LayerID) int64                    { return 0 }
func (r *recRules) Neck(geom.NetID, geom.LayerID, rules.PadSize) rules.Neck { return rules.Neck{} }
func (r *recRules) Clearance(a, b rules.Obj, l geom.LayerID) int64          { return 0 }
func (r *recRules) Vias(geom.NetID) []rules.ViaType                         { return r.vias }
func (r *recRules) Edge() int64                                             { return 0 }
func (r *recRules) Keepouts(geom.LayerID) []rules.Keepout                   { return r.keepouts }
func (r *recRules) IntentFloors(geom.NetID, geom.LayerID) rules.Floors      { return rules.Floors{} }

// load reads src into fresh fakes.
func load(src []byte) (*recBoard, *recRules, []string, error) {
	b, r := &recBoard{}, &recRules{}
	w, err := ReadWarnings(src, b, r)
	return b, r, w, err
}

// counts are the bench acceptance numbers of a design: pins (distinct pad
// Refs), nets, and connections = Σ over nets of (terminals − 1), where a
// net's terminals are its pins plus its plane zones.
func counts(b *recBoard) (pins, nets, conns int) {
	refs := map[string]bool{}
	terms := map[geom.NetID]map[string]bool{}
	add := func(n geom.NetID, key string) {
		if terms[n] == nil {
			terms[n] = map[string]bool{}
		}
		terms[n][key] = true
	}
	for _, it := range b.items {
		switch it.Kind {
		case board.Pad:
			refs[it.Ref] = true
			if it.Net != 0 {
				add(it.Net, it.Ref)
			}
		case board.Zone:
			add(it.Net, fmt.Sprint("zone", it.ID))
		}
	}
	for _, t := range terms {
		conns += len(t) - 1
	}
	return len(refs), len(b.nets), conns
}
