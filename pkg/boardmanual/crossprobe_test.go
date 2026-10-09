package boardmanual

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
)

const cpSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="297mm" height="210mm" viewBox="0.0000 0.0000 297.0000 210.0000"><title>plotted 2026-10-09T11:10:02</title><path d="M0 0"/></svg>`

// cpInput is a cross-probe of testBoard: every part has a symbol on the
// root page except U1 (sub-sheet) and D2 (none); S9 is a schematic-only
// BOM part; C1's footprint is not a BOM part.
func cpInput() *CrossProbeInput {
	in := &CrossProbeInput{
		Pages:    []CPPage{{Name: "", SVG: []byte(cpSVG)}, {Name: "MCU", SVG: []byte(cpSVG)}},
		Layers:   []CPLayer{{Name: "B.Cu", SVG: []byte(cpSVG)}, {Name: "F.Cu", SVG: []byte(cpSVG)}},
		NotInBOM: map[string]bool{"D2": true},
	}
	for i, r := range []string{"J1", "J2", "JP1", "X1", "C1", "D1"} {
		x := float64(20 + 15*i)
		in.Symbols = append(in.Symbols, CPSymbol{Ref: r, Page: 0, MinX: x, MinY: 40, MaxX: x + 8, MaxY: 52, InBOM: true, OnBoard: true})
	}
	in.Symbols = append(in.Symbols,
		CPSymbol{Ref: "U1", Page: 1, MinX: 100, MinY: 80, MaxX: 130, MaxY: 120, Rot: 90, InBOM: true, OnBoard: true},
		CPSymbol{Ref: "U1", Page: 1, MinX: 150, MinY: 80, MaxX: 160, MaxY: 100, InBOM: true, OnBoard: true}, // unit B
		CPSymbol{Ref: "S9", Page: 0, MinX: 5, MinY: 5, MaxX: 9, MaxY: 9, InBOM: true, OnBoard: true},
		CPSymbol{Ref: "LOGO1", Page: 0, MinX: 5, MinY: 5, MaxX: 9, MaxY: 9, InBOM: false, OnBoard: true},
	)
	return in
}

func TestCrossProbeRefMap(t *testing.T) {
	in := testInputs(t, "")
	in.CrossProbe = cpInput()
	in.Intent = &intent.Intent{Nets: map[string]*intent.NetPlan{
		"+3V3": {Role: "power", CurrentA: 0.5, WidthMil: intent.Width{Outer: 20, Min: 10}},
	}, Findings: []*intent.Finding{
		{Severity: "warn", Kind: "creepage", Message: "U1 pin 2 near VIN", Refs: []string{"U1"}},
		{Severity: "info", Kind: "rail", Message: "3V3 rail", Nets: []string{"+3V3"}},
		{Severity: "warn", Kind: "other", Message: "J1 only", Refs: []string{"J1"}},
	}}
	in.Post = &postsim.Result{Nets: []*postsim.NetResult{{Net: "+3V3", WorstMV: 12.5, BudgetMV: 33, Status: "ok"}}}
	in.Board.Part("U1").LCSC = "C12345"
	in.Board.Part("U1").Rotation = 180
	m := Build(in)
	xp := m.CrossProbe
	if xp == nil {
		t.Fatal("no cross-probe map")
	}
	if len(xp.Pages) != 2 || xp.Pages[0].W != 297 || xp.Pages[1].Name != "MCU" || !strings.HasPrefix(xp.Pages[0].URI, "data:image/svg+xml;base64,") {
		t.Fatalf("pages = %+v", xp.Pages)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(xp.Pages[0].URI, "data:image/svg+xml;base64,"))
	if strings.Contains(string(raw), "<title>") {
		t.Error("plot <title> (timestamp) kept: the manual is not reproducible")
	}
	if xp.Layers[1].Color == "" {
		t.Error("F.Cu has no tint")
	}
	// board outline 0..1574.8 × 0..1181.1 mil, y up → page mm, y down
	if b := xp.Board; b[0] != 0 || b[1] != -30 || b[2] != 40 || b[3] != 30 {
		t.Errorf("board box = %v", b)
	}
	parts := map[string]XPart{}
	for _, p := range xp.Parts {
		parts[p.Ref] = p
	}
	u1 := parts["U1"]
	if u1.SchPage != 1 || len(u1.SchBoxes) != 2 || u1.SchBoxes[0] != [4]float64{100, 80, 30, 40} {
		t.Errorf("U1 schematic = page %d %v", u1.SchPage, u1.SchBoxes)
	}
	if u1.PCBBox == nil || u1.LCSC != "C12345" || u1.Rotation != 90 {
		t.Errorf("U1 pcb box %v lcsc %q rotation %v (want 180−90)", u1.PCBBox, u1.LCSC, u1.Rotation)
	}
	var n3 *XNet
	for i := range u1.Nets {
		if u1.Nets[i].Name == "+3V3" {
			n3 = &u1.Nets[i]
		}
	}
	if n3 == nil || n3.Pins != "2" || n3.WidthMM != 0.508 || n3.MinMM != 0.254 || n3.IRmV != 12.5 || n3.IRStatus != "ok" {
		t.Errorf("U1 +3V3 = %+v", n3)
	}
	kinds := []string{}
	for _, f := range u1.Findings {
		kinds = append(kinds, f.Kind)
	}
	if strings.Join(kinds, ",") != "creepage,rail" {
		t.Errorf("U1 findings = %v, want its own + its net's (not J1's)", kinds)
	}
	// J1 pcb box: bbox (20,500)-(220,700) mil → x 0.508, y −17.78, 5.08 × 5.08
	if b := parts["J1"].PCBBox; b == nil || *b != [4]float64{0.508, -17.78, 5.08, 5.08} {
		t.Errorf("J1 pcb box = %v", b)
	}
	if _, ok := parts["LOGO1"]; !ok {
		t.Error("non-BOM symbol LOGO1 left out of the map")
	}
	want := []string{"S9 is in the schematic BOM but has no footprint on the PCB"}
	if strings.Join(xp.Missing, "|") != strings.Join(want, "|") {
		t.Errorf("missing = %q, want %q (D2 is not a BOM part, LOGO1 not in the BOM)", xp.Missing, want)
	}
	items := strings.Join(GateCheck(m, &Notes{}), "\n")
	if !strings.Contains(items, "cross-probe: S9 is in the schematic BOM") {
		t.Errorf("gate does not report the missing map entry:\n%s", items)
	}
}

func TestCrossProbeBoardOnlyPart(t *testing.T) {
	in := testInputs(t, "")
	cp := cpInput()
	cp.NotInBOM = nil                                                         // D2 is a BOM part now: no symbol
	in.Board.Components = append(in.Board.Components, Part{Designator: "H9"}) // PCB-only NPTH mounting hole: no copper pad
	cp.Pages[1].SVG = nil
	in.CrossProbe = cp
	m := Build(in)
	got := strings.Join(m.CrossProbe.Missing, "|")
	for _, w := range []string{`schematic page "MCU" has no plot`, "D2 is on the PCB but has no schematic symbol"} {
		if !strings.Contains(got, w) {
			t.Errorf("missing %q in %q", w, got)
		}
	}
	if strings.Contains(got, "H9") {
		t.Errorf("a footprint without copper pads needs no symbol: %q", got)
	}
}

func TestCrossProbeRendered(t *testing.T) {
	in := testInputs(t, "")
	in.CrossProbe = cpInput()
	html, err := RenderHTML(Build(in))
	if err != nil {
		t.Fatal(err)
	}
	s := string(html)
	for _, w := range []string{`id="s4x"`, `id="xp-lens"`, `"schBoxes":`, `"pcbBox":`, `window.XPLens`, `data-xp="U1"`} {
		if !strings.Contains(s, w) {
			t.Errorf("rendered manual lacks %s", w)
		}
	}
	if strings.Contains(s, "src=\"http") || strings.Contains(s, "href=\"http") {
		t.Error("manual fetches an external resource")
	}
	// without KiCad input: no lens section
	html, _ = RenderHTML(Build(testInputs(t, "")))
	if strings.Contains(string(html), `id="xp-lens"`) {
		t.Error("lens rendered without a cross-probe")
	}
}
