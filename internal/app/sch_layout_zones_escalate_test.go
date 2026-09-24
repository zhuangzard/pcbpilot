package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSchematicBudgetStopClassifiesOnlyBoundedSearchStops(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("zone: %w", errLibLayoutBudget),
		errors.New("allowed-pose feasibility failed: 17 attempts, stop=candidate-budget-exhausted"),
		errors.New("local search failed after 200000 candidates: candidate search budget exhausted"),
	} {
		if !schematicBudgetStop(err) {
			t.Fatalf("budget stop not recognized: %v", err)
		}
	}
	for _, err := range []error{
		errors.New("annealing placer: 3 peripherals have no connected host"),
		errors.New("unknown/cross-zone attachment R1"),
		errors.New("peripheral-direct-missing: module MCU"),
	} {
		if schematicBudgetStop(err) {
			t.Fatalf("structural failure must not be retried: %v", err)
		}
	}
}

func TestSchematicZonesCeilingValidation(t *testing.T) {
	spacing := 10.0
	for _, c := range []struct{ max, ceil int }{{200000, 100000}, {200000, 5000000}} {
		_, err := PlanSchematicZones(SchematicZonesInput{SchemaVersion: 1, Spacing: &spacing, MaxCandidates: c.max, MaxCandidatesCeiling: c.ceil,
			Components: []SchematicLayoutComponent{}, Zones: []SchematicZone{}})
		if err == nil || !strings.Contains(err.Error(), "maxCandidatesCeiling") {
			t.Fatalf("ceiling %d with max %d accepted: %v", c.ceil, c.max, err)
		}
	}
}
