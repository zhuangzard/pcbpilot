package board

import (
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// stressItem is a random item at odd nanometre coordinates: diagonal and
// orthogonal tracks on one of 3 layers, vias spanning layers, all-layer
// zones and net-0 copper.
func stressItem(r *rand.Rand) Item {
	net := geom.NetID(r.Intn(4)) // 0 is "no net"
	p := func() int64 { return int64(r.Intn(8*mm)) + 1 }
	x, y := p(), p()
	switch r.Intn(4) {
	case 0:
		l := geom.LayerID(r.Intn(3))
		d := int64(r.Intn(3 * mm))
		bx, by := x+d, y
		switch r.Intn(3) {
		case 1:
			bx, by = x, y+d
		case 2:
			bx, by = x+d, y+d
		}
		return Item{Kind: Track, Net: net, From: l, To: l,
			Shape: geom.Seg{A: geom.Pt{X: x, Y: y}, B: geom.Pt{X: bx, Y: by}, HalfW: int64(r.Intn(mm/5)) + 1}}
	case 1:
		f := geom.LayerID(r.Intn(3))
		return Item{Kind: Via, Net: net, From: f, To: f + geom.LayerID(r.Intn(3-int(f))),
			Shape: geom.Circle{C: geom.Pt{X: x, Y: y}, R: int64(r.Intn(mm/2)) + 1}}
	case 2:
		return Item{Kind: Zone, Net: net, From: geom.AllLayers, To: geom.AllLayers,
			Shape: geom.Rect{MinX: x, MinY: y, MaxX: x + int64(r.Intn(mm)) + 1, MaxY: y + int64(r.Intn(mm)) + 1}}
	}
	l := geom.LayerID(r.Intn(3))
	return Item{Kind: Track, Net: net, From: l, To: l, Shape: geom.Poly{Pts: []geom.Pt{
		{X: x, Y: y}, {X: x + int64(r.Intn(mm)) + 1, Y: y}, {X: x, Y: y + int64(r.Intn(mm)) + 1}}}}
}

// checkInvariants checks Item.Owner against Route.Items and Query against a
// linear scan of Items on random windows and every layer.
func checkInvariants(t *testing.T, v View, r *rand.Rand, step int) {
	t.Helper()
	owned := map[ItemID]ConnID{}
	for _, c := range v.Connections() {
		if c.Route == nil {
			continue
		}
		if len(c.Route.Items) == 0 {
			t.Fatalf("step %d: conn %d has an empty, non-nil route", step, c.ID)
		}
		for _, it := range c.Route.Items {
			live, ok := v.Item(it.ID)
			if !ok || !reflect.DeepEqual(live, it) || it.Owner != c.ID {
				t.Fatalf("step %d: conn %d route item %d is %+v live=%v %+v", step, c.ID, it.ID, it, ok, live)
			}
			owned[it.ID] = c.ID
		}
	}
	var all []Item
	v.Items(func(it Item) bool {
		if it.Owner != 0 && owned[it.ID] != it.Owner {
			t.Fatalf("step %d: item %d owned by %d but not in its route", step, it.ID, it.Owner)
		}
		all = append(all, it)
		return true
	})
	for k := 0; k < 6; k++ {
		x, y := int64(r.Intn(10*mm)), int64(r.Intn(10*mm))
		box := geom.Rect{MinX: x, MinY: y, MaxX: x + int64(r.Intn(3*mm)) + 1, MaxY: y + int64(r.Intn(3*mm)) + 1}
		for _, l := range []geom.LayerID{geom.AllLayers, 0, 1, 2} {
			var got, want []ItemID
			v.Query(box, l, func(it Item) bool { got = append(got, it.ID); return true })
			for _, it := range all {
				if onLayer(it, l) && it.Shape.Bounds().Intersects(box) {
					want = append(want, it.ID)
				}
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("step %d: Query(%v, %d) = %v, want %v", step, box, l, got, want)
			}
		}
	}
}

// TestStressEdits mixes Add, Remove, SetRoute, nested overlays up to three
// deep, Commit, Rollback, Snapshot and Restore, and checks connectivity
// against the BFS oracle, Owner/route consistency, Query against a linear
// scan, and that Rollback and Restore give back the exact earlier state.
func TestStressEdits(t *testing.T) {
	for seed := int64(1); seed <= 6; seed++ {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := rand.New(rand.NewSource(seed))
			b := NewBuilder()
			for i := 1; i <= 3; i++ {
				b.AddNet(fmt.Sprint("N", i))
			}
			for i := 0; i < 15; i++ {
				f := geom.LayerID(r.Intn(3))
				p := pad(geom.NetID(1+i%3), fmt.Sprintf("U%d-%d", i/3, i%3), int64(r.Intn(9*mm)), int64(r.Intn(9*mm)), f, f)
				p.Fixed = r.Intn(2) == 0
				b.AddItem(p)
			}
			for i := 0; i < 10; i++ {
				b.AddItem(stressItem(r))
			}
			d, err := b.Build(nil)
			if err != nil {
				t.Fatal(err)
			}
			conns := d.Connections()
			var snaps []Snapshot
			var snapDumps []string
			clean := true // no non-route edit since the last snapshot
			for step := 0; step < 80; step++ {
				before := dump(d)
				tx := d.Begin()
				chain := []Txn{tx}
				for depth := r.Intn(3); depth > 0; depth-- {
					chain = append(chain, chain[len(chain)-1].Begin())
				}
				cur := chain[len(chain)-1]
				for k := 0; k < 5; k++ {
					switch op := r.Intn(5); {
					case op == 0 && len(conns) > 0:
						c := conns[r.Intn(len(conns))].ID
						if r.Intn(4) == 0 {
							cur.SetRoute(c, nil)
							break
						}
						net := conns[c-1].Net
						var items []Item
						for n := r.Intn(3) + 1; n > 0; n-- {
							it := stressItem(r)
							it.Net = net
							items = append(items, it)
						}
						cur.SetRoute(c, &Route{Items: items})
					case op == 1 && len(conns) > 0:
						it := stressItem(r)
						c := conns[r.Intn(len(conns))]
						it.Net, it.Owner = c.Net, c.ID
						cur.Add(it)
					case op == 2:
						var live []ItemID
						cur.Items(func(it Item) bool {
							if it.Kind != Pad {
								live = append(live, it.ID)
							}
							return true
						})
						if len(live) > 0 {
							id := live[r.Intn(len(live))]
							if it, _ := cur.Item(id); it.Owner == 0 {
								clean = false
							}
							cur.Remove(id)
						}
					default:
						clean = false
						cur.Add(stressItem(r))
					}
				}
				checkConn(t, cur, step)
				checkInvariants(t, cur, r, step)
				// Fold the chain back, committing or dropping each level.
				dropped := false
				for i := len(chain) - 1; i >= 0; i-- {
					if r.Intn(4) == 0 {
						chain[i].Rollback()
						if i == 0 {
							dropped = true
						}
					} else {
						chain[i].Commit()
					}
					if i > 0 {
						checkConn(t, chain[i-1], step)
						checkInvariants(t, chain[i-1], r, step)
					}
				}
				if dropped {
					if got := dump(d); got != before {
						t.Fatalf("step %d: rollback changed the DB:\n%s\nwant\n%s", step, got, before)
					}
				}
				checkConn(t, d, step)
				checkInvariants(t, d, r, step)
				switch r.Intn(6) {
				case 0:
					snaps, snapDumps = append(snaps, d.Snapshot()), append(snapDumps, dump(d))
					clean = true
				case 1:
					if len(snaps) > 0 {
						i := len(snaps) - 1
						d.Restore(snaps[i])
						checkConn(t, d, step)
						checkInvariants(t, d, r, step)
						if clean {
							if got := dump(d); got != snapDumps[i] {
								t.Fatalf("step %d: restore:\n%s\nwant\n%s", step, got, snapDumps[i])
							}
						}
					}
				}
			}
		})
	}
}

// TestBuilderClosed: the DB shares the builder's slices, so adding to the
// builder after Build must panic instead of rewriting committed items.
func TestBuilderClosed(t *testing.T) {
	b := NewBuilder()
	n := b.AddNet("A")
	b.AddItem(via(n, 0, 0))
	if _, err := b.Build(nil); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]func(){
		"AddItem": func() { b.AddItem(via(n, mm, 0)) },
		"AddNet":  func() { b.AddNet("B") },
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s after Build did not panic", name)
				}
			}()
			f()
		}()
	}
}
