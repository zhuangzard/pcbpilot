package app

import (
	"errors"
)

var errLibLayoutBudget = errors.New("candidate search budget exhausted")

// schematicSearchDefaultWindow is the candidate search's historical default
// allowance; fallbacks never shrink the search below it.
const schematicSearchDefaultWindow = 20000

// annealHook observes the annealer's outcome (diagnostics).
var annealHook func(error)

func solveSchematicLayout(input SchematicLayoutInput, measured map[string]powerLayoutPlacement, members []string, hints map[string]SchematicLayoutPeripheral, budget *int, routing *schematicRoutingContext) (*SchematicLayoutResult, error) {
	core := measured[input.CoreComponentID]
	core = plTranslate(core, -core.X, -core.Y)
	p := powerLayoutPlan{Placements: []powerLayoutPlacement{core}}
	pending := []string{}
	for _, id := range members {
		if id != input.CoreComponentID {
			pending = append(pending, id)
		}
	}
	if !schematicAnnealDisabled && len(pending) >= annealMinPeripherals {
		// The annealer may spend at most 60 % of the budget; the rest stays for
		// the candidate search if it does not converge.
		share := *budget * 3 / 5
		local := share
		out, err := solveSchematicLayoutAnneal(input, measured, members, hints, &local, routing)
		*budget -= share - max(local, 0)
		if annealHook != nil {
			annealHook(err)
		}
		if err == nil {
			return out, nil
		}
		if *budget <= 0 {
			return nil, &SchematicLayoutSearchFailure{CandidatesUsed: 0, RemainingCandidates: 0, cause: errLibLayoutBudget}
		}
		// Fall back to the candidate search with what is left.
	}
	if !schematicAnnealDisabled && len(pending) >= annealFallbackMinPeripherals && len(pending) < annealMinPeripherals && *budget >= annealFallbackMinBudget {
		// Smaller zones: the candidate search first (its results are pinned by
		// many fixtures) on half the budget; if it cannot finish, the annealer
		// gets the other half (USB-C/CH340C zone, 5 peripherals: the search
		// exhausted 20 000 candidates on "+3V3 has no safe naming lead").
		// Two-peripheral zones (added to the fallback later) keep the
		// search's default 20k window: stress L1 showed ESP32 UART/USB_CONN
		// failing when the fallback took half of 20k, while 3-5 peripheral
		// zones (AT32 CAN) need the annealer's half at 20k. Both measured.
		half := *budget / 2
		if len(pending) < 3 {
			half = max(half, min(*budget, schematicSearchDefaultWindow))
		}
		rest := *budget - half
		s := newSchematicRepairSearch(input, measured, members, hints, &half, routing)
		out, err := s.solve(p, pending)
		*budget = rest + max(half, 0)
		if err == nil {
			// The search does not enforce the zone's peripheral-direct gate
			// (the annealer does); a layout that names a decoupler's rail with
			// a flag instead of wiring it would be rejected after solving.
			// Treat that as a search failure and let the annealer try.
			probe := input
			probe.NetPolicies = map[string]string{}
			for k, v := range input.NetPolicies {
				probe.NetPolicies[k] = v
			}
			check := *out
			if len(check.ComponentIDs) == 0 {
				check.ComponentIDs = map[string]string{}
				for id, m := range measured {
					check.ComponentIDs[m.Designator] = id
				}
			}
			if perr := validateSchematicLayoutPeripheralDirect(&check, input.CoreComponentID, schematicMandatoryPeripheralSignalPolicies(&probe)); perr == nil {
				return out, nil
			} else {
				err = perr
			}
		}
		local := *budget
		aout, aerr := solveSchematicLayoutAnneal(input, measured, members, hints, &local, routing)
		*budget = max(local, 0)
		if annealHook != nil {
			annealHook(aerr)
		}
		if aerr == nil {
			return aout, nil
		}
		return nil, err
	}
	s := newSchematicRepairSearch(input, measured, members, hints, budget, routing)
	return s.solve(p, pending)
}

// The final wiring/naming gate is also inside the checkpoint search: a legal
// placement prefix is not sufficient if its finished routes trap a later net.
func libFinishSchematicLayout(p powerLayoutPlan, netPolicies map[string]string, budget *int, routingArg ...*schematicRoutingContext) (*powerLayoutPlan, error) {
	return libFinishSchematicLayoutMode(p, netPolicies, budget, false, routingArg...)
}

// Placement routes are provisional geometry probes. The terminal solver must
// be able to withdraw every one of them and route the complete wire forest in
// a new order; otherwise an early attachment can permanently fence a sibling
// pin even though the component placement itself is routable.
func libFinishSchematicLayoutRegenerate(p powerLayoutPlan, netPolicies map[string]string, budget *int, routingArg ...*schematicRoutingContext) (*powerLayoutPlan, error) {
	return libFinishSchematicLayoutMode(p, netPolicies, budget, true, routingArg...)
}

func libFinishSchematicLayoutMode(p powerLayoutPlan, netPolicies map[string]string, budget *int, regenerate bool, routingArg ...*schematicRoutingContext) (*powerLayoutPlan, error) {
	var err error
	var routing *schematicRoutingContext
	if len(routingArg) > 0 {
		routing = routingArg[0]
	}
	if routing != nil {
		previous := routing.candidateBudget
		previousReserve := routing.namingReserve
		routing.candidateBudget = budget
		// Route-level lookahead and final naming share one candidate slice. Keep
		// enough candidates for the latter to expose a concrete sealed island,
		// even when many route alternatives each need a marker probe.
		routing.namingReserve = *budget / 4
		if routing.namingReserve > 4096 {
			routing.namingReserve = 4096
		}
		defer func() {
			routing.candidateBudget = previous
			routing.namingReserve = previousReserve
		}()
	}
	p.Flags = nil
	if regenerate {
		p.Wires = nil
	}
	base := p
	base.Wires = clonePowerLayoutWires(p.Wires)
	base.Flags = append([]powerLayoutFlag(nil), p.Flags...)
	finish := func(trial *powerLayoutPlan) error {
		if e := libNameIslands(trial, netPolicies, budget); e != nil {
			return e
		}
		libCompactMarkerEnvelope(trial, budget)
		if e := validateLibGeometry(trial); e != nil {
			return e
		}
		return validateSchCompositionNets(trial)
	}
	directFirstTried := false
	if libPreferDirectFirst(&base, netPolicies) {
		directFirstTried = true
		trial := base
		trial.Wires = clonePowerLayoutWires(base.Wires)
		err = libJoinDirectNets(&trial, netPolicies, routing)
		if err == nil {
			err = libJoinNearbyRails(&trial, netPolicies)
		}
		if err == nil {
			err = finish(&trial)
		}
		if err == nil {
			return &trial, nil
		}
		if errors.Is(err, errLibLayoutBudget) {
			return nil, err
		}
		var routingFailure *schematicRoutingFailure
		if errors.As(err, &routingFailure) && routingFailure.Kind == "expanded-node-budget-exhausted" {
			return nil, err
		}
		if routing != nil && !routing.beginReroute() {
			return nil, err
		}
	}

	// Preserve the established rail-first fast path. If and only if mandatory
	// direct routing reports a real route conflict, withdraw the entire forest
	// and spend one bounded reroute on direct-first. This prevents an optional
	// early rail from fencing a pin without multiplying every successful layout
	// candidate into four speculative naming passes.
	if err = libJoinNearbyRails(&p, netPolicies); err != nil {
		return nil, err
	}
	beforePortJoins := p
	beforePortJoins.Wires = clonePowerLayoutWires(p.Wires)
	err = libJoinDirectNets(&p, netPolicies, routing)
	if err != nil {
		var conflict *schematicRouteConflict
		var routingFailure *schematicRoutingFailure
		if errors.As(err, &routingFailure) && routingFailure.Kind == "expanded-node-budget-exhausted" {
			return nil, err
		}
		if !directFirstTried && errors.As(err, &conflict) && (routing == nil || routing.beginReroute()) {
			trial := base
			trial.Wires = clonePowerLayoutWires(base.Wires)
			if directErr := libJoinDirectNets(&trial, netPolicies, routing); directErr == nil {
				if directErr = libJoinNearbyRails(&trial, netPolicies); directErr == nil {
					if directErr = finish(&trial); directErr == nil {
						return &trial, nil
					}
				}
				err = directErr
			}
		}
	} else {
		err = finish(&p)
		if err == nil {
			return &p, nil
		}
	}
	if errors.Is(err, errLibLayoutBudget) {
		return nil, err
	}

	// Optional module-port joins can consume a naming corridor. Withdraw them
	// from the rail-first checkpoint, retain mandatory direct joins, and run the
	// same complete data gates again.
	p = beforePortJoins
	p.Wires = clonePowerLayoutWires(beforePortJoins.Wires)
	if err = libJoinNetsMode(&p, netPolicies, false, false, routing); err != nil {
		return nil, err
	}
	if err = finish(&p); err != nil {
		return nil, err
	}
	return &p, nil
}

// Repeated direct-net pins interleaved on one measured symbol side need their
// fanout trees before optional rails claim the shared corridor. Unique nets on
// the same side do not trigger this schedule; ordinary rail-oriented circuits
// retain the established fast path.
func libPreferDirectFirst(p *powerLayoutPlan, policies map[string]string) bool {
	for _, component := range p.Placements {
		bySide := map[string]map[string]int{}
		for _, pin := range component.Pins {
			if pin.Net == "" || !libDirectPolicy(policies[pin.Net]) {
				continue
			}
			side, err := libPinSide(pin, component.BBox)
			if err != nil {
				continue
			}
			if bySide[side] == nil {
				bySide[side] = map[string]int{}
			}
			bySide[side][pin.Net]++
			if bySide[side][pin.Net] > 1 {
				return true
			}
		}
	}
	return false
}
