package kicad

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const drcBoard = "testdata/drcboard/board.kicad_pcb"

func mustIndex(t *testing.T) *BoardIndex {
	t.Helper()
	ix, err := ReadBoardIndex(drcBoard)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

// Every uuid of the board resolves to its top-level object: a pad, a
// footprint field or silk line to the footprint; tracks, arcs, vias and
// zones carry their net (from the net table or net_name).
func TestBoardIndexLookup(t *testing.T) {
	ix := mustIndex(t)
	cases := map[string]BoardItem{
		"pad-r1-1":    {Kind: "footprint", UUID: "fp-r1", Ref: "R1", Layer: "F.Cu"},
		"silk-r1":     {Kind: "footprint", UUID: "fp-r1", Ref: "R1", Layer: "F.Cu"},
		"ref-c1":      {Kind: "footprint", UUID: "fp-c1", Ref: "C1", Layer: "B.Cu"},
		"trk-1":       {Kind: "track", UUID: "trk-1", Net: "VCC", Layer: "F.Cu"},
		"arc-1":       {Kind: "track", UUID: "arc-1", Net: "GND", Layer: "B.Cu"},
		"via-1":       {Kind: "via", UUID: "via-1", Net: "VCC"},
		"zone-1":      {Kind: "zone", UUID: "zone-1", Net: "GND", Layer: "B.Cu"},
		"edge-1":      {Kind: "outline", UUID: "edge-1", Layer: "Edge.Cuts"},
		"txt-1":       {Kind: "text", UUID: "txt-1", Layer: "F.SilkS"},
		"poly-logo-a": {Kind: "footprint", UUID: "fp-logo-a", Ref: "REF**", Layer: "F.Cu"},
	}
	for u, want := range cases {
		if got, ok := ix.Lookup(u); !ok || got != want {
			t.Errorf("%s: %+v %v, want %+v", u, got, ok, want)
		}
	}
	if _, ok := ix.Lookup("not-there"); ok {
		t.Error("unknown uuid resolved")
	}
}

func item(desc, uuid string, x, y float64) DRCItem {
	return DRCItem{Description: desc, UUID: uuid, X: x / 0.0254, Y: -y / 0.0254}
}

func viol(rule, section string, items ...DRCItem) Violation {
	v := Violation{Rule: rule, Section: section, Severity: "error", Message: rule, Items: items}
	if len(items) > 0 {
		x, y := items[0].X, items[0].Y
		v.X, v.Y = &x, &y
	}
	for _, it := range items {
		v.Objs = append(v.Objs, it.UUID)
	}
	return v
}

func TestAttributeClasses(t *testing.T) {
	ix := mustIndex(t)
	cases := []struct {
		v     Violation
		class string
		key   string
		refs  []string
	}{
		{viol("clearance", "violations", item("Track [VCC]", "trk-1", 1, 1), item("Pad 2 [GND] of R1", "pad-r1-2", 1, 1)), "routing", "routing|fp:R1,track:VCC", []string{"R1"}},
		{viol("courtyards_overlap", "violations", item("Footprint R1", "fp-r1", 1, 1), item("Footprint C1", "fp-c1", 2, 2)), "placement", "placement|fp:C1,fp:R1", []string{"C1", "R1"}},
		{viol("silk_over_copper", "violations", item("Segment of R1", "silk-r1", 1, 1), item("Pad 1 of R1", "pad-r1-1", 1, 1)), "footprint", "footprint|fp:R1", []string{"R1"}},
		{viol("copper_edge_clearance", "violations", item("Segment on Edge.Cuts", "edge-1", 0, 0), item("Zone [GND]", "zone-1", 3, 3)), "board-outline", "board-outline|outline,zone:GND", nil},
		{viol("lib_footprint_issues", "violations", item("Footprint R1", "fp-r1", 1, 1)), "environment", "environment|fp:R1", []string{"R1"}},
		{viol("missing_footprint", "schematic_parity", item("Symbol R7 [10k]", "sym-uuid", 0, 0)), "schematic-parity", "schematic-parity|?:Symbol R7", []string{"R7"}},
		{viol("text_height", "violations", item("Text on F.Silkscreen", "txt-1", 0, 0)), "graphic", "graphic|text:F.SilkS", nil},
		// Two REF** logos: the designator does not identify one, the uuid does.
		{viol("courtyards_overlap", "violations", item("Footprint REF**", "fp-logo-a", 1, 1), item("Footprint REF**", "fp-logo-b", 1, 1)), "placement", "placement|fp:fp-logo-a,fp:fp-logo-b", []string{"REF**"}},
		{viol("clearance", "violations", item("gone", "deleted", 0, 0)), "unresolved", "unresolved|?:gone", nil},
	}
	for _, c := range cases {
		o := ix.Attribute(c.v)
		if o.Class != c.class || o.Key != c.key || !reflect.DeepEqual(o.Refs, c.refs) {
			t.Errorf("%s: class %q key %q refs %v, want %q %q %v", c.v.Rule, o.Class, o.Key, o.Refs, c.class, c.key, c.refs)
		}
	}
	// Item lines carry what a waiver matches on.
	ov := ix.Own([]Violation{cases[0].v})[0]
	if it := ov.Item(); !strings.Contains(it, "clearance (error, routing: R1 [VCC]) @ (1, 1) mm") {
		t.Errorf("item line %q", it)
	}
}

// / sev returns v with a severity.
func sev(v Violation, s string) Violation { v.Severity = s; return v }

// Matching is by the offending objects (item uuids) and their positions,
// never by "some object of the same owner": a stationary pad does not make
// a clearance to another track of the same net old.
func TestDiffDRC(t *testing.T) {
	ix := mustIndex(t)
	base := ix.Own([]Violation{
		viol("clearance", "violations", item("Track [VCC]", "trk-1", 10, 10), item("Pad 2 of R1", "pad-r1-2", 10.8, 10)),
		viol("copper_edge_clearance", "violations", item("Segment on Edge.Cuts", "edge-1", 172, 133), item("Zone [GND]", "zone-1", 193, 133)),
		viol("clearance", "violations", item("Track [VCC]", "trk-1", 30, 30), item("Pad 1 of R1", "pad-r1-1", 30, 30)),
		viol("footprint_symbol_mismatch", "schematic_parity", item("Footprint R1", "fp-r1", 10, 10)),
		sev(viol("hole_to_hole", "violations", item("Via [VCC]", "via-1", 15, 10), item("Pad 1 of C1", "pad-c1-1", 15, 10)), "warning"),
		sev(viol("solder_mask_bridge", "violations", item("Pad 1 of C1", "pad-c1-1", 20, 10), item("Track [VCC]", "trk-1", 20, 10)), "error"),
	})
	// Refound: R1's pad against the other VCC track (same owner key, same
	// pad position), the board edge at another segment, the 30/30 pair
	// 0.2 mm away, the parity item with R1 moved, the hole_to_hole
	// escalated to error, the mask bridge now only a warning.
	cur := ix.Own([]Violation{
		viol("clearance", "violations", item("Track [VCC]", "trk-2", 10, 10), item("Pad 2 of R1", "pad-r1-2", 10.8, 10)),
		viol("copper_edge_clearance", "violations", item("Segment on Edge.Cuts", "edge-other", 90, 133), item("Zone [GND]", "zone-1", 193, 133)),
		viol("clearance", "violations", item("Track [VCC]", "trk-1", 30.2, 30), item("Pad 1 of R1", "pad-r1-1", 30.2, 30)),
		viol("footprint_symbol_mismatch", "schematic_parity", item("Footprint R1", "fp-r1", 50, 50)),
		viol("hole_to_hole", "violations", item("Via [VCC]", "via-1", 15, 10), item("Pad 1 of C1", "pad-c1-1", 15, 10)),
		sev(viol("solder_mask_bridge", "violations", item("Pad 1 of C1", "pad-c1-1", 20, 10), item("Track [VCC]", "trk-1", 20, 10)), "warning"),
	})
	if cur[0].Owner.Key != base[0].Owner.Key {
		t.Fatalf("test premise: same owner key %q vs %q", cur[0].Owner.Key, base[0].Owner.Key)
	}
	d := DiffDRC(base, cur, 0.5)
	var news []string
	for _, v := range d.New {
		news = append(news, v.Rule+"/"+v.Items[0].UUID)
	}
	want := []string{"clearance/trk-2", "copper_edge_clearance/edge-other", "hole_to_hole/via-1"}
	if !reflect.DeepEqual(news, want) || d.Matched != 3 || len(d.Resolved) != 3 {
		t.Fatalf("new %v matched %d resolved %d", news, d.Matched, len(d.Resolved))
	}
	// One-to-one: a second identical violation is new.
	dup := []OwnedViolation{base[0], base[0]}
	if d := DiffDRC(base[:1], dup, 0.5); len(d.New) != 1 || d.Matched != 1 {
		t.Fatalf("duplicate: %+v", d)
	}
	// The same objects beyond the tolerance: new, the old one resolved.
	far := ix.Own([]Violation{viol("clearance", "violations", item("Track [VCC]", "trk-1", 12, 10), item("Pad 2 of R1", "pad-r1-2", 12.8, 10))})
	if d := DiffDRC(base[:1], far, 0.5); len(d.New) != 1 || len(d.Resolved) != 1 {
		t.Fatalf("far: %+v", d)
	}
	// Without uuids: same owners and every item in order within tolerance.
	noID := func(x float64) []OwnedViolation {
		return ix.Own([]Violation{viol("clearance", "violations", item("Track [VCC]", "", x, 10), item("Pad 2 of R1", "", 10.8, 10))})
	}
	if d := DiffDRC(noID(10), noID(10.3), 0.5); d.Matched != 1 {
		t.Fatalf("uuid-less near: %+v", d)
	}
	if d := DiffDRC(noID(10), noID(11), 0.5); len(d.New) != 1 {
		t.Fatalf("uuid-less: a moved first item with a stationary second is new: %+v", d)
	}
}

func TestParseDRCKeepsItems(t *testing.T) {
	rep, err := ParseDRC(mustReadFile(t, "testdata/drc.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range rep.Violations {
		if len(v.Items) == 0 || len(v.Items) < len(v.Objs) || v.Items[0].Description == "" {
			t.Fatalf("items not kept: %+v", v)
		}
		if *v.X != v.Items[0].X || *v.Y != v.Items[0].Y {
			t.Fatalf("first item position %v,%v vs violation %v,%v", v.Items[0].X, v.Items[0].Y, *v.X, *v.Y)
		}
	}
}

func mustReadFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func near(p *float64, want float64) bool { return p != nil && *p > want-1e-9 && *p < want+1e-9 }

// The smallest allowed value of each contract item over Board Setup,
// netclasses, custom rules and local overrides (pads, footprints, zones).
func TestReadEffectiveRules(t *testing.T) {
	r, err := ReadEffectiveRules(drcBoard)
	if err != nil {
		t.Fatal(err)
	}
	if !near(r.Track.MinMm, 0.1524) || len(r.Track.Sources) != 2 || !r.Track.Covered() { // unconditional 6 mil rule < 0.2 Board Setup
		t.Errorf("track: %+v", r.Track)
	}
	// pad R1.1 local 0.1 < conditional dru 0.12 < Fine 0.15 < Default 0.2 = C1 local < zone 0.5
	if !near(r.Space.MinMm, 0.1) || len(r.Space.Unset) != 1 || len(r.Space.Sources) != 6 || !r.Space.Covered() {
		t.Errorf("space: %+v", r.Space)
	}
	zone := false
	for _, s := range r.Space.Sources {
		zone = zone || (s.Source == "local" && strings.HasPrefix(s.Where, "zone [GND]") && s.Mm == 0.5)
		if s.Where == `rule "fine pitch" constraint clearance` && s.Global {
			t.Error("a conditional rule counted as global")
		}
	}
	if !zone {
		t.Errorf("zone connect_pads clearance not read: %+v", r.Space.Sources)
	}
	if !near(r.Drill.MinMm, 0.25) { // dru hole_size; min_microvia_drill ignored (no microvias)
		t.Errorf("drill: %+v", r.Drill)
	}
	if !near(r.Annular.MinMm, 0.13) || len(r.Annular.Unknown) != 0 {
		t.Errorf("annular: %+v", r.Annular)
	}
	if !near(r.Measured.MinTrackMm, 0.15) || !near(r.Measured.MinViaDrillMm, 0.3) || !near(r.Measured.MinPadDrillMm, 0.6) {
		t.Errorf("measured: %+v", r.Measured)
	}
	b, _ := os.ReadFile(drcBoard)
	if f := BoardFinish(b); f != "ENIG" || !HasEdgeCuts(b) {
		t.Errorf("finish %q, edge cuts %v", f, HasEdgeCuts(b))
	}
	for in, want := range map[string]float64{"0.2mm": 0.2, "8mil": 0.2032, "0.01in": 0.254, "150um": 0.15, "0.3": 0.3} {
		if got, err := parseDRULength(in); err != nil || got < want-1e-9 || got > want+1e-9 {
			t.Errorf("parseDRULength(%q) = %v %v", in, got, err)
		}
	}
}

// What pcbpilot cannot prove is Unknown, and only global rules cover an
// item.
func TestReadEffectiveRulesUnproven(t *testing.T) {
	board := string(mustReadFile(t, drcBoard))
	run := func(pro, dru, pcb string) *EffectiveRules {
		t.Helper()
		dir := t.TempDir()
		if pcb == "" {
			pcb = board
		}
		_ = os.WriteFile(filepath.Join(dir, "b.kicad_pcb"), []byte(pcb), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "b.kicad_pro"), []byte(pro), 0o644)
		if dru != "" {
			_ = os.WriteFile(filepath.Join(dir, "b.kicad_dru"), []byte(dru), 0o644)
		}
		r, err := ReadEffectiveRules(filepath.Join(dir, "b.kicad_pcb"))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	okPro := `{"board":{"design_settings":{"rules":{"min_track_width":0.2,"min_clearance":0.2,"min_through_hole_diameter":0.3,"min_via_annular_width":0.13}}}}`
	cases := []struct {
		name, pro, dru, pcb string
		check               func(r *EffectiveRules) bool
	}{
		{"wrong-type Board Setup value", `{"board":{"design_settings":{"rules":{"min_track_width":"0.2"}}}}`, "", "",
			func(r *EffectiveRules) bool { return len(r.Track.Unknown) == 1 }},
		{"wrong-type netclass clearance", `{"net_settings":{"classes":[{"name":"Default","clearance":"wide"}]}}`, "", "",
			func(r *EffectiveRules) bool { return len(r.Space.Unknown) == 1 }},
		{"unreadable custom min", okPro, "(version 1)\n(rule a (constraint annular_width (min 2*0.1mm)))\n", "",
			func(r *EffectiveRules) bool { return len(r.Annular.Unknown) == 1 }},
		{"ignored custom constraint", okPro, "(version 1)\n(rule b (severity ignore) (constraint hole_size (min 0.1mm)))\n", "",
			func(r *EffectiveRules) bool { return len(r.Drill.Unknown) == 1 }},
		{"warning custom constraint", okPro, "(version 1)\n(rule w (severity warning) (constraint track_width (min 0.3mm)))\n", "",
			func(r *EffectiveRules) bool { return len(r.Track.Unknown) == 1 && len(r.Track.Sources) == 1 }},
		{"max / opt only", okPro, "(version 1)\n(rule m (condition \"A.NetClass == 'HV'\") (constraint clearance (opt 1mm) (max 2mm)))\n", "",
			func(r *EffectiveRules) bool { return len(r.Space.Unknown) == 1 }},
		{"only conditional / local", `{"board":{"design_settings":{"rules":{"min_clearance":0}}}}`,
			"(version 1)\n(rule c (layer outer) (constraint clearance (min 0.2mm)))\n", "",
			func(r *EffectiveRules) bool {
				return r.Space.MinMm != nil && !r.Space.Covered() && len(r.Space.Unknown) == 0
			}},
		{"invalid local override", okPro, "", strings.Replace(board, "(clearance 0.2)", "(clearance wide)", 1),
			func(r *EffectiveRules) bool { return len(r.Space.Unknown) == 1 }},
		{"error severity unconditional rule covers", `{}`, "(version 1)\n(rule e (severity error) (constraint track_width (min 0.15mm)))\n", "",
			func(r *EffectiveRules) bool { return r.Track.Covered() && len(r.Track.Unknown) == 0 }},
	}
	for _, c := range cases {
		if r := run(c.pro, c.dru, c.pcb); !c.check(r) {
			t.Errorf("%s: track %+v space %+v drill %+v annular %+v", c.name, r.Track, r.Space, r.Drill, r.Annular)
		}
	}
}

func TestFinishProcess(t *testing.T) {
	for label, want := range map[string]string{"HAL lead-free": FinishHASLLeadFree, "LF-HASL": FinishHASLLeadFree, "HASL lead free": FinishHASLLeadFree,
		"HAL SnPb": FinishHASLLeaded, "HASL with lead": FinishHASLLeaded, "ENIG": FinishENIG, "enepig": FinishENEPIG, "OSP": FinishOSP,
		"HT_OSP": FinishOSP, "Immersion silver": FinishImmSilver, "Immersion tin": FinishImmTin, "Hard gold": FinishHardGold} {
		if got, err := FinishProcess(label); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", label, got, err, want)
		}
	}
	for _, label := range []string{"HASL", "HAL", "gold", "Immersion gold", "User defined", ""} {
		if got, err := FinishProcess(label); err == nil {
			t.Errorf("%q: %q, want ambiguous / unknown", label, got)
		}
	}
}

func TestLoadFabContract(t *testing.T) {
	dir := t.TempDir()
	write := func(s string) string {
		p := filepath.Join(dir, "c.json")
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	good := `{"vendor":"JLCPCB","layers":2,"finish":"HASL lead-free","assembly":{"sides":"top"},
	  "limits":{"minTrackMm":0.127,"minSpaceMm":0.127,"minDrillMm":0.3,"minAnnularMm":0.13},
	  "sources":[{"item":"layers","url":"https://example.com/order","retrieved":"2026-10-27"},{"item":"limits","url":"https://example.com/capabilities","retrieved":"2026-10-27"},
	             {"item":"finish","url":"https://example.com/finish","retrieved":"2026-10-27"},
	             {"item":"assembly.sides","url":"https://example.com/quote","retrieved":"2026-10-27"}]}`
	c, err := LoadFabContract(write(good))
	if err != nil || c.Limit("drill") != 0.3 || c.Assembly.Sides != "top" {
		t.Fatalf("good contract: %+v %v", c, err)
	}
	// Per-limit sources cover too.
	src := func(item string) string {
		return `{"item":"` + item + `","url":"https://example.com/a","retrieved":"2026-10-27"},`
	}
	perLimit := strings.Replace(good, `{"item":"limits",`, src("limits.minTrackMm")+src("limits.minSpaceMm")+src("limits.minDrillMm")+`{"item":"limits.minAnnularMm",`, 1)
	if _, err := LoadFabContract(write(perLimit)); err != nil {
		t.Fatalf("per-limit sources: %v", err)
	}
	for name, bad := range map[string]string{
		"misspelt limit":        strings.Replace(good, "minTrackMm", "minTrakMm", 1),
		"no sources":            `{"vendor":"JLCPCB","layers":2,"sources":[]}`,
		"undated":               strings.Replace(good, "2026-10-27", "Oct 2026", 1),
		"not a url":             strings.Replace(good, "https://example.com/capabilities", "jlc website", 1),
		"sides":                 strings.Replace(good, `"top"`, `"front"`, 1),
		"no layers":             strings.Replace(good, `"layers":2`, `"layers":0`, 1),
		"finish without source": strings.Replace(good, `"item":"finish"`, `"item":"mask"`, 1),
		"sides without source":  strings.Replace(good, `"item":"assembly.sides"`, `"item":"silk"`, 1),
		"limit without source":  strings.Replace(perLimit, src("limits.minDrillMm"), "", 1),
		"unknown source item":   strings.Replace(good, `"item":"finish"`, `"item":"finsh"`, 1),
		"layers without source": strings.Replace(good, `{"item":"layers","url":"https://example.com/order","retrieved":"2026-10-27"},`, "", 1),
	} {
		if _, err := LoadFabContract(write(bad)); err == nil || !strings.Contains(err.Error(), "CONTRACT_INVALID") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func dirListing(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			out[p] = string(b)
		}
		return nil
	})
	return out
}

// The project is copied (board, project, rules, library tables with
// ${KIPRJMOD} resolved, the schematic hierarchy for parity); kicad-cli runs
// in the copy with the isolated configuration and the original is untouched.
func TestRunDRCStagedCopy(t *testing.T) {
	src, _ := filepath.Abs("testdata/drcboard")
	proj := t.TempDir()
	for p, body := range dirListing(t, src) {
		rel, _ := filepath.Rel(src, p)
		_ = os.MkdirAll(filepath.Dir(filepath.Join(proj, rel)), 0o755)
		if err := os.WriteFile(filepath.Join(proj, rel), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := dirListing(t, proj)
	tools := fakeDRC(t, "drc/clean-parity.json", "0")
	log := filepath.Join(t.TempDir(), "args.json")
	t.Setenv("PCBPILOT_FAKE_DRC_LOG", log)
	t.Setenv("KICAD_CONFIG_HOME", "")
	t.Setenv(EnvIsolateConfig, "")
	saved := filepath.Join(t.TempDir(), "raw.json")
	run, err := RunDRC(DRCRunOptions{PCB: filepath.Join(proj, "board.kicad_pcb"), SchematicParity: true, CLI: tools.CLI, SaveRaw: saved})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"board.kicad_pcb", "board.kicad_pro", "board.kicad_dru", "fp-lib-table", "board.kicad_sch", "sub/sub.kicad_sch"}
	if !reflect.DeepEqual(run.Staged, want) || run.Report.Total != 0 || run.Policy == nil || len(run.Policy.Exclusions) != 1 || !run.Isolated {
		t.Fatalf("run: %+v policy %+v", run, run.Policy)
	}
	if !reflect.DeepEqual(dirListing(t, proj), before) {
		t.Fatal("the project directory changed")
	}
	var got struct {
		Args       []string `json:"args"`
		Cwd        string   `json:"cwd"`
		ConfigHome string   `json:"configHome"`
	}
	if err := json.Unmarshal(mustReadFile(t, log), &got); err != nil {
		t.Fatal(err)
	}
	if !contains(got.Args, "--schematic-parity") || !contains(got.Args, "--severity-all") || got.ConfigHome == "" ||
		filepath.Dir(got.Args[len(got.Args)-1]) != got.Cwd || strings.HasPrefix(got.Cwd, proj) {
		t.Fatalf("kicad-cli call: %+v", got)
	}
	if _, err := os.Stat(got.Cwd); !os.IsNotExist(err) {
		t.Fatalf("staging directory left behind: %v", err)
	}
	if b, err := os.ReadFile(saved); err != nil || len(b) == 0 {
		t.Fatalf("raw report not saved: %v", err)
	}

	// The library table copy points back at the project.
	stage := t.TempDir()
	if _, err := StageProject(filepath.Join(proj, "board.kicad_pcb"), stage, false); err != nil {
		t.Fatal(err)
	}
	if lt := string(mustReadFile(t, filepath.Join(stage, "fp-lib-table"))); strings.Contains(lt, "KIPRJMOD") || !strings.Contains(lt, filepath.ToSlash(proj)+"/local.pretty") {
		t.Fatalf("fp-lib-table: %s", lt)
	}

	// Parity without the project's root schematic, or a sheet outside the
	// project, is a staging failure (cannot check), not a parity pass.
	_ = os.Remove(filepath.Join(proj, "board.kicad_sch"))
	if _, err := RunDRC(DRCRunOptions{PCB: filepath.Join(proj, "board.kicad_pcb"), SchematicParity: true, CLI: tools.CLI}); !errors.Is(err, ErrDRCStage) {
		t.Fatalf("missing root schematic: %v", err)
	}
	root := strings.Replace(string(mustReadFile(t, "testdata/drcboard/board.kicad_sch")), "sub/sub.kicad_sch", "../elsewhere.kicad_sch", 1)
	_ = os.WriteFile(filepath.Join(proj, "board.kicad_sch"), []byte(root), 0o644)
	_ = os.WriteFile(filepath.Join(filepath.Dir(proj), "elsewhere.kicad_sch"), []byte("(kicad_sch (version 20250114))"), 0o644)
	if _, err := StageProject(filepath.Join(proj, "board.kicad_pcb"), t.TempDir(), true); !errors.Is(err, ErrDRCStage) || !strings.Contains(err.Error(), "outside the project") {
		t.Fatalf("sheet outside the project: %v", err)
	}
	// A failing kicad-cli is ErrDRCReport.
	tools = fakeDRC(t, "drc/clean.json", "3")
	if _, err := RunDRC(DRCRunOptions{PCB: filepath.Join(proj, "board.kicad_pcb"), CLI: tools.CLI}); !errors.Is(err, ErrDRCReport) {
		t.Fatalf("failing kicad-cli: %v", err)
	}
}

// The shared policy evaluation behind every kicad-drc gate.
func TestEvalDRCPolicy(t *testing.T) {
	if EvalDRCPolicy(nil, nil) != nil {
		t.Fatal("nil policy")
	}
	pol := &DRCPolicy{
		Exclusions: []DRCExclusion{{Rule: "clearance", Key: "clearance|1|2|u1|u2"}, {Rule: "courtyards_overlap", Key: "courtyards_overlap|1|2|a|b"}},
		Ignored:    []string{"silk_overlap", "made_up_rule"},
		Downgraded: []string{"via_dangling", "overlapping_pads"},
	}
	r := EvalDRCPolicy(pol, []Violation{{Rule: "clearance", Objs: []string{"u1"}, Message: "Clearance violation"}})
	if len(r.Hard) != 2 || !strings.Contains(r.Hard[0], "EXCLUDED clearance|1|2|u1|u2: Clearance violation") || !strings.HasPrefix(r.Hard[1], "IGNORED made_up_rule") {
		t.Fatalf("hard: %v", r.Hard)
	}
	if len(r.Silenced) != 2 || r.Silenced[0].Rule != "courtyards_overlap" || r.Silenced[1].How != "downgraded" {
		t.Fatalf("silenced: %+v", r.Silenced)
	}
	if !pol.Ignores("silk_overlap") || pol.Ignores("clearance") || (*DRCPolicy)(nil).Ignores("x") {
		t.Fatal("Ignores")
	}
	if r.HardDetail() == "" || !strings.Contains(r.SilencedDetail(), "courtyards_overlap (excluded), overlapping_pads (downgraded)") {
		t.Fatalf("details: %q / %q", r.HardDetail(), r.SilencedDetail())
	}
}

// Mixed items: the uuids that are there still decide; only the item without
// one is compared by position.
func TestDiffDRCMixedUUIDs(t *testing.T) {
	ix := mustIndex(t)
	mk := func(track string, padX float64) []OwnedViolation {
		return ix.Own([]Violation{viol("clearance", "violations", item("Track [VCC]", track, 10, 10), item("Pad 2 of R1", "", padX, 10))})
	}
	base := mk("trk-1", 10.8)
	if d := DiffDRC(base, mk("trk-2", 10.8), 0.5); len(d.New) != 1 || d.Matched != 0 {
		t.Fatalf("other track, unidentified pad in place: %+v", d)
	}
	if d := DiffDRC(base, mk("trk-1", 10.9), 0.5); d.Matched != 1 {
		t.Fatalf("same track, pad within tolerance: %+v", d)
	}
	if d := DiffDRC(base, mk("trk-1", 12), 0.5); len(d.New) != 1 {
		t.Fatalf("same track, pad moved: %+v", d)
	}
	// A uuid on one side only: the other side's object cannot be shown to be
	// R1's pad (its owner is unresolved), so it is not the same violation.
	oneSided := ix.Own([]Violation{viol("clearance", "violations", item("Track [VCC]", "trk-1", 10, 10), item("Pad 2 of R1", "pad-r1-2", 10.8, 10))})
	if d := DiffDRC(base, oneSided, 0.5); len(d.New) != 1 {
		t.Fatalf("uuid on one side: %+v", d)
	}
}

// Rules that gate at warning severity are hard for the policy too: a
// project that excludes or ignores them fails unless a waiver accepts it.
func TestFabWarningRulesAreHard(t *testing.T) {
	for _, rule := range []string{"copper_sliver", "holes_co_located", "footprint_symbol_field_mismatch", "hole_to_hole", "connection_width"} {
		if DRCRuleClass(rule) != "hard" || !DRCGatesWarning(rule) {
			t.Errorf("%s: class %s, gates %v", rule, DRCRuleClass(rule), DRCGatesWarning(rule))
		}
		for _, pol := range []*DRCPolicy{
			{Exclusions: []DRCExclusion{{Rule: rule, Key: rule + "|1|2|a|b"}}},
			{Ignored: []string{rule}},
		} {
			if r := EvalDRCPolicy(pol, nil); len(r.Hard) != 1 {
				t.Errorf("%s %+v: %+v", rule, pol, r)
			}
		}
	}
	for _, rule := range []string{"silk_overlap", "isolated_copper", "lib_footprint_mismatch"} {
		if DRCGatesWarning(rule) {
			t.Errorf("%s gates as a warning", rule)
		}
	}
}

func TestReadEffectiveRulesNetclassesAndFlags(t *testing.T) {
	board := string(mustReadFile(t, drcBoard))
	run := func(pro string) *EffectiveRules {
		t.Helper()
		dir := t.TempDir()
		_ = os.WriteFile(filepath.Join(dir, "b.kicad_pcb"), []byte(board), 0o644)
		_ = os.WriteFile(filepath.Join(dir, "b.kicad_pro"), []byte(pro), 0o644)
		r, err := ReadEffectiveRules(filepath.Join(dir, "b.kicad_pcb"))
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	// Fine with clearance 0 next to Default 0.2 and no Board Setup minimum:
	// Fine's effective clearance is unproven.
	r := run(`{"board":{"design_settings":{"rules":{"min_clearance":0}}},"net_settings":{"classes":[{"name":"Default","clearance":0.2},{"name":"Fine","clearance":0}]}}`)
	if len(r.Space.Unknown) != 1 || !strings.Contains(r.Space.Unknown[0], "Fine") {
		t.Errorf("zero class clearance: %+v", r.Space)
	}
	// A class without a clearance inherits Default's.
	if r := run(`{"net_settings":{"classes":[{"name":"Default","clearance":0.2},{"name":"Fine"}]}}`); len(r.Space.Unknown) != 0 {
		t.Errorf("inherited clearance: %+v", r.Space)
	}
	if r := run(`{"net_settings":{"classes":[{"name":"Default"},{"name":"Fine"}]}}`); len(r.Space.Unknown) != 1 {
		t.Errorf("nothing to inherit: %+v", r.Space)
	}
	// Microvias allowed: their smaller drill minimum counts; a malformed
	// flag leaves the drill unproven.
	if r := run(`{"board":{"design_settings":{"rules":{"allow_microvias":true,"min_through_hole_diameter":0.3,"min_microvia_drill":0.1}}}}`); !near(r.Drill.MinMm, 0.1) || len(r.Drill.Unknown) != 0 {
		t.Errorf("microvias: %+v", r.Drill)
	}
	if r := run(`{"board":{"design_settings":{"rules":{"allow_microvias":"yes","min_through_hole_diameter":0.3,"min_microvia_drill":0.1}}}}`); len(r.Drill.Unknown) != 1 {
		t.Errorf("malformed allow_microvias: %+v", r.Drill)
	}
	if r := run(`{"board":{"design_settings":{"rules":{"allow_blind_buried_vias":1}}}}`); len(r.Drill.Unknown) != 1 {
		t.Errorf("malformed allow_blind_buried_vias: %+v", r.Drill)
	}
}
