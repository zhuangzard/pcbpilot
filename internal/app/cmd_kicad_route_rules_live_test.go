package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kfile"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// widenBoardMinTrack raises the board's own minimum track width in its
// .kicad_pro (board.design_settings.rules.min_track_width, mm).
func widenBoardMinTrack(t *testing.T, pcb string, mm float64) {
	t.Helper()
	p := boardSibling(pcb, ".kicad_pro")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	m["board"].(map[string]any)["design_settings"].(map[string]any)["rules"].(map[string]any)["min_track_width"] = mm
	nb, _ := json.MarshalIndent(m, "", "  ")
	if err := os.WriteFile(p, nb, 0o644); err != nil {
		t.Fatal(err)
	}
}

// rulesBlockLast: the pcbpilot-rules block exists and is the last block.
func rulesBlockLast(t *testing.T, pcb string) bool {
	t.Helper()
	b, err := os.ReadFile(boardSibling(pcb, ".kicad_dru"))
	if err != nil {
		return false
	}
	s := string(b)
	i, j := strings.Index(s, kicad.DRURulesBegin), strings.Index(s, kicad.DRURouteBegin)
	return i >= 0 && j >= 0 && i > j
}

// TestKicadRouteRulesLive drives kicad route with rules applied by kicad
// rules, on real KiCad. Skipped unless PCBPILOT_KICAD_LIVE=1.
//
//   - pic_programmer demo (no fastroute needed): route's stage 2 — the
//     shared derivation, rulesStamp, bridge netclasses, verifyRulesBlock,
//     moveRulesBlockLast, the exact calls kicad route makes — for (a) no
//     rules block, (b) after kicad rules --apply (block kept last), and the
//     full `kicad route` command for (c) a board minimum widened after the
//     apply: RULES_STALE before anything is routed.
//   - tiny-route fixture with PCBPILOT_FASTROUTE_LIVE=<fastroute>: the full
//     route for (a) and (b).
func TestKicadRouteRulesLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Fatal(err)
	}
	env := &kfile.Env{Host: "pcbpilot-live", User: "pcbpilot-live", Procs: func() ([]kfile.Proc, error) { return nil, nil }}
	write := func(t *testing.T, dir, name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	waivers := func(t *testing.T, dir string) string {
		return write(t, dir, "waivers.json", `[{"gate":"design-review","match":"--no-review","reason":"live unit test","by":"test"},
		{"gate":"board-manual","match":"--no-manual","reason":"live unit test","by":"test"}]`)
	}
	routeOpts := func(pcb, intent, dir string) kicadRouteOpts {
		o := kicadRouteOpts{pcb: pcb, intent: intent, outDir: filepath.Join(dir, "route"), widthBasis: "net", pours: "auto", gndNet: "GND", powerLayer: -1,
			silk: defaultSilkTightOpts(), noReview: true, noManual: true, waivers: waivers(t, dir), projectName: "live", projectConfig: "none", widenMax: 40, router: routerFastroute}
		o.fo.threads, o.fo.rounds, o.fo.timeout, o.fo.multiStart = 1, 1, 10*time.Minute, 1
		return o
	}

	t.Run("pic_programmer stage 2", func(t *testing.T) {
		demos := filepath.Join(kt.App, "share", "kicad", "demos")
		dir := t.TempDir()
		pcb := filepath.Join(dir, "pic_programmer.kicad_pcb")
		if _, err := copyKicadBoard(filepath.Join(demos, "pic_programmer", "pic_programmer.kicad_pcb"), pcb); err != nil {
			t.Skipf("no demo: %v", err)
		}
		intent := write(t, dir, "intent.json", `{"nets":{
 "VCC":{"role":"power","domain":"LV","currentA":0.3,"currentSource":"simulated","widthMil":{"outer":20,"inner":20,"min":12},"clearanceMil":8},
 "GND":{"role":"ground","domain":"LV","currentA":0.3,"currentSource":"simulated","widthMil":{"outer":20,"inner":20,"min":12},"clearanceMil":8},
 "VPP":{"role":"power","domain":"HV","currentA":0.05,"currentSource":"declared","widthMil":{"outer":12,"inner":12,"min":10},"clearanceMil":10}},
 "domains":[{"id":"HV","nets":["VPP"]},{"id":"LV","nets":["VCC","GND"]}],
 "pairs":[{"a":"HV","b":"LV","clearanceMm":0.6,"creepageMm":0.8}]}`)
		stage2 := func(t *testing.T) (string, error) {
			in, err := loadDesignIntent(intent)
			if err != nil {
				t.Fatal(err)
			}
			var stderr bytes.Buffer
			r := &kicadRun{o: routeOpts(pcb, intent, dir), kt: kt, stderr: &stderr, summary: map[string]any{}}
			inSnap, _, err := r.snapshot(pcb)
			if err != nil {
				t.Fatal(err)
			}
			if r.in, _, err = in.onBoard(boardNetIDs(inSnap)); err != nil {
				t.Fatal(err)
			}
			classed := filepath.Join(t.TempDir(), "classed.kicad_pcb")
			ri := deriveKicadRules(r.in, intent, nil)
			if ri, err = r.rulesStamp(ri, inSnap); err != nil {
				return classed, err
			}
			full, err := applyKicadNetclasses(kt, pcb, ri, nil, classed)
			if err != nil {
				return classed, err
			}
			if kr, _ := r.summary["kicadRules"].(map[string]any); kr != nil {
				if err := verifyRulesBlock(classed, full.ClassesNM, inSnap.CopperLayers); err != nil {
					return classed, err
				}
				if err := moveRulesBlockLast(classed); err != nil {
					return classed, err
				}
			}
			return classed, nil
		}
		// (a) No rules block: the plain netclasses step, no pcbpilot-rules block.
		classed, err := stage2(t)
		if err != nil {
			t.Fatalf("(a): %v", err)
		}
		if b, _ := os.ReadFile(boardSibling(classed, ".kicad_dru")); strings.Contains(string(b), kicad.DRURulesBegin) || !strings.Contains(string(b), kicad.DRURouteBegin) {
			t.Fatalf("(a) dru:\n%s", b)
		}
		// (b) After kicad rules --apply: the block is verified and kept last.
		ro := kicadRulesOpts{pcb: pcb, intent: intent, apply: true, replace: []string{"POWER", "Default"}, stage: "routed", env: env}
		if rep, code, se := runRules(t, ro); code != 0 {
			t.Fatalf("kicad rules --apply: exit %d %+v\n%s", code, rep.Errors, se)
		}
		if classed, err = stage2(t); err != nil {
			t.Fatalf("(b): %v", err)
		}
		if !rulesBlockLast(t, classed) {
			b, _ := os.ReadFile(boardSibling(classed, ".kicad_dru"))
			t.Fatalf("(b) the rules block is not last:\n%s", b)
		}
		// (c) The board minimum widened after the apply: the full kicad
		// route command stops with RULES_STALE before routing.
		widenBoardMinTrack(t, pcb, 1.0)
		o := routeOpts(pcb, intent, dir)
		bin, err := resolveFastroute(os.Getenv("PCBPILOT_FASTROUTE_LIVE"))
		if err != nil {
			t.Skipf("(c) needs fastroute to start kicad route: %v", err)
		}
		o.fastrouteBin = bin
		var stdout, stderr bytes.Buffer
		err = runKicadRoute(o, &stdout, &stderr)
		if err == nil || !strings.Contains(err.Error(), "RULES_STALE") || !strings.Contains(err.Error(), "rerun") {
			t.Fatalf("(c): %v\n%s", err, stderr.String())
		}
		if _, err := os.Stat(filepath.Join(o.outDir, "routed.kicad_pcb")); err == nil {
			t.Fatal("(c) routed although the rules are stale")
		}
	})

	t.Run("tiny-route full route", func(t *testing.T) {
		fr := os.Getenv("PCBPILOT_FASTROUTE_LIVE")
		if fr == "" {
			t.Skip("set PCBPILOT_FASTROUTE_LIVE=<fastroute binary> to route")
		}
		dir := t.TempDir()
		pcb := filepath.Join(dir, "tiny-route.kicad_pcb")
		if _, err := copyKicadBoard(filepath.Join("..", "kicad", "testdata", "tiny-route.kicad_pcb"), pcb); err != nil {
			t.Fatal(err)
		}
		intent := write(t, dir, "intent.json", `{"nets":{"VCC":{"role":"power","currentA":0.2,"currentSource":"simulated","widthMil":{"outer":20,"inner":20,"min":20},"clearanceMil":8},
	"GND":{"role":"ground","currentA":0.2,"currentSource":"simulated","widthMil":{"outer":16,"inner":16,"min":10},"clearanceMil":8},
	"SIG_A":{"role":"signal","widthMil":{"outer":8,"inner":8,"min":6},"clearanceMil":6}}}`)
		run := func(t *testing.T, sub string) (map[string]any, string, error) {
			_ = os.MkdirAll(filepath.Join(dir, sub), 0o755)
			o := routeOpts(pcb, intent, filepath.Join(dir, sub))
			o.fastrouteBin = fr
			var stdout, stderr bytes.Buffer
			err := runKicadRoute(o, &stdout, &stderr)
			var sum map[string]any
			b, _ := os.ReadFile(filepath.Join(o.outDir, "summary.json"))
			_ = json.Unmarshal(b, &sum)
			return sum, o.outDir, err
		}
		// (a) No rules block.
		sum, out, err := run(t, "a")
		if err != nil && strings.Contains(err.Error(), "RULES_STALE") || sum["kicadRules"] != nil || sum["routed"] == nil {
			t.Fatalf("(a): %v %v", err, sum["kicadRules"])
		}
		if b, _ := os.ReadFile(filepath.Join(out, "routed.kicad_dru")); strings.Contains(string(b), kicad.DRURulesBegin) {
			t.Fatal("(a) a rules block appeared")
		}
		// (b) After kicad rules --apply.
		ro := kicadRulesOpts{pcb: pcb, intent: intent, apply: true, stage: "routed", env: env}
		if rep, code, se := runRules(t, ro); code != 0 {
			t.Fatalf("kicad rules --apply: exit %d %+v\n%s", code, rep.Errors, se)
		}
		sum, out, err = run(t, "b")
		kr, _ := sum["kicadRules"].(map[string]any)
		if err != nil && strings.Contains(err.Error(), "RULES_STALE") || kr == nil || kr["stale"] != nil || sum["routed"] == nil {
			t.Fatalf("(b): %v %v", err, kr)
		}
		if !rulesBlockLast(t, filepath.Join(out, "routed.kicad_pcb")) {
			t.Fatal("(b) the rules block is not last on the routed board")
		}
		// (c) Board minimum widened after the apply.
		widenBoardMinTrack(t, pcb, 1.0)
		if _, _, err = run(t, "c"); err == nil || !strings.Contains(err.Error(), "RULES_STALE") {
			t.Fatalf("(c): %v", err)
		}
	})
}
