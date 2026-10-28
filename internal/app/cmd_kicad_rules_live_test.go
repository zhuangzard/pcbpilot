package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kfile"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// TestKicadRulesLiveDemos runs kicad rules against copies of KiCad's own
// demo boards with synthetic intents and simulations (the demos carry no
// intent). Skipped unless PCBPILOT_KICAD_LIVE=1; the demos are found under
// the KiCad install ($PCBPILOT_KICAD_APP/share/kicad/demos).
//
//	PCBPILOT_KICAD_LIVE=1 PCBPILOT_KICAD_APP=<KiCad root> go test ./internal/app -run TestKicadRulesLiveDemos -v
func TestKicadRulesLiveDemos(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Fatal(err)
	}
	demos := filepath.Join(kt.App, "share", "kicad", "demos")
	if _, err := os.Stat(demos); err != nil {
		t.Skipf("no KiCad demos at %s", demos)
	}
	copyDemo := func(t *testing.T, rel string) string {
		t.Helper()
		dir := t.TempDir()
		src := filepath.Join(demos, rel)
		dst := filepath.Join(dir, filepath.Base(rel))
		if _, err := copyKicadBoard(src, dst); err != nil {
			t.Fatal(err)
		}
		return dst
	}
	write := func(t *testing.T, dir, name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	env := &kfile.Env{Host: "pcbpilot-live", User: "pcbpilot-live", Procs: func() ([]kfile.Proc, error) { return nil, nil }}

	// pic_programmer: 2 layers, a user POWER class on GND / VCC, an
	// insulation pair VPP | the 5 V nets.
	t.Run("pic_programmer", func(t *testing.T) {
		pcb := copyDemo(t, filepath.Join("pic_programmer", "pic_programmer.kicad_pcb"))
		dir := filepath.Dir(pcb)
		o := kicadRulesOpts{pcb: pcb, stage: "routed", env: env,
			intent: write(t, dir, "intent.json", `{"nets":{
 "VCC":{"role":"power","domain":"LV","currentA":0.3,"currentSource":"simulated","widthMil":{"outer":20,"inner":20,"min":12},"clearanceMil":8,"viasPerTransition":1},
 "GND":{"role":"ground","domain":"LV","currentA":0.3,"currentSource":"simulated","widthMil":{"outer":20,"inner":20,"min":12},"clearanceMil":8},
 "VPP":{"role":"power","domain":"HV","currentA":0.05,"widthMil":{"outer":12,"inner":12,"min":10},"clearanceMil":10},
 "VCC_PIC":{"role":"power","domain":"LV","currentA":0.05,"currentSource":"declared","widthMil":{"outer":12,"inner":12,"min":10},"clearanceMil":8},
 "CLOCK-RB6":{"role":"signal","widthMil":{"outer":10,"inner":10,"min":10},"clearanceMil":8}},
 "domains":[{"id":"HV","nets":["VPP"]},{"id":"LV","nets":["VCC","GND","VCC_PIC"]}],
 "pairs":[{"a":"HV","b":"LV","clearanceMm":0.6,"creepageMm":0.8}],
 "copper":{"outerOz":1,"innerOz":0.5,"tempRiseC":10}}`),
			sim: write(t, dir, "sim.json", `{"schemaVersion":1,"generator":"synthetic","results":[{"scenario":"worst","converged":true,"nets":{
 "VCC":{"currentA":1.6,"role":"power"},"GND":{"currentA":1.6,"role":"ground"},"VPP":{"currentA":0.05,"role":"power"}}}]}`)}
		// The user's POWER class would be outranked: refused without --replace.
		a := o
		a.apply = true
		rep, code, stderr := runRules(t, a)
		if code != 6 || rep.Committed || rep.Errors[len(rep.Errors)-1].Code != errRulesConflict {
			t.Fatalf("conflict: exit %d %+v\n%s", code, rep.Errors, stderr)
		}
		a.replace = []string{"POWER", "Default"} // the demo sets its own Default sizes
		rep, code, stderr = runRules(t, a)
		if code != 0 || !rep.Committed || checkOf(rep, "readback").Status != gate.Pass || checkOf(rep, "staged-readback").Status != gate.Pass {
			t.Fatalf("apply: exit %d %+v\n%s", code, rep.Checks, stderr)
		}
		if v := netOf(rep, "VCC"); v.Current == nil || v.Current.Source != "sim" || v.Width.OuterMil <= 20 {
			t.Fatalf("VCC: %+v", v)
		}
		t.Logf("apply: %s", checkOf(rep, "readback").Detail)
		sum := rulesFileSHA(t, boardSibling(pcb, ".kicad_pro")) + rulesFileSHA(t, boardSibling(pcb, ".kicad_dru"))
		rep, code, _ = runRules(t, a)
		if code != 0 || len(rep.Files) != 0 || rulesFileSHA(t, boardSibling(pcb, ".kicad_pro"))+rulesFileSHA(t, boardSibling(pcb, ".kicad_dru")) != sum {
			t.Fatalf("re-apply is not a no-op: exit %d %+v", code, rep.Files)
		}
		c := o
		c.check = true
		rep, code, _ = runRules(t, c)
		if checkOf(rep, "effective-rules").Status != gate.Pass || checkOf(rep, "copper-widths").Name == "" || !checkOf(rep, "isolation-placement").Advisory {
			t.Fatalf("check: exit %d %+v", code, rep.Checks)
		}
		for _, ch := range rep.Checks {
			t.Logf("check %-20s %-12s %s", ch.Name, ch.Status, ch.Detail)
		}
	})

	// One-Air-Max: 4 layers, inner track_width rules.
	t.Run("openair-max", func(t *testing.T) {
		pcb := copyDemo(t, filepath.Join("openair-max", "One-Air-Max.kicad_pcb"))
		dir := filepath.Dir(pcb)
		o := kicadRulesOpts{pcb: pcb, stage: "routed", env: env,
			intent: write(t, dir, "intent.json", `{"nets":{
 "+5VUSB":{"role":"power","currentA":1.0,"currentSource":"simulated","widthMil":{"outer":20,"inner":20,"min":12},"clearanceMil":6},
 "+3V3":{"role":"power","currentA":0.5,"currentSource":"simulated","widthMil":{"outer":12,"inner":12,"min":8},"clearanceMil":6},
 "GND":{"role":"ground","currentA":1.0,"currentSource":"simulated","widthMil":{"outer":12,"inner":12,"min":8},"clearanceMil":6},
 "EN":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6},"clearanceMil":6}}}`),
			sim: write(t, dir, "sim.json", `{"schemaVersion":1,"generator":"synthetic","results":[{"scenario":"worst","converged":true,"nets":{
 "+5VUSB":{"currentA":1.0,"role":"power"},"+3V3":{"currentA":0.8,"role":"power"},"GND":{"currentA":1.0,"role":"ground"}}}]}`)}
		a := o
		a.apply = true
		a.replace = []string{"Default"} // the demo sets its own Default sizes
		rep, code, stderr := runRules(t, a)
		if code != 0 || !rep.Committed || checkOf(rep, "readback").Status != gate.Pass {
			t.Fatalf("apply: exit %d %+v\n%s", code, rep.Checks, stderr)
		}
		inner := 0
		for _, r := range rep.DRURules {
			if r.Layer == "inner" {
				inner++
			}
		}
		if inner == 0 || netOf(rep, "GND").InnerFloor <= 0 {
			t.Fatalf("no inner rules on a 4-layer board: %+v", rep.DRURules)
		}
		dru, _ := os.ReadFile(boardSibling(pcb, ".kicad_dru"))
		if !strings.Contains(string(dru), "(layer inner)") {
			t.Fatalf("dru:\n%s", dru)
		}
		c := o
		c.check = true
		rep, code, _ = runRules(t, c)
		if checkOf(rep, "effective-rules").Status != gate.Pass {
			t.Fatalf("check: exit %d %+v", code, rep.Checks)
		}
		for _, ch := range rep.Checks {
			t.Logf("check %-20s %-12s %s", ch.Name, ch.Status, ch.Detail)
		}
	})
}
