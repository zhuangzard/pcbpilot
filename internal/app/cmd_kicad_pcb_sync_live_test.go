package app

// Live tests of `kicad pcb-sync` against the installed KiCad 10 on copies
// of KiCad's shipped demos (never the demos themselves). Skipped unless
// PCBPILOT_KICAD_LIVE=1. Schematic edits are made with pcbpilot's own
// schematic editor (internal/kicad.SchEditor) or, for the symbol flags it
// has no setter for, a minimal text edit of one symbol's flag.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kfile"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func pcbSyncLive(t *testing.T) *kicad.Tools {
	t.Helper()
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Fatal(err)
	}
	return kt
}

// demoCopy copies KiCad's demo project name into a temp directory.
func demoCopy(t *testing.T, name string) string {
	t.Helper()
	var cands []string
	if v := os.Getenv("PCBPILOT_KICAD_DEMOS"); v != "" {
		cands = append(cands, v)
	}
	if sd := kicad.StockSymbolDir(); sd != "" {
		cands = append(cands, filepath.Join(sd, "..", "demos"))
	}
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		m, _ := filepath.Glob(filepath.Join(la, "Programs", "KiCad", "*", "share", "kicad", "demos"))
		cands = append(cands, m...)
	}
	for _, c := range cands {
		src := filepath.Join(c, name)
		if st, err := os.Stat(src); err == nil && st.IsDir() {
			dst := filepath.Join(t.TempDir(), name)
			if err := os.CopyFS(dst, os.DirFS(src)); err != nil {
				t.Fatal(err)
			}
			return dst
		}
	}
	t.Skipf("KiCad demo %s not installed", name)
	return ""
}

// runSync runs pcb-sync in process and returns the report and exit code.
func runSync(t *testing.T, o pcbSyncOpts) (*pcbSyncReport, int) {
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
	if errb.Len() > 0 {
		t.Logf("stderr: %s", errb.String())
	}
	return &rep, code
}

func syncOpts(sch, pcb string) pcbSyncOpts {
	return pcbSyncOpts{sch: sch, pcb: pcb, match: kicad.SyncMatchPathOrRef, orphans: "flag", renameNets: true,
		staging: "auto", stagingGap: 2, breakLock: "none"}
}

func gateOf(rep *pcbSyncReport, name string) pcbSyncGate {
	for _, g := range rep.Gates {
		if g.Name == name {
			return g
		}
	}
	return pcbSyncGate{Name: name, Status: "missing"}
}

func wantGates(t *testing.T, rep *pcbSyncReport, want map[string]gate.Status) {
	t.Helper()
	for n, st := range want {
		if g := gateOf(rep, n); g.Status != st {
			t.Errorf("gate %s: %s, want %s (%s; %v)", n, g.Status, st, g.Detail, g.Items)
		}
	}
}

func countOps(rep *pcbSyncReport) map[string]int {
	m := map[string]int{}
	for _, o := range rep.Ops {
		m[o.Op]++
	}
	return m
}

func syncFileSHA(t *testing.T, p string) string {
	t.Helper()
	s, err := kfile.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	return s.SHA256
}

func editSch(t *testing.T, path string, f func(e *kicad.SchEditor)) {
	t.Helper()
	e, err := kicad.OpenSchematicFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f(e)
	text, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPcbSyncLiveComplexHierarchy: KiCad's complex_hierarchy demo (one
// sheet used twice). Recorded baseline: the board names every footprint
// from the project library while the symbols name the stock libraries, so
// the plan is exactly 68 swaps (KiCad's own parity DRC reports those 68
// footprint_symbol_mismatch items) and nothing else. After the sync all
// three gates pass and --check finds no drift.
func TestPcbSyncLiveComplexHierarchy(t *testing.T) {
	pcbSyncLive(t)
	dir := demoCopy(t, "complex_hierarchy")
	sch, pcb := filepath.Join(dir, "complex_hierarchy.kicad_sch"), filepath.Join(dir, "complex_hierarchy.kicad_pcb")
	o := syncOpts(sch, pcb)
	o.dryRun = true
	rep, code := runSync(t, o)
	if code != 0 || rep.Counts.Swap != 68 || len(rep.Ops) != 68 || rep.Match.ByPath != 68 {
		t.Fatalf("baseline: exit %d counts %+v ops %d match %+v errors %+v", code, rep.Counts, len(rep.Ops), rep.Match, rep.Errors)
	}
	before := syncFileSHA(t, pcb)
	o = syncOpts(sch, pcb)
	o.out = filepath.Join(dir, "synced", "complex_hierarchy.kicad_pcb")
	rep, code = runSync(t, o)
	if code != 0 || !rep.Committed {
		t.Fatalf("sync: exit %d committed %v gates %+v errors %+v", code, rep.Committed, rep.Gates, rep.Errors)
	}
	wantGates(t, rep, map[string]gate.Status{"pad-net": gate.Pass, "schematic-parity": gate.Pass, "idempotent": gate.Pass})
	if syncFileSHA(t, pcb) != before {
		t.Fatal("--out modified the input board")
	}
	c := syncOpts(sch, o.out)
	c.check = true
	if rep, code = runSync(t, c); code != 0 || rep.Verdict != "in-sync" {
		t.Fatalf("--check after the sync: exit %d %s %+v", code, rep.Verdict, rep.Ops)
	}
	if entries, _ := filepath.Glob(filepath.Join(dir, "synced", ".pcbpilot-stage-*")); len(entries) != 0 {
		t.Fatalf("stage directory left behind: %v", entries)
	}
}

// ecc83Synced is the ecc83 demo copy after a first in-place sync (6 swaps
// to the stock libraries, sheet names "Racine" → "/"), so later edits are
// measured against a board in line with its schematic.
func ecc83Synced(t *testing.T) (dir, sch, pcb string) {
	t.Helper()
	dir = demoCopy(t, "ecc83")
	sch, pcb = filepath.Join(dir, "ecc83-pp.kicad_sch"), filepath.Join(dir, "ecc83-pp.kicad_pcb")
	o := syncOpts(sch, pcb)
	o.inPlace, o.noParity, o.forceUnverified = true, true, true
	rep, code := runSync(t, o)
	if code != 5 || !rep.Committed || rep.Counts.Swap != 6 || rep.Counts.Update != 15 {
		t.Fatalf("first sync: exit %d committed %v counts %+v errors %+v", code, rep.Committed, rep.Counts, rep.Errors)
	}
	return dir, sch, pcb
}

func footprintsByRef(t *testing.T, kt *kicad.Tools, pcb string) (map[string]kicad.SyncFP, *kicad.SyncInventory) {
	t.Helper()
	inv, err := kt.SyncRead(pcb)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]kicad.SyncFP{}
	for _, f := range inv.Footprints {
		m[f.Ref] = f
	}
	return m, inv
}

func copperTotals(inv *kicad.SyncInventory) (tracks, vias, zones int) {
	for _, n := range inv.Nets {
		tracks += n.Tracks
		vias += n.Vias
		zones += n.Zones
	}
	return
}

// TestPcbSyncLiveEcc83Edits: value change, new symbol, deleted symbol and a
// renamed net, made with pcbpilot's schematic editor, then one in-place
// sync: every gate passes, the new footprint is staged outside the
// outline, existing footprints keep their place, no copper is moved or
// lost, the renamed net keeps its tracks.
func TestPcbSyncLiveEcc83Edits(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	before, inv0 := footprintsByRef(t, kt, pcb)
	tr0, vi0, zo0 := copperTotals(inv0)
	var cathodeTracks int
	for _, n := range inv0.Nets {
		if n.Name == "Net-(U1A-K)" {
			cathodeTracks = n.Tracks
		}
	}
	if cathodeTracks == 0 {
		t.Fatal("fixture: Net-(U1A-K) has no tracks")
	}
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("R2", "Value", "2.2K"); err != nil {
			t.Fatal(err)
		}
		pins, err := e.SymbolPinPositions("C2")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.AddLabel(kicad.LabelLocal, "CATHODE", pins["2"], 0, ""); err != nil {
			t.Fatal(err)
		}
		if _, err := e.DeleteSymbol("R4"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.PlaceSymbol(kicad.SymbolInstance{LibID: "ecc83-pp:R", Ref: "R5", Value: "1M",
			Footprint: "Resistor_THT:R_Axial_DIN0207_L6.3mm_D2.5mm_P7.62mm_Horizontal", At: kicad.Pt{X: 25.4, Y: 190.5}}); err != nil {
			t.Fatal(err)
		}
		rp, err := e.SymbolPinPositions("R5")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.AddLabel(kicad.LabelLocal, "FB", rp["1"], 0, ""); err != nil {
			t.Fatal(err)
		}
	})
	pro := boardSibling(pcb, ".kicad_pro")
	proBefore := syncFileSHA(t, pro)
	o := syncOpts(sch, pcb)
	o.inPlace, o.orphans = true, "remove"
	rep, code := runSync(t, o)
	if code != 0 || !rep.Committed {
		t.Fatalf("sync: exit %d committed %v gates %+v errors %+v ops %+v", code, rep.Committed, rep.Gates, rep.Errors, rep.Ops)
	}
	if syncFileSHA(t, pro) != proBefore || len(rep.Files) != 1 {
		t.Fatalf("--in-place must write the board only: %+v", rep.Files)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(pcb), ".pcbpilot-stage-*")); len(left) != 0 {
		t.Fatalf("stage directory left behind: %v", left)
	}
	wantGates(t, rep, map[string]gate.Status{"pad-net": gate.Pass, "schematic-parity": gate.Pass, "idempotent": gate.Pass})
	n := countOps(rep)
	if n["setValue"] != 1 || n["addFootprint"] != 1 || n["orphan"] != 1 || rep.Counts.OrphanRemove != 1 || n["renameNet"] != 1 {
		t.Fatalf("ops %v: %+v", n, rep.Ops)
	}
	for _, op := range rep.Ops {
		if op.Op == "renameNet" && (op.From != "Net-(U1A-K)" || op.To != "/CATHODE") {
			t.Fatalf("rename %+v", op)
		}
	}
	after, inv1 := footprintsByRef(t, kt, pcb)
	if _, ok := after["R4"]; ok {
		t.Fatal("R4 not removed")
	}
	r5, ok := after["R5"]
	if !ok || inv1.Outline == nil || r5.Box.MinX <= inv1.Outline.MaxX {
		t.Fatalf("R5 must be staged right of the outline: %+v outline %+v", r5.Box, inv1.Outline)
	}
	for ref, f := range before {
		if ref == "R4" {
			continue
		}
		g := after[ref]
		if g.X != f.X || g.Y != f.Y || g.Rot != f.Rot || g.Side != f.Side {
			t.Errorf("%s moved: %v,%v,%v,%s → %v,%v,%v,%s", ref, f.X, f.Y, f.Rot, f.Side, g.X, g.Y, g.Rot, g.Side)
		}
	}
	if tr1, vi1, zo1 := copperTotals(inv1); tr1 != tr0 || vi1 != vi0 || zo1 != zo0 {
		t.Fatalf("copper changed: tracks %d→%d vias %d→%d zones %d→%d", tr0, tr1, vi0, vi1, zo0, zo1)
	}
	for _, nn := range inv1.Nets {
		if nn.Name == "Net-(U1A-K)" {
			t.Fatal("the old net name is still on the board")
		}
		if nn.Name == "/CATHODE" && nn.Tracks != cathodeTracks {
			t.Fatalf("/CATHODE has %d tracks, want %d", nn.Tracks, cathodeTracks)
		}
	}
	if len(rep.OrphanCopper) == 0 {
		t.Fatal("R4's removal leaves copper on its nets: orphanCopper must say so")
	}
}

// TestPcbSyncLiveFootprintSwap: a new Footprint field swaps the footprint
// and keeps its position, rotation, side and link.
func TestPcbSyncLiveFootprintSwap(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	// C2 carries a Footprint field naming its footprint (older boards do).
	pcbnewScript(t, kt, pcb, `fp = [f for f in b.GetFootprints() if f.GetReference() == "C2"][0]
fp.SetField("Footprint", str(fp.GetFPID().GetUniStringLibId()))
pcbnew.SaveBoard(sys.argv[1], b)
`)
	before, _ := footprintsByRef(t, kt, pcb)
	stale := `(property "Footprint" "` + before["C2"].FPID + `"`
	if b, _ := os.ReadFile(pcb); !strings.Contains(string(b), stale) {
		t.Fatal("fixture: no Footprint property written")
	}
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("C2", "Footprint", "Capacitor_THT:C_Disc_D5.0mm_W2.5mm_P5.00mm"); err != nil {
			t.Fatal(err)
		}
	})
	o := syncOpts(sch, pcb)
	o.inPlace = true
	rep, code := runSync(t, o)
	if code != 0 || rep.Counts.Swap != 1 || len(rep.Ops) != 1 {
		t.Fatalf("exit %d counts %+v ops %+v gates %+v", code, rep.Counts, rep.Ops, rep.Gates)
	}
	after, _ := footprintsByRef(t, kt, pcb)
	b, a := before["C2"], after["C2"]
	if a.FPID != "Capacitor_THT:C_Disc_D5.0mm_W2.5mm_P5.00mm" || a.X != b.X || a.Y != b.Y || a.Rot != b.Rot || a.Side != b.Side || a.Path != b.Path {
		t.Fatalf("swap: before %+v after %+v", b, a)
	}
	// KiCad 10 drops a stored Footprint property on load; whatever field
	// the result carries must name the new footprint, never the old one.
	if v, ok := a.Fields["Footprint"]; ok && v != a.FPID {
		t.Fatalf("the Footprint field must name the new footprint: %q (FPID %s)", v, a.FPID)
	}
	if b, _ := os.ReadFile(pcb); strings.Contains(string(b), stale) {
		t.Fatal("the board still names the old footprint in a Footprint property")
	}
}

// setSymbolFlag rewrites one flag of the symbol whose Reference is ref:
// the minimal text edit of a KiCad 10 sheet ("(dnp no)" → "(dnp yes)").
func setSymbolFlag(t *testing.T, path, ref, flag, val string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	at := strings.Index(s, `(property "Reference" "`+ref+`"`)
	if at < 0 {
		t.Fatalf("%s not in %s", ref, path)
	}
	start := max(strings.LastIndex(s[:at], "\n\t(symbol\n"), strings.LastIndex(s[:at], "\n\t(symbol\r\n")) // demos ship CRLF
	i := -1
	if start >= 0 {
		i = strings.Index(s[start:at], "\t\t("+flag+" ")
	}
	if i < 0 {
		t.Fatalf("%s has no %s flag", ref, flag)
	}
	i += start
	j := i + strings.Index(s[i:], ")")
	s = s[:i] + "\t\t(" + flag + " " + val + s[j:]
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPcbSyncLiveDNPAndExclude: DNP becomes the footprint's flag; a symbol
// taken off the board leaves an orphan that is flagged (parity sees the
// extra footprint as expected), and --orphans remove deletes it.
func TestPcbSyncLiveDNPAndExclude(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	setSymbolFlag(t, sch, "R1", "dnp", "yes")
	setSymbolFlag(t, sch, "R3", "on_board", "no")
	o := syncOpts(sch, pcb)
	o.inPlace = true
	rep, code := runSync(t, o)
	if code != 0 || rep.Counts.Update != 1 || rep.Counts.OrphanFlag != 1 {
		t.Fatalf("exit %d counts %+v ops %+v gates %+v", code, rep.Counts, rep.Ops, rep.Gates)
	}
	if g := gateOf(rep, "schematic-parity"); len(g.Info) != 1 || !strings.Contains(g.Info[0], "R3") {
		t.Fatalf("the flagged orphan must be the only, expected parity item: %+v", g)
	}
	fps, _ := footprintsByRef(t, kt, pcb)
	if !fps["R1"].DNP {
		t.Fatal("R1 is not DNP on the board")
	}
	if _, ok := fps["R3"]; !ok {
		t.Fatal("a flagged orphan must stay")
	}
	o.orphans = "remove"
	rep, code = runSync(t, o)
	if code != 0 || rep.Counts.OrphanRemove != 1 {
		t.Fatalf("remove: exit %d counts %+v gates %+v", code, rep.Counts, rep.Gates)
	}
	if fps, _ = footprintsByRef(t, kt, pcb); fps["R3"].Ref != "" {
		t.Fatal("R3 not removed")
	}
}

// TestPcbSyncLiveCreate: --create builds a board from the schematic alone.
func TestPcbSyncLiveCreate(t *testing.T) {
	kt := pcbSyncLive(t)
	dir := demoCopy(t, "ecc83")
	sch := filepath.Join(dir, "ecc83-pp.kicad_sch")
	// In the project directory: the symbols name the project library
	// (${KIPRJMOD}/footprints.pretty) as well as stock ones.
	pcb := filepath.Join(dir, "fresh.kicad_pcb")
	o := syncOpts(sch, pcb)
	o.inPlace, o.create = true, true
	rep, code := runSync(t, o)
	if code != 0 || !rep.Created || rep.Counts.Add != 15 || !rep.Committed {
		t.Fatalf("exit %d counts %+v gates %+v errors %+v", code, rep.Counts, rep.Gates, rep.Errors)
	}
	fps, inv := footprintsByRef(t, kt, pcb)
	if len(fps) != 15 || inv.Outline != nil || !fileExists(filepath.Join(dir, "fresh.kicad_pro")) {
		t.Fatalf("%d footprints, outline %+v", len(fps), inv.Outline)
	}
}

// TestPcbSyncLiveMultichannelIdempotent: the multichannel demo (one sheet
// used four times, many user fields). Recorded baseline: its symbols name
// footprint libraries a stock install does not have ("Capacitors THD", …;
// the board uses the project library), so a footprint update is refused
// with E_FP_LIB_UNRESOLVED and nothing is written — what KiCad's own update
// could not do either. Without footprints the plan is one value (J6 "CH1" →
// "Out"); after it, pad-net and idempotent pass and a second run is a no-op.
// (Parity is skipped: it reports every footprint_symbol_mismatch the
// excluded footprint update leaves.)
func TestPcbSyncLiveMultichannelIdempotent(t *testing.T) {
	pcbSyncLive(t)
	dir := demoCopy(t, "multichannel")
	sch, pcb := filepath.Join(dir, "multichannel_mixer.kicad_sch"), filepath.Join(dir, "multichannel_mixer.kicad_pcb")
	o := syncOpts(sch, pcb)
	o.out = filepath.Join(dir, "synced", "mixer.kicad_pcb")
	rep, code := runSync(t, o)
	if code != 1 || rep.Verdict != "refused" || len(rep.Errors) == 0 || rep.Errors[0].Code != kicad.ErrFPLibUnresolved || fileExists(o.out) {
		t.Fatalf("missing libraries: exit %d %s %+v", code, rep.Verdict, rep.Errors)
	}
	o.update, o.noParity = []string{"values", "fields", "refs", "dnp"}, true
	rep, code = runSync(t, o)
	if code != 5 || !rep.Committed || len(rep.Ops) != 1 || rep.Ops[0].Op != "setValue" || rep.Ops[0].Ref != "J6" {
		t.Fatalf("exit %d ops %+v gates %+v errors %+v", code, rep.Ops, rep.Gates, rep.Errors)
	}
	wantGates(t, rep, map[string]gate.Status{"pad-net": gate.Pass, "schematic-parity": gate.Skip, "idempotent": gate.Pass})
	c := syncOpts(sch, o.out)
	c.check, c.update = true, o.update
	if rep, code = runSync(t, c); code != 0 {
		t.Fatalf("second run is not a no-op: exit %d %+v", code, rep.Ops)
	}
}

// TestPcbSyncLiveLocksAndRevision: a KiCad lock refuses the write (exit 4,
// nothing written); --break-lock stale removes a lock left by a crashed
// session (fake process list: no KiCad running); a board edited between
// --plan-out and --apply-plan is E_REVISION_MISMATCH.
func TestPcbSyncLiveLocksAndRevision(t *testing.T) {
	pcbSyncLive(t)
	dir := demoCopy(t, "ecc83")
	sch, pcb := filepath.Join(dir, "ecc83-pp.kicad_sch"), filepath.Join(dir, "ecc83-pp.kicad_pcb")
	host, _ := os.Hostname()
	lock := filepath.Join(dir, "~ecc83-pp.kicad_pcb.lck")
	if err := os.WriteFile(lock, []byte(`{"hostname":"`+host+`","username":"tester"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	running := &kfile.Env{Host: host, User: "tester", Procs: func() ([]kfile.Proc, error) { return []kfile.Proc{{PID: 9, Name: "pcbnew.exe"}}, nil }}
	idle := &kfile.Env{Host: host, User: "tester", Procs: func() ([]kfile.Proc, error) { return nil, nil }}
	before := syncFileSHA(t, pcb)
	for _, env := range []*kfile.Env{running, idle} {
		o := syncOpts(sch, pcb)
		o.inPlace, o.noParity, o.forceUnverified, o.env = true, true, true, env
		rep, code := runSync(t, o)
		if code != 4 || rep.Committed || len(rep.Errors) == 0 || rep.Errors[0].Code != string(kfile.CodeLocked) {
			t.Fatalf("locked board: exit %d %+v", code, rep.Errors)
		}
		if syncFileSHA(t, pcb) != before {
			t.Fatal("a locked board was written")
		}
	}
	o := syncOpts(sch, pcb)
	o.inPlace, o.noParity, o.forceUnverified, o.env = true, true, true, running
	o.breakLock = "stale"
	if _, code := runSync(t, o); code != 4 {
		t.Fatalf("--break-lock stale must not remove a live lock: exit %d", code)
	}
	o.env = idle
	rep, code := runSync(t, o)
	if code != 5 || !rep.Committed || len(rep.Locks) != 1 || !rep.Locks[0].Broke {
		t.Fatalf("--break-lock stale on a stale lock: exit %d committed %v locks %+v errors %+v", code, rep.Committed, rep.Locks, rep.Errors)
	}
	if fileExists(lock) {
		t.Fatal("the stale lock is still there")
	}

	// Revision: plan, edit the board, apply.
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("R3", "Value", "1.5M"); err != nil {
			t.Fatal(err)
		}
	})
	planPath := filepath.Join(dir, "sync.plan.json")
	p := syncOpts(sch, pcb)
	p.planOut = planPath
	rep, code = runSync(t, p)
	if code != 0 || rep.Revision == "" || rep.Counts.Update != 1 {
		t.Fatalf("plan: exit %d %+v", code, rep)
	}
	rev := rep.Revision
	a := syncOpts(sch, pcb)
	a.inPlace, a.noParity, a.forceUnverified, a.applyPlan, a.revision = true, true, true, planPath, "0000"+rev[4:]
	if _, code = runSync(t, a); code != 4 {
		t.Fatalf("a wrong --revision must be refused: exit %d", code)
	}
	b, _ := os.ReadFile(pcb)
	if err := os.WriteFile(pcb, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	edited := syncFileSHA(t, pcb)
	a.revision = rev
	rep, code = runSync(t, a)
	if code != 4 || rep.Errors[0].Code != string(kfile.CodeRevision) || syncFileSHA(t, pcb) != edited {
		t.Fatalf("edited between plan and apply: exit %d %+v", code, rep.Errors)
	}
	// A fresh plan of the edited board applies.
	rep, code = runSync(t, p)
	a.revision = rep.Revision
	if rep, code = runSync(t, a); code != 5 || !rep.Committed {
		t.Fatalf("apply of a fresh plan: exit %d %+v", code, rep.Errors)
	}
}

// TestPcbSyncLiveGateNonVacuous: the gates judge the staged board against
// the schematic, not the plan. A plan with one pad net dropped makes the
// pad-net gate fail (exit 6) and --in-place leaves the board unchanged;
// with kicad-cli missing, the parity gate cannot check (exit 5).
func TestPcbSyncLiveGateNonVacuous(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	editSch(t, sch, func(e *kicad.SchEditor) {
		pins, err := e.SymbolPinPositions("C2")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.AddLabel(kicad.LabelLocal, "CATHODE", pins["2"], 0, ""); err != nil {
			t.Fatal(err)
		}
	})
	before := syncFileSHA(t, pcb)
	o := syncOpts(sch, pcb)
	o.inPlace, o.renameNets = true, false
	pcbSyncTamper = func(p *kicad.SyncPlan) *kicad.SyncPlan {
		q := *p
		q.Ops = nil
		dropped := false
		for _, op := range p.Ops {
			if op.Op == "setPadNet" && !dropped {
				dropped = true
				continue
			}
			q.Ops = append(q.Ops, op)
		}
		if !dropped {
			t.Error("fixture: no setPadNet to drop")
		}
		return &q
	}
	rep, code := runSync(t, o)
	pcbSyncTamper = nil
	if code != 6 || rep.Committed || gateOf(rep, "pad-net").Status != gate.Fail || gateOf(rep, "idempotent").Status != gate.Fail {
		t.Fatalf("tampered plan: exit %d committed %v gates %+v", code, rep.Committed, rep.Gates)
	}
	if syncFileSHA(t, pcb) != before {
		t.Fatal("--in-place wrote a board that failed verification")
	}

	// kicad-cli missing: pad-net and idempotent still run, parity cannot.
	netlist := filepath.Join(t.TempDir(), "net.xml")
	data, err := kicad.ExportSyncNetlist(kt.CLI, sch, kicad.NetlistKicadXML)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(netlist, data, 0o644); err != nil {
		t.Fatal(err)
	}
	o = syncOpts(sch, pcb)
	o.out = filepath.Join(filepath.Dir(pcb), "nocli", "b.kicad_pcb")
	o.netlist = netlist
	o.tools = &kicad.Tools{Python: kt.Python, CLI: filepath.Join(t.TempDir(), "no-kicad-cli.exe")}
	rep, code = runSync(t, o)
	if code != 5 || gateOf(rep, "schematic-parity").Status != gate.CannotCheck || gateOf(rep, "pad-net").Status != gate.Pass {
		t.Fatalf("missing kicad-cli: exit %d gates %+v", code, rep.Gates)
	}
}

// pcbnewScript runs a short pcbnew script on board (KiCad's Python) and
// returns its stdout.
func pcbnewScript(t *testing.T, kt *kicad.Tools, board, script string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.py")
	if err := os.WriteFile(p, []byte("import pcbnew, sys, json\nb = pcbnew.LoadBoard(sys.argv[1])\n"+script), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(kt.Python, p, board).CombinedOutput()
	if err != nil {
		t.Fatalf("pcbnew script: %v\n%s", err, out)
	}
	return string(out)
}

const readC2Overrides = `fp = [f for f in b.GetFootprints() if f.GetReference() == "C2"][0]
p1 = [p for p in fp.Pads() if p.GetNumber() == "1"][0]
print("RESULT" + json.dumps({"fpid": str(fp.GetFPID().GetUniStringLibId()), "clearance": fp.GetLocalClearance(),
    "zone": fp.GetLocalZoneConnection(), "courtyard": fp.AllowMissingCourtyard(),
    "mask": p1.GetLocalSolderMaskMargin(), "gap": p1.GetLocalThermalGapOverride()}))
`

func scriptResult(t *testing.T, out string) map[string]any {
	t.Helper()
	i := strings.LastIndex(out, "RESULT")
	if i < 0 {
		t.Fatalf("no result: %s", out)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out[i+6:])), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestPcbSyncLiveSwapKeepsOverrides: a swap carries the footprint's local
// clearance, zone connection and allow-missing-courtyard flag and each
// pad's local overrides onto the pad with the same number; a pad with
// overrides the new footprint has no pad for refuses the swap and the
// board is unchanged.
func TestPcbSyncLiveSwapKeepsOverrides(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	pcbnewScript(t, kt, pcb, `fp = [f for f in b.GetFootprints() if f.GetReference() == "C2"][0]
fp.SetLocalClearance(250000)
fp.SetLocalZoneConnection(pcbnew.ZONE_CONNECTION_NONE)
fp.SetAllowMissingCourtyard(True)
for p in fp.Pads():
    if p.GetNumber() == "1":
        p.SetLocalSolderMaskMargin(50000)
        p.SetThermalGap(300000)
pcbnew.SaveBoard(sys.argv[1], b)
`)
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("C2", "Footprint", "Capacitor_THT:C_Disc_D5.0mm_W2.5mm_P5.00mm"); err != nil {
			t.Fatal(err)
		}
	})
	o := syncOpts(sch, pcb)
	o.inPlace, o.noParity, o.forceUnverified = true, true, true
	if rep, code := runSync(t, o); code != 5 || !rep.Committed || rep.Counts.Swap != 1 {
		t.Fatalf("swap: exit %d %+v %+v", code, rep.Ops, rep.Errors)
	}
	got := scriptResult(t, pcbnewScript(t, kt, pcb, readC2Overrides))
	want := map[string]any{"fpid": "Capacitor_THT:C_Disc_D5.0mm_W2.5mm_P5.00mm", "clearance": 250000.0, "courtyard": true,
		"mask": 50000.0, "gap": 300000.0}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s after the swap: %v, want %v", k, got[k], v)
		}
	}
	if got["zone"] == -1.0 {
		t.Errorf("zone connection override lost: %v", got)
	}

	// Pad 2 gets an override; the new footprint has pad 1 only.
	pcbnewScript(t, kt, pcb, `fp = [f for f in b.GetFootprints() if f.GetReference() == "C2"][0]
[p for p in fp.Pads() if p.GetNumber() == "2"][0].SetLocalClearance(200000)
pcbnew.SaveBoard(sys.argv[1], b)
`)
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("C2", "Footprint", "TestPoint:TestPoint_THTPad_D1.5mm_Drill0.7mm"); err != nil {
			t.Fatal(err)
		}
	})
	before := syncFileSHA(t, pcb)
	rep, code := runSync(t, o)
	if code != 1 || rep.Committed || !strings.Contains(fmt.Sprint(rep.Errors), "has local overrides") || syncFileSHA(t, pcb) != before {
		t.Fatalf("a swap that would drop pad overrides must be refused: exit %d %+v", code, rep.Errors)
	}
}

// TestPcbSyncLiveNonASCII: a field value and a footprint library path with
// non-ASCII characters (the bridge reads its JSON as UTF-8, whatever the
// Windows code page) — the footprint is added with both intact.
func TestPcbSyncLiveNonASCII(t *testing.T) {
	kt := pcbSyncLive(t)
	dir, sch, pcb := ecc83Synced(t)
	stock := kicad.StockFootprintDir()
	if stock == "" {
		t.Skip("stock footprints not installed")
	}
	const name = "R_Axial_DIN0207_L6.3mm_D2.5mm_P7.62mm_Horizontal"
	lib := filepath.Join(dir, "库 Ω.pretty")
	b, err := os.ReadFile(filepath.Join(stock, "Resistor_THT.pretty", name+".kicad_mod"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, name+".kicad_mod"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	tbl := filepath.Join(dir, "fp-lib-table")
	tb, _ := os.ReadFile(tbl)
	text := strings.TrimRight(string(tb), " \r\n")
	text = strings.TrimSuffix(text, ")") + "  (lib (name \"LibU\")(type \"KiCad\")(uri \"${KIPRJMOD}/库 Ω.pretty\")(options \"\")(descr \"\"))\n)\n"
	if err := os.WriteFile(tbl, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	const note = "电阻 Ω µ — 1%"
	editSch(t, sch, func(e *kicad.SchEditor) {
		if _, err := e.PlaceSymbol(kicad.SymbolInstance{LibID: "ecc83-pp:R", Ref: "R5", Value: "10k", Footprint: "LibU:" + name,
			At: kicad.Pt{X: 25.4, Y: 190.5}, Fields: []kicad.Field{{Name: "Note", Value: note}}}); err != nil {
			t.Fatal(err)
		}
	})
	o := syncOpts(sch, pcb)
	o.inPlace = true
	rep, code := runSync(t, o)
	if code != 0 || rep.Counts.Add != 1 {
		t.Fatalf("exit %d %+v %+v %+v", code, rep.Ops, rep.Gates, rep.Errors)
	}
	fps, _ := footprintsByRef(t, kt, pcb)
	if r5 := fps["R5"]; r5.FPID != "LibU:"+name || r5.Fields["Note"] != note {
		t.Fatalf("R5 %q fields %q", r5.FPID, r5.Fields)
	}
}

// TestPcbSyncLivePublishesProject: with the real bridge, --out publishes
// the source project byte for byte (a non-default text variable kept).
func TestPcbSyncLivePublishesProject(t *testing.T) {
	pcbSyncLive(t)
	dir := demoCopy(t, "ecc83")
	sch, pcb := filepath.Join(dir, "ecc83-pp.kicad_sch"), filepath.Join(dir, "ecc83-pp.kicad_pcb")
	pro := filepath.Join(dir, "ecc83-pp.kicad_pro")
	var m map[string]any
	pb, _ := os.ReadFile(pro)
	if err := json.Unmarshal(pb, &m); err != nil {
		t.Fatal(err)
	}
	m["text_variables"] = map[string]any{"STAGE": "neutral"}
	pb, _ = json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(pro, pb, 0o644); err != nil {
		t.Fatal(err)
	}
	o := syncOpts(sch, pcb)
	o.out = filepath.Join(dir, "out", "e.kicad_pcb")
	rep, code := runSync(t, o)
	if code != 0 || !rep.Committed {
		t.Fatalf("exit %d %+v %+v", code, rep.Gates, rep.Errors)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "out", "e.kicad_pro")); !bytes.Equal(got, pb) {
		t.Fatalf("project not published as is:\n%s", got)
	}
}

// TestPcbSyncLiveSwapKeepsUUIDAndGroup: the replacement footprint keeps the
// board footprint's uuid and its place in a group.
func TestPcbSyncLiveSwapKeepsUUIDAndGroup(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	pcbnewScript(t, kt, pcb, `g = pcbnew.PCB_GROUP(b)
g.SetName("amp")
b.Add(g)
for f in b.GetFootprints():
    if f.GetReference() in ("C2", "R1"):
        g.AddItem(f)
pcbnew.SaveBoard(sys.argv[1], b)
`)
	before, _ := footprintsByRef(t, kt, pcb)
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("C2", "Footprint", "Capacitor_THT:C_Disc_D5.0mm_W2.5mm_P5.00mm"); err != nil {
			t.Fatal(err)
		}
	})
	o := syncOpts(sch, pcb)
	o.inPlace, o.noParity, o.forceUnverified = true, true, true
	if rep, code := runSync(t, o); code != 5 || !rep.Committed || rep.Counts.Swap != 1 {
		t.Fatalf("exit %d %+v %+v", code, rep.Ops, rep.Errors)
	}
	after, _ := footprintsByRef(t, kt, pcb)
	if after["C2"].UUID != before["C2"].UUID || after["C2"].FPID != "Capacitor_THT:C_Disc_D5.0mm_W2.5mm_P5.00mm" {
		t.Fatalf("uuid %s → %s (%s)", before["C2"].UUID, after["C2"].UUID, after["C2"].FPID)
	}
	got := scriptResult(t, pcbnewScript(t, kt, pcb, `out = {}
for f in b.GetFootprints():
    if f.GetReference() in ("C2", "R1"):
        g = f.GetParentGroup()
        out[f.GetReference()] = g.GetName() if g else ""
out["groups"] = [[g.GetName(), len(list(g.GetItems()))] for g in b.Groups()]
print("RESULT" + json.dumps(out))
`))
	if got["C2"] != "amp" || got["R1"] != "amp" || fmt.Sprint(got["groups"]) != "[[amp 2]]" {
		t.Fatalf("group after the swap: %v", got)
	}
}

// TestPcbSyncLiveDuplicatePadNumbers: two pads share a number and carry
// different overrides. A swap to a footprint with the same pads at the same
// positions keeps each pad's own settings; one whose pads moved cannot be
// matched and is refused.
func TestPcbSyncLiveDuplicatePadNumbers(t *testing.T) {
	kt := pcbSyncLive(t)
	dir, sch, pcb := ecc83Synced(t)
	stock := kicad.StockFootprintDir()
	src, err := os.ReadFile(filepath.Join(stock, "Resistor_THT.pretty", "R_Axial_DIN0207_L6.3mm_D2.5mm_P7.62mm_Horizontal.kicad_mod"))
	if err != nil {
		t.Skip("stock footprint not installed")
	}
	dup := strings.Replace(string(src), `(pad "2"`, `(pad "1"`, 1)
	moved := strings.Replace(dup, "(at 7.62 0)", "(at 5.08 0)", 1)
	lib := filepath.Join(dir, "dup.pretty")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"DupB": dup, "DupC": moved} {
		if err := os.WriteFile(filepath.Join(lib, name+".kicad_mod"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tbl := filepath.Join(dir, "fp-lib-table")
	tb, _ := os.ReadFile(tbl)
	text := strings.TrimSuffix(strings.TrimRight(string(tb), " \r\n"), ")") + "  (lib (name \"Dup\")(type \"KiCad\")(uri \"${KIPRJMOD}/dup.pretty\")(options \"\")(descr \"\"))\n)\n"
	if err := os.WriteFile(tbl, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	// R1 on the board: both pads numbered "1", different clearances.
	pcbnewScript(t, kt, pcb, `fp = [f for f in b.GetFootprints() if f.GetReference() == "R1"][0]
for p in fp.Pads():
    p.SetNumber("1")
    p.SetLocalClearance(200000 if p.GetFPRelativePosition().x == 0 else 300000)
pcbnew.SaveBoard(sys.argv[1], b)
`)
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("R1", "Footprint", "Dup:DupB"); err != nil {
			t.Fatal(err)
		}
	})
	o := syncOpts(sch, pcb)
	o.out, o.noParity = filepath.Join(dir, "out", "d.kicad_pcb"), true
	// Pin 2 of R1 has no pad on either footprint: pad-net fails (exit 6),
	// --out keeps the output, which is what this test reads.
	rep, code := runSync(t, o)
	if code != 6 || !rep.Committed || rep.Counts.Swap != 1 {
		t.Fatalf("exit %d %+v %+v", code, rep.Ops, rep.Errors)
	}
	got := scriptResult(t, pcbnewScript(t, kt, o.out, `fp = [f for f in b.GetFootprints() if f.GetReference() == "R1"][0]
print("RESULT" + json.dumps({str(p.GetFPRelativePosition().x): p.GetLocalClearance() for p in fp.Pads()}))
`))
	if got["0"] != 200000.0 || got["7620000"] != 300000.0 {
		t.Fatalf("each same-number pad must keep its own clearance: %v", got)
	}
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("R1", "Footprint", "Dup:DupC"); err != nil {
			t.Fatal(err)
		}
	})
	o.out = filepath.Join(dir, "out2", "d.kicad_pcb")
	rep, code = runSync(t, o)
	if code != 1 || fileExists(o.out) || !strings.Contains(fmt.Sprint(rep.Errors), "cannot be matched one to one") {
		t.Fatalf("ambiguous same-number pads: exit %d %+v", code, rep.Errors)
	}
}

// TestPcbSyncLiveUnnumberedPadBlocksRename: a connected pad with no number
// (no schematic pin speaks for it) on a footprint of a net that would be
// renamed keeps the net from being renamed: the pads are re-netted one by
// one instead, and the unnumbered pad is left where it is.
func TestPcbSyncLiveUnnumberedPadBlocksRename(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	pcbnewScript(t, kt, pcb, `fp = [f for f in b.GetFootprints() if f.GetReference() == "C2"][0]
src = [p for p in fp.Pads() if p.GetNumber() == "2"][0]
extra = pcbnew.PAD(src)
extra.SetNumber("")
extra.SetPosition(pcbnew.VECTOR2I(src.GetPosition().x, src.GetPosition().y + 3000000))
fp.Add(extra)
extra.SetNet(src.GetNet())
pcbnew.SaveBoard(sys.argv[1], b)
`)
	editSch(t, sch, func(e *kicad.SchEditor) {
		pins, err := e.SymbolPinPositions("C2")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.AddLabel(kicad.LabelLocal, "CATHODE", pins["2"], 0, ""); err != nil {
			t.Fatal(err)
		}
	})
	o := syncOpts(sch, pcb)
	o.dryRun = true
	rep, code := runSync(t, o)
	if code != 0 || rep.Counts.RenameNet != 0 || rep.Counts.PadNet != 3 {
		t.Fatalf("exit %d counts %+v ops %+v", code, rep.Counts, rep.Ops)
	}
}

// TestPcbSyncLiveRemoveGroupedOrphan: --orphans remove takes a grouped
// footprint out of its group before deleting it; the group keeps its other
// members and the saved board reloads cleanly.
func TestPcbSyncLiveRemoveGroupedOrphan(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	pcbnewScript(t, kt, pcb, `g = pcbnew.PCB_GROUP(b)
g.SetName("amp")
b.Add(g)
for f in b.GetFootprints():
    if f.GetReference() in ("C2", "R1", "R2"):
        g.AddItem(f)
pcbnew.SaveBoard(sys.argv[1], b)
`)
	editSch(t, sch, func(e *kicad.SchEditor) {
		if _, err := e.DeleteSymbol("R2"); err != nil {
			t.Fatal(err)
		}
	})
	o := syncOpts(sch, pcb)
	o.inPlace, o.orphans = true, "remove"
	rep, code := runSync(t, o)
	if code != 0 || !rep.Committed || rep.Counts.OrphanRemove != 1 {
		t.Fatalf("exit %d %+v %+v", code, rep.Counts, rep.Errors)
	}
	got := scriptResult(t, pcbnewScript(t, kt, pcb, `refs = sorted(f.GetReference() for f in b.GetFootprints())
groups = [[g.GetName(), sorted(i.GetReference() for i in g.GetItems() if hasattr(i, "GetReference"))] for g in b.Groups()]
print("RESULT" + json.dumps({"refs": refs, "groups": groups}))
`))
	if fmt.Sprint(got["groups"]) != "[[amp [C2 R1]]]" || strings.Contains(fmt.Sprint(got["refs"]), "R2") {
		t.Fatalf("after removing the grouped orphan: %v", got)
	}
	if _, err := kt.SyncRead(pcb); err != nil {
		t.Fatalf("the saved board does not reload: %v", err)
	}
}

// TestPcbSyncLiveSwapKeepsSpokeAngle: a pad's customised thermal spoke
// angle is an override: a swap carries it to the same pad, and a
// replacement without that pad refuses the swap.
func TestPcbSyncLiveSwapKeepsSpokeAngle(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	pcbnewScript(t, kt, pcb, `fp = [f for f in b.GetFootprints() if f.GetReference() == "C2"][0]
[p for p in fp.Pads() if p.GetNumber() == "2"][0].SetThermalSpokeAngleDegrees(30.0)
pcbnew.SaveBoard(sys.argv[1], b)
`)
	readAngle := `fp = [f for f in b.GetFootprints() if f.GetReference() == "C2"][0]
print("RESULT" + json.dumps({p.GetNumber(): p.GetThermalSpokeAngleDegrees() for p in fp.Pads()}))
`
	if got := scriptResult(t, pcbnewScript(t, kt, pcb, readAngle)); got["2"] != 30.0 {
		t.Fatalf("fixture: %v", got)
	}
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("C2", "Footprint", "Capacitor_THT:C_Disc_D5.0mm_W2.5mm_P5.00mm"); err != nil {
			t.Fatal(err)
		}
	})
	o := syncOpts(sch, pcb)
	o.inPlace, o.noParity, o.forceUnverified = true, true, true
	if rep, code := runSync(t, o); code != 5 || !rep.Committed || rep.Counts.Swap != 1 {
		t.Fatalf("exit %d %+v %+v", code, rep.Ops, rep.Errors)
	}
	if got := scriptResult(t, pcbnewScript(t, kt, pcb, readAngle)); got["2"] != 30.0 || got["1"] != 45.0 {
		t.Fatalf("spoke angles after the swap: %v", got)
	}
	editSch(t, sch, func(e *kicad.SchEditor) {
		if err := e.SetField("C2", "Footprint", "TestPoint:TestPoint_THTPad_D1.5mm_Drill0.7mm"); err != nil {
			t.Fatal(err)
		}
	})
	before := syncFileSHA(t, pcb)
	rep, code := runSync(t, o)
	if code != 1 || !strings.Contains(fmt.Sprint(rep.Errors), "ThermalSpokeAngleDegrees") || syncFileSHA(t, pcb) != before {
		t.Fatalf("a swap dropping the angle must be refused: exit %d %+v", code, rep.Errors)
	}
}

// TestPcbSyncLiveRenameMovesCopperShapes: a renamed net takes its copper
// drawings (board shapes carrying the net) along, like tracks and zones.
func TestPcbSyncLiveRenameMovesCopperShapes(t *testing.T) {
	kt := pcbSyncLive(t)
	_, sch, pcb := ecc83Synced(t)
	pcbnewScript(t, kt, pcb, `n = b.FindNet("Net-(U1A-K)")
s = pcbnew.PCB_SHAPE(b)
s.SetShape(pcbnew.SHAPE_T_SEGMENT)
s.SetStart(pcbnew.VECTOR2I(150000000, 120000000))
s.SetEnd(pcbnew.VECTOR2I(155000000, 120000000))
s.SetWidth(500000)
s.SetLayer(pcbnew.B_Cu)
s.SetNet(n)
b.Add(s)
pcbnew.SaveBoard(sys.argv[1], b)
`)
	readShapes := `print("RESULT" + json.dumps({"nets": sorted(d.GetNetname() for d in b.GetDrawings() if d.GetLayer() == pcbnew.B_Cu)}))
`
	if got := scriptResult(t, pcbnewScript(t, kt, pcb, readShapes)); fmt.Sprint(got["nets"]) != "[Net-(U1A-K)]" {
		t.Fatalf("fixture: %v", got)
	}
	editSch(t, sch, func(e *kicad.SchEditor) {
		pins, err := e.SymbolPinPositions("C2")
		if err != nil {
			t.Fatal(err)
		}
		if err := e.AddLabel(kicad.LabelLocal, "CATHODE", pins["2"], 0, ""); err != nil {
			t.Fatal(err)
		}
	})
	o := syncOpts(sch, pcb)
	o.inPlace = true
	rep, code := runSync(t, o)
	if code != 0 || rep.Counts.RenameNet != 1 {
		t.Fatalf("exit %d %+v %+v %+v", code, rep.Counts, rep.Gates, rep.Errors)
	}
	if got := scriptResult(t, pcbnewScript(t, kt, pcb, readShapes)); fmt.Sprint(got["nets"]) != "[/CATHODE]" {
		t.Fatalf("the copper shape stayed on the old net: %v", got)
	}
}
