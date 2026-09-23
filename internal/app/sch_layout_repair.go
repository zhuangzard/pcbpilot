package app

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

// SchematicLayoutSearchDiagnostics describes bounded search, not a proof that
// no placement exists outside the explored coordinates/branches.
type SchematicLayoutSearchDiagnostics struct {
	Strategy               string                       `json:"strategy"`
	Backtracks             int                          `json:"backtracks"`
	RepairAttempts         int                          `json:"repairAttempts"`
	TargetedRelocations    int                          `json:"targetedRelocations"`
	RelocationTargets      []string                     `json:"relocationTargets,omitempty"`
	RelocationAttempts     []SchematicRelocationAttempt `json:"relocationAttempts,omitempty"`
	MovedComponents        []string                     `json:"movedComponents"`
	BranchLimit            int                          `json:"branchLimit"`
	ConflictPasses         int                          `json:"conflictPasses"`
	SkippedCheckpoints     int                          `json:"skippedCheckpoints"`
	PlacementFailures      int                          `json:"placementFailures"`
	BackjumpTargets        []string                     `json:"backjumpTargets,omitempty"`
	RouteBackjumpTargets   []string                     `json:"routeBackjumpTargets,omitempty"`
	CheckpointAlternatives map[string]int               `json:"checkpointAlternatives,omitempty"`
}

type SchematicRelocationAttempt struct {
	ComponentID   string   `json:"componentId"`
	ComponentRef  string   `json:"componentRef"`
	Trigger       string   `json:"trigger,omitempty"`
	Group         []string `json:"group"`
	DX            float64  `json:"dx"`
	DY            float64  `json:"dy"`
	ExpandedNodes int      `json:"expandedNodes"`
	Result        string   `json:"result"`
	Message       string   `json:"message,omitempty"`
}

// SchematicLayoutSearchFailure retains bounded termination accounting without
// requiring callers to parse the human error message. Its cause still exposes
// the concrete placement/route conflict and any budget/branch sentinel.
type SchematicLayoutSearchFailure struct {
	CandidatesUsed      int                              `json:"candidatesUsed"`
	RemainingCandidates int                              `json:"remainingCandidates"`
	Search              SchematicLayoutSearchDiagnostics `json:"search"`
	cause               error
}

// A resource stop may retain the last observed concrete terminal failure.
// preRegenerationLayout is its placement checkpoint, including provisional
// geometry; it is not the failed regenerated trial.
type schematicTerminalFailure struct {
	cause                 error
	preRegenerationLayout *powerLayoutPlan
}

func (e *schematicTerminalFailure) Error() string { return e.cause.Error() }
func (e *schematicTerminalFailure) Unwrap() error { return e.cause }
func (e *schematicTerminalFailure) FailureDetails() any {
	result := struct {
		Kind                  string           `json:"kind"`
		Reason                string           `json:"reason"`
		ObstructionKind       string           `json:"obstructionKind,omitempty"`
		BlockerRefs           []string         `json:"blockerRefs,omitempty"`
		Nets                  []string         `json:"nets,omitempty"`
		PreRegenerationLayout *powerLayoutPlan `json:"preRegenerationLayout,omitempty"`
	}{Kind: "terminal-conflict", Reason: e.cause.Error(), PreRegenerationLayout: e.preRegenerationLayout}
	var obstruction *schGeometryObstruction
	if errors.As(e.cause, &obstruction) {
		result.ObstructionKind = obstruction.kind
		result.BlockerRefs = append([]string(nil), obstruction.blockers...)
		result.Nets = append([]string(nil), obstruction.nets...)
		sort.Strings(result.BlockerRefs)
		sort.Strings(result.Nets)
	}
	return result
}

func (e *SchematicLayoutSearchFailure) Error() string {
	return fmt.Sprintf("local search failed after %d candidates, %d backtracks, %d relocation attempts (branch limit %d; no capacity proof): %v", e.CandidatesUsed, e.Search.Backtracks, e.Search.RepairAttempts, e.Search.BranchLimit, e.cause)
}
func (e *SchematicLayoutSearchFailure) Unwrap() error { return e.cause }
func (e *SchematicLayoutSearchFailure) FailureDetails() any {
	return struct {
		Kind                      string `json:"kind"`
		GlobalInfeasibilityProven bool   `json:"globalInfeasibilityProven"`
		*SchematicLayoutSearchFailure
	}{"search-failure", false, e}
}

var errSchematicRepairBranches = errors.New("checkpoint branch limit exhausted")
var errSchematicRepairFocus = errors.New("focused conflict pass exhausted")
var errSchematicCandidateReserve = errors.New("candidate budget reserved for terminal blocker relocation")

// A failed direct route identifies the net and the bodies/stems which actually
// obstructed its candidate paths. Checkpoints unrelated to that conflict are
// rolled back without wasting their complete position-alternative allowance.
type schematicRouteConflict struct {
	net            string
	sourceIsland   string
	targetIsland   string
	endpointOwners map[string]bool
	blockers       map[string]bool
	ownersComplete bool
	cause          error
}

func (e *schematicRouteConflict) Error() string {
	if e.cause != nil {
		return fmt.Sprintf("cannot route direct net %s between physical islands %s and %s: %v", e.net, e.sourceIsland, e.targetIsland, e.cause)
	}
	return fmt.Sprintf("cannot route direct net %s between measured pins without crossing obstacles", e.net)
}
func (e *schematicRouteConflict) Unwrap() error { return e.cause }

func (e *schematicRouteConflict) FailureDetails() any {
	refs := make([]string, 0, len(e.blockers))
	for ref, blocks := range e.blockers {
		if blocks {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	endpoints := make([]string, 0, len(e.endpointOwners))
	for ref, owns := range e.endpointOwners {
		if owns {
			endpoints = append(endpoints, ref)
		}
	}
	sort.Strings(endpoints)
	return struct {
		Kind                      string   `json:"kind"`
		Net                       string   `json:"net"`
		SourceIsland              string   `json:"sourceIsland,omitempty"`
		TargetIsland              string   `json:"targetIsland,omitempty"`
		EndpointRefs              []string `json:"endpointRefs,omitempty"`
		BlockerRefs               []string `json:"blockerRefs"`
		OwnersComplete            bool     `json:"ownersComplete"`
		GlobalInfeasibilityProven bool     `json:"globalInfeasibilityProven"`
	}{"route-conflict", e.net, e.sourceIsland, e.targetIsland, endpoints, refs, e.ownersComplete, false}
}

type schematicRepairSearch struct {
	input           SchematicLayoutInput
	measured        map[string]powerLayoutPlacement
	members         []string
	hints           map[string]SchematicLayoutPeripheral
	budget          *int
	initial         int
	diagnostics     SchematicLayoutSearchDiagnostics
	firstXY         map[string][2]float64
	depth           map[string]int
	lastErr         error
	lastTerminal    powerLayoutPlan
	lastTerminalErr error
	focused         bool
	routing         *schematicRoutingContext
}

func newSchematicRepairSearch(input SchematicLayoutInput, measured map[string]powerLayoutPlacement, members []string, hints map[string]SchematicLayoutPeripheral, budget *int, routingArg ...*schematicRoutingContext) *schematicRepairSearch {
	var routing *schematicRoutingContext
	if len(routingArg) > 0 {
		routing = routingArg[0]
	}
	// Every checkpoint rollback follows a placement candidate which has already
	// debited the shared maxCandidates allowance. A smaller, member-count based
	// branch cap therefore cannot protect an otherwise unbounded search; it only
	// discards paid-for search capacity on dense zones. Mirror the shared budget
	// so candidate exhaustion remains the authoritative bound and failure cause.
	// Keep 128 for tiny synthetic budgets so tests and diagnostics can still
	// distinguish a candidate stop from the secondary recursion guard.
	branchLimit := *budget
	if branchLimit < 128 {
		branchLimit = 128
	}
	return &schematicRepairSearch{input: input, measured: measured, members: members, hints: hints, budget: budget, initial: *budget, routing: routing, depth: schematicAttachmentDepths(input.CoreComponentID, members, hints),
		diagnostics: SchematicLayoutSearchDiagnostics{Strategy: "checkpoint-local-repair-v1", BranchLimit: branchLimit, ConflictPasses: 1, MovedComponents: []string{}, CheckpointAlternatives: map[string]int{}}, firstXY: map[string][2]float64{}, focused: true}
}

// A checkpoint owns immutable placement/wire slices. On a descendant conflict,
// a different XY is selected at this checkpoint and the entire affected suffix
// is recomputed. Neither a translated stale wire nor a partial result escapes.
func (s *schematicRepairSearch) solve(p powerLayoutPlan, pending []string) (*SchematicLayoutResult, error) {
	finished, err := s.search(p, pending)
	if err != nil && errors.Is(err, errSchematicCandidateReserve) && s.lastTerminalErr != nil {
		if repaired, ok := s.tryTargetedTerminalRelocation(s.lastTerminal, s.lastTerminalErr); ok {
			finished, err = repaired, nil
		}
	}
	if err != nil && !errors.Is(err, errSchematicExpandedBudget) && !errors.Is(err, errSchematicCandidateReserve) && *s.budget > 0 && s.diagnostics.Backtracks < s.diagnostics.BranchLimit {
		// Actual rejected-edge blockers are the first relocation priority. If
		// that focused pass fails, retry conservatively with both blockers and
		// physical endpoint owners, sharing the SAME budget and branch limit.
		s.focused = false
		s.diagnostics.ConflictPasses++
		finished, err = s.search(p, pending)
	}
	if err != nil {
		if errors.Is(err, errSchematicRepairBranches) || errors.Is(err, errSchematicRepairFocus) || errors.Is(err, errLibLayoutBudget) || errors.Is(err, errSchematicExpandedBudget) || errors.Is(err, errSchematicCandidateReserve) {
			// Keep the resource stop authoritative while exposing the real final
			// conflict. A recursive budget sentinel is not itself that conflict.
			if s.lastTerminalErr != nil {
				err = fmt.Errorf("%w: last observed terminal conflict: %w", err, s.lastTerminalErr)
			} else if s.lastErr != nil {
				err = fmt.Errorf("%w: last placement conflict: %w", err, s.lastErr)
			}
		}
		return nil, &SchematicLayoutSearchFailure{CandidatesUsed: s.initial - *s.budget, RemainingCandidates: *s.budget, Search: s.diagnostics, cause: err}
	}
	for _, id := range s.members {
		before, ok := s.firstXY[id]
		if !ok {
			continue
		}
		for _, c := range finished.Placements {
			if c.Designator == s.measured[id].Designator && before != [2]float64{c.X, c.Y} {
				s.diagnostics.MovedComponents = append(s.diagnostics.MovedComponents, id)
			}
		}
	}
	return &SchematicLayoutResult{Placements: finished.Placements, Wires: finished.Wires, Flags: finished.Flags, Score: libCandidateScore(finished), Search: &s.diagnostics}, nil
}

func (s *schematicRepairSearch) search(p powerLayoutPlan, pending []string) (*powerLayoutPlan, error) {
	if *s.budget <= 0 {
		return nil, errLibLayoutBudget
	}
	if s.canTargetedTerminalRelocation() && *s.budget <= s.candidateReserve() {
		return nil, errSchematicCandidateReserve
	}
	if s.routing != nil && s.routing.expanded >= s.routing.options.MaxExpandedNodes {
		return nil, errSchematicExpandedBudget
	}
	if s.diagnostics.Backtracks >= s.diagnostics.BranchLimit {
		return nil, errSchematicRepairBranches
	}
	if s.focused && s.diagnostics.Backtracks >= s.diagnostics.BranchLimit/2 && s.diagnostics.Backtracks > 0 {
		return nil, errSchematicRepairFocus
	}
	if len(pending) == 0 {
		limit := s.sliceBudget()
		before := limit
		out, err := libFinishSchematicLayoutRegenerate(p, s.input.NetPolicies, &limit, s.routing)
		*s.budget -= before - limit
		if err != nil && !isBareSchematicResourceStop(err) {
			s.lastTerminal = p
			s.lastTerminal.Placements = append([]powerLayoutPlacement(nil), p.Placements...)
			s.lastTerminal.Wires = clonePowerLayoutWires(p.Wires)
			s.lastTerminal.Flags = append([]powerLayoutFlag(nil), p.Flags...)
			preRegenerationLayout := s.lastTerminal
			s.lastTerminalErr = &schematicTerminalFailure{cause: err, preRegenerationLayout: &preRegenerationLayout}
		}
		if err != nil && *s.budget > 0 && !isSchematicSearchResourceStop(err) {
			var routingFailure *schematicRoutingFailure
			if errors.As(err, &routingFailure) && routingFailure.Kind == "relocation-budget-reserved" {
				if repaired, ok := s.tryTargetedTerminalRelocation(p, err); ok {
					return repaired, nil
				}
			}
			var namingConflict *schematicNamingConflict
			if errors.As(err, &namingConflict) {
				if repaired, ok := s.tryNamingIslandRelocation(p, err); ok {
					return repaired, nil
				}
			}
		}
		return out, err
	}
	pending = append([]string(nil), pending...)
	sort.SliceStable(pending, func(i, j int) bool {
		a, b := libPeripheralPriority(s.measured[pending[i]], s.input.NetPolicies), libPeripheralPriority(s.measured[pending[j]], s.input.NetPolicies)
		if a != b {
			return a < b
		}
		// Place the most constrained symbol before small independent branches.
		// Otherwise arbitrary source-array order can fill the core neighborhood
		// with two-pin chains, then force a dense connector and its owned
		// peripherals into unrelated leftover gaps.
		a, b = libConnectedPinCount(s.measured[pending[i]]), libConnectedPinCount(s.measured[pending[j]])
		if a != b {
			return a > b
		}
		// Among equally sized peripherals, reserve dense host fanout before
		// unrelated chains occupy it. This uses only explicit ownership and
		// measured connected-pin cardinality; automatic multi-host parts remain
		// flexible and are therefore considered after pinned attachments.
		a, b = schematicAttachmentHostPinCount(pending[i], s.measured, s.hints), schematicAttachmentHostPinCount(pending[j], s.measured, s.hints)
		if a != b {
			return a > b
		}
		return s.depth[pending[i]] > s.depth[pending[j]]
	})
	placed := map[string]powerLayoutPlacement{}
	for _, c := range p.Placements {
		for _, id := range s.members {
			if c.Designator == s.measured[id].Designator {
				placed[id] = c
			}
		}
	}
	var lastErr error
	for i, id := range pending {
		pairs, err := libAttachmentPairs(id, s.measured[id], s.hints[id], placed, s.members, s.input.NetPolicies)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", id, err)
		}
		if len(pairs) == 0 {
			continue
		}
		rejected := map[[2]float64]bool{}
		cursor := 5.0
		for alternate := 0; alternate < 3; alternate++ {
			if *s.budget <= 0 {
				return nil, errLibLayoutBudget
			}
			if s.diagnostics.Backtracks >= s.diagnostics.BranchLimit {
				return nil, errSchematicRepairBranches
			}
			limit := s.sliceBudget()
			before := limit
			next, placeErr := libPlacePeripheralPairsWithRouting(p, s.measured[id], pairs, s.input.NetPolicies, &limit, rejected, s.routing, &cursor)
			*s.budget -= before - limit
			if next == nil {
				conflict := s.placementConflict(id, pairs, placed, pending, &p, placeErr)
				s.diagnostics.PlacementFailures++
				lastErr, s.lastErr = conflict, conflict
				// With a closed attachment-host set, adding unrelated suffix
				// components cannot remove these static obstacles. Backjump now;
				// pending automatic same-net hosts deliberately keep the old
				// ordering search available instead of being wrongly pruned.
				if !conflict.FutureHostsPossible {
					return nil, conflict
				}
				break
			}
			c := next.Placements[len(next.Placements)-1]
			xy := [2]float64{c.X, c.Y}
			rejected[xy] = true
			s.diagnostics.CheckpointAlternatives[id]++
			if _, ok := s.firstXY[id]; !ok {
				s.firstXY[id] = xy
			}
			if alternate > 0 {
				s.diagnostics.RepairAttempts++
			}
			rest := append(append([]string{}, pending[:i]...), pending[i+1:]...)
			out, childErr := s.search(*next, rest)
			if childErr == nil {
				return out, nil
			}
			if errors.Is(childErr, errSchematicRepairBranches) || errors.Is(childErr, errSchematicRepairFocus) {
				return nil, childErr
			}
			lastErr = childErr
			if !isSchematicSearchResourceStop(childErr) {
				s.lastErr = childErr
			}
			var placementConflict *SchematicPlacementConflict
			if errors.As(childErr, &placementConflict) {
				if !s.placementParticipates(id, placementConflict) {
					s.diagnostics.SkippedCheckpoints++
					return nil, childErr
				}
				s.diagnostics.BackjumpTargets = append(s.diagnostics.BackjumpTargets, id)
			}
			var conflict *schematicRouteConflict
			if errors.As(childErr, &conflict) {
				if !s.participates(id, conflict) {
					s.diagnostics.SkippedCheckpoints++
					return nil, childErr
				}
				s.diagnostics.RouteBackjumpTargets = append(s.diagnostics.RouteBackjumpTargets, id)
			}
			s.diagnostics.Backtracks++
			if s.diagnostics.Backtracks >= s.diagnostics.BranchLimit {
				return nil, errSchematicRepairBranches
			}
			if s.focused && s.diagnostics.Backtracks >= s.diagnostics.BranchLimit/2 {
				return nil, errSchematicRepairFocus
			}
			// Exhaustion of a local slice requests rollback; exhaustion of the
			// shared budget is terminal and never receives a fresh allowance.
			if errors.Is(childErr, errSchematicExpandedBudget) || errors.Is(childErr, errSchematicCandidateReserve) || (errors.Is(childErr, errLibLayoutBudget) && *s.budget == 0) {
				return nil, childErr
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("disconnected or cyclic attachment for %v", pending)
	}
	return nil, lastErr
}

func (s *schematicRepairSearch) candidateReserve() int {
	value := s.initial / 20
	if value < 512 {
		value = 512
	}
	if value > 10000 {
		value = 10000
	}
	return value
}

func isSchematicSearchResourceStop(err error) bool {
	return errors.Is(err, errSchematicRepairBranches) || errors.Is(err, errSchematicRepairFocus) || errors.Is(err, errLibLayoutBudget) || errors.Is(err, errSchematicExpandedBudget) || errors.Is(err, errSchematicCandidateReserve)
}

// A bare sentinel contains no terminal geometry evidence. In particular, a
// naming slice that ran out before trying every lead must not erase an earlier
// observed conflict or be reported as a proven no-safe-lead result.
func isBareSchematicResourceStop(err error) bool {
	return err == errLibLayoutBudget || err == errSchematicExpandedBudget || err == errSchematicCandidateReserve || err == errSchematicRepairBranches || err == errSchematicRepairFocus
}

// Reserving candidates only helps when the currently stored terminal layout
// contains a movable, attributed blocker. An unknown owner or a naming error
// cannot use the targeted stage, so ordinary bounded checkpoint search keeps
// the remaining candidates in those cases.
func (s *schematicRepairSearch) targetedTerminalBlockers(p *powerLayoutPlan, routeErr error) []string {
	var conflict *schematicRouteConflict
	if !errors.As(routeErr, &conflict) || !conflict.ownersComplete || len(conflict.blockers) == 0 {
		return nil
	}
	coreRef := s.measured[s.input.CoreComponentID].Designator
	refs := make([]string, 0, len(conflict.blockers))
	for ref, blocks := range conflict.blockers {
		if !blocks || ref == coreRef {
			continue
		}
		known, present := false, false
		for _, measured := range s.measured {
			known = known || measured.Designator == ref
		}
		for _, placement := range p.Placements {
			present = present || placement.Designator == ref
		}
		if known && present {
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := conflict.endpointOwners[refs[i]], conflict.endpointOwners[refs[j]]
		if a != b {
			return a
		}
		return refs[i] < refs[j]
	})
	return refs
}

func (s *schematicRepairSearch) canTargetedTerminalRelocation() bool {
	return len(s.targetedTerminalBlockers(&s.lastTerminal, s.lastTerminalErr)) > 0
}

func (s *schematicRepairSearch) namingIslandTargets(p *powerLayoutPlan, namingErr error) []string {
	var conflict *schematicNamingConflict
	if !errors.As(namingErr, &conflict) || !conflict.ownersComplete {
		return nil
	}
	coreRef := s.measured[s.input.CoreComponentID].Designator
	eligible := map[string]bool{}
	for ref := range conflict.endpointOwners {
		eligible[ref] = true
	}
	// An isolated core island can still be blocked by the explicitly owned
	// peripheral that must connect to that very pin. Terminal regeneration may
	// have withdrawn its provisional wire, so it is not yet an island endpoint.
	// This source attachment is an exact dependency, unlike a same-net guess.
	for id, hint := range s.hints {
		if hint.AttachTo == nil || !conflict.endpointOwners[s.measured[hint.AttachTo.ComponentID].Designator] {
			continue
		}
		hostMatches, ownMatches := false, false
		for _, pin := range s.measured[hint.AttachTo.ComponentID].Pins {
			hostMatches = hostMatches || pin.Number == hint.AttachTo.PinNumber && pin.Net == conflict.net
		}
		for _, pin := range s.measured[id].Pins {
			ownMatches = ownMatches || pin.Net == conflict.net && (hint.PinNumber == "" || pin.Number == hint.PinNumber)
		}
		if hostMatches && ownMatches {
			eligible[s.measured[id].Designator] = true
		}
	}
	refs := make([]string, 0, len(eligible))
	for ref := range eligible {
		if ref == coreRef {
			continue
		}
		known, present := false, false
		for _, measured := range s.measured {
			known = known || measured.Designator == ref
		}
		for _, placement := range p.Placements {
			present = present || placement.Designator == ref
		}
		if known && present {
			refs = append(refs, ref)
		}
	}
	sort.Strings(refs)
	return refs
}

// Naming island owners are dependencies rather than proven blockers. Probe
// their nearest outward grid step immediately. Never reserve candidates or
// prune other checkpoints on this evidence alone. Every success re-routes and
// re-names the full island forest under the shared budgets and geometry gate.
func (s *schematicRepairSearch) tryNamingIslandRelocation(p powerLayoutPlan, namingErr error) (*powerLayoutPlan, bool) {
	return s.tryTerminalRelocation(p, s.namingIslandTargets(&p, namingErr), "naming-island")
}

// After route-order rollback is exhausted, move only blockers proven by
// rejected search edges. Explicit attachment descendants travel with their
// host as one rigid group; every generated wire is withdrawn and recomputed.
// This is still bounded by the same candidate and routing contexts.
func (s *schematicRepairSearch) tryTargetedTerminalRelocation(p powerLayoutPlan, routeErr error) (*powerLayoutPlan, bool) {
	return s.tryTerminalRelocation(p, s.targetedTerminalBlockers(&p, routeErr), "route-blocker")
}

func (s *schematicRepairSearch) tryTerminalRelocation(p powerLayoutPlan, refs []string, trigger string) (*powerLayoutPlan, bool) {
	coreRef := s.measured[s.input.CoreComponentID].Designator
	for _, ref := range refs {
		rootID := ""
		for id, measured := range s.measured {
			if measured.Designator == ref {
				rootID = id
				break
			}
		}
		if rootID == "" || ref == coreRef {
			continue
		}
		group := s.attachmentDescendants(rootID)
		groupIDs := make([]string, 0, len(group))
		for id := range group {
			groupIDs = append(groupIDs, id)
		}
		sort.Strings(groupIDs)
		deltas := schematicTargetedRelocationDeltas(&p, ref, coreRef)
		if trigger == "naming-island" {
			// Island membership is dependency evidence, not a proven obstacle.
			// Probe only the nearest outward grid shell here; deeper movement
			// belongs to the ordinary checkpoint search and must keep its budget.
			deltas = deltas[:1]
		}
		for _, delta := range deltas {
			if *s.budget <= 0 {
				return nil, false
			}
			*s.budget--
			s.diagnostics.TargetedRelocations++
			trial := p
			trial.Placements = append([]powerLayoutPlacement(nil), p.Placements...)
			trial.Wires = nil
			trial.Flags = nil
			moved := false
			for i := range trial.Placements {
				id := ""
				for candidateID, measured := range s.measured {
					if measured.Designator == trial.Placements[i].Designator {
						id = candidateID
						break
					}
				}
				if group[id] {
					trial.Placements[i] = plTranslate(trial.Placements[i], delta[0], delta[1])
					moved = true
				}
			}
			attempt := SchematicRelocationAttempt{ComponentID: rootID, ComponentRef: ref, Trigger: trigger, Group: append([]string(nil), groupIDs...), DX: delta[0], DY: delta[1]}
			if !moved {
				attempt.Result = "target-missing"
				s.diagnostics.RelocationAttempts = append(s.diagnostics.RelocationAttempts, attempt)
				continue
			}
			if err := validateLibGeometry(&trial); err != nil {
				attempt.Result = "geometry-rejected"
				s.diagnostics.RelocationAttempts = append(s.diagnostics.RelocationAttempts, attempt)
				continue
			}
			limit := s.sliceBudget()
			if trigger == "naming-island" && limit > 4096 {
				limit = 4096
			}
			before := limit
			expandedBefore := 0
			if s.routing != nil {
				expandedBefore = s.routing.expanded
				s.routing.relocation++
			}
			finished, err := libFinishSchematicLayoutRegenerate(trial, s.input.NetPolicies, &limit, s.routing)
			if s.routing != nil {
				s.routing.relocation--
			}
			*s.budget -= before - limit
			if s.routing != nil {
				attempt.ExpandedNodes = s.routing.expanded - expandedBefore
			}
			if err == nil {
				attempt.Result = "accepted"
				s.diagnostics.RelocationAttempts = append(s.diagnostics.RelocationAttempts, attempt)
				s.diagnostics.RelocationTargets = append(s.diagnostics.RelocationTargets, rootID)
				return finished, true
			}
			var routingFailure *schematicRoutingFailure
			if errors.As(err, &routingFailure) {
				attempt.Result = routingFailure.Kind
			} else if errors.Is(err, errLibLayoutBudget) {
				attempt.Result = "candidate-budget-exhausted"
			} else {
				attempt.Result = "final-validation-failed"
			}
			attempt.Message = err.Error()
			s.diagnostics.RelocationAttempts = append(s.diagnostics.RelocationAttempts, attempt)
		}
	}
	return nil, false
}

func schematicTargetedRelocationDeltas(p *powerLayoutPlan, ref, coreRef string) [][2]float64 {
	component, core := libPlacementByDesignator(p, ref), libPlacementByDesignator(p, coreRef)
	if component == nil || core == nil {
		return [][2]float64{{5, 0}, {-5, 0}, {0, 5}, {0, -5}, {10, 0}, {-10, 0}, {0, 10}, {0, -10}}
	}
	dx, dy := component.X-core.X, component.Y-core.Y
	sx, sy := 1.0, 1.0
	if dx < 0 {
		sx = -1
	}
	if dy < 0 {
		sy = -1
	}
	xAxis := [2]float64{sx, 0}
	yAxis := [2]float64{0, sy}
	primary, secondary := xAxis, yAxis
	if math.Abs(dy) > math.Abs(dx) {
		primary, secondary = yAxis, xAxis
	}
	scale := func(v [2]float64, n float64) [2]float64 { return [2]float64{v[0] * n, v[1] * n} }
	// Small outward moves remain the first recovery step, but a dense owned
	// cluster can need more than 10 raw before its routed tree leaves room for
	// ground/power naming. Expand monotonically to one 40-raw routing shell;
	// every attempt still consumes the shared candidate and A* budgets.
	var deltas [][2]float64
	appendShell := func(axis [2]float64, sign float64) {
		for distance := 5.0; distance <= 40; distance += 5 {
			deltas = append(deltas, scale(axis, sign*distance))
		}
	}
	appendShell(primary, 1)
	appendShell(secondary, 1)
	appendShell(primary, -1)
	appendShell(secondary, -1)
	return deltas
}

func (s *schematicRepairSearch) attachmentDescendants(root string) map[string]bool {
	out := map[string]bool{root: true}
	changed := true
	for changed {
		changed = false
		for id, hint := range s.hints {
			if out[id] || hint.AttachTo == nil || !out[hint.AttachTo.ComponentID] {
				continue
			}
			out[id] = true
			changed = true
		}
	}
	return out
}

func libConnectedPinCount(component powerLayoutPlacement) int {
	count := 0
	for _, pin := range component.Pins {
		if pin.Net != "" {
			count++
		}
	}
	return count
}

func schematicAttachmentHostPinCount(id string, measured map[string]powerLayoutPlacement, hints map[string]SchematicLayoutPeripheral) int {
	hint := hints[id]
	if hint.AttachTo == nil {
		return 0
	}
	return libConnectedPinCount(measured[hint.AttachTo.ComponentID])
}

func schematicAttachmentDepths(core string, members []string, hints map[string]SchematicLayoutPeripheral) map[string]int {
	depth := map[string]int{core: 0}
	visiting := map[string]bool{}
	var visit func(string) int
	visit = func(id string) int {
		if value, ok := depth[id]; ok {
			return value
		}
		if visiting[id] {
			return 1 // Cycles are rejected later as disconnected attachment data.
		}
		visiting[id] = true
		value := 1
		if hint := hints[id]; hint.AttachTo != nil {
			value = visit(hint.AttachTo.ComponentID) + 1
		}
		visiting[id] = false
		depth[id] = value
		return value
	}
	for _, id := range members {
		visit(id)
	}
	return depth
}

func (s *schematicRepairSearch) participates(id string, conflict *schematicRouteConflict) bool {
	if !conflict.ownersComplete {
		return true // Unknown ownership disables pruning in either pass.
	}
	ref := s.measured[id].Designator
	if s.focused {
		return conflict.blockers[ref]
	}
	return conflict.endpointOwners[ref] || conflict.blockers[ref]
}

// A failed greedy branch must leave budget for relocation. All nested naming,
// escape and compaction attempts debit this slice, then the same shared budget.
func (s *schematicRepairSearch) sliceBudget() int {
	quota := s.initial / 8
	if quota < 512 {
		quota = 512
	}
	if quota > 16384 {
		quota = 16384
	}
	if quota > *s.budget {
		quota = *s.budget
	}
	return quota
}
