package app

import (
	"errors"
	"reflect"
	"testing"
)

// Two independent signal islands share a marker corridor. Each first choice
// is individually legal, but A's short lead occupies B's only legal lead.
func jointNamingCompetitionFixture() (powerLayoutPlan, []libIsland, libNamingOptionVisitor) {
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{{
		Designator: "J1", BBox: layoutBBox{-20, -20, 20, 25},
		TextBBoxes: []layoutBBox{{-19, -19, -18, -18}},
		Pins:       []powerLayoutPin{{Number: "1", Net: "A", X: 30, Y: 0}, {Number: "2", Net: "B", X: 30, Y: 5}},
	}}}
	islands := libIslands(&p)
	options := func(current *powerLayoutPlan, island libIsland, kind string, accept func(*powerLayoutPlan) bool, budget *int) {
		choices := []powerLayoutFlag{{Net: island.net, Kind: kind, PinX: 30, PinY: island.pins[0].Y, Direction: "right", Offset: 10}}
		if island.net == "A" {
			choices = append(choices, powerLayoutFlag{Net: "A", Kind: kind, PinX: 35, PinY: -5, Direction: "down", Offset: 10})
		}
		for _, flag := range choices {
			if !libSpendNamingBudget([]*int{budget}) {
				return
			}
			candidate := *current
			candidate.Flags = append(append([]powerLayoutFlag(nil), current.Flags...), flag)
			if island.net == "A" && flag.Direction == "down" {
				candidate.Wires = libAppendRoute(current.Wires, []powerLayoutWire{
					{Net: "A", Points: [][2]float64{{30, 0}, {35, 0}}},
					{Net: "A", Points: [][2]float64{{35, 0}, {35, -5}}},
				})
			}
			if validateLibGeometry(&candidate) == nil && accept(&candidate) {
				return
			}
		}
	}
	return p, islands, options
}

func TestJointNamingRevisesEarlierLegalLead(t *testing.T) {
	p, islands, options := jointNamingCompetitionFixture()
	if err := validateLibGeometry(&p); err != nil {
		t.Fatal(err)
	}
	first := p
	first.Flags = []powerLayoutFlag{{Net: "A", Kind: "net_port_bi", PinX: 30, PinY: 0, Direction: "right", Offset: 10}, {Net: "B", Kind: "net_port_bi", PinX: 30, PinY: 5, Direction: "right", Offset: 10}}
	for _, flag := range first.Flags {
		alone := p
		alone.Flags = []powerLayoutFlag{flag}
		if err := validateLibGeometry(&alone); err != nil {
			t.Fatalf("individual lead %s is invalid: %v", flag.Net, err)
		}
	}
	if err := validateLibGeometry(&first); err == nil {
		t.Fatal("first legal A lead should block B")
	}
	budget := 4
	if err := libNameIslandsJointWith(&p, map[string]string{"A": "module_port", "B": "module_port"}, islands, &budget, options); err != nil {
		t.Fatal(err)
	}
	if budget != 0 || len(p.Flags) != 2 || p.Flags[0].Direction != "down" || p.Flags[1].Direction != "right" {
		t.Fatalf("joint search did not revise A: flags=%+v remaining=%d", p.Flags, budget)
	}
	if err := validateLibGeometry(&p); err != nil {
		t.Fatal(err)
	}
	if err := validateSchCompositionNets(&p); err != nil {
		t.Fatal(err)
	}
	production, productionIslands, _ := jointNamingCompetitionFixture()
	productionBudget := 1000
	if err := libNameIslandsJoint(&production, map[string]string{"A": "module_port", "B": "module_port"}, productionIslands, &productionBudget); err != nil {
		t.Fatalf("real marker generator did not expose an alternate lead: %v", err)
	}
	if err := validateLibGeometry(&production); err != nil {
		t.Fatal(err)
	}
	if err := validateSchCompositionNets(&production); err != nil {
		t.Fatal(err)
	}
}

func TestJointNamingBudgetExhaustionDoesNotPublishPartial(t *testing.T) {
	p, islands, options := jointNamingCompetitionFixture()
	before := p
	budget := 3
	err := libNameIslandsJointWith(&p, map[string]string{"A": "module_port", "B": "module_port"}, islands, &budget, options)
	if !errors.Is(err, errLibLayoutBudget) || budget != 0 || !reflect.DeepEqual(p, before) {
		t.Fatalf("bounded search published partial result: err=%v budget=%d flags=%+v", err, budget, p.Flags)
	}
}
