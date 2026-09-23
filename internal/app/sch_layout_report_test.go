package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLayoutPlanMachineReportPreservesFailureContract(t *testing.T) {
	for _, scenario := range []string{"success", "decode-failure", "budget-failure"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			from, out, report := filepath.Join(dir, "input.json"), filepath.Join(dir, "layout.json"), filepath.Join(dir, "report.json")
			input := standaloneLayoutFixture()
			if scenario == "budget-failure" {
				input.MaxCandidates = 1
			}
			raw, _ := json.Marshal(input)
			if scenario == "decode-failure" {
				raw = []byte(`{"schemaVersion":1}`)
			}
			if err := os.WriteFile(from, raw, 0600); err != nil {
				t.Fatal(err)
			}
			previous := []byte("previous validated layout")
			if err := os.WriteFile(out, previous, 0600); err != nil {
				t.Fatal(err)
			}
			var stdout bytes.Buffer
			cmd := newSchLayoutPlanCmd(&stdout)
			cmd.SetArgs([]string{"--from", from, "--out", out, "--report", report})
			err := cmd.Execute()
			if (err == nil) != (scenario == "success") {
				t.Fatalf("%s: %v", scenario, err)
			}
			data, readErr := os.ReadFile(report)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var r schLayoutReport
			if err := json.Unmarshal(data, &r); err != nil {
				t.Fatal(err)
			}
			if r.SourceSHA256 != sha256Hex(raw) || r.Scope != "offline-layout-only" || r.GlobalInfeasibilityProven {
				t.Fatalf("bad evidence %+v", r)
			}
			if scenario == "success" {
				if r.Status != "planned" || r.Phase != "complete" || len(r.Diagnostics) == 0 {
					t.Fatalf("bad success %+v", r)
				}
			} else {
				if r.Status != "failed" || r.Error == "" {
					t.Fatalf("failure disguised: %+v", r)
				}
				wantPhase := "solve"
				if scenario == "decode-failure" {
					wantPhase = "decode"
				}
				if r.Phase != wantPhase {
					t.Fatalf("phase=%s want=%s", r.Phase, wantPhase)
				}
				wantClass := "candidate-budget-exhausted"
				if scenario == "decode-failure" {
					wantClass = "data-missing"
				}
				if r.FailureClass != wantClass {
					t.Fatalf("failureClass=%s want=%s", r.FailureClass, wantClass)
				}
				kept, _ := os.ReadFile(out)
				if !bytes.Equal(kept, previous) || stdout.Len() != 0 {
					t.Fatal("failure overwrote prior layout or emitted a partial plan")
				}
			}
			keptSource, _ := os.ReadFile(from)
			if !bytes.Equal(keptSource, raw) {
				t.Fatal("source mutated")
			}
		})
	}
}

func TestLayoutReportClassifiesRoutingFailureAndDuration(t *testing.T) {
	for _, kind := range []string{"data-missing", "no-path-within-bounds", "expanded-node-budget-exhausted", "final-validation-failed"} {
		failure := &schematicRoutingFailure{Kind: kind, Net: "N", SourceIsland: "a", TargetIsland: "b",
			Routing: &SchematicRoutingDiagnostics{duration: 1500 * time.Millisecond}, cause: errors.New("fixture")}
		if got := schLayoutFailureClass(fmt.Errorf("wrapped: %w", failure), "solve"); got != kind {
			t.Fatalf("kind %s classified as %s", kind, got)
		}
		if got := schLayoutFailureRoutingDuration(failure); got != 1500*time.Millisecond {
			t.Fatalf("duration=%s", got)
		}
	}
	if got := schLayoutFailureClass(fmt.Errorf("outer: %w", errSchematicExpandedBudget), "solve"); got != "expanded-node-budget-exhausted" {
		t.Fatalf("bare expanded-node sentinel classified as %s", got)
	}
}

func TestSchematicRoutingJSONRequiresPositiveExplicitReroutes(t *testing.T) {
	for raw, wantErr := range map[string]bool{
		`{}`: false, `{"maxReroutes":1}`: false, `{"maxReroutes":0}`: true,
		`{"maxReroutes":33}`: true, `{"maxExpandedNodes":0}`: true, `null`: true,
	} {
		err := validateSchematicRoutingJSON(map[string]json.RawMessage{"routing": json.RawMessage(raw)})
		if (err != nil) != wantErr {
			t.Fatalf("routing=%s err=%v wantErr=%v", raw, err, wantErr)
		}
	}
}

type reportFixtureError struct{}

func (reportFixtureError) Error() string { return "opaque failure message" }
func (reportFixtureError) FailureDetails() any {
	return map[string]any{"type": "placement-conflict", "componentId": "opaque-id", "blockerOwners": []string{"other-id"}}
}

func TestLayoutReportReadsTypedDetailsWithoutMessageParsing(t *testing.T) {
	for _, cause := range []error{
		fmt.Errorf("wrapper: %w", reportFixtureError{}),
		fmt.Errorf("%w: %w", errors.New("branch limit"), fmt.Errorf("inner: %w", reportFixtureError{})),
		errors.Join(nil, errors.New("budget limit"), reportFixtureError{}),
	} {
		path := filepath.Join(t.TempDir(), "report.json")
		if err := writeSchLayoutReport(path, []byte("source"), "solve", false, nil, cause); err != nil {
			t.Fatal(err)
		}
		raw, _ := os.ReadFile(path)
		var r schLayoutReport
		if err := json.Unmarshal(raw, &r); err != nil {
			t.Fatal(err)
		}
		if len(r.Diagnostics) != 1 || r.Diagnostics[0].(map[string]any)["componentId"] != "opaque-id" {
			t.Fatalf("lost typed conflict: %s", raw)
		}
	}
}

type reportCycleError struct{}

func (e *reportCycleError) Error() string { return "cycle" }
func (e *reportCycleError) Unwrap() error { return e }

func TestLayoutReportBoundsMalformedErrorTree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.json")
	if err := writeSchLayoutReport(path, nil, "solve", false, nil, &reportCycleError{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var r schLayoutReport
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Diagnostics) != 1 || r.Diagnostics[0].(map[string]any)["type"] != "diagnostic-traversal-limit" {
		t.Fatalf("traversal loss hidden: %s", raw)
	}
}

func TestLayoutReportRealPlacementAndRouteConflicts(t *testing.T) {
	in := dependencyBackjumpFixture()
	in.MaxCandidates = 10
	_, cause := PlanSchematicLayout(in)
	path := filepath.Join(t.TempDir(), "placement.json")
	if err := writeSchLayoutReport(path, nil, "solve", false, nil, cause); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	var r schLayoutReport
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	byKind := map[string]map[string]any{}
	for _, diagnostic := range r.Diagnostics {
		d := diagnostic.(map[string]any)
		if kind, ok := d["kind"].(string); ok {
			byKind[kind] = d
		}
	}
	placement := byKind["placement-conflict"]
	if placement["kind"] != "placement-conflict" || placement["searchExhaustion"] != "candidate-budget" || placement["componentId"] == "" {
		t.Fatalf("incomplete placement diagnosis: %s", raw)
	}
	search := byKind["search-failure"]
	if search["candidatesUsed"] != float64(in.MaxCandidates) || search["remainingCandidates"] != float64(0) || search["globalInfeasibilityProven"] != false {
		t.Fatalf("lost actual search accounting: %s", raw)
	}
	wantBranchLimit := in.MaxCandidates
	if wantBranchLimit < 128 {
		wantBranchLimit = 128
	}
	if search["search"].(map[string]any)["branchLimit"] != float64(wantBranchLimit) {
		t.Fatalf("lost bounded branch contract: %s", raw)
	}
	route := &schematicRouteConflict{net: "OPAQUE_NET", blockers: map[string]bool{"X3": true, "X1": true}, ownersComplete: true}
	if err := writeSchLayoutReport(path, nil, "solve", false, nil, fmt.Errorf("%w: %w", errSchematicRepairBranches, route)); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(path)
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Diagnostics) != 1 || r.Diagnostics[0].(map[string]any)["kind"] != "route-conflict" {
		t.Fatalf("lost route conflict: %s", raw)
	}
}

func TestLayoutReportRefusesPathAliasesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	from := filepath.Join(dir, "source.json")
	raw, _ := json.Marshal(standaloneLayoutFixture())
	if err := os.WriteFile(from, raw, 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias.json")
	if err := os.Link(from, alias); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{from, alias} {
		cmd := newSchLayoutPlanCmd(&bytes.Buffer{})
		cmd.SetArgs([]string{"--from", from, "--report", path})
		if err := cmd.Execute(); err == nil {
			t.Fatal("report overwrote input alias")
		}
	}
	kept, _ := os.ReadFile(from)
	if !bytes.Equal(kept, raw) {
		t.Fatal("source changed")
	}
	if err := schLayoutReportPaths(from, filepath.Join(dir, "layout.json"), filepath.Join(dir, "layout.json")); err == nil {
		t.Fatal("report/output alias accepted")
	}
	linkedDir := filepath.Join(dir, "linked-dir")
	if err := os.Symlink(dir, linkedDir); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "not-created.json")
	if err := schLayoutReportPaths(from, out, filepath.Join(linkedDir, "not-created.json")); err == nil {
		t.Fatal("not-yet-created output/report directory aliases accepted")
	}
	dangling := filepath.Join(dir, "dangling.json")
	if err := os.Symlink(out, dangling); err != nil {
		t.Fatal(err)
	}
	if err := schLayoutReportPaths(from, out, dangling); err == nil {
		t.Fatal("dangling output/report alias accepted")
	}
}

func TestLayoutReportWriteFailureDoesNotSuppressOriginalFailure(t *testing.T) {
	dir := t.TempDir()
	from, report := filepath.Join(dir, "missing-input.json"), filepath.Join(dir, "missing", "report.json")
	cmd := newSchLayoutPlanCmd(&bytes.Buffer{})
	cmd.SetArgs([]string{"--from", from, "--report", report})
	err := cmd.Execute()
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original/report IO failure hidden: %v", err)
	}
	paths := map[string]bool{}
	walkSchLayoutErrors(err, func(e error) {
		if p, ok := e.(*os.PathError); ok {
			paths[p.Path] = true
		}
	})
	if !paths[from] || !paths[report] {
		t.Fatalf("lost one of original/report failures: %v", err)
	}
}
