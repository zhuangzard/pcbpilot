package kicad

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateSyncGolden = flag.Bool("update-sync-golden", false, "rewrite testdata/pcbsync/golden/*.json")

// The synthetic design every BuildSyncPlan case starts from (root sheet):
//
//	R1 10k   R_0603  1:VCC 2:SIG     U1 LM358 (2 units, SOIC-8)
//	R2 1k    R_0603  1:SIG 2:GND        1:OUT 2:SIG 3:REF 4:GND 8:VCC
//	C1 100n  C_0603  1:VCC 2:GND     J1 CONN_3  1:VCC 2:OUT 3:GND
//
// and the board that matches it exactly: every footprint linked by path,
// tracks on SIG and VCC, a GND zone, plus a board-only logo.
const (
	uR1  = "00000000-0000-0000-0000-0000000000a1"
	uR2  = "00000000-0000-0000-0000-0000000000a2"
	uC1  = "00000000-0000-0000-0000-0000000000c1"
	uU1a = "00000000-0000-0000-0000-0000000000b1"
	uU1b = "00000000-0000-0000-0000-0000000000b2"
	uJ1  = "00000000-0000-0000-0000-0000000000d1"
	uR3  = "00000000-0000-0000-0000-0000000000a3"
)

func syncComp(ref, value, fp, uuid string, units ...string) SyncComp {
	ts := append([]string{uuid}, units...)
	return SyncComp{Ref: ref, Value: value, Footprint: fp, Lib: "lib", Part: ref[:1], SheetNames: "/", SheetTstamps: "/",
		Tstamps: ts, Fields: map[string]string{"Footprint": fp, "Datasheet": "", "Description": ""},
		Properties: map[string]string{"Sheetname": "root", "Sheetfile": "t.kicad_sch"}}
}

func baseNetlist() *SyncNetlist {
	nl := &SyncNetlist{Sheets: []SyncSheet{{"1", "/", "/"}}, Components: []SyncComp{
		syncComp("C1", "100n", "Capacitor_SMD:C_0603", uC1),
		syncComp("J1", "CONN_3", "Connector:CONN_3", uJ1),
		syncComp("R1", "10k", "Resistor_SMD:R_0603", uR1),
		syncComp("R2", "1k", "Resistor_SMD:R_0603", uR2),
		syncComp("U1", "LM358", "Package_SO:SOIC-8", uU1a, uU1b),
	}}
	net := func(code, name string, nodes ...string) SyncNet {
		n := SyncNet{Code: code, Name: name}
		for _, d := range nodes {
			r, p, _ := strings.Cut(d, ".")
			n.Nodes = append(n.Nodes, SyncNode{Ref: r, Pin: p})
		}
		return n
	}
	nl.Nets = []SyncNet{
		net("1", "VCC", "R1.1", "C1.1", "U1.8", "J1.1"),
		net("2", "GND", "R2.2", "C1.2", "U1.4", "J1.3"),
		net("3", "/SIG", "R1.2", "R2.1", "U1.2"),
		net("4", "/OUT", "U1.1", "J1.2"),
		net("5", "unconnected-(U1-REF-Pad3)", "U1.3"),
	}
	return nl
}

// baseInventory is the board as sync-read reports it for baseNetlist.
func baseInventory(nl *SyncNetlist) *SyncInventory {
	pinNet := map[[2]string]string{}
	for _, n := range nl.Nets {
		for _, d := range n.Nodes {
			pinNet[[2]string{d.Ref, d.Pin}] = n.Name
		}
	}
	inv := &SyncInventory{KiCadVersion: "10.0.6", Outline: &SyncBox{0, 0, 50, 40}}
	pads := map[string][]string{"C1": {"1", "2"}, "J1": {"1", "2", "3"}, "R1": {"1", "2"}, "R2": {"1", "2"},
		"U1": {"1", "2", "3", "4", "5", "6", "7", "8"}}
	x := 5.0
	for _, c := range nl.Components {
		f := SyncFP{ID: "fp-" + c.Ref, UUID: "fp-" + c.Ref, Ref: c.Ref, Path: c.Keys()[0], Sheetname: "/", Sheetfile: "t.kicad_sch",
			FPID: c.Footprint, Value: c.Value, Fields: map[string]string{"Datasheet": "", "Description": ""},
			X: x, Y: 10, Side: "F", Box: &SyncBox{x - 2, 8, x + 2, 12}}
		for _, p := range pads[c.Ref] {
			f.Pads = append(f.Pads, SyncPad{Number: p, Net: pinNet[[2]string{c.Ref, p}], Type: "smd"})
		}
		inv.Footprints = append(inv.Footprints, f)
		x += 8
	}
	inv.Footprints = append(inv.Footprints, SyncFP{ID: "fp-logo", Ref: "G***", FPID: "Logo:pcbpilot", BoardOnly: true,
		Fields: map[string]string{}, Side: "F", Pads: []SyncPad{{Number: "1", Net: "GND", Type: "smd"}}})
	inv.Nets = []SyncBoardNet{
		{Name: "/OUT", Pads: 2, Tracks: 2},
		{Name: "/SIG", Pads: 3, Tracks: 4, Vias: 1},
		{Name: "GND", Pads: 5, Zones: 1},
		{Name: "VCC", Pads: 4, Tracks: 3},
		{Name: "unconnected-(U1-REF-Pad3)", Pads: 1},
	}
	return inv
}

type fakeLibs map[string]FPRef

func (f fakeLibs) Resolve(id string) (FPRef, error) {
	if r, ok := f[id]; ok {
		return r, nil
	}
	nick, name, _ := strings.Cut(id, ":")
	return FPRef{}, &FPLibError{Code: ErrFPLibUnresolved, Lib: nick, Name: name, Var: "KICAD10_FOOTPRINT_DIR",
		Detail: "library \"" + nick + "\" is in neither the project nor the global fp-lib-table"}
}

func libRef(id string) FPRef {
	nick, name, _ := strings.Cut(id, ":")
	return FPRef{Lib: nick, Name: name, LibDir: "/libs/" + nick + ".pretty", Path: "/libs/" + nick + ".pretty/" + name + ".kicad_mod", Scope: "global"}
}

func smdPads(nums ...string) []SyncPad {
	var out []SyncPad
	for _, n := range nums {
		out = append(out, SyncPad{Number: n, Type: "smd"})
	}
	return out
}

// syncCase builds the input of one case from the base design.
func syncCase(edit func(nl *SyncNetlist, inv *SyncInventory, o *SyncOptions)) SyncInput {
	nl := baseNetlist()
	inv := baseInventory(nl)
	o := SyncOptions{RenameNets: true}
	if edit != nil {
		edit(nl, inv, &o)
	}
	libs := fakeLibs{}
	infos := map[string]SyncFPInfo{
		"Resistor_SMD:R_0603":  {Pads: smdPads("1", "2"), Box: &SyncBox{-1.5, -0.8, 1.5, 0.8}, BoxSource: "courtyard"},
		"Resistor_SMD:R_0805":  {Pads: smdPads("1", "2"), Box: &SyncBox{-1.7, -1, 1.7, 1}, BoxSource: "courtyard"},
		"Capacitor_SMD:C_0603": {Pads: smdPads("1", "2"), Box: &SyncBox{-1.5, -0.8, 1.5, 0.8}, BoxSource: "courtyard"},
		"Package_SO:SOIC-8":    {Pads: smdPads("1", "2", "3", "4", "5", "6", "7", "8"), Box: &SyncBox{-3.7, -2.7, 3.7, 2.7}, BoxSource: "courtyard"},
		"Connector:CONN_3":     {Pads: smdPads("1", "2", "3"), Box: &SyncBox{-1, -3.5, 1, 3.5}, BoxSource: "pads"},
		"Connector:CONN_2":     {Pads: smdPads("1", "2"), Box: &SyncBox{-1, -2.5, 1, 2.5}, BoxSource: "pads"},
		"Connector:CONN_3B":    {Pads: smdPads("1", "2", "3"), Box: &SyncBox{-1.2, -3.5, 1.2, 3.5}, BoxSource: "pads"},
	}
	for id := range infos {
		libs[id] = libRef(id)
	}
	return SyncInput{Netlist: nl, Inventory: inv, Libs: libs, Opts: o,
		Info: func(refs []FPRef) (map[string]SyncFPInfo, []SyncError) {
			out := map[string]SyncFPInfo{}
			var bad []SyncError
			for _, r := range refs {
				if i, ok := infos[r.Lib+":"+r.Name]; ok {
					out[r.Lib+":"+r.Name] = i
				} else {
					bad = append(bad, SyncError{Code: ErrFPNotFound, Detail: r.Lib + ":" + r.Name + ": did not load"})
				}
			}
			return out, bad
		}}
}

func compByRef(nl *SyncNetlist, ref string) *SyncComp {
	for i := range nl.Components {
		if nl.Components[i].Ref == ref {
			return &nl.Components[i]
		}
	}
	panic("no " + ref)
}

func indexByRef(inv *SyncInventory, ref string) int {
	for i := range inv.Footprints {
		if inv.Footprints[i].Ref == ref {
			return i
		}
	}
	panic("no " + ref)
}

func fpByRef(inv *SyncInventory, ref string) *SyncFP {
	for i := range inv.Footprints {
		if inv.Footprints[i].Ref == ref {
			return &inv.Footprints[i]
		}
	}
	panic("no " + ref)
}

func netByName(nl *SyncNetlist, name string) *SyncNet {
	for i := range nl.Nets {
		if nl.Nets[i].Name == name {
			return &nl.Nets[i]
		}
	}
	panic("no net " + name)
}

func opsOf(p *SyncPlan, kind string) []SyncOp {
	var out []SyncOp
	for _, o := range p.Ops {
		if o.Op == kind {
			out = append(out, o)
		}
	}
	return out
}

func checkGolden(t *testing.T, name string, v any) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "pcbsync", "golden", name+".json")
	if *updateSyncGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -run TestBuildSyncPlan -update-sync-golden)", err)
	}
	if strings.ReplaceAll(string(want), "\r\n", "\n") != string(got) {
		t.Errorf("%s differs from the golden file:\n%s", name, got)
	}
}

// TestBuildSyncPlan runs every op and refusal of the design over the
// synthetic design; each case checks its key facts and the full plan
// against testdata/pcbsync/golden/<case>.json.
func TestBuildSyncPlan(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(nl *SyncNetlist, inv *SyncInventory, o *SyncOptions)
		check func(t *testing.T, p *SyncPlan)
	}{
		{"noop", nil, func(t *testing.T, p *SyncPlan) {
			if len(p.Ops) != 0 || p.Drift() || p.Match.ByPath != 5 {
				t.Fatalf("an in-sync board must plan nothing: %+v", p)
			}
		}},
		{"set-value", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) { compByRef(nl, "R2").Value = "2k2" },
			func(t *testing.T, p *SyncPlan) {
				ops := opsOf(p, "setValue")
				if len(ops) != 1 || *ops[0].Value != "2k2" || ops[0].From != "1k" || ops[0].FP != "fp-R2" || p.Counts.Update != 1 || len(p.Ops) != 1 {
					t.Fatalf("%+v", p.Ops)
				}
			}},
		{"add-footprint", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			nl.Components = append(nl.Components, syncComp("R3", "4k7", "Resistor_SMD:R_0805", uR3))
			nl.Components[len(nl.Components)-1].Fields["MPN"] = "RC0805"
			s := netByName(nl, "/SIG")
			s.Nodes = append(s.Nodes, SyncNode{Ref: "R3", Pin: "1"})
			nl.Nets = append(nl.Nets, SyncNet{Code: "6", Name: "/FB", Nodes: []SyncNode{{Ref: "R3", Pin: "2"}}})
		}, func(t *testing.T, p *SyncPlan) {
			ops := opsOf(p, "addFootprint")
			if len(ops) != 1 || p.Counts.Add != 1 {
				t.Fatalf("%+v", p.Ops)
			}
			a := ops[0]
			if a.Ref != "R3" || a.Key != "/"+uR3 || a.LibDir != "/libs/Resistor_SMD.pretty" || a.PadNets["1"] != "/SIG" || a.PadNets["2"] != "/FB" ||
				a.Fields["MPN"] != "RC0805" || a.Fields["Footprint"] != "" || *a.Sheetname != "/" || *a.Sheetfile != "t.kicad_sch" {
				t.Fatalf("%+v", a)
			}
			// 10 mm right of the outline (max X 50), top edge at the outline's min Y.
			if *a.X != 61.7 || *a.Y != 1 || len(p.Staged) != 1 || p.Staged[0].X != 61.7 {
				t.Fatalf("staged at %v,%v (%+v)", *a.X, *a.Y, p.Staged)
			}
		}},
		{"orphan-flag", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) { dropComp(nl, "R2") },
			func(t *testing.T, p *SyncPlan) {
				ops := opsOf(p, "orphan")
				if len(ops) != 1 || ops[0].Action != "flag" || !ops[0].Info || p.Counts.OrphanFlag != 1 || len(p.Mutating()) != 0 || !p.Drift() {
					t.Fatalf("%+v", p.Ops)
				}
				if len(p.Match.UnmatchedFootprints) != 1 || p.Match.UnmatchedFootprints[0] != "R2" {
					t.Fatalf("%+v", p.Match)
				}
			}},
		{"orphan-remove", func(nl *SyncNetlist, _ *SyncInventory, o *SyncOptions) { dropComp(nl, "R2"); o.Orphans = "remove" },
			func(t *testing.T, p *SyncPlan) {
				ops := opsOf(p, "orphan")
				if len(ops) != 1 || ops[0].Action != "remove" || ops[0].Info || p.Counts.OrphanRemove != 1 {
					t.Fatalf("%+v", p.Ops)
				}
				// R2.1 was on /SIG (tracks) and R2.2 on GND (zone).
				if len(p.OrphanCopper) != 2 {
					t.Fatalf("orphan copper %+v", p.OrphanCopper)
				}
			}},
		{"orphan-remove-locked", func(nl *SyncNetlist, inv *SyncInventory, o *SyncOptions) {
			dropComp(nl, "R2")
			fpByRef(inv, "R2").Locked = true
			o.Orphans = "remove"
		}, func(t *testing.T, p *SyncPlan) {
			ops := opsOf(p, "orphan")
			if len(ops) != 1 || ops[0].Action != "flag" || !strings.Contains(ops[0].Reason, "locked") || p.Counts.OrphanRemove != 0 {
				t.Fatalf("a locked footprint must never be removed: %+v", p.Ops)
			}
		}},
		{"exclude-from-board", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) { compByRef(nl, "C1").ExcludeFromBoard = true },
			func(t *testing.T, p *SyncPlan) {
				ops := opsOf(p, "orphan")
				if len(ops) != 1 || ops[0].Ref != "C1" || !strings.Contains(ops[0].Reason, "excluded from the board") {
					t.Fatalf("%+v", p.Ops)
				}
			}},
		{"board-only-never-orphan", func(_ *SyncNetlist, inv *SyncInventory, o *SyncOptions) { o.Orphans = "remove" },
			func(t *testing.T, p *SyncPlan) {
				if len(p.Ops) != 0 {
					t.Fatalf("the board-only logo must be left alone: %+v", p.Ops)
				}
			}},
		{"swap-footprint", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			c := compByRef(nl, "J1")
			c.Footprint, c.Fields["Footprint"] = "Connector:CONN_2", "Connector:CONN_2"
		}, func(t *testing.T, p *SyncPlan) {
			ops := opsOf(p, "swapFootprint")
			if len(ops) != 1 || ops[0].From != "Connector:CONN_3" || ops[0].To != "Connector:CONN_2" || ops[0].PadNets["1"] != "VCC" || ops[0].PadNets["2"] != "/OUT" {
				t.Fatalf("%+v", p.Ops)
			}
			if len(opsOf(p, "setPadNet")) != 0 {
				t.Fatalf("a swapped footprint takes its nets from the swap: %+v", p.Ops)
			}
			// J1.3 (GND, zone) has no pad on CONN_2, and the schematic pin 3 has no pad.
			if len(p.OrphanCopper) != 1 || p.OrphanCopper[0].Pad != "3" || len(p.Errors) != 1 || p.Errors[0].Code != ErrPinPadMismatch {
				t.Fatalf("orphan copper %+v errors %+v", p.OrphanCopper, p.Errors)
			}
		}},
		{"rename-net", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) { netByName(nl, "/SIG").Name = "/SENSE" },
			func(t *testing.T, p *SyncPlan) {
				ops := opsOf(p, "renameNet")
				if len(ops) != 1 || ops[0].From != "/SIG" || ops[0].To != "/SENSE" || len(opsOf(p, "setPadNet")) != 0 || len(p.DanglingCopper) != 0 {
					t.Fatalf("%+v dangling %+v", p.Ops, p.DanglingCopper)
				}
			}},
		{"rename-ambiguous-split", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			// /SIG splits: R1.2+R2.1 → /A, U1.2 → /B. Not a rename.
			s := netByName(nl, "/SIG")
			s.Name = "/A"
			s.Nodes = s.Nodes[:2]
			nl.Nets = append(nl.Nets, SyncNet{Code: "7", Name: "/B", Nodes: []SyncNode{{Ref: "U1", Pin: "2"}}})
		}, func(t *testing.T, p *SyncPlan) {
			if len(opsOf(p, "renameNet")) != 0 || len(opsOf(p, "setPadNet")) != 3 {
				t.Fatalf("an ambiguous rename must not rename: %+v", p.Ops)
			}
			if len(opsOf(p, "removeNet")) != 1 || len(p.DanglingCopper) != 4 {
				t.Fatalf("dangling %+v ops %+v", p.DanglingCopper, p.Ops)
			}
		}},
		{"rename-ambiguous-merge", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			// /SIG and /OUT merge into /NODE: two board nets claim it.
			s, o := netByName(nl, "/SIG"), netByName(nl, "/OUT")
			s.Name = "/NODE"
			s.Nodes = append(s.Nodes, o.Nodes...)
			dropNet(nl, "/OUT")
		}, func(t *testing.T, p *SyncPlan) {
			if len(opsOf(p, "renameNet")) != 0 || len(opsOf(p, "setPadNet")) != 5 {
				t.Fatalf("a merge must not rename: %+v", p.Ops)
			}
		}},
		{"rename-taken-name", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			// /SIG becomes /OUT's name while /OUT's pads move to /OUT2: the
			// target name is still on the board, so no rename.
			netByName(nl, "/OUT").Name = "/OUT2"
			netByName(nl, "/SIG").Name = "/OUT"
		}, func(t *testing.T, p *SyncPlan) {
			for _, o := range opsOf(p, "renameNet") {
				if o.To == "/OUT" {
					t.Fatalf("renamed onto a name the board still uses: %+v", o)
				}
			}
		}},
		{"rename-disabled", func(nl *SyncNetlist, _ *SyncInventory, o *SyncOptions) {
			netByName(nl, "/SIG").Name = "/SENSE"
			o.RenameNets = false
		}, func(t *testing.T, p *SyncPlan) {
			if len(opsOf(p, "renameNet")) != 0 || len(opsOf(p, "setPadNet")) != 3 {
				t.Fatalf("%+v", p.Ops)
			}
		}},
		{"set-pad-net", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			// U1.5 joins /OUT.
			o := netByName(nl, "/OUT")
			o.Nodes = append(o.Nodes, SyncNode{Ref: "U1", Pin: "5"})
		}, func(t *testing.T, p *SyncPlan) {
			ops := opsOf(p, "setPadNet")
			if len(ops) != 1 || ops[0].Ref != "U1" || ops[0].Pad != "5" || *ops[0].Net != "/OUT" || ops[0].From != "" {
				t.Fatalf("%+v", p.Ops)
			}
		}},
		{"set-ref", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			c := compByRef(nl, "R2")
			c.Ref = "R7"
			renameNodes(nl, "R2", "R7")
		}, func(t *testing.T, p *SyncPlan) {
			ops := opsOf(p, "setRef")
			if len(ops) != 1 || ops[0].Ref != "R7" || ops[0].From != "R2" || len(p.Ops) != 1 {
				t.Fatalf("%+v", p.Ops)
			}
		}},
		{"ref-fallback-relink", func(_ *SyncNetlist, inv *SyncInventory, _ *SyncOptions) {
			fpByRef(inv, "R1").Path = "/00000000-0000-0000-0000-0000000000ff"
		}, func(t *testing.T, p *SyncPlan) {
			ops := opsOf(p, "setPath")
			if len(ops) != 1 || ops[0].Path != "/"+uR1 || ops[0].MatchedBy != "ref" || p.Match.ByRef != 1 || len(p.Ops) != 1 {
				t.Fatalf("%+v", p.Ops)
			}
		}},
		{"match-path-only", func(_ *SyncNetlist, inv *SyncInventory, o *SyncOptions) {
			fpByRef(inv, "R1").Path = "/00000000-0000-0000-0000-0000000000ff"
			o.Match = SyncMatchPath
		}, func(t *testing.T, p *SyncPlan) {
			if p.Counts.Add != 1 || p.Counts.OrphanFlag != 1 || p.Match.ByRef != 0 {
				t.Fatalf("--match path must not fall back to references: %+v", p.Ops)
			}
		}},
		{"match-ref-only", func(_ *SyncNetlist, inv *SyncInventory, o *SyncOptions) {
			fpByRef(inv, "R1").Path = ""
			o.Match = SyncMatchRef
		}, func(t *testing.T, p *SyncPlan) {
			if p.Match.ByRef != 5 || len(opsOf(p, "setPath")) != 1 {
				t.Fatalf("%+v %+v", p.Match, p.Ops)
			}
		}},
		{"ref-fallback-not-unique", func(_ *SyncNetlist, inv *SyncInventory, _ *SyncOptions) {
			// R1's footprint lost its path and a second, unlinked "R1" is on
			// the board: the reference is not unique, so no relink.
			f := fpByRef(inv, "R1")
			f.Path = ""
			dup := *f
			dup.ID, dup.UUID = "fp-R1b", "fp-R1b"
			inv.Footprints = append(inv.Footprints, dup)
		}, func(t *testing.T, p *SyncPlan) {
			if p.Match.ByRef != 0 || p.Counts.Add != 1 || p.Counts.OrphanFlag != 2 {
				t.Fatalf("%+v", p.Ops)
			}
		}},
		{"ref-fallback-globally-ambiguous", func(_ *SyncNetlist, inv *SyncInventory, _ *SyncOptions) {
			// R1 lost its link; R2's footprint (still linked to R2 by path)
			// is labelled "R1" too. "R1" is on two footprints, so it is no
			// key even though only one of them is left after path matching.
			fpByRef(inv, "R1").Path = "/00000000-0000-0000-0000-0000000000ff"
			inv.Footprints[indexByRef(inv, "R2")].Ref = "R1"
		}, func(t *testing.T, p *SyncPlan) {
			if p.Match.ByRef != 0 || p.Counts.Add != 1 || p.Counts.OrphanFlag != 1 || len(opsOf(p, "setRef")) != 1 {
				t.Fatalf("a globally ambiguous reference must not be matched: %+v %+v", p.Match, p.Ops)
			}
		}},
		{"swap-and-renet", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			c := compByRef(nl, "J1")
			c.Footprint, c.Fields["Footprint"] = "Connector:CONN_3B", "Connector:CONN_3B"
			v := netByName(nl, "VCC")
			v.Nodes = v.Nodes[:3]                                                                                // J1.1 leaves VCC (3 tracks) …
			nl.Nets = append(nl.Nets, SyncNet{Code: "8", Name: "/IN", Nodes: []SyncNode{{Ref: "J1", Pin: "1"}}}) // … for /IN
		}, func(t *testing.T, p *SyncPlan) {
			sw := opsOf(p, "swapFootprint")
			if len(sw) != 1 || sw[0].PadNets["1"] != "/IN" {
				t.Fatalf("%+v", p.Ops)
			}
			if len(p.DanglingCopper) != 1 || p.DanglingCopper[0].Ref != "J1" || p.DanglingCopper[0].Pad != "1" ||
				p.DanglingCopper[0].Net != "VCC" || p.DanglingCopper[0].To != "/IN" || p.DanglingCopper[0].Tracks != 3 {
				t.Fatalf("a swapped pad leaving a net with copper must be reported: %+v", p.DanglingCopper)
			}
		}},
		{"add-fields-ignore", func(nl *SyncNetlist, _ *SyncInventory, o *SyncOptions) {
			nl.Components = append(nl.Components, syncComp("R3", "4k7", "Resistor_SMD:R_0805", uR3))
			nl.Components[len(nl.Components)-1].Fields["MPN"] = "RC0805"
			o.FieldsIgnore = map[string]bool{"MPN": true}
		}, func(t *testing.T, p *SyncPlan) {
			a := opsOf(p, "addFootprint")
			if len(a) != 1 || a[0].Fields["MPN"] != "" || len(a[0].Fields) != 2 {
				t.Fatalf("--fields-ignore must hold for new footprints: %+v", a)
			}
		}},
		{"fields", func(nl *SyncNetlist, inv *SyncInventory, o *SyncOptions) {
			compByRef(nl, "R1").Fields["MPN"] = "RC0603FR-0710KL"
			compByRef(nl, "R1").Fields["Datasheet"] = "https://example.invalid/r.pdf"
			fpByRef(inv, "R2").Fields["Old"] = "x"
			fpByRef(inv, "C1").Fields["Keep"] = "mine"
			o.FieldsIgnore = map[string]bool{"Keep": true}
		}, func(t *testing.T, p *SyncPlan) {
			set, rm := opsOf(p, "setField"), opsOf(p, "removeField")
			if len(set) != 2 || set[0].Field != "Datasheet" || set[1].Field != "MPN" || len(rm) != 1 || rm[0].Ref != "R2" || rm[0].Field != "Old" {
				t.Fatalf("%+v", p.Ops)
			}
		}},
		{"sheet", func(_ *SyncNetlist, inv *SyncInventory, _ *SyncOptions) { fpByRef(inv, "C1").Sheetname = "Racine" },
			func(t *testing.T, p *SyncPlan) {
				ops := opsOf(p, "setSheet")
				if len(ops) != 1 || *ops[0].Sheetname != "/" || *ops[0].Sheetfile != "t.kicad_sch" {
					t.Fatalf("%+v", p.Ops)
				}
			}},
		{"flags", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			compByRef(nl, "R1").DNP = true
			compByRef(nl, "C1").ExcludeFromBOM = true
		}, func(t *testing.T, p *SyncPlan) {
			ops := opsOf(p, "setFlags")
			if len(ops) != 2 || ops[0].Ref != "C1" || !*ops[0].ExcludeFromBOM || *ops[0].DNP || ops[1].Ref != "R1" || !*ops[1].DNP {
				t.Fatalf("%+v", p.Ops)
			}
		}},
		{"update-subset", func(nl *SyncNetlist, _ *SyncInventory, o *SyncOptions) {
			compByRef(nl, "R2").Value = "2k2"
			compByRef(nl, "R1").DNP = true
			o.Update = map[string]bool{"dnp": true}
		}, func(t *testing.T, p *SyncPlan) {
			if len(opsOf(p, "setValue")) != 0 || len(opsOf(p, "setFlags")) != 1 {
				t.Fatalf("--update dnp must leave values: %+v", p.Ops)
			}
		}},
		{"multi-unit-second-key", func(_ *SyncNetlist, inv *SyncInventory, _ *SyncOptions) {
			fpByRef(inv, "U1").Path = "/" + uU1b
		}, func(t *testing.T, p *SyncPlan) {
			if len(p.Ops) != 0 || p.Match.ByPath != 5 {
				t.Fatalf("a footprint on any unit's key is linked: %+v", p.Ops)
			}
		}},
		{"pin-pad-mismatch", func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			o := netByName(nl, "/OUT")
			o.Nodes = append(o.Nodes, SyncNode{Ref: "U1", Pin: "9"})
		}, func(t *testing.T, p *SyncPlan) {
			if len(p.Errors) != 1 || p.Errors[0].Code != ErrPinPadMismatch || p.Errors[0].Ref != "U1" {
				t.Fatalf("%+v", p.Errors)
			}
		}},
		{"npth-untouched", func(_ *SyncNetlist, inv *SyncInventory, _ *SyncOptions) {
			f := fpByRef(inv, "J1")
			f.Pads = append(f.Pads, SyncPad{Number: "", Type: "npth"}, SyncPad{Number: "MP", Net: "", Type: "npth"})
		}, func(t *testing.T, p *SyncPlan) {
			if len(p.Ops) != 0 {
				t.Fatalf("non-plated holes carry no net: %+v", p.Ops)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := syncCase(c.edit)
			p, err := BuildSyncPlan(in)
			if err != nil {
				t.Fatal(err)
			}
			c.check(t, p)
			checkGolden(t, c.name, p)
			again, _ := BuildSyncPlan(syncCase(c.edit))
			a, _ := json.Marshal(p)
			b, _ := json.Marshal(again)
			if string(a) != string(b) {
				t.Fatal("BuildSyncPlan is not deterministic")
			}
		})
	}
}

func dropComp(nl *SyncNetlist, ref string) {
	var keep []SyncComp
	for _, c := range nl.Components {
		if c.Ref != ref {
			keep = append(keep, c)
		}
	}
	nl.Components = keep
	for i := range nl.Nets {
		var nodes []SyncNode
		for _, d := range nl.Nets[i].Nodes {
			if d.Ref != ref {
				nodes = append(nodes, d)
			}
		}
		nl.Nets[i].Nodes = nodes
	}
}

func dropNet(nl *SyncNetlist, name string) {
	var keep []SyncNet
	for _, n := range nl.Nets {
		if n.Name != name {
			keep = append(keep, n)
		}
	}
	nl.Nets = keep
}

func renameNodes(nl *SyncNetlist, from, to string) {
	for i := range nl.Nets {
		for j := range nl.Nets[i].Nodes {
			if nl.Nets[i].Nodes[j].Ref == from {
				nl.Nets[i].Nodes[j].Ref = to
			}
		}
	}
}

// TestBuildSyncPlanRefusals: every refusal of the design rejects the whole
// plan with its code; nothing is planned.
func TestBuildSyncPlanRefusals(t *testing.T) {
	cases := []struct {
		name, code string
		edit       func(nl *SyncNetlist, inv *SyncInventory, o *SyncOptions)
	}{
		{"unannotated", ErrUnannotated, func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			compByRef(nl, "R2").Ref = "R?"
			renameNodes(nl, "R2", "R?")
		}},
		{"dup-ref", ErrDupRef, func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) { compByRef(nl, "R2").Ref = "R1" }},
		{"dup-path", ErrDupPath, func(_ *SyncNetlist, inv *SyncInventory, _ *SyncOptions) {
			fpByRef(inv, "R2").Path = fpByRef(inv, "R1").Path
		}},
		{"fp-missing-field", ErrFPMissingField, func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			compByRef(nl, "C1").Footprint = ""
		}},
		{"lib-unresolved", ErrFPLibUnresolved, func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			c := compByRef(nl, "C1")
			c.Footprint = "NoSuchLib:C_0603"
		}},
		{"multi-unit-two-footprints", ErrDupPath, func(_ *SyncNetlist, inv *SyncInventory, _ *SyncOptions) {
			// U1's unit A on one footprint, unit B on a second one.
			second := *fpByRef(inv, "U1")
			second.ID, second.UUID, second.Ref, second.Path = "fp-U1b", "fp-U1b", "U1", "/"+uU1b
			inv.Footprints = append(inv.Footprints, second)
		}},
		{"multi-unit-two-footprints-reversed", ErrDupPath, func(_ *SyncNetlist, inv *SyncInventory, _ *SyncOptions) {
			second := *fpByRef(inv, "U1")
			second.ID, second.UUID, second.Ref, second.Path = "fp-U1b", "fp-U1b", "U1", "/"+uU1b
			inv.Footprints = append([]SyncFP{second}, inv.Footprints...)
		}},
		{"fp-not-found", ErrFPNotFound, func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) {
			nl.Components = append(nl.Components, syncComp("R3", "1k", "Resistor_SMD:R_1206", uR3))
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			in := syncCase(c.edit)
			if c.name == "fp-not-found" {
				in.Libs.(fakeLibs)["Resistor_SMD:R_1206"] = libRef("Resistor_SMD:R_1206")
			}
			p, err := BuildSyncPlan(in)
			r, ok := IsSyncRefusal(err)
			if !ok || p != nil {
				t.Fatalf("want a refusal, got plan %+v err %v", p, err)
			}
			found := false
			for _, e := range r.Errors {
				found = found || e.Code == c.code
			}
			if !found {
				t.Fatalf("want %s, got %+v", c.code, r.Errors)
			}
			if c.code == ErrFPLibUnresolved && r.Errors[0].Var != "KICAD10_FOOTPRINT_DIR" {
				t.Fatalf("the unresolved error must name the variable: %+v", r.Errors[0])
			}
		})
	}
}

// TestSyncStagingDeterministic: new footprints are packed in sheet / ref
// order into rows right of the outline, never on the board, and the same
// input gives the same places; footprints already parked there push the
// next batch further right.
func TestSyncStagingDeterministic(t *testing.T) {
	inv := &SyncInventory{Outline: &SyncBox{0, 0, 30, 20}}
	var boxes []*SyncBox
	for i := 0; i < 12; i++ {
		boxes = append(boxes, &SyncBox{-5, -3, 5, 3})
	}
	at := stagePositions(boxes, inv, SyncOptions{StagingGap: 2})
	for i, p := range at {
		if p[0]-5 < 40 {
			t.Fatalf("footprint %d at %v is not 10 mm right of the outline", i, p)
		}
	}
	// 100 mm rows of 10 mm boxes with 2 mm gaps: 8 per row.
	if at[7][1] != at[0][1] || at[8][1] == at[0][1] || at[8][0] != at[0][0] {
		t.Fatalf("rows: %v", at)
	}
	if again := stagePositions(boxes, inv, SyncOptions{StagingGap: 2}); !equalPos(at, again) {
		t.Fatal("not deterministic")
	}
	inv.Footprints = []SyncFP{{Box: &SyncBox{45, 0, 70, 6}}}
	pushed := stagePositions(boxes[:1], inv, SyncOptions{StagingGap: 2})
	if pushed[0][0]-5 < 80 {
		t.Fatalf("a parked footprint must push the next batch right: %v", pushed)
	}
	fixed := stagePositions(boxes[:1], inv, SyncOptions{Staging: &[2]float64{-50, -50}, StagingGap: 2})
	if fixed[0] != [2]float64{-45, -47} {
		t.Fatalf("--staging: %v", fixed)
	}
	none := stagePositions(boxes[:1], &SyncInventory{}, SyncOptions{})
	if none[0] != [2]float64{5, 3} {
		t.Fatalf("no outline, no footprints: from the origin, got %v", none)
	}
}

func equalPos(a, b [][2]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestSyncRevisionStable: the revision depends on the files' hashes, the
// options and the ops — nothing else.
func TestSyncRevisionStable(t *testing.T) {
	in := syncCase(func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) { compByRef(nl, "R2").Value = "2k2" })
	p1, _ := BuildSyncPlan(in)
	p2, _ := BuildSyncPlan(syncCase(func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) { compByRef(nl, "R2").Value = "2k2" }))
	r1 := SyncRevision("aa", "bb", in.Opts, p1)
	if r1 != SyncRevision("aa", "bb", in.Opts, p2) {
		t.Fatal("same input, different revision")
	}
	if r1 == SyncRevision("ab", "bb", in.Opts, p1) || r1 == SyncRevision("aa", "", in.Opts, p1) {
		t.Fatal("the revision must change with the files")
	}
	o := in.Opts
	o.Orphans = "remove"
	if r1 == SyncRevision("aa", "bb", o, p1) {
		t.Fatal("the revision must change with the options")
	}
	p3, _ := BuildSyncPlan(syncCase(func(nl *SyncNetlist, _ *SyncInventory, _ *SyncOptions) { compByRef(nl, "R2").Value = "3k3" }))
	if r1 == SyncRevision("aa", "bb", in.Opts, p3) {
		t.Fatal("the revision must change with the ops")
	}
}
