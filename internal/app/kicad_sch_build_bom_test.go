package app

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func TestSbPackageOf(t *testing.T) {
	for fp, want := range map[string]string{
		"":                                                           "",
		"Resistor_SMD:R_0603_1608Metric":                             "0603",
		"Capacitor_SMD:C_0805_2012Metric":                            "0805",
		"LED_SMD:LED_0603_1608Metric":                                "0603",
		"R_0402_1005Metric_Pad0.72x0.64mm":                           "0402",
		"lcsc:R0805":                                                 "0805",
		"Package_SO:SOIC-8_3.9x4.9mm_P1.27mm":                        "SOIC-8",
		"Package_QFP:LQFP-48_7x7mm_P0.5mm":                           "LQFP-48",
		"Package_TO_SOT_SMD:SOT-23-5":                                "SOT-23-5",
		"QFN-32-1EP_5x5mm_P0.5mm_EP3.1x3.1mm":                        "QFN-32",
		"Diode_SMD:D_SOD-123":                                        "SOD-123",
		"Diode_SMD:D_SMA":                                            "D_SMA",
		"Package_TO_SOT_THT:TO-220-3_Vertical":                       "TO-220-3",
		"Resistor_SMD:R_Array_Convex_4x0603":                         "4x0603",
		"Connector_PinHeader_2.54mm:PinHeader_1x02_P2.54mm_Vertical": "PinHeader",
		"Inductor_SMD:L_Bourns_SRR1260":                              "L_Bourns_SRR1260",
		"Transformer_THT:Transformer_Breve_TEZ-22x24_AndSomeVeryLongSuffix": "Transformer",
	} {
		got := sbPackageOf(fp)
		if got != want {
			t.Errorf("sbPackageOf(%q) = %q, want %q", fp, got, want)
		}
		if len([]rune(got)) > sbPkgMax {
			t.Errorf("sbPackageOf(%q) = %q: longer than %d", fp, got, sbPkgMax)
		}
	}
	// the spec's own word wins
	p := &sbRPart{sbPart: sbPart{Ref: "T1", Footprint: "My:XFMR_EE16_Horizontal", Fields: map[string]string{"package": " EE16  THT "}}}
	if got := sbPackageText(p); got != "EE16 THT" {
		t.Errorf("fields.Package override: %q", got)
	}
}

// sbTestSym is a synthetic symbol: body w x h around the origin, pins on
// the named sides.
func sbTestSym(w, h float64, sides ...string) kicad.SceneSymbol {
	s := kicad.SceneSymbol{Ref: "X1", HasBox: true, Box: kicad.Box{MinX: -w / 2, MinY: -h / 2, MaxX: w / 2, MaxY: h / 2}}
	s.Body = s.Box
	for i, side := range sides {
		q := kicad.ScenePin{Number: strconv.Itoa(i + 1)}
		switch side {
		case "left":
			q.At, q.Outward = kicad.Pt{X: -w / 2}, 180
		case "right":
			q.At, q.Outward = kicad.Pt{X: w / 2}, 0
		case "top":
			q.At, q.Outward = kicad.Pt{Y: -h / 2}, 90
		case "bottom":
			q.At, q.Outward = kicad.Pt{Y: h / 2}, 270
		}
		s.Pins = append(s.Pins, q)
	}
	return s
}

func TestSbFieldsPackageInValueBox(t *testing.T) {
	inside := func(in, out kicad.Box) bool {
		const e = 1e-6
		return in.MinX >= out.MinX-e && in.MinY >= out.MinY-e && in.MaxX <= out.MaxX+e && in.MaxY <= out.MaxY+e
	}
	textBox := func(at kicad.Pt, s string) kicad.Box {
		w := sbTextW(s)
		return kicad.Box{MinX: at.X - w/2, MinY: at.Y - 0.9, MaxX: at.X + w/2, MaxY: at.Y + 0.9}
	}
	for _, c := range []struct {
		name            string
		sym             kicad.SceneSymbol
		ref, value, pkg string
		lines           int // text lines under / beside the body the value + package take
	}{
		{"horizontal two-pin, short texts share a line", sbTestSym(7.62, 2.54, "left", "right"), "R1", "1k", "0603", 1},
		{"horizontal two-pin, long value: package on its own line", sbTestSym(7.62, 2.54, "left", "right"), "R1", "4.7k 1%", "0805", 2},
		{"vertical two-pin: three lines beside", sbTestSym(2.54, 7.62, "top", "bottom"), "C1", "100nF", "0805", 2},
		{"wide IC: one line under the body", sbTestSym(30.48, 20.32, "left", "right"), "U1", "MCU-8", "SOIC-8", 1},
		{"pins all round: under the body", sbTestSym(20.32, 20.32, "left", "right", "top", "bottom"), "U2", "A-VERY-LONG-VALUE-NAME", "LQFP-48", 2},
	} {
		f := sbFields(c.sym, sbPose{}, c.ref, c.value, c.pkg)
		val := textBox(f.Val, c.value)
		if f.PkgBox == (kicad.Box{}) {
			t.Errorf("%s: no package box", c.name)
			continue
		}
		if !inside(f.PkgBox, f.VBox) || !inside(val, f.VBox) {
			t.Errorf("%s: VBox %v must cover the value %v and the package %v", c.name, f.VBox, val, f.PkgBox)
		}
		for what, b := range map[string]kicad.Box{"value": val, "reference": f.RefBox, "body": c.sym.Box} {
			if sbOverlap(f.PkgBox, b) {
				t.Errorf("%s: package text %v lies on the %s %v", c.name, f.PkgBox, what, b)
			}
		}
		if sbOverlap(val, f.RefBox) || sbOverlap(val, c.sym.Box) {
			t.Errorf("%s: value text %v lies on the reference or the body", c.name, val)
		}
		lines := 1
		if f.Pkg.Y != f.Val.Y {
			lines = 2
		}
		if lines != c.lines {
			t.Errorf("%s: value + package take %d line(s), want %d", c.name, lines, c.lines)
		}
		// no package: exactly the old two texts
		g := sbFields(c.sym, sbPose{}, c.ref, c.value, "")
		if g.PkgBox != (kicad.Box{}) || g.VBox != textBox(g.Val, c.value) {
			t.Errorf("%s: without a package the value box must be the value text alone", c.name)
		}
	}
}

func sbTestPart(ref, value, footprint string, fields map[string]string) *sbRPart {
	p := &sbRPart{sbPart: sbPart{Ref: ref, Value: value, Footprint: footprint, Fields: fields}}
	p.Package = sbPackageText(p)
	return p
}

func TestSbBomRows(t *testing.T) {
	r := func(ref string) *sbRPart { return sbTestPart(ref, "10k", "Resistor_SMD:R_0603_1608Metric", nil) }
	t1 := sbTestPart("T1", "EE16 1:1.2", "Transformer_SMD:Transformer_Demo", map[string]string{
		"description": "Flyback  transformer", "Power": "5 W", "Inductance": "450 uH", "Sourcing": "custom / self-sourced", "Datasheet": "x.pdf"})
	u1 := sbTestPart("U1", "LDO", "Package_TO_SOT_SMD:SOT-23-5", nil)
	u1.LCSC = "C51118"
	d4 := sbTestPart("D4", "RED", "LED_SMD:LED_0603_1608Metric", nil)
	d4.DNP = true
	d3 := sbTestPart("D3", "RED", "LED_SMD:LED_0603_1608Metric", nil)
	rows := sbBomRows([]*sbRPart{r("R7"), t1, r("R2"), d4, r("R1"), u1, r("R4"), d3, r("R3")})
	got := map[string]sbBomRow{}
	var order []string
	for _, row := range rows {
		got[row.RefText] = row
		order = append(order, row.RefText)
	}
	if want := "D3 | D4 (DNP) | R1-R4, R7 | T1 | U1"; strings.Join(order, " | ") != want {
		t.Fatalf("rows = %q, want %q", strings.Join(order, " | "), want)
	}
	if row := got["R1-R4, R7"]; row.Qty != 5 || row.Package != "0603" || row.Value != "10k" || row.Sourcing != "—" || row.Spec != "" {
		t.Errorf("resistor row %+v", row)
	}
	if row := got["T1"]; row.Spec != "Flyback transformer; Power: 5 W; Inductance: 450 uH" || row.Sourcing != "custom / self-sourced" {
		t.Errorf("transformer row %+v", row)
	}
	if row := got["U1"]; row.Sourcing != "LCSC" || row.LCSC != "C51118" || row.Package != "SOT-23-5" {
		t.Errorf("LCSC row %+v", row)
	}
	if !got["D4 (DNP)"].DNP || got["D3"].DNP {
		t.Errorf("a DNP part is a line of its own: %+v", rows)
	}
	for in, want := range map[string]string{
		"R1":                    "R1",
		"R2 R1":                 "R1, R2",
		"R3 R1 R2":              "R1-R3",
		"C10 C9 C8 C1 C12":      "C1, C8-C10, C12",
		"R1 R2 R3 C1 C2 C3 TP1": "C1-C3, R1-R3, TP1",
		"U1A U1B":               "U1A, U1B",
	} {
		if got := sbCompressRefs(strings.Fields(in)); got != want {
			t.Errorf("sbCompressRefs(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSbWrap(t *testing.T) {
	text := "Flyback transformer, custom wound; Inductance: 450 uH primary; Isolation: 1.5 kV"
	lines := sbWrap(text, 24)
	if len(lines) < 3 {
		t.Fatalf("lines %q", lines)
	}
	for _, l := range lines {
		if n := len([]rune(l)); n > 24 || n == 0 {
			t.Errorf("line %q: %d characters", l, n)
		}
	}
	strip := func(s string) string { return strings.Join(strings.Fields(s), "") }
	if strip(strings.Join(lines, " ")) != strip(text) {
		t.Errorf("wrapped text lost characters: %q", lines)
	}
	if got := sbWrap("ABCDEFGHIJKLMNOPQRSTUVWXYZ", 10); len(got) != 3 || got[0] != "ABCDEFGHIJ" {
		t.Errorf("a long word is split: %q", got)
	}
	if got := sbWrap("", 10); len(got) != 1 {
		t.Errorf("empty cell: %q", got)
	}
}

func TestSbBomLayout(t *testing.T) {
	mk := func(n int) []sbBomRow {
		var parts []*sbRPart
		for i := 1; i <= n; i++ {
			parts = append(parts, sbTestPart("U"+strconv.Itoa(i), "IC-"+strconv.Itoa(i), "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm",
				map[string]string{"Description": "synthetic part number " + strconv.Itoa(i) + " with a spec long enough to wrap in its column, twice over"}))
		}
		return sbBomRows(parts)
	}
	check := func(sheets []sbBomSheet, rows int) {
		t.Helper()
		total := 0
		for i, sh := range sheets {
			total += sh.Rows
			if len(sh.Cells) != sh.Rows+1 || strings.Join(sh.Cells[0], "|") != strings.Join(sbBomHeader, "|") {
				t.Errorf("sheet %d: the header heads every sheet (%d cells rows, %d rows)", i+1, len(sh.Cells), sh.Rows)
			}
			pw, ph := sbPaperSize(sh.Paper)
			w, h := 0.0, 0.0
			for _, c := range sh.ColW {
				w += c
			}
			for _, r := range sh.RowH {
				h += r
			}
			if sbBomLeft+w > pw-sbBomBorder+1e-6 || sbBomTop+h > ph-sbBomTitleH+1e-6 {
				t.Errorf("sheet %d (%s): table %gx%g leaves the drawing area", i+1, sh.Paper, w, h)
			}
			// what is written is a table and a heading, no symbol; kicad
			// sch-fit leaves it where it is
			e, err := sbNewSheet("00000000-0000-4000-8000-000000000001", false, kicad.TitleBlock{Title: "t"})
			if err != nil {
				t.Fatal(err)
			}
			e.SetPaper(sh.Paper)
			e.AddRaw(sbBomHeading("00000000-0000-4000-8000-000000000001", "heading"))
			e.AddRaw(sbBomTable(sh, "00000000-0000-4000-8000-000000000001"))
			text, err := e.Render()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(text, "(symbol ") || !strings.Contains(text, "(table") {
				t.Errorf("sheet %d: want a table and no symbol", i+1)
			}
			_, fr, err := kicad.FitSheet(text)
			if err != nil {
				t.Fatal(err)
			}
			if fr.TooBig || fr.To != sh.Paper || fr.ShiftX != 0 || fr.ShiftY != 0 {
				t.Errorf("sheet %d: sch-fit would change it: %+v", i+1, fr)
			}
		}
		if total != rows {
			t.Errorf("rows on the sheets = %d, want %d", total, rows)
		}
	}
	few := sbBomLayout(mk(5))
	if len(few) != 1 || few[0].Paper != "A4" {
		t.Fatalf("5 rows: %d sheet(s), paper %s; want one A4", len(few), few[0].Paper)
	}
	check(few, 5)
	many := sbBomLayout(mk(400))
	if len(many) < 2 {
		t.Fatalf("400 rows on %d sheet(s): want continuation sheets", len(many))
	}
	check(many, 400)
	// a row that would be taller than a sheet is cut, not printed off it;
	// every row is on a sheet however many there are
	huge := mk(3)
	huge[1].Spec = strings.Repeat("a requirement that goes on and on, ", 400)
	cut := sbBomLayout(huge)
	check(cut, 3)
	found := false
	for _, sh := range cut {
		for _, r := range sh.Cells {
			for _, c := range r {
				found = found || strings.Contains(c, "… (cut: see the spec)")
			}
		}
	}
	if !found {
		t.Errorf("the oversized cell carries no cut mark")
	}
	check(sbBomLayout(mk(9000)), 9000)
	if sbBomFile("power.kicad_sch", 1, nil) != "power-bom.kicad_sch" || sbBomFile("power.kicad_sch", 2, nil) != "power-bom-2.kicad_sch" {
		t.Errorf("file names: %s, %s", sbBomFile("power.kicad_sch", 1, nil), sbBomFile("power.kicad_sch", 2, nil))
	}
	if sbBomSheetName("Power", 1) != "Power BOM" || sbBomSheetName("Power", 2) != "Power BOM 2" || sbBomSheetName("", 1) != "BOM" {
		t.Errorf("sheet names: %q %q %q", sbBomSheetName("Power", 1), sbBomSheetName("Power", 2), sbBomSheetName("", 1))
	}
}

func TestSbBomCheck(t *testing.T) {
	u1 := sbTestPart("U1", "LDO", "Package_TO_SOT_SMD:SOT-23-5", nil)
	u1.MPN = "DEMO-3V3"
	tp := sbTestPart("TP1", "TP", "TestPoint:TestPoint_Pad_D1.5mm", nil)
	parts := []*sbRPart{
		sbTestPart("U2", "MCU", "Package_SO:SOIC-8_3.9x4.9mm_P1.27mm", nil), // no MPN, no description
		sbTestPart("R1", "10k", "Resistor_SMD:R_0603_1608Metric", nil),      // jellybean
		sbTestPart("C1", "100nF", "", nil),                                  // no footprint
		sbTestPart("T1", "EE16", "My:XFMR", map[string]string{"Voltage": "1.5 kV"}),
		sbTestPart("T2", "EE16", "My:XFMR", map[string]string{"Description": "custom flyback", "Sourcing": "custom"}),
		u1, tp,
	}
	noPkg, noSpec, missing := sbBomCheck(parts)
	if strings.Join(noPkg, ",") != "C1" {
		t.Errorf("noPackage = %v", noPkg)
	}
	if strings.Join(noSpec, ",") != "U1,U2" {
		t.Errorf("noSpec = %v", noSpec)
	}
	var refs []string
	for _, m := range missing {
		refs = append(refs, m.Ref)
		if !strings.Contains(m.Fix, m.Ref) || m.Reason == "" {
			t.Errorf("missing entry without a fix naming the part: %+v", m)
		}
	}
	if strings.Join(refs, ",") != "C1,T1,U2" {
		t.Errorf("missing = %v, want C1 (no footprint), T1 and U2 (not identified)", refs)
	}
}

func sbBomBuild(t *testing.T, spec *sbSpec, o sbOptions) *sbReport {
	t.Helper()
	o.Jobs, o.PlannerBudget, o.NoIntent = 4, 20000, true
	res, err := runSchBuild(spec, o, nil)
	if err != nil || !res.OK {
		t.Fatalf("build: %v %+v", err, res.Error)
	}
	return res
}

func TestSchBuildBomPages(t *testing.T) {
	sbNeedKicad(t)
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	out := filepath.Join(t.TempDir(), "bomdemo")
	spec, err := loadSbSpec("testdata/sch-build/bom.json")
	if err != nil {
		t.Fatal(err)
	}
	// an author's package text for the custom part: it must outlive an edit
	for i := range spec.Parts {
		if spec.Parts[i].Ref == "T1" {
			if spec.Parts[i].Fields == nil {
				spec.Parts[i].Fields = map[string]string{}
			}
			spec.Parts[i].Fields["Package"] = "EE16 THT"
		}
	}
	res := sbBomBuild(t, spec, sbOptions{OutDir: out})
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	// the report
	b := res.Bom
	if b == nil || !b.Sheets || !b.PackageText || len(b.Pages) != 2 {
		t.Fatalf("bom report %+v", b)
	}
	if b.Pages[0].File != "power-bom.kicad_sch" || b.Pages[0].No != 3 || b.Pages[0].Sheet != "Power BOM" ||
		b.Pages[1].File != "logic-bom.kicad_sch" || b.Pages[1].No != 5 {
		t.Errorf("bom pages %+v: want power-bom as page 3 and logic-bom as page 5 (root 1, power 2, logic 4)", b.Pages)
	}
	if strings.Join(b.NoPackage, ",") != "L1" {
		t.Errorf("noPackage = %v, want L1 (no footprint)", b.NoPackage)
	}
	miss := map[string]bool{}
	for _, m := range b.Missing {
		miss[m.Ref] = true
	}
	if !miss["L1"] || !miss["U2"] || !miss["D2"] || miss["T1"] || miss["U1"] || miss["R1"] {
		t.Errorf("bom.missing = %+v", b.Missing)
	}
	warned := false
	for _, w := range res.Warnings {
		warned = warned || strings.Contains(w, "bom.missing")
	}
	if !warned {
		t.Errorf("no warning about bom.missing: %q", res.Warnings)
	}
	// the page gates judge the two schematic pages only
	if len(res.Pages) != 2 {
		t.Errorf("report pages = %v, want the two schematic pages", res.Pages)
	}
	// package text on the page, BOM sheet: a table, no symbol
	power := read("power.kicad_sch")
	for _, want := range []string{`(property "Package" "0805"`, `(property "Package" "SOT-23-5"`} {
		if !strings.Contains(power, want) {
			t.Errorf("power.kicad_sch lacks %s", want)
		}
	}
	bomText := read("power-bom.kicad_sch")
	if strings.Contains(bomText, "(symbol ") || !strings.Contains(bomText, "(table") {
		t.Errorf("power-bom.kicad_sch: want a table and no symbol")
	}
	for _, want := range []string{"C1, C2", "custom / self-sourced", "AP2112K-3.3TRG1", "Spec / requirements"} {
		if !strings.Contains(bomText, want) {
			t.Errorf("power-bom.kicad_sch lacks %q", want)
		}
	}
	if _, fr, err := kicad.FitSheet(bomText); err != nil || fr.ShiftX != 0 || fr.ShiftY != 0 || fr.To != fr.From {
		t.Errorf("sch-fit would change the BOM sheet: %+v %v", fr, err)
	}
	// print order: page, its BOM, next page, its BOM
	root := filepath.Join(out, "bomdemo.kicad_sch")
	files, err := kicad.SheetFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range files {
		names = append(names, filepath.Base(f))
	}
	if want := "bomdemo.kicad_sch power.kicad_sch power-bom.kicad_sch logic.kicad_sch logic-bom.kicad_sch"; strings.Join(names, " ") != want {
		t.Errorf("sheets = %v, want %s", names, want)
	}
	pdf, err := runSchPdf(root, filepath.Join(t.TempDir(), "bomdemo.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if pdf.Pages != 5 || pdf.Bytes == 0 {
		t.Errorf("pdf: %d page(s), %d bytes; want 5 pages", pdf.Pages, pdf.Bytes)
	}
	// reading the project back: the schematic pages, no BOM page, no
	// Package field leaking into the spec
	back, err := readSbSpec(out, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(back.Pages) != 2 || len(back.Parts) != len(spec.Parts) {
		t.Errorf("read back: %d page(s), %d part(s)", len(back.Pages), len(back.Parts))
	}
	for _, p := range back.Parts {
		if _, ok := p.Fields["Package"]; ok {
			t.Errorf("read back: %s carries the drawn Package text as a spec field", p.Ref)
		}
	}
	// in-place rebuild: same uuids, same BOM sheets
	st, _ := loadSbState(out)
	if st.PageUUIDs["power#bom#1"] == "" || st.SheetSymUUIDs["logic#bom#1"] == "" {
		t.Fatalf("state has no BOM sheet uuids: %v %v", st.PageUUIDs, st.SheetSymUUIDs)
	}
	logicBom := read("logic-bom.kicad_sch")
	sbBomBuild(t, spec, sbOptions{OutDir: out})
	st2, _ := loadSbState(out)
	for name, pair := range map[string][2]map[string]string{"page": {st.PageUUIDs, st2.PageUUIDs}, "sheet symbol": {st.SheetSymUUIDs, st2.SheetSymUUIDs}, "symbol": {st.SymUUIDs, st2.SymUUIDs}} {
		for k, v := range pair[0] {
			if pair[1][k] != v {
				t.Errorf("%s uuid of %s changed on an in-place rebuild", name, k)
			}
		}
	}
	if read("power-bom.kicad_sch") != bomText || read("logic-bom.kicad_sch") != logicBom {
		t.Errorf("an in-place rebuild rewrote a BOM sheet differently")
	}
	// an unrelated sch-edit keeps the author's package text (it rebuilds
	// from the stored spec, not from what is drawn)
	er, err := runSchEdit(sbOptions{OutDir: out, Jobs: 4, PlannerBudget: 20000}, []sbOp{{Op: "set_value", Ref: "R1", Value: "2k2"}})
	if err != nil || er == nil || !er.OK {
		t.Fatalf("sch-edit: %+v %v", er, err)
	}
	edited := read("power.kicad_sch") + read("logic.kicad_sch")
	if !strings.Contains(edited, `(property "Package" "EE16 THT"`) {
		t.Errorf("sch-edit dropped the fields.Package override")
	}
	if !strings.Contains(read("power-bom.kicad_sch")+read("logic-bom.kicad_sch"), "EE16 THT") {
		t.Errorf("sch-edit dropped the override from the BOM sheet")
	}
	// both off: no BOM sheet, no package text; the state remembers it
	res3 := sbBomBuild(t, spec, sbOptions{OutDir: out, NoBomPages: true, NoPackageText: true})
	if res3.Bom == nil || res3.Bom.Sheets || res3.Bom.PackageText || len(res3.Bom.Pages) != 0 {
		t.Errorf("flags off: bom report %+v", res3.Bom)
	}
	if len(res3.Bom.Missing) == 0 {
		t.Errorf("flags off: bom.missing is still reported")
	}
	for _, f := range []string{"power-bom.kicad_sch", "logic-bom.kicad_sch"} {
		if fileExists(filepath.Join(out, f)) {
			t.Errorf("--no-bom-pages left %s behind", f)
		}
	}
	// (the author's fields.Package stays, as the hidden field it is)
	if strings.Contains(read("power.kicad_sch"), `(property "Package" "0805"`) {
		t.Errorf("--no-package-text left a Package property")
	}
	if strings.Contains(read("bomdemo.kicad_sch"), "-bom.kicad_sch") {
		t.Errorf("--no-bom-pages left a BOM sheet symbol on the root")
	}
	if st3, _ := loadSbState(out); !st3.NoBomPages || !st3.NoPackageText {
		t.Errorf("state does not remember the flags (kicad sch-edit would bring the BOM sheets back)")
	}
}

func TestSchBuildBomSinglePage(t *testing.T) {
	sbNeedKicad(t)
	t.Setenv(kicad.EnvCacheDir, t.TempDir())
	out := filepath.Join(t.TempDir(), "one")
	spec, err := loadSbSpec("testdata/sch-build/bom.json")
	if err != nil {
		t.Fatal(err)
	}
	// one page: the schematic is the root sheet, its BOM hangs off it
	spec.Pages = nil
	for i := range spec.Zones {
		spec.Zones[i].Page = ""
	}
	res := sbBomBuild(t, spec, sbOptions{OutDir: out})
	if res.Bom == nil || len(res.Bom.Pages) != 1 {
		t.Fatalf("bom report %+v", res.Bom)
	}
	pg := res.Bom.Pages[0]
	if pg.File != "bomdemo-bom.kicad_sch" || pg.No != 2 || pg.Sheet != "BOM" {
		t.Errorf("bom page %+v, want bomdemo-bom.kicad_sch as page 2, sheet BOM", pg)
	}
	files, err := kicad.SheetFiles(filepath.Join(out, "bomdemo.kicad_sch"))
	if err != nil || len(files) != 2 || filepath.Base(files[1]) != "bomdemo-bom.kicad_sch" {
		t.Errorf("sheets %v %v", files, err)
	}
}

func TestSchPdfNeedsARoot(t *testing.T) {
	// the output never replaces a project file
	dir := t.TempDir()
	root := filepath.Join(dir, "p.kicad_sch")
	if err := os.WriteFile(root, []byte("(kicad_sch)"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runSchPdf(root, root); err == nil || !strings.Contains(err.Error(), ".pdf") {
		t.Errorf("--out naming the root schematic: %v", err)
	}
	if b, _ := os.ReadFile(root); string(b) != "(kicad_sch)" {
		t.Errorf("the root schematic was touched")
	}
	if _, err := schPdfRoot("", ""); err == nil {
		t.Errorf("no --project and no --sch must be an error")
	}
	if _, err := schPdfRoot(t.TempDir(), ""); err == nil {
		t.Errorf("a directory without a project must be an error")
	}
}
