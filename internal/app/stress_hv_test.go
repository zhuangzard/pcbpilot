//go:build stress

package app

// High-voltage stress suite (make stress-hv): every case under
// testdata/stress/hv/<case>/ runs the full offline chain
//
//	sim power → intent derive --spec → pcb auto run --intent --sim [--place]
//	→ pcb check --intent --board board.routed.json
//
// and is compared with the engineering answer derived by hand from the
// standard in expect.json. Slow (place + route per variant): kept out of the
// -short CI run by the build tag.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

const stressHVDir = "../../testdata/stress/hv"

type hvExpect struct {
	Case     string      `json:"case"`
	Summary  string      `json:"summary"`
	Variants []hvVariant `json:"variants"`
}

type hvVariant struct {
	Name  string `json:"name"`
	Spec  string `json:"spec"`
	Route struct {
		Place         bool    `json:"place"`
		RouteOnly     bool    `json:"routeOnly"`
		Timeout       string  `json:"timeout"`
		Grid          float64 `json:"grid"`
		Layers        int     `json:"layers"`
		Loops         int     `json:"loops"`
		MinCompletion float64 `json:"minCompletion"`
	} `json:"route"`
	Domains []struct {
		Kinds        []string `json:"kinds"`
		Nets         []string `json:"nets"`
		WorkingVrms  float64  `json:"workingVrms"`
		WorkingVpeak float64  `json:"workingVpeak"`
	} `json:"domains"`
	Pairs []struct {
		Kinds       [2][]string `json:"kinds"`
		Insulation  string      `json:"insulation"`
		ClearanceMm float64     `json:"clearanceMm"`
		CreepageMm  float64     `json:"creepageMm"`
		SlotWidthMm float64     `json:"slotWidthMm"`
		MOP         string      `json:"mop"`
		MOPCount    int         `json:"mopCount"`
		Transient   string      `json:"transient"`
		MainsVrms   float64     `json:"mainsVrms"`
		Bridges     []string    `json:"bridges"`
		Why         string      `json:"why"`
	} `json:"pairs"`
	Nets map[string]struct {
		CurrentA       float64 `json:"currentA"`
		WidthOuterMil  float64 `json:"widthOuterMil"`
		WidthInnerMil  float64 `json:"widthInnerMil"`
		Vias           int     `json:"vias"`
		ViaDrillMil    float64 `json:"viaDrillMil"`
		ClearanceMil   float64 `json:"clearanceMil"`
		VoltageMax     float64 `json:"voltageMax"`
		VoltagePeak    float64 `json:"voltagePeak"`
		VoltageTolFrac float64 `json:"voltageTol"`
	} `json:"nets"`
	Slots struct {
		Under     []string           `json:"under"`
		NotUnder  []string           `json:"notUnder"`
		LengthMil map[string]float64 `json:"lengthMil"`
	} `json:"slots"`
	Findings struct {
		NoErrors bool     `json:"noErrors"`
		Expect   []string `json:"expect"`
		Absent   []string `json:"absent"`
	} `json:"findings"`
	Infeasible     []string `json:"infeasible"`
	MaxIsoFindings *int     `json:"maxIsoFindings"`
	// Chain: the divider nodes' peak voltages; the routed copper of two
	// chain nets must keep IPC-2221B B2 at their voltage difference.
	Chain map[string]float64 `json:"chain"`
	// Routed nets that must be complete (gate / Kelvin loops) and the
	// longest their routed copper may be (mm).
	MustRoute []string `json:"mustRoute"`
	// NeedsPour: nets too wide for a routed track, reported "needs-pour".
	NeedsPour   []string           `json:"needsPour"`
	MaxLengthMm map[string]float64 `json:"maxLengthMm"`
	// KnownLimits are checks (by prefix) that currently fail for a documented
	// engine limit: reported as KNOWN, not counted as failures. Each entry
	// names the limit (recipes/hv-isolation.md 能力边界).
	KnownLimits []struct {
		Check string `json:"check"`
		Why   string `json:"why"`
	} `json:"knownLimits"`
}

// hvResult is one variant's outcome (the summary table rows).
type hvResult struct {
	rows  []string
	fail  int
	known []string // check prefixes allowed to fail (documented limits)
}

func (r *hvResult) check(ok bool, what, want, got string) {
	mark := "ok"
	if !ok {
		mark = "FAIL"
		for _, k := range r.known {
			if strings.HasPrefix(what, k) {
				mark = "KNOWN"
			}
		}
		if mark == "FAIL" {
			r.fail++
		}
	}
	r.rows = append(r.rows, fmt.Sprintf("%-4s | %-44s | %-30s | %s", mark, what, want, got))
}

func approx(got, want, tol float64) bool { return math.Abs(got-want) <= tol+1e-9 }

func hvRunCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := newRootCmd(&stdout, &stderr)
	root.SetArgs(args)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

func TestStressHV(t *testing.T) {
	cases, err := filepath.Glob(filepath.Join(stressHVDir, "*", "expect.json"))
	if err != nil || len(cases) == 0 {
		t.Fatalf("no stress cases under %s", stressHVDir)
	}
	only := os.Getenv("STRESS_HV_CASE")
	lib := "../../.agents/skills/pcbpilot/references/power-models.json"
	var table []string
	for _, ef := range cases {
		dir := filepath.Dir(ef)
		name := filepath.Base(dir)
		if only != "" && only != name {
			continue
		}
		raw, err := os.ReadFile(ef)
		if err != nil {
			t.Fatal(err)
		}
		var ex hvExpect
		if err := json.Unmarshal(raw, &ex); err != nil {
			t.Fatalf("%s: %v", ef, err)
		}
		for _, v := range ex.Variants {
			v := v
			t.Run(name+"/"+v.Name, func(t *testing.T) {
				res := runHVVariant(t, dir, lib, v)
				for _, r := range res.rows {
					table = append(table, name+"/"+v.Name+" | "+r)
				}
				if res.fail > 0 {
					t.Errorf("%d expectation(s) failed", res.fail)
				}
			})
		}
	}
	t.Logf("\n%s", strings.Join(table, "\n"))
}

func runHVVariant(t *testing.T, dir, lib string, v hvVariant) *hvResult {
	res := &hvResult{}
	for _, k := range v.KnownLimits {
		res.known = append(res.known, k.Check)
	}
	out := t.TempDir()
	if keep := os.Getenv("STRESS_HV_OUT"); keep != "" {
		out = filepath.Join(keep, filepath.Base(dir), v.Name)
		_ = os.MkdirAll(out, 0o755)
	}
	conn, vals, models := filepath.Join(dir, "connectivity.json"), filepath.Join(dir, "values.json"), filepath.Join(dir, "models.json")
	simPath, intentPath := filepath.Join(out, "sim.json"), filepath.Join(out, "intent.json")
	if _, se, err := hvRunCLI(t, "sim", "power", "--connectivity", conn, "--values", vals, "--models", models, "--models-lib", lib, "--out", simPath); err != nil {
		t.Fatalf("sim power: %v\n%s", err, se)
	}
	if _, se, err := hvRunCLI(t, "intent", "derive", "--connectivity", conn, "--values", vals, "--models", models, "--models-lib", lib,
		"--sim", simPath, "--spec", filepath.Join(dir, v.Spec), "--out", intentPath, "--report", filepath.Join(out, "intent.md")); err != nil {
		t.Fatalf("intent derive: %v\n%s", err, se)
	}
	raw, err := os.ReadFile(intentPath)
	if err != nil {
		t.Fatal(err)
	}
	var it intent.Intent
	if err := json.Unmarshal(raw, &it); err != nil {
		t.Fatal(err)
	}
	hvCheckIntent(res, &it, v)

	type run struct {
		name string
		args []string
	}
	var runs []run
	common := []string{"pcb", "auto", "run", "--board", filepath.Join(dir, "board.json"), "--intent", intentPath, "--sim", simPath, "--no-feedback", "--seed", "1"}
	if v.Route.Timeout != "" {
		common = append(common, "--timeout", v.Route.Timeout)
	}
	if v.Route.Grid > 0 {
		common = append(common, "--grid", fmt.Sprint(v.Route.Grid))
	}
	if v.Route.Layers > 0 {
		common = append(common, "--layers", fmt.Sprint(v.Route.Layers))
	}
	if v.Route.Place {
		a := append(append([]string(nil), common...), "--place", "--loops", fmt.Sprint(max(v.Route.Loops, 0)), "--out-dir", filepath.Join(out, "place"))
		runs = append(runs, run{"place", a})
	}
	if v.Route.RouteOnly {
		runs = append(runs, run{"route", append(append([]string(nil), common...), "--out-dir", filepath.Join(out, "route"))})
	}
	for _, r := range runs {
		_, se, err := hvRunCLI(t, r.args...)
		if err != nil {
			t.Fatalf("pcb auto run (%s): %v\n%s", r.name, err, se)
		}
		t.Logf("%s: %s", r.name, hvLastLines(se, 6))
		hvCheckAuto(t, res, r.name, filepath.Join(out, r.name), intentPath, v)
	}
	return res
}

func hvLastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " ⏎ ")
}

// kindsOf maps domain ids to kinds.
func hvKindsOf(it *intent.Intent) map[string]string {
	m := map[string]string{}
	for _, d := range it.Domains {
		m[d.ID] = d.Kind
	}
	return m
}

func hvInList(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func hvCheckIntent(res *hvResult, it *intent.Intent, v hvVariant) {
	kinds := hvKindsOf(it)
	for _, ed := range v.Domains {
		var got *intent.Domain
		for _, d := range it.Domains {
			if hvInList(ed.Kinds, d.Kind) {
				ok := true
				for _, n := range ed.Nets {
					ok = ok && it.Nets[n] != nil && it.Nets[n].Domain == d.ID
				}
				if ok {
					got = d
				}
			}
		}
		what := fmt.Sprintf("domain %s %v", strings.Join(ed.Kinds, "|"), ed.Nets)
		if got == nil {
			res.check(false, what, "present", "missing")
			continue
		}
		res.check(approx(got.WorkingVrms, ed.WorkingVrms, 0.01*ed.WorkingVrms+0.1) && approx(got.WorkingVpeak, ed.WorkingVpeak, 0.01*ed.WorkingVpeak+0.1),
			what+" V", fmt.Sprintf("%.1f Vrms / %.1f Vpk", ed.WorkingVrms, ed.WorkingVpeak), fmt.Sprintf("%.1f / %.1f (%s)", got.WorkingVrms, got.WorkingVpeak, got.ID))
	}
	for _, ep := range v.Pairs {
		var got *intent.Pair
		for _, p := range it.Pairs {
			ka, kb := kinds[strings.TrimPrefix(p.A, "domain:")], kinds[strings.TrimPrefix(p.B, "domain:")]
			if hvInList(ep.Kinds[0], ka) && hvInList(ep.Kinds[1], kb) || hvInList(ep.Kinds[0], kb) && hvInList(ep.Kinds[1], ka) {
				got = p
			}
		}
		what := fmt.Sprintf("pair %s↔%s", strings.Join(ep.Kinds[0], "|"), strings.Join(ep.Kinds[1], "|"))
		if got == nil {
			res.check(false, what, "present", "missing")
			continue
		}
		res.check(got.Insulation == ep.Insulation, what+" insulation", ep.Insulation, got.Insulation)
		res.check(approx(got.ClearanceMm, ep.ClearanceMm, 0.05), what+" clearance mm", fmt.Sprint(ep.ClearanceMm), fmt.Sprint(got.ClearanceMm))
		res.check(approx(got.CreepageMm, ep.CreepageMm, 0.05), what+" creepage mm", fmt.Sprint(ep.CreepageMm), fmt.Sprint(got.CreepageMm))
		if ep.SlotWidthMm > 0 || got.SlotWidthMm > 0 {
			res.check(approx(got.SlotWidthMm, ep.SlotWidthMm, 0.01), what+" slot width mm", fmt.Sprint(ep.SlotWidthMm), fmt.Sprint(got.SlotWidthMm))
		}
		if ep.MOP != "" {
			res.check(got.MOP == ep.MOP && got.MOPCount == ep.MOPCount, what+" means of protection", fmt.Sprintf("%d×%s", ep.MOPCount, ep.MOP), fmt.Sprintf("%d×%s", got.MOPCount, got.MOP))
		}
		if ep.Transient != "" {
			res.check(got.Transient == ep.Transient && approx(got.MainsVrms, ep.MainsVrms, 0.5), what+" transient", fmt.Sprintf("%s @ %.0f V", ep.Transient, ep.MainsVrms), fmt.Sprintf("%s @ %.0f V", got.Transient, got.MainsVrms))
		}
		for _, b := range ep.Bridges {
			res.check(hvInList(got.Bridges, b), what+" bridge "+b, "listed", strings.Join(got.Bridges, ","))
		}
	}
	names := hvKeys(v.Nets)
	for _, n := range names {
		en := v.Nets[n]
		np := it.Nets[n]
		if np == nil {
			res.check(false, "net "+n, "present", "missing")
			continue
		}
		if en.CurrentA > 0 {
			res.check(approx(np.CurrentA, en.CurrentA, 0.03*en.CurrentA+0.001), "net "+n+" current A", fmt.Sprint(en.CurrentA), fmt.Sprintf("%.4g (%s)", np.CurrentA, np.CurrentSource))
		}
		if en.WidthOuterMil > 0 {
			res.check(approx(np.WidthMil.Outer, en.WidthOuterMil, 0.1*en.WidthOuterMil), "net "+n+" outer width mil", fmt.Sprint(en.WidthOuterMil), fmt.Sprint(np.WidthMil.Outer))
		}
		if en.WidthInnerMil > 0 {
			res.check(approx(np.WidthMil.Inner, en.WidthInnerMil, 0.1*en.WidthInnerMil), "net "+n+" inner width mil", fmt.Sprint(en.WidthInnerMil), fmt.Sprint(np.WidthMil.Inner))
		}
		if en.Vias > 0 {
			res.check(np.ViasPerTransition == en.Vias, "net "+n+" vias/transition", fmt.Sprint(en.Vias), fmt.Sprint(np.ViasPerTransition))
		}
		if en.ViaDrillMil > 0 {
			got := 0.0
			if np.Via != nil {
				got = np.Via.DrillMil
			}
			res.check(approx(got, en.ViaDrillMil, 0.01), "net "+n+" via drill mil", fmt.Sprint(en.ViaDrillMil), fmt.Sprint(got))
		}
		if en.ClearanceMil > 0 {
			res.check(approx(np.ClearanceMil, en.ClearanceMil, 0.5), "net "+n+" clearance mil", fmt.Sprint(en.ClearanceMil), fmt.Sprint(np.ClearanceMil))
		}
		tol := en.VoltageTolFrac
		if tol == 0 {
			tol = 0.02
		}
		if en.VoltageMax != 0 {
			res.check(approx(np.Voltage.Max, en.VoltageMax, tol*math.Abs(en.VoltageMax)+0.02), "net "+n+" V max", fmt.Sprint(en.VoltageMax), fmt.Sprint(np.Voltage.Max))
		}
		if en.VoltagePeak != 0 {
			res.check(approx(np.Voltage.Peak, en.VoltagePeak, tol*math.Abs(en.VoltagePeak)+0.02), "net "+n+" V peak", fmt.Sprint(en.VoltagePeak), fmt.Sprint(np.Voltage.Peak))
		}
	}
	errs := 0
	kindsSeen := map[string]bool{}
	for _, f := range it.Findings {
		kindsSeen[f.Kind] = true
		if f.Severity == "error" {
			errs++
		}
	}
	if v.Findings.NoErrors {
		var msgs []string
		for _, f := range it.Findings {
			if f.Severity == "error" {
				msgs = append(msgs, f.Message)
			}
		}
		res.check(errs == 0, "intent findings: no error", "0", fmt.Sprintf("%d %v", errs, msgs))
	}
	for _, k := range v.Findings.Expect {
		res.check(kindsSeen[k], "intent finding "+k, "present", fmt.Sprint(kindsSeen[k]))
	}
	for _, k := range v.Findings.Absent {
		res.check(!kindsSeen[k], "intent finding "+k, "absent", fmt.Sprint(kindsSeen[k]))
	}
}

func hvKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// autoPlan is the part of plan.json the suite reads.
type autoPlan struct {
	Result struct {
		Analysis struct {
			Nets []struct {
				Net               string  `json:"net"`
				WidthMil          float64 `json:"widthMil"`
				ViasPerTransition int     `json:"viasPerTransition"`
				ClearanceMil      float64 `json:"clearanceMil"`
			} `json:"nets"`
		} `json:"analysis"`
		Route struct {
			Stats struct {
				Completion float64 `json:"completion"`
			} `json:"stats"`
			Unrouted []struct {
				Net    string   `json:"net"`
				Pads   []string `json:"pads"`
				Reason string   `json:"reason"`
			} `json:"unrouted"`
			Tracks []struct {
				Net   string  `json:"net"`
				Layer int     `json:"layer"`
				Width float64 `json:"width"`
				A     struct{ X, Y float64 }
				B     struct{ X, Y float64 }
			} `json:"tracks"`
			ViaShortfalls []pcbauto.ViaShortfall `json:"viaShortfalls"`
		} `json:"route"`
		DRC struct {
			Violations []json.RawMessage `json:"violations"`
		} `json:"drc"`
		Isolation *pcbauto.IsolationReport `json:"isolation"`
	} `json:"result"`
}

func hvCheckAuto(t *testing.T, res *hvResult, run, dir, intentPath string, v hvVariant) {
	raw, err := os.ReadFile(filepath.Join(dir, "plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var plan autoPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatal(err)
	}
	r := plan.Result
	iso := r.Isolation
	p := run + ": "
	if iso == nil {
		res.check(false, p+"isolation report", "present", "nil")
		return
	}
	// Per-net plan in the engine (intent widths / vias / clearance applied).
	byNet := map[string]int{}
	for i, n := range r.Analysis.Nets {
		byNet[n.Net] = i
	}
	for _, name := range hvKeys(v.Nets) {
		en := v.Nets[name]
		i, ok := byNet[name]
		if !ok {
			continue
		}
		n := r.Analysis.Nets[i]
		if en.WidthOuterMil > 0 {
			res.check(approx(n.WidthMil, en.WidthOuterMil, 0.1*en.WidthOuterMil), p+"engine width "+name, fmt.Sprint(en.WidthOuterMil), fmt.Sprint(n.WidthMil))
		}
		if en.Vias > 0 {
			res.check(n.ViasPerTransition == en.Vias, p+"engine vias "+name, fmt.Sprint(en.Vias), fmt.Sprint(n.ViasPerTransition))
		}
	}
	// Slots.
	slotRefs := map[string]pcbauto.IsoSlot{}
	for _, s := range iso.Slots {
		slotRefs[s.Ref] = s
	}
	for _, ref := range v.Slots.Under {
		s, ok := slotRefs[ref]
		res.check(ok, p+"slot under "+ref, "planned", fmt.Sprint(ok))
		if want, has := v.Slots.LengthMil[ref]; ok && has {
			res.check(approx(s.LengthMil, want, 12), p+"slot length "+ref+" mil", fmt.Sprint(want), fmt.Sprint(s.LengthMil))
		}
		if ok {
			res.check(s.WidthMil >= 39.37-0.01 || s.WidthMil >= 9.8, p+"slot width "+ref+" mil", "≥ PD slot width", fmt.Sprint(s.WidthMil))
		}
	}
	for _, ref := range v.Slots.NotUnder {
		_, ok := slotRefs[ref]
		res.check(!ok, p+"no slot under "+ref, "none", fmt.Sprint(ok))
	}
	// Infeasible bridges.
	var bad []string
	for _, f := range iso.Infeasible {
		bad = append(bad, f.Ref)
	}
	sort.Strings(bad)
	want := append([]string(nil), v.Infeasible...)
	sort.Strings(want)
	res.check(strings.Join(bad, ",") == strings.Join(want, ","), p+"infeasible bridges", strings.Join(want, ",")+" ", strings.Join(bad, ",")+" ")
	// Playbook: slots as MULTI fills, isolation bands / moats as regions.
	pbRaw, err := os.ReadFile(filepath.Join(dir, "playbook.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pb pcbauto.Playbook
	if err := json.Unmarshal(pbRaw, &pb); err != nil {
		t.Fatal(err)
	}
	slotSteps, bandSteps, moatSteps := 0, 0, 0
	for _, st := range pb.Steps {
		switch {
		case strings.HasPrefix(st.ID, "iso-slot-") && st.Action == "pcb.fill.create" && fmt.Sprint(st.Payload["layer"]) == "12":
			slotSteps++
		case strings.HasPrefix(st.ID, "iso-band-") && st.Action == "pcb.region.create" && strings.Contains(fmt.Sprint(st.Payload["ruleType"]), "no-pours"):
			bandSteps++
		case strings.HasPrefix(st.ID, "iso-moat-") && st.Action == "pcb.region.create" && strings.Contains(fmt.Sprint(st.Payload["ruleType"]), "no-pours"):
			moatSteps++
		}
	}
	res.check(slotSteps == len(iso.Slots), p+"playbook slot fills (MULTI)", fmt.Sprint(len(iso.Slots)), fmt.Sprint(slotSteps))
	if len(iso.Pairs) > 0 && len(v.Infeasible) == 0 {
		res.check(bandSteps+moatSteps > 0, p+"playbook no-pour band/moat", "≥1", fmt.Sprintf("bands %d moats %d", bandSteps, moatSteps))
	}
	// Routing outcome.
	if v.Route.MinCompletion > 0 {
		var un []string
		for _, u := range r.Route.Unrouted {
			un = append(un, u.Net+"("+u.Reason+")")
		}
		res.check(r.Route.Stats.Completion >= v.Route.MinCompletion, p+"routed completion %", fmt.Sprintf("≥ %.0f", v.Route.MinCompletion),
			fmt.Sprintf("%.1f %s", r.Route.Stats.Completion, strings.Join(un, " ")))
	}
	res.check(len(r.DRC.Violations) == 0, p+"engine DRC violations", "0", fmt.Sprint(len(r.DRC.Violations)))
	if v.MaxIsoFindings != nil {
		res.check(len(iso.Findings) <= *v.MaxIsoFindings, p+"engine isolation findings", fmt.Sprintf("≤ %d", *v.MaxIsoFindings), hvIsoSummary(iso.Findings))
	}
	for _, n := range v.NeedsPour {
		reason := ""
		for _, u := range r.Route.Unrouted {
			if u.Net == n && u.Reason == "needs-pour" {
				reason = u.Reason
			}
		}
		res.check(reason != "", p+"needs-pour "+n, "reported", reason)
	}
	for _, n := range v.MustRoute {
		missing := ""
		for _, u := range r.Route.Unrouted {
			if u.Net == n {
				missing = u.Reason
			}
		}
		res.check(missing == "", p+"routed "+n, "complete", "unrouted "+missing)
	}
	for _, n := range hvKeys(v.MaxLengthMm) {
		l := 0.0
		for _, tr := range r.Route.Tracks {
			if tr.Net == n {
				l += math.Hypot(tr.B.X-tr.A.X, tr.B.Y-tr.A.Y) * 0.0254
			}
		}
		res.check(l > 0 && l <= v.MaxLengthMm[n], p+"routed length "+n+" mm", fmt.Sprintf("≤ %.0f", v.MaxLengthMm[n]), fmt.Sprintf("%.1f", l))
	}
	// Offline pcb check on the routed board.
	routed := filepath.Join(dir, "board.routed.json")
	stdout, se, err := hvRunCLI(t, "pcb", "check", "--intent", intentPath, "--board", routed, "--json")
	if err != nil {
		t.Fatalf("pcb check: %v\n%s", err, se)
	}
	var chk pcbCheckReport
	if err := json.Unmarshal([]byte(stdout), &chk); err != nil {
		t.Fatalf("pcb check json: %v\n%s", err, stdout)
	}
	infeas, isoF := 0, 0
	for _, f := range chk.Findings {
		switch f.Type {
		case "iso-infeasible":
			infeas++
		case "iso-clearance", "iso-creepage":
			isoF++
		}
	}
	res.check(infeas == len(v.Infeasible), p+"pcb check iso-infeasible", fmt.Sprint(len(v.Infeasible)), fmt.Sprint(infeas))
	// Via current: every transition carries its sized current, or the
	// router reported the transition it could not complete (viafix.go) —
	// an ERROR the plan does not explain is a silent electrical defect.
	short := map[string]string{}
	for _, s := range r.Route.ViaShortfalls {
		short[s.Net] = s.Reason
	}
	var silent, explained []string
	for _, f := range chk.Findings {
		if f.Type != "via-current" || f.Level != "ERROR" {
			continue
		}
		for _, n := range f.Nets {
			if _, ok := short[n]; ok {
				explained = append(explained, n)
			} else {
				silent = append(silent, n)
			}
		}
	}
	res.check(len(silent) == 0, p+"pcb check via-current ERROR", "0 (or a reported via shortfall)",
		fmt.Sprintf("%d silent %v, %d reported %v", len(silent), silent, len(explained), explained))
	if v.MaxIsoFindings != nil {
		res.check(isoF <= *v.MaxIsoFindings, p+"pcb check clearance/creepage", fmt.Sprintf("≤ %d", *v.MaxIsoFindings), fmt.Sprint(isoF))
	}
	if len(v.Infeasible) > 0 {
		_, _, err := hvRunCLI(t, "pcb", "check", "--intent", intentPath, "--board", routed, "--strict")
		res.check(err != nil, p+"pcb check --strict gates", "non-zero exit", fmt.Sprint(err != nil))
	} else if v.MaxIsoFindings != nil && *v.MaxIsoFindings == 0 {
		res.check(chk.Summary.Isolation == 0, p+"pcb check isolation summary", "0", fmt.Sprint(chk.Summary.Isolation))
	}
	if len(v.Chain) > 0 {
		hvCheckChain(t, res, p, routed, v.Chain)
	}
}

func hvIsoSummary(fs []pcbauto.IsoFinding) string {
	if len(fs) == 0 {
		return "0"
	}
	var s []string
	for i, f := range fs {
		if i == 3 {
			s = append(s, "…")
			break
		}
		s = append(s, fmt.Sprintf("%s %s↔%s %.0f<%.0f", f.Kind, f.ItemA, f.ItemB, math.Max(f.GapMil, f.PathMil), f.RequiredMil))
	}
	return fmt.Sprintf("%d: %s", len(fs), strings.Join(s, "; "))
}

// ipcB2 is IPC-2221B Table 6-1 column B2 (external, uncoated, ≤ 3050 m), mm —
// written out here independently of the engine.
func hvIPCB2(v float64) float64 {
	v = math.Abs(v)
	rows := []struct{ max, mm float64 }{{15, 0.1}, {30, 0.1}, {50, 0.6}, {100, 0.6}, {150, 0.6}, {170, 1.25}, {250, 1.25}, {300, 1.25}, {500, 2.5}}
	for _, r := range rows {
		if v <= r.max {
			return r.mm
		}
	}
	return 2.5 + (v-500)*0.005
}

// checkChain verifies that the routed copper (pads, tracks, vias) of any two
// divider-chain nets keeps IPC-2221B B2 at their voltage difference, except
// the pads of one resistor (the footprint's own spacing, checked separately
// against the same rule).
func hvCheckChain(t *testing.T, res *hvResult, p, routed string, chain map[string]float64) {
	raw, err := os.ReadFile(routed)
	if err != nil {
		t.Fatal(err)
	}
	b, err := pcbauto.FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Copper struct {
			Lines []struct {
				Net                        string
				Layer                      int
				StartX, StartY, EndX, EndY float64
				LineWidth                  float64 `json:"lineWidth"`
			} `json:"lines"`
		} `json:"copper"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	type item struct {
		net, name, part string
		layer           int
		poly            []pcbauto.Point
	}
	var items []item
	for _, part := range b.Parts {
		for _, pd := range part.Pads {
			if _, ok := chain[pd.Net]; ok {
				items = append(items, item{pd.Net, part.Ref + "." + pd.Number, part.Ref, pd.Layer, pcbauto.PadPoly(pd)})
			}
		}
	}
	for i, l := range doc.Copper.Lines {
		if _, ok := chain[l.Net]; !ok {
			continue
		}
		a, c := pcbauto.Point{X: l.StartX, Y: l.StartY}, pcbauto.Point{X: l.EndX, Y: l.EndY}
		items = append(items, item{l.Net, fmt.Sprintf("track#%d", i+1), "", l.Layer, pcbauto.SegmentPoly(a, c, l.LineWidth/2)})
	}
	worst, where := math.Inf(1), ""
	bad := 0
	for i := range items {
		for j := i + 1; j < len(items); j++ {
			a, c := items[i], items[j]
			if a.net == c.net || a.layer != c.layer && a.layer != pcbauto.LayerMulti && c.layer != pcbauto.LayerMulti {
				continue
			}
			req := hvIPCB2(chain[a.net]-chain[c.net]) / 0.0254
			d := pcbauto.PolyGap(a.poly, c.poly)
			if m := d / req; m < worst {
				worst, where = m, fmt.Sprintf("%s(%s)↔%s(%s) %.0f/%.0f mil", a.name, a.net, c.name, c.net, d, req)
			}
			if d < req-0.5 {
				bad++
			}
		}
	}
	res.check(bad == 0, p+"chain ΔV spacing (IPC-2221B B2)", "0 short", fmt.Sprintf("%d short, tightest %s", bad, where))
}
