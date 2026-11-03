package app

import (
	"strings"
	"testing"
)

const insulationDSN = `(pcb t
  (parser
    (string_quote ")
    (space_in_quoted_tokens on)
  )
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
	// Pairs with none of their domains in any class: nothing would protect them.
	if _, _, err := dsnInsulationRules(`(pcb t (parser (string_quote ")) (network (class "A" B (rule (clearance 1)))) (wiring))`, iso, 5.08); err == nil {
		t.Fatal("pairs and no class of their domains must be an error")
	}
	// Each pair on its own: one good pair does not cover another.
	two := []map[string]any{iso[0], {"a": "domain:c", "b": "domain:d", "clearanceMm": 8.0}}
	if _, _, err := dsnInsulationRules(insulationDSN, two, 5.08); err == nil {
		t.Fatal("a second pair with no classes must be an error")
	}
	oneSided := []map[string]any{{"a": "domain:a", "b": "domain:c", "clearanceMm": 8.0}}
	if _, _, err := dsnInsulationRules(insulationDSN, oneSided, 5.08); err == nil {
		t.Fatal("a pair with one side in no class must be an error")
	}
	// A side the intent gives no net has nothing to protect.
	empty := []map[string]any{{"a": "domain:a", "b": "domain:c", "aNets": []string{"A"}, "bNets": []string{}, "clearanceMm": 8.0}}
	if got, n, err := dsnInsulationRules(insulationDSN, empty, 5.08); err != nil || n != 0 || got != insulationDSN {
		t.Fatalf("empty side: %d %v", n, err)
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

func TestWidenStepsBackFromShorts(t *testing.T) {
	for rule, want := range map[string]bool{"clearance": true, "shorting_items": true, "track_width": false} {
		if widenStepsBackFor(rule) != want {
			t.Errorf("%s: want %v", rule, want)
		}
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

func TestDSNIsolateNets(t *testing.T) {
	dsn := `(pcb t (parser (string_quote ")) (network
    (class "P8+D" "+3V3" RAW
      (rule (width 400) (clearance 205)))
    (class "P16+D" GND
      (rule (width 800) (clearance 205)))
    (class "P1+D" SIG
      (rule (width 200) (clearance 205)))
  ) (wiring))`
	out, classes, err := dsnIsolateNets(dsn, []string{"+3V3", "GND", "NOT_THERE"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(classes, ",") != "P16+D,P8+D__POUR" {
		t.Fatalf("classes %v\n%s", classes, out)
	}
	if !strings.Contains(out, `(class "P8+D" RAW`) || !strings.Contains(out, `(class "P8+D__POUR" "+3V3"`) {
		t.Fatalf("split:\n%s", out)
	}
	if strings.Count(out, "(width 400)") != 2 || !strings.Contains(out, `(class "P1+D" SIG`) {
		t.Fatalf("rule lost or an untouched class changed:\n%s", out)
	}
	// A net in the DSN without a class cannot be left out silently.
	if _, _, err := dsnIsolateNets(`(pcb t (network (net N (pins U1-1)) (class "A" B (rule (clearance 1)))) (wiring))`, []string{"N"}); err == nil {
		t.Fatal("a net without a class must be an error")
	}
	// An existing class of that name is not reused.
	taken := `(pcb t (network
    (class "X" P Q (rule (clearance 1)))
    (class "X__POUR" S (rule (clearance 1)))
  ) (wiring))`
	if _, cl, err := dsnIsolateNets(taken, []string{"P"}); err != nil || strings.Join(cl, ",") != "X__POUR2" {
		t.Fatalf("collision: %v %v", cl, err)
	}
	if got, cl, err := dsnIsolateNets(dsn, nil); got != dsn || len(cl) != 0 || err != nil {
		t.Fatalf("no nets must not change the DSN: %v %v", cl, err)
	}
	// The split class still gets its insulation rule.
	iso := []map[string]any{{"a": "domain:a", "b": "domain:b", "clearanceMm": 8.0}}
	in := `(pcb t (network
    (class "P8+PPD_domain_a" P RAW (rule (clearance 205)))
    (class "P1+PPD_domain_b" Q (rule (clearance 205)))
  ) (wiring))`
	split, _, _ := dsnIsolateNets(in, []string{"P"})
	if ruled, n, err := dsnInsulationRules(split, iso, 0); err != nil || n != 2 || !strings.Contains(ruled, "P8+PPD_domain_a__POUR") {
		t.Fatalf("n=%d err=%v\n%s", n, err, ruled)
	}
}

func TestUseLargeBoardProfile(t *testing.T) {
	for _, c := range []struct {
		mode string
		n    int
		want bool
	}{{"auto", 1999, false}, {"auto", 2000, true}, {"on", 10, true}, {"off", 9999, false}} {
		if useLargeBoardProfile(c.mode, c.n) != c.want {
			t.Errorf("%+v", c)
		}
	}
}

func TestRouteCompleteHeldToBoardConnectivity(t *testing.T) {
	r := &kicadRun{summary: map[string]any{"boardConnectivity": map[string]any{"unconnected": 3}}}
	g := gateResult{Gate: "route-complete", Pass: true}
	r.boardConnectivityGate(&g)
	if g.Pass {
		t.Fatal("open pads must fail route-complete")
	}
	r = &kicadRun{summary: map[string]any{"largeBoard": map[string]any{}}}
	g = gateResult{Gate: "route-complete", Pass: true}
	r.boardConnectivityGate(&g)
	if g.Pass || !g.CannotCheck {
		t.Fatalf("no count on the large-board profile: %+v", g)
	}
	r = &kicadRun{summary: map[string]any{}}
	g = gateResult{Gate: "route-complete", Pass: true}
	if r.boardConnectivityGate(&g); !g.Pass {
		t.Fatal("no count and no profile keeps the gate as it was")
	}
	r = &kicadRun{summary: map[string]any{"boardConnectivity": map[string]any{"unconnected": 0}}}
	g = gateResult{Gate: "route-complete", Pass: true}
	if r.boardConnectivityGate(&g); !g.Pass {
		t.Fatal("zero unconnected keeps the pass")
	}
}
