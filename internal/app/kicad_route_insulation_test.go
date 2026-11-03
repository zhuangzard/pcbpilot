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
	out, n := dsnInsulationRules(insulationDSN, iso, 5.08)
	if n != 1 || strings.Count(out, "class_class") != 1 {
		t.Fatalf("n=%d\n%s", n, out)
	}
	if !strings.Contains(out, `(class_class (classes "PP1_W8+PPD_domain_a+Default" "PP5_W10+PPD_domain_b+Default") (rule (clearance 8005.08)))`) {
		t.Fatalf("rule:\n%s", out)
	}
	if strings.Index(out, "class_class") > strings.Index(out, "(wiring") {
		t.Fatalf("outside the network section:\n%s", out)
	}
	if got, n := dsnInsulationRules(insulationDSN, nil, 5.08); got != insulationDSN || n != 0 {
		t.Fatalf("no pairs must not change the DSN: %d", n)
	}
	if got, n := dsnInsulationRules("(pcb t)", iso, 5.08); got != "(pcb t)" || n != 0 {
		t.Fatalf("no network section: %d", n)
	}
}
