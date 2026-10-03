package schaes

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Source formats accepted by Parse (auto-detected):
//
//	components-list  `pcbpilot sch list --include-pins --include-bbox --include-wires`
//	                 (schematic.components.list result, optionally wrapped in
//	                 {"result":…}); optional extra keys merged by the caller:
//	                 texts (sch text list), rectangles (rectangles.list),
//	                 designators (sch designator-geometry), buses (sch bus list).
//	layout           `sch layout-plan` output {placements,wires,flags}
//	lib-layout       `sch lib-layout` / compose source {modules:[{placements,wires,flags}]}
//	canonical        1.4 connectivity snapshot {components[].placement,connections,nets}
//	                 (no wires/markers: wiring metrics are skipped, not scored)
//
// A zones packet (layout-plan --zones / layout-render input) holds zone-local
// geometry: use ParseZones and score each zone on its own.

// Parse normalizes one page snapshot.
func Parse(raw []byte) (*Snapshot, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("schematic snapshot: %w", err)
	}
	if r, ok := top["result"]; ok && top["components"] == nil && top["placements"] == nil {
		var inner map[string]json.RawMessage
		if json.Unmarshal(r, &inner) == nil {
			top = inner
		}
	}
	switch {
	case top["parts"] != nil && top["source"] != nil:
		// native normalized snapshot (this package's own JSON, e.g. fixtures)
		var s Snapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, fmt.Errorf("snapshot: %w", err)
		}
		for i := range s.Parts {
			s.Parts[i].HasBox = s.Parts[i].Box.Valid()
		}
		return &s, nil
	case top["placements"] != nil:
		return parseLayout(top, "layout")
	case top["modules"] != nil:
		var mods []map[string]json.RawMessage
		if err := json.Unmarshal(top["modules"], &mods); err != nil {
			return nil, fmt.Errorf("modules: %w", err)
		}
		s := &Snapshot{Source: "lib-layout"}
		for _, m := range mods {
			one, err := parseLayout(m, "lib-layout")
			if err != nil {
				return nil, err
			}
			mergeSnapshot(s, one)
			if f := moduleFrame(m, one); f != nil {
				s.Frames = append(s.Frames, *f)
				s.HasFrames = true
			}
		}
		// top-level "sheet" is page coordinates while module geometry is
		// module-local (core at 0,0): not comparable, so it is not used.
		s.Notes = append(s.Notes, "lib-layout modules are module-local (core at 0,0): sheet centring not measured")
		return s, nil
	case top["zones"] != nil:
		return nil, fmt.Errorf("zones packet holds zone-local geometry; score one zone at a time (--zone ID)")
	case top["components"] != nil:
		var comps []map[string]json.RawMessage
		if err := json.Unmarshal(top["components"], &comps); err != nil {
			return nil, fmt.Errorf("components: %w", err)
		}
		if len(comps) > 0 && comps[0]["placement"] != nil {
			return parseCanonical(top, comps)
		}
		return parseComponentsList(top, comps)
	}
	return nil, fmt.Errorf("unrecognized schematic snapshot: need components (sch list / canonical), placements (layout-plan) or modules (lib-layout)")
}

// ZoneSnapshot is one zone of a zones packet.
type ZoneSnapshot struct {
	ID       string
	Title    string
	Snapshot *Snapshot
}

// ParseZones splits a zones packet into per-zone snapshots (local coords).
func ParseZones(raw []byte) ([]ZoneSnapshot, error) {
	var top struct {
		Zones []struct {
			ID     string                     `json:"id"`
			Title  string                     `json:"title"`
			Layout map[string]json.RawMessage `json:"layout"`
			Frame  *struct {
				Title string `json:"title"`
				Rect  Box    `json:"rect"`
			} `json:"frame"`
		} `json:"zones"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	if len(top.Zones) == 0 {
		return nil, fmt.Errorf("no zones")
	}
	var out []ZoneSnapshot
	for _, z := range top.Zones {
		if z.Layout == nil {
			continue
		}
		s, err := parseLayout(z.Layout, "layout-zone")
		if err != nil {
			return nil, fmt.Errorf("zone %s: %w", z.ID, err)
		}
		if z.Frame != nil && z.Frame.Rect.Valid() {
			s.Frames = append(s.Frames, Frame{ID: z.ID, Title: z.Frame.Title, Box: z.Frame.Rect})
			s.HasFrames = true
		}
		out = append(out, ZoneSnapshot{ID: z.ID, Title: z.Title, Snapshot: s})
	}
	return out, nil
}

func mergeSnapshot(dst, src *Snapshot) {
	dst.Parts = append(dst.Parts, src.Parts...)
	dst.Wires = append(dst.Wires, src.Wires...)
	dst.Markers = append(dst.Markers, src.Markers...)
	dst.Texts = append(dst.Texts, src.Texts...)
	dst.Buses = append(dst.Buses, src.Buses...)
	dst.HasBuses = dst.HasBuses || src.HasBuses
	dst.HasWires = dst.HasWires || src.HasWires
	dst.HasMarkers = dst.HasMarkers || src.HasMarkers
	dst.HasPins = dst.HasPins || src.HasPins
	dst.HasTexts = dst.HasTexts || src.HasTexts
	dst.Notes = append(dst.Notes, src.Notes...)
}

// moduleFrame derives a module frame from an explicit "frame"/"bounds" key.
func moduleFrame(m map[string]json.RawMessage, s *Snapshot) *Frame {
	var title, id string
	_ = json.Unmarshal(m["title"], &title)
	_ = json.Unmarshal(m["id"], &id)
	if fr, ok := m["frame"]; ok {
		var f struct {
			Title string `json:"title"`
			Rect  Box    `json:"rect"`
		}
		if json.Unmarshal(fr, &f) == nil && f.Rect.Valid() {
			return &Frame{ID: id, Title: f.Title, Box: f.Rect}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// layout (layout-plan / lib-layout module)

type layoutPin struct {
	Number   string   `json:"number"`
	Name     string   `json:"name"`
	Net      string   `json:"net"`
	X        float64  `json:"x"`
	Y        float64  `json:"y"`
	Rotation *float64 `json:"rotation"`
}

type layoutPlacement struct {
	PrimitiveID string      `json:"primitiveId"`
	Designator  string      `json:"designator"`
	X           float64     `json:"x"`
	Y           float64     `json:"y"`
	Rotation    float64     `json:"rotation"`
	BBox        Box         `json:"bbox"`
	TextBBoxes  []Box       `json:"textBboxes"`
	Pins        []layoutPin `json:"pins"`
}

type layoutFlag struct {
	Net       string  `json:"net"`
	Kind      string  `json:"kind"`
	PinX      float64 `json:"pinX"`
	PinY      float64 `json:"pinY"`
	Direction string  `json:"direction"`
	Offset    float64 `json:"offset"`
}

func parseLayout(m map[string]json.RawMessage, source string) (*Snapshot, error) {
	var pls []layoutPlacement
	if err := json.Unmarshal(m["placements"], &pls); err != nil {
		return nil, fmt.Errorf("placements: %w", err)
	}
	var wires []struct {
		Net    string       `json:"net"`
		Points [][2]float64 `json:"points"`
	}
	if r, ok := m["wires"]; ok {
		if err := json.Unmarshal(r, &wires); err != nil {
			return nil, fmt.Errorf("wires: %w", err)
		}
	}
	var flags []layoutFlag
	if r, ok := m["flags"]; ok {
		if err := json.Unmarshal(r, &flags); err != nil {
			return nil, fmt.Errorf("flags: %w", err)
		}
	}
	var compIDs map[string]string
	_ = json.Unmarshal(m["componentIds"], &compIDs)
	var pinStates map[string]map[string]string
	_ = json.Unmarshal(m["pinStates"], &pinStates)
	s := &Snapshot{Source: source, HasWires: m["wires"] != nil, HasMarkers: m["flags"] != nil}
	for _, p := range pls {
		part := Part{ID: p.PrimitiveID, Ref: p.Designator, X: p.X, Y: p.Y, Rot: p.Rotation, Box: p.BBox, HasBox: p.BBox.Valid()}
		states := pinStates[compIDs[p.Designator]]
		for _, q := range p.Pins {
			pin := Pin{Number: q.Number, Name: q.Name, Net: q.Net, X: q.X, Y: q.Y, NC: states[q.Number] == "nc"}
			if q.Rotation != nil {
				pin.Dir = dirOfAngle(*q.Rotation)
			}
			if pin.Dir == "" {
				pin.Dir = dirFromBox(Pt{q.X, q.Y}, p.BBox)
			}
			part.Pins = append(part.Pins, pin)
		}
		if len(part.Pins) > 0 {
			s.HasPins = true
		}
		s.Parts = append(s.Parts, part)
		if len(p.TextBBoxes) > 0 {
			s.HasTexts = true
			for _, b := range p.TextBBoxes {
				if b.Valid() {
					s.Texts = append(s.Texts, Text{Kind: "designator", Content: p.Designator, Owner: p.Designator, Box: b})
				}
			}
		}
	}
	for _, w := range wires {
		var pts []Pt
		for _, q := range w.Points {
			pts = append(pts, Pt{q[0], q[1]})
		}
		s.Wires = append(s.Wires, Wire{Net: w.Net, Pts: pts})
	}
	for _, f := range flags {
		kind := normalizeKind(f.Kind, f.Net)
		v := dirVec[f.Direction]
		a := Pt{f.PinX + v.X*f.Offset, f.PinY + v.Y*f.Offset}
		if f.Offset > 0 {
			s.Wires = append(s.Wires, Wire{Net: f.Net, Pts: []Pt{{f.PinX, f.PinY}, a}, Implicit: true})
		}
		s.Markers = append(s.Markers, Marker{Kind: kind, Net: f.Net, Anchor: a, Dir: f.Direction,
			Box: predictMarkerBox(kind, f.Net, a, f.Direction), Estimated: true})
	}
	parseBuses(m, s)
	return s, nil
}

func normalizeKind(k, net string) string {
	switch strings.ToLower(strings.ReplaceAll(k, "_", "")) {
	case "ground", "gnd", "analogground", "protectground":
		return KindGround
	case "power", "vcc":
		return KindPower
	case "netlabel", "label":
		return KindNetLabel
	case "netport", "port", "in", "out", "bi":
		return KindNetPort
	case "netflag", "flag":
		if IsGroundNet(net) {
			return KindGround
		}
		return KindPower
	}
	return KindNetPort
}

// ---------------------------------------------------------------------------
// components.list result

func parseComponentsList(top map[string]json.RawMessage, comps []map[string]json.RawMessage) (*Snapshot, error) {
	s := &Snapshot{Source: "components-list", HasMarkers: true}
	f := func(m map[string]json.RawMessage, k string) (float64, bool) {
		var v float64
		if r, ok := m[k]; ok && json.Unmarshal(r, &v) == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			return v, true
		}
		return 0, false
	}
	str := func(m map[string]json.RawMessage, keys ...string) string {
		for _, k := range keys {
			var v string
			if r, ok := m[k]; ok && json.Unmarshal(r, &v) == nil && v != "" {
				return v
			}
		}
		return ""
	}
	for _, c := range comps {
		typ := str(c, "componentType")
		x, _ := f(c, "x")
		y, _ := f(c, "y")
		rot, _ := f(c, "rotation")
		box, hasBox := decodeBox(c["bbox"])
		id := str(c, "primitiveId")
		net := str(c, "net")
		switch typ {
		case "sheet":
			if hasBox {
				b := box
				s.Sheet = &b
			}
		case "netflag", "netport", "netlabel":
			kind := KindNetPort
			family := "port"
			switch typ {
			case "netflag":
				kind = normalizeKind("netflag", net)
				family = kind
			case "netlabel":
				kind = KindNetLabel
			}
			dir := ""
			if typ != "netlabel" {
				dir = flagDirOf(family, rot)
			}
			mk := Marker{ID: id, Kind: kind, Net: net, Anchor: Pt{x, y}, Dir: dir, PortIO: strings.ToUpper(str(c, "portIO", "direction"))}
			pred := predictMarkerBox(kind, net, mk.Anchor, dir)
			if hasBox {
				mk.Box = box.Union(pred) // symbol bbox ∪ predicted text band
			} else {
				mk.Box, mk.Estimated = pred, true
			}
			s.Markers = append(s.Markers, mk)
		case "part", "":
			p := Part{ID: id, Ref: str(c, "designator"), X: x, Y: y, Rot: rot, Box: box, HasBox: hasBox}
			var pins []map[string]json.RawMessage
			if r, ok := c["pins"]; ok && json.Unmarshal(r, &pins) == nil {
				s.HasPins = true
				for _, q := range pins {
					px, _ := f(q, "x")
					py, _ := f(q, "y")
					pin := Pin{Number: str(q, "pinNumber", "number"), Name: str(q, "pinName", "name"), Net: str(q, "net"), X: px, Y: py}
					var nc bool
					_ = json.Unmarshal(q["noConnected"], &nc)
					pin.NC = nc
					if r, ok := f(q, "rotation"); ok {
						pin.Dir = dirOfAngle(r)
					}
					if pin.Dir == "" {
						pin.Dir = dirFromBox(Pt{px, py}, box)
					}
					p.Pins = append(p.Pins, pin)
				}
			}
			s.Parts = append(s.Parts, p)
		}
	}
	if r, ok := top["wires"]; ok {
		var ws []map[string]json.RawMessage
		if err := json.Unmarshal(r, &ws); err != nil {
			return nil, fmt.Errorf("wires: %w", err)
		}
		s.HasWires = true
		for _, w := range ws {
			if _, has := w["x0"]; has {
				x0, _ := f(w, "x0")
				y0, _ := f(w, "y0")
				x1, _ := f(w, "x1")
				y1, _ := f(w, "y1")
				s.Wires = append(s.Wires, Wire{ID: str(w, "primitiveId"), Net: str(w, "net"), Pts: []Pt{{x0, y0}, {x1, y1}}})
				continue
			}
			var pts [][2]float64
			if json.Unmarshal(w["points"], &pts) == nil && len(pts) >= 2 {
				var pp []Pt
				for _, q := range pts {
					pp = append(pp, Pt{q[0], q[1]})
				}
				s.Wires = append(s.Wires, Wire{ID: str(w, "primitiveId"), Net: str(w, "net"), Pts: pp})
			}
		}
	} else {
		var avail bool
		_ = json.Unmarshal(top["wiresAvailable"], &avail)
		s.HasWires = avail
	}
	parseExtras(top, s)
	return s, nil
}

// parseExtras merges optional texts / rectangles / designators / buses.
func parseExtras(top map[string]json.RawMessage, s *Snapshot) {
	if r, ok := top["texts"]; ok {
		var ts []struct {
			Content  string  `json:"content"`
			X        float64 `json:"x"`
			Y        float64 `json:"y"`
			FontSize float64 `json:"fontSize"`
			BBox     *Box    `json:"bbox"`
		}
		if json.Unmarshal(r, &ts) == nil {
			s.HasTexts = true
			for _, t := range ts {
				tx := Text{Kind: "free", Content: t.Content}
				if t.BBox != nil && t.BBox.Valid() {
					tx.Box = *t.BBox
				} else {
					w, h := noteSize(t.Content, t.FontSize)
					tx.Box, tx.Estimated = Box{t.X, t.Y - h, t.X + w, t.Y}, true
				}
				s.Texts = append(s.Texts, tx)
			}
		}
	}
	if r, ok := top["designators"]; ok {
		var ds []struct {
			ParentID string `json:"parentId"`
			Value    string `json:"value"`
			BBox     Box    `json:"bbox"`
		}
		if json.Unmarshal(r, &ds) == nil {
			s.HasTexts = true
			owner := map[string]string{}
			for _, p := range s.Parts {
				owner[p.ID] = p.Ref
			}
			for _, d := range ds {
				if d.BBox.Valid() {
					o := owner[d.ParentID]
					if o == "" {
						o = d.Value
					}
					s.Texts = append(s.Texts, Text{Kind: "designator", Content: d.Value, Owner: o, Box: d.BBox})
				}
			}
		}
	}
	if r, ok := top["rectangles"]; ok {
		var rs []struct {
			PrimitiveID string  `json:"primitiveId"`
			X           float64 `json:"x"`
			Y           float64 `json:"y"`
			Width       float64 `json:"width"`
			Height      float64 `json:"height"`
		}
		if json.Unmarshal(r, &rs) == nil {
			s.HasFrames = true
			for _, q := range rs {
				top := math.Abs(q.Y) // 3.2.149 mirrors TopLeftY about y=0 (schFrameRectTopY)
				b := Box{q.X, top - q.Height, q.X + q.Width, top}
				if b.Valid() {
					s.Frames = append(s.Frames, Frame{ID: q.PrimitiveID, Box: b})
				}
			}
		}
	}
	if r, ok := top["frames"]; ok {
		var fs []Frame
		if json.Unmarshal(r, &fs) == nil {
			s.HasFrames = true
			s.Frames = append(s.Frames, fs...)
		}
	}
	parseBuses(top, s)
}

// parseBuses reads a "buses" array in the official encoding (sch bus list,
// layout plans: primitiveId, busName, line).
func parseBuses(top map[string]json.RawMessage, s *Snapshot) {
	if r, ok := top["buses"]; ok {
		var bs []struct {
			PrimitiveID string          `json:"primitiveId"`
			BusName     string          `json:"busName"`
			Line        json.RawMessage `json:"line"`
		}
		if json.Unmarshal(r, &bs) == nil {
			s.HasBuses = true
			for _, b := range bs {
				s.Buses = append(s.Buses, Bus{ID: b.PrimitiveID, Name: b.BusName, Pts: DecodeBusLine(b.Line)})
			}
		}
	}
}

// DecodeBusLine reads the official bus line encoding: either one flat
// polyline [x1,y1,x2,y2,…] or several [[x1,y1,…],[…]].
func DecodeBusLine(raw json.RawMessage) [][]Pt {
	var flat []float64
	if json.Unmarshal(raw, &flat) == nil {
		return [][]Pt{pairs(flat)}
	}
	var multi [][]float64
	if json.Unmarshal(raw, &multi) == nil {
		var out [][]Pt
		for _, l := range multi {
			if p := pairs(l); len(p) >= 2 {
				out = append(out, p)
			}
		}
		return out
	}
	return nil
}

func pairs(v []float64) []Pt {
	var out []Pt
	for i := 0; i+1 < len(v); i += 2 {
		out = append(out, Pt{v[i], v[i+1]})
	}
	return out
}

// noteSize mirrors internal/app noteSizeOf (CJK full width, 0.55 em latin,
// 1.3 line height; default font size 7).
func noteSize(content string, fontSize float64) (w, h float64) {
	if fontSize <= 0 {
		fontSize = 7
	}
	lines := strings.Split(content, "\n")
	for _, ln := range lines {
		lw := 0.0
		for _, r := range ln {
			if r > 0x2E80 {
				lw += fontSize
			} else {
				lw += fontSize * 0.55
			}
		}
		w = math.Max(w, lw)
	}
	return w, float64(len(lines)) * fontSize * 1.3
}

func decodeBox(raw json.RawMessage) (Box, bool) {
	if raw == nil {
		return Box{}, false
	}
	var b Box
	if json.Unmarshal(raw, &b) == nil && b.Valid() {
		return b, true
	}
	var arr []float64 // python lint fixtures: [minX,minY,maxX,maxY]
	if json.Unmarshal(raw, &arr) == nil && len(arr) == 4 {
		b = Box{arr[0], arr[1], arr[2], arr[3]}
		return b, b.Valid()
	}
	return Box{}, false
}

// ---------------------------------------------------------------------------
// canonical 1.4 connectivity snapshot

func parseCanonical(top map[string]json.RawMessage, comps []map[string]json.RawMessage) (*Snapshot, error) {
	var nets []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal(top["nets"], &nets)
	netName := map[string]string{}
	for _, n := range nets {
		netName[n.ID] = n.Name
	}
	var conns []struct {
		ComponentID string `json:"componentId"`
		PinNumber   string `json:"pinNumber"`
		NetID       string `json:"netId"`
	}
	_ = json.Unmarshal(top["connections"], &conns)
	pinNet := map[string]string{}
	for _, c := range conns {
		pinNet[c.ComponentID+"\x00"+c.PinNumber] = netName[c.NetID]
	}
	s := &Snapshot{Source: "canonical", HasPins: true}
	s.Notes = append(s.Notes, "canonical snapshot carries no wires/markers: wiring and label metrics are skipped (not measured)")
	for _, c := range comps {
		var cc struct {
			ID   string `json:"id"`
			Ref  string `json:"ref"`
			Pins []struct {
				Number      string   `json:"number"`
				Name        string   `json:"name"`
				X           *float64 `json:"x"`
				Y           *float64 `json:"y"`
				NoConnected bool     `json:"noConnected"`
			} `json:"pins"`
			Placement *struct {
				X        float64 `json:"x"`
				Y        float64 `json:"y"`
				Rotation float64 `json:"rotation"`
				BBox     *Box    `json:"bbox"`
			} `json:"placement"`
		}
		raw, _ := json.Marshal(c)
		if err := json.Unmarshal(raw, &cc); err != nil || cc.Placement == nil {
			continue
		}
		p := Part{ID: cc.ID, Ref: cc.Ref, X: cc.Placement.X, Y: cc.Placement.Y, Rot: cc.Placement.Rotation}
		if cc.Placement.BBox != nil && cc.Placement.BBox.Valid() {
			p.Box, p.HasBox = *cc.Placement.BBox, true
		}
		for _, q := range cc.Pins {
			if q.X == nil || q.Y == nil {
				continue
			}
			pin := Pin{Number: q.Number, Name: q.Name, Net: pinNet[cc.ID+"\x00"+q.Number], X: *q.X, Y: *q.Y, NC: q.NoConnected}
			pin.Dir = dirFromBox(Pt{pin.X, pin.Y}, p.Box)
			p.Pins = append(p.Pins, pin)
		}
		s.Parts = append(s.Parts, p)
	}
	sort.SliceStable(s.Parts, func(i, j int) bool { return s.Parts[i].Ref < s.Parts[j].Ref })
	return s, nil
}
