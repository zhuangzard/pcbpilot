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

// A board that moved one footprint: only what involves it is new; KiCad
// reporting a still-present violation at another of its objects' points
// (a refill) still matches; a parity item matches wherever the part is.
func TestDiffDRC(t *testing.T) {
	ix := mustIndex(t)
	base := ix.Own([]Violation{
		viol("copper_edge_clearance", "violations", item("Segment on Edge.Cuts", "edge-1", 172, 133), item("Zone [GND]", "zone-1", 193, 133)),
		viol("clearance", "violations", item("Track [VCC]", "trk-1", 10, 10), item("Pad 2 of R1", "pad-r1-2", 10, 10)),
		viol("clearance", "violations", item("Track [VCC]", "trk-1", 30, 30), item("Pad 2 of R1", "pad-r1-2", 30, 30)),
		viol("footprint_symbol_mismatch", "schematic_parity", item("Footprint R1", "fp-r1", 10, 10)),
	})
	cur := ix.Own([]Violation{
		viol("copper_edge_clearance", "violations", item("Segment on Edge.Cuts", "edge-1", 90, 133), item("Zone [GND]", "zone-1", 193, 133)),
		viol("clearance", "violations", item("Track [VCC]", "trk-1", 10.3, 10)), // same owners? no: pad gone → other key
		viol("clearance", "violations", item("Track [VCC]", "trk-1", 30.2, 30), item("Pad 2 of R1", "pad-r1-2", 30.2, 30)),
		viol("courtyards_overlap", "violations", item("Footprint R1", "fp-r1", 50, 50), item("Footprint C1", "fp-c1", 50, 50)),
		viol("footprint_symbol_mismatch", "schematic_parity", item("Footprint R1", "fp-r1", 50, 50)),
	})
	d := DiffDRC(base, cur, 0.5)
	var newRules []string
	for _, v := range d.New {
		newRules = append(newRules, v.Rule+"|"+v.Owner.Key)
	}
	want := []string{"clearance|routing|track:VCC", "courtyards_overlap|placement|fp:C1,fp:R1"}
	if !reflect.DeepEqual(newRules, want) || d.Matched != 3 || len(d.Resolved) != 1 || d.Resolved[0].Owner.Key != "routing|fp:R1,track:VCC" {
		t.Fatalf("new %v matched %d resolved %+v", newRules, d.Matched, d.Resolved)
	}
	// One-to-one: a second identical violation is new.
	dup := append(append([]OwnedViolation{}, base[1]), base[1])
	if d := DiffDRC(base[1:2], dup, 0.5); len(d.New) != 1 || d.Matched != 1 {
		t.Fatalf("duplicate: %+v", d)
	}
	// Beyond the tolerance it is new and the old one resolved.
	far := ix.Own([]Violation{viol("clearance", "violations", item("Track [VCC]", "trk-1", 12, 10), item("Pad 2 of R1", "pad-r1-2", 12, 10))})
	if d := DiffDRC(base[1:2], far, 0.5); len(d.New) != 1 || len(d.Resolved) != 1 {
		t.Fatalf("far: %+v", d)
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

// The smallest allowed value of each contract item over Board Setup,
// netclasses, custom rules and local overrides.
func TestReadEffectiveRules(t *testing.T) {
	r, err := ReadEffectiveRules(drcBoard)
	if err != nil {
		t.Fatal(err)
	}
	near := func(p *float64, want float64) bool { return p != nil && *p > want-1e-9 && *p < want+1e-9 }
	if !near(r.Track.MinMm, 0.1524) || len(r.Track.Sources) != 2 { // 6 mil custom rule < 0.2 Board Setup
		t.Errorf("track: %+v", r.Track)
	}
	if !near(r.Space.MinMm, 0.1) || len(r.Space.Unset) != 1 { // pad R1.1 local 0.1 < dru 0.12 < Fine 0.15
		t.Errorf("space: %+v", r.Space)
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

	// A rule pcbpilot cannot read, or one that turns a contract item's check
	// off, leaves that item unproven.
	dir := t.TempDir()
	for _, ext := range []string{".kicad_pcb", ".kicad_pro"} {
		if err := os.WriteFile(filepath.Join(dir, "b"+ext), mustReadFile(t, "testdata/drcboard/board"+ext), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dru := "(version 1)\n(rule a (constraint annular_width (min 2*0.1mm)))\n(rule b (severity ignore) (constraint hole_size (min 0.1mm)))\n"
	if err := os.WriteFile(filepath.Join(dir, "b.kicad_dru"), []byte(dru), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = ReadEffectiveRules(filepath.Join(dir, "b.kicad_pcb"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Annular.Unknown) != 1 || len(r.Drill.Unknown) != 1 || len(r.Track.Unknown) != 0 {
		t.Fatalf("unknown: annular %v drill %v track %v", r.Annular.Unknown, r.Drill.Unknown, r.Track.Unknown)
	}
	for in, want := range map[string]float64{"0.2mm": 0.2, "8mil": 0.2032, "0.01in": 0.254, "150um": 0.15, "0.3": 0.3} {
		if got, err := parseDRULength(in); err != nil || got < want-1e-9 || got > want+1e-9 {
			t.Errorf("parseDRULength(%q) = %v %v", in, got, err)
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
	good := `{"vendor":"JLCPCB","layers":2,"finish":"HASL","assembly":{"sides":"top"},
	  "limits":{"minTrackMm":0.127,"minSpaceMm":0.127,"minDrillMm":0.3,"minAnnularMm":0.13},
	  "sources":[{"item":"limits","url":"https://example.com/capabilities","retrieved":"2026-10-27"}]}`
	c, err := LoadFabContract(write(good))
	if err != nil || c.Limit("drill") != 0.3 || c.Assembly.Sides != "top" {
		t.Fatalf("good contract: %+v %v", c, err)
	}
	for name, bad := range map[string]string{
		"misspelt limit": strings.Replace(good, "minTrackMm", "minTrakMm", 1),
		"no sources":     strings.Replace(good, `"sources":[{"item":"limits","url":"https://example.com/capabilities","retrieved":"2026-10-27"}]`, `"sources":[]`, 1),
		"undated":        strings.Replace(good, "2026-10-27", "Oct 2026", 1),
		"not a url":      strings.Replace(good, "https://example.com/capabilities", "jlc website", 1),
		"sides":          strings.Replace(good, `"top"`, `"front"`, 1),
		"no layers":      strings.Replace(good, `"layers":2`, `"layers":0`, 1),
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
