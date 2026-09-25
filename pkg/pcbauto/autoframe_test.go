package pcbauto

import (
	"os"
	"testing"
	"time"
)

// esp32Mini is the ESP32-S3 mini board right after `pcb import-changes`
// (2026-09-25 E2E, desktop V3): 30 parts spread over 4.9" × 1.9", no outline.
func esp32Mini(t *testing.T) (*Board, *Analysis, *Circuit) {
	t.Helper()
	raw, err := os.ReadFile("testdata/esp32-mini-import.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	an := Analyze(b, PowerSpec{Rails: []PowerRail{{Net: "LX", Voltage: 5, CurrentA: 1.5}, {Net: "+3V3", Voltage: 3.3, CurrentA: 0.8}}}, nil)
	c := Understand(b, an)
	graw, err := os.ReadFile("testdata/esp32-mini-groups.json")
	if err != nil {
		t.Fatal(err)
	}
	gs, err := ParseGroups(graw)
	if err != nil {
		t.Fatal(err)
	}
	c.Notes = append(c.Notes, ApplyGroups(c, b, an, gs)...)
	return b, an, c
}

func ownerOf(c *Circuit, ref string) (core, role string) {
	for _, bl := range c.Blocks {
		for _, m := range bl.Members {
			if m.Ref == ref {
				return bl.Core, m.Role
			}
		}
	}
	return "", ""
}

// Schematic modules decide ownership on shared rails; port protection stays
// at its connector whatever page drew it.
func TestApplyGroupsSchematicOwnership(t *testing.T) {
	_, _, c := esp32Mini(t)
	for _, tc := range []struct{ ref, core, role string }{
		{"C2", "U1", "power-stage"}, // buck 22 µF output: was handed to U2 by rail load-balancing
		{"C3", "U1", "power-stage"}, // buck 100 nF output: was handed to U3
		{"C5", "U3", "decap"},       // module bulk cap: was handed to U2
		{"D1", "J1", "power-path"},  // terminal OR-ing diode: was unassigned
		{"D3", "J2", "protection"},  // USBLC6 drawn in the UART module stays at the USB port
	} {
		core, role := ownerOf(c, tc.ref)
		if core != tc.core || role != tc.role {
			t.Errorf("%s: owner %s/%s, want %s/%s", tc.ref, core, role, tc.core, tc.role)
		}
	}
}

// A declared switch node sizes its copper but never becomes a plane net.
func TestDeclaredSwitchNodeKeepsRole(t *testing.T) {
	_, an, _ := esp32Mini(t)
	if r := an.Plan("LX", DefaultRules()).Role; r != RoleSwitch {
		t.Fatalf("LX role %s, want switch", r)
	}
}

// autoSize without a size searches the frame: holes in the corners, edge
// parts flush, the antenna end kept clear, auxiliaries still at their pins.
func TestAutoFrameESP32Mini(t *testing.T) {
	if testing.Short() {
		t.Skip("places ~6 frames")
	}
	b, an, c := esp32Mini(t)
	spec, err := ParseMech([]byte(`{"units":"mm","board":{"autoSize":true,"margin":1.5},"cornerHoles":{"size":"M3"},
		"edge":[{"ref":"U3","edge":"top","at":-1},{"ref":"J2","edge":"bottom","at":-1,"overhang":0.5},{"ref":"J1","edge":"left","at":-1}]}`))
	if err != nil {
		t.Fatal(err)
	}
	opt := PlaceOptions{Seed: 0, Timeout: 30 * time.Second}
	sized, fs, err := AutoFrame(b, an, c, spec, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("frame %.1f × %.1f mm, trials %+v", sized.Board.Width, sized.Board.Height, fs.Trials)
	if sized.Board.AutoSize || sized.Board.Width*sized.Board.Height > 50*50 {
		t.Fatalf("frame %.1f × %.1f mm: not a compact fixed frame", sized.Board.Width, sized.Board.Height)
	}
	if len(b.Outline) != 0 || len(b.Holes) != 0 {
		t.Fatalf("AutoFrame left trial mechanics on the board")
	}
	mc, err := ApplyMech(b, sized)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Place(b, an, c, mc, opt)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Metrics
	if m.Overlaps != 0 || m.OutOfBoard != 0 || m.KeepoutHits != 0 {
		t.Fatalf("illegal placement: %+v", m)
	}
	if len(b.Holes) != 4 {
		t.Fatalf("want 4 corner holes, got %d", len(b.Holes))
	}
	for _, h := range b.Holes {
		if !PolyContains(b.Outline, h.C) {
			t.Errorf("hole %s outside the outline", h.Name)
		}
	}
	var ant *Keepout
	for _, k := range b.Keepouts {
		if k.Owner == "U3" {
			ant = k
		}
	}
	if ant == nil {
		t.Fatal("no antenna keep-out for the WROOM module")
	}
	if kb, u3 := PolyBounds(ant.Poly), b.Part("U3").Body(); kb.MinY <= u3.Center().Y || kb.MaxY < u3.MaxY {
		t.Errorf("antenna keep-out %+v not over U3's pad-free top end %+v", kb, u3)
	}
	// Body edge gaps: centre distances grow with the part size and said
	// nothing about adjacency once bodies were modelled at full size.
	near := func(a, bref string, mil float64) {
		d := rectDist(b.Part(a).Body(), b.Part(bref).Body())
		t.Logf("%s–%s edge gap %.0f mil", a, bref, d)
		if d > mil {
			t.Errorf("%s is %.0f mil from %s (> %.0f)", a, d, bref, mil)
		}
	}
	near("L1", "U1", 120)
	near("C2", "L1", 120)
	near("D1", "J1", 150)
	near("D3", "J2", 200)
}
