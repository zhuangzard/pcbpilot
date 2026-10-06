package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFastrouteArgs(t *testing.T) {
	o := fastrouteOpts{multiStart: 8, minTraceUm: 152, maxTime: 10 * time.Minute}
	got := strings.Join(fastrouteArgs(o, "b.dsn", "b.r1.ses", "b.r1-report.json", "b.r0.ses"), " ")
	want := "-de b.dsn -do b.r1.ses --report=b.r1-report.json --diagnose --multi-start=8 --router.min_trace_width_um=152 --max-time=600 --initial-session=b.r0.ses"
	if got != want {
		t.Fatalf("args\n got %s\nwant %s", got, want)
	}
	if got := strings.Join(fastrouteArgs(fastrouteOpts{}, "b.dsn", "b.ses", "r.json", ""), " "); got != "-de b.dsn -do b.ses --report=r.json --diagnose" {
		t.Fatalf("minimal args = %s", got)
	}
}

func TestReadFastrouteReport(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "r.json")
	// Shape of a fastroute 0.1.7 --report file (trimmed).
	if err := os.WriteFile(p, []byte(`{"fastroute":"0.1.7","stats":{"layers":4,"connections":462,"unrouted":3,"violations":16},"unrouted":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	u, v, err := readFastrouteReport(p)
	if err != nil || u != 3 || v != 16 {
		t.Fatalf("got %d %d %v", u, v, err)
	}
	if err := os.WriteFile(p, []byte(`{"stats":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readFastrouteReport(p); err == nil {
		t.Fatal("report without stats.unrouted accepted")
	}
}

func TestResolveFastroute(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "fastroute")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("FASTROUTE_BIN", "")
	saved := fastrouteLookPath
	fastrouteLookPath = exec.LookPath
	defer func() { fastrouteLookPath = saved }()
	if got, err := resolveFastroute(""); err != nil || got != bin {
		t.Fatalf("PATH lookup = %q %v", got, err)
	}
	if _, err := resolveFastroute(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing explicit binary accepted")
	}
	t.Setenv("PATH", t.TempDir())
	_, err := resolveFastroute("")
	if err == nil || !strings.Contains(err.Error(), "install-fastroute.sh") || !strings.Contains(err.Error(), "never downloads") {
		t.Fatalf("not-installed error = %v", err)
	}
}

func TestDsnFixFlagsEscapes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "esc.json")
	if err := os.WriteFile(p, []byte(`[{"net":"GND","layer":"TopLayer","widthMil":10,"path":[[4776.1,313.9],[4710.65,313.9]],"via":true}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	f := dsnFixFlags{edgeOuterMil: 20, edgeInnerMil: 30, planeNet: "GND", escapesFile: p}
	opt, err := f.options()
	if err != nil {
		t.Fatal(err)
	}
	if opt.PlaneNet != "" {
		t.Error("plane set without --gnd-plane")
	}
	if len(opt.Escapes) != 1 || !opt.Escapes[0].Via || opt.Escapes[0].Path[1][0] != 4710.65 {
		t.Fatalf("escapes = %+v", opt.Escapes)
	}
	// Short form recorded on the Gas Module v6A3 run.
	if err := os.WriteFile(p, []byte(`[[[2399.6, 1427.2], [2334.15, 1427.2]], [[1537.4, 1801.2], [1602.85, 1801.2]]]`), 0o644); err != nil {
		t.Fatal(err)
	}
	opt, err = f.options()
	if err != nil || len(opt.Escapes) != 2 || opt.Escapes[1].Net != "GND" || opt.Escapes[1].Layer != "TopLayer" || !opt.Escapes[1].Via || opt.Escapes[1].Path[1][0] != 1602.85 {
		t.Fatalf("short-form escapes = %+v %v", opt.Escapes, err)
	}
	f.gndPlane = true
	if opt, _ := f.options(); opt.PlaneNet != "GND" {
		t.Error("--gnd-plane did not set the plane net")
	}
}

func TestMainPowerRailAndPourLayers(t *testing.T) {
	pads := []pcbPadP{{Net: "GND"}, {Net: "GND"}, {Net: "GND"}, {Net: "+12V"}, {Net: "+12V"}, {Net: "+3V3"}, {Net: "SIG"}, {Net: "SIG"}, {Net: "SIG"}}
	if got := mainPowerRail(pads); got != "+12V" {
		t.Fatalf("main rail = %q, want +12V", got)
	}
	if got := mainPowerRail([]pcbPadP{{Net: "GND"}, {Net: "SIG"}}); got != "" {
		t.Fatalf("no rail = %q", got)
	}
	if g, p := defaultPourLayers(4); len(g) != 3 || g[0] != 1 || g[1] != 15 || g[2] != 2 || p != 16 {
		t.Fatalf("4-layer pours = %v %d", g, p)
	}
	if g, p := defaultPourLayers(2); len(g) != 2 || p != 0 {
		t.Fatalf("2-layer pours = %v %d", g, p)
	}
}
