package app

import (
	"fmt"
	"math"
	"sort"
)

// Hierarchical zones (2026-09-24, user design): a zone declared with
// embedIn:<parent> is solved first on its own - its parts wired and named -
// then frozen into one macro part. The parent zone places that macro as a
// single rotatable peripheral whose pins are the child's ports (the nets it
// shares with the parent), and the result is expanded back into the child's
// real placements, wires and flags. Output format is unchanged.

type schematicMacro struct {
	zone   SchematicZone
	layout *SchematicLayoutResult // child layout, core-normalized at (0,0)
	ports  map[string]bool        // nets shared with the parent
	part   SchematicLayoutComponent
	// child flags that name the ports (dropped on expansion; parent wires replace them)
	portFlags map[int]bool
	stems     []powerLayoutWire // port point -> macro pin, restored on expansion
}

func macroComponentID(zoneID string) string { return "macro:" + zoneID }

// buildSchematicMacro freezes a solved child layout into one part.
func buildSchematicMacro(z SchematicZone, layout *SchematicLayoutResult, ports map[string]bool) (*schematicMacro, error) {
	m := &schematicMacro{zone: z, layout: layout, ports: ports, portFlags: map[int]bool{}}
	var pins []powerLayoutPin
	seen := map[string]bool{}
	for i, f := range layout.Flags {
		if !ports[f.Net] {
			continue
		}
		if seen[f.Net] {
			return nil, fmt.Errorf("macro %s: port %s is named twice in the child layout", z.ID, f.Net)
		}
		seen[f.Net] = true
		m.portFlags[i] = true
		angle := map[string]float64{"right": 0, "up": 90, "left": 180, "down": 270}[f.Direction]
		pins = append(pins, powerLayoutPin{Number: fmt.Sprintf("P%d", len(pins)+1), Name: f.Net, Net: f.Net, X: f.PinX, Y: f.PinY, Rotation: &angle})
	}
	for net := range ports {
		if !seen[net] {
			return nil, fmt.Errorf("macro %s: port %s has no named exit in the child layout", z.ID, net)
		}
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Net < pins[j].Net })
	for i := range pins {
		pins[i].Number = fmt.Sprintf("P%d", i+1)
	}
	env := layoutBBox{math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)}
	grow := func(b layoutBBox) {
		env.MinX, env.MinY = math.Min(env.MinX, b.MinX), math.Min(env.MinY, b.MinY)
		env.MaxX, env.MaxY = math.Max(env.MaxX, b.MaxX), math.Max(env.MaxY, b.MaxY)
	}
	// Designators are not part of the rigid body: they are re-laid out when a
	// part rotates. The macro carries every child designator as its own text
	// boxes, with the exact pose for each macro rotation.
	texts := map[string][]layoutBBox{}
	for q := 0; q < 4; q++ {
		key := fmt.Sprint(q * 90)
		for _, c := range layout.Placements {
			x, y := c.X, c.Y
			for i := 0; i < q; i++ {
				x, y = -y, x
			}
			r := plRotate(c, q)
			r = plTranslate(r, x-r.X, y-r.Y)
			texts[key] = append(texts[key], r.TextBBoxes...)
		}
	}
	for _, c := range layout.Placements {
		grow(c.BBox)
		for _, q := range c.Pins {
			grow(layoutBBox{q.X - .5, q.Y - .5, q.X + .5, q.Y + .5})
		}
	}
	for _, w := range layout.Wires {
		for _, pt := range w.Points {
			grow(layoutBBox{pt[0] - .5, pt[1] - .5, pt[0] + .5, pt[1] + .5})
		}
	}
	for i, f := range layout.Flags {
		if m.portFlags[i] {
			continue
		}
		x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
		grow(predictedMarkerBBox(x, y, f.Kind, f.Direction, f.Net))
		grow(layoutBBox{math.Min(f.PinX, x) - .5, math.Min(f.PinY, y) - .5, math.Max(f.PinX, x) + .5, math.Max(f.PinY, y) + .5})
	}
	// Each port leaves the envelope through a 5-raw stem, like a real pin: the
	// macro pin sits outside the body and expansion restores the stem wire. A
	// port with content beyond it on its exit side is not on the edge: refuse.
	// Obstacles a port stem must not cross: bodies, designators, other nets'
	// wires and the non-port flags (with their leads).
	var blocks []struct {
		b   layoutBBox
		net string
	}
	type segBlock struct {
		a, b [2]float64
		net  string
	}
	var segs []segBlock
	for _, c := range layout.Placements {
		blocks = append(blocks, struct {
			b   layoutBBox
			net string
		}{c.BBox, ""})
	}
	for q := 0; q < 4; q++ {
		for _, t := range texts[fmt.Sprint(q*90)] {
			ax, ay, bx, by := t.MinX, t.MinY, t.MaxX, t.MaxY
			for i := 0; i < (4-q)%4; i++ {
				ax, ay = -ay, ax
				bx, by = -by, bx
			}
			blocks = append(blocks, struct {
				b   layoutBBox
				net string
			}{layoutBBox{math.Min(ax, bx), math.Min(ay, by), math.Max(ax, bx), math.Max(ay, by)}, ""})
		}
	}
	for _, w := range layout.Wires {
		for k := 1; k < len(w.Points); k++ {
			segs = append(segs, segBlock{w.Points[k-1], w.Points[k], w.Net})
		}
	}
	for i, f := range layout.Flags {
		if m.portFlags[i] {
			continue
		}
		x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
		blocks = append(blocks, struct {
			b   layoutBBox
			net string
		}{predictedMarkerBBox(x, y, f.Kind, f.Direction, f.Net), ""})
	}
	// Each port leaves the envelope through the shortest clear straight stem
	// from any point of its net in the child (a pin, or a wire point on the
	// 5-raw grid), in any direction, to 5 raw beyond the envelope. The stem may
	// touch only its own net; expansion restores it as a wire.
	clear := func(net string, stem layoutBBox) bool {
		for _, blk := range blocks {
			if stem.MinX < blk.b.MaxX && blk.b.MinX < stem.MaxX && stem.MinY < blk.b.MaxY && blk.b.MinY < stem.MaxY {
				return false
			}
		}
		// Other nets' wires: a perpendicular crossing away from both segments'
		// ends is a legal unconnected crossing; touching/overlapping is not.
		vertical := stem.MaxX-stem.MinX < stem.MaxY-stem.MinY
		for _, sg := range segs {
			if sg.net == net {
				continue
			}
			lo := layoutBBox{math.Min(sg.a[0], sg.b[0]) - .5, math.Min(sg.a[1], sg.b[1]) - .5, math.Max(sg.a[0], sg.b[0]) + .5, math.Max(sg.a[1], sg.b[1]) + .5}
			if !(stem.MinX < lo.MaxX && lo.MinX < stem.MaxX && stem.MinY < lo.MaxY && lo.MinY < stem.MaxY) {
				continue
			}
			segVertical := sg.a[0] == sg.b[0]
			if segVertical == vertical {
				return false // collinear overlap
			}
			// crossing point
			cx, cy := sg.a[0], (stem.MinY+stem.MaxY)/2
			if vertical {
				cx, cy = (stem.MinX+stem.MaxX)/2, sg.a[1]
			}
			atEnd := func(p [2]float64) bool { return math.Abs(p[0]-cx) < 1 && math.Abs(p[1]-cy) < 1 }
			if atEnd(sg.a) || atEnd(sg.b) {
				return false
			}
			if vertical && (cy <= stem.MinY+1 || cy >= stem.MaxY-1) || !vertical && (cx <= stem.MinX+1 || cx >= stem.MaxX-1) {
				return false
			}
		}
		return true
	}
	base := env
	type exit struct {
		stem [2][2]float64
		side string
		len  float64
	}
	// bestExit: shortest clear straight exit of one port, optionally on a
	// fixed side ("" = any side).
	bestExit := func(net, only string) (exit, bool) {
		var starts [][2]float64
		pinSide := map[[2]float64]string{} // a pin may only be left along its outward axis
		for _, c := range layout.Placements {
			for _, q := range c.Pins {
				if q.Net == net {
					pt := [2]float64{q.X, q.Y}
					starts = append(starts, pt)
					if side, err := libPinSide(q, c.BBox); err == nil {
						pinSide[pt] = side
					}
				}
			}
		}
		for _, w := range layout.Wires {
			if w.Net != net {
				continue
			}
			for k := 1; k < len(w.Points); k++ {
				a, b := w.Points[k-1], w.Points[k]
				n := int(math.Max(math.Abs(b[0]-a[0]), math.Abs(b[1]-a[1])) / 5)
				for j := 0; j <= n; j++ {
					t := float64(j) / math.Max(1, float64(n))
					starts = append(starts, [2]float64{a[0] + (b[0]-a[0])*t, a[1] + (b[1]-a[1])*t})
				}
			}
		}
		best := exit{len: math.Inf(1)}
		for _, st := range starts {
			for _, side := range []string{"left", "right", "up", "down"} {
				if only != "" && side != only {
					continue
				}
				if ps, isPin := pinSide[st]; isPin && ps != side {
					continue
				}
				var length float64
				switch side {
				case "left":
					length = st[0] - base.MinX + 5
				case "right":
					length = base.MaxX - st[0] + 5
				case "up":
					length = base.MaxY - st[1] + 5
				default:
					length = st[1] - base.MinY + 5
				}
				length = math.Max(5, math.Ceil(length/5)*5)
				if length >= best.len {
					continue
				}
				ex, ey := endpointFor(st[0], st[1], length, side)
				box := layoutBBox{math.Min(st[0], ex) - .5, math.Min(st[1], ey) - .5, math.Max(st[0], ex) + .5, math.Max(st[1], ey) + .5}
				if clear(net, box) {
					best = exit{[2][2]float64{st, {ex, ey}}, side, length}
				}
			}
		}
		return best, !math.IsInf(best.len, 1)
	}
	// Prefer one common exit side for every port (a crystal's two ports on one
	// side, in order, like a hand-drawn oscillator); else each its own best.
	chosen := make([]exit, len(pins))
	common, commonLen := "", math.Inf(1)
	for _, side := range []string{"left", "right", "up", "down"} {
		total, ok := 0.0, true
		for _, p := range pins {
			e, found := bestExit(p.Net, side)
			if !found {
				ok = false
				break
			}
			total += e.len
		}
		if ok && total < commonLen {
			common, commonLen = side, total
		}

	}
	for i, p := range pins {
		e, found := bestExit(p.Net, common)
		if !found {
			return nil, fmt.Errorf("macro %s port %s has no clear straight exit from its net to the envelope", z.ID, p.Net)
		}
		chosen[i] = e
	}
	for i, e := range chosen {
		angle := map[string]float64{"right": 0, "up": 90, "left": 180, "down": 270}[e.side]
		m.stems = append(m.stems, powerLayoutWire{Net: pins[i].Net, Points: [][2]float64{e.stem[0], e.stem[1]}})
		pins[i].X, pins[i].Y, pins[i].Rotation = e.stem[1][0], e.stem[1][1], &angle
		grow(layoutBBox{math.Min(e.stem[0][0], e.stem[1][0]), math.Min(e.stem[0][1], e.stem[1][1]), math.Max(e.stem[0][0], e.stem[1][0]), math.Max(e.stem[0][1], e.stem[1][1])})
	}
	// The stems are part of the macro body except their last 5 raw (the pin).
	for _, p := range pins {
		side, _ := libPinSide(p, env)
		switch side {
		case "left":
			env.MinX = math.Max(env.MinX, p.X+5)
		case "right":
			env.MaxX = math.Min(env.MaxX, p.X-5)
		case "up":
			env.MaxY = math.Min(env.MaxY, p.Y-5)
		default:
			env.MinY = math.Max(env.MinY, p.Y+5)
		}
	}
	m.part = SchematicLayoutComponent{ID: macroComponentID(z.ID), AllowedRotations: []float64{0, 90, 180, 270},
		Measurement: SchematicPlacement{Designator: z.ID, X: 0, Y: 0, BBox: env, TextBBoxes: texts["0"], TextBBoxesByRotation: texts, Pins: pins}}
	return m, nil
}

// expand replaces the macro placement in a solved parent layout with the
// child's real placements, wires and non-port flags, rigidly transformed.
func (m *schematicMacro) expand(parent *SchematicLayoutResult) error {
	idx := -1
	for i, c := range parent.Placements {
		if parent.ComponentIDs[c.Designator] == macroComponentID(m.zone.ID) {
			idx = i
		}
	}
	if idx < 0 {
		return fmt.Errorf("macro %s missing from the parent layout", m.zone.ID)
	}
	placed := parent.Placements[idx]
	quarters := int(math.Mod(placed.Rotation+360, 360) / 90)
	rot := func(x, y float64) (float64, float64) {
		for i := 0; i < quarters; i++ {
			x, y = -y, x
		}
		return x + placed.X, y + placed.Y
	}
	turn := func(dir string) string {
		order := []string{"right", "up", "left", "down"}
		for i, d := range order {
			if d == dir {
				return order[(i+quarters)%4]
			}
		}
		return dir
	}
	var parts []powerLayoutPlacement
	for _, c := range m.layout.Placements {
		x, y := rot(c.X, c.Y)
		r := plRotate(c, quarters)
		parts = append(parts, plTranslate(r, x-r.X, y-r.Y))
	}
	placements := append(append(append([]powerLayoutPlacement(nil), parent.Placements[:idx]...), parts...), parent.Placements[idx+1:]...)
	for _, w := range append(append([]powerLayoutWire(nil), m.layout.Wires...), m.stems...) {
		nw := powerLayoutWire{Net: w.Net}
		for _, pt := range w.Points {
			x, y := rot(pt[0], pt[1])
			nw.Points = append(nw.Points, [2]float64{x, y})
		}
		parent.Wires = append(parent.Wires, nw)
	}
	for i, f := range m.layout.Flags {
		if m.portFlags[i] {
			continue
		}
		f.PinX, f.PinY = rot(f.PinX, f.PinY)
		f.Direction = turn(f.Direction)
		if f.Anchor != nil {
			a := *f.Anchor
			if a.X != nil && a.Y != nil {
				x, y := rot(*a.X, *a.Y)
				a.X, a.Y = &x, &y
			}
			f.Anchor = &a
		}
		parent.Flags = append(parent.Flags, f)
	}
	parent.Placements = placements
	delete(parent.ComponentIDs, m.zone.ID)
	for ref, id := range m.layout.ComponentIDs {
		parent.ComponentIDs[ref] = id
	}
	if parent.PinStates == nil {
		parent.PinStates = map[string]map[string]string{}
	}
	delete(parent.PinStates, macroComponentID(m.zone.ID))
	for id, st := range m.layout.PinStates {
		parent.PinStates[id] = st
	}
	p := powerLayoutPlan{Placements: parent.Placements, Wires: parent.Wires, Flags: parent.Flags}
	if err := validateLibGeometry(&p); err != nil {
		return fmt.Errorf("macro %s expansion: %w", m.zone.ID, err)
	}
	return validateSchCompositionNets(&p)
}
