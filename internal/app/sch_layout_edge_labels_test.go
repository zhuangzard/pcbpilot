package app

import (
	"fmt"
	"testing"
)

// Four port pins on one edge at a 10-raw pitch cannot all use the same lead:
// the planner alternates short/long and the result validates as a whole.
func TestEdgeLabelsAlternateOnDensePitch(t *testing.T) {
	rot := 180.0
	c := powerLayoutPlacement{Designator: "U1", BBox: layoutBBox{-20.5, -25.5, 20.5, 45.5}}
	for i := 0; i < 5; i++ {
		c.Pins = append(c.Pins, powerLayoutPin{Number: fmt.Sprint(i + 1), Name: fmt.Sprintf("P%d", i), Net: fmt.Sprintf("SIG%d", i), X: -30, Y: float64(i * 10), Rotation: &rot})
	}
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{c}}
	policies := map[string]string{}
	for i := 0; i < 5; i++ {
		policies[fmt.Sprintf("SIG%d", i)] = "module_port"
	}
	flags, named := libPlanEdgeLabels(&p, policies)
	if len(flags) != 5 || len(named) != 5 {
		t.Fatalf("dense run not planned: %d flags, %d named", len(flags), len(named))
	}
	for i := 1; i < len(flags); i++ {
		if flags[i].Offset == flags[i-1].Offset {
			t.Fatalf("neighbours share a lead length: %+v", flags)
		}
		if flags[i].Direction != "left" {
			t.Fatalf("planned lead must leave straight out of the edge: %+v", flags[i])
		}
	}
	trial := p
	trial.Flags = flags
	if err := validateLibGeometry(&trial); err != nil {
		t.Fatalf("planned run does not validate: %v", err)
	}
	// Two candidates are not a run: left to the ordinary search.
	p.Placements[0].Pins = p.Placements[0].Pins[:2]
	if flags, _ := libPlanEdgeLabels(&p, policies); len(flags) != 0 {
		t.Fatal("a two-pin run must not be planned")
	}
}
