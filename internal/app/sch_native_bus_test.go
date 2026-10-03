package app

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

func busLaneRun(t *testing.T, style string, opts func(*SchematicAestheticsOptions)) *SchematicLayoutResult {
	t.Helper()
	ids, p := busLaneFixture()
	policies := map[string]string{"GND": "local_ground", "D0": "module_port", "D1": "module_port", "D2": "module_port", "D3": "module_port"}
	roles := map[string]string{"GND": "ground", "D0": "signal", "D1": "signal", "D2": "signal", "D3": "signal"}
	res := &SchematicLayoutResult{ComponentIDs: ids, PinStates: map[string]map[string]string{"core": nil}, Placements: p.Placements, Wires: p.Wires, Flags: p.Flags}
	o := &SchematicAestheticsOptions{Style: style}
	if opts != nil {
		opts(o)
	}
	budget := 0
	return applySchematicAesthetics(SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: "core", Aesthetics: o}, res, policies, roles, &budget)
}

// The synthetic D0..D3 fixture: balanced aligns the lane AND draws one
// native bus beside the label column; members keep wire + label taps.
func TestNativeBusPlanSyntheticLane(t *testing.T) {
	out := busLaneRun(t, "balanced", nil)
	if len(out.Buses) != 1 {
		t.Fatalf("buses %+v (lanes %+v)", out.Buses, out.Aesthetics.BusLanes)
	}
	b := out.Buses[0]
	if b.BusName != "D[0:3]" || b.Kind != "indexed" || strings.Join(b.Members, ",") != "D0,D1,D2,D3" || b.Status != "planned" {
		t.Fatalf("bus identity %+v", b)
	}
	plan := powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags, Buses: out.Buses}
	if err := validateSchNativeBuses(&plan); err != nil {
		t.Fatal(err)
	}
	// trunk: vertical, beside (right of) every label body/text, ≥15 units past
	trunk := b.Line[0]
	if trunk[0] != trunk[2] || len(b.Line) != 5 {
		t.Fatalf("want a vertical trunk + 4 comb branches: %v", b.Line)
	}
	for _, f := range out.Flags {
		if f.Kind != "net_port_bi" {
			continue
		}
		for _, box := range schTerminalMarkerBoxes(f) {
			if trunk[0]-box.MaxX < 15 {
				t.Fatalf("trunk x=%g only %g past label %s", trunk[0], trunk[0]-box.MaxX, f.Net)
			}
		}
	}
	// the bus touches no wire or marker lead and no pin
	for _, s := range b.segments() {
		for _, f := range out.Flags {
			x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
			if plSegmentsMeet(s[0], s[1], [2]float64{f.PinX, f.PinY}, [2]float64{x, y}) {
				t.Fatalf("bus segment %v touches lead %s", s, f.Net)
			}
		}
	}
	// N3 credits the native bus, and the bus member check passes.
	if out.Aesthetics.MetricsAfter["N3"] != 100 || out.Aesthetics.NativeBuses != 1 {
		t.Fatalf("N3 %v nativeBuses %d", out.Aesthetics.MetricsAfter["N3"], out.Aesthetics.NativeBuses)
	}
	raw, _ := json.Marshal(out)
	snap, err := schaes.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	checks := schaes.CheckBuses(snap)
	if len(checks) != 1 || !checks[0].OK || len(checks[0].Findings) != 0 {
		t.Fatalf("bus member check %+v", checks)
	}
	// Rendered as a thick line with the name.
	svg, err := RenderSchematicLayoutSVG(SchematicRenderInput{SchemaVersion: 1, Zones: []SchematicRenderZone{{ID: "bus", Title: "bus", Layout: out}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(svg, []byte(`class="bus"`)) || !bytes.Contains(svg, []byte(">D[0:3]<")) {
		t.Fatal("layout-render does not draw the native bus and its name")
	}
	// Deterministic.
	again := busLaneRun(t, "balanced", nil)
	a, _ := json.Marshal(again.Buses)
	bb, _ := json.Marshal(out.Buses)
	if !bytes.Equal(a, bb) {
		t.Fatal("native bus plan is not deterministic")
	}
}

func TestNativeBusProfilesAndFallbacks(t *testing.T) {
	if out := busLaneRun(t, "functional", nil); len(out.Buses) != 0 {
		t.Fatalf("functional draws no native bus by default: %+v", out.Buses)
	}
	on := true
	if out := busLaneRun(t, "functional", func(o *SchematicAestheticsOptions) { o.NativeBus = &on }); len(out.Buses) != 1 {
		t.Fatalf("explicit --native-bus on functional: %+v", out.Aesthetics.BusLanes)
	}
	if out := busLaneRun(t, "precision", nil); len(out.Buses) != 1 {
		t.Fatalf("precision draws the lane as a bus: %+v", out.Aesthetics.BusLanes)
	}
	// Host without the bus API: virtual lane only, reason recorded.
	out := busLaneRun(t, "balanced", func(o *SchematicAestheticsOptions) { o.HostBusAPI = "absent" })
	if len(out.Buses) != 0 || out.Aesthetics.BusLanes[0].Native == nil || out.Aesthetics.BusLanes[0].Native.Status != "fallback-virtual" {
		t.Fatalf("absent host: %+v %+v", out.Buses, out.Aesthetics.BusLanes)
	}
	if out.Aesthetics.MetricsAfter["N3"] < 90 {
		t.Fatalf("fallback keeps the aligned virtual lane: N3 %v", out.Aesthetics.MetricsAfter["N3"])
	}
	// V4 / unprobed host: still planned, marked host-unverified.
	out = busLaneRun(t, "balanced", func(o *SchematicAestheticsOptions) { o.HostBusAPI = "unverified" })
	if len(out.Buses) != 1 || out.Buses[0].Status != "host-unverified" || out.Aesthetics.BusLanes[0].Native.Status != "host-unverified" {
		t.Fatalf("unverified host: %+v", out.Buses)
	}
	out = busLaneRun(t, "balanced", func(o *SchematicAestheticsOptions) { o.HostBusAPI = "maybe" })
	if out.Aesthetics.Status != "skipped" {
		t.Fatalf("unknown hostBusApi must be refused: %+v", out.Aesthetics)
	}
}

func TestNativeBusKeepoutsAndMembersRefuse(t *testing.T) {
	out := busLaneRun(t, "balanced", nil)
	base := powerLayoutPlan{Placements: out.Placements, Wires: out.Wires, Flags: out.Flags}
	good := out.Buses[0]
	try := func(name string, mutate func(p *powerLayoutPlan, b *SchematicNativeBus)) {
		t.Helper()
		p := base
		p.Flags = append([]powerLayoutFlag(nil), base.Flags...)
		p.Wires = clonePowerLayoutWires(base.Wires)
		b := cloneNativeBuses([]SchematicNativeBus{good})[0]
		mutate(&p, &b)
		p.Buses = []SchematicNativeBus{b}
		if err := validateSchNativeBuses(&p); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	try("member without label", func(p *powerLayoutPlan, b *SchematicNativeBus) {
		var keep []powerLayoutFlag
		for _, f := range p.Flags {
			if f.Net != "D2" {
				keep = append(keep, f)
			}
		}
		p.Flags = keep
	})
	try("member on no pin", func(p *powerLayoutPlan, b *SchematicNativeBus) { b.Members = append(b.Members, "D9") })
	try("crosses the core body", func(p *powerLayoutPlan, b *SchematicNativeBus) { b.Line = [][]float64{{0, -30, 0, 30}} })
	try("touches a wire", func(p *powerLayoutPlan, b *SchematicNativeBus) {
		t0 := b.Line[0]
		p.Wires = append(p.Wires, powerLayoutWire{Net: "X", Points: [][2]float64{{t0[0] - 20, 0}, {t0[0] + 20, 0}}})
	})
	try("diagonal", func(p *powerLayoutPlan, b *SchematicNativeBus) { b.Line = [][]float64{{200, 0, 210, 10}} })
	try("off grid", func(p *powerLayoutPlan, b *SchematicNativeBus) {
		b.Line = [][]float64{{b.Line[0][0] + 2, b.Line[0][1], b.Line[0][2] + 2, b.Line[0][3]}}
	})
	try("branch off the trunk", func(p *powerLayoutPlan, b *SchematicNativeBus) {
		b.Line = append(b.Line, []float64{b.Line[0][0] + 50, 0, b.Line[0][0] + 60, 0})
	})
	try("host id in a plan", func(p *powerLayoutPlan, b *SchematicNativeBus) { b.PrimitiveID = "abc" })
}

// esp32 zones: at balanced/precision no real esp32 group forms a complete
// lane (U0_UART has 2 members and its labels are not aligned), so no native
// bus is drawn and every candidate lane records why.
func TestNativeBusEsp32UARTZone(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "esp32-v05", "zones-pwr.json"))
	if err != nil {
		t.Fatal(err)
	}
	in, err := decodeSchematicZonesInput(raw)
	if err != nil {
		t.Fatal(err)
	}
	var zone SchematicZone
	for _, z := range in.Zones {
		if z.ID == "UART" {
			zone = z
		}
	}
	keep := map[string]bool{}
	for _, id := range zone.ComponentIDs {
		keep[id] = true
	}
	var comps []SchematicLayoutComponent
	used := map[string]bool{}
	for _, c := range in.Components {
		if keep[c.ID] {
			comps = append(comps, c)
			for _, q := range c.Measurement.Pins {
				used[q.Net] = true
			}
		}
	}
	policies := map[string]string{}
	for n, p := range in.NetPolicies {
		if used[n] {
			policies[n] = p
		}
	}
	in.Components, in.NetPolicies, in.Zones = comps, policies, []SchematicZone{zone}
	in.Attachments = nil
	for _, style := range []string{"balanced", "precision"} {
		in.Aesthetics = &SchematicAestheticsOptions{Style: style}
		out, err := PlanSchematicZones(in)
		if err != nil {
			t.Fatalf("%s: %v", style, err)
		}
		l := out.Zones[0].Layout
		plan := powerLayoutPlan{Placements: l.Placements, Wires: l.Wires, Flags: l.Flags, Buses: l.Buses}
		if err := validateSchNativeBuses(&plan); err != nil {
			t.Fatalf("%s: emitted bus fails the composition gate: %v", style, err)
		}
		lanes := 0
		for _, lane := range l.Aesthetics.BusLanes {
			if lane.Markers < 2 || lane.Kind == "usb" || lane.Kind == "mipi" {
				continue // pairs are never buses
			}
			lanes++
			if lane.Native == nil || (lane.Native.Status != "planned" && lane.Native.Reason == "") {
				t.Fatalf("%s lane %s without a native bus decision: %+v", style, lane.Group, lane.Native)
			}
			if lane.Native.Status == "planned" && len(l.Buses) == 0 {
				t.Fatalf("%s lane %s planned but no bus emitted", style, lane.Group)
			}
		}
		if lanes == 0 {
			t.Fatalf("%s: the UART zone should expose its U0_UART lane", style)
		}
	}
}

// compose: module buses are translated with the module, validated, and the
// playbook creates them after the wire-tree check, before saving.
func TestNativeBusComposePlaybookStepAndGuard(t *testing.T) {
	p, env := composeApplyFixture(t, true)
	p.Layout.Buses = []SchematicNativeBus{{BusName: "D[0:3]", Line: [][]float64{{400, 300, 400, 360}}, Members: []string{"D0", "D1"}}}
	dir := t.TempDir()
	p.BusJournal = filepath.Join(dir, "bus.json")
	pb, err := schCompositionPlaybook(p, composeApplyBytes(t, env), false)
	if err != nil {
		t.Fatal(err)
	}
	iBus, step := composeStep(t, pb, "native-buses")
	iTree, _ := composeStep(t, pb, "wire-tree-check")
	iSave, _ := composeStep(t, pb, "save-composition")
	if !(iTree < iBus && iBus < iSave) || step.Run != "sch bus apply" || step.Flags["journal"] != p.BusJournal {
		t.Fatalf("bus step order/flags: tree %d bus %d save %d %+v", iTree, iBus, iSave, step)
	}
	raw, err := base64.StdEncoding.DecodeString(step.Flags["buses-b64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	var got []SchematicNativeBus
	if err := json.Unmarshal(raw, &got); err != nil || len(got) != 1 || got[0].BusName != "D[0:3]" {
		t.Fatalf("bus payload %s %v", raw, err)
	}
	_, verify := composeStep(t, pb, "verify-all-pins-nets-nc")
	if verify.ExpectSchematic.Drawing.MaxBuses != 1 {
		t.Fatalf("drawing must allow exactly the composition's buses: %d", verify.ExpectSchematic.Drawing.MaxBuses)
	}

	// A rebuild must never clear a user bus: refused at compile time.
	p2, env2 := composeApplyFixture(t, false)
	p2.BusJournal = filepath.Join(dir, "bus2.json")
	page := env2["result"].(map[string]any)
	composeFixturePageInventory(page)
	userBus := map[string]any{"primitiveId": "user-bus", "BusName": "ADDR[0:3]", "Line": []any{600.0, 100.0, 600.0, 200.0}, "Color": nil, "LineWidth": 1.0, "LineType": 0.0}
	inject := func(e map[string]any) []byte {
		b, _ := json.Marshal(e)
		var c map[string]any
		_ = json.Unmarshal(b, &c)
		c["result"].(map[string]any)["pagePrimitives"].(map[string]any)["buses"] = []any{userBus}
		c["result"].(map[string]any)["connectivitySummary"].(map[string]any)["buses"] = 1.0
		out, _ := json.Marshal(c)
		return out
	}
	if _, err := schCompositionPlaybook(p2, inject(env2), true); err == nil || !strings.Contains(err.Error(), "did not create") {
		t.Fatalf("user bus would be cleared: %v", err)
	}
	// The same bus, journalled by pcbpilot with identical name + geometry
	// (host out-and-back readback order), may be cleared and recreated.
	j := &schBusJournal{Buses: []SchematicNativeBus{{PrimitiveID: "user-bus", BusName: "ADDR[0:3]", Line: [][]float64{{600, 200, 600, 100}}}}}
	if err := writeSchBusJournal(p2.BusJournal, j); err != nil {
		t.Fatal(err)
	}
	if _, err := schCompositionPlaybook(p2, inject(env2), true); err != nil {
		t.Fatalf("journalled bus must be replaceable: %v", err)
	}
}

func TestSchSameBusLineOutAndBack(t *testing.T) {
	branches := [][]float64{{620, 260, 780, 260}, {620, 260, 620, 320}}
	flat := [][]float64{{620, 320, 620, 260, 780, 260, 620, 260}}
	if !schSameBusLine(branches, flat) {
		t.Fatal("V3 out-and-back readback must equal the two branches")
	}
	if schSameBusLine(branches, [][]float64{{620, 320, 620, 260, 780, 260}}) == false {
		t.Fatal("a plain L path is the same segment set")
	}
	if schSameBusLine(branches, [][]float64{{620, 320, 620, 260, 790, 260}}) {
		t.Fatal("a longer branch is a different bus")
	}
}

// ---------------------------------------------------------------------------
// sch bus apply against a fake host that reads buses back as ONE flat
// out-and-back path (V3 3.2.149 behaviour, 2026-10-01).

type fakeBusHost struct {
	version string
	members map[string]string
	snap    *schaes.Snapshot
	buses   map[string]SchematicNativeBus
	next    int
	creates int
	deletes [][]string
}

func newFakeBusHost() *fakeBusHost {
	h := &fakeBusHost{version: "3.2.149.88089769", buses: map[string]SchematicNativeBus{},
		members: map[string]string{"sch_PrimitiveBus.create": "function", "sch_PrimitiveBus.getAll": "function", "sch_PrimitiveBus.get": "function", "sch_PrimitiveBus.delete": "function"}}
	_, p := busLaneFixture()
	s := &schaes.Snapshot{Source: "components-list", HasMarkers: true, HasPins: true, HasWires: true}
	part := schaes.Part{Ref: "U1"}
	for _, q := range p.Placements[0].Pins {
		part.Pins = append(part.Pins, schaes.Pin{Number: q.Number, Net: q.Net, X: q.X, Y: q.Y})
	}
	s.Parts = []schaes.Part{part}
	for _, f := range p.Flags {
		x, y := endpointFor(f.PinX, f.PinY, f.Offset, f.Direction)
		kind := schaes.KindNetPort
		if f.Kind == "ground" {
			kind = schaes.KindGround
		}
		s.Markers = append(s.Markers, schaes.Marker{Kind: kind, Net: f.Net, Anchor: schaes.Pt{X: x, Y: y}})
	}
	h.snap = s
	return h
}

// outAndBack flattens a trunk + comb into the host's single flat path.
func outAndBack(line [][]float64) [][]float64 {
	trunk := line[0]
	branchAt := map[[2]float64][2]float64{}
	for _, b := range line[1:] {
		branchAt[[2]float64{b[0], b[1]}] = [2]float64{b[len(b)-2], b[len(b)-1]}
	}
	var flat []float64
	for j := 0; j+1 < len(trunk); j += 2 {
		p := [2]float64{trunk[j], trunk[j+1]}
		flat = append(flat, p[0], p[1])
		if e, ok := branchAt[p]; ok {
			flat = append(flat, e[0], e[1], p[0], p[1])
			delete(branchAt, p)
		}
	}
	// branches in the trunk interior: walk to them along the trunk
	keys := make([][2]float64, 0, len(branchAt))
	for k := range branchAt {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(a, b int) bool {
		return keys[a][1] < keys[b][1] || (keys[a][1] == keys[b][1] && keys[a][0] < keys[b][0])
	})
	for _, k := range keys {
		e := branchAt[k]
		flat = append(flat, k[0], k[1], e[0], e[1], k[0], k[1])
	}
	return [][]float64{flat}
}

func (h *fakeBusHost) Probe(paths []string) (map[string]string, string, error) {
	return h.members, h.version, nil
}

func (h *fakeBusHost) Page() (*schaes.Snapshot, string, string, error) {
	s := *h.snap
	s.Buses = nil
	s.HasBuses = true
	ids := make([]string, 0, len(h.buses))
	for id := range h.buses {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s.Buses = append(s.Buses, busToSchaes(h.buses[id]))
	}
	return &s, "P1", "DOC1", nil
}

func (h *fakeBusHost) Create(payload map[string]any) (map[string]any, error) {
	h.creates++
	h.next++
	id := fmt.Sprintf("bus%d", h.next)
	var line [][]float64
	switch l := payload["line"].(type) {
	case []float64:
		line = [][]float64{l}
	case [][]float64:
		line = l
	}
	stored := SchematicNativeBus{PrimitiveID: id, BusName: payload["busName"].(string), Line: outAndBack(line)}
	h.buses[id] = stored
	return map[string]any{"primitiveId": id, "verified": schSameBusLine(stored.Line, line)}, nil
}

func (h *fakeBusHost) Delete(ids []string) (map[string]any, error) {
	h.deletes = append(h.deletes, append([]string(nil), ids...))
	for _, id := range ids {
		delete(h.buses, id)
	}
	return map[string]any{"verified": true, "survivedIds": []any{}}, nil
}

func syntheticBusPlan(t *testing.T) []SchematicNativeBus {
	t.Helper()
	out := busLaneRun(t, "balanced", nil)
	if len(out.Buses) != 1 {
		t.Fatal("fixture bus missing")
	}
	// The fake page holds the fixture's members and labels; apply checks
	// members (pins + own labels), not the plan's offline geometry.
	return out.Buses
}

func TestSchBusApplyJournalReplaceRoundTrip(t *testing.T) {
	plan := syntheticBusPlan(t)
	journal := filepath.Join(t.TempDir(), "j.json")
	h := newFakeBusHost()
	// a user bus elsewhere on the page: never touched
	h.buses["user1"] = SchematicNativeBus{PrimitiveID: "user1", BusName: "ADDR[0:3]", Line: [][]float64{{600, 100, 600, 200}}}

	rep, err := runSchBusApply(h, plan, journal, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Created) != 1 || len(rep.Deleted) != 0 || len(h.buses) != 2 || rep.UserBusesKept[0] != "user1" || !strings.Contains(rep.Verification, "live-verified") {
		t.Fatalf("first apply %+v buses %v", rep, h.buses)
	}
	first := rep.Created[0].PrimitiveID
	j, _ := readSchBusJournal(journal)
	if len(j.Buses) != 1 || j.Buses[0].PrimitiveID != first || j.Project != "P1" || j.Doc != "DOC1" {
		t.Fatalf("journal %+v", j)
	}
	// Re-run: the journalled bus (matched by id AND name + out-and-back
	// geometry) is replaced, never duplicated; the user bus stays.
	rep, err = runSchBusApply(h, plan, journal, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Deleted) != 1 || rep.Deleted[0] != first || len(rep.Created) != 1 || len(h.buses) != 2 {
		t.Fatalf("re-run %+v buses %v", rep, h.buses)
	}
	if _, ok := h.buses["user1"]; !ok {
		t.Fatal("user bus deleted")
	}
	for _, d := range h.deletes {
		for _, id := range d {
			if id == "user1" {
				t.Fatal("user bus id sent to delete")
			}
		}
	}
	second := rep.Created[0].PrimitiveID
	j, _ = readSchBusJournal(journal)
	if len(j.Buses) != 1 || j.Buses[0].PrimitiveID != second {
		t.Fatalf("journal after replace %+v", j)
	}
	// A journalled bus edited on the page is not ours any more: refuse
	// before any write.
	edited := h.buses[second]
	edited.Line = [][]float64{{edited.Line[0][0] + 5, edited.Line[0][1], edited.Line[0][2] + 5, edited.Line[0][3]}}
	h.buses[second] = edited
	creates := h.creates
	if _, err := runSchBusApply(h, plan, journal, false); err == nil || !strings.Contains(err.Error(), "changed on the page") || h.creates != creates {
		t.Fatalf("edited journalled bus: %v (creates %d→%d)", err, creates, h.creates)
	}
	h.buses[second] = SchematicNativeBus{PrimitiveID: second, BusName: plan[0].BusName, Line: outAndBack(plan[0].Line)}
	// Rollback deletes exactly the journalled bus.
	rep, err = runSchBusApply(h, nil, journal, true)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Status != "rolled-back" || len(rep.Deleted) != 1 || rep.Deleted[0] != second || len(h.buses) != 1 {
		t.Fatalf("rollback %+v buses %v", rep, h.buses)
	}
	j, _ = readSchBusJournal(journal)
	if len(j.Buses) != 0 {
		t.Fatalf("journal after rollback %+v", j)
	}
}

func TestSchBusApplyNoDuplicateOfIdenticalUserBus(t *testing.T) {
	plan := syntheticBusPlan(t)
	h := newFakeBusHost()
	h.buses["mine-by-hand"] = SchematicNativeBus{PrimitiveID: "mine-by-hand", BusName: plan[0].BusName, Line: outAndBack(plan[0].Line)}
	rep, err := runSchBusApply(h, plan, filepath.Join(t.TempDir(), "j.json"), false)
	if err != nil {
		t.Fatal(err)
	}
	if h.creates != 0 || len(rep.NotDuplicated) != 1 || len(h.buses) != 1 {
		t.Fatalf("identical user bus duplicated: %+v", rep)
	}
}

func TestSchBusApplyFallbackAndGuards(t *testing.T) {
	plan := syntheticBusPlan(t)
	dir := t.TempDir()
	// API absent: virtual lane fallback, no write, no journal.
	h := newFakeBusHost()
	h.members["sch_PrimitiveBus.create"] = "undefined"
	rep, err := runSchBusApply(h, plan, filepath.Join(dir, "a.json"), false)
	if err != nil || rep.Status != "fallback-virtual-bus" || h.creates != 0 {
		t.Fatalf("absent API: %+v %v", rep, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "a.json")); !os.IsNotExist(err) {
		t.Fatal("fallback must not write a journal")
	}
	// V4: created and read back, reported host-unverified.
	h = newFakeBusHost()
	h.version = "4.1.60"
	rep, err = runSchBusApply(h, plan, filepath.Join(dir, "b.json"), false)
	if err != nil || h.creates != 1 || !strings.Contains(rep.Verification, "host-unverified") {
		t.Fatalf("V4: %+v %v", rep, err)
	}
	// A member without its own label: refused before any write.
	h = newFakeBusHost()
	var keep []schaes.Marker
	for _, m := range h.snap.Markers {
		if m.Net != "D1" {
			keep = append(keep, m)
		}
	}
	h.snap.Markers = keep
	if _, err := runSchBusApply(h, plan, filepath.Join(dir, "c.json"), false); err == nil || !strings.Contains(err.Error(), "member-without-label") || h.creates != 0 {
		t.Fatalf("label-less member: %v creates %d", err, h.creates)
	}
}

func TestSchBusApplyDryRunPayloads(t *testing.T) {
	plan := syntheticBusPlan(t)
	dir := t.TempDir()
	planPath := filepath.Join(dir, "plan.json")
	raw, _ := json.Marshal(map[string]any{"layout": map[string]any{"buses": plan}})
	if err := os.WriteFile(planPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "list.json")
	listRaw, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"count": 1, "buses": []any{map[string]any{"primitiveId": "u9", "busName": "X[0:1]", "line": []float64{900, 0, 900, 40}}}}})
	_ = os.WriteFile(list, listRaw, 0o644)
	var out, errOut bytes.Buffer
	window := ""
	c := newSchBusApplyCmd(nil, &window, &out, &errOut)
	c.SetArgs([]string{"--plan", planPath, "--dry-run", "--bus-list", list})
	if err := c.Execute(); err != nil {
		t.Fatal(err, errOut.String())
	}
	var rep schBusApplyReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Status != "dry-run" || len(rep.Creates) != 1 || rep.Creates[0]["busName"] != "D[0:3]" || len(rep.UserBusesKept) != 1 || rep.UserBusesKept[0] != "u9" {
		t.Fatalf("dry-run %+v", rep)
	}
	line, _ := json.Marshal(rep.Creates[0]["line"])
	if !strings.HasPrefix(string(line), "[[") {
		t.Fatalf("trunk + branches must be sent as nested polylines: %s", line)
	}
}

// A module carrying a native bus composes onto the sheet: the bus moves with
// the module, the frame encloses it, and the composition gate re-validates it.
func TestNativeBusComposeModuleTranslatesAndFrames(t *testing.T) {
	out := busLaneRun(t, "balanced", nil)
	if len(out.Buses) != 1 {
		t.Fatal("fixture bus missing")
	}
	src := schCompositionSource{SchemaVersion: 1, Sheet: layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 827}}
	src.Connectivity = connectivity.Document{SchemaVersion: "1.4", ProjectID: "P", DocumentID: "DOC"}
	d := &src.Connectivity
	comp := connectivity.Component{ID: "stable-U1", Ref: "U1", Device: connectivity.Device{LibraryUUID: "11111111111111111111111111111111", UUID: "22222222222222222222222222222222", Name: "bus-core"}}
	seenNet := map[string]bool{}
	for _, q := range out.Placements[0].Pins {
		comp.Pins = append(comp.Pins, connectivity.Pin{Number: q.Number, Name: q.Net})
		if !seenNet[q.Net] {
			seenNet[q.Net] = true
			d.Nets = append(d.Nets, connectivity.Net{ID: "net-" + q.Net, Name: q.Net})
		}
		d.Connections = append(d.Connections, connectivity.Connection{ComponentID: comp.ID, PinNumber: q.Number, NetID: "net-" + q.Net, Kind: "pin_net"})
	}
	d.Components = []connectivity.Component{comp}
	d.Modules = []connectivity.Module{{ID: "bus-module", Name: "BUS", CoreComponents: []string{comp.ID}}}
	placement := out.Placements[0]
	placement.Designator = "U1"
	src.Modules = []schCompositionModule{{ID: "bus-module", Title: "BUS", Placements: []powerLayoutPlacement{placement}, Wires: out.Wires, Flags: out.Flags, Buses: cloneNativeBuses(out.Buses)}}
	plan, err := planSchComposition(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Layout.Buses) != 1 {
		t.Fatalf("composed buses %+v", plan.Layout.Buses)
	}
	dx := plan.Layout.Placements[0].X - out.Placements[0].X
	dy := plan.Layout.Placements[0].Y - out.Placements[0].Y
	want := cloneNativeBuses(out.Buses)
	translateNativeBuses(want, dx, dy)
	if !schSameBusLine(want[0].Line, plan.Layout.Buses[0].Line) {
		t.Fatalf("bus not translated with its module: %v vs %v", want[0].Line, plan.Layout.Buses[0].Line)
	}
	for _, box := range schNativeBusObstacles(plan.Layout.Buses[0]) {
		if !boxInside(box, plan.Layout.Frames[0].Rect) {
			t.Fatalf("frame %+v clips bus box %+v", plan.Layout.Frames[0].Rect, box)
		}
	}
	if err := validateSchNativeBuses(&plan.Layout); err != nil {
		t.Fatal(err)
	}
	// A module bus whose member lost its label is refused by compose.
	bad := src
	bad.Modules = []schCompositionModule{src.Modules[0]}
	bad.Modules[0].Buses = cloneNativeBuses(out.Buses)
	bad.Modules[0].Buses[0].Members = append(bad.Modules[0].Buses[0].Members, "GND")
	if _, err := planSchComposition(bad); err == nil {
		t.Fatal("compose accepted a bus member without its own label")
	}
}

func TestSchBusApplyRefusesTouchingBus(t *testing.T) {
	plan := syntheticBusPlan(t)
	h := newFakeBusHost()
	t0 := plan[0].Line[0]
	h.snap.Wires = append(h.snap.Wires, schaes.Wire{Net: "X", Pts: []schaes.Pt{{X: t0[0] - 20, Y: 0}, {X: t0[0] + 20, Y: 0}}})
	if _, err := runSchBusApply(h, plan, filepath.Join(t.TempDir(), "j.json"), false); err == nil || !strings.Contains(err.Error(), "bus-touches-wire") || h.creates != 0 {
		t.Fatalf("touching bus: %v creates %d", err, h.creates)
	}
}
