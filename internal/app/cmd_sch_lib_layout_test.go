package app

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"github.com/zhuangzard/pcbpilot/internal/schguard"
)

func libLayoutFixture() libLayoutSource {
	c := composeFixture(1)
	c.Sheet = layoutBBox{-1000, -1000, 1000, 1000}
	core := c.Connectivity.Components[0]
	core.Pins = []connectivity.Pin{{Number: "1"}, {Number: "2"}, {Number: "3", NoConnected: true}}
	c.Connectivity.Components[0] = core
	c.Connectivity.Components = append(c.Connectivity.Components, connectivity.Component{ID: "stable-R1", Ref: "R1", Device: core.Device, Pins: []connectivity.Pin{{Number: "1"}, {Number: "2"}}})
	c.Connectivity.Modules[0].PeripheralComponents = []string{"stable-R1"}
	c.Connectivity.Connections = append(c.Connectivity.Connections, connectivity.Connection{ComponentID: "stable-R1", PinNumber: "1", NetID: "stable-vdd-id"}, connectivity.Connection{ComponentID: "stable-R1", PinNumber: "2", NetID: "stable-gnd-id"})
	measurements := []powerLayoutPlacement{
		{Designator: "U1", X: 100, Y: 100, BBox: layoutBBox{80, 80, 120, 120}, Pins: []powerLayoutPin{{Number: "1", Net: "+3V3", X: 130, Y: 100}, {Number: "2", Net: "GND", X: 100, Y: 70}, {Number: "3", X: 70, Y: 110}}},
		{Designator: "R1", X: 300, Y: 100, BBox: layoutBBox{290, 90, 310, 110}, Pins: []powerLayoutPin{{Number: "1", Net: "+3V3", X: 280, Y: 100}, {Number: "2", Net: "GND", X: 320, Y: 100}}},
	}
	return libLayoutSource{SchemaVersion: 1, Connectivity: c.Connectivity, Sheet: c.Sheet, Measurements: measurements, LayoutModules: []libLayoutModule{{ID: c.Connectivity.Modules[0].ID, Title: "CORE", CoreComponentID: core.ID, NetPolicies: map[string]string{"stable-vdd-id": "direct", "stable-gnd-id": "local_ground"}}}}
}
func TestLibLayoutMeasuredDirectPeripheral(t *testing.T) {
	src := libLayoutFixture()
	before, _ := json.Marshal(src)
	out, e := planLibLayout(src)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = planSchComposition(*out); e != nil {
		t.Fatal(e)
	}
	after, _ := json.Marshal(src)
	if string(before) != string(after) {
		t.Fatal("source mutated")
	}
	again, e := planLibLayout(src)
	if e != nil || !reflect.DeepEqual(out, again) {
		t.Fatal("not deterministic")
	}
	m := out.Modules[0]
	a, b := m.Placements[0], m.Placements[1]
	if a.Pins[0].Y != b.Pins[0].Y || b.Pins[0].X <= a.Pins[0].X {
		t.Fatal("attachment must be straight outward")
	}
	if b.Rotation != src.Measurements[1].Rotation || b.Mirror != src.Measurements[1].Mirror {
		t.Fatal("orientation changed")
	}
	if len(m.Wires) == 0 {
		t.Fatal("direct net requires real wires")
	}
	count := 0
	for _, f := range m.Flags {
		if f.Net == "+3V3" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("direct tree has more than one naming marker")
	}
}
func TestLibLayoutRejectsUnresolvedInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*libLayoutSource)
		want string
	}{
		{"missing measurement", func(s *libLayoutSource) { s.Measurements = s.Measurements[:1] }, "measurements"},
		{"missing policy", func(s *libLayoutSource) { delete(s.LayoutModules[0].NetPolicies, "stable-vdd-id") }, "policy"},
		{"wrong net", func(s *libLayoutSource) { s.Measurements[1].Pins[0].Net = "BAD" }, "canonical IR"},
		{"corner ambiguity", func(s *libLayoutSource) { s.Measurements[0].Pins[0].Y = 130 }, "ambiguous"},
		{"multiple core", func(s *libLayoutSource) {
			s.Connectivity.Modules[0].CoreComponents = append(s.Connectivity.Modules[0].CoreComponents, "stable-R1")
		}, "repeated module member"},
		{"no room", func(s *libLayoutSource) { s.Sheet = layoutBBox{0, 0, 20, 20} }, "drawing boundary"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := libLayoutFixture()
			tc.edit(&src)
			out, e := planLibLayout(src)
			if e == nil || out != nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("want %s, got %v", tc.want, e)
			}
		})
	}
}
func TestLibLayoutTranslationInvariant(t *testing.T) {
	a := libLayoutFixture()
	b := libLayoutFixture()
	for i, c := range b.Measurements {
		b.Measurements[i] = plTranslate(c, 245, -165)
	}
	x, e := planLibLayout(a)
	if e != nil {
		t.Fatal(e)
	}
	y, e := planLibLayout(b)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(x, y) {
		t.Fatal("measurement origin changed layout")
	}
}

func TestLibLayoutFourMeasuredSidesAndPreservedPose(t *testing.T) {
	for turns := 0; turns < 4; turns++ {
		s := libLayoutFixture()
		for i, c := range s.Measurements {
			s.Measurements[i] = plRotate(c, turns)
		}
		out, e := planLibLayout(s)
		if e != nil {
			t.Fatalf("turns=%d: %v", turns, e)
		}
		m := out.Modules[0]
		a, b := m.Placements[0].Pins[0], m.Placements[1].Pins[0]
		if a.X != b.X && a.Y != b.Y {
			t.Fatalf("turns=%d: did not prefer direct straight line", turns)
		}
		for i, c := range m.Placements {
			if c.Rotation != s.Measurements[i].Rotation || c.Mirror != s.Measurements[i].Mirror {
				t.Fatal("measured pose changed")
			}
		}
		// A naming T branch is legitimate; count geometry, not primitives.
		rows := make([]any, 0, len(m.Wires))
		for _, w := range m.Wires {
			rows = append(rows, map[string]any{"points": w.Points})
		}
		if err := schguard.VerifyWirePresent(map[string]any{"wires": rows}, map[string]any{"points": [][2]float64{{a.X, a.Y}, {b.X, b.Y}}}); err != nil {
			t.Fatal("lost direct physical attachment", err)
		}
		for i, w := range m.Wires {
			for _, other := range m.Wires[:i] {
				x, y := w.Points[0], w.Points[1]
				u, v := other.Points[0], other.Points[1]
				axis := -1
				if x[0] == y[0] && x[0] == u[0] && x[0] == v[0] {
					axis = 1
				}
				if x[1] == y[1] && x[1] == u[1] && x[1] == v[1] {
					axis = 0
				}
				if axis >= 0 && math.Max(math.Min(x[axis], y[axis]), math.Min(u[axis], v[axis])) < math.Min(math.Max(x[axis], y[axis]), math.Max(u[axis], v[axis])) {
					t.Fatal("duplicate positive-length physical wire")
				}
			}
		}
	}
}

func TestLibLayoutSharedNetThreePeripheralTree(t *testing.T) {
	s := libLayoutFixture()
	for _, ref := range []string{"R2", "R3"} {
		c := s.Connectivity.Components[1]
		c.ID = "stable-" + ref
		c.Ref = ref
		s.Connectivity.Components = append(s.Connectivity.Components, c)
		s.Connectivity.Modules[0].PeripheralComponents = append(s.Connectivity.Modules[0].PeripheralComponents, c.ID)
		s.Connectivity.Connections = append(s.Connectivity.Connections, connectivity.Connection{ComponentID: c.ID, PinNumber: "1", NetID: "stable-vdd-id"}, connectivity.Connection{ComponentID: c.ID, PinNumber: "2", NetID: "stable-gnd-id"})
		m := s.Measurements[1]
		m.Designator = ref
		s.Measurements = append(s.Measurements, m)
	}
	out, e := planLibLayout(s)
	if e != nil {
		t.Fatal(e)
	}
	m := out.Modules[0]
	if len(m.Placements) != 4 {
		t.Fatal("lost peripheral")
	}
	p := powerLayoutPlan{Placements: m.Placements, Wires: m.Wires, Flags: m.Flags}
	if e = validateSchCompositionNets(&p); e != nil {
		t.Fatal(e)
	}
	count := 0
	for _, f := range m.Flags {
		if f.Net == "+3V3" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("direct net must use one named wire tree")
	}
}

func TestLibLayoutRealAMS1117PerpendicularCaps(t *testing.T) {
	raw, err := os.ReadFile("testdata/lib-layout/ams1117.json")
	if err != nil {
		t.Fatal(err)
	}
	src, err := decodeLibLayout(raw)
	if err != nil {
		t.Fatal(err)
	}
	out, err := planLibLayout(src)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := planSchComposition(*out)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Modules[0].Placements) != 4 || len(plan.Layout.ExpectedPinNets) != 10 {
		t.Fatal("lost measured devices/pins")
	}
	if !reflect.DeepEqual(src.Connectivity, out.Connectivity) {
		t.Fatal("electrical source or original refs changed")
	}
	if plan.Layout.ExpectedPinNets["U1.2"] != "+3V3" || plan.Layout.ExpectedPinNets["U1.4"] != "+3V3" {
		t.Fatal("lost duplicate VOUT")
	}
	if err = validateSchCompositionNets(&plan.Layout); err != nil {
		t.Fatal(err)
	}
	for _, p := range out.Modules[0].Placements {
		for _, m := range src.Measurements {
			if p.Designator == m.Designator {
				if p.Rotation != m.Rotation || p.Mirror != m.Mirror {
					t.Fatal("invented pose")
				}
			}
		}
	}
}

func TestLibLayoutFollowsSeriesBranchAndMultipleCores(t *testing.T) {
	for _, explicit := range []bool{false, true} {
		s := libLayoutFixture()
		r := s.Connectivity.Components[1]
		led := r
		led.ID = "opaque-led-id"
		led.Ref = "D0002"
		s.Connectivity.Components = append(s.Connectivity.Components, led)
		s.Connectivity.Nets = append(s.Connectivity.Nets, connectivity.Net{ID: "led-anode", Name: "LED_A"})
		for i, e := range s.Connectivity.Connections {
			if e.ComponentID == r.ID && e.PinNumber == "2" {
				s.Connectivity.Connections[i].NetID = "led-anode"
			}
		}
		s.Connectivity.Connections = append(s.Connectivity.Connections, connectivity.Connection{ComponentID: led.ID, PinNumber: "1", NetID: "led-anode"}, connectivity.Connection{ComponentID: led.ID, PinNumber: "2", NetID: "stable-gnd-id"})
		s.Measurements[1].Pins[1].Net = "LED_A"
		m := s.Measurements[1]
		m.Designator = led.Ref
		m.Pins = append([]powerLayoutPin{}, m.Pins...)
		m.Pins[0].Net = "LED_A"
		m.Pins[1].Net = "GND"
		s.Measurements = append(s.Measurements, m)
		s.Connectivity.Modules[0].PeripheralComponents = []string{led.ID, r.ID} // Child precedes its parent in authored order.
		s.LayoutModules[0].NetPolicies["led-anode"] = "direct"
		if explicit {
			s.LayoutModules[0].Peripherals = []libLayoutPeripheral{{ComponentID: led.ID, PinNumber: "1", AttachTo: &libLayoutAttach{ComponentID: r.ID, PinNumber: "2"}}}
			// Additional core members use the same electrical attachment graph.
			s.Connectivity.Modules[0].CoreComponents = append(s.Connectivity.Modules[0].CoreComponents, r.ID)
			s.Connectivity.Modules[0].PeripheralComponents = []string{led.ID}
		}
		out, e := planLibLayout(s)
		if e != nil {
			t.Fatalf("explicit=%v: %v", explicit, e)
		}
		p := out.Modules[0]
		if p.Placements[2].Designator != "D0002" {
			t.Fatal("child was placed before parent or renamed")
		}
		plan := powerLayoutPlan{Placements: p.Placements, Wires: p.Wires, Flags: p.Flags}
		if e = validateSchCompositionNets(&plan); e != nil {
			t.Fatal(e)
		}
	}
}

func TestLibLayoutRejectsMissingEvidenceWithoutWritingOutput(t *testing.T) {
	raw, err := os.ReadFile("testdata/lib-layout/ams1117.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"x", "y", "rotation", "mirror", "bbox", "pins"} {
		t.Run(field, func(t *testing.T) {
			var input map[string]any
			_ = json.Unmarshal(raw, &input)
			delete(input["measurements"].([]any)[0].(map[string]any), field)
			data, _ := json.Marshal(input)
			dir := t.TempDir()
			from, out := filepath.Join(dir, "input.json"), filepath.Join(dir, "result.json")
			_ = os.WriteFile(from, data, 0600)
			_ = os.WriteFile(out, []byte("previous-good-output"), 0600)
			var stdout, stderr bytes.Buffer
			cmd := newSchLibLayoutCmd(&stdout, &stderr)
			cmd.SetArgs([]string{"--from", from, "--out", out})
			if err := cmd.Execute(); err == nil {
				t.Fatal("missing measurement accepted")
			}
			remaining, _ := os.ReadFile(out)
			if string(remaining) != "previous-good-output" {
				t.Fatal("failed planning overwrote output")
			}
		})
	}
	for _, edit := range []func([]byte) []byte{
		func(b []byte) []byte { return append(b, []byte("{}")...) },
		func(b []byte) []byte {
			return bytes.Replace(b, []byte(`"schemaVersion": 1,`), []byte(`"schemaVersion": 1, "schemaVersion": 1,`), 1)
		},
		func(b []byte) []byte { return bytes.Replace(b, []byte(`"rotation":`), []byte(`"Rotation":`), 1) },
	} {
		if _, err := decodeLibLayout(edit(raw)); err == nil {
			t.Fatal("ambiguous JSON accepted")
		}
	}
}

func TestLibLayoutSearchBudgetPreservesOutput(t *testing.T) {
	raw, err := os.ReadFile("testdata/lib-layout/ams1117.json")
	if err != nil {
		t.Fatal(err)
	}
	var input map[string]any
	_ = json.Unmarshal(raw, &input)
	input["maxCandidates"] = 1
	data, _ := json.Marshal(input)
	dir := t.TempDir()
	from, out := filepath.Join(dir, "input.json"), filepath.Join(dir, "result.json")
	_ = os.WriteFile(from, data, 0600)
	_ = os.WriteFile(out, []byte("previous-good-output"), 0600)
	var stdout, stderr bytes.Buffer
	cmd := newSchLibLayoutCmd(&stdout, &stderr)
	cmd.SetArgs([]string{"--from", from, "--out", out})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "budget exhausted") {
		t.Fatalf("expected bounded failure, got %v", err)
	}
	remaining, _ := os.ReadFile(out)
	if string(remaining) != "previous-good-output" {
		t.Fatal("budget failure overwrote result")
	}
}

func TestLibRouteMergesOverlapsWithoutLosingBranches(t *testing.T) {
	wire := func(net string, a, b [2]float64) powerLayoutWire {
		return powerLayoutWire{Net: net, Points: [][2]float64{a, b}}
	}
	for _, vertical := range []bool{false, true} {
		pt := func(x, y float64) [2]float64 {
			if vertical {
				return [2]float64{y, x}
			}
			return [2]float64{x, y}
		}
		existing := []powerLayoutWire{wire("VOUT", pt(0, 0), pt(10, 0)), wire("VOUT", pt(40, 0), pt(30, 0)), wire("VOUT", pt(15, 0), pt(15, 10)), wire("OTHER", pt(0, 0), pt(40, 0))}
		before, _ := json.Marshal(existing)
		out := libAppendRoute(existing, []powerLayoutWire{wire("VOUT", pt(5, 0), pt(35, 0))})
		after, _ := json.Marshal(existing)
		if !bytes.Equal(before, after) {
			t.Fatal("mutated candidate parent")
		}
		if len(out) != 5 {
			t.Fatalf("expected non-overlapping trunks split at the preserved real branch, got %+v", out)
		}
		for _, net := range []string{"VOUT", "OTHER"} {
			var trunk []powerLayoutWire
			for _, w := range out {
				if w.Net == net && w.Points[0] != pt(15, 10) && w.Points[1] != pt(15, 10) {
					trunk = append(trunk, w)
				}
			}
			got, e := drawingEdges(trunk)
			want, _ := drawingEdges([]powerLayoutWire{wire(net, pt(0, 0), pt(40, 0))})
			if e != nil || !reflect.DeepEqual(got, want) {
				t.Fatal("lost trunk coverage or merged across nets", out, e)
			}
		}
		if !reflect.DeepEqual(out[0], existing[2]) {
			t.Fatal("lost branch")
		}
		out[0].Points[0] = pt(99, 99)
		after, _ = json.Marshal(existing)
		if !bytes.Equal(before, after) {
			t.Fatal("shared output point storage with parent")
		}
	}
}

func TestLibLayoutCLIReportsSummarySeparatelyFromJSON(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := newSchLibLayoutCmd(&stdout, &stderr)
	cmd.SetArgs([]string{"--from", "testdata/lib-layout/ams1117.json"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	var output schCompositionSource
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("stdout is not clean JSON: %v", err)
	}
	if len(output.Modules) != 1 || !strings.Contains(stderr.String(), "1 modules, 4 parts,") || !strings.Contains(stderr.String(), "compose source ready") {
		t.Fatal("missing result summary")
	}
}
