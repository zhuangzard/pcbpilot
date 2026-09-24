package app

// Route joins must retain a naming lead for an island that already contains
// every physical pin of its net. A witness uses the same terminal constructor
// and text/lead clearance checks as final naming. If a new route invalidates
// it, search all marker families before rejecting the route. Incomplete
// probes never prune a route.
type libRouteNamingWitness struct {
	layout         *powerLayoutPlan
	baselineClosed bool
}

type libRouteNamingWitnesses map[string]libRouteNamingWitness

func libRouteKeepsCompletedNamingFrontiers(before, after *powerLayoutPlan, policies map[string]string, routing *schematicRoutingContext, known libRouteNamingWitnesses) (bool, libRouteNamingWitnesses) {
	if before == nil || after == nil {
		return true, known
	}
	if known == nil {
		known = libRouteNamingWitnesses{}
	}
	updated := make(libRouteNamingWitnesses, len(known))
	for key, state := range known {
		updated[key] = state
	}
	var beforeIslands []libIsland
	for _, island := range libIslands(after) {
		if policies[island.net] == "" || len(island.pins) == 0 || libPlanNetPinCount(after, island.net) != len(island.pins) {
			continue
		}
		key := libIslandStableID(island)
		state, seen := known[key]
		if !seen {
			if beforeIslands == nil {
				beforeIslands = libIslands(before)
			}
			for _, old := range beforeIslands {
				if old.net == island.net && libIslandContainsPhysicalPin(old, island.pins[0]) {
					baselineComplete := len(old.pins) == libPlanNetPinCount(before, old.net)
					var complete bool
					state.layout, complete = libRouteNamingProbe(before, old, policies[old.net], routing)
					if complete {
						state.baselineClosed = baselineComplete && state.layout == nil
						updated[key] = state
					}
					break
				}
			}
		}
		if state.layout == nil {
			if state.baselineClosed {
				continue // This route did not cause a pre-existing completed closure.
			}
			// The newly joined island has become final. Its old fragment did
			// not need a marker of its own, so prove the completed tree now.
			replacement, complete := libRouteNamingProbe(after, island, policies[island.net], routing)
			if complete && replacement == nil {
				return false, known
			}
			if complete {
				updated[key] = libRouteNamingWitness{layout: replacement}
			}
			continue
		}
		if libNamingWitnessStillLegal(before, after, state.layout) {
			preserved := *after
			preserved.Wires = append(clonePowerLayoutWires(after.Wires), state.layout.Wires[len(before.Wires):]...)
			preserved.Flags = append(append([]powerLayoutFlag(nil), after.Flags...), state.layout.Flags[len(before.Flags):]...)
			updated[key] = libRouteNamingWitness{layout: &preserved}
			continue
		}
		replacement, complete := libRouteNamingProbe(after, island, policies[island.net], routing)
		if complete && replacement == nil {
			return false, known
		}
		if complete {
			updated[key] = libRouteNamingWitness{layout: replacement}
		}
	}
	return true, updated
}

func libRouteNamingProbe(p *powerLayoutPlan, island libIsland, policy string, routing *schematicRoutingContext) (*powerLayoutPlan, bool) {
	// Provisional rail joins and standalone route probes have no shared
	// candidate slice. Their lines are withdrawn before the terminal gate, so
	// the result is inconclusive here; final libNameIslands still proves every
	// island before a layout can be published. Never run a free 2048-candidate
	// search that hides real work from maxCandidates accounting.
	if routing == nil || routing.candidateBudget == nil {
		return nil, false
	}
	// Dense connector ports can exhaust hundreds of actual marker candidates
	// before proving closure (MCU_EN/RX needs 524 on a measured zone). Keep the
	// per-island proof bounded, while allowing that complete negative result
	// and the 1816-candidate closed CC tree seen on a measured USB zone.
	allowance := 2048
	shared := routing.candidateBudget
	available := *shared - routing.namingReserve
	if available < allowance {
		allowance = available
	}
	if allowance <= 0 {
		return nil, false
	}
	before := allowance
	witness, complete := libNamingFrontierWitness(p, island, policy, &allowance)
	*shared -= before - allowance
	return witness, complete
}
