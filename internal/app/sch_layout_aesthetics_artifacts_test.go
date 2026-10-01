package app

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

// TestSchAesPhaseBArtifacts regenerates the Phase B before/after evidence
// (layouts, SVG previews, metric table) for every offline schematic layout
// fixture. Opt-in: SCHAES_PHASEB_OUT=<dir> go test ./internal/app -run
// TestSchAesPhaseBArtifacts -timeout 60m. Previews come from the repo's own
// offline renderer (sch layout-render); they are simplified symbols, not
// official EasyEDA graphics.
func TestSchAesPhaseBArtifacts(t *testing.T) {
	dir := os.Getenv("SCHAES_PHASEB_OUT")
	if dir == "" {
		t.Skip("set SCHAES_PHASEB_OUT to regenerate docs/reviews/2026-10-schematic-aesthetics/phaseB")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	style := os.Getenv("SCHAES_PHASEB_STYLE")
	if style == "" {
		style = "balanced"
	}
	type row struct {
		name            string
		before, after   *SchematicLayoutResult
		report          *SchematicAestheticsReport
		beforeM, afterM map[string]float64
	}
	var rows []row
	add := func(name string, before, after *SchematicLayoutResult) {
		r := row{name: name, before: before, after: after, report: after.Aesthetics}
		r.beforeM, r.afterM = schAesMeasureLayout(t, before, style), schAesMeasureLayout(t, after, style)
		rows = append(rows, r)
		for tag, l := range map[string]*SchematicLayoutResult{"before": before, "after": after} {
			in := SchematicRenderInput{SchemaVersion: 1, Title: tag + " · " + style, Zones: []SchematicRenderZone{{ID: name, Title: name, Layout: l}}}
			// Previews are complete layouts, not diagnostics: prove it first.
			if err := validateCompleteLayoutPreview(in); err != nil {
				t.Fatalf("%s %s preview completeness: %v", name, tag, err)
			}
			svg, err := RenderSchematicLayoutSVG(in)
			if err != nil {
				t.Fatalf("%s %s render: %v", name, tag, err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+"-"+tag+".svg"), svg, 0o644); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.MarshalIndent(l, "", " ")
			if err := os.WriteFile(filepath.Join(dir, name+"-"+tag+".layout.json"), raw, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 1. AMS1117 lib-layout (measured POWER module).
	{
		src := ams1117LibLayoutSource(t)
		base, err := planLibLayout(src)
		if err != nil {
			t.Fatal(err)
		}
		src.Aesthetics = &SchematicAestheticsOptions{Style: style}
		var reps []libLayoutAestheticsReport
		out, err := planLibLayoutWithReports(src, &reps)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]string{}
		for _, c := range base.Connectivity.Components {
			ids[c.Ref] = c.ID
		}
		mk := func(m schCompositionModule) *SchematicLayoutResult {
			return &SchematicLayoutResult{SchemaVersion: 1, ComponentIDs: ids, PinStates: map[string]map[string]string{}, Placements: m.Placements, Wires: m.Wires, Flags: m.Flags}
		}
		after := mk(out.Modules[0])
		after.Aesthetics = reps[0].Report
		add("ams1117-lib-layout", mk(base.Modules[0]), after)
	}
	// 2. In-repo zone fixtures (layout-plan).
	zoneFixtures := []struct {
		name string
		in   SchematicLayoutInput
	}{{"standalone", standaloneLayoutFixture()}, {"buck-zone", buckZoneFixture()}, {"mcu-zone", mcuZoneFixture()}}
	for _, f := range zoneFixtures {
		in := f.in
		in.MaxCandidates = 200000
		base, err := PlanSchematicLayout(in)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		in.Aesthetics = &SchematicAestheticsOptions{Style: style}
		out, err := PlanSchematicLayout(in)
		if err != nil {
			t.Fatalf("%s: %v", f.name, err)
		}
		add(f.name, base, out)
	}
	// 3. esp32-v05 pages through layout-plan --zones (derived zones inputs).
	for _, page := range []string{"mcu", "pwr"} {
		raw, err := os.ReadFile(filepath.Join("testdata", "esp32-v05", "zones-"+page+".json"))
		if err != nil {
			t.Fatal(err)
		}
		in, err := decodeSchematicZonesInput(raw)
		if err != nil {
			t.Fatal(err)
		}
		base, err := PlanSchematicZones(in)
		if err != nil {
			t.Fatalf("esp32 %s: %v", page, err)
		}
		in.Aesthetics = &SchematicAestheticsOptions{Style: style}
		out, err := PlanSchematicZones(in)
		if err != nil {
			t.Fatalf("esp32 %s aes: %v", page, err)
		}
		for i := range base.Zones {
			add("esp32-"+page+"-"+strings.ToLower(base.Zones[i].ID), base.Zones[i].Layout, out.Zones[i].Layout)
		}
	}
	// 4. Hand-built B2/B3 fixtures.
	{
		roles := map[string]string{"SIG": "signal", "GND": "ground", "A": "signal", "B": "signal"}
		ids, p := longWireFixture()
		before := &SchematicLayoutResult{SchemaVersion: 1, ComponentIDs: ids, PinStates: map[string]map[string]string{}, Placements: p.Placements, Wires: clonePowerLayoutWires(p.Wires), Flags: append([]powerLayoutFlag(nil), p.Flags...)}
		after := schAesRun(t, "core", ids, map[string]string{"SIG": "module_port", "GND": "local_ground", "A": "direct", "B": "direct"}, roles, p, style)
		add("synthetic-long-wire", before, after)
	}
	{
		ids, p := busLaneFixture()
		policies := map[string]string{"GND": "local_ground", "D0": "module_port", "D1": "module_port", "D2": "module_port", "D3": "module_port"}
		roles := map[string]string{"GND": "ground", "D0": "signal", "D1": "signal", "D2": "signal", "D3": "signal"}
		before := &SchematicLayoutResult{SchemaVersion: 1, ComponentIDs: ids, PinStates: map[string]map[string]string{}, Placements: p.Placements, Flags: append([]powerLayoutFlag(nil), p.Flags...)}
		after := schAesRun(t, "core", ids, policies, roles, p, style)
		add("synthetic-bus-lane", before, after)
	}
	// Table.
	ids := []string{"W1", "W2", "W3", "W4", "W5", "W6", "W7", "W8", "L1", "L2", "L3", "L4", "L6", "L7", "N1", "N2", "N3"}
	var b strings.Builder
	fmt.Fprintf(&b, "| fixture | score | defects | %s | connectivity | check/lint | status | evals |\n|---|---|---|%s---|---|---|---|\n", strings.Join(ids, " | "), strings.Repeat("---|", len(ids)))
	cell := func(m map[string]float64, id string) string {
		v, ok := m[id]
		if !ok {
			return "–"
		}
		return fmt.Sprintf("%.0f", v)
	}
	var summary []map[string]any
	for _, r := range rows {
		var metrics []string
		for _, id := range ids {
			bv, av := cell(r.beforeM, id), cell(r.afterM, id)
			if bv == av {
				metrics = append(metrics, bv)
			} else {
				metrics = append(metrics, bv+"→"+av)
			}
		}
		check := "="
		if worse := schAesCheckWorse(r.report.CheckBefore, r.report.CheckAfter); worse != "" {
			check = "WORSE " + worse
		} else {
			keys := map[string]bool{}
			for k := range r.report.CheckBefore {
				keys[k] = true
			}
			for k := range r.report.CheckAfter {
				keys[k] = true
			}
			var diffs []string
			for k := range keys {
				if r.report.CheckAfter[k] != r.report.CheckBefore[k] {
					diffs = append(diffs, fmt.Sprintf("%s %d→%d", k, r.report.CheckBefore[k], r.report.CheckAfter[k]))
				}
			}
			sort.Strings(diffs)
			if len(diffs) > 0 {
				check = strings.Join(diffs, ", ")
			}
		}
		conn := "pin→net ✓, islands ✓"
		if !r.report.PinNetIdentical {
			conn = "PIN→NET CHANGED"
		} else if !r.report.ConnectivityIdentical {
			conn = "pin→net ✓, islands split by label (B2)"
		}
		fmt.Fprintf(&b, "| %s | %.1f→%.1f | %d→%d | %s | %v | %s | %s | %d |\n", r.name, r.beforeM["score"], r.afterM["score"], r.report.DefectsBefore, r.report.DefectsAfter,
			strings.Join(metrics, " | "), conn, check, r.report.Status, r.report.Evaluations)
		summary = append(summary, map[string]any{"fixture": r.name, "before": r.beforeM, "after": r.afterM, "report": r.report})
	}
	if err := os.WriteFile(filepath.Join(dir, "table-"+style+".md"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.MarshalIndent(summary, "", " ")
	if err := os.WriteFile(filepath.Join(dir, "summary-"+style+".json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + b.String())
}

func schAesMeasureLayout(t *testing.T, l *SchematicLayoutResult, style string) map[string]float64 {
	t.Helper()
	raw, _ := json.Marshal(struct {
		Placements   []SchematicPlacement         `json:"placements"`
		Wires        []SchematicWire              `json:"wires"`
		Flags        []SchematicMarker            `json:"flags"`
		ComponentIDs map[string]string            `json:"componentIds"`
		PinStates    map[string]map[string]string `json:"pinStates"`
	}{l.Placements, append([]SchematicWire{}, l.Wires...), append([]SchematicMarker{}, l.Flags...), l.ComponentIDs, l.PinStates})
	s, err := schaes.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	prof, err := schaes.ProfileByName(style)
	if err != nil {
		t.Fatal(err)
	}
	return schAesMetricScores(schaes.Analyze(s, &prof))
}
