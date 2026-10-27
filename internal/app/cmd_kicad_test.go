package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

// The KiCad snapshot fixture decodes with every existing snapshot reader.
func TestKicadSnapshotFixtureDecodes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	snap, err := loadBoardSnapshotFile(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Components) != 2 || snap.Outline == nil || snap.Outline.Source != "polygon" || snap.CopperLayers != 2 {
		t.Fatalf("boardSnapshot: %+v", snap)
	}
	if p := snap.Components[1].Pads[0]; p.Layer != pcbLayerMulti || !p.isThroughHole() {
		t.Fatalf("THT pad: %+v", p)
	}
	if r := snap.Rules.toPcbRules(); r.trackWidthMinMil < 5.9 || r.source != "kicad" {
		t.Fatalf("rules: %+v", r)
	}
	b, err := pcbauto.FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Parts) != 2 || len(b.Holes) != 1 || len(b.Outline) != 4 {
		t.Fatalf("pcbauto: %d parts %d holes %d outline", len(b.Parts), len(b.Holes), len(b.Outline))
	}
	pb, err := postsim.ParseBoard(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !pb.Copper || len(pb.Tracks) != 4 || len(pb.Vias) != 1 || pb.PadByPin("J1", "1") == nil || pb.PadByPin("J1", "1").Drill < 39 {
		t.Fatalf("postsim: copper %v tracks %d vias %d", pb.Copper, len(pb.Tracks), len(pb.Vias))
	}
}

func TestKicadSnapshotGates(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{
		"VCC":{"role":"power","widthMil":{"outer":20,"inner":20,"min":8}},
		"SIG_A":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6},"lengthGroup":"g","lengthTolMil":5},
		"GND":{"role":"ground","widthMil":{"outer":30,"inner":30,"min":10}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := loadDesignIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	var snap boardSnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	var gates []gateResult
	if !snapshotIntentGates(&snap, in, intent, "", nil, nil, nil, true, func(g gateResult) { gates = append(gates, g) }) {
		t.Fatal("snapshot not judged")
	}
	names := []string{}
	for _, g := range gates {
		names = append(names, g.Gate)
	}
	if strings.Join(names, ",") != "intent-widths,intent-lengths,copper-to-edge,isolation,via-current,pour-layer-mismatch,post-layout-sim" {
		t.Fatalf("gates %v", names)
	}
	// t-vcc-neck: 10 < 20 starting on R1.1 but ending 69.6 mil past its
	// copper, beyond the pad-escape zone max(3 × 20, 30) = 60 mil → violation
	// (a narrow run that merely starts at a pad is not an escape);
	// t-vcc-trunk: 10 < 20 away from any pad → violation; t-vcc-wide ok.
	g := gates[0]
	if g.Pass || len(g.Items) != 1 || !strings.HasPrefix(g.Items[0], "VCC: 2 track(s), worst 10.00 < 20.00 mil on layer 1") {
		t.Fatalf("intent-widths: %+v", g)
	}
	if !gates[1].Pass || gates[1].Detail != "the intent has no length group or pair skew" {
		t.Fatalf("intent-lengths: %+v", gates[1])
	}
	if gates[6].Pass || gates[6].Detail != "not run (needs --sim)" {
		t.Fatalf("post-layout-sim without a sim must fail: %+v", gates[6])
	}
	// The silkscreen gate reads the same snapshot (no silk texts: nothing to judge).
	_, _, font := silkTightInput(&snap, defaultSilkTightOpts())
	if sg := silkGate(&snap, font, defaultSilkTightOpts()); sg.Gate != "silkscreen" {
		t.Fatalf("silk gate %+v", sg)
	}
}

func TestKicadDRCGate(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "drc.json"))
	if err != nil {
		t.Fatal(err)
	}
	rep, err := kicad.ParseDRC(data)
	if err != nil {
		t.Fatal(err)
	}
	g := kicadDRCGate(rep, "drc.json")
	if g.Pass || g.Gate != "kicad-drc" || !strings.HasPrefix(g.Detail, "3 error(s)") {
		t.Fatalf("%+v", g)
	}
	joined := strings.Join(g.Items, "\n")
	for _, want := range []string{"clearance: 1", "unconnected_items: 1", "clearance [Track to Via] F.Cu net VCC / GND at (1000.0,-2000.0) objs u-track-1,u-via-1"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in\n%s", want, joined)
		}
	}
	applyWaivers(&g, []gateWaiver{{Gate: "kicad-drc", Match: ":", Reason: "test", By: "t"}})
	if !g.Pass {
		t.Fatal("waiver covering every item must pass the gate")
	}
	empty, _ := json.Marshal(map[string]any{"violations": []any{}, "unconnected_items": []any{}, "coordinate_units": "mm"})
	rep, _ = kicad.ParseDRC(empty)
	if g := kicadDRCGate(rep, "x"); !g.Pass {
		t.Fatalf("clean report fails: %+v", g)
	}
}

func TestCopyKicadBoard(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "a.kicad_pcb")
	for _, ext := range []string{".kicad_pcb", ".kicad_pro"} {
		if err := os.WriteFile(filepath.Join(dir, "a"+ext), []byte(ext), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dst := filepath.Join(dir, "w", "b.kicad_pcb")
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	if has, err := copyKicadBoard(src, dst); err != nil || !has {
		t.Fatal(has, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "w", "b.kicad_pro")); string(b) != ".kicad_pro" {
		t.Fatalf("pro not copied: %q", b)
	}
	_ = os.Remove(filepath.Join(dir, "a.kicad_pro"))
	if has, err := copyKicadBoard(src, dst); err != nil || has {
		t.Fatal("missing .kicad_pro: want ok, hasPro=false", has, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "w", "b.kicad_pro")); !os.IsNotExist(err) {
		t.Fatal("stale .kicad_pro left in the work copy")
	}
}

// interfUPro is KiCad's shipped interf_u demo project ("" when KiCad's demos
// are not installed next to kicad-cli or under %LOCALAPPDATA%).
func interfUPro() string {
	var cands []string
	if v := os.Getenv("PCBPILOT_KICAD_DEMOS"); v != "" {
		cands = append(cands, v)
	}
	if cli, err := kicad.KicadCLI(); err == nil {
		cands = append(cands, filepath.Join(filepath.Dir(cli), "..", "share", "kicad", "demos"))
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		m, _ := filepath.Glob(filepath.Join(la, "Programs", "KiCad", "*", "share", "kicad", "demos"))
		cands = append(cands, m...)
	}
	for _, c := range cands {
		p := filepath.Join(c, "interf_u", "interf_u.kicad_pro")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// interf_u's rules: min hole 0.5 mm, min via 0.9 mm, min annulus 0.05 mm,
// min track 0.2 mm. A synthetic project with the same rules always runs;
// the real demo file is read too when it is installed.
func TestKicadRuleFloorsRefuseBelowBoardMinimum(t *testing.T) {
	syn := filepath.Join(t.TempDir(), "b.kicad_pro")
	if err := os.WriteFile(syn, []byte(`{"board":{"design_settings":{"rules":{"min_through_hole_diameter":0.5,"min_via_diameter":0.9,"min_via_annular_width":0.05,"min_track_width":0.2}}},
	"net_settings":{"classes":[{"name":"Default","via_diameter":1.4,"via_drill":0.6},{"name":"Power","via_diameter":1.6,"via_drill":0.6}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	pros := []string{syn}
	if p := interfUPro(); p != "" {
		pros = append(pros, p)
	} else {
		t.Log("interf_u demo not installed: synthetic rules only")
	}
	refused := func(t *testing.T, r kicad.Rules, f kicadRuleFloors, subs ...string) {
		t.Helper()
		err := checkRuleFloors(r, f)
		if err == nil {
			t.Fatalf("%+v accepted", r)
		}
		for _, s := range append(subs, "RULE_BELOW_BOARD_MINIMUM", "fix:") {
			if !strings.Contains(err.Error(), s) {
				t.Fatalf("%+v: error lacks %q: %v", r, s, err)
			}
		}
	}
	for _, pro := range pros {
		f, err := readKicadRuleFloors(pro)
		if err != nil {
			t.Fatal(err)
		}
		want := kicadRuleFloors{ViaDia: 900000, ViaDrill: 500000, ViaAnnular: 50000, Track: 200000, DefaultViaDia: 1400000, DefaultViaDrill: 600000}
		if f != want {
			t.Fatalf("%s: floors %+v, want %+v", pro, f, want)
		}
		// What `kicad route --via-drill-mil 11.81 --via-dia-mil 23.62` (0.3 /
		// 0.6 mm) used to write onto this board.
		refused(t, kicad.Rules{ViaDiaMil: 23.62, ViaDrillMil: 11.81}, f, "--via-drill-mil", "--via-dia-mil", "0.5 mm")
		// Each flag alone, partnered with the Default netclass value.
		refused(t, kicad.Rules{ViaDrillMil: 11.81}, f, "--via-drill-mil")
		refused(t, kicad.Rules{ViaDiaMil: 23.62}, f, "--via-dia-mil", "annular") // 0.6 mm round a 0.6 mm drill
		refused(t, kicad.Rules{ViaDrillMil: 1.35 / 0.0254}, f, "annular")        // 1.35 mm drill in the Default 1.4 mm via
		refused(t, kicad.Rules{ViaDiaMil: 35.5, ViaDrillMil: 35}, f, "annular")
		refused(t, kicad.Rules{TrackMil: 6}, f, "--track-mil")
		// 0.2 mm is 7.874 mil: 7.87 is below it in nm, not within a tolerance.
		refused(t, kicad.Rules{TrackMil: 7.87}, f, "--track-mil")
		for _, ok := range []kicad.Rules{
			{},
			{ViaDiaMil: 0.9 / 0.0254, ViaDrillMil: 0.5 / 0.0254}, // exactly the minimums
			{ViaDrillMil: 0.5 / 0.0254},                          // in the Default 1.4 mm via
			{ViaDiaMil: 0.9 / 0.0254},                            // round the Default 0.6 mm drill: 0.15 mm ring
			{ViaDiaMil: 55.12, ViaDrillMil: 23.62, TrackMil: 15.75},
			{TrackMil: 0.2 / 0.0254},
			{TrackMil: 6, MinTrackMil: 6}, // --min-track-mil replaces the board minimum
		} {
			if err := checkRuleFloors(ok, f); err != nil {
				t.Errorf("%s: %+v refused: %v", pro, ok, err)
			}
		}
	}
}

// The intent's netClasses via reaches the bridge request per net (largest
// when a net is in two classes); nets outside any class carry none.
func TestKicadRequirementsCarryIntentVia(t *testing.T) {
	in := &designIntent{
		Nets: map[string]*intentNet{"VBUS": {WidthMil: intentWidth{Outer: 20}}, "SIG": {WidthMil: intentWidth{Outer: 8}}},
		NetClasses: []intentNetClass{
			{Name: "PWR", Nets: []string{"VBUS"}, ViaDiaMil: 24, ViaDrillMil: 12},
			{Name: "HV", Nets: []string{"VBUS"}, ViaDiaMil: 40, ViaDrillMil: 20},
		},
	}
	reqs := kicadRequirements(in)
	if r := reqs["VBUS"]; r.ViaDiaMil != 40 || r.ViaDrillMil != 20 {
		t.Fatalf("VBUS via %+v", r)
	}
	if r := reqs["SIG"]; r.ViaDiaMil != 0 || r.ViaDrillMil != 0 {
		t.Fatalf("SIG via %+v", r)
	}
	b, _ := json.Marshal(reqs["SIG"])
	if strings.Contains(string(b), "via") {
		t.Fatalf("no intent via must stay out of the bridge request: %s", b)
	}
}

// A project's excluded clearance and its unconnected_items set to "ignore"
// fail the kicad-drc gate (listed as items) unless signed waivers match
// every one; excluded courtyards, ignored courtyard checks and the like are
// info only.
func TestKicadDRCGateProjectPolicy(t *testing.T) {
	rep, err := kicad.ParseDRC(mustRead(t, filepath.Join("..", "kicad", "testdata", "drc", "exclusions-all.json")))
	if err != nil {
		t.Fatal(err)
	}
	pol, err := kicad.ReadDRCPolicy(filepath.Join("..", "kicad", "testdata", "drc", "exclusions.kicad_pro"))
	if err != nil {
		t.Fatal(err)
	}
	errs, _, _ := rep.SplitErrors()
	// The excluded unconnected / parity items are not report errors: only the
	// policy below judges them (once).
	if errs.Total != 1 || errs.Counts["invalid_outline"] != 1 {
		t.Fatalf("errors: %+v", errs)
	}
	errs.Violations, errs.Total, errs.Counts = nil, 0, map[string]int{} // only the policy under test
	g := kicadDRCGate(errs, "drc.json")
	if !g.Pass {
		t.Fatalf("clean report: %+v", g)
	}
	silenced := applyDRCPolicy(&g, pol, rep.Excluded())
	// Hard: electrical / fabrication rules, size rules, and a rule pcbpilot
	// does not know (fail closed).
	critical := []string{"EXCLUDED clearance|10000000", "IGNORED unconnected_items", "EXCLUDED unconnected_items|30000000",
		"EXCLUDED extra_footprint|5000000",
		"EXCLUDED via_diameter|", "EXCLUDED track_width|",
		"IGNORED track_width", "IGNORED annular_width", "DOWNGRADED drill_out_of_range", "IGNORED made_up_rule"}
	if g.Pass || len(g.Items) != len(critical) {
		t.Fatalf("critical exclusion / ignore passed: %+v", g)
	}
	// Soft: other error-level rules are a warning (detail, info, summary),
	// not a failure.
	if len(silenced) != 2 || silenced[0].Rule != "courtyards_overlap" || silenced[0].How != "excluded" || !strings.Contains(silenced[0].Where, "Footprint J601") ||
		silenced[1].Rule != "pth_inside_courtyard" || silenced[1].How != "downgraded" {
		t.Fatalf("silenced: %+v", silenced)
	}
	if !strings.Contains(g.Detail, "WARNING: 2 other error-level check(s) silenced") || !strings.Contains(g.Detail, "courtyards_overlap (excluded)") ||
		!strings.Contains(g.Detail, "pth_inside_courtyard (downgraded)") {
		t.Fatalf("detail: %s", g.Detail)
	}
	if !strings.Contains(strings.Join(g.Info, "\n"), "SILENCED EXCLUDED courtyards_overlap|") {
		t.Fatalf("info: %v", g.Info)
	}
	joined := strings.Join(g.Items, "\n")
	for _, want := range append([]string{"too close on purpose", "Track [GND]", "joined by the pour", "Footprint FID1"}, critical...) {
		if !strings.Contains(joined, want) {
			t.Errorf("items lack %q:\n%s", want, joined)
		}
	}
	info := strings.Join(g.Info, "\n")
	for _, want := range []string{"EXCLUDED via_dangling", "IGNORED missing_courtyard"} {
		if !strings.Contains(info, want) {
			t.Errorf("info lacks %q:\n%s", want, info)
		}
	}
	// silk_overlap and missing_footprint at warning are KiCad defaults, not downgrades.
	if strings.Contains(joined+info, "silk_overlap") || strings.Contains(joined+info, "DOWNGRADED missing_footprint") {
		t.Errorf("warning-level rule reported:\n%s\n%s", joined, info)
	}
	// Some waivers are not enough; one per item is.
	one := g
	applyWaivers(&one, []gateWaiver{{Gate: "kicad-drc", Match: "EXCLUDED clearance|10000000", Reason: "reviewed", By: "test"}})
	if one.Pass {
		t.Fatal("half-waived policy passed")
	}
	both := g
	var all []gateWaiver
	for _, m := range critical {
		all = append(all, gateWaiver{Gate: "kicad-drc", Match: m, Reason: "reviewed", By: "test"})
	}
	applyWaivers(&both, all)
	if !both.Pass || len(both.Waived) != len(critical) {
		t.Fatalf("fully waived policy: %+v", both)
	}
	// No policy file: nothing added.
	g2 := kicadDRCGate(errs, "drc.json")
	applyDRCPolicy(&g2, &kicad.DRCPolicy{}, nil)
	if !g2.Pass || len(g2.Items)+len(g2.Info) != 0 {
		t.Fatalf("empty policy: %+v", g2)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Only courtyard / placement-class silencing: the gate warns and passes.
func TestKicadDRCGateSoftPolicyPasses(t *testing.T) {
	g := gateResult{Gate: "kicad-drc", Pass: true, Detail: "0 error(s)"}
	pol := &kicad.DRCPolicy{
		Exclusions: []kicad.DRCExclusion{{Rule: "courtyards_overlap", Key: "courtyards_overlap|1|2|a|b", Comment: "Intentional Overlap"}},
		Ignored:    []string{"npth_inside_courtyard", "silk_overlap"},
		Downgraded: []string{"overlapping_pads", "missing_footprint"},
	}
	s := applyDRCPolicy(&g, pol, nil)
	if !g.Pass || len(g.Items) != 0 || len(s) != 3 || !strings.Contains(g.Detail, "WARNING: 3 other") {
		t.Fatalf("soft policy: %+v silenced %+v", g, s)
	}
	for _, r := range []string{"via_diameter", "drill_out_of_range", "track_width", "annular_width", "hole_to_hole", "connection_width",
		"solder_mask_bridge", "starved_thermal", "zones_intersect", "microvia_drill_out_of_range", "edge_clearance", "no_such_rule"} {
		if drcRuleClass(r) != "hard" {
			t.Errorf("%s is %s, want hard", r, drcRuleClass(r))
		}
	}
	for _, r := range []string{"courtyards_overlap", "pth_inside_courtyard", "npth_inside_courtyard", "overlapping_pads", "footprint"} {
		if drcRuleClass(r) != "soft" {
			t.Errorf("%s is %s, want soft", r, drcRuleClass(r))
		}
	}
}

// The fabrication checks a fab builds from need a waiver however they are
// silenced: excluded, ignored, or downgraded to warning.
func TestKicadDRCGateFabricationRulesAreHard(t *testing.T) {
	for _, rule := range []string{"items_not_allowed", "through_hole_pad_without_hole", "invalid_outline",
		"item_on_disabled_layer", "track_on_post_machined_layer"} {
		for _, pol := range []*kicad.DRCPolicy{
			{Exclusions: []kicad.DRCExclusion{{Rule: rule, Key: rule + "|1|2|a|b"}}},
			{Ignored: []string{rule}},
			{Downgraded: []string{rule}},
		} {
			g := gateResult{Gate: "kicad-drc", Pass: true}
			if s := applyDRCPolicy(&g, pol, nil); g.Pass || len(g.Items) != 1 || len(s) != 0 {
				t.Fatalf("%s %+v: passed or not hard: %+v", rule, pol, g)
			}
			applyWaivers(&g, []gateWaiver{{Gate: "kicad-drc", Match: rule, Reason: "reviewed", By: "test"}})
			if !g.Pass {
				t.Fatalf("%s %+v: a matching waiver did not pass it: %+v", rule, pol, g)
			}
		}
	}
}
