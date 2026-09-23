package daemon

import (
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/protocol"
	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

func TestWorkflowStagesDoNotAuthorizeTypedActions(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	s := New(Options{})
	req := &protocol.Request{Action: "pcb.line.create", Project: "fresh-project"}
	req.ID = "req_no_stage_gate"

	if resp := s.checkStageGate(req); resp != nil {
		t.Fatalf("routing must not be refused by workflow stage state: %+v", resp.Error)
	}
}

func TestTypedActionsDoNotInvalidateHistoricalStages(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	s := New(Options{})
	st, err := workflow.Load("history-project")
	if err != nil {
		t.Fatal(err)
	}
	st.Confirm(workflow.StagePlacementConfirmed, "confirm", "historical note")
	st.Confirm(workflow.StageOutlineConfirmed, "confirm", "historical note")
	st.Confirm(workflow.StagePreRoutePassed, "gate-pass", "historical note")
	if err := workflow.Save(st); err != nil {
		t.Fatal(err)
	}

	resp := &protocol.Response{OK: true}
	s.maybeInvalidateStage(&protocol.Request{Action: "pcb.components.move", Project: "history-project"}, resp)
	got, err := workflow.Load("history-project")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Has(workflow.StagePlacementConfirmed) || !got.Has(workflow.StageOutlineConfirmed) || !got.Has(workflow.StagePreRoutePassed) {
		t.Fatalf("typed action must leave historical stage records untouched: %+v", got.Confirmed)
	}
	if len(resp.Warnings) != 0 {
		t.Fatalf("stage invalidation warnings are obsolete, got %v", resp.Warnings)
	}
}
