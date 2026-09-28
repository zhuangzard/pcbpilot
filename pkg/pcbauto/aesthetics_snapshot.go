package pcbauto

import (
	"encoding/json"
	"math"
	"strings"
)

// AesInputFromSnapshot builds the analyser input from a `pcb dump
// [--include-copper]` document or a routed snapshot written by `pcb auto
// run` (board.routed.json, ExportRoutedSnapshot). Both carry the same schema.
// Arcs are counted but not measured (rounded corners are deliberate style).
func AesInputFromSnapshot(raw []byte) (AesInput, error) {
	b, err := FromSnapshot(raw)
	if err != nil {
		return AesInput{}, err
	}
	var s struct {
		Components []struct {
			ID         string `json:"primitiveId"`
			Designator string `json:"designator"`
		} `json:"components"`
		Copper *struct {
			Lines   []map[string]any `json:"lines"`
			Arcs    []map[string]any `json:"arcs"`
			Vias    []map[string]any `json:"vias"`
			Pours   []map[string]any `json:"pours"`
			Poured  []map[string]any `json:"poured"`
			Regions []map[string]any `json:"regions"`
			Fills   []map[string]any `json:"fills"`
		} `json:"copper"`
		Silk []struct {
			Key      string    `json:"Key"`
			Text     string    `json:"Text"`
			Layer    int       `json:"Layer"`
			Rotation float64   `json:"Rotation"`
			FontSize float64   `json:"FontSize"`
			CompID   string    `json:"CompID"`
			X        float64   `json:"X"`
			Y        float64   `json:"Y"`
			BBox     *snapBBox `json:"BBox"`
		} `json:"silk"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return AesInput{}, err
	}
	in := AesInput{Board: b, PlaneNets: map[string]bool{}}
	copperLayer := func(l int) bool { return l == LayerTop || l == LayerBottom || l >= LayerInner1 && l < LayerInner1+32 }
	if cu := s.Copper; cu != nil {
		in.Copper = true
		for _, l := range cu.Lines {
			ly := int(num(l["layer"]))
			net := str(l["net"])
			if net == "" || !copperLayer(ly) {
				continue
			}
			in.Tracks = append(in.Tracks, Track{Net: net, Layer: ly, Width: num(l["lineWidth"]),
				A: Point{num(l["startX"]), num(l["startY"])}, B: Point{num(l["endX"]), num(l["endY"])}})
			in.TrackIDs = append(in.TrackIDs, str(l["primitiveId"]))
		}
		for _, v := range cu.Vias {
			in.Vias = append(in.Vias, Via{Net: str(v["net"]), C: Point{num(v["x"]), num(v["y"])}, Dia: num(v["diameter"]), Drill: num(v["holeDiameter"])})
			in.ViaIDs = append(in.ViaIDs, str(v["primitiveId"]))
		}
		for _, p := range append(append([]map[string]any(nil), cu.Pours...), cu.Poured...) {
			if n := str(p["net"]); n != "" {
				in.PlaneNets[n] = true
			}
		}
		for _, r := range cu.Regions {
			name := strings.ToLower(str(r["name"]))
			if strings.Contains(name, "isolation") || strings.Contains(name, "moat") || strings.Contains(name, "creepage") {
				if poly := sourcePoints(r["source"]); len(poly) >= 3 {
					in.ExemptZones = append(in.ExemptZones, poly)
				}
			}
		}
		for _, f := range cu.Fills {
			if int(num(f["layer"])) != LayerMulti {
				continue
			}
			// A milled slot is an elongated MULTI cutout; a round mounting
			// hole is not an exemption zone.
			poly := sourcePoints(f["source"])
			if len(poly) < 3 {
				continue
			}
			bb := PolyBounds(poly)
			if lo, hi := math.Min(bb.W(), bb.H()), math.Max(bb.W(), bb.H()); lo > 0 && hi/lo >= 1.5 {
				in.ExemptZones = append(in.ExemptZones, poly)
			}
		}
	}
	refOf := map[string]string{}
	for _, c := range s.Components {
		refOf[c.ID] = c.Designator
	}
	for _, t := range s.Silk {
		if t.Key != "Designator" {
			continue
		}
		in.SilkKnown = true
		st := SilkText{Ref: refOf[t.CompID], Text: t.Text, Layer: t.Layer, Rotation: t.Rotation, FontSize: t.FontSize, X: t.X, Y: t.Y}
		if st.Ref == "" {
			st.Ref = t.Text
		}
		if t.BBox != nil && t.BBox.MaxX > t.BBox.MinX {
			st.BBox = Rect{t.BBox.MinX, t.BBox.MinY, t.BBox.MaxX, t.BBox.MaxY}
			st.HasBBox = true
		}
		in.Silk = append(in.Silk, st)
	}
	return in, nil
}

// AestheticsFromSnapshot analyses a dump / routed snapshot offline.
func AestheticsFromSnapshot(raw []byte) (*AestheticsReport, error) {
	in, err := AesInputFromSnapshot(raw)
	if err != nil {
		return nil, err
	}
	rep := Aesthetics(in)
	var s struct {
		Copper *struct {
			Arcs []any `json:"arcs"`
		} `json:"copper"`
	}
	if json.Unmarshal(raw, &s) == nil && s.Copper != nil && len(s.Copper.Arcs) > 0 {
		rep.Counts["arcsIgnored"] = len(s.Copper.Arcs)
		rep.Notes = append(rep.Notes, "arc tracks are not measured (rounded corners are a deliberate style)")
	}
	return rep, nil
}

// AesInputFromResult is the analyser input for an engine run (pcb auto).
func AesInputFromResult(b *Board, an *Analysis, c *Circuit, st *Stackup, rr *RouteResult, iso *IsolationReport) AesInput {
	in := AesInput{Board: b, Analysis: an, Circuit: c, Stackup: st, PlaneNets: map[string]bool{}}
	if rr != nil {
		in.Copper = true
		in.Tracks, in.Vias = rr.Tracks, rr.Vias
		for _, p := range rr.Planes {
			in.PlaneNets[p.Net] = true
		}
	}
	if st != nil {
		for _, l := range st.Stack {
			for _, n := range l.Nets {
				in.PlaneNets[n] = true
			}
			for _, n := range l.PourNets {
				in.PlaneNets[n] = true
			}
		}
	}
	if an != nil {
		for n, np := range an.ByNet {
			if np.Plane {
				in.PlaneNets[n] = true
			}
		}
	}
	if iso != nil {
		in.ExemptZones = append(in.ExemptZones, iso.Moats...)
		for _, s := range iso.Slots {
			in.ExemptZones = append(in.ExemptZones, s.Poly)
		}
	}
	for _, h := range b.Holes {
		if len(h.Poly) >= 3 && h.Owner == "" {
			in.ExemptZones = append(in.ExemptZones, h.Poly)
		}
	}
	return in
}
