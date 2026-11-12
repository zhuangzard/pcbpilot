package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbWatchdogSpec: an 8-pin IC whose four signal pins each have a series
// resistor and a pull-up, plus four key circuits (pull-up, switch, series
// resistor, filter cap) no IC pin reaches — the zone shape of the field board F
// keys and encoders.
func sbWatchdogSpec() *sbSpec {
	two := func(a, b string) []sbPin {
		return []sbPin{{Number: "1", Name: a, Type: "passive"}, {Number: "2", Name: b, Type: "passive"}}
	}
	s := &sbSpec{
		Name:  "wd",
		Zones: []sbZone{{ID: "io", Title: "IO"}},
		Parts: []sbPart{{Ref: "U1", Value: "MCU", Zone: "io", Pins: []sbPin{
			{Number: "1", Name: "VDD", Type: "power_in"}, {Number: "2", Name: "PA0", Type: "bidirectional"},
			{Number: "3", Name: "PA1", Type: "bidirectional"}, {Number: "4", Name: "VSS", Type: "power_in"},
			{Number: "5", Name: "PB0", Type: "bidirectional"}, {Number: "6", Name: "PB1", Type: "bidirectional"},
			{Number: "7", Name: "NC1", Type: "passive"}, {Number: "8", Name: "NC2", Type: "passive"}}}},
		Nets:      []sbNet{{Name: "+3V3", Pins: []string{"U1:VDD"}, Kind: "power"}, {Name: "GND", Pins: []string{"U1:VSS"}, Kind: "ground"}},
		NoConnect: []string{"U1:NC1", "U1:NC2"},
	}
	add := func(ref string, pins []sbPin) {
		s.Parts = append(s.Parts, sbPart{Ref: ref, Value: "10k", Zone: "io", Pins: pins})
	}
	net := func(name string, pins ...string) {
		for i := range s.Nets {
			if s.Nets[i].Name == name {
				s.Nets[i].Pins = append(s.Nets[i].Pins, pins...)
				return
			}
		}
		s.Nets = append(s.Nets, sbNet{Name: name, Pins: pins})
	}
	for i, pin := range []string{"PA0", "PA1", "PB0", "PB1"} {
		rs, ru := "R"+string(rune('1'+i)), "R"+string(rune('5'+i))
		add(rs, two("1", "2"))
		add(ru, two("1", "2"))
		net("SIG_"+pin, "U1:"+pin, rs+":1", ru+":1")
		net("OUT_"+pin, rs+":2")
		net("+3V3", ru+":2")
	}
	for i := 0; i < 4; i++ {
		k := string(rune('A' + i))
		add("RK"+k, two("1", "2")) // pull-up
		add("RS"+k, two("1", "2")) // series
		add("SW"+k, two("1", "2")) // key
		add("CK"+k, two("1", "2")) // filter
		net("KEY_"+k+"_RAW", "RK"+k+":2", "RS"+k+":1", "SW"+k+":1")
		net("KEY_"+k, "RS"+k+":2", "CK"+k+":1")
		net("+3V3", "RK"+k+":1")
		net("GND", "SW"+k+":2", "CK"+k+":2")
	}
	return s
}

func sbWatchdogZone(t *testing.T, v sbGridVariant) (*sbDesign, *sbZoneIn, *sbZoneLayout) {
	t.Helper()
	d, _, err := buildDesign(sbWatchdogSpec(), sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	zi := d.zoneInputs()[0]
	zl, err := gridZone(d, zi, &sbMeasure{cache: map[string]kicad.SceneSymbol{}}, v)
	if err != nil {
		t.Fatalf("grid: %v", err)
	}
	return d, zi, zl
}

// A zone drawn the v0.12.8 way with plain columns (every pin on its own stub
// and label) is what the user rejected: the watchdog must stop it, name the
// metric, value and limit, and suggest near hints for the loose passives.
func TestSbWatchdogFlagsLabelOnlyZone(t *testing.T) {
	d, _, err := buildDesign(sbWatchdogSpec(), sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	zi := d.zoneInputs()[0]
	var zl *sbZoneLayout
	for _, v := range sbGridVariants { // the first plain-column variant that draws it
		if v.NoAttach {
			if zl, err = gridZone(d, zi, &sbMeasure{cache: map[string]kicad.SceneSymbol{}}, v); err == nil {
				break
			}
		}
	}
	if zl == nil {
		t.Fatalf("no column drawing: %v", err)
	}
	q := sbZoneQualityOf(d, zi, zl, &sbMeasure{cache: map[string]kicad.SceneSymbol{}})
	if q.SigConns < 8 || q.LabelShare() < 0.9 || q.AttachedShare() > 0.1 {
		t.Fatalf("fixture: the column drawing should be labels only: %+v (label %.2f attached %.2f)", q, q.LabelShare(), q.AttachedShare())
	}
	g, issues := sbLayoutQualityGate(d, []*sbZoneIn{zi}, map[string]*sbZoneLayout{"io": zl}, nil, nil, "strict", 0, sbLayoutQualityLimits)
	if g.Status != "fail" || len(issues) == 0 {
		t.Fatalf("watchdog passed a labels-only zone: %s", g.Detail)
	}
	var metrics []string
	for _, is := range issues {
		metrics = append(metrics, is.Scope+"/"+is.Metric)
		if is.Scope == "zone" && (is.ID != "io" || is.Limit == 0 || is.Fix == "") {
			t.Errorf("zone issue without id/limit/fix: %+v", is)
		}
	}
	for _, want := range []string{"zone/signalLabelShare", "zone/attachedShare", "board/signalLabelShare", "board/attachedShare"} {
		if !strings.Contains(strings.Join(metrics, " "), want) {
			t.Errorf("missing %s in %v", want, metrics)
		}
	}
	if fix := sbLQFix(issues); !strings.Contains(fix, "near U1:PA0") || !strings.Contains(fix, "split zone io") {
		t.Errorf("fix lacks a near hint / split: %s", fix)
	}
	adv, _ := sbLayoutQualityGate(d, []*sbZoneIn{zi}, map[string]*sbZoneLayout{"io": zl}, nil, nil, "advisory", 0, sbLayoutQualityLimits)
	if adv.Status != "warn" {
		t.Errorf("advisory status %s, want warn", adv.Status)
	}
}

// The compact grid draws the same zone with wires (pin -> R chains, the
// key circuits as wired blocks): the watchdog does not flag it.
func TestSbWatchdogPassesWiredZone(t *testing.T) {
	d, zi, zl := sbWatchdogZone(t, sbGridVariants[0])
	m := &sbMeasure{cache: map[string]kicad.SceneSymbol{}}
	q := sbZoneQualityOf(d, zi, zl, m)
	if q.LabelShare() > 0.5 || q.AttachedShare() < 0.6 {
		t.Errorf("compact grid left the zone label-heavy: label %.2f attached %.2f (%+v)", q.LabelShare(), q.AttachedShare(), q)
	}
	g, issues := sbLayoutQualityGate(d, []*sbZoneIn{zi}, map[string]*sbZoneLayout{"io": zl}, []sbEGPage{{Sheet: "io.kicad_sch", FillRatio: 0.55}}, map[string]string{"io.kicad_sch": "A3"}, "strict", 0, sbLayoutQualityLimits)
	if g.Status != "pass" || len(issues) != 0 {
		t.Errorf("watchdog flagged a wired zone: %s", g.Detail)
	}
}

func TestSbWatchdogPageFill(t *testing.T) {
	d, zi, zl := sbWatchdogZone(t, sbGridVariants[0])
	// two small zones on one page: packing could bring them down a size
	zi2 := *zi
	zi2.Zone.ID = "io2"
	small := *zl
	small.Box = kicad.Box{MaxX: 50, MaxY: 40}
	zis := []*sbZoneIn{zi, &zi2}
	lays := map[string]*sbZoneLayout{"io": &small, "io2": &small}
	g, issues := sbLayoutQualityGate(d, zis, lays,
		[]sbEGPage{{Sheet: "io.kicad_sch", FillRatio: 0.12}}, map[string]string{"io.kicad_sch": "A2"}, "strict", 0, sbLayoutQualityLimits)
	if g.Status != "fail" || len(issues) != 1 || issues[0].Scope != "page" || issues[0].Metric != "fillRatio" || !strings.Contains(issues[0].Fix, "A2") {
		t.Fatalf("a mostly blank sheet passed or was misreported: %s %+v", g.Detail, issues)
	}
	// one zone alone on its page: its own shape sets the paper (no repack helps)
	if g, _ := sbLayoutQualityGate(d, []*sbZoneIn{zi}, map[string]*sbZoneLayout{"io": zl},
		[]sbEGPage{{Sheet: "io.kicad_sch", FillRatio: 0.12}}, map[string]string{"io.kicad_sch": "A2"}, "strict", 0, sbLayoutQualityLimits); g.Status != "pass" {
		t.Errorf("single-zone page flagged: %s", g.Detail)
	}
	// on A4, the smallest paper, a small circuit is never "mostly blank"
	if g, _ := sbLayoutQualityGate(d, zis, lays,
		[]sbEGPage{{Sheet: "io.kicad_sch", FillRatio: 0.12}}, map[string]string{"io.kicad_sch": "A4"}, "strict", 0, sbLayoutQualityLimits); g.Status != "pass" {
		t.Errorf("A4 page flagged: %s", g.Detail)
	}
}

// A zone that ran past --zone-timeout is reported by name, time and the
// drawing kept — not only in warnings.
func TestSbWatchdogReportsSlowZone(t *testing.T) {
	d, zi, zl := sbWatchdogZone(t, sbGridVariants[0])
	zl.TimedOut, zl.Skipped, zl.Ms, zl.Variant = true, 7, 61234, sbGridVariants[0].name()
	g, _ := sbLayoutQualityGate(d, []*sbZoneIn{zi}, map[string]*sbZoneLayout{"io": zl}, nil, nil, "strict", 60e9, sbLayoutQualityLimits)
	if !strings.Contains(g.Detail, "zone io took 61.2 s (limit 60 s): kept compact r1 o0, 7 variant(s) not tried") {
		t.Errorf("slow zone not reported: %s", g.Detail)
	}
}

func TestSbLayoutQualityModeNeedsWaiver(t *testing.T) {
	if m, f := sbLayoutQualityMode("", nil); m != "strict" || f != nil {
		t.Errorf("default %q %+v, want strict", m, f)
	}
	if _, f := sbLayoutQualityMode("advisory", nil); f == nil || f.Code != "LAYOUT_QUALITY_WAIVER_REQUIRED" {
		t.Errorf("advisory without a waiver: %+v", f)
	}
	w := []gateWaiver{{Gate: "layout-quality", Match: "--layout-quality advisory", Reason: "r", By: "b"}}
	if m, f := sbLayoutQualityMode("advisory", w); m != "advisory" || f != nil {
		t.Errorf("advisory with a waiver: %q %+v", m, f)
	}
	if _, f := sbLayoutQualityMode("loose", nil); f == nil {
		t.Error("unknown mode accepted")
	}
}

// End to end: a build the watchdog stops is a structured failure — ok
// false, LAYOUT_QUALITY with per-metric issues and fixes, the failed project
// kept, --out untouched.
func TestSbBuildLayoutQualityFailure(t *testing.T) {
	sbNeedKicad(t)
	old := sbLayoutQualityLimits
	defer func() { sbLayoutQualityLimits = old }()
	// limits no drawing can meet stand in for a bad drawing
	sbLayoutQualityLimits.BoardAttachedMin = 1.01
	out := filepath.Join(t.TempDir(), "p")
	rep, err := runSchBuild(sbWatchdogSpec(), sbOptions{OutDir: out, NoIntent: true, NoPlanner: true, Jobs: 2}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if rep.OK || rep.Error == nil || rep.Error.Code != "LAYOUT_QUALITY" || len(rep.Error.Issues) == 0 {
		t.Fatalf("want ok:false LAYOUT_QUALITY with issues, got ok=%v %+v", rep.OK, rep.Error)
	}
	if is := rep.Error.Issues[0]; is.Metric != "attachedShare" || is.Limit != 1.01 || is.Fix == "" {
		t.Errorf("issue %+v", is)
	}
	if !strings.Contains(rep.Error.Fix, "failed project is in") || rep.Outputs["failedBuild"] == "" {
		t.Errorf("failed build not kept: %s %v", rep.Error.Fix, rep.Outputs)
	}
	if _, err := os.Stat(filepath.Join(out, "p.kicad_sch")); err == nil {
		t.Error("--out was written by a build the watchdog stopped")
	}
	// the same build under the calibrated limits passes
	sbLayoutQualityLimits = old
	rep, err = runSchBuild(sbWatchdogSpec(), sbOptions{OutDir: out, NoIntent: true, NoPlanner: true, Jobs: 2}, nil)
	if err != nil || !rep.OK {
		t.Fatalf("calibrated limits: %v %+v", err, rep.Error)
	}
}

// sbPack2D finds the arrangement the row packer misses (a tall zone with two
// short ones beside it) and so a smaller paper; frames never overlap.
func TestSbPack2DSmallerPaper(t *testing.T) {
	items := []sbPackItem{
		{ID: "wide", Box: kicad.Box{MinX: -10, MinY: -5, MaxX: 230, MaxY: 60}},
		{ID: "tall", Box: kicad.Box{MaxX: 120, MaxY: 230}},
		{ID: "b", Box: kicad.Box{MaxX: 230, MaxY: 50}},
		{ID: "c", Box: kicad.Box{MaxX: 230, MaxY: 50}},
	}
	papers := map[string]bool{}
	var frames []kicad.Box
	for _, p := range kicad.Papers {
		if out, _, ok := sbPack2D(items, p.W, p.H, 0); ok {
			papers[p.Name] = true
			if len(frames) == 0 {
				for _, it := range items {
					frames = append(frames, sbFootprint(it.Box, out[it.ID]))
				}
			}
		}
	}
	if !papers["A3"] || papers["A4"] {
		t.Fatalf("papers that fit: %v, want A3 the smallest", papers)
	}
	for i := range frames {
		for j := i + 1; j < len(frames); j++ {
			if sbOverlap(sbGrow(frames[i], 2), frames[j]) {
				t.Errorf("frames %d and %d closer than the gap: %v %v", i, j, frames[i], frames[j])
			}
		}
	}
	var zs []SchematicZoneResult
	for _, it := range items {
		fp := sbFootprint(it.Box, kicad.Pt{})
		zs = append(zs, SchematicZoneResult{ID: it.ID, Frame: schFrameSpec{ID: it.ID, Rect: boxToEE(fp)}})
	}
	if sbPackRows(zs, 420, 297, 0) != nil {
		t.Error("fixture: the row packer should not fit A3 (the point of sbPack2D)")
	}
	// frames exactly one gap apart (floating-point noise) still fit
	off := []sbPackItem{{ID: "a", Box: kicad.Box{MinX: -311.12, MinY: -55.88, MaxX: 116.84, MaxY: 63.5}},
		{ID: "b", Box: kicad.Box{MinX: -311.12, MinY: -55.88, MaxX: 116.84, MaxY: 63.5}},
		{ID: "c", Box: kicad.Box{MinX: -210.79, MinY: -49.67, MaxX: 10.16, MaxY: 37.73}}}
	if _, _, ok := sbPack2D(off, 841, 594, 0); !ok {
		t.Error("stacked frames one gap apart rejected on A1")
	}
}

// The PWR_FLAG row sits under the frames above its own width only.
func TestSbFlagRowBottomOnlyFramesAbove(t *testing.T) {
	frames := []kicad.Box{{MinX: 15, MinY: 15, MaxX: 100, MaxY: 120}, {MinX: 300, MinY: 15, MaxX: 400, MaxY: 380}}
	if b := sbFlagRowBottom(frames, 80); b != 120 {
		t.Errorf("bottom %v, want 120 (the tall frame is right of the row)", b)
	}
	if b := sbFlagRowBottom(frames, 400); b != 380 {
		t.Errorf("bottom %v, want 380 (a wide row runs under the tall frame)", b)
	}
	if !sbFlagRowFits(frames, 80, 420, 297) {
		t.Error("a short row under the low frame should fit A3 beside a tall frame")
	}
}

// --zone-timeout: past the limit no further grid variant starts; the zone
// keeps the best drawing so far and says how many variants it skipped.
func TestSbZoneTimeoutKeepsBestSoFar(t *testing.T) {
	d, _, err := buildDesign(sbWatchdogSpec(), sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	layouts, notes := layoutZones(d, d.zoneInputs(), nil, sbLayoutOpts{NoPlanner: true, Jobs: 1, ZoneTimeout: time.Nanosecond})
	zl := layouts["io"]
	if zl == nil {
		t.Fatalf("no layout: %v", notes)
	}
	if !zl.TimedOut || zl.Skipped != len(sbGridVariants)-1 || zl.Variant != sbGridVariants[0].name() {
		t.Errorf("timed out %v, skipped %d, kept %q: want true, %d, the first variant", zl.TimedOut, zl.Skipped, zl.Variant, len(sbGridVariants)-1)
	}
	layouts, _ = layoutZones(d, d.zoneInputs(), nil, sbLayoutOpts{NoPlanner: true, Jobs: 1})
	if l := layouts["io"]; l == nil || l.TimedOut || l.Skipped != 0 {
		t.Errorf("no limit: %+v", l)
	}
}

// T taps: the third part of a node (the key switch beside its pull-up and
// series resistor) joins the node's wire in its middle instead of getting a
// label; no point carries two branches (EG-09).
func TestSbGridTapsThirdPartOfANode(t *testing.T) {
	d, zi, zl := sbWatchdogZone(t, sbGridVariants[0])
	q := sbZoneQualityOf(d, zi, zl, &sbMeasure{cache: map[string]kicad.SceneSymbol{}})
	for _, r := range q.Unwired {
		if strings.HasPrefix(r, "SW") {
			t.Errorf("%s not wired to its node (unwired %v)", r, q.Unwired)
		}
	}
	taps := 0
	ends := map[kicad.Pt]int{}
	for _, w := range zl.Wires {
		for _, o := range zl.Wires {
			for i := 0; i+1 < len(o); i++ {
				if sbOnInterior(w[0], [2]kicad.Pt{o[i], o[i+1]}) {
					taps++
					ends[w[0]]++
				}
			}
		}
	}
	if taps == 0 {
		t.Error("no wire starts on another wire's middle: no T tap drawn")
	}
	for p, n := range ends {
		if n > 1 {
			t.Errorf("%d branches at %v (a four-way junction)", n, p)
		}
	}
	if f := sbZoneFindings(d, zi, zl); len(f) > 0 {
		t.Errorf("tapped zone has findings: %v", f)
	}
}
