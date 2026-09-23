package app

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbrouting"
)

// PCB identities and units belong to the host adapter, not the search package.
type pcbRouteRequest struct {
	SchemaVersion int     `json:"schemaVersion"`
	Units         string  `json:"units"`
	Net           string  `json:"net"`
	Layer         int     `json:"layer"`
	WidthMil      float64 `json:"widthMil"`
	From          string  `json:"from"`
	To            string  `json:"to"`
	StepMil       float64 `json:"stepMil"`
	MaxDetourMil  float64 `json:"maxDetourMil"`
	MaxStates     int     `json:"maxStates"`
}

type pcbRouteCandidate struct {
	Net      string       `json:"net"`
	Layer    int          `json:"layer"`
	WidthMil float64      `json:"widthMil"`
	Points   [][2]float64 `json:"points"`
}

type pcbRouteReport struct {
	SchemaVersion int                `json:"schemaVersion"`
	Status        string             `json:"status"`
	BoardSHA256   string             `json:"boardSha256"`
	RequestSHA256 string             `json:"requestSha256"`
	PlanSHA256    string             `json:"planSha256,omitempty"`
	Preview       string             `json:"preview"`
	Candidate     *pcbRouteCandidate `json:"candidate,omitempty"`
	Search        *pcbrouting.Result `json:"search,omitempty"`
	LengthMil     float64            `json:"lengthMil"`
	Bends         int                `json:"bends"`
	Reasons       []string           `json:"reasons,omitempty"`
	Limitations   []string           `json:"limitations"`
}

func newPCBRouteReport(board, request []byte) pcbRouteReport {
	return pcbRouteReport{SchemaVersion: 1, Status: "incomplete", BoardSHA256: sha256String(board), RequestSHA256: sha256String(request), Limitations: []string{
		"offline single-layer endpoint-pair proof only; no editor writes or saved-state evidence",
		"no global optimality, whole-board routing, differential-pair, impedance or return-path proof",
	}}
}

func preparePCBRouteRequest(in pcbRouteRequest, snap *boardSnapshot) (pcbrouting.Request, pcbrouting.SegmentClear, string, error) {
	r := pcbrouting.Request{Step: in.StepMil, MaxDetour: in.MaxDetourMil, MaxStates: in.MaxStates}
	fail := func(status, reason string) (pcbrouting.Request, pcbrouting.SegmentClear, string, error) {
		return r, nil, status, fmt.Errorf("%s", reason)
	}
	if in.SchemaVersion != 1 || in.Units != "mil" || strings.TrimSpace(in.Net) == "" || in.From == "" || in.To == "" || in.From == in.To || !allFinite(in.WidthMil) || in.WidthMil <= 0 {
		return fail("fail", "route request requires schemaVersion 1, units mil, net, distinct from/to pads and finite positive widthMil")
	}
	if err := r.Validate(); err != nil {
		return fail("fail", err.Error())
	}
	if in.Layer != 1 && in.Layer != 2 {
		return fail("incomplete", "route solver currently supports TOP/BOTTOM only")
	}
	if snap == nil || len(snap.Partial) > 0 || snap.Components == nil || snap.CopperLayers < 1 || in.Layer == 2 && snap.CopperLayers < 2 {
		return fail("incomplete", "route solver needs a complete measured snapshot and stackup")
	}
	g, status, err := preparePCBRouteGeometry(snap)
	if err != nil {
		return fail(status, err.Error())
	}
	if err := validateCrystalFenceContour(snap.Outline.Points); err != nil {
		return fail("incomplete", "invalid board outline: "+err.Error())
	}
	if len(g.arcs) != 0 {
		return fail("incomplete", "exact arc-track clearance is unsupported by route solve/check")
	}
	if in.WidthMil < snap.Rules.TrackWidthMinMil {
		return fail("fail", "requested widthMil is below the measured minimum")
	}
	a, aok := g.pads[in.From]
	z, zok := g.pads[in.To]
	if !aok || !zok || g.ambiguous[in.From] || g.ambiguous[in.To] {
		return fail("incomplete", "route endpoints require unique measured designator.pad identities")
	}
	if a.Net != in.Net || z.Net != in.Net {
		return fail("fail", "route endpoint net differs from the independent request")
	}
	if !padLayerMatches(a.Layer, in.Layer) || !padLayerMatches(z.Layer, in.Layer) {
		return fail("incomplete", "endpoint needs an unsupported layer transition")
	}
	r.From, r.To = [2]float64{a.X, a.Y}, [2]float64{z.X, z.Y}
	if err := r.Validate(); err != nil {
		return fail("fail", err.Error())
	}
	// These stricter capability checks are local to the new command. Existing
	// planners retain their own contracts. In particular, never turn arc chords,
	// thermal-spoke strokes or missing cutout geometry into free space.
	var slots []pcbCopperArea
	type strokedArea struct {
		area  pcbCopperArea
		width float64
	}
	var strokes []strokedArea
	for _, raw := range snap.Copper.Fills {
		m := raw.(map[string]any) // checked by preparePCBRouteGeometry
		layer := int(asFloat(m["layer"]))
		if !netPathCopperLayer(layer) && layer != pcbLayerMulti {
			continue
		}
		if m["geometryAvailable"] != true {
			return fail("incomplete", "fill/cutout geometry is unknown")
		}
		stroke, ok := asFloatOK(m["lineWidth"])
		if !ok || !allFinite(stroke) || stroke < 0 || m["fillMode"] != "solid" {
			return fail("incomplete", "fill/cutout needs known solid fill mode and nonnegative stroke width")
		}
		contours, err := pcbRouteExactContours(m["source"])
		if err != nil {
			return fail("incomplete", err.Error())
		}
		if layer == pcbLayerMulti {
			if stroke != 0 {
				return fail("incomplete", "stroked cutout boundary is unsupported")
			}
			slots = append(slots, pcbCopperArea{Contours: contours})
		} else {
			strokes = append(strokes, strokedArea{area: pcbCopperArea{Net: asString(m["net"]), Layer: layer, Contours: contours}, width: stroke})
		}
	}
	for _, raw := range snap.Copper.Poured {
		m := raw.(map[string]any)
		for _, rawFill := range m["fills"].([]any) {
			fill := rawFill.(map[string]any)
			if fill["fill"] != true {
				return fail("incomplete", "poured fill/stroke semantics unknown or unsupported")
			}
			width, ok := asFloatOK(fill["lineWidth"])
			if !ok || !allFinite(width) || width < 0 {
				return fail("incomplete", "poured polygon stroke width unknown or unsupported")
			}
			contours, err := pcbRouteExactContours(fill["source"])
			if err != nil {
				return fail("incomplete", err.Error())
			}
			strokes = append(strokes, strokedArea{area: pcbCopperArea{Net: asString(m["net"]), Layer: int(asFloat(m["layer"])), Contours: contours}, width: width})
		}
	}
	for _, raw := range snap.Copper.Regions {
		m := raw.(map[string]any)
		for _, rule := range m["ruleType"].([]any) {
			if asFloat(rule) == 5 {
				layer := int(asFloat(m["layer"]))
				if layer != pcbLayerMulti && !netPathCopperLayer(layer) {
					return fail("incomplete", "no-wires region layer is unsupported")
				}
				if _, err := pcbRouteExactContours(m["source"]); err != nil {
					return fail("incomplete", err.Error())
				}
			}
		}
	}
	clear, err := g.clear(pcbEscapeDemand{Net: in.Net, Layer: in.Layer, WidthMil: in.WidthMil}, nil)
	if err != nil {
		return fail("incomplete", err.Error())
	}
	return r, func(a, z [2]float64) bool {
		if !clear(a, z) {
			return false
		}
		for _, slot := range slots {
			if copperAreaSegmentDistance(slot, a, z) < in.WidthMil/2+math.Max(8, math.Max(snap.Rules.ClearanceMil, snap.Rules.CopperToEdgeMil))-netPathGeomEps {
				return false
			}
		}
		for _, stroke := range strokes {
			if stroke.area.Net != in.Net && stroke.area.Layer == in.Layer && copperAreaSegmentDistance(stroke.area, a, z) < in.WidthMil/2+stroke.width/2+snap.Rules.ClearanceMil-netPathGeomEps {
				return false
			}
		}
		return true
	}, "", nil
}

// No curved contours are approximated for this proof. The broader host parser
// can flatten ARC for other consumers; here only exact polygonal edges qualify.
func pcbRouteExactContours(raw any) ([][][2]float64, error) {
	var checkTokens func(any) error
	checkTokens = func(v any) error {
		switch x := v.(type) {
		case []any:
			for _, child := range x {
				if err := checkTokens(child); err != nil {
					return err
				}
			}
		case string:
			if x != "L" {
				return fmt.Errorf("route solver requires exact polygonal contours; %s is unsupported", x)
			}
		}
		return nil
	}
	if err := checkTokens(raw); err != nil {
		return nil, err
	}
	contours, err := polygonSourceContours(raw)
	if err != nil {
		return nil, err
	}
	for _, contour := range contours {
		if err := validateCrystalFenceContour(contour); err != nil {
			return nil, err
		}
	}
	return contours, nil
}

func solvePCBRoute(ctx context.Context, in pcbRouteRequest, snap *boardSnapshot, report *pcbRouteReport) error {
	r, clear, status, err := preparePCBRouteRequest(in, snap)
	if err != nil {
		report.Status = status
		report.Reasons = []string{err.Error()}
		return err
	}
	result, err := pcbrouting.Solve(ctx, r, clear)
	report.Search = &result
	if err != nil {
		report.Reasons = []string{err.Error()}
		return err
	}
	if result.Status != pcbrouting.Found {
		report.Reasons = []string{result.Reason}
		return fmt.Errorf("route search incomplete: %s", result.Reason)
	}
	report.Status = "pass"
	report.Candidate = &pcbRouteCandidate{Net: in.Net, Layer: in.Layer, WidthMil: in.WidthMil, Points: result.Points}
	report.LengthMil, report.Bends = result.Length, result.Bends
	return nil
}

func checkPCBRoute(ctx context.Context, in pcbRouteRequest, snap *boardSnapshot, plan pcbRouteReport, report *pcbRouteReport) error {
	fail := func(status, reason string) error {
		report.Status = status
		report.Reasons = []string{reason}
		return fmt.Errorf("%s", reason)
	}
	r, clear, status, err := preparePCBRouteRequest(in, snap)
	if err != nil {
		return fail(status, err.Error())
	}
	if plan.SchemaVersion != 1 || plan.BoardSHA256 != report.BoardSHA256 || plan.RequestSHA256 != report.RequestSHA256 {
		return fail("fail", "plan provenance differs from board/request inputs")
	}
	c := plan.Candidate
	if c != nil {
		report.Candidate = &pcbRouteCandidate{Net: c.Net, Layer: c.Layer, WidthMil: c.WidthMil, Points: append([][2]float64(nil), c.Points...)}
	}
	if plan.Status != "pass" || c == nil || c.Net != in.Net || c.Layer != in.Layer || c.WidthMil != in.WidthMil {
		return fail("fail", "plan candidate is missing or differs from requested net/layer/width")
	}
	if err := pcbrouting.Check(ctx, r, c.Points, clear); err != nil {
		return fail("fail", err.Error())
	}
	report.Status = "pass"
	report.LengthMil, report.Bends = pcbrouting.Metrics(c.Points)
	return nil
}
