package app

import (
	"math"
	"sort"
)

// Edge label planning (boundary labelling, 2026-09-24).
//
// A net-port body is 11 raw across, wider than the 10-raw pin pitch of dense
// IC symbols, so neighbouring ports on one edge must alternate lead lengths
// (solved layouts show exactly 10 / 75-80). Greedy per-island naming finds
// that pattern by luck on easy zones and runs out of budget on dense cores
// (AT32 QFN32 + one more peripheral). This pass plans whole runs at once:
// every run of >= edgeLabelMinRun single-pin port islands at <= 10.5 raw pitch
// on one side of one part gets alternating short/long leads, trying both
// phases and the shortest long level that validates. Planned flags are fixed
// obstacles; the remaining islands go to the existing naming search.

const (
	edgeLabelMinRun = 3
	edgeLabelShort  = 10.0
)

var edgeLabelPlanning = true

type edgeLabelPin struct {
	pin  powerLayoutPin
	side string
	axis float64
}

// libPlanEdgeLabels returns planned flags (already validated together with
// p's wires and placements) and the island keys they name.
func libPlanEdgeLabels(p *powerLayoutPlan, policies map[string]string) ([]powerLayoutFlag, map[string]bool) {
	named := map[string]bool{}
	if !edgeLabelPlanning {
		return nil, named
	}
	single := map[[2]float64]string{} // pin position -> island key, for wireless single-pin islands
	for _, island := range libIslands(p) {
		if len(island.pins) == 1 && len(island.wireIndices) == 0 && policies[island.net] == "module_port" {
			q := island.pins[0]
			single[[2]float64{q.X, q.Y}] = island.key
		}
	}
	var planned []powerLayoutFlag
	for _, c := range p.Placements {
		bySide := map[string][]edgeLabelPin{}
		for _, q := range c.Pins {
			side, err := libPinSide(q, c.BBox)
			if err != nil {
				continue
			}
			axis := q.Y
			if side == "up" || side == "down" {
				axis = q.X
			}
			bySide[side] = append(bySide[side], edgeLabelPin{q, side, axis})
		}
		for _, side := range []string{"left", "right", "up", "down"} {
			pins := bySide[side]
			sort.Slice(pins, func(i, j int) bool { return pins[i].axis < pins[j].axis })
			// Runs of candidate pins that are consecutive along the edge (a
			// non-candidate pin - wired, NC, power - breaks the run).
			var run []edgeLabelPin
			flush := func() {
				if len(run) >= edgeLabelMinRun {
					if flags := libPlanEdgeRun(p, planned, run); flags != nil {
						planned = append(planned, flags...)
						for _, e := range run {
							named[single[[2]float64{e.pin.X, e.pin.Y}]] = true
						}
					}
				}
				run = nil
			}
			for i, e := range pins {
				_, candidate := single[[2]float64{e.pin.X, e.pin.Y}]
				if !candidate || (len(run) > 0 && math.Abs(e.axis-run[len(run)-1].axis) > 10.5) {
					flush()
				}
				if candidate {
					run = append(run, e)
				}
				if i == len(pins)-1 {
					flush()
				}
			}
		}
	}
	return planned, named
}

func libPlanEdgeRun(p *powerLayoutPlan, planned []powerLayoutFlag, run []edgeLabelPin) []powerLayoutFlag {
	for long := 60.0; long <= 120; long += 5 {
		for phase := 0; phase < 2; phase++ {
			flags := make([]powerLayoutFlag, 0, len(run))
			for i, e := range run {
				offset := edgeLabelShort
				if (i+phase)%2 == 1 {
					offset = long
				}
				flags = append(flags, powerLayoutFlag{Net: e.pin.Net, Kind: "net_port_bi", PinX: e.pin.X, PinY: e.pin.Y, Direction: e.side, Offset: offset})
			}
			trial := *p
			trial.Flags = append(append(append([]powerLayoutFlag(nil), p.Flags...), planned...), flags...)
			if validateLibGeometry(&trial) == nil {
				return flags
			}
		}
	}
	return nil
}
