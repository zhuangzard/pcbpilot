package app

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// TestPcbSyncUsageExit2: every malformed invocation exits 2 before any
// file is read or written.
func TestPcbSyncUsageExit2(t *testing.T) {
	dir := t.TempDir()
	sch := filepath.Join(dir, "a.kicad_sch")
	pcb := filepath.Join(dir, "a.kicad_pcb")
	for _, p := range []string{sch, pcb} {
		if err := os.WriteFile(p, []byte("(x)"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	base := func() pcbSyncOpts {
		o := syncOpts(sch, pcb)
		o.inPlace = true
		return o
	}
	cases := map[string]func(o *pcbSyncOpts){
		"no --sch":                  func(o *pcbSyncOpts) { o.sch = "" },
		"no --pcb":                  func(o *pcbSyncOpts) { o.pcb = "" },
		"--out and --in-place":      func(o *pcbSyncOpts) { o.out = filepath.Join(dir, "b.kicad_pcb") },
		"no target":                 func(o *pcbSyncOpts) { o.inPlace = false },
		"--out is the input":        func(o *pcbSyncOpts) { o.inPlace, o.out = false, pcb },
		"--apply-plan no revision":  func(o *pcbSyncOpts) { o.applyPlan = filepath.Join(dir, "p.json") },
		"--revision alone":          func(o *pcbSyncOpts) { o.revision = "abc" },
		"--check with --dry-run":    func(o *pcbSyncOpts) { o.check, o.dryRun = true, true },
		"--plan-out + --apply-plan": func(o *pcbSyncOpts) { o.planOut, o.applyPlan, o.revision = "x", "y", "z" },
		"bad --match":               func(o *pcbSyncOpts) { o.match = "uuid" },
		"bad --orphans":             func(o *pcbSyncOpts) { o.orphans = "delete" },
		"bad --update":              func(o *pcbSyncOpts) { o.update = []string{"nets"} },
		"bad --break-lock":          func(o *pcbSyncOpts) { o.breakLock = "all" },
		"bad --staging":             func(o *pcbSyncOpts) { o.staging = "left" },
		"bad --netlist-format":      func(o *pcbSyncOpts) { o.netlistFormat = "spice" },
		"missing board":             func(o *pcbSyncOpts) { o.pcb = filepath.Join(dir, "none.kicad_pcb") },
		"missing schematic":         func(o *pcbSyncOpts) { o.sch = filepath.Join(dir, "none.kicad_sch") },
		"not a board":               func(o *pcbSyncOpts) { o.pcb = sch },
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			o := base()
			edit(&o)
			var out, errb bytes.Buffer
			err := runPcbSync(o, &out, &errb)
			var ec exitCodeError
			if !errors.As(err, &ec) || ec.code != 2 || errb.Len() == 0 {
				t.Fatalf("want exit 2 with a message, got %v (%s)", err, errb.String())
			}
			if out.Len() != 0 {
				t.Fatalf("a usage error writes no report: %s", out.String())
			}
		})
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 2 {
		t.Fatalf("usage errors must not write files: %v", ents)
	}
}

// TestPcbSyncPadNetGate: the gate compares the schematic's pins with the
// pads of the footprints linked to its symbols — orphans, board-only
// footprints, excluded symbols and power symbols stay out — and fails on
// any difference.
func TestPcbSyncPadNetGate(t *testing.T) {
	nl := &kicad.SyncNetlist{Components: []kicad.SyncComp{
		{Ref: "R1", SheetTstamps: "/", Tstamps: []string{"00000000-0000-0000-0000-000000000001"}},
		{Ref: "R2", SheetTstamps: "/", Tstamps: []string{"00000000-0000-0000-0000-000000000002"}, ExcludeFromBoard: true},
		{Ref: "#PWR01", SheetTstamps: "/", Tstamps: []string{"00000000-0000-0000-0000-000000000003"}},
	}, Nets: []kicad.SyncNet{
		{Code: "1", Name: "VCC", Nodes: []kicad.SyncNode{{Ref: "R1", Pin: "1"}, {Ref: "#PWR01", Pin: "1"}, {Ref: "R2", Pin: "1"}}},
		{Code: "2", Name: "/SIG", Nodes: []kicad.SyncNode{{Ref: "R1", Pin: "2"}, {Ref: "R2", Pin: "2"}}},
	}}
	inv := func(r1pad2 string) *kicad.SyncInventory {
		return &kicad.SyncInventory{Footprints: []kicad.SyncFP{
			{Ref: "R1", Path: "/00000000-0000-0000-0000-000000000001", Pads: []kicad.SyncPad{
				{Number: "1", Net: "VCC", Type: "pth"}, {Number: "2", Net: r1pad2, Type: "pth"}, {Number: "", Type: "npth"}}},
			{Ref: "R2", Path: "/00000000-0000-0000-0000-000000000002", Pads: []kicad.SyncPad{{Number: "1", Net: "VCC", Type: "pth"}}},
			{Ref: "X9", Pads: []kicad.SyncPad{{Number: "1", Net: "VCC", Type: "smd"}}},
			{Ref: "G1", BoardOnly: true, Path: "/00000000-0000-0000-0000-000000000001", Pads: []kicad.SyncPad{{Number: "1", Net: "GND", Type: "smd"}}},
		}}
	}
	if g := pcbSyncPadNetGate(nl, inv("/SIG")); g.Status != gate.Pass {
		t.Fatalf("in-line board: %+v", g)
	}
	g := pcbSyncPadNetGate(nl, inv(""))
	if g.Status != gate.Fail || !strings.Contains(strings.Join(g.Items, "\n"), "R1.2") {
		t.Fatalf("a pad without its net must fail: %+v", g)
	}
	if g := pcbSyncPadNetGate(nl, inv("VCC")); g.Status != gate.Fail {
		t.Fatalf("a pad on the wrong net must fail: %+v", g)
	}
	// Two pads numbered 1, one on VCC (right), one without a net.
	dup := inv("/SIG")
	dup.Footprints[0].Pads = append(dup.Footprints[0].Pads, kicad.SyncPad{Number: "1", Net: "", Type: "pth"})
	if g := pcbSyncPadNetGate(nl, dup); g.Status != gate.Fail || !strings.Contains(strings.Join(g.Items, "\n"), "pads numbered 1") {
		t.Fatalf("a same-number pad left unconnected must fail: %+v", g)
	}
}
