package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func feasibilityFixture(count int) (SchematicLayoutInput, map[string]powerLayoutPlacement, map[string][]float64) {
	in := SchematicLayoutInput{CoreComponentID: "core"}
	measured, allowed := map[string]powerLayoutPlacement{}, map[string][]float64{}
	for i := 0; i <= count; i++ {
		id := "core"
		if i > 0 {
			id = fmt.Sprintf("peripheral-%d", i)
		}
		p := powerLayoutPlacement{Designator: id, X: float64(i) * 100, Y: 20, Rotation: 270,
			BBox:       layoutBBox{float64(i)*100 - 5, 10, float64(i)*100 + 5, 30},
			Pins:       []powerLayoutPin{{Number: "1", Net: "SUPPLY", X: float64(i) * 100, Y: 40}, {Number: "2", Net: "GND", X: float64(i) * 100, Y: 0}},
			TextBBoxes: []layoutBBox{{float64(i)*100 + 10, 20, float64(i)*100 + 20, 28}}}
		c := SchematicLayoutComponent{ID: id, Measurement: p, PinStates: map[string]string{}}
		allowed[id] = []float64{270}
		if i > 0 {
			c.AllowedRotations = []float64{90, 270}
			allowed[id] = c.AllowedRotations
		}
		in.Components = append(in.Components, c)
		measured[id] = p
	}
	return in, measured, allowed
}

func TestFeasibilityTriesCoordinatedAllowedPoseWithinOriginalBudget(t *testing.T) {
	defer poseMenuOnly()()
	in, measured, allowed := feasibilityFixture(2)
	before, _ := json.Marshal([]any{in, measured})
	budget, calls := 100000, 0
	out, report, err := runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(pose map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
		calls++
		if !reflect.DeepEqual(pose["core"], measured["core"]) {
			t.Fatal("core moved or rotated")
		}
		if calls == 1 {
			if *quota != 75000 {
				t.Fatalf("source pose consumed fallback reservation: %d", *quota)
			}
			*quota = 0
			return nil, errLibLayoutBudget
		}
		for _, id := range []string{"peripheral-1", "peripheral-2"} {
			if !reflect.DeepEqual(pose[id], plRotate(measured[id], 2)) {
				t.Fatal("joint pose missing, or not a rigid transform of original pins/bbox/text/mirror")
			}
		}
		*quota -= 7
		return &SchematicLayoutResult{}, nil
	})
	if err != nil || out == nil || calls != 2 || budget != 24993 || report.CandidatesUsed != 75007 || report.StopReason != "allowed-pose-feasible" || len(report.SelectedRotations) != 2 {
		t.Fatalf("bad feasibility accounting/result: out=%v report=%+v budget=%d err=%v", out, report, budget, err)
	}
	after, _ := json.Marshal([]any{in, measured})
	if string(before) != string(after) {
		t.Fatal("source geometry or permissions changed")
	}
}

func TestFeasibilityKeepsValidSourceAndDoesNotProbeAlternates(t *testing.T) {
	in, measured, allowed := feasibilityFixture(2)
	budget, calls := 100, 0
	_, report, err := runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(pose map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
		calls++
		if *quota != 100 {
			t.Fatal("small source budget was truncated to reserve optional poses")
		}
		if !reflect.DeepEqual(pose, measured) {
			t.Fatal("source pose was not first")
		}
		*quota -= 2
		return &SchematicLayoutResult{}, nil
	})
	if err != nil || calls != 1 || budget != 98 || report.StopReason != "source-pose-feasible" {
		t.Fatal(calls, budget, report, err)
	}
}

func TestFeasibilityFixedPoseRetainsExistingContract(t *testing.T) {
	in, measured, allowed := feasibilityFixture(2)
	for i := range in.Components {
		in.Components[i].AllowedRotations = nil
	}
	budget, calls := 100, 0
	out, report, err := runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(pose map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
		calls++
		if *quota != 100 || !reflect.DeepEqual(pose, measured) {
			t.Fatal("unapproved pose changed original contract")
		}
		*quota = 0
		return nil, errLibLayoutBudget
	})
	if out != nil || report != nil || calls != 1 || !errors.Is(err, errLibLayoutBudget) {
		t.Fatal(out, report, calls, err)
	}
}

func TestFeasibilityBudgetCannotResetAcrossFailedPoses(t *testing.T) {
	defer poseMenuOnly()()
	in, measured, allowed := feasibilityFixture(2)
	budget, allocated := 100000, 0
	out, report, err := runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(_ map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
		allocated += *quota
		*quota = 0
		return nil, errLibLayoutBudget
	})
	var failure *schematicFeasibilityError
	if out != nil || !errors.As(err, &failure) || !errors.Is(err, errLibLayoutBudget) || allocated != 100000 || budget != 0 || report.CandidatesUsed != 100000 || len(report.Attempts) != 4 || report.StopReason != "candidate-budget-exhausted" {
		t.Fatal(out, report, allocated, budget, err)
	}
	for _, attempt := range report.Attempts {
		if attempt.Failure == "" || attempt.CandidatesUsed > attempt.CandidateQuota {
			t.Fatal("missing failure trace or budget overrun", attempt)
		}
	}
}

func TestFeasibilityIncludesIndividualAndPairSubsetPoses(t *testing.T) {
	for _, count := range []int{2, 4} {
		in, measured, allowed := feasibilityFixture(count)
		budget := 10000
		_, report, err := runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(pose map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
			*quota--
			want := map[string]bool{"peripheral-1": true}
			if count == 4 {
				want["peripheral-2"] = true
			}
			for id, part := range pose {
				if (part.Rotation == 90) != want[id] {
					return nil, fmt.Errorf("not the feasible subset")
				}
			}
			return &SchematicLayoutResult{}, nil
		})
		if err != nil || report.StopReason != "allowed-pose-feasible" || len(report.Attempts) < 3 {
			t.Fatal(count, report, err)
		}
	}
}

func TestFeasibilityPoseMenuBoundedAndDeterministic(t *testing.T) {
	in, _, allowed := feasibilityFixture(20)
	a, truncated := schematicFeasibilityPoses(in, allowed)
	b, again := schematicFeasibilityPoses(in, allowed)
	if !truncated || !again || len(a) != schematicFeasibilityPoseLimit || !reflect.DeepEqual(a, b) || len(a[0]) != 20 {
		t.Fatal("pose limit, joint candidate, or determinism broken", len(a), truncated)
	}
	for _, pose := range a {
		if _, exists := pose["core"]; exists {
			t.Fatal("core permission leaked into alternatives")
		}
	}
}

func TestFeasibilityPreservesDefaultSourceWindowAndReturnsUnusedQuota(t *testing.T) {
	defer poseMenuOnly()()
	in, measured, allowed := feasibilityFixture(2)
	budget, calls := 30000, 0
	_, report, err := runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(_ map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
		calls++
		if calls == 1 {
			if *quota != 20000 {
				t.Fatalf("default source window changed: %d", *quota)
			}
			*quota -= 100
			return nil, errors.New("early placement failure")
		}
		if *quota != 29900/3 {
			t.Fatalf("unused source quota was not returned: %d", *quota)
		}
		*quota -= 7
		return &SchematicLayoutResult{}, nil
	})
	if err != nil || calls != 2 || budget != 29893 || report.CandidatesUsed != 107 {
		t.Fatal(calls, budget, report, err)
	}
}

func TestFeasibilityErrorPreservesEveryAttemptInCLIText(t *testing.T) {
	in, measured, allowed := feasibilityFixture(2)
	budget, calls := 100, 0
	_, report, err := runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(_ map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
		calls++
		*quota--
		return nil, fmt.Errorf("pose failure %d", calls)
	})
	if err == nil || report.StopReason != "pose-menu-exhausted" || budget != 96 {
		t.Fatal(budget, report, err)
	}
	for i := 1; i <= 4; i++ {
		if !strings.Contains(err.Error(), fmt.Sprintf("pose failure %d", i)) {
			t.Fatalf("CLI error lost attempt %d: %v", i, err)
		}
	}
	if !strings.Contains(err.Error(), "no global feasibility proof") {
		t.Fatal("bounded failure overclaims infeasibility", err)
	}
}

func TestFeasibilityRetainsTypedDiagnosticsForEarlierFailedPoses(t *testing.T) {
	in, measured, allowed := feasibilityFixture(2)
	budget, calls := 100, 0
	_, report, err := runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(_ map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
		calls++
		*quota--
		if calls == 3 {
			return &SchematicLayoutResult{}, nil
		}
		return nil, fmt.Errorf("%w: %w", errSchematicRepairFocus, reportFixtureError{})
	})
	if err != nil || len(report.Attempts) != 3 {
		t.Fatal(report, err)
	}
	for _, attempt := range report.Attempts[:2] {
		if len(attempt.Diagnostics) != 1 || attempt.Diagnostics[0].(map[string]any)["componentId"] != "opaque-id" {
			t.Fatalf("earlier failed pose lost typed conflict: %+v", attempt)
		}
	}
	if len(report.Attempts[2].Diagnostics) != 0 || budget != 97 {
		t.Fatal("success retained stale conflict or budget changed", report, budget)
	}
}

// poseMenuOnly pins the pose-menu contract by switching off the leading
// measured-pose attempt (tested separately below).
func poseMenuOnly() func() {
	old := schematicPoseMenuDefersToAnneal
	schematicPoseMenuDefersToAnneal = false
	return func() { schematicPoseMenuDefersToAnneal = old }
}

func TestFeasibilityLeadsWithHalfBudgetOnMeasuredPose(t *testing.T) {
	in, measured, allowed := feasibilityFixture(2)
	budget, calls := 100000, 0
	_, report, err := runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(_ map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
		calls++
		if calls == 1 {
			if *quota != 50000 {
				t.Fatalf("leading measured-pose attempt must get half: %d", *quota)
			}
			*quota -= 30000
			return nil, errors.New("measured pose failed")
		}
		*quota -= 10
		return &SchematicLayoutResult{}, nil
	})
	if err != nil || calls != 2 || report == nil || budget != 100000-30000-10 {
		t.Fatal(calls, budget, report, err)
	}
	budget, calls = 100000, 0
	if _, report, err = runSchematicLayoutFeasibility(in, measured, allowed, &budget, func(_ map[string]powerLayoutPlacement, quota *int) (*SchematicLayoutResult, error) {
		calls++
		*quota -= 5
		return &SchematicLayoutResult{}, nil
	}); err != nil || calls != 1 || report != nil || budget != 99995 {
		t.Fatal("a solved measured pose must return without the menu", calls, budget, report, err)
	}
}
