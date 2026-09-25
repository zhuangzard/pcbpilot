package app

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"

	"github.com/zhuangzard/pcbpilot/internal/schguard"
)

// Compare drawn geometry independently of the pin netlist. Collinear wires may
// be split/merged by the editor; their occupied grid edges must still match.
type schematicDrawingExpectation struct {
	Wires []powerLayoutWire `json:"wires"`
	Flags []powerLayoutFlag `json:"flags"`
}

func (e *schematicDrawingExpectation) validate() error {
	_, _, err := e.geometry()
	return err
}

func drawingEdges(wires []powerLayoutWire) (map[string]bool, error) {
	out := map[string]bool{}
	for _, w := range wires {
		if len(w.Points) < 2 {
			return nil, fmt.Errorf("drawing wire needs two points")
		}
		for i := 1; i < len(w.Points); i++ {
			a, b := w.Points[i-1], w.Points[i]
			if !plGrid(a[0]) || !plGrid(a[1]) || !plGrid(b[0]) || !plGrid(b[1]) {
				return nil, fmt.Errorf("drawing wire must be nonzero, orthogonal and on the 5-unit grid")
			}
			for axis := range a {
				a[axis] = math.Round(a[axis]/5) * 5
				b[axis] = math.Round(b[axis]/5) * 5
			}
			if a == b || (a[0] != b[0] && a[1] != b[1]) {
				return nil, fmt.Errorf("drawing wire must be nonzero and orthogonal on grid")
			}
			n := int((math.Abs(b[0]-a[0]) + math.Abs(b[1]-a[1])) / 5)
			if n > 100000 {
				return nil, fmt.Errorf("drawing wire exceeds supported extent")
			}
			dx, dy := (b[0]-a[0])/float64(n), (b[1]-a[1])/float64(n)
			for j := 0; j < n; j++ {
				p := [2]float64{a[0] + float64(j)*dx, a[1] + float64(j)*dy}
				q := [2]float64{p[0] + dx, p[1] + dy}
				if p[0] > q[0] || (p[0] == q[0] && p[1] > q[1]) {
					p, q = q, p
				}
				out[fmt.Sprintf("%g,%g:%g,%g", p[0], p[1], q[0], q[1])] = true
			}
		}
	}
	return out, nil
}

func (e *schematicDrawingExpectation) geometry() (map[string]bool, map[string]int, error) {
	wires := append([]powerLayoutWire(nil), e.Wires...)
	markers := map[string]int{}
	for _, f := range e.Flags {
		family, kind := f.Kind, "netflag"
		if isNetPortKind(f.Kind) {
			family, kind = "port", "netport"
		}
		label := f.Kind == "net_label"
		if label {
			family = "port" // direction table only; a label is not a component
		}
		rot, ok := flagBodyRotation[family][f.Direction]
		if !ok || f.Net == "" || f.Offset <= 0 || !plGrid(f.Offset) {
			return nil, nil, fmt.Errorf("invalid drawing marker")
		}
		x, y := f.PinX, f.PinY
		switch f.Direction {
		case "left":
			x -= f.Offset
		case "right":
			x += f.Offset
		case "up":
			y += f.Offset
		case "down":
			y -= f.Offset
		}
		wires = append(wires, powerLayoutWire{Net: f.Net, Points: [][2]float64{{f.PinX, f.PinY}, {x, y}}})
		if label {
			// V3: the stub wire's visible Name attribute; V4: a label
			// attribute. Neither is a component: its lead is checked here and
			// its name by the per-pin net readback.
			continue
		}
		markers[fmt.Sprintf("%s:%s:%g,%g:%g", kind, f.Net, x, y, rot)]++
	}
	edges, err := drawingEdges(wires)
	return edges, markers, err
}

func (e *schematicDrawingExpectation) check(result any) error {
	wantEdges, wantMarkers, err := e.geometry()
	if err != nil {
		return err
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	var live struct {
		Summary *struct {
			Scope        string `json:"scope"`
			Buses        *int   `json:"buses"`
			ShortSymbols *int   `json:"shortSymbols"`
		} `json:"connectivitySummary"`
		Wires *[]struct {
			X0 *float64 `json:"x0"`
			Y0 *float64 `json:"y0"`
			X1 *float64 `json:"x1"`
			Y1 *float64 `json:"y1"`
		} `json:"wires"`
		Components []struct {
			Kind     string   `json:"componentType"`
			Net      string   `json:"net"`
			X        *float64 `json:"x"`
			Y        *float64 `json:"y"`
			Rotation *float64 `json:"rotation"`
		} `json:"components"`
	}
	if err = json.Unmarshal(raw, &live); err != nil {
		return err
	}
	if live.Summary == nil || live.Summary.Scope != "activePage" || live.Summary.Buses == nil || live.Summary.ShortSymbols == nil {
		return fmt.Errorf("drawing requires active-page bus/short-symbol counts (includeConnectivitySummary:true)")
	}
	if *live.Summary.Buses != 0 || *live.Summary.ShortSymbols != 0 {
		return fmt.Errorf("drawing contains unsupported buses/short symbols (%d/%d)", *live.Summary.Buses, *live.Summary.ShortSymbols)
	}
	if live.Wires == nil {
		return fmt.Errorf("drawing requires measured wires (includeWires:true)")
	}
	var wires []powerLayoutWire
	for _, w := range *live.Wires {
		if w.X0 == nil || w.X1 == nil || w.Y0 == nil || w.Y1 == nil {
			return fmt.Errorf("incomplete measured wire")
		}
		wires = append(wires, powerLayoutWire{Points: [][2]float64{{*w.X0, *w.Y0}, {*w.X1, *w.Y1}}})
	}
	edges, err := drawingEdges(wires)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(edges, wantEdges) {
		return fmt.Errorf("drawn wire paths differ from composition (%d grid edges, expected %d)", len(edges), len(wantEdges))
	}
	wantWires := append([]powerLayoutWire(nil), e.Wires...)
	for _, f := range e.Flags {
		x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
		wantWires = append(wantWires, powerLayoutWire{Net: f.Net, Points: [][2]float64{{f.PinX, f.PinY}, {x, y}}})
	}
	// Grid-edge coverage cannot distinguish a bare X from a real junction.
	// Compare the physical partition separately, before names/ownership gates.
	// Only original action/observed segment vertices are fed to this kernel;
	// the synthetic 5raw subdivisions from drawingEdges are never junctions.
	if err := schguard.CompareWireTopology(drawingContactSnapshot(wantWires), drawingContactSnapshot(wires)); err != nil {
		return fmt.Errorf("drawn wire contact topology differs from composition: %w", err)
	}
	markers := map[string]int{}
	for _, c := range live.Components {
		if c.Kind == "part" || c.Kind == "sheet" || c.Kind == "nonElectrical" {
			continue
		}
		if c.Kind != "netflag" && c.Kind != "netport" {
			return fmt.Errorf("unexpected drawing component type %s", c.Kind)
		}
		if c.X == nil || c.Y == nil || c.Rotation == nil || c.Net == "" {
			return fmt.Errorf("incomplete measured marker")
		}
		markers[fmt.Sprintf("%s:%s:%g,%g:%g", c.Kind, c.Net, *c.X, *c.Y, *c.Rotation)]++
	}
	if !reflect.DeepEqual(markers, wantMarkers) {
		return fmt.Errorf("drawn markers differ from composition")
	}
	return nil
}

func drawingContactSnapshot(wires []powerLayoutWire) map[string]any {
	var rows []any
	for _, w := range wires {
		points := plNormalizeWirePoints(w.Points)
		for i := 1; i < len(points); i++ {
			a, b := points[i-1], points[i]
			rows = append(rows, map[string]any{"x0": a[0], "y0": a[1], "x1": b[0], "y1": b[1]})
		}
	}
	if rows == nil {
		rows = []any{}
	}
	return map[string]any{"wiresAvailable": true, "wires": rows}
}
