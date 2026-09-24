package app

// A locally legal direct wire can close the only exit of another still
// unfinished physical island. Reject that route choice while alternatives are
// available. The 40 raw flood fill is only a local necessary condition: an
// exhausted search or an island already closed before this wire is inconclusive
// and remains for the complete terminal routing gate.
func libDirectRouteKeepsFrontiers(before, after *powerLayoutPlan, policies map[string]string) bool {
	if before == nil || after == nil {
		return true
	}
	afterIslands := libIslands(after)
	counts := map[string]int{}
	for _, island := range afterIslands {
		counts[island.net]++
	}
	var beforeIslands []libIsland
	for _, island := range afterIslands {
		if policies[island.net] != "direct" || counts[island.net] < 2 || len(island.pins) == 0 {
			continue
		}
		newFrontier := libDirectIslandLocalFrontier(after, island, libDirectFrontierExpansionRaw, libDirectFrontierNodeLimit)
		if newFrontier.reachable || newFrontier.exhausted {
			continue
		}
		if beforeIslands == nil {
			beforeIslands = libIslands(before)
		}
		for _, old := range beforeIslands {
			if old.net != island.net || !libIslandContainsPhysicalPin(old, island.pins[0]) {
				continue
			}
			oldFrontier := libDirectIslandLocalFrontier(before, old, libDirectFrontierExpansionRaw, libDirectFrontierNodeLimit)
			if oldFrontier.reachable {
				return false
			}
			break
		}
	}
	return true
}

func libIslandContainsPhysicalPin(island libIsland, pin powerLayoutPin) bool {
	for _, member := range island.pins {
		if libSamePhysicalPin(member, pin) {
			return true
		}
	}
	return false
}
