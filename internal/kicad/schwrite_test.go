package kicad

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A two-pin library symbol (pins at x = ±3.81, y up), and an asymmetric
// three-pin one so rotation and mirror are observable.
const testRes = `(symbol "R"
	(pin_numbers (hide yes))
	(exclude_from_sim no) (in_bom yes) (on_board yes)
	(property "Reference" "R" (at 2.032 0 90) (effects (font (size 1.27 1.27))))
	(property "Value" "R" (at 0 0 90) (effects (font (size 1.27 1.27))))
	(symbol "R_0_1" (rectangle (start -1.016 -2.54) (end 1.016 2.54) (stroke (width 0.254) (type default)) (fill (type none))))
	(symbol "R_1_1"
		(pin passive line (at 0 3.81 270) (length 1.27) (name "~" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
		(pin passive line (at 0 -3.81 90) (length 1.27) (name "~" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27)))))
	)
)`

const testTri = `(symbol "T"
	(exclude_from_sim no) (in_bom yes) (on_board yes)
	(property "Reference" "U" (at 0 5.08 0) (effects (font (size 1.27 1.27))))
	(property "Value" "T" (at 0 -5.08 0) (effects (font (size 1.27 1.27))))
	(symbol "T_0_1" (rectangle (start -2.54 2.54) (end 2.54 -2.54) (stroke (width 0.254) (type default)) (fill (type none))))
	(symbol "T_1_1"
		(pin input line (at -5.08 1.27 0) (length 2.54) (name "A" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
		(pin input line (at -5.08 -1.27 0) (length 2.54) (name "B" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27)))))
		(pin output line (at 5.08 2.54 180) (length 2.54) (name "Y" (effects (font (size 1.27 1.27)))) (number "3" (effects (font (size 1.27 1.27)))))
	)
)`

func TestSymbolXform(t *testing.T) {
	at := Pt{100, 50}
	cases := []struct {
		rot  float64
		mir  string
		want Pt
	}{
		{0, "", Pt{105.08, 47.46}},
		{90, "", Pt{97.46, 44.92}},
		{180, "", Pt{94.92, 52.54}},
		{270, "", Pt{102.54, 55.08}},
		{0, "y", Pt{94.92, 47.46}},
		{0, "x", Pt{105.08, 52.54}},
	}
	for _, c := range cases {
		got := SymbolXform(Pt{5.08, 2.54}, at, c.rot, c.mir)
		if (Pt{round4(got.X), round4(got.Y)}) != c.want {
			t.Errorf("rot %v mirror %q: got %v want %v", c.rot, c.mir, got, c.want)
		}
	}
}

func round4(v float64) float64 { return math.Round(v*1e4) / 1e4 }

func buildTestSheet(t *testing.T) (string, *SchEditor) {
	t.Helper()
	e, err := OpenSchematic(NewSchematicText("A4", NewUUID(), true))
	if err != nil {
		t.Fatal(err)
	}
	e.Project = "t"
	if err := e.AddLibSymbol("test:R", testRes); err != nil {
		t.Fatal(err)
	}
	if err := e.AddLibSymbol("test:T", testTri); err != nil {
		t.Fatal(err)
	}
	// U1 rotated 90 and mirrored; R1/R2 plain/rotated.
	if _, err := e.PlaceSymbol(SymbolInstance{LibID: "test:T", Ref: "U1", Value: "T", At: Pt{101.6, 76.2}, Rot: 90, Mirror: "y",
		Fields: []Field{{Name: "LCSC", Value: "C1234", Hide: true}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PlaceSymbol(SymbolInstance{LibID: "test:R", Ref: "R1", Value: "10k", Footprint: "R0603", At: Pt{127, 76.2}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PlaceSymbol(SymbolInstance{LibID: "test:R", Ref: "R2", Value: "1k", At: Pt{152.4, 76.2}, Rot: 270}); err != nil {
		t.Fatal(err)
	}
	u, _ := e.SymbolPinPositions("U1")
	r1, _ := e.SymbolPinPositions("R1")
	r2, _ := e.SymbolPinPositions("R2")
	// U1.3 (Y) → R1.1 with an L-shaped wire; R1.2 → GND; U1.1 → label IN;
	// U1.2 no-connect; R2.1 + R2.2 on global label SIG / power +3V3.
	e.AddWire(u["3"], Pt{u["3"].X, r1["1"].Y}, r1["1"])
	if _, err := e.AddPower("GND", r1["2"], 0, true); err != nil {
		t.Fatal(err)
	}
	if err := e.AddLabel(LabelLocal, "IN", u["1"], 0, ""); err != nil {
		t.Fatal(err)
	}
	e.AddNoConnect(u["2"])
	if err := e.AddLabel(LabelGlobal, "SIG", r2["1"], 0, "bidirectional"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AddPower("+3V3", r2["2"], 0, false); err != nil {
		t.Fatal(err)
	}
	if err := e.SetField("R1", "Value", "4.7k"); err != nil {
		t.Fatal(err)
	}
	if err := e.SetField("R1", "LCSC", "C23162"); err != nil {
		t.Fatal(err)
	}
	out, err := e.Render()
	if err != nil {
		t.Fatal(err)
	}
	return out, e
}

func TestSchEditorRoundTrip(t *testing.T) {
	out, _ := buildTestSheet(t)
	e2, err := OpenSchematic(out)
	if err != nil {
		t.Fatal(err)
	}
	if !e2.HasLibSymbol("pcbpilot_power:GND") || !e2.HasLibSymbol("test:T") {
		t.Fatal("lib symbols lost")
	}
	if !strings.Contains(out, `(property "Value" "4.7k"`) || !strings.Contains(out, `(property "LCSC" "C23162"`) {
		t.Fatal("SetField on a pending symbol did not apply")
	}
	// edit the re-parsed file: rename, move, and the instance path stays.
	if err := e2.SetField("R2", "Reference", "R9"); err != nil {
		t.Fatal(err)
	}
	if err := e2.MoveSymbol("R2", Pt{160.02, 76.2}, 270); err != nil {
		t.Fatal(err)
	}
	out2, err := e2.Render()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out2, `(reference "R9")`) || strings.Contains(out2, `(reference "R2")`) {
		t.Fatal("rename did not reach the instance")
	}
	if !strings.Contains(out2, "(at 160.02 76.2 270)") {
		t.Fatal("move not applied")
	}
	if strings.Count(out2, "#PWR0001") != 2 || strings.Count(out2, "#PWR0002") != 2 {
		t.Fatal("power references not unique")
	}
}

// TestSchEditorKicadNetlist checks the written sheet with kicad-cli: the
// pin positions SymbolXform computes are where KiCad connects.
func TestSchEditorKicadNetlist(t *testing.T) {
	if _, err := KicadCLI(); err != nil {
		t.Skip(err)
	}
	out, _ := buildTestSheet(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "t.kicad_sch")
	if err := os.WriteFile(p, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	nl, err := ExportSchNetlist(p)
	if err != nil {
		t.Fatal(err)
	}
	pn := nl.PinNets()
	want := map[string]string{"R1.2": "GND", "U1.1": "/IN", "R2.1": "SIG", "R2.2": "+3V3"}
	for k, v := range want {
		if pn[k] != v {
			t.Errorf("%s on %q, want %q (all: %v)", k, pn[k], v, pn)
		}
	}
	if pn["U1.3"] == "" || pn["U1.3"] != pn["R1.1"] {
		t.Errorf("U1.3 (%q) and R1.1 (%q) not joined by the wire", pn["U1.3"], pn["R1.1"])
	}
	doc, err := nl.ToConnectivityDoc("t")
	if err != nil {
		t.Fatal(err)
	}
	var nc, lcsc bool
	for _, c := range doc.Components {
		for _, pin := range c.Pins {
			if c.Ref == "U1" && pin.Number == "2" && pin.NoConnected {
				nc = true
			}
		}
		if c.Ref == "R1" && c.Device.SupplierID == "C23162" && c.Device.Name == "4.7k" {
			lcsc = true
		}
	}
	if !nc || !lcsc {
		t.Errorf("no-connect %v, R1 LCSC/value %v", nc, lcsc)
	}
	for _, n := range doc.Nets {
		if n.Name == "/IN" {
			t.Error("unique sheet-local name kept its path")
		}
	}
}
