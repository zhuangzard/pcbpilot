package board

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

const mm = 1_000_000

func pad(net geom.NetID, ref string, x, y int64, from, to geom.LayerID) Item {
	return Item{Kind: Pad, Net: net, From: from, To: to, Fixed: true, Ref: ref,
		Shape: geom.Rect{MinX: x - mm/2, MinY: y - mm/2, MaxX: x + mm/2 + 1, MaxY: y + mm/2 + 1}}
}

func track(net geom.NetID, l geom.LayerID, ax, ay, bx, by int64) Item {
	return Item{Kind: Track, Net: net, From: l, To: l,
		Shape: geom.Seg{A: geom.Pt{X: ax, Y: ay}, B: geom.Pt{X: bx, Y: by}, HalfW: mm / 10}}
}

func via(net geom.NetID, x, y int64) Item {
	return Item{Kind: Via, Net: net, From: 0, To: 1, Shape: geom.Circle{C: geom.Pt{X: x, Y: y}, R: mm / 4}}
}

// small builds two nets: A with pads at x = 0, 10, 20 mm (pad 3 on layer 1),
// B with two pads, a fixed track joining A's first two pads, and a keep-out.
func small(t *testing.T) DB {
	t.Helper()
	b := NewBuilder()
	a, bn := b.AddNet("A"), b.AddNet("B")
	b.AddItem(pad(a, "U1-1", 0, 0, 0, 0))
	b.AddItem(pad(a, "U1-2", 10*mm, 0, 0, 0))
	b.AddItem(pad(a, "U2-1", 20*mm, 0, 1, 1))
	b.AddItem(pad(bn, "U3-1", 0, 10*mm, 0, 1))
	b.AddItem(pad(bn, "U3-2", 20*mm, 10*mm, 0, 1))
	fx := track(a, 0, 0, 0, 10*mm, 0)
	fx.Fixed = true
	b.AddItem(fx)
	b.AddItem(Item{Kind: Keepout, From: geom.AllLayers, To: geom.AllLayers, Fixed: true,
		Shape: geom.Rect{MinX: 5 * mm, MinY: 4 * mm, MaxX: 6 * mm, MaxY: 6 * mm}})
	d, err := b.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// dump renders everything a View exposes, in its own iteration order.
func dump(v View) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "nets %d\n", v.NumNets())
	var ids []ItemID
	v.Items(func(it Item) bool {
		fmt.Fprintf(&sb, "%+v\n", it)
		ids = append(ids, it.ID)
		return true
	})
	for _, c := range v.Connections() {
		fmt.Fprintf(&sb, "conn %d net %d %v-%v route", c.ID, c.Net, c.From, c.To)
		if c.Route != nil {
			for _, it := range c.Route.Items {
				fmt.Fprintf(&sb, " %d", it.ID)
			}
		}
		sb.WriteByte('\n')
	}
	for _, a := range ids {
		for _, b := range ids {
			if b > a && v.Connected(a, b) {
				fmt.Fprintf(&sb, "%d~%d ", a, b)
			}
		}
	}
	// Query order is fixed per level but not the same across levels: compare sets.
	q := queryOrder(v)
	slices.Sort(q)
	fmt.Fprintf(&sb, "\nquery %v", q)
	return sb.String()
}

func queryOrder(v View) []ItemID {
	var q []ItemID
	v.Query(geom.Rect{MinX: -mm, MinY: -mm, MaxX: 30 * mm, MaxY: 30 * mm}, geom.AllLayers, func(it Item) bool {
		q = append(q, it.ID)
		return true
	})
	return q
}

func TestBuildConnections(t *testing.T) {
	d := small(t)
	cs := d.Connections()
	// A: {U1-1, U1-2} already joined by fixed copper, plus U2-1; B: two pads.
	if len(cs) != 2 {
		t.Fatalf("connections = %d, want 2: %+v", len(cs), cs)
	}
	if c := cs[0]; c.Net != 1 || c.From.Item != 2 || c.To.Item != 3 || c.To.At != (geom.Pt{X: 20 * mm}) {
		t.Errorf("net A connection = %+v, want U1-2 → U2-1", c)
	}
	if c := cs[1]; c.Net != 2 || c.From.Item != 4 || c.To.Item != 5 {
		t.Errorf("net B connection = %+v", c)
	}
	if !d.Connected(1, 2) || !d.Connected(1, 6) || d.Connected(1, 3) || d.Connected(1, 4) || d.Connected(7, 7) {
		t.Error("initial connectivity wrong")
	}
}

func TestBuildMST(t *testing.T) {
	// Five pads on a line in shuffled order: the tree is the four short gaps.
	b := NewBuilder()
	n := b.AddNet("N")
	for i, x := range []int64{30, 0, 40, 10, 20} {
		b.AddItem(pad(n, fmt.Sprint("P-", i), x*mm, 0, 0, 0))
	}
	d, err := b.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	for _, c := range d.Connections() {
		total += abs(c.From.At.X - c.To.At.X)
	}
	if len(d.Connections()) != 4 || total != 40*mm {
		t.Errorf("MST: %d connections, length %d, want 4 and %d", len(d.Connections()), total, 40*mm)
	}
}

func TestBuildErrors(t *testing.T) {
	for name, it := range map[string]Item{
		"no shape":  {Kind: Track},
		"bad net":   track(9, 0, 0, 0, mm, 0),
		"bad span":  {Kind: Pad, From: 1, To: 0, Shape: geom.Circle{R: 1}},
		"too far":   track(0, 0, 0, 0, geom.MaxCoord+1, 0),
		"has owner": {Kind: Track, Owner: 1, Shape: geom.Circle{R: 1}},
	} {
		b := NewBuilder()
		b.AddItem(it)
		if _, err := b.Build(nil); err == nil {
			t.Errorf("%s: Build accepted %+v", name, it)
		}
	}
}

func TestCommitRollback(t *testing.T) {
	d := small(t)
	before := dump(d)

	tx := d.Begin()
	id := tx.Add(track(1, 1, 10*mm, 0, 20*mm, 0)) // misses: U1-2 is on layer 0 only
	if tx.Connected(2, 3) {
		t.Error("track on layer 1 joined a layer-0 pad")
	}
	v := tx.Add(via(1, 10*mm, 0))
	if !tx.Connected(1, 3) || !tx.Connected(id, v) {
		t.Error("via did not join the layers")
	}
	if d.Connected(1, 3) {
		t.Error("DB sees an uncommitted edit")
	}
	tx.Remove(id)
	tx.Rollback()
	if got := dump(d); got != before {
		t.Errorf("rollback changed the DB:\n%s\nwant\n%s", got, before)
	}

	tx = d.Begin()
	id = tx.Add(track(1, 1, 10*mm, 0, 20*mm, 0))
	v = tx.Add(via(1, 10*mm, 0))
	inTxn := dump(tx)
	tx.Commit()
	if got := dump(d); got != inTxn {
		t.Errorf("commit differs from the transaction's view:\n%s\nwant\n%s", got, inTxn)
	}
	if !d.Connected(1, 3) {
		t.Error("committed via does not join")
	}

	tx = d.Begin()
	tx.Remove(v)
	tx.Remove(id)
	tx.Commit()
	if d.Connected(1, 3) {
		t.Error("removal not committed")
	}
	if _, ok := d.Item(v); ok {
		t.Error("removed item still visible")
	}
}

func TestNestedOverlays(t *testing.T) {
	d := small(t)
	tx := d.Begin()
	tx.Add(track(2, 0, 0, 10*mm, 10*mm, 10*mm))
	outer := dump(tx)

	sh := tx.Begin() // a shove attempt that fails
	sh.Add(track(2, 0, 10*mm, 10*mm, 20*mm, 10*mm))
	if !sh.Connected(4, 5) {
		t.Error("nested overlay does not see its parent's edit")
	}
	sh.Rollback()
	if got := dump(tx); got != outer {
		t.Error("nested rollback changed the parent")
	}

	sh = tx.Begin() // one that succeeds, with a deeper level inside
	deep := sh.Begin()
	deep.Add(track(2, 0, 10*mm, 10*mm, 20*mm, 10*mm))
	deep.Commit()
	if !sh.Connected(4, 5) || tx.Connected(4, 5) {
		t.Error("nested commit leaked or was lost")
	}
	want := dump(sh)
	sh.Commit()
	if got := dump(tx); got != want {
		t.Error("nested commit differs from the nested view")
	}
	tx.Commit()
	if got := dump(d); got != want || !d.Connected(4, 5) {
		t.Error("outer commit lost nested edits")
	}
}

func TestRoutesAndSnapshot(t *testing.T) {
	d := small(t)
	c := d.Connections()[1].ID // net B
	s0 := d.Snapshot()
	before := dump(d)

	tx := d.Begin()
	tx.SetRoute(c, &Route{Items: []Item{track(2, 0, 0, 10*mm, 20*mm, 10*mm)}})
	tx.Commit()
	cn, _ := d.Connection(c)
	if cn.Route == nil || len(cn.Route.Items) != 1 || cn.Route.Items[0].Owner != c || !d.Connected(4, 5) {
		t.Fatalf("SetRoute not applied: %+v", cn)
	}
	first := cn.Route.Items[0].ID
	s1 := d.Snapshot()
	routed := dump(d)

	// Reroute in two pieces, then trim one piece with Remove.
	tx = d.Begin()
	tx.SetRoute(c, &Route{Items: []Item{
		track(2, 1, 0, 10*mm, 10*mm, 10*mm), track(2, 1, 10*mm, 10*mm, 20*mm, 10*mm)}})
	cn, _ = tx.Connection(c)
	tx.Remove(cn.Route.Items[0].ID)
	if cn, _ = tx.Connection(c); len(cn.Route.Items) != 1 || tx.Connected(4, 5) {
		t.Error("Remove did not trim the route")
	}
	if _, ok := tx.Item(first); ok {
		t.Error("SetRoute kept the old route's items")
	}
	tx.Add(Item{Kind: Via, Net: 2, From: 0, To: 1, Owner: c, Shape: geom.Circle{C: geom.Pt{X: 7 * mm, Y: 10 * mm}, R: mm / 4}})
	if cn, _ = tx.Connection(c); len(cn.Route.Items) != 2 {
		t.Error("Add with Owner did not extend the route")
	}
	tx.Commit()

	d.Restore(s1)
	if got := dump(d); got != routed {
		t.Errorf("restore of the routed snapshot:\n%s\nwant\n%s", got, routed)
	}
	d.Restore(s0)
	if got := dump(d); !strings.HasPrefix(got, before[:strings.Index(before, "conn")]) || d.Connected(4, 5) {
		t.Errorf("restore of the empty snapshot:\n%s", got)
	}
	tx = d.Begin()
	tx.SetRoute(c, nil)
	tx.Commit()
	if cn, _ = d.Connection(c); cn.Route != nil {
		t.Error("rip-up left a route")
	}
}

// bfs is the brute-force oracle: connectivity by breadth-first search over
// all item pairs, using the exact distance kernel directly.
func bfs(v View) map[[2]ItemID]bool {
	var its []Item
	v.Items(func(it Item) bool { its = append(its, it); return true })
	adj := func(a, b Item) bool {
		if a.Net == 0 || a.Net != b.Net || a.Kind == Keepout || a.Kind == Edge || b.Kind == Keepout || b.Kind == Edge {
			return false
		}
		if a.Ref != "" && a.Ref == b.Ref {
			return true
		}
		lay := a.From == geom.AllLayers || b.From == geom.AllLayers || a.From <= b.To && b.From <= a.To
		return lay && geom.Dist(a.Shape, b.Shape) == 0
	}
	out := map[[2]ItemID]bool{}
	for i, s := range its {
		if s.Net == 0 || s.Kind == Keepout || s.Kind == Edge {
			continue
		}
		seen := map[int]bool{i: true}
		queue := []int{i}
		for len(queue) > 0 {
			k := queue[0]
			queue = queue[1:]
			out[[2]ItemID{s.ID, its[k].ID}] = true
			for j := range its {
				if !seen[j] && adj(its[k], its[j]) {
					seen[j] = true
					queue = append(queue, j)
				}
			}
		}
	}
	return out
}

func checkConn(t *testing.T, v View, step int) {
	t.Helper()
	want := bfs(v)
	var ids []ItemID
	v.Items(func(it Item) bool { ids = append(ids, it.ID); return true })
	for _, a := range ids {
		for _, b := range ids {
			if got := v.Connected(a, b); got != want[[2]ItemID{a, b}] {
				t.Fatalf("step %d: Connected(%d, %d) = %v, oracle %v", step, a, b, got, !got)
			}
		}
	}
}

func randItem(r *rand.Rand) Item {
	net := geom.NetID(1 + r.Intn(3))
	x, y := int64(r.Intn(20))*mm, int64(r.Intn(20))*mm
	switch r.Intn(3) {
	case 0:
		l := geom.LayerID(r.Intn(2))
		if r.Intn(2) == 0 {
			return track(net, l, x, y, x+int64(r.Intn(8))*mm, y)
		}
		return track(net, l, x, y, x, y+int64(r.Intn(8))*mm)
	case 1:
		return via(net, x, y)
	}
	return Item{Kind: Zone, Net: net, From: geom.LayerID(r.Intn(2)), To: 1,
		Shape: geom.Poly{Pts: []geom.Pt{{X: x, Y: y}, {X: x + 2*mm, Y: y}, {X: x, Y: y + 2*mm}}}}
}

func TestRandomConnectivity(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	b := NewBuilder()
	for i := 1; i <= 3; i++ {
		b.AddNet(fmt.Sprint("N", i))
	}
	for i := 0; i < 12; i++ {
		l := geom.LayerID(r.Intn(2))
		b.AddItem(pad(geom.NetID(1+i%3), fmt.Sprintf("U%d-%d", i/3, i%3), int64(r.Intn(20))*mm, int64(r.Intn(20))*mm, l, l))
	}
	d, err := b.Build(nil)
	if err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 60; step++ {
		tx := d.Begin()
		var nest Txn
		cur := tx
		if step%3 == 0 {
			nest = tx.Begin()
			cur = nest
		}
		for k := 0; k < 4; k++ {
			var live []ItemID
			cur.Items(func(it Item) bool {
				if it.Kind != Pad {
					live = append(live, it.ID)
				}
				return true
			})
			if len(live) > 0 && r.Intn(3) == 0 {
				cur.Remove(live[r.Intn(len(live))])
			} else {
				cur.Add(randItem(r))
			}
		}
		checkConn(t, cur, step)
		if nest != nil {
			if step%2 == 0 {
				nest.Commit()
			} else {
				nest.Rollback()
			}
			checkConn(t, tx, step)
		}
		if r.Intn(4) == 0 {
			tx.Rollback()
		} else {
			tx.Commit()
		}
		checkConn(t, d, step)
	}
}

func TestDeterministicOrder(t *testing.T) {
	build := func() string {
		d := small(t)
		tx := d.Begin()
		for i := int64(0); i < 20; i++ {
			tx.Add(track(geom.NetID(1+i%2), geom.LayerID(i%2), i*mm, 0, i*mm, 5*mm))
		}
		tx.Remove(tx.Add(via(1, 3*mm, 3*mm)))
		tx.Commit()
		return dump(d) + fmt.Sprint(queryOrder(d))
	}
	want := build()
	for i := 0; i < 5; i++ {
		if got := build(); got != want {
			t.Fatal("dump differs between identical builds")
		}
	}
}

func TestMisuse(t *testing.T) {
	d := small(t)
	mustPanic := func(name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f()
	}
	tx := d.Begin()
	mustPanic("second Begin", func() { d.Begin() })
	mustPanic("remove fixed pad", func() { tx.Remove(1) })
	mustPanic("restore while open", func() { d.Restore(d.Snapshot()) })
	child := tx.Begin()
	mustPanic("edit parent with open child", func() { tx.Add(via(1, 0, 0)) })
	child.Rollback()
	tx.Commit()
	mustPanic("use after commit", func() { tx.Add(via(1, 0, 0)) })
}
