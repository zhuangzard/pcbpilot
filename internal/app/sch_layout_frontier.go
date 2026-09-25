package app

import (
	"fmt"
	"math"
	"sort"
)

const (
	libDirectFrontierExpansionRaw = 40.0
	libDirectFrontierNodeLimit    = 4096
)

type libDirectFrontierResult struct {
	reachable bool
	complete  bool
	exhausted bool
	expanded  int
	blockers  map[string]bool
}

// libValidateMandatoryDirectPlacementFrontiers is a cheap, local placement
// gate in front of the global A* router. A candidate must not turn an unfinished
// mandatory direct island into a closed pocket. It deliberately proves only a
// local escape frontier (another physical island or the 40 raw envelope), not a
// complete route; the terminal routing gate remains authoritative.
//
// Only closures attributable to the newly placed component are rejected. This
// prevents a pre-existing incomplete layout from being blamed on an unrelated
// checkpoint and keeps conflict-directed backjump evidence honest.
func libValidateMandatoryDirectPlacementFrontiers(before, after *powerLayoutPlan, placed powerLayoutPlacement, policies map[string]string, routing *schematicRoutingContext) error {
	if before == nil || after == nil || placed.Designator == "" {
		return nil
	}
	expectedPins := map[string]int{}
	for _, component := range after.Placements {
		for _, pin := range component.Pins {
			if pin.Net != "" {
				expectedPins[pin.Net]++
			}
		}
	}
	if routing != nil {
		for net, count := range routing.netPins {
			if count > expectedPins[net] {
				expectedPins[net] = count
			}
		}
	}

	afterIslands := libIslands(after)
	islandCounts := map[string]int{}
	for _, island := range afterIslands {
		islandCounts[island.net]++
	}
	for _, island := range afterIslands {
		if !libDirectPolicy(policies[island.net]) || (islandCounts[island.net] <= 1 && expectedPins[island.net] <= len(island.pins)) {
			continue
		}
		if !libPlacementCanInfluenceFrontier(after, island, placed, libDirectFrontierExpansionRaw) {
			continue
		}
		result := libDirectIslandLocalFrontier(after, island, libDirectFrontierExpansionRaw, libDirectFrontierNodeLimit)
		if result.reachable || result.exhausted {
			continue
		}

		containsPlacedPin := false
		endpointRefs := map[string]bool{}
		for _, pin := range island.pins {
			if owner := libExactPinOwner(after, pin); owner != "" {
				endpointRefs[owner] = true
				containsPlacedPin = containsPlacedPin || owner == placed.Designator
			}
		}
		if !containsPlacedPin && !result.blockers[placed.Designator] {
			// The local island may already have been closed in an incomplete
			// source fixture. Do not manufacture causality at this checkpoint.
			continue
		}

		blockerRefs := make([]string, 0, len(result.blockers))
		for ref := range result.blockers {
			blockerRefs = append(blockerRefs, ref)
		}
		sort.Strings(blockerRefs)
		endpoints := make([]string, 0, len(endpointRefs))
		for ref := range endpointRefs {
			endpoints = append(endpoints, ref)
		}
		sort.Strings(endpoints)
		cause := fmt.Errorf("mandatory direct island %s has no local frontier after placing %s (endpoints=%v blockers=%v expanded=%d)", libIslandStableID(island), placed.Designator, endpoints, blockerRefs, result.expanded)
		return &schGeometryObstruction{
			kind:             "direct-island-enclosed",
			owners:           append([]string(nil), blockerRefs...),
			blockers:         append([]string(nil), blockerRefs...),
			blockersExplicit: true,
			nets:             []string{island.net},
			complete:         result.complete && len(blockerRefs) > 0,
			cause:            cause,
		}
	}
	return nil
}

func libPlacementCanInfluenceFrontier(p *powerLayoutPlan, island libIsland, placed powerLayoutPlacement, expansion float64) bool {
	access := libIslandAccessPoints(p, island)
	if len(access) == 0 {
		return false
	}
	islandBox := layoutBBox{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	for state := range access {
		point := mazePoint(state)
		islandBox.MinX = math.Min(islandBox.MinX, point[0])
		islandBox.MinY = math.Min(islandBox.MinY, point[1])
		islandBox.MaxX = math.Max(islandBox.MaxX, point[0])
		islandBox.MaxY = math.Max(islandBox.MaxY, point[1])
	}
	islandBox.MinX -= expansion
	islandBox.MinY -= expansion
	islandBox.MaxX += expansion
	islandBox.MaxY += expansion
	focus := placed.BBox
	for _, box := range placed.TextBBoxes {
		focus.MinX = math.Min(focus.MinX, box.MinX)
		focus.MinY = math.Min(focus.MinY, box.MinY)
		focus.MaxX = math.Max(focus.MaxX, box.MaxX)
		focus.MaxY = math.Max(focus.MaxY, box.MaxY)
	}
	for _, pin := range placed.Pins {
		focus.MinX = math.Min(focus.MinX, pin.X-schAnchorGrid)
		focus.MinY = math.Min(focus.MinY, pin.Y-schAnchorGrid)
		focus.MaxX = math.Max(focus.MaxX, pin.X+schAnchorGrid)
		focus.MaxY = math.Max(focus.MaxY, pin.Y+schAnchorGrid)
	}
	return !(focus.MaxX < islandBox.MinX || focus.MinX > islandBox.MaxX || focus.MaxY < islandBox.MinY || focus.MinY > islandBox.MaxY)
}

// libDirectIslandLocalFrontier flood-fills the same 5 raw routing graph used by
// A*. Pin starts retain the official outward direction constraint and strict
// foreign X crossings are advanced as one uninterrupted edge. Reaching another
// same-net physical island or the local envelope proves an escape frontier.
func libDirectIslandLocalFrontier(p *powerLayoutPlan, source libIsland, expansion float64, nodeLimit int) libDirectFrontierResult {
	result := libDirectFrontierResult{complete: true, blockers: map[string]bool{}}
	starts := libIslandAccessPoints(p, source)
	if len(starts) == 0 || nodeLimit < 1 {
		result.complete = false
		return result
	}

	minX, minY, maxX, maxY := math.MaxInt, math.MaxInt, math.MinInt, math.MinInt
	for state := range starts {
		if state.X < minX {
			minX = state.X
		}
		if state.Y < minY {
			minY = state.Y
		}
		if state.X > maxX {
			maxX = state.X
		}
		if state.Y > maxY {
			maxY = state.Y
		}
	}
	margin := int(math.Ceil(expansion / schAnchorGrid))
	bounds := [4]int{minX - margin, minY - margin, maxX + margin, maxY + margin}
	goals := map[mazeState]bool{}
	for _, island := range libIslands(p) {
		if island.net != source.net || libIslandStableID(island) == libIslandStableID(source) {
			continue
		}
		for state := range libIslandAccessPoints(p, island) {
			state.Dir = 0
			goals[state] = true
		}
	}

	// Depth-first traversal normally reaches an open envelope in only a few
	// nodes; breadth-first expansion made this guard dominate placement time on
	// otherwise open candidates. Reachability has no path-cost preference, so a
	// stack and position-only visited set preserve the same verdict.
	stack := make([]mazeState, 0, len(starts))
	seen := map[[2]int]bool{}
	for state := range starts {
		state.Dir = 0
		if required, constrained := libIslandPinDirection(p, source, state); constrained && libDirectPinNeedsFanoutLaunch(p, source, state) {
			delta := mazeDirections[required-1]
			launch := mazeState{X: state.X + 2*delta.X, Y: state.Y + 2*delta.Y, Dir: required}
			if launch.X < bounds[0] || launch.X > bounds[2] || launch.Y < bounds[1] || launch.Y > bounds[3] {
				result.reachable = true
				return result
			}
			if err := validateLibRoutingEdge(p, source.net, mazePoint(state), mazePoint(launch)); err != nil {
				libDirectFrontierObserve(&result, err)
				continue
			}
			goal := launch
			goal.Dir = 0
			if goals[goal] {
				result.reachable = true
				return result
			}
			seen[[2]int{launch.X, launch.Y}] = true
			stack = append(stack, launch)
			continue
		}
		seen[[2]int{state.X, state.Y}] = true
		stack = append(stack, state)
	}
	for len(stack) > 0 {
		state := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		result.expanded++
		if result.expanded > nodeLimit {
			result.exhausted = true
			result.complete = false
			return result
		}
		if state.X == bounds[0] || state.X == bounds[2] || state.Y == bounds[1] || state.Y == bounds[3] {
			result.reachable = true
			return result
		}
		directions := []int{1, 2, 3, 4}
		if state.Dir != 0 {
			directions = []int{state.Dir}
			for direction := 1; direction <= len(mazeDirections); direction++ {
				if direction != state.Dir {
					directions = append(directions, direction)
				}
			}
		}
		// Push alternatives in reverse so continuing straight is explored first.
		for i := len(directions) - 1; i >= 0; i-- {
			direction := directions[i]
			if state.Dir == 0 {
				if required, constrained := libIslandPinDirection(p, source, state); constrained && direction != required {
					continue
				}
			}
			next, ok := mazeAdvanceAcrossForeignX(p, source.net, state, direction, bounds)
			if !ok {
				continue
			}
			if err := validateLibRoutingEdge(p, source.net, mazePoint(state), mazePoint(next)); err != nil {
				libDirectFrontierObserve(&result, err)
				continue
			}
			goal := next
			goal.Dir = 0
			if goals[goal] {
				result.reachable = true
				return result
			}
			next.Dir = direction
			key := [2]int{next.X, next.Y}
			if !seen[key] {
				seen[key] = true
				stack = append(stack, next)
			}
		}
	}
	return result
}

// Repeated same-net pins on one symbol side need a branchable trunk. A single
// 5 raw exit is electrically legal but lets the next component occupy the very
// first trunk column; reserve one additional grid step only for that fanout
// case. Ordinary direct pins retain the normal 5 raw turn freedom.
func libDirectPinNeedsFanoutLaunch(p *powerLayoutPlan, source libIsland, state mazeState) bool {
	point := mazePoint(state)
	for _, component := range p.Placements {
		var pin *powerLayoutPin
		for i := range component.Pins {
			candidate := &component.Pins[i]
			if candidate.Net == source.net && math.Abs(candidate.X-point[0]) <= 1e-6 && math.Abs(candidate.Y-point[1]) <= 1e-6 {
				pin = candidate
				break
			}
		}
		if pin == nil {
			continue
		}
		side, err := libPinSide(*pin, component.BBox)
		if err != nil {
			return false
		}
		count := 0
		for _, candidate := range component.Pins {
			candidateSide, candidateErr := libPinSide(candidate, component.BBox)
			if candidateErr == nil && candidate.Net == source.net && candidateSide == side {
				count++
			}
		}
		return count > 1
	}
	return false
}

func libDirectFrontierObserve(result *libDirectFrontierResult, err error) {
	var obstruction *schGeometryObstruction
	if !asGeometryObstruction(err, &obstruction) {
		result.complete = false
		return
	}
	result.complete = result.complete && obstruction.complete
	refs := obstruction.blockers
	if len(refs) == 0 && !obstruction.blockersExplicit {
		refs = obstruction.owners
	}
	for _, ref := range refs {
		if ref != "" {
			result.blockers[ref] = true
		}
	}
}

// Kept as a tiny wrapper so the flood-fill does not need to import errors just
// for one typed provenance check.
func asGeometryObstruction(err error, target **schGeometryObstruction) bool {
	for err != nil {
		if value, ok := err.(*schGeometryObstruction); ok {
			*target = value
			return true
		}
		type unwrapper interface{ Unwrap() error }
		value, ok := err.(unwrapper)
		if !ok {
			break
		}
		err = value.Unwrap()
	}
	return false
}
