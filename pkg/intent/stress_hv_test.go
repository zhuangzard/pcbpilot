package intent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

// Regressions from the high-voltage stress suite (testdata/stress/hv, run in
// full by `make stress-hv`). These use the real standards tables through the
// same adapter the CLI installs.

const stressDir = "../../testdata/stress/hv"

func withSafety(t *testing.T) {
	t.Helper()
	old := SafetyProvider
	SafetyProvider = func(p Pair, st Standard) (float64, float64, bool, float64, string, []string) {
		r := safety.Distances(safety.Pair{A: p.A, B: p.B, WorkingVrms: p.WorkingVrms, WorkingVpeak: p.WorkingVpeak,
			Insulation: p.Insulation, MOP: p.MOP, MOPCount: p.MOPCount, Transient: p.Transient, MainsVrms: p.MainsVrms},
			safety.Standard{Name: st.Name, Insulation: st.Insulation, MOP: st.MOP, MOPCount: st.MOPCount,
				PollutionDegree: st.PollutionDegree, MaterialGroup: st.MaterialGroup, AltitudeM: st.AltitudeM,
				OvervoltageCategory: st.OvervoltageCategory, Coated: st.Coated})
		return r.ClearanceMm, r.CreepageMm, r.SlotRequired, r.SlotWidthMm, r.Ref, r.Why
	}
	t.Cleanup(func() { SafetyProvider = old })
}

func stressIntent(t *testing.T, name, spec string, mutate func(d *Input)) *Intent {
	t.Helper()
	dir := filepath.Join(stressDir, name)
	d := loadDesign(t, dir, []string{"connectivity.json"})
	sb, err := os.ReadFile(filepath.Join(dir, spec))
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ParseSpec(sb)
	if err != nil {
		t.Fatal(err)
	}
	in := Input{Design: d, Libs: libs(t, filepath.Join(dir, "models.json")), Spec: sp}
	if mutate != nil {
		mutate(&in)
	}
	it, err := Derive(in)
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func onlyPair(t *testing.T, it *Intent) *Pair {
	t.Helper()
	if len(it.Pairs) != 1 {
		t.Fatalf("pairs %+v", it.Pairs)
	}
	return it.Pairs[0]
}

// Flyback: the rectified primary is one mains-connected domain with the
// line (not a second domain "insulated" from it by its own bridge
// rectifier); the pair to the secondary takes the mains transient from the
// 230 V line (not from the 325 V bulk) and the working voltage from the bulk
// and drain; the Y capacitor is a listed bridge; low-voltage primary nets are
// not in the HV class; a voltage-only rail keeps its simulated current.
func TestStressFlybackIntent(t *testing.T) {
	withSafety(t)
	it := stressIntent(t, "flyback", "spec.json", nil)
	if len(it.Domains) != 2 {
		t.Fatalf("domains %+v", it.Domains)
	}
	p := onlyPair(t, it)
	if p.Insulation != "reinforced" || p.ClearanceMm != 3.0 || p.CreepageMm != 6.6 || p.Transient != "mains" || p.MainsVrms != 230 {
		t.Fatalf("pair %+v", p)
	}
	hasAll(t, "bridges", p.Bridges, "T1", "U2", "C8")
	if d := it.Nets["PGND"].Domain; d != it.Nets["L"].Domain || d != it.Nets["HV_BULK"].Domain || !strings.HasPrefix(d, "MAINS_230VAC") {
		t.Fatalf("primary not merged with the line: PGND %s L %s", d, it.Nets["L"].Domain)
	}
	near(t, "domain Vpeak", it.domainOf(t, "PGND").WorkingVpeak, 650, 0.1)
	if c := it.Nets["PGND"].NetClass; c != "GND_MAINS_230VAC" {
		t.Fatalf("PGND class %s", c)
	}
	if c := it.Nets["VCC_P"].NetClass; strings.HasPrefix(c, "HV_") {
		t.Fatalf("15 V VCC_P in class %s", c)
	}
	if c := it.Nets["DRAIN"].NetClass; !strings.HasPrefix(c, "HV_") || it.Nets["DRAIN"].Role != "switch" {
		t.Fatalf("DRAIN %s / %s (a flyback drain is a switch node, not 'analog')", c, it.Nets["DRAIN"].Role)
	}
	near(t, "VOUT current (voltage-only rail)", it.Nets["VOUT"].CurrentA, 2.0, 0.01)
	for _, f := range it.Findings {
		if f.Severity == "error" || f.Kind == "declared-below-sim" {
			t.Fatalf("finding %+v", f)
		}
		if f.Kind == "cap-voltage" && strings.Contains(f.Message, "MLCC") {
			t.Fatalf("electrolytic called an MLCC: %s", f.Message)
		}
	}
	// 5000 m: clearance × 1.48, creepage unchanged.
	it5 := stressIntent(t, "flyback", "spec-5000m.json", nil)
	if p := onlyPair(t, it5); p.ClearanceMm != 4.44 || p.CreepageMm != 6.6 {
		t.Fatalf("5000 m pair %+v", p)
	}
}

func (it *Intent) domainOf(t *testing.T, net string) *Domain {
	t.Helper()
	for _, d := range it.Domains {
		if d.ID == it.Nets[net].Domain {
			return d
		}
	}
	t.Fatalf("no domain for %s", net)
	return nil
}

// Medical: the product's declared 2 × MOPP applies to the patient barrier
// (it was hard-coded to 1 MOPP = 4 mm / 2.5 mm); 1 × MOOP stays basic.
func TestStressMedicalIntent(t *testing.T) {
	withSafety(t)
	p := onlyPair(t, stressIntent(t, "medical", "spec.json", nil))
	if p.MOP != "MOPP" || p.MOPCount != 2 || p.Insulation != "double" || p.CreepageMm != 8 || p.ClearanceMm != 5 {
		t.Fatalf("2×MOPP pair %+v", p)
	}
	hasAll(t, "bridges", p.Bridges, "T1", "U3")
	p = onlyPair(t, stressIntent(t, "medical", "spec-moop.json", nil))
	if p.MOP != "MOOP" || p.MOPCount != 1 || p.Insulation != "basic" || p.CreepageMm != 2.5 || p.ClearanceMm != 1.5 {
		t.Fatalf("1×MOOP pair %+v", p)
	}
}

// CAT III 600 V: the declared mains-kind measuring domain takes its 600 V
// (not 230 V) as the Table F.1 row; the divider nodes carry their own peak
// voltages and classes; a chain resistor over its working voltage is an error.
func TestStressCat3Intent(t *testing.T) {
	withSafety(t)
	it := stressIntent(t, "cat3", "spec.json", nil)
	p := onlyPair(t, it)
	if p.Transient != "mains" || p.MainsVrms != 600 || p.ClearanceMm != 11 || p.CreepageMm != 12 {
		t.Fatalf("pair %+v", p)
	}
	near(t, "N3", it.Nets["N3"].Voltage.Max, 424.55, 0.5)
	if c := it.Nets["ADC_IN"].NetClass; strings.HasPrefix(c, "HV_") || it.Nets["ADC_IN"].ClearanceMil > 10 {
		t.Fatalf("0.7 V ADC input: class %s clearance %.1f", c, it.Nets["ADC_IN"].ClearanceMil)
	}
	for _, f := range it.Findings {
		if f.Kind == "resistor-voltage" {
			t.Fatalf("chain within rating flagged: %s", f.Message)
		}
	}
	// A 3 MΩ top resistor takes 3/8 of 848 V = 318 V > 200 V.
	bad := stressIntent(t, "cat3", "spec.json", func(in *Input) {
		for _, p := range in.Design.Parts {
			if p.Ref == "R1" {
				p.Value = "3MΩ"
			}
		}
	})
	found := false
	for _, f := range bad.Findings {
		found = found || f.Kind == "resistor-voltage" && f.Severity == "error" && has(f.Refs, "R1")
	}
	if !found {
		t.Fatal("over-voltage chain resistor not reported")
	}
}

// Inverter: an isolated gate driver (UCC21520) and isolated bias modules
// (MGJ2) are bridges — unrecognised they were SELV parts and the gate drive
// became "touchable"; the declared transient (400 V DC bus, OVC II) sets the
// clearance; the 30 A bus return does not share a class with the SELV ground.
func TestStressInverterIntent(t *testing.T) {
	withSafety(t)
	it := stressIntent(t, "inverter", "spec.json", nil)
	p := onlyPair(t, it)
	// Working voltage 465 V: the high-side gate supply rides 15 V above the
	// 450 V bus (floating island) → creepage 2 × 4.7 = 9.4 mm.
	if p.ClearanceMm != 5.5 || p.CreepageMm != 9.4 || p.Transient != "mains" || p.MainsVrms != 400 {
		t.Fatalf("pair %+v", p)
	}
	hasAll(t, "bridges", p.Bridges, "U1", "U2", "U3")
	if it.Nets["KS_H"].Domain != it.Nets["HV+"].Domain {
		t.Fatalf("gate drive outside the HV domain: KS_H %s", it.Nets["KS_H"].Domain)
	}
	if it.Nets["HV_GND"].NetClass == it.Nets["GND"].NetClass {
		t.Fatalf("HV_GND and GND share class %s", it.Nets["GND"].NetClass)
	}
	// The high-side gate drive rides on the switch node: its copper swings
	// to the bus, the gate loop is 15 V relative.
	for _, n := range []string{"KS_H", "G_H", "OUTA", "VDDA"} {
		np := it.Nets[n]
		if np.FloatsOn != "PHASE" || np.Voltage.Peak < 520 || np.RelVoltage == nil || np.RelVoltage.Peak > 16 {
			t.Fatalf("%s: floatsOn %q peak %.1f rel %+v", n, np.FloatsOn, np.Voltage.Peak, np.RelVoltage)
		}
	}
	for _, f := range it.Findings {
		if f.Severity == "error" {
			t.Fatalf("finding %+v", f)
		}
	}
	pf := onlyPair(t, stressIntent(t, "inverter", "spec-functional.json", nil))
	if pf.Insulation != "functional" || pf.ClearanceMm != 2.67 || pf.CreepageMm != 2.67 {
		t.Fatalf("functional pair %+v", pf)
	}
}
