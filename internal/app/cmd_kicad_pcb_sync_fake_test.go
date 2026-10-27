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
}

func (f *fakeSync) inventory(value string) *kicad.SyncInventory {
	return &kicad.SyncInventory{KiCadVersion: "10.0.6", Outline: &kicad.SyncBox{MaxX: 20, MaxY: 20}, Footprints: []kicad.SyncFP{{
		ID: "u1", UUID: "u1", Ref: "R1", Path: fakeR1Path, Sheetname: "/", Sheetfile: "a.kicad_sch", FPID: "L:R", Value: value,
		Fields: map[string]string{}, Side: "F", Box: &kicad.SyncBox{MinX: 1, MinY: 1, MaxX: 3, MaxY: 2},
		Pads: []kicad.SyncPad{{Number: "1", Net: "GND", Type: "smd"}, {Number: "2", Net: "VCC", Type: "smd"}}}},
		Nets: []kicad.SyncBoardNet{{Name: "GND", Pads: 1}, {Name: "VCC", Pads: 1}}}
}

func (f *fakeSync) SyncRead(pcb string) (*kicad.SyncInventory, error) {
	if strings.Contains(pcb, ".pcbpilot-stage-") {
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

func (f *fakeSync) RunParity(pcb, pro, sch string, expected func(kicad.Violation) bool) kicad.ParityResult {
	return kicad.ParityResult{Status: gate.Pass, Detail: "fake parity"}
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
	files := map[string]string{"a.kicad_sch": "(kicad_sch)\n", "sub/b.kicad_sch": "(kicad_sch)\n", "a.kicad_pcb": "(kicad_pcb)\n",
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
		"report = hard link":        func(o *pcbSyncOpts) { o.dryRun, o.report = true, link },
		"report via ..":             func(o *pcbSyncOpts) { o.dryRun, o.report = true, in(filepath.Join("sub", "..", "net.xml")) },
		"plan-out = board":          func(o *pcbSyncOpts) { o.planOut = in("a.kicad_pcb") },
		"plan-out = netlist":        func(o *pcbSyncOpts) { o.planOut = in("net.xml") },
		"plan-out = report":         func(o *pcbSyncOpts) { o.planOut, o.report = in("x.json"), in("x.json") },
		"plan-out = default report": func(o *pcbSyncOpts) { o.planOut = in("a.pcb-sync.json") },
		"report KiCad extension":    func(o *pcbSyncOpts) { o.dryRun, o.report = true, in("new.kicad_sym") },
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

// TestPcbSyncReportHonoursLocks: the report is written with the KiCad lock
// check; a report that cannot be written makes the run fail and stdout
// says ok:false.
func TestPcbSyncReportHonoursLocks(t *testing.T) {
	dir, o, fake := fakeProject(t, fakeNetlist())
	fake.value = "1k"
	if err := os.WriteFile(filepath.Join(dir, "~a.kicad_pro.lck"), []byte(`{"hostname":"HOST","username":"user"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	o.env.Procs = func() ([]kfile.Proc, error) { return []kfile.Proc{{PID: 7, Name: "kicad.exe"}}, nil }
	o.check = true
	rep, code, stderr := runSyncCode(t, o)
	if code != 4 || rep.OK || !strings.Contains(stderr, "cannot write the report") || fileExists(filepath.Join(dir, "a.pcb-sync.json")) {
		t.Fatalf("exit %d ok %v %s", code, rep.OK, stderr)
	}
	o.report = filepath.Join(t.TempDir(), "r.json")
	if rep, code, _ = runSyncCode(t, o); code != 0 || !rep.OK || rep.Verdict != "in-sync" {
		t.Fatalf("report elsewhere: exit %d %s", code, rep.Verdict)
	}
}
