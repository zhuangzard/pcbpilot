package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kfile"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// TestKicadRulesLiveUserMax: a user track_width rule with a maximum on a
// planned net of a KiCad demo board (One-Air-Max), the plan inside its
// range. On real KiCad: apply carries the max into the owned rule, the
// staged and published readback and the DRC probes pass, --check passes,
// and kicad route's stage 2 (verifyRulesBlock with the recomputed maxima)
// accepts the block. Skipped unless PCBPILOT_KICAD_LIVE=1.
func TestKicadRulesLiveUserMax(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pcb := filepath.Join(dir, "One-Air-Max.kicad_pcb")
	if _, err := copyKicadBoard(filepath.Join(kt.App, "share", "kicad", "demos", "openair-max", "One-Air-Max.kicad_pcb"), pcb); err != nil {
		t.Skipf("no demo: %v", err)
	}
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	intent := write("intent.json", `{"nets":{
 "+5VUSB":{"role":"power","currentA":1.0,"currentSource":"simulated","widthMil":{"outer":20,"inner":20,"min":12},"clearanceMil":6},
 "GND":{"role":"ground","currentA":1.0,"currentSource":"simulated","widthMil":{"outer":12,"inner":12,"min":8},"clearanceMil":6},
 "EN":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6},"clearanceMil":6}}}`)
	// /EN is planned at the Default 0.25 mm width: inside [0.15, 0.25] mm.
	write("One-Air-Max.kicad_dru", "(version 1)\n(rule \"user_sig_width\"\n  (condition \"A.NetName == '/EN'\")\n  (constraint track_width (min 0.15mm) (max 0.25mm)))\n")
	env := &kfile.Env{Host: "pcbpilot-live", User: "pcbpilot-live", Procs: func() ([]kfile.Proc, error) { return nil, nil }}
	o := kicadRulesOpts{pcb: pcb, intent: intent, stage: "routed", env: env, replace: []string{"Default"}}

	a := o
	a.apply = true
	rep, code, stderr := runRules(t, a)
	if code != 0 || !rep.Committed || checkOf(rep, "staged-readback").Status != gate.Pass || checkOf(rep, "readback").Status != gate.Pass {
		t.Fatalf("apply: exit %d %+v %+v\n%s", code, rep.Errors, rep.Checks, stderr)
	}
	en := netOf(rep, "/EN")
	carried := 0
	for _, r := range rep.DRURules {
		if r.Class == en.Class {
			if r.MaxNm != 250000 || r.MaxFrom != "user_sig_width" {
				t.Fatalf("owned rule %s: %+v", r.Name, r)
			}
			carried++
		}
	}
	if carried == 0 {
		t.Fatalf("no owned rule for %s: %+v", en.Class, rep.DRURules)
	}
	dru, _ := os.ReadFile(boardSibling(pcb, ".kicad_dru"))
	_, blk, _, _ := kicad.SplitDRU(strings.ReplaceAll(string(dru), "\r\n", "\n"))
	if strings.Count(blk, "(max 0.25mm)") != carried || !strings.Contains(string(dru), "user_sig_width") {
		t.Fatalf("dru:\n%s", dru)
	}
	t.Logf("apply: %s; %s", checkOf(rep, "staged-readback").Detail, checkOf(rep, "readback").Detail)

	c := o
	c.check, c.stage = true, "placed"
	rep, code, _ = runRules(t, c)
	if e := checkOf(rep, "effective-rules"); e.Status != gate.Pass {
		t.Fatalf("check: exit %d %+v", code, e)
	}

	// kicad route stage 2 with the block: the same calls route makes.
	in, err := loadDesignIntent(intent)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	r := &kicadRun{o: kicadRouteOpts{pcb: pcb, intent: intent}, kt: kt, stderr: &buf, summary: map[string]any{}}
	inSnap, raw, err := r.snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	r.inRaw = raw
	if r.in, _, err = in.onBoard(boardNetIDs(inSnap)); err != nil {
		t.Fatal(err)
	}
	ri, err := r.rulesStamp(deriveKicadRules(r.in, intent, nil), inSnap)
	if err != nil {
		t.Fatalf("rulesStamp: %v", err)
	}
	classed := filepath.Join(t.TempDir(), "classed.kicad_pcb")
	full, err := applyKicadNetclasses(kt, pcb, ri, nil, classed)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRulesBlock(classed, full.ClassesNM, inSnap.CopperLayers, r.routeTrackMaxes(classed, full, inSnap.CopperLayers)); err != nil {
		t.Fatalf("route stage 2: %v", err)
	}
	if err := moveRulesBlockLast(classed); err != nil || !rulesBlockLast(t, classed) {
		t.Fatalf("block not last: %v", err)
	}
}
