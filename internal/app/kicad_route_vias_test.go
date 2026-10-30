package app

import (
	"math"
	"strings"
	"testing"
)

const testViaDSN = `(pcb x (structure (layer F.Cu) (via "Via[0-5]_800:200_um" "Via[0-5]_700:400_um" "Via[0-5]_600:300_um")
 (rule (width 200)))
  (library
    (padstack "Via[0-5]_600:300_um"
      (shape (circle F.Cu 600))
      (shape (circle B.Cu 600))
      (attach off)
    )
    (padstack "Via[0-5]_800:200_um"
      (shape (circle F.Cu 800))
      (shape (circle B.Cu 800))
      (attach off)
    )
    (padstack "Via[0-5]_700:400_um"
      (shape (circle F.Cu 700))
      (shape (circle B.Cu 700))
      (attach off)
    )
  ))`

// Mixed classes 0.8/0.2 and 0.7/0.4: fastroute's padstack is 0.8/0.4 (the
// largest diameter AND the largest drill), added and listed first.
func TestDSNRouterViaComponentwiseMax(t *testing.T) {
	out, order, top := dsnRouterVia(testViaDSN)
	if top == nil || top.dia != 800 || top.drill != 400 || top.name != `"Via[0-5]_800:400_um"` {
		t.Fatalf("router via %+v", top)
	}
	if len(order) != 1 || order[0] != top.name {
		t.Fatalf("order %v", order)
	}
	if !strings.Contains(out, `(via "Via[0-5]_800:400_um")`) || !strings.Contains(out, `(padstack "Via[0-5]_700:400_um"`) {
		t.Fatalf("via list:\n%s", out)
	}
	at := strings.Index(out, `(padstack "Via[0-5]_800:400_um"`)
	if at < 0 || !strings.Contains(out[at:at+200], "(circle F.Cu 800)") || !strings.Contains(out[at:at+200], "(circle B.Cu 800)") {
		t.Fatalf("padstack:\n%s", out)
	}
	if strings.Count(out, `(padstack "Via[0-5]_800:200_um"`) != 1 {
		t.Fatal("source padstack changed")
	}
	// The maximum already exists: no new padstack, it moves first.
	have := strings.Replace(testViaDSN, `"Via[0-5]_700:400_um"`, `"Via[0-5]_800:400_um"`, -1)
	have = strings.Replace(have, "(circle F.Cu 700)", "(circle F.Cu 800)", 1)
	out, order, top = dsnRouterVia(have)
	if top.name != `"Via[0-5]_800:400_um"` || order[0] != top.name || strings.Count(out, `(padstack "Via[0-5]_800:400_um"`) != 1 {
		t.Fatalf("existing max: %v %+v", order, top)
	}
	// An unreadable size leaves the DSN alone.
	odd := `(pcb x (structure (via "MyVia" "Via[0-5]_600:300_um")))`
	if got, o, v := dsnRouterVia(odd); got != odd || o != nil || v != nil {
		t.Fatalf("changed %s", got)
	}
}

// Every class via is reached by shrinking the componentwise maximum, with
// its own annular ring (1.0/0.5 → 1.0/0.2 = 0.4 mm ring, → 0.8/0.5 = 0.15
// mm); a via that cannot shrink to its class, or a net without a class via,
// is a problem, never skipped.
func TestClassViaPlan(t *testing.T) {
	mil := func(um float64) float64 { return um / 25.4 }
	class := map[string][2]int64{"A": {1000000, 200000}, "B": {800000, 500000}, "D": {1000000, 500000}}
	eff := func(net string) (int64, int64, bool) {
		c, ok := class[net]
		return c[0], c[1], ok
	}
	vias := []routeVia{
		{ID: "a", Net: "A", Dia: mil(1000), Drill: mil(500)},
		{ID: "b", Net: "B", Dia: mil(1000), Drill: mil(500)},
		{ID: "d", Net: "D", Dia: mil(1000), Drill: mil(500)}, // already its class
		{ID: "x", Net: "B", Dia: mil(1000), Drill: mil(400)}, // drill below the class: unreachable
		{ID: "u", Net: "NOCLASS", Dia: mil(1000), Drill: mil(500)},
		{ID: "old", Net: "A", Dia: mil(600), Drill: mil(300)}, // not a router via
	}
	ops, fail, unknown, n := classViaPlan(vias, map[string]bool{"old": true}, eff)
	if n != 5 || len(ops) != 2 || len(fail) != 1 || len(unknown) != 1 {
		t.Fatalf("ops %v fail %v unknown %v n %d", ops, fail, unknown, n)
	}
	for _, op := range ops {
		c := class[map[string]string{"a": "A", "b": "B"}[op["id"].(string)]]
		dia, drill := op["diameter"].(float64)*25400, op["drill"].(float64)*25400
		if math.Abs(dia-float64(c[0])) > 1 || math.Abs(drill-float64(c[1])) > 1 {
			t.Fatalf("op %v != class %v", op, c)
		}
		if ring := (dia - drill) / 2; ring < 149999 {
			t.Fatalf("ring %v", ring)
		}
	}
	if !strings.Contains(fail[0], "cannot be shrunk") || !strings.Contains(unknown[0], "NOCLASS") {
		t.Fatalf("%v %v", fail, unknown)
	}
}
