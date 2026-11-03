package app

import (
	"strings"
	"testing"
)

const insulationDSN = `(pcb t
  (network
    (net A (pins U1-1))
    (class "PP1_W8+PPD_domain_a+Default" A
      (rule (width 200) (clearance 205.08)))
    (class "PP5_W10+PPD_domain_b+Default" B
      (rule (width 254) (clearance 205.08)))
    (class "PP1_W8+Default" C
      (rule (width 200) (clearance 205.08)))
  )
  (wiring)
)`

func TestDSNInsulationRules(t *testing.T) {
	iso := []map[string]any{{"a": "domain:a", "b": "domain:b", "clearanceMm": 8.0}}
	out, n, err := dsnInsulationRules(insulationDSN, iso, 5.08)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || strings.Count(out, "class_class") != 1 {
		t.Fatalf("n=%d\n%s", n, out)
	}
	if !strings.Contains(out, `(class_class (classes "PP1_W8+PPD_domain_a+Default" "PP5_W10+PPD_domain_b+Default") (rule (clearance 8005.08)))`) {
		t.Fatalf("rule:\n%s", out)
	}
	if strings.Index(out, "class_class") > strings.Index(out, "(wiring") {
		t.Fatalf("outside the network section:\n%s", out)
	}
	if got, n, err := dsnInsulationRules(insulationDSN, nil, 5.08); got != insulationDSN || n != 0 || err != nil {
		t.Fatalf("no pairs must not change the DSN: %d %v", n, err)
	}
	// A pair below a class's own clearance keeps the larger one.
	small := []map[string]any{{"a": "domain:a", "b": "domain:b", "clearanceMm": 0.1}}
	if out, _, _ := dsnInsulationRules(insulationDSN, small, 5.08); !strings.Contains(out, "(rule (clearance 205.08))") || strings.Contains(out, "(clearance 105.08)") {
		t.Fatalf("class clearance lost:\n%s", out)
	}
	// "(network" inside a quoted name is not the section.
	quoted := strings.Replace(insulationDSN, "(pcb t", `(pcb "x (network y"`, 1)
	out, n, err = dsnInsulationRules(quoted, iso, 5.08)
	if err != nil || n != 1 || strings.Index(out, "class_class") < strings.Index(out, "PP5_W10") {
		t.Fatalf("quoted name: %d %v\n%s", n, err, out)
	}
	// Rules that are needed but cannot be written fail.
	broken := strings.Replace(insulationDSN, "(network", "(netwrk", 1)
	if _, _, err := dsnInsulationRules(broken, iso, 5.08); err == nil {
		t.Fatal("no network section must be an error")
	}
}

func TestDSNInsulationClassOnBothSides(t *testing.T) {
	iso := []map[string]any{{"a": "domain:a", "b": "domain:b", "clearanceMm": 8.0}}
	both := `(pcb t (network (class "PP1+PPD_domain_a+PPD_domain_b" A (rule (clearance 200)))) (wiring))`
	if _, _, err := dsnInsulationRules(both, iso, 5.08); err == nil {
		t.Fatal("a class on both sides must be an error")
	}
}

// Every round, resumed ones too, keeps pcbpilot's own arguments and the choice
// about --diagnose, with the extra arguments last.
func TestFastrouteArgsExtraAndDiagnose(t *testing.T) {
	o := fastrouteOpts{noDiagnose: true, extraArgs: []string{"--no-time-limits"}}
	for _, initial := range []string{"", "prev.ses"} {
		args := fastrouteArgs(o, "d.dsn", "o.ses", "r.json", initial)
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "--diagnose") || args[len(args)-1] != "--no-time-limits" || !strings.Contains(joined, "-do o.ses") {
			t.Fatalf("initial %q: %v", initial, args)
		}
		if initial != "" && !strings.Contains(joined, "--initial-session=prev.ses") {
			t.Fatalf("resume lost: %v", args)
		}
	}
	if args := strings.Join(fastrouteArgs(fastrouteOpts{}, "d.dsn", "o.ses", "r.json", ""), " "); !strings.Contains(args, "--diagnose") {
		t.Fatalf("--diagnose is the default: %s", args)
	}
}

func TestCheckRouterArgs(t *testing.T) {
	for _, bad := range []string{"--initial-session=x.ses", "-do=x", "--report=x", "--max-time=9", "--diagnose", "x.ses", "--router.autorouter.max_threads=8"} {
		if err := checkRouterArgs([]string{bad}); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if err := checkRouterArgs([]string{"--no-time-limits", "--router.autorouter.ignore_net_classes=A,B"}); err != nil {
		t.Error(err)
	}
}
