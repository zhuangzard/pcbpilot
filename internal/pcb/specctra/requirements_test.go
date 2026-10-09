package specctra

import (
	"strings"
	"testing"
)

// Requirements as written by `intent derive` for the Gas Module V5 board
// (pre-intent.json): +12V forbids neck-down, GND may neck to 10 mil.
func gasReqs() map[string]NetRequirement {
	return map[string]NetRequirement{
		"+12V":    {OuterMil: 21.65, InnerMil: 41.34, MinMil: 21.65, ClearanceMil: 6},
		"GND":     {OuterMil: 21.65, InnerMil: 43.31, MinMil: 10, ClearanceMil: 6},
		"SV1_DRV": {OuterMil: 10, InnerMil: 10, MinMil: 10, ClearanceMil: 6},
	}
}

func fixedFixture(t *testing.T) string {
	t.Helper()
	out, _, err := FixDSN(readFixture(t, "easyeda-export.dsn"), FixOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestCheckNetRequirements_FindsInnerShortfall(t *testing.T) {
	short, err := CheckNetRequirements(fixedFixture(t), gasReqs())
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(short, "\n")
	for _, want := range []string{"+12V: Inner1 width 21.65 < 41.34 mil", "GND: Inner2 width 21.65 < 43.31 mil", "SV1_DRV: no net class in the DSN"} {
		if !strings.Contains(joined, want) {
			t.Errorf("shortfalls lack %q:\n%s", want, joined)
		}
	}
}

func TestApplyNetRequirements(t *testing.T) {
	out, rep, err := ApplyNetRequirements(fixedFixture(t), gasReqs())
	if err != nil {
		t.Fatal(err)
	}
	short, err := CheckNetRequirements(out, gasReqs())
	if err != nil || len(short) != 0 {
		t.Fatalf("after apply still short: %v %v", short, err)
	}
	if rep.Classes != 2 || rep.NewClasses != 1 {
		t.Errorf("report = %+v", rep)
	}
	// +12V and GND sit above the 10 mil floor (GND's widthMil.min < outer no
	// longer lets it neck down: fastroute's micro neck-down is not limited to
	// pins); SV1_DRV (new class) is already at the floor.
	if strings.Join(rep.NoNeckdown, ",") != "+12V,GND" {
		t.Errorf("NoNeckdown = %v", rep.NoNeckdown)
	}
	if rep.MinTraceMil != 10 {
		t.Errorf("MinTraceMil = %v", rep.MinTraceMil)
	}
	if !strings.Contains(out, `(layer_rule Inner1 Inner2 (rule (width 41.34)))`) {
		t.Error("inner layer_rule for +12V missing")
	}
	// Values already above the requirement are kept; the result parses and
	// a second pass changes nothing.
	if _, err := parseSexpr(out); err != nil {
		t.Fatal(err)
	}
	again, rep2, err := ApplyNetRequirements(out, gasReqs())
	if err != nil || again != out || rep2.Classes != 0 || rep2.NewClasses != 0 {
		t.Errorf("second pass not a no-op: %+v %v", rep2, err)
	}
}

func TestAddClearanceMargin(t *testing.T) {
	in := "(rule(clear 6.03))\n(rule(clear 6.03 (type smd_smd)))\n(rule \n (width 21.65)\n (clearance 6)\n)"
	out := AddClearanceMargin(in, 0.2)
	for _, want := range []string{"(rule(clear 6.23))", "(rule(clear 6.23 (type smd_smd)))", "(clearance 6.2)", "(width 21.65)"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output lacks %q:\n%s", want, out)
		}
	}
}
