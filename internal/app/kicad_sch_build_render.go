package app

// kicad_sch_build_render.go — draws the zone layouts onto KiCad sheets,
// writes the project into a staging directory, verifies it (KiCad netlist
// == spec is the hard, transactional gate; ERC; offline layout checks),
// commits it to --out, emits connectivity.json / intent.json and saves the
// build state + a checkpoint.

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// ── state ───────────────────────────────────────────────────────────────────

// sbState makes rebuilds and edits stable: uuids (the schematic ↔ PCB link)
// and zone layouts + positions survive; only changed zones are redrawn.
type sbState struct {
	Version       int                     `json:"version"`
	Name          string                  `json:"name"`
	RootUUID      string                  `json:"rootUuid"`
	PageUUIDs     map[string]string       `json:"pageUuids"`
	SheetSymUUIDs map[string]string       `json:"sheetSymbolUuids"`
	SymUUIDs      map[string]string       `json:"symbolUuids"` // "REF/unit"
	Zones         map[string]*sbZoneState `json:"zones"`
	Files         []string                `json:"files"` // files this build owns in --out
}

type sbZoneState struct {
	Page   string        `json:"page"`
	T      kicad.Pt      `json:"t"`
	Layout *sbZoneLayout `json:"layout"`
}

func newSbState() *sbState {
	return &sbState{Version: 1, PageUUIDs: map[string]string{}, SheetSymUUIDs: map[string]string{},
		SymUUIDs: map[string]string{}, Zones: map[string]*sbZoneState{}}
}

func sbMetaDir(out string) string { return filepath.Join(out, ".pcbpilot", "sch-build") }

func loadSbState(out string) (*sbState, *sbSpec) {
	b, err := os.ReadFile(filepath.Join(sbMetaDir(out), "state.json"))
	if err != nil {
		return nil, nil
	}
	st := newSbState()
	if json.Unmarshal(b, st) != nil {
		return nil, nil
	}
	for _, m := range []*map[string]string{&st.PageUUIDs, &st.SheetSymUUIDs, &st.SymUUIDs} {
		if *m == nil {
			*m = map[string]string{}
		}
	}
	if st.Zones == nil {
		st.Zones = map[string]*sbZoneState{}
	}
	var spec *sbSpec
	if sb, err := os.ReadFile(filepath.Join(sbMetaDir(out), "spec.json")); err == nil {
		var s sbSpec
		if json.Unmarshal(sb, &s) == nil {
			spec = &s
		}
	}
	return st, spec
}

func (st *sbState) uuid(m map[string]string, key string) string {
	if u := m[key]; u != "" {
		return u
	}
	u := kicad.NewUUID()
	m[key] = u
	return u
}

// ── report ──────────────────────────────────────────────────────────────────

type sbGate struct {
	Name   string `json:"name"`
	Status string `json:"status"` // pass | warn | fail | skip
	Detail string `json:"detail,omitempty"`
	Data   any    `json:"data,omitempty"`
}

type sbZoneReport struct {
	ID       string    `json:"id"`
	Page     string    `json:"page"`
	Parts    []string  `json:"parts"`
	Method   string    `json:"method"`
	Reused   bool      `json:"reused,omitempty"`
	Moved    bool      `json:"moved,omitempty"`
	Box      []float64 `json:"boxMm"` // absolute minX,minY,maxX,maxY
	Ms       float64   `json:"ms,omitempty"`
	Notes    []string  `json:"notes,omitempty"`
	Findings []string  `json:"qualityFindings,omitempty"`
}

type sbReport struct {
	OK         bool              `json:"ok"`
	Mode       string            `json:"mode"`
	Name       string            `json:"name"`
	Project    string            `json:"project,omitempty"`
	Root       string            `json:"root,omitempty"`
	Sheets     []string          `json:"sheets,omitempty"`
	Parts      int               `json:"parts"`
	Nets       int               `json:"nets"`
	Pins       int               `json:"connectedPins"`
	NoConnect  int               `json:"noConnectPins"`
	Pages      []string          `json:"pages"`
	Zones      []sbZoneReport    `json:"zones"`
	Resolve    sbResolveStats    `json:"symbols"`
	Gates      []sbGate          `json:"gates"`
	Timings    []sbStage         `json:"timingsMs"`
	TotalMs    float64           `json:"totalMs"`
	Warnings   []string          `json:"warnings,omitempty"`
	Outputs    map[string]string `json:"outputs,omitempty"`
	Checkpoint int               `json:"checkpoint,omitempty"`
	Edit       any               `json:"edit,omitempty"`
	Plan       *sbPlan           `json:"plan,omitempty"`
	PartBoxes  map[string]sbBox  `json:"partBoxes,omitempty"`
	Error      *sbFailure        `json:"error,omitempty"`
	Undo       string            `json:"undo,omitempty"`
	Next       []string          `json:"next,omitempty"`
}

// sbBox is a part's drawn extent on its sheet (mm).
type sbBox struct {
	Sheet string    `json:"sheet"`
	Box   []float64 `json:"bboxMm"`
}

// sbPartBoxes reads every sheet back: ref → body box (all units).
func sbPartBoxes(dir string, files []string) map[string]sbBox {
	out := map[string]sbBox{}
	for _, f := range files {
		if !strings.HasSuffix(f, ".kicad_sch") {
			continue
		}
		p, err := sbSafeJoin(dir, f)
		if err != nil {
			continue
		}
		e, err := kicad.OpenSchematicFile(p)
		if err != nil {
			continue
		}
		sc, err := e.Scene()
		if err != nil {
			continue
		}
		for _, s := range sc.Symbols {
			if s.Power || !s.HasBox || strings.HasPrefix(s.Ref, "#") {
				continue
			}
			b := s.Box
			if old, ok := out[s.Ref]; ok {
				b = sbBoxUnion(b, kicad.Box{MinX: old.Box[0], MinY: old.Box[1], MaxX: old.Box[2], MaxY: old.Box[3]})
			}
			out[s.Ref] = sbBox{Sheet: f, Box: []float64{sbRound2(b.MinX), sbRound2(b.MinY), sbRound2(b.MaxX), sbRound2(b.MaxY)}}
		}
	}
	return out
}

// sbTestGateHook lets tests inject a gate result before the commit decision.
var sbTestGateHook func(rep *sbReport)

// sbFailure is a refusal with a code and the fix.
type sbFailure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Fix     string `json:"fix"`
}

// sbProjectSHA hashes the files a build owns (compare-and-swap).
func sbProjectSHA(out string, st *sbState) string {
	h := sha256.New()
	for _, f := range st.Files {
		if p, err := sbSafeJoin(out, f); err == nil {
			b, _ := os.ReadFile(p)
			fmt.Fprintf(h, "%s\x00%d\x00", f, len(b))
			h.Write(b)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil))
}

func sbCheckUnchanged(out string, st *sbState, start string) error {
	if now := sbProjectSHA(out, st); now != start {
		return fmt.Errorf("%s changed while sch-build ran (sha256 %s → %s)", out, start[:12], now[:12])
	}
	return nil
}

// sbLock serializes writers of one project (O_EXCL lock file; a lock older
// than 10 minutes is stale).
func sbLock(out string) (func(), error) {
	if err := os.MkdirAll(sbMetaDir(out), 0o755); err != nil {
		return nil, err
	}
	p := filepath.Join(sbMetaDir(out), "lock")
	for i := 0; i < 2; i++ {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d %s\n", os.Getpid(), time.Now().Format(time.RFC3339))
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if st, serr := os.Stat(p); serr == nil && time.Since(st.ModTime()) > 10*time.Minute {
			os.Remove(p)
			continue
		}
		return nil, fmt.Errorf("another sch-build/sch-edit is writing %s (lock %s)", out, p)
	}
	return nil, fmt.Errorf("cannot lock %s", out)
}

// sbPlan is the --dry-run output: everything that would be drawn.
type sbPlan struct {
	Placements  []sbPlanPart `json:"placements"`
	Connections []sbPlanNet  `json:"connections"`
	NoConnect   []string     `json:"noConnect,omitempty"`
	Gates       []string     `json:"gates"`
	Files       []string     `json:"files"`
}

type sbPlanPart struct {
	Ref    string  `json:"ref"`
	Page   string  `json:"page"`
	Zone   string  `json:"zone"`
	X      float64 `json:"xMm"`
	Y      float64 `json:"yMm"`
	Rot    float64 `json:"rot"`
	Symbol string  `json:"symbol"`
	Source string  `json:"source"`
}

type sbPlanNet struct {
	Net     string   `json:"net"`
	Kind    string   `json:"kind"`
	Pins    []string `json:"pins"`
	Markers []string `json:"markers"`
	Wires   int      `json:"wires"`
}

func (r *sbReport) gate(name, status, detail string, data any) {
	r.Gates = append(r.Gates, sbGate{Name: name, Status: status, Detail: detail, Data: data})
	if status == "fail" {
		r.OK = false
	}
}

// ── build ───────────────────────────────────────────────────────────────────

type sbEditCtx struct {
	Rename  map[string]string   // old net → new net (cached layouts relabel)
	ZoneAt  map[string]kicad.Pt // zone → frame top-left (mm)
	Summary any
}

func (ec *sbEditCtx) zoneAt() map[string]kicad.Pt {
	if ec == nil {
		return nil
	}
	return ec.ZoneAt
}

func runSchBuild(spec *sbSpec, o sbOptions, ec *sbEditCtx) (*sbReport, error) {
	tm := newSbTimer()
	out, err := filepath.Abs(o.OutDir)
	if err != nil {
		return nil, err
	}
	rep := &sbReport{OK: true, Mode: "build", Outputs: map[string]string{}}
	if ec != nil {
		rep.Mode, rep.Edit = "edit", ec.Summary
	}
	if o.DryRun {
		rep.Mode += "+dry-run"
	}
	st, _ := loadSbState(out)
	if st == nil || o.Fresh {
		old := st
		st = newSbState()
		if old != nil {
			st.Files = old.Files // still owned: replaced or removed on commit
		}
	}
	startSHA := sbProjectSHA(out, st)
	if !o.DryRun {
		unlock, err := sbLock(out)
		if err != nil {
			rep.OK = false
			rep.Error = &sbFailure{Code: "LOCKED", Message: err.Error(), Fix: "wait for the other writer, or delete the stale lock file"}
			return rep, nil
		}
		defer unlock()
	}
	name := o.Name
	if name == "" {
		name = spec.Name
	}
	if name == "" {
		name = st.Name
	}
	if name == "" {
		name = filepath.Base(out)
	}
	name = strings.Trim(sbSanitize(name), "._-") // file names below --out come from it
	if name == "" {
		name = "pcbpilot"
	}
	if name == "" {
		name = "pcbpilot"
	}
	st.Name, rep.Name = name, name
	waivers, err := loadWaivers(o.Waivers)
	if err != nil {
		return rep, err
	}
	if len(o.Requirements) > 0 && o.NoReview && !hasWaiver(waivers, "design-review", noReviewMatch) {
		rep.OK = false
		rep.Error = &sbFailure{Code: "REVIEW_WAIVER_REQUIRED", Message: "--no-review with --requirements skips the schematic design review",
			Fix: `add a signed waiver to --waivers: {"gate":"design-review","match":"--no-review","reason":…,"by":…}, or drop --no-review`}
		return rep, nil
	}

	d, rs, err := buildDesign(spec, sbResolveOpts{OutDir: out, Offline: o.Offline, Jobs: o.Jobs, PartsPath: o.PartsPath})
	rep.Resolve = rs
	if err != nil {
		return rep, err
	}
	tm.mark("resolve")
	rep.Warnings = append(rep.Warnings, d.Warnings...)
	// EG-22: a complete title block (defaults are reported)
	var defs []string
	if spec.Title.Title == "" {
		spec.Title.Title, defs = name, append(defs, "title="+name)
	}
	if spec.Title.Date == "" {
		spec.Title.Date = time.Now().Format("2006-01-02")
		defs = append(defs, "date="+spec.Title.Date)
	}
	if spec.Title.Rev == "" {
		spec.Title.Rev, defs = "A", append(defs, "rev=A")
	}
	if spec.Title.Company == "" {
		spec.Title.Company, defs = "pcbpilot", append(defs, "company=pcbpilot")
	}
	if len(defs) > 0 {
		rep.Warnings = append(rep.Warnings, "title block defaults: "+strings.Join(defs, ", ")+" (set title.* in the spec)")
	}
	rep.Warnings = append(rep.Warnings, rs.Notes...)
	rep.Parts, rep.Nets, rep.Pins, rep.NoConnect = len(d.Parts), len(d.Nets), len(d.PinNet), len(d.NC)
	for _, pg := range d.Pages {
		rep.Pages = append(rep.Pages, pg.ID)
	}

	// zone layouts (reuse unchanged ones)
	zis := d.zoneInputs()
	reuse := map[string]*sbZoneLayout{}
	for id, zs := range st.Zones {
		if zs.Layout != nil {
			l := *zs.Layout
			if len(ec.renameMap()) > 0 {
				l.Markers = append([]sbMarker(nil), l.Markers...)
				for i := range l.Markers {
					if n, ok := ec.renameMap()[l.Markers[i].Net]; ok {
						l.Markers[i].Net = n
					}
				}
			}
			reuse[id] = &l
		}
	}
	layouts, notes := layoutZones(d, zis, reuse, sbLayoutOpts{NoPlanner: o.NoPlanner, Budget: o.PlannerBudget, Jobs: o.Jobs, Timeout: o.PlannerTimeout})
	rep.Warnings = append(rep.Warnings, notes...)
	for _, zi := range zis {
		if layouts[zi.Zone.ID] == nil {
			return rep, fmt.Errorf("zone %s could not be laid out", zi.Zone.ID)
		}
	}
	tm.mark("layout")

	// positions per page
	trans := map[string]kicad.Pt{}
	moved := map[string]bool{}
	for _, pg := range d.Pages {
		var ids []string
		keep := map[string]kicad.Pt{}
		for _, zi := range zis {
			if zi.Zone.Page != pg.ID {
				continue
			}
			ids = append(ids, zi.Zone.ID)
			if zs := st.Zones[zi.Zone.ID]; zs != nil && zs.Page == pg.ID && !o.Fresh {
				keep[zi.Zone.ID] = zs.T
			}
			if at, ok := ec.zoneAt()[zi.Zone.ID]; ok { // sch-edit move_zone --at: frame top-left
				b := layouts[zi.Zone.ID].Box
				keep[zi.Zone.ID] = kicad.Pt{X: sbSnap(at.X + sbZoneMargin - b.MinX), Y: sbSnap(at.Y + sbZoneMargin + sbZoneTitle - b.MinY)}
			}
		}
		for id, t := range packZones(ids, layouts, keep) {
			trans[id] = t
			if k, ok := keep[id]; ok && k != t {
				moved[id] = true
			} else if !ok && st.Zones[id] != nil {
				moved[id] = true
			}
		}
	}
	for _, zi := range zis {
		zl := layouts[zi.Zone.ID]
		t := trans[zi.Zone.ID]
		var refs []string
		for _, p := range zi.Parts {
			refs = append(refs, p.Ref)
		}
		zr := sbZoneReport{ID: zi.Zone.ID, Page: zi.Zone.Page, Parts: refs, Method: zl.Method, Ms: zl.Ms, Notes: zl.Notes, Findings: zl.Findings,
			Box:   []float64{sbRound2(zl.Box.MinX + t.X), sbRound2(zl.Box.MinY + t.Y), sbRound2(zl.Box.MaxX + t.X), sbRound2(zl.Box.MaxY + t.Y)},
			Moved: moved[zi.Zone.ID]}
		if r := reuse[zi.Zone.ID]; r != nil && r == zl {
			zr.Reused, zr.Ms = true, 0
		}
		rep.Zones = append(rep.Zones, zr)
	}

	if o.DryRun {
		rep.Plan = sbMakePlan(d, zis, layouts, trans)
		tm.mark("plan")
		rep.Timings, rep.TotalMs = tm.Steps, tm.total()
		return rep, nil
	}

	// render + write the project into a staging directory
	stage, err := os.MkdirTemp("", "pcbpilot-sch-build-")
	if err != nil {
		return rep, err
	}
	defer os.RemoveAll(stage)
	files, sheetTexts, err := writeSbProject(stage, d, st, zis, layouts, trans)
	if err != nil {
		return rep, err
	}
	tm.mark("render+write")
	// fit gate
	fitBad := []string{}
	for f, r := range sheetTexts {
		if r.TooBig {
			fitBad = append(fitBad, f)
		}
	}
	sort.Strings(fitBad)

	// verify: netlist (hard) and ERC in parallel
	root := filepath.Join(stage, name+".kicad_sch")
	var nl *kicad.SchNetlist
	var nlErr, ercErr error
	var erc *kicad.ERCReport
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); nl, nlErr = kicad.ExportSchNetlist(root) }()
	go func() {
		defer wg.Done()
		if erc, ercErr = kicad.RunERC(root); ercErr == nil {
			_ = writeJSONFile(filepath.Join(stage, "erc.json"), erc)
		}
	}()
	wg.Wait()
	tm.mark("netlist+erc")
	if nlErr != nil {
		return rep, fmt.Errorf("KiCad netlist: %w", nlErr)
	}
	diff := compareSbNetlist(d, nl)
	if !diff.Equal {
		rep.gate("netlist", "fail", diff.summary(), diff)
		rep.Error = &sbFailure{Code: "NETLIST_MISMATCH", Message: diff.summary(), Fix: "nothing was written; this is a pcbpilot drawing bug — report it with the failed build, or retry with --no-planner"}
		failed := filepath.Join(sbMetaDir(out), "failed")
		_ = os.RemoveAll(failed)
		if err := copySbFiles(stage, failed, append(files, "erc.json")); err == nil {
			rep.Outputs["failedBuild"] = failed
		}
		rep.Timings, rep.TotalMs = tm.Steps, tm.total()
		return rep, fmt.Errorf("KiCad netlist != spec (%s) — %s left unchanged; the failed build is in %s", diff.summary(), out, failed)
	}
	rep.gate("netlist", "pass", fmt.Sprintf("KiCad netlist == spec: %d nets, %d pins, %d no-connect", diff.Nets, diff.Pins, len(d.NC)), nil)
	if ercErr != nil {
		rep.gate("erc", "fail", ercErr.Error(), nil)
	} else {
		status := "pass"
		if erc.Errors > 0 {
			status = "fail"
		} else if erc.Warnings > 0 {
			status = "warn"
		}
		var top []kicad.ERCViolation
		byType := map[string]int{}
		for _, v := range erc.Violations {
			byType[v.Type]++
			if v.Severity == "error" && len(top) < 20 {
				top = append(top, v)
			}
		}
		rep.gate("erc", status, fmt.Sprintf("kicad-cli ERC: %d error(s), %d warning(s)", erc.Errors, erc.Warnings),
			map[string]any{"byType": byType, "errors": top})
	}
	if len(fitBad) > 0 {
		rep.gate("page-fit", "fail", "content does not fit A0 on "+strings.Join(fitBad, ", ")+" — split into more pages", nil)
	} else {
		var sizes []string
		for f, r := range sheetTexts {
			sizes = append(sizes, f+"="+r.To)
		}
		sort.Strings(sizes)
		rep.gate("page-fit", "pass", strings.Join(sizes, " "), nil)
	}
	lay := sbQualityCheck(stage, files)
	rep.gate(lay.Name, lay.Status, lay.Detail, lay.Data)
	eg := sbEngineerGrade(stage, files, d)
	rep.gate(eg.Name, eg.Status, eg.Detail, eg.Data)
	if sbTestGateHook != nil {
		sbTestGateHook(rep)
	}
	tm.mark("checks")
	// hard gates are transactional: ERC errors, quality findings or a page
	// that does not fit leave --out untouched
	if !rep.OK {
		var bad []string
		for _, g := range rep.Gates {
			if g.Status == "fail" {
				bad = append(bad, g.Name+": "+g.Detail)
			}
		}
		failed := filepath.Join(sbMetaDir(out), "failed")
		_ = os.RemoveAll(failed)
		if err := copySbFiles(stage, failed, append(files, "erc.json")); err == nil {
			rep.Outputs["failedBuild"] = failed
		}
		rep.Timings, rep.TotalMs = tm.Steps, tm.total()
		rep.Error = &sbFailure{Code: "GATE_FAILED", Message: strings.Join(bad, "; "),
			Fix: "nothing was written; fix the spec (or zone/page split) and rebuild — the failed project is in " + failed}
		return rep, nil
	}
	// compare-and-swap: the project must not have changed while we built
	if err := sbCheckUnchanged(out, st, startSHA); err != nil {
		rep.OK = false
		rep.Error = &sbFailure{Code: "CONCURRENT_CHANGE", Message: err.Error(), Fix: "re-run: another writer changed the project during this build"}
		return rep, nil
	}

	// commit
	if err := os.MkdirAll(out, 0o755); err != nil {
		return rep, err
	}
	for _, f := range st.Files { // files a previous build wrote that this one does not
		if !sbContains(files, f) {
			if p, err := sbSafeJoin(out, f); err == nil {
				_ = os.Remove(p)
			}
		}
	}
	if err := copySbFiles(stage, out, append(files, "erc.json")); err != nil {
		return rep, err
	}
	_ = os.RemoveAll(filepath.Join(sbMetaDir(out), "failed"))
	st.Files = files
	rep.Project = filepath.Join(out, name+".kicad_pro")
	rep.Root = filepath.Join(out, name+".kicad_sch")
	for _, f := range files {
		if strings.HasSuffix(f, ".kicad_sch") {
			rep.Sheets = append(rep.Sheets, filepath.Join(out, f))
		}
	}
	rep.Outputs["erc"] = filepath.Join(out, "erc.json")
	rep.PartBoxes = sbPartBoxes(out, files)
	tm.mark("commit")

	// connectivity.json + intent
	conn, err := nl.ToConnectivityDoc(name)
	if err != nil {
		rep.gate("connectivity", "fail", err.Error(), nil)
	} else {
		cp := filepath.Join(out, "connectivity.json")
		if err := writeJSONFile(cp, conn); err != nil {
			return rep, err
		}
		rep.Outputs["connectivity"] = cp
		rep.gate("connectivity", "pass", fmt.Sprintf("connectivity IR 1.4: %d components, %d nets", len(conn.Components), len(conn.Nets)), nil)
		vp := filepath.Join(out, "values.json")
		if err := writeJSONFile(vp, d.values()); err != nil {
			return rep, err
		}
		rep.Outputs["values"] = vp
		ip := filepath.Join(out, "intent-spec.json")
		if err := writeJSONFile(ip, d.intentSpec()); err != nil {
			return rep, err
		}
		rep.Outputs["intentSpec"] = ip
		if o.NoIntent {
			rep.gate("intent", "skip", "--no-intent", nil)
		} else {
			args := []string{"--connectivity", cp, "--values", vp, "--spec", ip, "--out", filepath.Join(out, "intent.json"), "--report", filepath.Join(out, "intent.md")}
			if !o.Analog {
				args = append(args, "--no-analog")
			}
			var so, se bytes.Buffer
			window := ""
			c := newIntentDeriveCmd(&appConfig{}, &window, &so, &se)
			c.SetArgs(args)
			c.SetOut(&so)
			c.SetErr(&se)
			c.SilenceUsage, c.SilenceErrors = true, true
			if err := c.Execute(); err != nil {
				rep.gate("intent", "fail", "intent derive: "+sbClip(err.Error()+" "+se.String(), 600), nil)
			} else {
				rep.Outputs["intent"] = filepath.Join(out, "intent.json")
				rep.Outputs["intentReport"] = filepath.Join(out, "intent.md")
				rep.gate("intent", "pass", strings.TrimSpace(sbLastLine(se.String())), nil)
			}
		}
	}
	tm.mark("connectivity+intent")

	// state, spec, checkpoint
	for _, zi := range zis {
		st.Zones[zi.Zone.ID] = &sbZoneState{Page: zi.Zone.Page, T: trans[zi.Zone.ID], Layout: layouts[zi.Zone.ID]}
	}
	for id := range st.Zones {
		found := false
		for _, zi := range zis {
			found = found || zi.Zone.ID == id
		}
		if !found {
			delete(st.Zones, id)
		}
	}
	norm := d.normalizedSpec()
	norm.Name = name
	if err := os.MkdirAll(sbMetaDir(out), 0o755); err != nil {
		return rep, err
	}
	if err := writeJSONFile(filepath.Join(sbMetaDir(out), "spec.json"), norm); err != nil {
		return rep, err
	}
	if err := writeJSONFile(filepath.Join(sbMetaDir(out), "state.json"), st); err != nil {
		return rep, err
	}
	n, err := saveSbCheckpoint(out, files, rep.Mode)
	if err != nil {
		rep.Warnings = append(rep.Warnings, "checkpoint: "+err.Error())
	}
	rep.Checkpoint = n
	sbReviewGate(rep, o, out, waivers)
	tm.mark("review")
	if n > 1 {
		rep.Undo = fmt.Sprintf("pcbpilot kicad sch-checkpoint restore %d --project %s", n-1, out)
	}
	tm.mark("state+checkpoint")
	rep.Timings, rep.TotalMs = tm.Steps, tm.total()
	rep.Next = []string{
		"pcbpilot review-panel (design review) on " + rep.Root,
		"pcbpilot kicad sch-edit --project " + out + " --spec delta.json (incremental change)",
		"pcbpilot kicad sch-read --project " + out + " (compact parts/nets JSON)",
	}
	return rep, nil
}

// sbReviewGate is the schematic design review (review-panel, stage
// schematic) on the project requirements: on by default when --requirements
// is given; --no-review needs a signed waiver (checked before the build).
func sbReviewGate(rep *sbReport, o sbOptions, out string, waivers []gateWaiver) {
	if len(o.Requirements) == 0 {
		rep.gate("design-review", "skip", "no --requirements given (the review judges the schematic against the project requirement documents)", nil)
		return
	}
	if o.NoReview {
		rep.gate("design-review", "pass", "skipped by --no-review under a signed waiver", nil)
		return
	}
	var ev []string
	for _, k := range []string{"connectivity", "values", "intentSpec", "intentReport", "erc"} {
		if p := rep.Outputs[k]; p != "" {
			ev = append(ev, p)
		}
	}
	ev = append(ev, filepath.Join(sbMetaDir(out), "spec.json"))
	var reviewers []string
	for _, r := range strings.Split(o.Reviewers, ",") {
		if r = strings.TrimSpace(r); r != "" {
			reviewers = append(reviewers, r)
		}
	}
	dir := filepath.Join(out, "review-schematic")
	if sha, err := reviewInputSHA("schematic", o.Requirements, ev, reviewMaxBytes); err == nil {
		var old reviewRecord
		if b, err := os.ReadFile(filepath.Join(dir, "review.json")); err == nil && json.Unmarshal(b, &old) == nil &&
			old.Stage == "schematic" && old.InputSHA256 == sha && old.Gate.Pass {
			rep.Outputs["review"] = filepath.Join(dir, "review.json")
			rep.gate("design-review", "pass", "reused "+filepath.Join(dir, "review.json")+" (same inputs): "+old.Gate.Detail, nil)
			return
		}
	}
	timeout := o.ReviewTimeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	rec, err := runReviewPanel("schematic", o.Requirements, ev, reviewers, dir, timeout, reviewMaxBytes, waivers, io.Discard)
	if err != nil {
		rep.gate("design-review", "fail", "review-panel: "+err.Error(), nil)
		return
	}
	rep.Outputs["review"] = filepath.Join(dir, "review.json")
	status := "pass"
	if !rec.Gate.Pass {
		status = "fail"
	}
	rep.gate("design-review", status, rec.Gate.Detail, rec.Gate.Items)
}

func (ec *sbEditCtx) renameMap() map[string]string {
	if ec == nil {
		return nil
	}
	return ec.Rename
}

func sbRound2(v float64) float64 { return math.Round(v*100) / 100 }

func sbContains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func sbLastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// ── rendering ───────────────────────────────────────────────────────────────

func sbSheetFile(page string) string {
	n := sbSanitize(page)
	if n == "" {
		n = "page-" + sbShortHash(page)
	}
	return n + ".kicad_sch"
}

// sbSafeName reports whether s is a plain file-name component: a safe
// charset, no path separator, no "..", not empty.
func sbSafeName(s string) bool {
	if s == "" || s == "." || strings.Contains(s, "..") || len(s) > 200 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.' || r == '+' || r == '(' || r == ')' || r == ' ' || r == ',' || r == '=':
		default:
			return false
		}
	}
	return true
}

// sbSafeJoin joins rel below base and refuses anything that escapes it
// (absolute paths, "..", separators smuggled through spec fields).
func sbSafeJoin(base, rel string) (string, error) {
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\\") {
		return "", fmt.Errorf("unsafe path %q", rel)
	}
	for _, part := range strings.Split(rel, "/") {
		if !sbSafeName(part) {
			return "", fmt.Errorf("unsafe path %q", rel)
		}
	}
	target := filepath.Join(base, filepath.FromSlash(rel))
	r, err := filepath.Rel(base, target)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || filepath.IsAbs(r) {
		return "", fmt.Errorf("path %q escapes %s", rel, base)
	}
	return target, nil
}

// writeSbProject renders every sheet and writes the project into dir.
// Returns the written files (relative) and each sheet's fit result.
func writeSbProject(dir string, d *sbDesign, st *sbState, zis []*sbZoneIn, layouts map[string]*sbZoneLayout, trans map[string]kicad.Pt) ([]string, map[string]kicad.FitResult, error) {
	name := st.Name
	multi := len(d.Pages) > 1
	if st.RootUUID == "" {
		st.RootUUID = kicad.NewUUID()
	}
	fits := map[string]kicad.FitResult{}
	var files []string
	write := func(rel string, data []byte) error {
		p, err := sbSafeJoin(dir, rel)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		files = append(files, rel)
		return os.WriteFile(p, data, 0o644)
	}
	// power nets needing a PWR_FLAG: no power_out pin on the net
	driven := map[string]bool{}
	for _, p := range d.Parts {
		for _, num := range p.PowerPins {
			if n, ok := d.PinNet[sbPinKey(p.Ref, num)]; ok {
				driven[n] = true
			}
		}
	}
	flagPage := map[string]string{}
	for _, n := range d.Nets {
		k := d.NetKind[n.Name]
		if (k == "power" || k == "ground") && !driven[n.Name] {
			for _, pg := range d.Pages {
				if d.NetPages[n.Name][pg.ID] {
					flagPage[n.Name] = pg.ID
					break
				}
			}
		}
	}
	pwr, flg := 1, 1
	powerSyms := map[string]string{}
	pageNo := 2
	type sheetRef struct {
		page          sbPage
		file, symUUID string
		no            int
	}
	var sheets []sheetRef
	for _, pg := range d.Pages {
		file := name + ".kicad_sch"
		uuid := st.RootUUID
		inst := "/" + st.RootUUID
		if multi {
			file = sbSheetFile(pg.ID)
			uuid = st.uuid(st.PageUUIDs, pg.ID)
			su := st.uuid(st.SheetSymUUIDs, pg.ID)
			inst = "/" + st.RootUUID + "/" + su
			sheets = append(sheets, sheetRef{pg, file, su, pageNo})
			pageNo++
		}
		tb := d.Spec.Title.kicad()
		if tb.Title == "" {
			tb.Title = name
		}
		if multi {
			t := pg.Title
			if t == "" {
				t = pg.ID
			}
			tb.Title = tb.Title + " — " + t
		}
		e, err := sbNewSheet(uuid, !multi, tb)
		if err != nil {
			return nil, nil, err
		}
		e.Project, e.InstancePath = name, inst
		e.SetPowerRefNext(pwr)
		e.SetFlagFloor(flg)
		var flagNets []string
		for n, p := range flagPage {
			if p == pg.ID {
				flagNets = append(flagNets, n)
			}
		}
		sort.Strings(flagNets)
		if err := renderSbPage(e, d, st, pg, zis, layouts, trans, flagNets); err != nil {
			return nil, nil, fmt.Errorf("page %s: %w", pg.ID, err)
		}
		pwr = e.PowerRefNext()
		text, err := e.Render()
		if err != nil {
			return nil, nil, fmt.Errorf("page %s: %w", pg.ID, err)
		}
		flg = max(flg, kicad.MaxFlagRef(text)+1)
		text, fr, err := kicad.FitSheet(text)
		if err != nil {
			return nil, nil, err
		}
		fits[file] = fr
		if err := write(file, []byte(text)); err != nil {
			return nil, nil, err
		}
		for _, n := range d.Nets {
			if k := d.NetKind[n.Name]; k == "power" || k == "ground" {
				powerSyms[n.Name] = mustRename(kicad.PowerSymbolText(n.Name, k == "ground"), n.Name)
			}
		}
	}
	if multi {
		tb := d.Spec.Title.kicad()
		if tb.Title == "" {
			tb.Title = name
		}
		re, err := sbNewSheet(st.RootUUID, true, tb)
		if err != nil {
			return nil, nil, err
		}
		// the root shows the functional blocks: one sheet symbol per page,
		// a sheet pin per cross-page signal net (nets shared with an earlier
		// page on the left edge, the rest on the right), each pin on a short
		// stub with the net's label; supplies stay power symbols
		pageIdx := map[string]int{}
		for i, s := range sheets {
			pageIdx[s.page.ID] = i
		}
		type sheetPins struct {
			left, right []string
			x, w, h     float64
			title       string
		}
		sp := make([]sheetPins, len(sheets))
		for i, s := range sheets {
			for _, n := range d.Nets {
				pg := d.NetPages[n.Name]
				if len(pg) < 2 || !pg[s.page.ID] || d.NetKind[n.Name] != "signal" {
					continue
				}
				earlier := false
				for p := range pg {
					earlier = earlier || pageIdx[p] < i
				}
				if earlier {
					sp[i].left = append(sp[i].left, n.Name)
				} else {
					sp[i].right = append(sp[i].right, n.Name)
				}
			}
			sort.Strings(sp[i].left)
			sort.Strings(sp[i].right)
			sp[i].title = s.page.Title
			if sp[i].title == "" {
				sp[i].title = s.page.ID
			}
		}
		same := func(a, b []string) bool { return len(a) > 0 && strings.Join(a, "\x00") == strings.Join(b, "\x00") }
		x := 25.4
		for i := range sp {
			lw, rw := 0.0, 0.0
			for _, n := range sp[i].left {
				lw = math.Max(lw, sbTextW(n))
			}
			for _, n := range sp[i].right {
				rw = math.Max(rw, sbTextW(n))
			}
			sp[i].w = math.Max(50.8, sbSnap(math.Max(sbTextW(sp[i].title), lw+rw+10.16)))
			sp[i].h = sbSnap(math.Max(15.24, float64(max(len(sp[i].left), len(sp[i].right))+1)*2.54+2.54))
			if i == 0 || !same(sp[i-1].right, sp[i].left) {
				x += sbSnap(lw + 7.62 + 2.54)
			}
			sp[i].x = x
			x += sp[i].w + 25.4
			if i+1 < len(sp) && !same(sp[i].right, sp[i+1].left) {
				x += sbSnap(rw + 7.62 + 2.54)
			}
		}
		const y = 38.1
		for i, s := range sheets {
			var pins strings.Builder
			addPin := func(net string, px, py, ang float64, just string, stub bool) {
				fmt.Fprintf(&pins, "\n\t\t(pin %s bidirectional (at %s %s %s) (uuid %s) (effects (font (size 1.27 1.27)) (justify %s)))",
					kicad.Q(net), kicad.F(px), kicad.F(py), kicad.F(ang), kicad.Q(st.uuid(st.SheetSymUUIDs, s.page.ID+"#pin#"+net)), just)
				if !stub {
					return
				}
				dx := 5.08
				if ang == 180 {
					dx = -5.08
				}
				re.AddWire(kicad.Pt{X: px, Y: py}, kicad.Pt{X: px + dx, Y: py})
				la := 0.0
				if dx < 0 {
					la = 180
				}
				_ = re.AddLabel(kicad.LabelLocal, net, kicad.Pt{X: px + dx, Y: py}, la, "")
			}
			wiredLeft := i > 0 && same(sp[i-1].right, sp[i].left)
			wiredRight := i+1 < len(sp) && same(sp[i].right, sp[i+1].left)
			for k, n := range sp[i].left {
				addPin(n, sp[i].x, y+2.54*float64(k+1), 180, "left", !wiredLeft)
			}
			for k, n := range sp[i].right {
				py := y + 2.54*float64(k+1)
				addPin(n, sp[i].x+sp[i].w, py, 0, "right", !wiredRight)
				if wiredRight { // facing blocks: one short wire per interface net
					re.AddWire(kicad.Pt{X: sp[i].x + sp[i].w, Y: py}, kicad.Pt{X: sp[i+1].x, Y: py})
				}
			}
			re.AddRaw(fmt.Sprintf(`(sheet
		(at %s %s)
		(size %s %s)
		(exclude_from_sim no) (in_bom yes) (on_board yes) (dnp no)
		(stroke (width 0.1524) (type solid))
		(fill (color 0 0 0 0.0000))
		(uuid %s)
		(property "Sheetname" %s (at %s %s 0) (effects (font (size 1.27 1.27)) (justify left bottom)))
		(property "Sheetfile" %s (at %s %s 0) (effects (font (size 1.27 1.27)) (justify left top)))%s
		(instances (project %s (path %s (page %s))))
	)`, kicad.F(sp[i].x), kicad.F(y), kicad.F(sp[i].w), kicad.F(sp[i].h), kicad.Q(s.symUUID), kicad.Q(sp[i].title), kicad.F(sp[i].x), kicad.F(y-0.7), kicad.Q(s.file), kicad.F(sp[i].x), kicad.F(y+sp[i].h+0.6),
				pins.String(), kicad.Q(name), kicad.Q("/"+st.RootUUID), kicad.Q(strconv.Itoa(s.no))))
		}
		rt, err := re.Render()
		if err != nil {
			return nil, nil, err
		}
		rt, fr, err := kicad.FitSheet(rt)
		if err != nil {
			return nil, nil, err
		}
		fits[name+".kicad_sch"] = fr
		if err := write(name+".kicad_sch", []byte(rt)); err != nil {
			return nil, nil, err
		}
	}
	// project file
	pro := map[string]any{"meta": map[string]any{"filename": name + ".kicad_pro", "version": 3},
		"schematic": map[string]any{"legacy_lib_dir": "", "legacy_lib_list": []any{}}}
	if multi {
		var sh []any
		sh = append(sh, []any{st.RootUUID, "Root"})
		for _, s := range sheets {
			sh = append(sh, []any{s.symUUID, s.page.ID})
		}
		pro["sheets"] = sh
	}
	pb, _ := json.MarshalIndent(pro, "", "  ")
	if err := write(name+".kicad_pro", append(pb, '\n')); err != nil {
		return nil, nil, err
	}
	// libraries
	libs := map[string]map[string]string{} // nickname → bare name → text
	for _, p := range d.Parts {
		if p.LibName == "" {
			continue
		}
		if !sbSafeName(p.LibName) || strings.ContainsAny(p.LibName, " ") {
			return nil, nil, fmt.Errorf("%s: library nickname %q is not a safe file name", p.Ref, p.LibName)
		}
		if libs[p.LibName] == nil {
			libs[p.LibName] = map[string]string{}
		}
		libs[p.LibName][p.SymName] = mustRename(p.SymText, p.SymName)
	}
	powerSyms["PWR_FLAG"] = kicad.PwrFlagSymbolText()
	libs["pcbpilot_power"] = powerSyms
	symTable := map[string]string{}
	var libNames []string
	for n := range libs {
		libNames = append(libNames, n)
	}
	sort.Strings(libNames)
	for _, n := range libNames {
		if err := write(n+".kicad_sym", []byte(kicad.SymbolLibText(libs[n]))); err != nil {
			return nil, nil, err
		}
		symTable[n] = n + ".kicad_sym"
	}
	if err := write("sym-lib-table", []byte(kicad.LibTableText("sym_lib_table", symTable))); err != nil {
		return nil, nil, err
	}
	fpTable := map[string]string{}
	for _, p := range d.Parts {
		if p.FPFile == "" {
			continue
		}
		lib, fp, _ := strings.Cut(p.Footprint, ":")
		if !sbSafeName(lib) || !sbSafeName(fp) {
			return nil, nil, fmt.Errorf("%s: footprint %q is not a safe file name", p.Ref, p.Footprint)
		}
		rel := lib + ".pretty/" + fp + ".kicad_mod"
		if sbContains(files, rel) {
			continue
		}
		b, err := os.ReadFile(p.FPFile)
		if err != nil {
			return nil, nil, err
		}
		if err := write(rel, b); err != nil {
			return nil, nil, err
		}
		fpTable[lib] = lib + ".pretty"
	}
	if len(fpTable) > 0 {
		if err := write("fp-lib-table", []byte(kicad.LibTableText("fp_lib_table", fpTable))); err != nil {
			return nil, nil, err
		}
	}
	sort.Strings(files)
	return files, fits, nil
}

// sbNewSheet is an empty A4 sheet with its title block (kicad SetTitleBlock).
func sbNewSheet(uuid string, root bool, tb kicad.TitleBlock) (*kicad.SchEditor, error) {
	e, err := kicad.OpenSchematic(kicad.NewSchematicText("A4", uuid, root))
	if err != nil {
		return nil, err
	}
	if err := e.SetTitleBlock(tb); err != nil {
		return nil, err
	}
	text, err := e.Render()
	if err != nil {
		return nil, err
	}
	return kicad.OpenSchematic(text)
}

func mustRename(sym, id string) string {
	t, err := kicad.RenameLibSymbol(sym, id)
	if err != nil {
		return sym
	}
	return t
}

// renderSbPage draws the zones of one page: symbols, wires, junctions,
// markers, no-connects, zone frames + titles, PWR_FLAGs.
func renderSbPage(e *kicad.SchEditor, d *sbDesign, st *sbState, pg sbPage, zis []*sbZoneIn, layouts map[string]*sbZoneLayout, trans map[string]kicad.Pt, flagNets []string) error {
	m := &sbMeasure{cache: map[string]kicad.SceneSymbol{}}
	var segs [][2]kicad.Pt
	pinPts := map[kicad.Pt]int{}
	bottom := sbPageOrigin
	for _, zi := range zis {
		if zi.Zone.Page != pg.ID {
			continue
		}
		zl, t := layouts[zi.Zone.ID], trans[zi.Zone.ID]
		mv := func(p kicad.Pt) kicad.Pt { return kicad.Pt{X: sbRound4(p.X + t.X), Y: sbRound4(p.Y + t.Y)} }
		for _, p := range zi.Parts {
			if err := e.AddLibSymbol(p.LibID, p.SymText); err != nil {
				return err
			}
			for _, pose := range zl.Parts[p.Ref] {
				unit := max(pose.Unit, 1)
				s, err := m.get(p, unit)
				if err != nil {
					return err
				}
				ap := sbPose{At: mv(pose.At), Rot: pose.Rot, Unit: unit}
				f := sbFields(s, ap, p.Ref, p.Value)
				ang := 0.0
				if math.Mod(pose.Rot, 180) == 90 {
					ang = 90
				}
				fields := []kicad.Field{{Name: "Reference", Value: p.Ref, At: &f.Ref, Angle: ang}, {Name: "Value", Value: p.Value, At: &f.Val, Angle: ang}}
				extra := map[string]string{}
				for k, v := range p.Fields {
					extra[k] = v
				}
				if p.LCSC != "" {
					extra[kicad.DefaultLCSCField] = p.LCSC
				}
				if p.MPN != "" {
					extra["MPN"] = p.MPN
				}
				extra["pcbpilot_zone"] = p.ZoneID
				if p.Block != "" {
					extra["pcbpilot_block"] = p.Block + "." + p.Role
				}
				var keys []string
				for k := range extra {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fields = append(fields, kicad.Field{Name: k, Value: extra[k], Hide: true})
				}
				uuid := st.uuid(st.SymUUIDs, fmt.Sprintf("%s/%d", p.Ref, unit))
				if _, err := e.PlaceSymbol(kicad.SymbolInstance{LibID: p.LibID, Ref: p.Ref, Unit: unit, At: ap.At, Rot: ap.Rot,
					Value: p.Value, Footprint: p.Footprint, Fields: fields, UUID: uuid}); err != nil {
					return err
				}
				for _, q := range s.Pins {
					pinPts[sbXform(q.At, ap)]++
				}
			}
		}
		for _, w := range zl.Wires {
			var pts []kicad.Pt
			for _, p := range w {
				pts = append(pts, mv(p))
			}
			e.AddWire(pts...)
			for i := 0; i+1 < len(pts); i++ {
				if pts[i] != pts[i+1] {
					segs = append(segs, [2]kicad.Pt{pts[i], pts[i+1]})
				}
			}
		}
		for _, mk := range zl.Markers {
			at := mv(mk.At)
			if mk.OnWire {
				ang := map[string]float64{"right": 0, "up": 90, "left": 180, "down": 270}[mk.Dir]
				kind, shape := kicad.LabelLocal, ""
				if len(d.NetPages[mk.Net]) > 1 { // reaches the root through a sheet pin
					kind, shape = kicad.LabelHier, "bidirectional"
				}
				if err := e.AddLabel(kind, mk.Net, at, ang, shape); err != nil {
					return err
				}
				continue
			}
			if mk.Kind == "power" || mk.Kind == "ground" {
				// supply symbols always point up, ground symbols down (EG-05):
				// a stub that arrives the other way gets a short jog
				if (mk.Kind == "power" && mk.Dir == "down") || (mk.Kind == "ground" && mk.Dir == "up") {
					j := kicad.Pt{X: at.X + 2.54, Y: at.Y}
					e.AddWire(at, j)
					segs = append(segs, [2]kicad.Pt{at, j})
					at = j
				}
				if _, err := e.AddPower(mk.Net, at, 0, mk.Kind == "ground"); err != nil {
					return err
				}
				pinPts[at]++
				continue
			}
			if len(d.NetPages[mk.Net]) > 1 { // cross-page signal: hierarchical label → sheet pin
				ang := map[string]float64{"right": 0, "up": 90, "left": 180, "down": 270}[mk.Dir]
				if err := e.AddLabel(kicad.LabelHier, mk.Net, at, ang, "bidirectional"); err != nil {
					return err
				}
				continue
			}
			if _, err := kicadPlaceMarker(e, "net_label", mk.Net, at, mk.Dir, nil); err != nil {
				return err
			}
		}
		for _, p := range zl.NoConnects {
			e.AddNoConnect(mv(p))
		}
		fp := sbFootprint(zl.Box, t)
		e.AddRect(kicad.Pt{X: sbSnapHalf(fp.MinX), Y: sbSnapHalf(fp.MinY + sbZoneTitle)}, kicad.Pt{X: sbSnapHalf(fp.MaxX), Y: sbSnapHalf(fp.MaxY)})
		title := zi.Zone.Title
		e.AddText(title, kicad.Pt{X: sbSnapHalf(fp.MinX + 1.27), Y: sbSnapHalf(fp.MinY + sbZoneTitle - 1.27)}, 0, 1.778)
		bottom = math.Max(bottom, fp.MaxY)
	}
	// junctions: 3+ connections at a point, or a wire end on another wire
	ends := map[kicad.Pt]int{}
	for _, s := range segs {
		ends[s[0]]++
		ends[s[1]]++
	}
	var junc []kicad.Pt
	for p, n := range ends {
		c := n + pinPts[p]
		for _, s := range segs {
			if sbOnInterior(p, s) {
				c += 2
			}
		}
		if c >= 3 {
			junc = append(junc, p)
		}
	}
	sort.Slice(junc, func(i, j int) bool {
		if junc[i].Y != junc[j].Y {
			return junc[i].Y < junc[j].Y
		}
		return junc[i].X < junc[j].X
	})
	for _, p := range junc {
		e.AddJunction(p)
	}
	// PWR_FLAGs: one per undriven power net, in a row under the zones
	x, y := sbPageOrigin+5.08, sbSnap(bottom+12.7)
	for i, n := range flagNets {
		if i == 0 {
			e.AddText("PWR_FLAG (ERC: power source of each rail)", kicad.Pt{X: sbPageOrigin, Y: y - 7.62}, 0, 1.27)
		}
		p := kicad.Pt{X: sbSnap(x), Y: y}
		ground := d.NetKind[n] == "ground"
		if _, err := e.AddPower(n, p, 0, ground); err != nil {
			return err
		}
		dir := 3 // flag below a supply symbol, above a ground symbol
		if ground {
			dir = 1
		}
		if _, err := e.AddPwrFlag(p, dir, 2.54); err != nil {
			return err
		}
		x += math.Max(15.24, sbTextW(n)+7.62)
	}
	return nil
}

func sbRound4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

func sbSnapHalf(v float64) float64 { return math.Round(v/1.27) * 1.27 }

func sbOnInterior(p kicad.Pt, s [2]kicad.Pt) bool {
	a, b := s[0], s[1]
	if p == a || p == b {
		return false
	}
	const eps = 1e-6
	if math.Abs(a.X-b.X) < eps && math.Abs(p.X-a.X) < eps {
		return p.Y > math.Min(a.Y, b.Y)+eps && p.Y < math.Max(a.Y, b.Y)-eps
	}
	if math.Abs(a.Y-b.Y) < eps && math.Abs(p.Y-a.Y) < eps {
		return p.X > math.Min(a.X, b.X)+eps && p.X < math.Max(a.X, b.X)-eps
	}
	return false
}

func copySbFiles(from, to string, files []string) error {
	for _, f := range files {
		src, err := sbSafeJoin(from, f)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(src)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		dst, err := sbSafeJoin(to, f)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		tmp := dst + ".pcbpilot-tmp"
		if err := os.WriteFile(tmp, b, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			return err
		}
	}
	return nil
}

// ── netlist comparison ──────────────────────────────────────────────────────

type sbNetDiff struct {
	Equal      bool     `json:"equal"`
	Nets       int      `json:"nets"`
	Pins       int      `json:"pins"`
	Missing    []string `json:"missingPins,omitempty"`    // spec pin not on its net
	Extra      []string `json:"unexpectedPins,omitempty"` // pin on a net the spec does not give it
	WrongNet   []string `json:"wrongNet,omitempty"`
	MergedNets []string `json:"mergedNets,omitempty"`
}

func (d sbNetDiff) summary() string {
	if d.Equal {
		return "equal"
	}
	var parts []string
	add := func(label string, xs []string) {
		if len(xs) == 0 {
			return
		}
		s := xs
		if len(s) > 6 {
			s = append(append([]string{}, s[:6]...), fmt.Sprintf("… %d more", len(xs)-6))
		}
		parts = append(parts, label+": "+strings.Join(s, ", "))
	}
	add("wrong net", d.WrongNet)
	add("missing", d.Missing)
	add("unexpected", d.Extra)
	add("merged nets", d.MergedNets)
	return strings.Join(parts, "; ")
}

// compareSbNetlist: every spec pin on a KiCad net of the same name with
// exactly the spec's members; every other pin unconnected.
func compareSbNetlist(d *sbDesign, nl *kicad.SchNetlist) sbNetDiff {
	diff := sbNetDiff{Nets: len(d.Nets), Pins: len(d.PinNet)}
	actual := map[string]string{} // pin → net (named nets only)
	for _, n := range nl.Nets {
		if strings.HasPrefix(n.Name, "unconnected-(") {
			continue
		}
		real := 0
		for _, nd := range n.Nodes {
			if !strings.HasPrefix(nd.Ref, "#") {
				real++
			}
		}
		for _, nd := range n.Nodes {
			if strings.HasPrefix(nd.Ref, "#") {
				continue
			}
			k := nd.Ref + "." + nd.Pin
			name := stripSheetPath(n.Name)
			if real == 1 && strings.HasPrefix(n.Name, "Net-(") {
				continue // a lone pin KiCad named after itself = unconnected
			}
			actual[k] = name
		}
	}
	for k, want := range d.PinNet {
		got, ok := actual[k]
		switch {
		case !ok:
			diff.Missing = append(diff.Missing, k+"→"+want)
		case got != want:
			diff.WrongNet = append(diff.WrongNet, fmt.Sprintf("%s on %q, spec %q", k, got, want))
		}
	}
	for k, got := range actual {
		if _, ok := d.PinNet[k]; !ok {
			diff.Extra = append(diff.Extra, k+"→"+got)
		}
	}
	// two spec nets that ended up as one KiCad net
	byNet := map[string]map[string]bool{}
	for k, got := range actual {
		if want, ok := d.PinNet[k]; ok {
			if byNet[got] == nil {
				byNet[got] = map[string]bool{}
			}
			byNet[got][want] = true
		}
	}
	for got, wants := range byNet {
		if len(wants) > 1 {
			var ws []string
			for w := range wants {
				ws = append(ws, w)
			}
			sort.Strings(ws)
			diff.MergedNets = append(diff.MergedNets, got+"="+strings.Join(ws, "+"))
		}
	}
	sort.Strings(diff.Missing)
	sort.Strings(diff.Extra)
	sort.Strings(diff.WrongNet)
	sort.Strings(diff.MergedNets)
	diff.Equal = len(diff.Missing)+len(diff.Extra)+len(diff.WrongNet)+len(diff.MergedNets) == 0
	return diff
}

// ── offline layout checks ───────────────────────────────────────────────────

// sbQualityCheck runs the KiCad schematic quality checks of `kicad
// sch-check` (kicad.CheckSchematic) on every written sheet.
func sbQualityCheck(dir string, files []string) sbGate {
	type sheet struct {
		File     string          `json:"file"`
		Findings []kicad.Finding `json:"findings"`
	}
	var out []sheet
	n := 0
	counts := map[string]int{}
	for _, f := range files {
		if !strings.HasSuffix(f, ".kicad_sch") {
			continue
		}
		fp, err := sbSafeJoin(dir, f)
		if err != nil {
			return sbGate{Name: "quality", Status: "fail", Detail: err.Error()}
		}
		b, err := os.ReadFile(fp)
		if err != nil {
			return sbGate{Name: "quality", Status: "fail", Detail: err.Error()}
		}
		fs := kicad.CheckSchematic(string(b), kicad.CheckOptions{})
		for _, x := range fs {
			counts[x.Kind]++
		}
		n += len(fs)
		if len(fs) > 0 {
			if len(fs) > 25 {
				fs = fs[:25]
			}
			out = append(out, sheet{f, fs})
		}
	}
	if n == 0 {
		return sbGate{Name: "quality", Status: "pass", Detail: "kicad sch-check quality: no findings"}
	}
	var cs []string
	for k, v := range counts {
		cs = append(cs, fmt.Sprintf("%s %d", k, v))
	}
	sort.Strings(cs)
	return sbGate{Name: "quality", Status: "fail", Detail: fmt.Sprintf("kicad sch-check quality: %d finding(s): %s", n, strings.Join(cs, ", ")), Data: out}
}

// ── dry-run plan ────────────────────────────────────────────────────────────

func sbMakePlan(d *sbDesign, zis []*sbZoneIn, layouts map[string]*sbZoneLayout, trans map[string]kicad.Pt) *sbPlan {
	p := &sbPlan{Gates: []string{"netlist == spec (hard, transactional)", "kicad-cli ERC", "page fit (A4…A0)", "layout overlaps", "connectivity.json", "intent derive"}}
	markers := map[string][]string{}
	wires := map[string]int{}
	for _, zi := range zis {
		zl, t := layouts[zi.Zone.ID], trans[zi.Zone.ID]
		for _, part := range zi.Parts {
			for _, pose := range zl.Parts[part.Ref] {
				p.Placements = append(p.Placements, sbPlanPart{Ref: part.Ref, Page: zi.Zone.Page, Zone: zi.Zone.ID,
					X: sbRound2(pose.At.X + t.X), Y: sbRound2(pose.At.Y + t.Y), Rot: pose.Rot, Symbol: part.LibID, Source: part.Source})
			}
		}
		for _, mk := range zl.Markers {
			kind := mk.Kind
			if kind == "label" && len(d.NetPages[mk.Net]) > 1 {
				kind = "global-label"
			}
			markers[mk.Net] = append(markers[mk.Net], fmt.Sprintf("%s@%s,%s", kind, kicad.F(sbRound2(mk.At.X+t.X)), kicad.F(sbRound2(mk.At.Y+t.Y))))
		}
		wires[zi.Zone.ID] += len(zl.Wires)
	}
	for _, n := range d.Nets {
		p.Connections = append(p.Connections, sbPlanNet{Net: n.Name, Kind: d.NetKind[n.Name], Pins: n.Pins, Markers: markers[n.Name]})
	}
	for k := range d.NC {
		p.NoConnect = append(p.NoConnect, k)
	}
	sort.Strings(p.NoConnect)
	p.Files = []string{"<name>.kicad_pro", "<name>.kicad_sch"}
	if len(d.Pages) > 1 {
		for _, pg := range d.Pages {
			p.Files = append(p.Files, sbSheetFile(pg.ID))
		}
	}
	p.Files = append(p.Files, "sym-lib-table", "fp-lib-table", "*.kicad_sym", "lcsc.pretty/", "connectivity.json", "values.json", "intent-spec.json", "intent.json", "erc.json")
	return p
}

// ── intent inputs ───────────────────────────────────────────────────────────

func (d *sbDesign) values() map[string]any {
	parts := map[string]any{}
	for _, p := range d.Parts {
		v := map[string]string{"value": p.Value}
		if p.MPN != "" {
			v["mpn"] = p.MPN
		}
		if p.LCSC != "" {
			v["lcsc"] = p.LCSC
		}
		parts[p.Ref] = v
	}
	return map[string]any{"parts": parts}
}

// intentSpec is spec.intent with the rails (spec rails + net voltage /
// current annotations) merged in.
func (d *sbDesign) intentSpec() map[string]any {
	out := map[string]any{}
	if len(d.Spec.Intent) > 0 {
		_ = json.Unmarshal(d.Spec.Intent, &out)
	}
	rails := map[string]map[string]any{}
	var order []string
	if rs, ok := out["rails"].([]any); ok {
		for _, r := range rs {
			if m, ok := r.(map[string]any); ok {
				if n, _ := m["net"].(string); n != "" {
					rails[n] = m
					order = append(order, n)
				}
			}
		}
	}
	set := func(net string, v, a float64) {
		m := rails[net]
		if m == nil {
			m = map[string]any{"net": net}
			rails[net] = m
			order = append(order, net)
		}
		if v != 0 {
			m["voltage"] = v
		}
		if a != 0 {
			m["currentA"] = a
		}
	}
	for _, r := range d.Spec.Rails {
		set(r.Net, r.Voltage, r.CurrentA)
	}
	for _, n := range d.Nets {
		if n.VoltageV != 0 || n.CurrentA != 0 {
			set(n.Name, n.VoltageV, n.CurrentA)
		}
	}
	if len(order) > 0 {
		var rs []any
		for _, n := range order {
			rs = append(rs, rails[n])
		}
		out["rails"] = rs
	}
	return out
}

// ── checkpoints ─────────────────────────────────────────────────────────────

func sbCheckpointDir(out string) string { return filepath.Join(out, ".pcbpilot", "checkpoints") }

type sbCheckpointMeta struct {
	N     int      `json:"n"`
	Time  string   `json:"time"`
	Mode  string   `json:"mode"`
	Files []string `json:"files"`
}

// saveSbCheckpoint copies the project's sheets, libraries, spec and state
// into the next checkpoint directory.
func saveSbCheckpoint(out string, files []string, mode string) (int, error) {
	list, _ := listSbCheckpoints(out)
	n := 1
	if len(list) > 0 {
		n = list[len(list)-1].N + 1
	}
	dir := filepath.Join(sbCheckpointDir(out), strconv.Itoa(n))
	if err := copySbFiles(out, dir, files); err != nil {
		return 0, err
	}
	if err := copySbFiles(sbMetaDir(out), filepath.Join(dir, ".sch-build"), []string{"spec.json", "state.json"}); err != nil {
		return 0, err
	}
	meta := sbCheckpointMeta{N: n, Time: time.Now().Format(time.RFC3339), Mode: mode, Files: files}
	return n, writeJSONFile(filepath.Join(dir, "checkpoint.json"), meta)
}

func listSbCheckpoints(out string) ([]sbCheckpointMeta, error) {
	ents, err := os.ReadDir(sbCheckpointDir(out))
	if err != nil {
		return nil, err
	}
	var list []sbCheckpointMeta
	for _, e := range ents {
		b, err := os.ReadFile(filepath.Join(sbCheckpointDir(out), e.Name(), "checkpoint.json"))
		if err != nil {
			continue
		}
		var m sbCheckpointMeta
		if json.Unmarshal(b, &m) == nil && m.N > 0 {
			list = append(list, m)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].N < list[j].N })
	return list, nil
}
