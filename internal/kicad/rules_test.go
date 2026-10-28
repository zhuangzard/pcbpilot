package kicad

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestNetclassRequestBodyIsRouteRequest(t *testing.T) {
	reqs := map[string]NetRequirement{"V": {OuterMil: 10, InnerMil: 8, ClearanceMil: 6}}
	iso := []map[string]any{{"a": "A", "b": "B", "clearanceMm": 1.0}}
	old, _ := json.MarshalIndent(map[string]any{"pcbpilotReqs": 2, "nets": reqs, "isolation": iso, "edges": []map[string]any(nil)}, "", "  ")
	now, _ := json.MarshalIndent(NetclassRequest{Nets: reqs, Isolation: iso}.Body(), "", "  ")
	if string(old) != string(now) {
		t.Fatalf("route's request changed:\n%s\n%s", old, now)
	}
	withSizes, _ := json.Marshal(NetclassRequest{Nets: reqs, Sizes: &SizeSet{}}.Body())
	if !strings.Contains(string(withSizes), `"predefinedSizes":{"owned":{"tracks":[],"vias":[]}}`) {
		t.Fatalf("sizes: %s", withSizes)
	}
}

func TestParseDRUAndSplit(t *testing.T) {
	text := "(version 1)\n# a comment\n(rule \"user\"\n  (condition \"A.NetName == 'X'\")\n  (constraint clearance (min 0.5mm) (max 2mm)))\n" +
		DRURulesBegin + "\n# request-sha256 x\n(rule \"pcbpilot-rules PP1_W10 outer\"\n  (layer outer)\n  (condition \"A.hasNetclass('PP1_W10')\")\n  (constraint track_width (min 10mil) (opt 0.3)))\n" + DRURulesEnd + "\n" +
		DRURouteBegin + "\n(rule \"pcbpilot PP1_W10 min\"\n  (condition \"A.hasNetclass('PP1_W10')\")\n  (constraint track_width (min 150um)))\n(rule \"e\"\n  (constraint edge_clearance (min (1mm + 1mm))))\n" + DRURouteEnd + "\n"
	user, rules, route, err := SplitDRU(text)
	if err != nil || !strings.Contains(user, "\"user\"") || strings.Contains(user, "pcbpilot") || !strings.HasPrefix(rules, DRURulesBegin) || !strings.HasPrefix(route, DRURouteBegin) {
		t.Fatalf("split: %v\nuser %q\nrules %q\nroute %q", err, user, rules, route)
	}
	rs, err := ParseDRU(text)
	if err != nil || len(rs) != 4 {
		t.Fatalf("%v %+v", err, rs)
	}
	if rs[0].Block != "" || *rs[0].Constraints[0].Min != 500000 || *rs[0].Constraints[0].Max != 2000000 {
		t.Fatalf("user rule: %+v", rs[0])
	}
	if rs[1].Block != "rules" || rs[1].Layer != "outer" || *rs[1].Constraints[0].Min != 254000 || *rs[1].Constraints[0].Opt != 300000 {
		t.Fatalf("rules rule: %+v", rs[1])
	}
	if rs[2].Block != "route" || *rs[2].Constraints[0].Min != 150000 || rs[2].Index != 2 {
		t.Fatalf("route rule: %+v", rs[2])
	}
	if !rs[3].Constraints[0].Unparsed {
		t.Fatalf("an expression is unparsed: %+v", rs[3])
	}
	if _, _, _, err := SplitDRU(DRURulesBegin + "\n(rule \"x\")\n"); err == nil {
		t.Fatal("an unterminated block must be an error")
	}
	for _, c := range []struct {
		s  string
		nm int64
		ok bool
	}{{"0.254mm", 254000, true}, {"10mil", 254000, true}, {"1in", 25400000, true}, {"0.2", 200000, true}, {"5um", 5000, true}, {"x", 0, false}} {
		if v, ok := DRULength(c.s); v != c.nm || ok != c.ok {
			t.Fatalf("DRULength(%q) = %d %v", c.s, v, ok)
		}
	}
	if MmOfNm(254000) != "0.254mm" {
		t.Fatal(MmOfNm(254000))
	}
}

func TestReadStackupCopper(t *testing.T) {
	b := []byte(`(kicad_pcb (version 20241229)
  (setup (stackup (layer "F.SilkS" (type "Top Silk Screen")) (layer "F.Cu" (type "copper") (thickness 0.035))
    (layer "dielectric 1" (type "core") (thickness 1.51)) (layer "In1.Cu" (type "copper") (thickness 0.0152))
    (layer "In2.Cu" (type "copper")) (layer "B.Cu" (type "copper") (thickness 0.035)))))`)
	cu, err := ReadStackupCopper(b)
	if err != nil || len(cu) != 4 || cu[0].Layer != "F.Cu" || cu[1].ThicknessMm != 0.0152 || cu[2].ThicknessMm != 0 {
		t.Fatalf("%v %+v", err, cu)
	}
	if cu, err := ReadStackupCopper([]byte(`(kicad_pcb (version 1) (setup))`)); err != nil || cu != nil {
		t.Fatalf("no stackup: %v %+v", err, cu)
	}
	if _, err := ReadStackupCopper([]byte(`(kicad_sch)`)); err == nil {
		t.Fatal("not a board")
	}
}
