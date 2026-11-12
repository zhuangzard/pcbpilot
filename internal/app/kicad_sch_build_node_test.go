package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// sbNodeAmpLib is a 16-pin + pad amplifier symbol as vendor libraries draw
// a small QFN: four pins a side, 2.54 mm apart, half a pitch off the
// 2.54 mm grid (a wire from such a pin to a part on the grid has a bend).
func sbNodeAmpLib() string {
	var b strings.Builder
	b.WriteString(`(kicad_symbol_lib (version 20251024) (generator "test")
	(symbol "AMP"
		(exclude_from_sim no) (in_bom yes) (on_board yes)
		(property "Reference" "U" (at 0 15.24 0) (effects (font (size 1.27 1.27))))
		(property "Value" "AMP" (at 0 -15.24 0) (effects (font (size 1.27 1.27))))
		(symbol "AMP_0_1" (rectangle (start -7.62 10.16) (end 7.62 -10.16) (stroke (width 0.254) (type default)) (fill (type background))))
		(symbol "AMP_1_1"
`)
	pin := func(num int, name, typ string, x, y float64, rot int) {
		fmt.Fprintf(&b, "\t\t\t(pin %s line (at %g %g %d) (length 2.54) (name %q (effects (font (size 1.27 1.27)))) (number \"%d\" (effects (font (size 1.27 1.27)))))\n", typ, x, y, rot, name, num)
	}
	off := []float64{3.81, 1.27, -1.27, -3.81}
	for i, n := range []string{"OUT-", "IN+", "IN-", "OUT+"} {
		pin(1+i, n, map[bool]string{true: "output", false: "input"}[i == 0 || i == 3], -10.16, off[i], 0)
	}
	for i := 0; i < 4; i++ {
		pin(5+i, "VS+", "power_in", -off[i], -12.7, 90)
	}
	for i, n := range []string{"VOCM", "OUT+", "OUT-", "PD"} {
		pin(9+i, n, map[bool]string{true: "output", false: "input"}[i == 1 || i == 2], 10.16, -off[i], 180)
	}
	for i := 0; i < 4; i++ {
		pin(13+i, "VS-", "power_in", off[i], 12.7, 270)
	}
	pin(17, "EP", "passive", 6.35, 12.7, 270)
	b.WriteString("\t\t)\n\t)\n)\n")
	return b.String()
}

// sbNodeSpec: a differential amplifier front end. One 17-pin IC, eleven
// resistors, seven capacitors and two three-pin clamp diodes. CH1_XP / CH1_XN have
// five members each (the input resistor's far end, a filter capacitor, a clamp
// diode, the feedback resistor and the series resistor into the IC) and CH1_CT
// four; none of them is on an IC pin — second-level nodes, reached only
// through a part that hangs on the IC.
func sbNodeSpec(lib string) *sbSpec {
	two := []sbPin{{Number: "1", Name: "1", Type: "passive"}, {Number: "2", Name: "2", Type: "passive"}}
	three := []sbPin{{Number: "1", Name: "A", Type: "passive"}, {Number: "2", Name: "K", Type: "passive"}, {Number: "3", Name: "C", Type: "passive"}}
	s := &sbSpec{
		Name:  "node",
		Zones: []sbZone{{ID: "afe", Title: "AFE"}, {ID: "ext", Title: "EXT"}},
		Parts: []sbPart{{Ref: "U1", Value: "AMP", Symbol: lib + ":AMP", Zone: "afe", Pins: []sbPin{
			{Number: "1", Name: "OUT-", Type: "output"}, {Number: "2", Name: "IN+", Type: "input"},
			{Number: "3", Name: "IN-", Type: "input"}, {Number: "4", Name: "OUT+", Type: "output"},
			{Number: "5", Name: "VS+", Type: "power_in"}, {Number: "6", Name: "VS+", Type: "power_in"},
			{Number: "7", Name: "VS+", Type: "power_in"}, {Number: "8", Name: "VS+", Type: "power_in"},
			{Number: "9", Name: "VOCM", Type: "input"}, {Number: "10", Name: "OUT+", Type: "output"},
			{Number: "11", Name: "OUT-", Type: "output"}, {Number: "12", Name: "PD", Type: "input"},
			{Number: "13", Name: "VS-", Type: "power_in"}, {Number: "14", Name: "VS-", Type: "power_in"},
			{Number: "15", Name: "VS-", Type: "power_in"}, {Number: "16", Name: "VS-", Type: "power_in"},
			{Number: "17", Name: "EP", Type: "passive"}}},
			{Ref: "J1", Value: "CONN", Zone: "ext", Pins: []sbPin{
				{Number: "1", Name: "1", Type: "passive"}, {Number: "2", Name: "2", Type: "passive"},
				{Number: "3", Name: "3", Type: "passive"}, {Number: "4", Name: "4", Type: "passive"},
				{Number: "5", Name: "5", Type: "passive"}, {Number: "6", Name: "6", Type: "passive"}}}},
		Nets: []sbNet{
			{Name: "+3V3", Kind: "power", Pins: []string{"U1:5", "U1:6", "U1:7", "U1:8", "U1:12", "D1:2", "D2:2", "C5:1", "C6:1"}},
			{Name: "GND", Kind: "ground", Pins: []string{"U1:13", "U1:14", "U1:15", "U1:16", "U1:17", "D1:1", "D2:1", "C1:2", "C5:2", "C6:2", "J1:6"}},
			{Name: "CH1_IN_P", Pins: []string{"R1:1", "R4:1", "J1:1"}},
			{Name: "CH1_IN_N", Pins: []string{"R2:1", "R5:1", "J1:2"}},
			{Name: "ADC1_AP", Pins: []string{"R10:2", "C7:1", "J1:3"}},
			{Name: "ADC1_AN", Pins: []string{"R11:2", "C7:2", "J1:4"}},
			{Name: "ADC1_VREF", Pins: []string{"R3:2", "U1:9", "J1:5"}},
			{Name: "CH1_CT", Pins: []string{"R1:2", "R2:2", "C1:1", "R3:1"}},
			{Name: "CH1_XP", Pins: []string{"R4:2", "C2:1", "D1:3", "R6:1", "R8:1"}},
			{Name: "CH1_XN", Pins: []string{"R5:2", "C2:2", "D2:3", "R7:1", "R9:1"}},
			{Name: "CH1_OUTN", Pins: []string{"R6:2", "C3:2", "U1:1", "U1:11", "R11:1"}},
			{Name: "CH1_OUTP", Pins: []string{"R7:2", "C4:2", "U1:4", "U1:10", "R10:1"}},
			{Name: "CH1_INP", Pins: []string{"R8:2", "C3:1", "U1:2"}},
			{Name: "CH1_INN", Pins: []string{"R9:2", "C4:1", "U1:3"}},
		},
	}
	for _, r := range []string{"R1", "R2", "R3", "R4", "R5", "R6", "R7", "R8", "R9", "R10", "R11"} {
		s.Parts = append(s.Parts, sbPart{Ref: r, Value: "499", Zone: "afe", Pins: two})
	}
	for _, c := range []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7"} {
		s.Parts = append(s.Parts, sbPart{Ref: c, Value: "100n", Zone: "afe", Pins: two})
	}
	for _, dd := range []string{"D1", "D2"} {
		s.Parts = append(s.Parts, sbPart{Ref: dd, Value: "CLAMP", Zone: "afe", Pins: three})
	}
	return s
}

// Second-level nodes are drawn with wires: the zone as the build draws it
// (the best clean grid variant) joins most signal connections by wire, with
// no findings and inside the layout watchdog's limits.
func TestSbGridWiresSecondLevelNodes(t *testing.T) {
	lib := filepath.Join(t.TempDir(), "node.kicad_sym")
	if err := os.WriteFile(lib, []byte(sbNodeAmpLib()), 0o644); err != nil {
		t.Fatal(err)
	}
	d, _, err := buildDesign(sbNodeSpec(lib), sbResolveOpts{Offline: true})
	if err != nil {
		t.Fatalf("design: %v", err)
	}
	var zis []*sbZoneIn
	for _, zi := range d.zoneInputs() {
		if zi.Zone.ID == "afe" {
			zis = append(zis, zi)
		}
	}
	m := &sbMeasure{cache: map[string]kicad.SceneSymbol{}}
	for _, v := range sbGridVariants[:8] {
		zl, err := gridZone(d, zis[0], m, v)
		if err != nil {
			t.Logf("%s: %v", v.name(), err)
			continue
		}
		q := sbZoneQualityOf(d, zis[0], zl, m)
		t.Logf("%s: %.0f mm2, label share %.2f (%d/%d wired), attached %.2f, findings %d, unwired %v", v.name(), q.AreaMM2, q.LabelShare(), q.SigWired, q.SigConns, q.AttachedShare(), len(sbZoneFindings(d, zis[0], zl)), q.Unwired)
	}
	layouts, notes := layoutZones(d, zis, nil, sbLayoutOpts{NoPlanner: true, Jobs: 1})
	zl := layouts["afe"]
	if zl == nil {
		t.Fatalf("no layout: %v", notes)
	}
	q := sbZoneQualityOf(d, zis[0], zl, m)
	t.Logf("kept %s: label share %.2f (%d of %d by wire), attached %.2f", zl.Variant, q.LabelShare(), q.SigWired, q.SigConns, q.AttachedShare())
	// the plain compact variants alone keep 0.43 (16 of 28 by wire), attached
	// 0.94, on a drawing a third larger
	if q.LabelShare() > 0.40 {
		t.Errorf("signal label share %.2f > 0.40 (%d of %d connections by wire)", q.LabelShare(), q.SigWired, q.SigConns)
	}
	if q.AttachedShare() < 0.95 {
		t.Errorf("attached share %.2f < 0.95, not wired to another part: %v", q.AttachedShare(), q.Unwired)
	}
	if len(zl.Findings) > 0 {
		t.Errorf("findings: %v", zl.Findings)
	}
	if !strings.Contains(zl.Variant, "-node") {
		t.Errorf("kept %s: the node wiring is not the drawing kept", zl.Variant)
	}
	if strings.HasPrefix(zl.Variant, "legacy") {
		t.Errorf("kept %s: no compact variant drew the zone clean", zl.Variant)
	}
	if is := sbZoneLQIssues(q, sbLayoutQualityLimits); len(is) > 0 {
		t.Errorf("layout watchdog: %v", is)
	}
}
