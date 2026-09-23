package app

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// A separate inventory prevents non-designator attributes (Name, Value, MPN,
// Description, etc.) from becoming collision obstacles by accident.
type schDesignatorGeometry struct {
	ID       string      `json:"id"`
	ParentID string      `json:"parentId"`
	Key      string      `json:"key"`
	Value    string      `json:"value"`
	Visible  *bool       `json:"visible"`
	BBox     *layoutBBox `json:"bbox"`
	Source   string      `json:"source,omitempty"`
}

func buildSchVisualSurveyJS(comps []layoutComp) string {
	var ids []string
	for _, c := range comps {
		if c.ComponentType == "part" {
			ids = append(ids, c.ID)
		}
	}
	sort.Strings(ids)
	encoded, _ := json.Marshal(ids)
	return "const frame = await (async()=>{" + buildSchFrameSurveyJS() + "})();\n" +
		"const parentIds = " + string(encoded) + ` || [];
const designators = [];
for (const parentId of parentIds) {
  // EDA 3.2.186 returns [] for the unscoped getAll(); ALWAYS pass the parent.
  const attrs = await eda.sch_PrimitiveAttribute.getAll(parentId);
  if (!Array.isArray(attrs)) throw new Error('designator inventory unavailable: '+parentId);
  for (const a of attrs) {
    if (a.getState_Key() !== 'Designator') continue;
    const id = a.getState_PrimitiveId();
    designators.push({id,parentId:a.getState_ParentPrimitiveId(),key:a.getState_Key(),value:a.getState_Value(),visible:a.getState_ValueVisible(),bbox:await eda.sch_Primitive.getPrimitivesBBox([id])});
  }
}
return {...frame, designators};`
}

func parseSchDesignatorGeometry(v map[string]any) ([]schDesignatorGeometry, error) {
	items, ok := v["designators"].([]any)
	if !ok {
		return nil, fmt.Errorf("designator inventory missing")
	}
	b, err := json.Marshal(v["designators"])
	if err != nil {
		return nil, err
	}
	var rows []schDesignatorGeometry
	if err = json.Unmarshal(b, &rows); err != nil {
		return nil, err
	}
	for i, item := range items {
		m, _ := item.(map[string]any)
		bb, _ := m["bbox"].(map[string]any)
		for _, k := range []string{"minX", "minY", "maxX", "maxY"} {
			n, ok := bb[k].(float64)
			if !ok || !plFinite(n) {
				rows[i].BBox = nil
				break
			}
		}
	}
	return rows, nil
}

func schPositiveBBox(b *layoutBBox) bool {
	return b != nil && plBoxValid(*b) && b.MaxX > b.MinX && b.MaxY > b.MinY
}

func schModuleFrames(s schFrameSurvey) []schFrameSpec {
	var frames []schFrameSpec
	for id, m := range s.Rectangles {
		if !strings.EqualFold(fmt.Sprint(m["color"]), "#AA00AA") {
			continue
		}
		x, xok := m["x"].(float64)
		y, yok := m["y"].(float64)
		w, wok := m["width"].(float64)
		h, hok := m["height"].(float64)
		r, rok := m["rotation"].(float64)
		b := layoutBBox{MinX: x, MinY: y - h, MaxX: x + w, MaxY: y}
		if xok && yok && wok && hok && rok && r == 0 && schPositiveBBox(&b) {
			frames = append(frames, schFrameSpec{ID: id, Rect: b})
		}
	}
	sort.Slice(frames, func(i, j int) bool { return frames[i].ID < frames[j].ID })
	return frames
}

func schDesignatorFindings(s schFrameSurvey, rows []schDesignatorGeometry, comps []layoutComp, wires []schGroupWire) []checkFinding {
	var out []checkFinding
	byParent := map[string][]schDesignatorGeometry{}
	seen := map[string]bool{}
	for _, d := range rows {
		if d.Key != "Designator" {
			continue
		} // No property-text collision/containment rules.
		if d.ID == "" || seen[d.ID] {
			out = append(out, checkFinding{Type: "designator-geometry-unavailable", Level: "ERROR", Message: "missing/duplicate designator identity"})
			continue
		}
		seen[d.ID] = true
		byParent[d.ParentID] = append(byParent[d.ParentID], d)
	}
	var valid []schDesignatorGeometry
	frames := schModuleFrames(s)
	for _, c := range comps {
		if c.ComponentType != "part" {
			continue
		}
		ds := byParent[c.ID]
		if len(ds) != 1 || ds[0].Value != c.Designator || ds[0].Visible == nil || !*ds[0].Visible || !schPositiveBBox(ds[0].BBox) || !schPositiveBBox(c.BBox) {
			out = append(out, checkFinding{Type: "designator-geometry-unavailable", Level: "ERROR", PrimitiveId: c.ID, Designator: c.Designator, Message: "expected one visible matching Designator with real bbox and parent body geometry; missing data is not a pass"})
			continue
		}
		d := ds[0]
		valid = append(valid, d)
		// Infer a geometric owner only when exactly one frame contains the body
		// centre. An outside label must not become legal by sitting in another frame.
		if len(frames) > 0 {
			x, y := bboxCenter(*c.BBox)
			var owners []schFrameSpec
			for _, f := range frames {
				if x >= f.Rect.MinX && x <= f.Rect.MaxX && y >= f.Rect.MinY && y <= f.Rect.MaxY {
					owners = append(owners, f)
				}
			}
			if len(owners) != 1 {
				out = append(out, checkFinding{Type: "part-frame-unresolved", Level: "ERROR", PrimitiveId: c.ID, Designator: c.Designator, Message: "body must belong to exactly one live module frame"})
			} else {
				f := owners[0]
				if !boxInside(*c.BBox, f.Rect) {
					out = append(out, checkFinding{Type: "part-out-of-frame", Level: "ERROR", PrimitiveId: c.ID, Designator: c.Designator, PrimitiveIds: []string{c.ID, f.ID}, BBox: c.BBox, Keepout: &f.Rect, Message: "component body extends outside its module frame"})
				}
				if !boxInside(*d.BBox, f.Rect) {
					out = append(out, checkFinding{Type: "designator-out-of-frame", Level: "ERROR", PrimitiveId: d.ID, Designator: c.Designator, PrimitiveIds: []string{d.ID, c.ID, f.ID}, BBox: d.BBox, Keepout: &f.Rect, Message: "Designator extends outside its parent component's frame (model/parameter text excluded)"})
				}
			}
		}
	}
	// Parent IDs must resolve to the same component snapshot, not another page.
	for parent := range byParent {
		found := false
		for _, c := range comps {
			if c.ID == parent && c.ComponentType == "part" {
				found = true
				break
			}
		}
		if !found {
			out = append(out, checkFinding{Type: "designator-geometry-unavailable", Level: "ERROR", PrimitiveId: parent, Message: "designator parent absent from component snapshot"})
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].ID < valid[j].ID })
	for i, d := range valid {
		addOverlap := func(id string, b *layoutBBox) {
			if !schPositiveBBox(b) {
				return
			}
			ox, oy, overlap := overlapExtent(*d.BBox, *b)
			// Native default labels sit on their OWN body's outline. The SDK
			// body bbox includes the 1-raw outline's half-stroke (0.5 raw).
			// Allow only that own-parent edge contact, not foreign objects or
			// a label penetrating farther into the body. ceshi real fixtures.
			if id == d.ParentID && math.Min(ox, oy) <= 0.5+1e-6 {
				return
			}
			if overlap && ox > 0 && oy > 0 {
				out = append(out, checkFinding{Type: "designator-overlap", Level: "warn", PrimitiveId: d.ID, Designator: d.Value, PrimitiveIds: []string{d.ID, id}, BBox: d.BBox, OverlapX: ox, OverlapY: oy, Message: "Designator overlaps body/marker/title/another designator; non-designator properties excluded"})
			}
		}
		for _, c := range comps {
			if c.ComponentType != "part" && !isSchMarker(c.ComponentType) {
				continue
			}
			b := c.BBox
			if band := flagTextBand(c); band != nil && b != nil {
				merged := layoutBBox{MinX: math.Min(b.MinX, band.MinX), MinY: math.Min(b.MinY, band.MinY), MaxX: math.Max(b.MaxX, band.MaxX), MaxY: math.Max(b.MaxY, band.MaxY)}
				b = &merged
			}
			addOverlap(c.ID, b)
		}
		for _, other := range valid[i+1:] {
			addOverlap(other.ID, other.BBox)
		}
		for _, id := range sortedStateKeys(s.Texts) {
			b, _ := json.Marshal(s.Texts[id]["bbox"])
			var box layoutBBox
			if json.Unmarshal(b, &box) == nil {
				addOverlap(id, &box)
			}
		}
		for _, w := range wires {
			for _, seg := range schDesignatorWireSegments(w) {
				if schSegmentCrossesBox(seg[0], seg[1], seg[2], seg[3], *d.BBox) {
					out = append(out, checkFinding{Type: "designator-wire-overlap", Level: "warn", PrimitiveId: d.ID, Designator: d.Value, WirePrimitiveId: w.ID, BBox: d.BBox, Message: "wire crosses Designator text bbox"})
					break
				}
			}
		}
	}
	return out
}

// schDesignatorWireSegments preserves the observed wire encoding. Current
// EasyEDA getState_Line flat arrays are independent four-coordinate records,
// while older tests/callers supplied a continuous vertex polyline. Never join
// independent observed records with a phantom tail-to-head diagonal.
func schDesignatorWireSegments(w schGroupWire) [][4]float64 {
	if len(w.ObservedSegments) > 0 {
		return w.ObservedSegments
	}
	segments := make([][4]float64, 0, len(w.Points)/2)
	for i := 0; i+3 < len(w.Points); i += 2 {
		segments = append(segments, [4]float64{w.Points[i], w.Points[i+1], w.Points[i+2], w.Points[i+3]})
	}
	return segments
}

// Designator text is a closed obstacle: a real wire touching its edge or corner
// obscures it, including a tangent or an endpoint. This rule is deliberately
// separate from component-body contacts, where a pin endpoint can be legal.
// Liang–Barsky handles diagonal segments without inventing a bounding-box hit.
func schSegmentCrossesBox(x0, y0, x1, y1 float64, b layoutBBox) bool {
	if x0 == x1 && y0 == y1 {
		return false // a zero-length record is not a real wire segment
	}
	lo, hi := 0.0, 1.0
	for _, axis := range [][4]float64{{x0, x1, b.MinX, b.MaxX}, {y0, y1, b.MinY, b.MaxY}} {
		d := axis[1] - axis[0]
		if d == 0 {
			if axis[0] < axis[2] || axis[0] > axis[3] {
				return false
			}
			continue
		}
		a, z := (axis[2]-axis[0])/d, (axis[3]-axis[0])/d
		if a > z {
			a, z = z, a
		}
		lo = math.Max(lo, a)
		hi = math.Min(hi, z)
		if lo > hi {
			return false
		}
	}
	return lo <= hi
}
