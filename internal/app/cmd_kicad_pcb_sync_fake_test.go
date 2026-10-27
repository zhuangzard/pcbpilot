package app

// pcb-sync command tests without KiCad: a fake backend stands in for the
// bridge and kicad-cli (the netlist comes from --netlist), so every refusal,
// file-safety rule and exit code of the command runs everywhere.

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kfile"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

const fakeR1Path = "/00000000-0000-0000-0000-000000000001"

// fakeNetlist is a kicadxml export of one resistor R1 (1k, L:R) between
// GND and VCC; extra adds nodes ("R1.3" puts a pin without a pad on VCC).
func fakeNetlist(extra ...string) string {
	vcc := `<node ref="R1" pin="2"/>`
	for _, e := range extra {
		r, p, _ := strings.Cut(e, ".")
		vcc += `<node ref="` + r + `" pin="` + p + `"/>`
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<export version="E"><design><source>a.kicad_sch</source><tool>Eeschema 10.0.6</tool>
<sheet number="1" name="/" tstamps="/"></sheet></design>
<components><comp ref="R1"><value>1k</value><footprint>L:R</footprint>
<fields><field name="Footprint">L:R</field></fields><libsource lib="d" part="R" description=""/>
<property name="Sheetname" value="Root"/><property name="Sheetfile" value="a.kicad_sch"/>
<sheetpath names="/" tstamps="/"/><tstamps>00000000-0000-0000-0000-000000000001</tstamps></comp></components>
<libparts/><nets><net code="1" name="GND"><node ref="R1" pin="1"/></net><net code="2" name="VCC">` + vcc + `</net></nets></export>
`
}

// fakeSync: the board holds R1 with value "2k" (one setValue to make);
// apply and the read-back of a staged board simulate the bridge.
type fakeSync struct {
	value   string // R1's value on the source board
	version string // pcbnew version the "bridge" reports
	onApply func(stage string)
	after   *kicad.SyncInventory
	// parityDru: the rules beside the board the parity check was given.
	parityDru []byte
	parity    gate.Status // the parity verdict to report ("" = pass)
	// onParity runs inside the parity check; paritySheets are the sheet
	// copies it was given; onRead runs inside every sync-read; reads lists
	// the boards read.
	onParity     func()
	paritySheets []string
	onRead       func()
	reads        []string
}

func (f *fakeSync) inventory(value string) *kicad.SyncInventory {
	return &kicad.SyncInventory{KiCadVersion: "10.0.6", Outline: &kicad.SyncBox{MaxX: 20, MaxY: 20}, Footprints: []kicad.SyncFP{{
		ID: "u1", UUID: "u1", Ref: "R1", Path: fakeR1Path, Sheetname: "/", Sheetfile: "a.kicad_sch", FPID: "L:R", Value: value,
		Fields: map[string]string{}, Side: "F", Box: &kicad.SyncBox{MinX: 1, MinY: 1, MaxX: 3, MaxY: 2},
		Pads: []kicad.SyncPad{{Number: "1", Net: "GND", Type: "smd"}, {Number: "2", Net: "VCC", Type: "smd"}}}},
		Nets: []kicad.SyncBoardNet{{Name: "GND", Pads: 1}, {Name: "VCC", Pads: 1}}}
}

func (f *fakeSync) SyncRead(pcb string) (*kicad.SyncInventory, error) {
	f.reads = append(f.reads, pcb)
	if f.onRead != nil {
		f.onRead()
	}
	if strings.Contains(pcb, ".pcbpilot-stage-") && f.after != nil {
		return f.after, nil
	}
	return f.inventory(f.value), nil
}

func (f *fakeSync) FPInfo(refs []kicad.FPRef) (map[string]kicad.SyncFPInfo, []kicad.SyncError, error) {
	m := map[string]kicad.SyncFPInfo{}
	for _, r := range refs {
		m[r.Lib+":"+r.Name] = kicad.SyncFPInfo{Pads: []kicad.SyncPad{{Number: "1", Type: "smd"}, {Number: "2", Type: "smd"}},
			Box: &kicad.SyncBox{MinX: -1, MinY: -1, MaxX: 1, MaxY: 1}}
	}
	return m, nil, nil
}

func (f *fakeSync) SyncApply(pcb string, plan *kicad.SyncPlan, stage, base string) (*kicad.SyncApplyResult, error) {
	inv := f.inventory(f.value)
	if pcb == "" {
		inv = f.inventory("1k")
	}
	for _, op := range plan.Mutating() {
		if op.Op == "setValue" {
			inv.Footprints[0].Value = *op.Value
		}
		if op.Op == "addFootprint" {
			f := kicad.SyncFP{ID: op.Key, UUID: op.Key, Ref: op.Ref, Path: op.Key, Sheetname: *op.Sheetname, Sheetfile: *op.Sheetfile,
				FPID: op.FPID, Value: *op.Value, Fields: op.Fields, Side: "F", Box: &kicad.SyncBox{MinX: 30, MinY: 0, MaxX: 32, MaxY: 2}}
			for _, n := range []string{"1", "2"} {
				f.Pads = append(f.Pads, kicad.SyncPad{Number: n, Net: op.PadNets[n], Type: "smd"})
			}
			inv.Footprints = append(inv.Footprints, f)
		}
	}
	f.after = inv
	// What SaveBoard leaves: the board and a project of its own.
	if err := os.WriteFile(filepath.Join(stage, base+".kicad_pcb"), []byte("(kicad_pcb (synced))\n"), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(stage, base+".kicad_pro"), []byte("{}\n"), 0o600); err != nil {
		return nil, err
	}
	if f.onApply != nil {
		f.onApply(stage)
	}
	v := f.version
	if v == "" {
		v = "10.0.6"
	}
	return &kicad.SyncApplyResult{KiCadVersion: v}, nil
}

func (f *fakeSync) RunParity(pcb, pro string, sheets []string, expected func(kicad.Violation) bool) kicad.ParityResult {
	f.paritySheets = sheets
	if f.onParity != nil {
		f.onParity()
	}
	f.parityDru, _ = os.ReadFile(boardSibling(pcb, ".kicad_dru"))
	st := gate.Pass
	if f.parity != "" {
		st = f.parity
	}
	return kicad.ParityResult{Status: st, Detail: "fake parity"}
}

const fakePro = `{"meta":{"filename":"a.kicad_pro"},"text_variables":{"STAGE":"neutral"},"net_settings":{"classes":[{"name":"Default","clearance":0.31}]}}` + "\n"

// fakeProject writes a project directory and an empty KiCad config.
func fakeProject(t *testing.T, netlist string) (dir string, o pcbSyncOpts, fake *fakeSync) {
	t.Helper()
	dir = t.TempDir()
	cfg := filepath.Join(t.TempDir(), "kicad-config")
	table := "(fp_lib_table\n  (version 7)\n)\n"
	for _, d := range []string{cfg, filepath.Join(cfg, "10.0")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "fp-lib-table"), []byte(table), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("KICAD_CONFIG_HOME", cfg)
	// a.kicad_sch references sub/b.kicad_sch and a sheet outside the
	// project directory (../ext-<dir>/c.kicad_sch).
	ext := "ext-" + filepath.Base(dir)
	files := map[string]string{
		"a.kicad_sch":     "(kicad_sch\n  (sheet (property \"Sheetfile\" \"sub/b.kicad_sch\"))\n  (sheet (property \"Sheetfile\" \"../" + ext + "/c.kicad_sch\"))\n)\n",
		"sub/b.kicad_sch": "(kicad_sch)\n", "../" + ext + "/c.kicad_sch": "(kicad_sch)\n", "a.kicad_pcb": "(kicad_pcb)\n",
		"a.kicad_pro": fakePro, "a.kicad_dru": "(version 1)\n", "net.xml": netlist,
		"fp-lib-table":         "(fp_lib_table\n  (version 7)\n  (lib (name \"L\")(type \"KiCad\")(uri \"${KIPRJMOD}/L.pretty\")(options \"\")(descr \"\"))\n)\n",
		"L.pretty/R.kicad_mod": "(footprint \"R\")\n"}
	for name, text := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	fake = &fakeSync{value: "2k"}
	o = syncOpts(filepath.Join(dir, "a.kicad_sch"), filepath.Join(dir, "a.kicad_pcb"))
	o.netlist, o.backend = filepath.Join(dir, "net.xml"), fake
	o.env = &kfile.Env{Host: "HOST", User: "user", Procs: func() ([]kfile.Proc, error) { return nil, nil }}
	return dir, o, fake
}

// snapshotDir hashes every file under dir (paths relative).
func snapshotDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out[rel] = syncFileSHA(t, p)
		}
		return nil
	})
	return out
}

func runSyncCode(t *testing.T, o pcbSyncOpts) (*pcbSyncReport, int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	err := runPcbSync(o, &out, &errb)
	code := 0
	if err != nil {
		var ec exitCodeError
		if !errors.As(err, &ec) {
			t.Fatalf("pcb-sync: %v", err)
		}
		code = ec.code
	}
	var rep pcbSyncReport
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
			t.Fatalf("report: %v\n%s", err, out.String())
		}
	}
	return &rep, code, errb.String()
}

// TestPcbSyncOutputAliasesRefused: --report and --plan-out may name no
// input or output file — by path, case variant or hard link — and no KiCad
// file; every collision is a usage error (exit 2) that writes nothing.
func TestPcbSyncOutputAliasesRefused(t *testing.T) {
	dir, base, _ := fakeProject(t, fakeNetlist())
	in := func(n string) string { return filepath.Join(dir, n) }
	if err := os.WriteFile(in("plan.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := in("hardlink.json")
	if err := os.Link(in("a.kicad_pcb"), link); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(o *pcbSyncOpts){
		"report = board":          func(o *pcbSyncOpts) { o.dryRun, o.report = true, in("a.kicad_pcb") },
		"report = project":        func(o *pcbSyncOpts) { o.dryRun, o.report = true, in("a.kicad_pro") },
		"report = rules":          func(o *pcbSyncOpts) { o.dryRun, o.report = true, in("a.kicad_dru") },
		"report = root sheet":     func(o *pcbSyncOpts) { o.dryRun, o.report = true, in("a.kicad_sch") },
		"report = sub sheet":      func(o *pcbSyncOpts) { o.dryRun, o.report = true, in(filepath.Join("sub", "b.kicad_sch")) },
		"report = netlist":        func(o *pcbSyncOpts) { o.check, o.report = true, in("net.xml") },
		"report = output board":   func(o *pcbSyncOpts) { o.out, o.report = in("o.kicad_pcb"), in("o.kicad_pcb") },
		"report = output project": func(o *pcbSyncOpts) { o.out, o.report = in("o.kicad_pcb"), in("o.kicad_pro") },
		"report = applied plan": func(o *pcbSyncOpts) {
			o.inPlace, o.applyPlan, o.revision, o.report = true, in("plan.json"), "r", in("plan.json")
		},
		"report = hard link":     func(o *pcbSyncOpts) { o.dryRun, o.report = true, link },
		"report via ..":          func(o *pcbSyncOpts) { o.dryRun, o.report = true, in(filepath.Join("sub", "..", "net.xml")) },
		"plan-out = board":       func(o *pcbSyncOpts) { o.planOut = in("a.kicad_pcb") },
		"plan-out = netlist":     func(o *pcbSyncOpts) { o.planOut = in("net.xml") },
		"plan-out = report":      func(o *pcbSyncOpts) { o.planOut, o.report = in("x.json"), in("x.json") },
		"report KiCad extension": func(o *pcbSyncOpts) { o.dryRun, o.report = true, in("new.kicad_sym") },
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		cases["report = board, other case"] = func(o *pcbSyncOpts) { o.dryRun, o.report = true, in("A.KICAD_PCB") }
		cases["report = netlist, other case"] = func(o *pcbSyncOpts) { o.dryRun, o.report = true, in("NET.XML") }
	}
	before := snapshotDir(t, dir)
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			o := base
			edit(&o)
			rep, code, stderr := runSyncCode(t, o)
			alias := strings.Contains(stderr, "never writes over") || strings.Contains(stderr, "are the same file") || strings.Contains(stderr, "KiCad file extension")
			if code != 2 || rep.Schema != "" || !alias {
				t.Fatalf("want exit 2 and no report, got %d (%s)", code, stderr)
			}
		})
	}
	if after := snapshotDir(t, dir); len(after) != len(before) {
		t.Fatalf("files were written: %v → %v", before, after)
	} else {
		for k, v := range before {
			if after[k] != v {
				t.Fatalf("%s changed", k)
			}
		}
	}
}

// TestPcbSyncPlanErrorsFailReadOnlyModes: a netlist pin without a pad
// (E_PIN_PAD_MISMATCH) leaves no op, but the board is still not in line:
// --check, --dry-run and --plan-out fail instead of "in-sync".
func TestPcbSyncPlanErrorsFailReadOnlyModes(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlist("R1.3"))
	fake.value = "1k"
	for _, mode := range []string{"check", "dry-run", "plan-out"} {
		m := o
		switch mode {
		case "check":
			m.check = true
		case "dry-run":
			m.dryRun = true
		default:
			m.planOut = filepath.Join(dir, "plan.json")
		}
		rep, code, _ := runSyncCode(t, m)
		if code != 1 || rep.OK || rep.Verdict != "plan-errors" || len(rep.Errors) == 0 || rep.Errors[0].Code != kicad.ErrPinPadMismatch {
			t.Fatalf("%s: exit %d %s %+v", mode, code, rep.Verdict, rep.Errors)
		}
	}
	if fileExists(filepath.Join(dir, "plan.json")) {
		t.Fatal("a plan with errors must not be written")
	}
}

// TestPcbSyncOutPublishesSourceProject: --out publishes the source
// project's bytes (non-default settings included), not what SaveBoard
// wrote, and the rules file along with it.
func TestPcbSyncOutPublishesSourceProject(t *testing.T) {
	dir, o, _ := fakeProject(t, fakeNetlist())
	o.out = filepath.Join(dir, "out", "o.kicad_pcb")
	rep, code, _ := runSyncCode(t, o)
	if code != 0 || !rep.OK || !rep.Committed || len(rep.Ops) != 1 {
		t.Fatalf("exit %d %+v %+v", code, rep.Gates, rep.Errors)
	}
	pro, _ := os.ReadFile(filepath.Join(dir, "out", "o.kicad_pro"))
	if string(pro) != fakePro {
		t.Fatalf("project settings lost: %q", pro)
	}
	if dru, _ := os.ReadFile(filepath.Join(dir, "out", "o.kicad_dru")); string(dru) != "(version 1)\n" {
		t.Fatalf("rules not carried: %q", dru)
	}
}

// TestPcbSyncSourceChanged: a source board or project edited after the
// plan — before the apply, or while the bridge ran — is refused (exit 4)
// and nothing is written.
func TestPcbSyncSourceChanged(t *testing.T) {
	for _, when := range []string{"before apply", "before commit"} {
		t.Run(when, func(t *testing.T) {
			dir, o, fake := fakeProject(t, fakeNetlist())
			o.out = filepath.Join(dir, "out", "o.kicad_pcb")
			edit := func() {
				if err := os.WriteFile(filepath.Join(dir, "a.kicad_pro"), []byte(`{"edited":true}`), 0o644); err != nil {
					t.Error(err)
				}
			}
			if when == "before apply" {
				pcbSyncBeforeApply = edit
				defer func() { pcbSyncBeforeApply = nil }()
			} else {
				fake.onApply = func(string) { edit() }
			}
			rep, code, _ := runSyncCode(t, o)
			if code != 4 || rep.OK || rep.Committed || len(rep.Errors) == 0 || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeChanged) {
				t.Fatalf("exit %d %+v", code, rep.Errors)
			}
			if fileExists(o.out) {
				t.Fatal("output written from a changed source")
			}
		})
	}
}

// TestPcbSyncCreateNeedsKiCad10: --create has no source board to read the
// version from; the version the bridge applied with is checked instead.
func TestPcbSyncCreateNeedsKiCad10(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlist())
	fake.version = "9.0.2"
	o.pcb, o.create, o.inPlace = filepath.Join(dir, "fresh.kicad_pcb"), true, true
	rep, code, _ := runSyncCode(t, o)
	if code != 5 || rep.Committed || rep.Errors[len(rep.Errors)-1].Code != errSyncCannotCheck || fileExists(o.pcb) {
		t.Fatalf("exit %d %+v", code, rep.Errors)
	}
}

// TestPcbSyncReportHonoursLocks: read-only runs write no report unless
// --report names one (so --check works while KiCad has the project open);
// an explicit report is written with the KiCad lock check, and one that
// cannot be written fails the run with ok:false on stdout.
func TestPcbSyncReportHonoursLocks(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlist())
	fake.value = "1k"
	if err := os.WriteFile(filepath.Join(dir, "~a.kicad_pro.lck"), []byte(`{"hostname":"HOST","username":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	o.env.Procs = func() ([]kfile.Proc, error) { return []kfile.Proc{{PID: 7, Name: "kicad.exe"}}, nil }
	o.check = true
	rep, code, _ := runSyncCode(t, o)
	if code != 0 || !rep.OK || rep.Verdict != "in-sync" || fileExists(filepath.Join(dir, "a.pcb-sync.json")) {
		t.Fatalf("--check without --report: exit %d %s", code, rep.Verdict)
	}
	o.report = filepath.Join(dir, "r.json")
	rep, code, stderr := runSyncCode(t, o)
	if code != 4 || rep.OK || !strings.Contains(stderr, "cannot write the report") || fileExists(o.report) {
		t.Fatalf("report beside an open project: exit %d ok %v %s", code, rep.OK, stderr)
	}
	o.report = filepath.Join(t.TempDir(), "r.json")
	if rep, code, _ = runSyncCode(t, o); code != 0 || !rep.OK || !fileExists(o.report) {
		t.Fatalf("report elsewhere: exit %d %s", code, rep.Verdict)
	}
}

// fakeNetlistR2 adds R2 (L:R, not on the board) to fakeNetlist.
func fakeNetlistR2() string {
	s := fakeNetlist()
	s = strings.Replace(s, `</components>`, `<comp ref="R2"><value>1k</value><footprint>L:R</footprint>
<fields><field name="Footprint">L:R</field></fields><libsource lib="d" part="R" description=""/>
<property name="Sheetname" value="Root"/><property name="Sheetfile" value="a.kicad_sch"/>
<sheetpath names="/" tstamps="/"/><tstamps>00000000-0000-0000-0000-000000000002</tstamps></comp></components>`, 1)
	return strings.Replace(s, `<net code="1" name="GND"><node ref="R1" pin="1"/>`, `<net code="1" name="GND"><node ref="R1" pin="1"/><node ref="R2" pin="1"/>`, 1)
}

type libCheckSync struct {
	*fakeSync
	libDirs []string
}

func (f *libCheckSync) SyncApply(pcb string, plan *kicad.SyncPlan, stage, base string) (*kicad.SyncApplyResult, error) {
	for _, op := range plan.Mutating() {
		if op.Op == "addFootprint" {
			f.libDirs = append(f.libDirs, op.LibDir)
		}
	}
	return f.fakeSync.SyncApply(pcb, plan, stage, base)
}

// TestPcbSyncLibraryChangedAfterPlan: the footprint bytes planned with are
// in the revision and are the bytes installed (copied into the stage); a
// library footprint edited between --plan-out and --apply-plan, or between
// the plan and the apply of one run, is refused (exit 4).
func TestPcbSyncLibraryChangedAfterPlan(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlistR2())
	fake.value = "1k"
	be := &libCheckSync{fakeSync: fake}
	o.backend = be
	mod := filepath.Join(dir, "L.pretty", "R.kicad_mod")
	planPath := filepath.Join(t.TempDir(), "plan.json")
	p := o
	p.planOut = planPath
	rep, code, _ := runSyncCode(t, p)
	if code != 0 || rep.Counts.Add != 1 || rep.Ops[0].FPSha == "" {
		t.Fatalf("plan: exit %d %+v", code, rep.Ops)
	}
	if err := os.WriteFile(mod, []byte(`(footprint "R" (moved))`), 0o644); err != nil {
		t.Fatal(err)
	}
	a := o
	a.inPlace, a.applyPlan, a.revision = true, planPath, rep.Revision
	if rep, code, _ = runSyncCode(t, a); code != 4 || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeRevision) {
		t.Fatalf("library edited after --plan-out: exit %d %+v", code, rep.Errors)
	}

	// In one run: the plan is made, then the library changes.
	o.out = filepath.Join(dir, "out", "o.kicad_pcb")
	pcbSyncBeforeApply = func() {
		if err := os.WriteFile(mod, []byte(`(footprint "R" (moved again))`), 0o644); err != nil {
			t.Error(err)
		}
	}
	rep, code, _ = runSyncCode(t, o)
	pcbSyncBeforeApply = nil
	if code != 4 || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeChanged) || fileExists(o.out) || len(be.libDirs) != 0 {
		t.Fatalf("library edited before the apply: exit %d %+v", code, rep.Errors)
	}
	// Unchanged: the bridge loads the staged copy.
	if rep, code, _ = runSyncCode(t, o); code != 0 || len(be.libDirs) != 1 || !strings.Contains(be.libDirs[0], ".pcbpilot-stage-") {
		t.Fatalf("exit %d lib dirs %v %+v", code, be.libDirs, rep.Errors)
	}
}

// TestPcbSyncRenameReferencedRefused: renaming a net that a netclass
// assignment names is refused (v0.11 does not rewrite the project).
func TestPcbSyncRenameReferencedRefused(t *testing.T) {
	dir, o, fake := fakeProject(t, strings.Replace(fakeNetlist(), `name="VCC"`, `name="/V5"`, 1))
	fake.value = "1k"
	pro := `{"meta":{"filename":"a.kicad_pro"},"net_settings":{"netclass_assignments":{"VCC":"Power"}}}`
	if err := os.WriteFile(filepath.Join(dir, "a.kicad_pro"), []byte(pro), 0o644); err != nil {
		t.Fatal(err)
	}
	o.dryRun = true
	rep, code, _ := runSyncCode(t, o)
	if code != 1 || rep.Verdict != "refused" || rep.Errors[len(rep.Errors)-1].Code != kicad.ErrRenameReferenced {
		t.Fatalf("exit %d %s %+v", code, rep.Verdict, rep.Errors)
	}
	o.renameNets = false
	if rep, code, _ = runSyncCode(t, o); code != 0 || rep.Counts.PadNet != 1 {
		t.Fatalf("--rename-nets=false: exit %d %+v", code, rep.Ops)
	}
}

// TestPcbSyncOutKeepsTargetProject: with no source project, an existing
// target project stays as it is (SaveBoard's default one is not published).
func TestPcbSyncOutKeepsTargetProject(t *testing.T) {
	dir, o, _ := fakeProject(t, fakeNetlist())
	if err := os.Remove(filepath.Join(dir, "a.kicad_pro")); err != nil {
		t.Fatal(err)
	}
	o.out = filepath.Join(dir, "out", "o.kicad_pcb")
	tpro := filepath.Join(dir, "out", "o.kicad_pro")
	if err := os.MkdirAll(filepath.Dir(tpro), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tpro, []byte(`{"target":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, code, _ := runSyncCode(t, o)
	if code != 0 || !rep.Committed {
		t.Fatalf("exit %d %+v", code, rep.Errors)
	}
	if b, _ := os.ReadFile(tpro); string(b) != `{"target":true}` {
		t.Fatalf("target project replaced: %s", b)
	}
	for _, f := range rep.Files {
		if strings.HasSuffix(f.Path, ".kicad_pro") {
			t.Fatalf("project in the transaction: %+v", rep.Files)
		}
	}
}

// TestPcbSyncRenameCheckedAgainstPublishedProject: without a source
// project --out keeps the target's; a rename that project references is
// refused, and the kept project changing before the commit is refused too.
func TestPcbSyncRenameCheckedAgainstPublishedProject(t *testing.T) {
	dir, o, fake := fakeProject(t, strings.Replace(fakeNetlist(), `name="VCC"`, `name="/V5"`, 1))
	fake.value = "1k"
	if err := os.Remove(filepath.Join(dir, "a.kicad_pro")); err != nil {
		t.Fatal(err)
	}
	o.out = filepath.Join(dir, "out", "o.kicad_pcb")
	tpro := filepath.Join(dir, "out", "o.kicad_pro")
	if err := os.MkdirAll(filepath.Dir(tpro), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tpro, []byte(`{"net_settings":{"netclass_assignments":{"VCC":"Power"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, code, _ := runSyncCode(t, o)
	if code != 1 || rep.Errors[len(rep.Errors)-1].Code != kicad.ErrRenameReferenced || fileExists(o.out) {
		t.Fatalf("rename referenced by the kept target project: exit %d %+v", code, rep.Errors)
	}
	// An unreferencing target project, edited while the bridge runs.
	if err := os.WriteFile(tpro, []byte(`{"meta":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.onApply = func(string) {
		if err := os.WriteFile(tpro, []byte(`{"meta":{"edited":true}}`), 0o644); err != nil {
			t.Error(err)
		}
	}
	rep, code, _ = runSyncCode(t, o)
	if code != 4 || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeChanged) || fileExists(o.out) {
		t.Fatalf("kept project changed before the commit: exit %d %+v", code, rep.Errors)
	}
}

// TestPcbSyncOutPublishesSourceRules: an output whose rules differ from
// the source's gets the source's (parity checks with them); with no source
// rules the output's stay.
func TestPcbSyncOutPublishesSourceRules(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlist())
	o.out = filepath.Join(dir, "out", "o.kicad_pcb")
	tdru := filepath.Join(dir, "out", "o.kicad_dru")
	if err := os.MkdirAll(filepath.Dir(tdru), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tdru, []byte("(version 1)\n(rule stale)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rep, code, _ := runSyncCode(t, o)
	if code != 0 || !rep.Committed {
		t.Fatalf("exit %d %+v", code, rep.Errors)
	}
	if b, _ := os.ReadFile(tdru); string(b) != "(version 1)\n" || string(fake.parityDru) != "(version 1)\n" {
		t.Fatalf("published rules %q, parity rules %q", b, fake.parityDru)
	}
	// No source rules: the output's are kept and checked with.
	if err := os.Remove(filepath.Join(dir, "a.kicad_dru")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tdru, []byte("(version 1)\n(rule mine)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake.value = "3k"
	if rep, code, _ = runSyncCode(t, o); code != 0 {
		t.Fatalf("exit %d %+v", code, rep.Errors)
	}
	if b, _ := os.ReadFile(tdru); string(b) != "(version 1)\n(rule mine)\n" || string(fake.parityDru) != string(b) {
		t.Fatalf("kept rules %q, parity rules %q", b, fake.parityDru)
	}
}

// TestPcbSyncOutputsAreOwnJSON: --report / --plan-out end in .json, and an
// existing file there must be a pcb-sync report or plan (never a library
// table, a config or someone else's JSON).
func TestPcbSyncOutputsAreOwnJSON(t *testing.T) {
	dir, o, _ := fakeProject(t, fakeNetlist())
	foreign := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(foreign, []byte(`{"schema":"other/1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(o *pcbSyncOpts){
		"report = fp-lib-table":    func(o *pcbSyncOpts) { o.dryRun, o.report = true, filepath.Join(dir, "fp-lib-table") },
		"report not .json":         func(o *pcbSyncOpts) { o.dryRun, o.report = true, filepath.Join(dir, "new.txt") },
		"report = foreign json":    func(o *pcbSyncOpts) { o.dryRun, o.report = true, foreign },
		"plan-out = sym-lib-table": func(o *pcbSyncOpts) { o.planOut = filepath.Join(dir, "sym-lib-table") },
		"plan-out = foreign json":  func(o *pcbSyncOpts) { o.planOut = foreign },
	}
	for name, edit := range cases {
		m := o
		edit(&m)
		if _, code, stderr := runSyncCode(t, m); code != 2 || stderr == "" {
			t.Errorf("%s: exit %d", name, code)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "fp-lib-table")); !strings.Contains(string(b), "fp_lib_table") {
		t.Fatal("fp-lib-table changed")
	}
	// Its own report is replaced, run after run.
	m := o
	m.dryRun, m.report = true, filepath.Join(dir, "r.json")
	for i := 0; i < 2; i++ {
		if _, code, _ := runSyncCode(t, m); code != 0 {
			t.Fatalf("run %d over its own report: exit %d", i, code)
		}
	}
}

// TestPcbSyncRevisionCoversProjectAndRules: the source rules, or a kept
// target project, edited between --plan-out and --apply-plan change the
// revision: refused (exit 4).
func TestPcbSyncRevisionCoversProjectAndRules(t *testing.T) {
	t.Run("source rules", func(t *testing.T) {
		dir, o, _ := fakeProject(t, fakeNetlist())
		plan := filepath.Join(t.TempDir(), "p.json")
		p := o
		p.planOut = plan
		rep, code, _ := runSyncCode(t, p)
		if code != 0 {
			t.Fatalf("plan: %d %+v", code, rep.Errors)
		}
		if err := os.WriteFile(filepath.Join(dir, "a.kicad_dru"), []byte("(version 1)\n(rule changed)\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		a := o
		a.inPlace, a.applyPlan, a.revision = true, plan, rep.Revision
		if rep, code, _ = runSyncCode(t, a); code != 4 || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeRevision) {
			t.Fatalf("exit %d %+v", code, rep.Errors)
		}
	})
	t.Run("kept target project", func(t *testing.T) {
		dir, o, _ := fakeProject(t, fakeNetlist())
		if err := os.Remove(filepath.Join(dir, "a.kicad_pro")); err != nil {
			t.Fatal(err)
		}
		o.out = filepath.Join(dir, "out", "o.kicad_pcb")
		tpro := boardSibling(o.out, ".kicad_pro")
		if err := os.MkdirAll(filepath.Dir(tpro), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(tpro, []byte(`{"meta":{}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		plan := filepath.Join(t.TempDir(), "p.json")
		p := o
		p.planOut = plan
		rep, code, _ := runSyncCode(t, p)
		if code != 0 {
			t.Fatalf("plan: %d %+v", code, rep.Errors)
		}
		if err := os.WriteFile(tpro, []byte(`{"meta":{},"text_variables":{"X":"y"}}`), 0o644); err != nil {
			t.Fatal(err)
		}
		a := o
		a.applyPlan, a.revision = plan, rep.Revision
		if rep, code, _ = runSyncCode(t, a); code != 4 || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeRevision) || fileExists(o.out) {
			t.Fatalf("exit %d %+v", code, rep.Errors)
		}
	})
}

// TestPcbSyncInPlaceNeedsParity: --in-place commits only after schematic
// parity passed; a parity check that could not run never commits, and a
// skipped one (--no-parity) commits only with --force-unverified.
func TestPcbSyncInPlaceNeedsParity(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlist())
	pcb := filepath.Join(dir, "a.kicad_pcb")
	before := syncFileSHA(t, pcb)
	o.inPlace = true
	fake.parity = gate.CannotCheck
	if rep, code, _ := runSyncCode(t, o); code != 5 || rep.Committed || syncFileSHA(t, pcb) != before {
		t.Fatalf("parity cannot-check: exit %d committed %v", code, rep.Committed)
	}
	fake.parity = ""
	o.noParity = true
	if rep, code, _ := runSyncCode(t, o); code != 5 || rep.Committed || syncFileSHA(t, pcb) != before {
		t.Fatalf("--no-parity alone: exit %d committed %v", code, rep.Committed)
	}
	o.forceUnverified = true
	if rep, code, _ := runSyncCode(t, o); code != 5 || !rep.Committed || syncFileSHA(t, pcb) == before {
		t.Fatalf("--no-parity --force-unverified: exit %d committed %v", code, rep.Committed)
	}
	o.noParity = false
	if _, code, _ := runSyncCode(t, o); code != 2 {
		t.Fatalf("--force-unverified without --no-parity: exit %d", code)
	}
}

// TestPcbSyncInputsRecheckedBeforeCommit: a schematic sheet or the
// --netlist file edited while the sync runs refuses the commit (exit 4).
func TestPcbSyncInputsRecheckedBeforeCommit(t *testing.T) {
	for _, f := range []string{filepath.Join("sub", "b.kicad_sch"), "a.kicad_sch", "net.xml"} {
		t.Run(f, func(t *testing.T) {
			dir, o, fake := fakeProject(t, fakeNetlist())
			o.out = filepath.Join(dir, "out", "o.kicad_pcb")
			fake.onApply = func(string) {
				p := filepath.Join(dir, f)
				b, _ := os.ReadFile(p)
				if err := os.WriteFile(p, append(b, ' '), 0o644); err != nil {
					t.Error(err)
				}
			}
			rep, code, _ := runSyncCode(t, o)
			if code != 4 || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeChanged) || fileExists(o.out) {
				t.Fatalf("exit %d %+v", code, rep.Errors)
			}
		})
	}
}

// TestPcbSyncExternalSheetRechecked: a sheet outside the project directory
// (../) is part of the inputs — copied for the export and parity, and an
// edit to it before the commit is refused.
func TestPcbSyncExternalSheetRechecked(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlist())
	ext := filepath.Join(filepath.Dir(dir), "ext-"+filepath.Base(dir), "c.kicad_sch")
	o.out = filepath.Join(dir, "out", "o.kicad_pcb")
	rep, code, _ := runSyncCode(t, o)
	if code != 0 || len(fake.paritySheets) != 3 {
		t.Fatalf("exit %d, parity sheets %v %+v", code, fake.paritySheets, rep.Errors)
	}
	for _, p := range fake.paritySheets {
		if pathAlias(p, ext) || pathAlias(p, filepath.Join(dir, "a.kicad_sch")) {
			t.Fatalf("parity must run on copies, got %s", p)
		}
	}
	if err := os.Remove(o.out); err != nil {
		t.Fatal(err)
	}
	fake.onApply = func(string) {
		if err := os.WriteFile(ext, []byte("(kicad_sch (edited))\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	rep, code, _ = runSyncCode(t, o)
	if code != 4 || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeChanged) || fileExists(o.out) {
		t.Fatalf("external sheet edited: exit %d %+v", code, rep.Errors)
	}
}

// TestPcbSyncNoopVerifiesStagedCopy: an in-place run with nothing to do
// verifies a staged copy of the bytes read, not the live board, and an edit
// of the board while it is checked turns the success into exit 4.
func TestPcbSyncNoopVerifiesStagedCopy(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlist())
	fake.value = "1k"
	pcb := filepath.Join(dir, "a.kicad_pcb")
	o.inPlace = true
	rep, code, _ := runSyncCode(t, o)
	if code != 0 || len(rep.Ops) != 0 || rep.Committed || len(fake.reads) != 2 || !strings.Contains(fake.reads[1], ".pcbpilot-stage-") {
		t.Fatalf("no-op: exit %d reads %v", code, fake.reads)
	}
	fake.onParity = func() {
		if err := os.WriteFile(pcb, []byte("(kicad_pcb (edited))\n"), 0o644); err != nil {
			t.Error(err)
		}
	}
	rep, code, _ = runSyncCode(t, o)
	if code != 4 || rep.OK || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeChanged) {
		t.Fatalf("board edited during parity: exit %d %+v", code, rep.Errors)
	}
}

// TestPcbSyncOutputsWrittenOverTheirSnapshot: a report or plan file that
// appears (or changes) after the start is not overwritten.
func TestPcbSyncOutputsWrittenOverTheirSnapshot(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlist())
	foreign := []byte(`{"schema":"someone/else"}`)
	o.out = filepath.Join(dir, "out", "o.kicad_pcb")
	o.report = filepath.Join(dir, "r.json")
	fake.onApply = func(string) {
		if err := os.WriteFile(o.report, foreign, 0o644); err != nil {
			t.Error(err)
		}
	}
	rep, code, _ := runSyncCode(t, o)
	if b, _ := os.ReadFile(o.report); code != 4 || rep.OK || !bytes.Equal(b, foreign) {
		t.Fatalf("report appeared: exit %d ok %v file %s", code, rep.OK, b)
	}
	p := o
	p.out, p.report, p.planOut = "", "", filepath.Join(dir, "plan.json")
	fake.onApply = nil
	fake.onRead = func() {
		if err := os.WriteFile(p.planOut, foreign, 0o644); err != nil {
			t.Error(err)
		}
	}
	rep, code, _ = runSyncCode(t, p)
	if b, _ := os.ReadFile(p.planOut); code != 4 || !bytes.Equal(b, foreign) {
		t.Fatalf("plan appeared: exit %d file %s %+v", code, b, rep.Errors)
	}
}

// TestPcbSyncRevisionCoversTarget: an existing --out board edited between
// --plan-out and --apply-plan is refused (exit 4).
func TestPcbSyncRevisionCoversTarget(t *testing.T) {
	dir, o, _ := fakeProject(t, fakeNetlist())
	o.out = filepath.Join(dir, "out", "o.kicad_pcb")
	if _, code, _ := runSyncCode(t, o); code != 0 {
		t.Fatalf("first sync: %d", code)
	}
	plan := filepath.Join(t.TempDir(), "p.json")
	p := o
	p.planOut = plan
	rep, code, _ := runSyncCode(t, p)
	if code != 0 {
		t.Fatalf("plan: %d %+v", code, rep.Errors)
	}
	if err := os.WriteFile(o.out, []byte("(kicad_pcb (edited))\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := o
	a.applyPlan, a.revision = plan, rep.Revision
	if rep, code, _ = runSyncCode(t, a); code != 4 || rep.Errors[len(rep.Errors)-1].Code != string(kfile.CodeRevision) {
		t.Fatalf("target edited after the plan: exit %d %+v", code, rep.Errors)
	}
}
