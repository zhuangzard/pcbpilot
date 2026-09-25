package pcbauto

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// Snapshot adapter for `pcbpilot pcb dump [--include-copper]` JSON (mil, y-up).
// Only the fields the engine needs are decoded; unknown fields are ignored so
// the evolving dump schema stays compatible.

type snapPad struct {
	Number   string  `json:"padNumber"`
	Net      string  `json:"net"`
	Layer    int     `json:"layer"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	W        float64 `json:"width"`
	H        float64 `json:"height"`
	Rotation float64 `json:"rotation"`
	Shape    any     `json:"shape"`
}

type snapBBox struct {
	MinX, MinY, MaxX, MaxY float64
}

type snapComp struct {
	ID         string    `json:"primitiveId"`
	Designator string    `json:"designator"`
	Device     string    `json:"device"`
	Layer      int       `json:"layer"`
	X          float64   `json:"x"`
	Y          float64   `json:"y"`
	Rotation   float64   `json:"rotation"`
	Locked     bool      `json:"locked"`
	BBox       *snapBBox `json:"bbox"`
	Pads       []snapPad `json:"pads"`
}

type snapFootprintHole struct {
	Owner    string       `json:"owner"`
	SourceID string       `json:"sourceId"`
	Shape    string       `json:"shape"`
	X        float64      `json:"x"`
	Y        float64      `json:"y"`
	Dia      float64      `json:"dia"`
	Points   [][2]float64 `json:"points"`
}

// DefaultSlotClearance is the copper → footprint NPTH/slot spacing when the
// snapshot's rules carry no Slot Region entry: 0.3 mm, what native DRC
// ("Slot Region to Track", Safe Spacing copperThickness1oz) demanded on the
// 2026-09-25 ESP32 E2E board.
const DefaultSlotClearance = 11.811

type snapshot struct {
	Components []snapComp `json:"components"`
	Outline    *struct {
		BBox   snapBBox     `json:"bbox"`
		Points [][2]float64 `json:"points"`
		Source string       `json:"source"`
	} `json:"outline"`
	CopperLayers int `json:"copperLayers"`
	Rules        *struct {
		ClearanceMil    float64 `json:"clearanceMil"`
		TrackWidthMil   float64 `json:"trackWidthMil"`
		TrackWidthMin   float64 `json:"trackWidthMinMil"`
		ViaDrillMil     float64 `json:"viaDrillMil"`
		ViaDiameterMil  float64 `json:"viaDiameterMil"`
		CopperToEdgeMil float64 `json:"copperToEdgeMil"`
		HoleToHoleMil   float64 `json:"holeToHoleMil"`
		SlotClearance   float64 `json:"slotClearanceMil"`
	} `json:"rules"`
	// FootprintHoles: NPTH/slot regions inside placed footprints (pcb dump
	// footprintHoles[], board coordinates). Not pads, no net.
	FootprintHoles []snapFootprintHole `json:"footprintHoles"`
	Copper         *struct {
		Regions []map[string]any `json:"regions"`
		Fills   []map[string]any `json:"fills"`
	} `json:"copper"`
}

// FromSnapshot builds a Board from `pcb dump` JSON.
func FromSnapshot(raw []byte) (*Board, error) {
	var s snapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	b := &Board{Rules: DefaultRules(), CopperLayers: s.CopperLayers}
	if s.Rules != nil {
		b.Rules.Clearance = s.Rules.ClearanceMil
		b.Rules.TrackWidth = s.Rules.TrackWidthMil
		b.Rules.MinTrack = s.Rules.TrackWidthMin
		b.Rules.ViaDrill = s.Rules.ViaDrillMil
		b.Rules.ViaDia = s.Rules.ViaDiameterMil
		b.Rules.EdgeClearance = s.Rules.CopperToEdgeMil
		if s.Rules.HoleToHoleMil > 0 {
			b.Rules.HoleGap = s.Rules.HoleToHoleMil
		}
	}
	if s.Outline != nil {
		if len(s.Outline.Points) >= 3 {
			for _, p := range s.Outline.Points {
				b.Outline = append(b.Outline, Point{p[0], p[1]})
			}
			// A polyline covering under half its bbox is an inner cutout, not
			// the board edge (same sanity rule as the dump consumer).
			bb := s.Outline.BBox
			if PolyArea(b.Outline) < 0.5*(bb.MaxX-bb.MinX)*(bb.MaxY-bb.MinY) {
				b.Outline = nil
			}
		}
		if len(b.Outline) < 3 && s.Outline.BBox.MaxX > s.Outline.BBox.MinX {
			bb := s.Outline.BBox
			b.Outline = Rect{bb.MinX, bb.MinY, bb.MaxX, bb.MaxY}.Corners()
		}
	}
	for _, c := range s.Components {
		if c.Designator == "" {
			continue
		}
		p := &Part{Ref: c.Designator, ID: c.ID, Device: c.Device, Pos: Point{c.X, c.Y},
			Rotation: normDeg(c.Rotation), Side: c.Layer, Fixed: c.Locked}
		if p.Side != LayerBottom {
			p.Side = LayerTop
		}
		sized := true
		for _, sp := range c.Pads {
			p.Pads = append(p.Pads, snapPadToPad(sp))
			if !padSized(sp) {
				sized = false
			}
		}
		if c.BBox != nil && c.BBox.MaxX > c.BBox.MinX && !sized {
			// Pads without a size (mounting hardware, SMT standoffs): the
			// rendered outline is the only extent we have, and for hardware it
			// stands for the screw head / washer / tool clearance — keep it
			// whole, as layout-lint does.
			p.SetBody(Rect{c.BBox.MinX, c.BBox.MinY, c.BBox.MaxX, c.BBox.MaxY})
		} else if c.BBox != nil && c.BBox.MaxX > c.BBox.MinX {
			// The rendered bbox includes silk; shrink toward the pads so the
			// courtyard is not ~40% oversized — by half the gap, but never more
			// than a silk margin per side: a pad-free end is body, not silk.
			// Halving it cut an ESP32-S3-WROOM-1's 300 mil antenna end to
			// 159 mil, so the "flush" module hung 3.5 mm off the board edge and
			// its antenna keep-out sat 140 mil short.
			bb := Rect{c.BBox.MinX, c.BBox.MinY, c.BBox.MaxX, c.BBox.MaxY}
			pads := EmptyRect()
			for _, pd := range p.Pads {
				pads = pads.Union(pd.Box.Bounds())
			}
			if !pads.Empty() {
				// gap > 0: silk/body beyond the pads (shrink ≤ margin);
				// gap < 0: a pad beyond the bbox (grow halfway, as before).
				in := func(gap float64) float64 { return math.Min(gap/2, silkMarginMil) }
				bb = Rect{bb.MinX + in(pads.MinX-bb.MinX), bb.MinY + in(pads.MinY-bb.MinY),
					bb.MaxX - in(bb.MaxX-pads.MaxX), bb.MaxY - in(bb.MaxY-pads.MaxY)}
			}
			p.SetBody(bb)
		}
		b.Parts = append(b.Parts, p)
	}
	if s.Copper != nil {
		for _, r := range s.Copper.Regions {
			k := regionKeepout(r)
			if k != nil {
				b.Keepouts = append(b.Keepouts, k)
			}
		}
		for _, f := range s.Copper.Fills {
			if num(f["layer"]) != LayerMulti {
				continue
			}
			// MULTI-layer fill = board cutout / mounting hole (pcb mount-holes).
			bb, ok := anyBBox(f["bbox"])
			if !ok {
				poly := sourcePoints(f["source"])
				if len(poly) < 3 {
					continue
				}
				bb = PolyBounds(poly)
			}
			b.Holes = append(b.Holes, &Hole{Name: str(f["primitiveId"]), C: bb.Center(), Dia: math.Min(bb.W(), bb.H())})
		}
	}
	slotClr := DefaultSlotClearance
	if s.Rules != nil && s.Rules.SlotClearance > 0 {
		slotClr = s.Rules.SlotClearance
	}
	for _, fh := range s.FootprintHoles {
		h := &Hole{Name: fh.Owner + ":" + fh.SourceID, Owner: fh.Owner, C: Point{fh.X, fh.Y}, Clr: slotClr}
		if b.partByRef(fh.Owner) == nil {
			h.Owner = "" // owner not on the board model: a fixed obstacle
		}
		switch {
		case fh.Shape == "circle" && fh.Dia > 0:
			h.Dia = fh.Dia
		case len(fh.Points) >= 3:
			for _, q := range fh.Points {
				h.Poly = append(h.Poly, Point{q[0], q[1]})
			}
		default:
			continue
		}
		b.Holes = append(b.Holes, h)
	}
	if err := b.Index(); err != nil {
		return nil, err
	}
	return b, nil
}

// partByRef is a pre-Index lookup.
func (b *Board) partByRef(ref string) *Part {
	for _, p := range b.Parts {
		if p.Ref == ref {
			return p
		}
	}
	return nil
}

// silkMarginMil caps how far the rendered bbox is shrunk toward the pads on
// each side (silk outline + line width).
const silkMarginMil = 20.0

// padSized reports whether the snapshot gives the pad a real size.
func padSized(sp snapPad) bool {
	if sp.W > 0 && sp.H > 0 {
		return true
	}
	if arr, ok := sp.Shape.([]any); ok && len(arr) >= 3 {
		return num(arr[1]) > 0 && num(arr[2]) > 0
	}
	return false
}

func snapPadToPad(sp snapPad) *Pad {
	pd := &Pad{Number: sp.Number, Net: sp.Net, Layer: sp.Layer}
	if pd.Layer != LayerBottom && pd.Layer != LayerMulti {
		pd.Layer = LayerTop
	}
	box := OrientedBox{C: Point{sp.X, sp.Y}, W: sp.W, H: sp.H}
	// Raw shape tuple: [RECT,w,h,r] / [ELLIPSE|OVAL,w,h] carries the true size
	// in the pad frame; width/height are already axis-aligned extents.
	if arr, ok := sp.Shape.([]any); ok && len(arr) >= 3 {
		kind := strings.ToUpper(str(arr[0]))
		w, h := num(arr[1]), num(arr[2])
		if w > 0 && h > 0 {
			box.W, box.H, box.Rot = w, h, normDeg(sp.Rotation)
			box.Round = kind == "ELLIPSE" || kind == "OVAL" || kind == "CIRCLE"
		}
	}
	if box.W <= 0 || box.H <= 0 {
		box.W, box.H = math.Max(box.W, 10), math.Max(box.H, 10)
	}
	pd.Box = box
	return pd
}

// regionKeepout converts a pcb.region.list record. EasyEDA rule types:
// 2 no-components, 5 no-wires, 6 no-fills, 7 no-pours, 8 no-inner-electrical.
func regionKeepout(r map[string]any) *Keepout {
	k := &Keepout{Name: str(r["name"]), ID: str(r["primitiveId"])}
	if rules, ok := r["ruleType"].([]any); ok {
		for _, v := range rules {
			switch int(num(v)) {
			case 2:
				k.NoParts = true
			case 5:
				k.NoCopper, k.NoVias = true, true
			}
		}
	}
	if !k.NoCopper && !k.NoParts {
		return nil
	}
	if l := int(num(r["layer"])); l != 0 && l != LayerMulti {
		k.Layers = []int{l}
	}
	k.Poly = sourcePoints(r["source"])
	if len(k.Poly) < 3 {
		bb, ok := anyBBox(r["bbox"])
		if !ok {
			return nil
		}
		k.Poly = bb.Corners()
	}
	return k
}

// sourcePoints reads the vertices of an EasyEDA polygon source
// [x,y,'L',x,y,…] (arcs contribute their end points; good enough for bounds).
func sourcePoints(v any) []Point {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	// Nested complex polygon: take the first contour.
	if len(arr) > 0 {
		if inner, ok := arr[0].([]any); ok {
			return sourcePoints(inner)
		}
	}
	var nums []float64
	var pts []Point
	for _, t := range arr {
		switch x := t.(type) {
		case float64:
			nums = append(nums, x)
		case string:
			if strings.EqualFold(x, "ARC") || strings.EqualFold(x, "CARC") {
				nums = append(nums, math.NaN()) // marker: next number is an angle
			}
		}
	}
	for i := 0; i < len(nums); i++ {
		if math.IsNaN(nums[i]) {
			i++ // skip the arc angle
			continue
		}
		if i+1 < len(nums) && !math.IsNaN(nums[i+1]) {
			pts = append(pts, Point{nums[i], nums[i+1]})
			i++
		}
	}
	return pts
}

func anyBBox(v any) (Rect, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return Rect{}, false
	}
	r := Rect{num(m["minX"]), num(m["minY"]), num(m["maxX"]), num(m["maxY"])}
	return r, r.MaxX > r.MinX && r.MaxY > r.MinY
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

// ExportPlacedSnapshot rewrites a `pcb dump` snapshot with the board's current
// part poses: component anchor/rotation, every pad centre/rotation (width and
// height swapped on quarter turns) and the rendered bbox moved rigidly with
// the part. All other fields are preserved, so any snapshot consumer
// (layout-score, layout-lint, route solve) can judge an engine placement
// exactly as it judges a real board.
func ExportPlacedSnapshot(raw []byte, b *Board) ([]byte, error) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	comps, _ := doc["components"].([]any)
	movers := map[string]func(Point) Point{}
	for _, ci := range comps {
		c, ok := ci.(map[string]any)
		if !ok {
			continue
		}
		p := b.Part(str(c["designator"]))
		if p == nil {
			continue
		}
		oldPos := Point{num(c["x"]), num(c["y"])}
		oldRot := normDeg(num(c["rotation"]))
		delta := normDeg(p.Rotation - oldRot)
		move := func(q Point) Point { return p.Pos.Add(q.Sub(oldPos).Rotate(delta)) }
		movers[p.Ref] = move
		quarter := int(math.Round(delta/90)) % 2
		c["x"], c["y"], c["rotation"] = round2(p.Pos.X), round2(p.Pos.Y), p.Rotation
		if bb, ok := anyBBox(c["bbox"]); ok {
			nb := EmptyRect()
			for _, k := range bb.Corners() {
				nb = nb.AddPoint(move(k))
			}
			c["bbox"] = map[string]any{"minX": round2(nb.MinX), "minY": round2(nb.MinY), "maxX": round2(nb.MaxX), "maxY": round2(nb.MaxY)}
		}
		pads, _ := c["pads"].([]any)
		for _, pi := range pads {
			pd, ok := pi.(map[string]any)
			if !ok {
				continue
			}
			np := move(Point{num(pd["x"]), num(pd["y"])})
			pd["x"], pd["y"] = round2(np.X), round2(np.Y)
			if _, has := pd["rotation"]; has || delta != 0 {
				pd["rotation"] = normDeg(num(pd["rotation"]) + delta)
			}
			if quarter == 1 {
				if w, ok := pd["width"]; ok {
					pd["width"], pd["height"] = pd["height"], w
				}
			}
		}
	}
	// Footprint NPTH/slot regions are part of their footprint: rigid with it.
	fhs, _ := doc["footprintHoles"].([]any)
	for _, fi := range fhs {
		fh, ok := fi.(map[string]any)
		if !ok {
			continue
		}
		move := movers[str(fh["owner"])]
		if move == nil {
			continue
		}
		c := move(Point{num(fh["x"]), num(fh["y"])})
		fh["x"], fh["y"] = round2(c.X), round2(c.Y)
		if pts, ok := fh["points"].([]any); ok {
			for i, pi := range pts {
				if xy, ok := pi.([]any); ok && len(xy) == 2 {
					q := move(Point{num(xy[0]), num(xy[1])})
					pts[i] = []any{round2(q.X), round2(q.Y)}
				}
			}
		}
	}
	if len(b.Outline) >= 3 {
		if o, ok := doc["outline"].(map[string]any); ok {
			bb := PolyBounds(b.Outline)
			pts := make([][2]float64, len(b.Outline))
			for i, q := range b.Outline {
				pts[i] = [2]float64{round2(q.X), round2(q.Y)}
			}
			o["points"] = pts
			o["bbox"] = map[string]any{"minX": bb.MinX, "minY": bb.MinY, "maxX": bb.MaxX, "maxY": bb.MaxY}
		}
	}
	// A moved board is no longer the captured board: drop routed copper so
	// nobody reads stale tracks against new pad positions.
	delete(doc, "copper")
	delete(doc, "semanticSha256")
	return json.MarshalIndent(doc, "", " ")
}
