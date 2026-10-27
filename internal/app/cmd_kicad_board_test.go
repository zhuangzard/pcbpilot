package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// fakeBoardTools stands in for KiCad: BoardOp appends a marker to the staged
// board (and writes the project pcbnew would), Stats / BoardDRC answer what
// the test sets.
type fakeBoardTools struct {
	during  func()           // runs inside BoardOp (a concurrent edit)
	stats   kicad.BoardStats // returned for every Stats call
	statErr error
	drc     []*kicad.DRCReport
	opErr   error
	calls   int
	druSeen []string // the .kicad_dru next to each board DRC ran on
}

func (f *fakeBoardTools) BoardOp(op string, spec kicad.BoardOpSpec, pcb, out string) (*kicad.BoardResult, error) {
	f.calls++
	if f.during != nil {
		f.during()
	}
	if f.opErr != nil {
		return nil, f.opErr
	}
	data := []byte("(kicad_pcb)\n")
	if pcb != "" {
		var err error
		if data, err = os.ReadFile(pcb); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(out, append(data, []byte("; "+op+"\n")...), 0o644); err != nil {
		return nil, err
	}
	if err := os.WriteFile(strings.TrimSuffix(out, ".kicad_pcb")+".kicad_pro", []byte("{}\n"), 0o644); err != nil {
		return nil, err
	}
	return &kicad.BoardResult{OK: true, Op: op, Created: []kicad.BoardItem{{Kind: "text", UUID: "u-new"}}, Verify: kicad.BoardVerification{Status: "pass"}}, nil
}

func (f *fakeBoardTools) Stats(string) (*kicad.BoardStats, error) {
	s := f.stats
	return &s, f.statErr
}

func (f *fakeBoardTools) BoardDRC(pcb, _ string) (*kicad.DRCReport, error) {
	if b, err := os.ReadFile(strings.TrimSuffix(pcb, ".kicad_pcb") + ".kicad_dru"); err == nil {
		f.druSeen = append(f.druSeen, string(b))
	} else {
		f.druSeen = append(f.druSeen, "")
	}
	if len(f.drc) == 0 {
		return &kicad.DRCReport{}, nil
	}
	r := f.drc[0]
	f.drc = f.drc[1:]
	return r, nil
}

func boardFixture(t *testing.T) (dir, pcb string) {
	t.Helper()
	dir = t.TempDir()
	pcb = filepath.Join(dir, "b.kicad_pcb")
	if err := os.WriteFile(pcb, []byte("(kicad_pcb (version 20260206))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.kicad_pro"), []byte(`{"meta":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, pcb
}

func textSpec() *kicad.TextSpec { return &kicad.TextSpec{Text: "rev A", X: 1, Y: 2} }

func issueCodes(r *kicadBoardReport) []string {
	var out []string
	for _, i := range r.Issues {
		out = append(out, i.Code)
	}
	return out
}

func TestKicadBoardPaths(t *testing.T) {
	dir, pcb := boardFixture(t)
	other := filepath.Join(dir, "new.kicad_pcb")
	cases := []struct {
		name string
		op   string
		o    kicadBoardOpts
		code string
	}{
		{"no pcb", "text", kicadBoardOpts{out: other}, kicad.ErrBoardArg},
		{"not a board", "text", kicadBoardOpts{pcb: filepath.Join(dir, "b.kicad_pro"), out: other}, kicad.ErrBoardArg},
		{"missing", "text", kicadBoardOpts{pcb: filepath.Join(dir, "x.kicad_pcb"), out: other}, kicad.ErrBoardArg},
		{"neither", "text", kicadBoardOpts{pcb: pcb}, kicad.ErrBoardArg},
		{"both", "text", kicadBoardOpts{pcb: pcb, out: other, inPlace: true}, kicad.ErrBoardArg},
		{"out is pcb", "text", kicadBoardOpts{pcb: pcb, out: pcb}, kicad.ErrBoardArg},
		{"out ext", "text", kicadBoardOpts{pcb: pcb, out: filepath.Join(dir, "x.pcb")}, kicad.ErrBoardArg},
		{"out exists", "text", kicadBoardOpts{pcb: pcb, out: pcb + ".kicad_pcb"}, ""},
		{"new with pcb", "new", kicadBoardOpts{pcb: pcb, out: other}, kicad.ErrBoardArg},
		{"new exists", "new", kicadBoardOpts{out: pcb}, kicad.ErrBoardOutExists},
	}
	if err := os.WriteFile(pcb+".kicad_pcb", nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cases[7].code = kicad.ErrBoardOutExists
	for _, c := range cases {
		err := boardCheckPaths(c.op, c.o)
		var be *kicad.BoardError
		if !errors.As(err, &be) || be.Issues[0].Code != c.code || be.Issues[0].Fix == "" {
			t.Errorf("%s: want %s with a fix, got %v", c.name, c.code, err)
		}
	}
	for _, o := range []kicadBoardOpts{{pcb: pcb, out: other}, {pcb: pcb, inPlace: true}} {
		if err := boardCheckPaths("text", o); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
	}
}

func TestKicadBoardFlagParsing(t *testing.T) {
	if v, err := parseNums("rect", "1,2.5,3,4", 4, 5); err != nil || v[1] != 2.5 {
		t.Fatalf("%v %v", v, err)
	}
	for _, bad := range []string{"1,2", "1,2,3,4,5,6", "1,2mm,3,4", "1,NaN,3,4", "1,,3,4"} {
		if _, err := parseNums("rect", bad, 4, 5); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	pts, err := parsePolygon("polygon", "0,0 80,0;80,50  0,50")
	if err != nil || len(pts) != 4 || pts[2] != (kicad.XY{80, 50}) {
		t.Fatalf("%v %v", pts, err)
	}
	if _, err := parsePolygon("polygon", "0,0 80"); err == nil {
		t.Error("half a point accepted")
	}
	s, err := shapeFlags{rect: "1,2,3,4,0.5"}.shape()
	if err != nil || s.Rect == nil || s.Rect.CornerRadius != 0.5 {
		t.Errorf("%+v %v", s, err)
	}
	if got := splitList(" F.Cu, ,B.Cu "); len(got) != 2 || got[1] != "B.Cu" {
		t.Errorf("splitList %v", got)
	}
}

func TestKicadBoardOutAndInPlace(t *testing.T) {
	dir, pcb := boardFixture(t)
	orig, _ := os.ReadFile(pcb)
	out := filepath.Join(dir, "sub", "new.kicad_pcb")
	f := &fakeBoardTools{}
	rep := &kicadBoardReport{}
	runKicadBoard(rep, "text", textSpec(), kicadBoardOpts{pcb: pcb, out: out}, f)
	if !rep.OK || rep.exit != 0 || rep.Verdict != gate.Pass {
		t.Fatalf("--out: %+v", rep)
	}
	if b, _ := os.ReadFile(pcb); !bytes.Equal(b, orig) {
		t.Error("--out modified the input")
	}
	if b, _ := os.ReadFile(out); !strings.HasSuffix(string(b), "; text\n") {
		t.Errorf("--out board: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "sub", "new.kicad_pro")); err != nil || len(rep.Written) != 2 {
		t.Errorf("--out project: %v, written %v", err, rep.Written)
	}

	rep = &kicadBoardReport{}
	runKicadBoard(rep, "text", textSpec(), kicadBoardOpts{pcb: pcb, inPlace: true}, f)
	if !rep.OK || len(rep.Written) != 1 {
		t.Fatalf("--in-place: %+v", rep)
	}
	if b, _ := os.ReadFile(pcb); !strings.HasSuffix(string(b), "; text\n") {
		t.Errorf("--in-place board: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.kicad_pro")); string(b) != `{"meta":{}}` {
		t.Errorf("--in-place rewrote the project: %q", b)
	}
}

func TestKicadBoardInPlaceChangedMeanwhile(t *testing.T) {
	_, pcb := boardFixture(t)
	edited := []byte("(kicad_pcb (version 20260206) (saved by KiCad))\n")
	f := &fakeBoardTools{during: func() { _ = os.WriteFile(pcb, edited, 0o644) }}
	rep := &kicadBoardReport{}
	runKicadBoard(rep, "text", textSpec(), kicadBoardOpts{pcb: pcb, inPlace: true}, f)
	if rep.OK || rep.exit != 4 || len(rep.Issues) == 0 || rep.Issues[0].Code != "E_FILE_CHANGED" {
		t.Fatalf("want E_FILE_CHANGED / exit 4, got exit %d %v", rep.exit, issueCodes(rep))
	}
	if b, _ := os.ReadFile(pcb); !bytes.Equal(b, edited) {
		t.Errorf("the concurrent save was overwritten: %q", b)
	}
}

func TestKicadBoardLocked(t *testing.T) {
	dir, pcb := boardFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "~b.kicad_pcb.lck"), []byte(`{"hostname":"elsewhere","username":"someone"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeBoardTools{}
	rep := &kicadBoardReport{}
	runKicadBoard(rep, "text", textSpec(), kicadBoardOpts{pcb: pcb, inPlace: true}, f)
	if rep.OK || rep.exit != 4 || len(rep.Issues) == 0 || rep.Issues[0].Code != "E_KICAD_LOCKED" || f.calls != 0 {
		t.Fatalf("want E_KICAD_LOCKED before any KiCad run, got exit %d %v calls %d", rep.exit, issueCodes(rep), f.calls)
	}
}

func TestKicadBoardRefusedAndFailedChecks(t *testing.T) {
	dir, pcb := boardFixture(t)
	orig, _ := os.ReadFile(pcb)
	// Bad input: refused before KiCad runs, exit 2.
	f := &fakeBoardTools{}
	rep := &kicadBoardReport{}
	runKicadBoard(rep, "text", &kicad.TextSpec{Text: ""}, kicadBoardOpts{pcb: pcb, inPlace: true}, f)
	if rep.exit != 2 || f.calls != 0 || rep.Verdict != gate.Fail {
		t.Fatalf("empty text: exit %d calls %d %v", rep.exit, f.calls, issueCodes(rep))
	}
	// board.py refusal (e.g. an unknown net): exit 2, nothing written.
	f = &fakeBoardTools{opErr: &kicad.BoardError{Op: "zone", Issues: []kicad.BoardIssue{{Code: kicad.ErrBoardNet, Field: "net", Detail: "x", Fix: "y"}}}}
	rep = &kicadBoardReport{}
	zs := &kicad.ZoneSpec{BoardOutline: true, Net: "GND", Layers: []string{"F.Cu"}}
	runKicadBoard(rep, "zone", zs, kicadBoardOpts{pcb: pcb, inPlace: true}, f)
	if rep.exit != 2 || rep.Issues[0].Code != kicad.ErrBoardNet {
		t.Fatalf("board.py refusal: exit %d %v", rep.exit, issueCodes(rep))
	}
	// kicad-cli disagrees with the op: fail, exit 6, nothing written.
	f = &fakeBoardTools{stats: kicad.BoardStats{HasOutline: true, Width: 10, Height: 10, Thickness: 1.6}}
	rep = &kicadBoardReport{}
	os_ := &kicad.OutlineSpec{Outer: kicad.BoardShape{Rect: &kicad.BoardRect{Width: 80, Height: 50}}, Replace: true}
	runKicadBoard(rep, "outline", os_, kicadBoardOpts{pcb: pcb, inPlace: true}, f)
	if rep.exit != 6 || rep.Verdict != gate.Fail || len(rep.Written) != 0 {
		t.Fatalf("stats mismatch: exit %d verdict %s %+v", rep.exit, rep.Verdict, rep.Checks)
	}
	// kicad-cli could not run: cannot-check, exit 5, nothing written.
	f = &fakeBoardTools{statErr: errors.New("kicad-cli crashed")}
	rep = &kicadBoardReport{}
	runKicadBoard(rep, "stackup", &kicad.StackupSpec{Layers: 4}, kicadBoardOpts{pcb: pcb, inPlace: true}, f)
	if rep.exit != 5 || rep.Verdict != gate.CannotCheck {
		t.Fatalf("stats error: exit %d verdict %s", rep.exit, rep.Verdict)
	}
	// --drc: a new error-level violation on an object the op made fails.
	f = &fakeBoardTools{drc: []*kicad.DRCReport{
		{Violations: []kicad.Violation{{Rule: "clearance", Objs: []string{"a", "b"}, Section: "violations"}}},
		{Violations: []kicad.Violation{{Rule: "clearance", Objs: []string{"b", "a"}, Section: "violations"}, {Rule: "silk_over_copper", Objs: []string{"u-new"}, Section: "violations"}}},
	}}
	rep = &kicadBoardReport{}
	runKicadBoard(rep, "text", textSpec(), kicadBoardOpts{pcb: pcb, inPlace: true, drc: true}, f)
	if rep.exit != 6 || rep.DRC == nil || len(rep.DRC.New) != 1 || !rep.DRC.New[0].Ours || rep.DRC.Before != 1 {
		t.Fatalf("--drc: exit %d %+v", rep.exit, rep.DRC)
	}
	if b, _ := os.ReadFile(pcb); !bytes.Equal(b, orig) {
		t.Error("a refused or failed op wrote the board")
	}
	_ = dir
}

func TestKicadBoardSpecFlagConflict(t *testing.T) {
	dir, pcb := boardFixture(t)
	spec := filepath.Join(dir, "o.json")
	if err := os.WriteFile(spec, []byte(`{"rect":{"x":0,"y":0,"width":10,"height":10}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	c := newKicadBoardCmd(&out)
	c.SetArgs([]string{"outline", "--pcb", pcb, "--in-place", "--spec", spec, "--rect", "5,5"})
	c.SetOut(io.Discard)
	c.SetErr(io.Discard)
	err := c.Execute()
	var ec exitCodeError
	if !errors.As(err, &ec) || ec.code != 2 {
		t.Fatalf("want exit 2, got %v", err)
	}
	var rep kicadBoardReport
	if json.Unmarshal(out.Bytes(), &rep) != nil || len(rep.Issues) != 1 || rep.Issues[0].Field != "rect" || rep.Issues[0].Code != kicad.ErrBoardArg {
		t.Fatalf("report: %s", out.String())
	}
	// A unit other than mm in a spec is refused, not converted.
	if err := os.WriteFile(spec, []byte(`{"units":"mil","layers":2}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var st kicad.StackupSpec
	if err := kicad.ParseBoardSpec("stackup", []byte(`{"units":"mil","layers":2}`), &st); err != nil {
		t.Fatal(err)
	}
	if bad := st.Validate(); len(bad) == 0 || bad[0].Code != kicad.ErrBoardUnits {
		t.Errorf("mil stackup: %v", bad)
	}
}

func TestKicadPlaceKeepsCutoutPolygon(t *testing.T) {
	// A 2 x 20 mm slot (mil, y-up) as the bridge writes an Edge.Cuts hole.
	slot := []any{787.4, -984.25, "L", 1574.8, -984.25, 1574.8, -905.5, 787.4, -905.5}
	snap := map[string]any{
		"outline": map[string]any{"points": [][2]float64{{0, 0}, {3150, 0}, {3150, -1969}, {0, -1969}},
			"bbox": map[string]float64{"minX": 0, "minY": -1969, "maxX": 3150, "maxY": 0}},
		"components": []any{},
		"copper": map[string]any{"fills": []any{map[string]any{"primitiveId": "edgecut-0", "layer": 12, "net": "", "source": slot,
			"bbox": map[string]float64{"minX": 787.4, "minY": -984.25, "maxX": 1574.8, "maxY": -905.5}}}},
	}
	raw, _ := json.Marshal(snap)
	b, _, _, err := kicadBoard(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Holes) != 1 || len(b.Holes[0].Poly) != 4 {
		t.Fatalf("cutout hole: %+v", b.Holes)
	}
	if bb := b.Holes[0].Bounds(); bb.W() < 787 || bb.H() < 78 {
		t.Errorf("cutout covers %.0f x %.0f mil, want the whole 787 x 79 mil slot", bb.W(), bb.H())
	}
}

func boardFixtureDru(t *testing.T) (dir, pcb string) {
	dir, pcb = boardFixture(t)
	if err := os.WriteFile(filepath.Join(dir, "b.kicad_dru"), []byte("(version 1)\n(rule \"x\" (constraint disallow text))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, pcb
}

func TestKicadBoardSidecarsVerifiedAndCommitted(t *testing.T) {
	dir, pcb := boardFixtureDru(t)
	dru, _ := os.ReadFile(filepath.Join(dir, "b.kicad_dru"))
	out := filepath.Join(dir, "o", "n.kicad_pcb")
	f := &fakeBoardTools{} // writes its own "{}" project next to the result, as pcbnew might
	rep := &kicadBoardReport{}
	runKicadBoard(rep, "text", textSpec(), kicadBoardOpts{pcb: pcb, out: out, drc: true}, f)
	if !rep.OK {
		t.Fatalf("%+v", rep)
	}
	if len(f.druSeen) != 2 || f.druSeen[0] != string(dru) || f.druSeen[1] != string(dru) {
		t.Errorf("DRC before/after ran with rules %q, want the input's on both", f.druSeen)
	}
	for ext, want := range map[string]string{".kicad_pro": `{"meta":{}}`, ".kicad_dru": string(dru)} {
		if b, err := os.ReadFile(filepath.Join(dir, "o", "n"+ext)); err != nil || string(b) != want {
			t.Errorf("--out %s: %q %v, want the verified source bytes", ext, b, err)
		}
	}
	if len(rep.Written) != 3 {
		t.Errorf("written %v", rep.Written)
	}
	// --out refuses a destination whose project or rules already exist.
	err := boardCheckPaths("text", kicadBoardOpts{pcb: pcb, out: filepath.Join(dir, "o", "n2.kicad_pcb")})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "o", "n2.kicad_dru"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var be *kicad.BoardError
	if err := boardCheckPaths("text", kicadBoardOpts{pcb: pcb, out: filepath.Join(dir, "o", "n2.kicad_pcb")}); !errors.As(err, &be) || be.Issues[0].Code != kicad.ErrBoardOutExists {
		t.Errorf("existing destination rules: %v", err)
	}
}

func TestKicadBoardSidecarChangedMeanwhile(t *testing.T) {
	for _, inPlace := range []bool{false, true} {
		dir, pcb := boardFixtureDru(t)
		orig, _ := os.ReadFile(pcb)
		o := kicadBoardOpts{pcb: pcb, inPlace: inPlace}
		if !inPlace {
			o.out = filepath.Join(dir, "n.kicad_pcb")
		}
		f := &fakeBoardTools{during: func() { _ = os.WriteFile(filepath.Join(dir, "b.kicad_dru"), []byte("(version 1)\n"), 0o644) }}
		rep := &kicadBoardReport{}
		runKicadBoard(rep, "text", textSpec(), o, f)
		if rep.OK || rep.exit != 4 || rep.Issues[0].Code != "E_FILE_CHANGED" {
			t.Fatalf("in-place %v: want E_FILE_CHANGED, got exit %d %v", inPlace, rep.exit, issueCodes(rep))
		}
		if b, _ := os.ReadFile(pcb); !bytes.Equal(b, orig) {
			t.Errorf("in-place %v: board written", inPlace)
		}
		if _, err := os.Stat(filepath.Join(dir, "n.kicad_pcb")); err == nil {
			t.Errorf("in-place %v: --out written", inPlace)
		}
	}
}

func TestKicadBoardReportAlias(t *testing.T) {
	dir, pcb := boardFixture(t)
	orig, _ := os.ReadFile(pcb)
	pro, _ := os.ReadFile(filepath.Join(dir, "b.kicad_pro"))
	link := filepath.Join(dir, "report.json")
	if err := os.Link(pcb, link); err != nil {
		t.Skip("hard links unsupported:", err)
	}
	out := filepath.Join(dir, "n.kicad_pcb")
	f := &fakeBoardTools{}
	old := boardLocate
	boardLocate = func() (kicadBoardTools, error) { return f, nil }
	defer func() { boardLocate = old }()
	run := func(args ...string) (int, kicadBoardReport) {
		var buf bytes.Buffer
		c := newKicadBoardCmd(&buf)
		c.SetArgs(args)
		c.SetOut(io.Discard)
		c.SetErr(io.Discard)
		code := 0
		var ec exitCodeError
		if err := c.Execute(); errors.As(err, &ec) {
			code = ec.code
		} else if err != nil {
			t.Fatal(err)
		}
		var rep kicadBoardReport
		_ = json.Unmarshal(buf.Bytes(), &rep)
		return code, rep
	}
	upper := filepath.Join(dir, "B.KICAD_PCB")
	for _, rp := range []string{pcb, filepath.Join(dir, "b.kicad_pro"), filepath.Join(dir, "b.kicad_dru"), filepath.Join(dir, "~b.kicad_pcb.lck"),
		out, filepath.Join(dir, "n.kicad_pro"), link, upper, filepath.Join(dir, "sub", "..", "b.kicad_pcb")} {
		for _, good := range []bool{true, false} {
			text := "rev A"
			if !good {
				text = ""
			}
			code, rep := run("text", "--pcb", pcb, "--out", out, "--text", text, "--at", "1,2", "--report", rp)
			if code != 2 || len(rep.Issues) != 1 || rep.Issues[0].Field != "report" {
				t.Errorf("--report %s (valid op %v): exit %d %v", rp, good, code, rep.Issues)
			}
		}
	}
	if b, _ := os.ReadFile(pcb); !bytes.Equal(b, orig) {
		t.Error("a refused report overwrote the board")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "b.kicad_pro")); !bytes.Equal(b, pro) {
		t.Error("a refused report overwrote the project")
	}
	if f.calls != 0 {
		t.Errorf("KiCad ran %d times for refused reports", f.calls)
	}
	// A report of its own: written, for a successful and a refused op.
	rp := filepath.Join(dir, "r.json")
	if code, _ := run("text", "--pcb", pcb, "--out", out, "--text", "rev A", "--at", "1,2", "--report", rp); code != 0 {
		t.Fatalf("good run: exit %d", code)
	}
	var rep kicadBoardReport
	if b, err := os.ReadFile(rp); err != nil || json.Unmarshal(b, &rep) != nil || !rep.OK {
		t.Errorf("report file: %v %+v", err, rep)
	}
	if code, _ := run("text", "--pcb", pcb, "--in-place", "--text", "", "--at", "1,2", "--report", rp); code != 2 {
		t.Errorf("refused op: exit %d", code)
	}
	if b, _ := os.ReadFile(rp); json.Unmarshal(b, &rep) != nil || rep.OK {
		t.Errorf("report of the refused run: %s", b)
	}
}

func TestKicadBoardDRCByDefault(t *testing.T) {
	_, pcb := boardFixture(t)
	orig, _ := os.ReadFile(pcb)
	keep := &kicad.KeepoutSpec{Shape: kicad.BoardShape{Rect: &kicad.BoardRect{X: 1, Y: 1, Width: 2, Height: 2}}}
	// A copper/hole op runs DRC without --drc, and a new error refuses it.
	f := &fakeBoardTools{drc: []*kicad.DRCReport{{}, {Violations: []kicad.Violation{{Rule: "clearance", Objs: []string{"u-new"}, Section: "violations"}}}}}
	rep := &kicadBoardReport{}
	runKicadBoard(rep, "keepout", keep, kicadBoardOpts{pcb: pcb, inPlace: true}, f)
	if rep.exit != 6 || len(f.druSeen) != 2 || len(rep.Written) != 0 {
		t.Fatalf("default DRC: exit %d, %d DRC runs, written %v", rep.exit, len(f.druSeen), rep.Written)
	}
	// --no-drc: unverified, exit 5, nothing written.
	f = &fakeBoardTools{}
	rep = &kicadBoardReport{}
	runKicadBoard(rep, "keepout", keep, kicadBoardOpts{pcb: pcb, inPlace: true, noDRC: true}, f)
	if rep.exit != 5 || rep.Verdict != gate.Skip || rep.OK || len(rep.Written) != 0 || len(f.druSeen) != 0 {
		t.Fatalf("--no-drc: exit %d verdict %s ok %v written %v", rep.exit, rep.Verdict, rep.OK, rep.Written)
	}
	if b, _ := os.ReadFile(pcb); !bytes.Equal(b, orig) {
		t.Fatal("--no-drc wrote the board")
	}
	// --no-drc --force-unverified: written, still exit 5 and not ok.
	rep = &kicadBoardReport{}
	runKicadBoard(rep, "keepout", keep, kicadBoardOpts{pcb: pcb, inPlace: true, noDRC: true, forceUnverified: true}, f)
	if rep.exit != 5 || rep.OK || len(rep.Written) != 1 {
		t.Fatalf("--force-unverified: exit %d ok %v written %v", rep.exit, rep.OK, rep.Written)
	}
	// --force-unverified alone is refused; text runs DRC only with --drc.
	rep = &kicadBoardReport{}
	runKicadBoard(rep, "keepout", keep, kicadBoardOpts{pcb: pcb, inPlace: true, forceUnverified: true}, f)
	if rep.exit != 2 {
		t.Errorf("--force-unverified without --no-drc: exit %d", rep.exit)
	}
	f = &fakeBoardTools{}
	rep = &kicadBoardReport{}
	runKicadBoard(rep, "text", textSpec(), kicadBoardOpts{pcb: pcb, inPlace: true}, f)
	if !rep.OK || len(f.druSeen) != 0 {
		t.Errorf("text without --drc ran DRC %d times", len(f.druSeen))
	}
}
