package app

// Live generator for the power-stub backtest (skipped unless PP_LIVE_BOARD is
// set): runs the pre-route part of `kicad route` (rules untouched, optional
// explicit plane, netclasses, DSN export + prepare with the joint power
// stubs) on a real board and writes route.dsn, classed.kicad_pcb and
// stubs.kicad_pcb (classed + the fixed stubs and vias) and summary.json to
// PP_LIVE_OUT — for a host where the pcbpilot binary cannot run the router
// (the fastroute / SES / DRC steps are then run directly).
//
//	PP_LIVE_BOARD=x.kicad_pcb PP_LIVE_INTENT=intent.json PP_LIVE_OUT=dir \
//	  [PP_LIVE_PLANE=+3V3:In2] [PP_LIVE_NOSTUBS=1] [PP_LIVE_VIA=touch] \
//	  go test ./internal/app -run TestPowerStubLive -v

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func TestPowerStubLive(t *testing.T) {
	boardIn := os.Getenv("PP_LIVE_BOARD")
	if boardIn == "" {
		t.Skip("PP_LIVE_BOARD not set")
	}
	out := os.Getenv("PP_LIVE_OUT")
	in, err := loadDesignIntent(os.Getenv("PP_LIVE_INTENT"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(out, "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	o := kicadRouteOpts{pcb: boardIn, intent: os.Getenv("PP_LIVE_INTENT"), outDir: out, noPowerStubs: os.Getenv("PP_LIVE_NOSTUBS") != "",
		stubViaAtPad: os.Getenv("PP_LIVE_VIA")}
	if p := os.Getenv("PP_LIVE_PLANE"); p != "" {
		o.powerPlanes = strings.Split(p, ",")
	}
	r := &kicadRun{o: o, in: in, stderr: os.Stderr, work: filepath.Join(out, "work"), summary: map[string]any{}}
	if r.kt, err = kicad.Locate(); err != nil {
		t.Skip(err)
	}
	input := filepath.Join(r.work, "00-input.kicad_pcb")
	if _, err := copyKicadBoard(boardIn, input); err != nil {
		t.Fatal(err)
	}
	board := input
	if os.Getenv("PP_LIVE_RIPUP") != "" {
		out := r.next("ripup")
		if _, err := r.kt.RipUp(board, out); err != nil {
			t.Fatal(err)
		}
		board = out
	}
	snap, _, err := r.snapshot(board)
	if err != nil {
		t.Fatal(err)
	}
	r.inRules = snap.Rules
	planes, err := r.resolvePlanes(snap.CopperLayers)
	if err != nil {
		t.Fatal(err)
	}
	r.o.planes = planes
	if len(planes) > 0 {
		if board, err = r.applyPlanes(board, planes); err != nil {
			t.Fatal(err)
		}
	}
	if snap, r.inRaw, err = r.snapshot(board); err != nil {
		t.Fatal(err)
	}
	reqs := kicadRequirements(r.in)
	classed := r.next("classed")
	ncRes, err := r.kt.NetclassesIso(board, reqs, kicadIsolation(r.in), kicadEdges(o.intent, r.in), classed)
	if err != nil {
		t.Fatal(err)
	}
	r.classes = ncRes.Classes
	if _, _, err := r.prepareRoute(classed, reqs, snap); err != nil {
		t.Fatal(err)
	}
	stubsBoard := filepath.Join(out, "stubs.kicad_pcb")
	nb, err := r.addEscapes(classed, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".kicad_pcb", ".kicad_pro", ".kicad_dru"} {
		for src, dst := range map[string]string{strings.TrimSuffix(classed, ".kicad_pcb") + ext: filepath.Join(out, "classed"+ext), strings.TrimSuffix(nb, ".kicad_pcb") + ext: strings.TrimSuffix(stubsBoard, ".kicad_pcb") + ext} {
			if b, err := os.ReadFile(src); err == nil {
				_ = os.WriteFile(dst, b, 0o644)
			}
		}
	}
	_ = writeJSONFile(filepath.Join(out, "summary.json"), r.summary)
}
