package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

func foregroundDispatch(docType string, calls *int) dispatchFn {
	return func(_ context.Context, req protocol.Request) (*protocol.Response, error) {
		*calls++
		if req.Action != "document.current" {
			panic("unexpected probe " + req.Action)
		}
		return &protocol.Response{OK: true, Result: map[string]any{"documentType": docType, "uuid": "cb1db9305eeaf3c1"}}, nil
	}
}

func failed(code, msg string) *protocol.Response {
	return &protocol.Response{OK: false, Error: &protocol.ErrorInfo{Code: code, Message: msg, Detail: "Cannot read properties of null (reading 'map')"}}
}

func TestExplainDocTypeMismatch(t *testing.T) {
	t.Run("read on schematic becomes actionable refusal", func(t *testing.T) {
		calls := 0
		resp := failed("EDA_CALL_FAILED", "Failed to list PCB components.")
		explainDocTypeMismatch(context.Background(), &protocol.Request{Action: "pcb.components.list"}, resp, foregroundDispatch("schematic", &calls))
		if resp.Error.Code != "PRECONDITION_REFUSED" || !requestRefusalCodes[resp.Error.Code] {
			t.Fatalf("code = %q, want PRECONDITION_REFUSED (excluded from write health)", resp.Error.Code)
		}
		if !strings.Contains(resp.Error.Message, "--doc") || !strings.Contains(resp.Error.Message, "schematic") {
			t.Fatalf("message lacks the actionable fix: %q", resp.Error.Message)
		}
		if !strings.Contains(resp.Error.Detail, "EDA_CALL_FAILED") || !strings.Contains(resp.Error.Detail, "reading 'map'") {
			t.Fatalf("original error not preserved: %q", resp.Error.Detail)
		}
	})
	t.Run("mutation keeps its code but gains the hint", func(t *testing.T) {
		calls := 0
		resp := failed("EDA_CALL_FAILED", "Failed to create via.")
		explainDocTypeMismatch(context.Background(), &protocol.Request{Action: "pcb.via.create"}, resp, foregroundDispatch("schematic", &calls))
		if resp.Error.Code != "EDA_CALL_FAILED" || !strings.Contains(resp.Error.Message, "--doc") {
			t.Fatalf("got %q / %q", resp.Error.Code, resp.Error.Message)
		}
	})
	t.Run("matching document leaves the error untouched", func(t *testing.T) {
		calls := 0
		resp := failed("EDA_CALL_FAILED", "Failed to list PCB components.")
		explainDocTypeMismatch(context.Background(), &protocol.Request{Action: "pcb.components.list"}, resp, foregroundDispatch("pcb", &calls))
		if resp.Error.Code != "EDA_CALL_FAILED" || resp.Error.Message != "Failed to list PCB components." {
			t.Fatalf("matching doc must not rewrite: %+v", resp.Error)
		}
	})
	t.Run("success and domain-agnostic actions never probe", func(t *testing.T) {
		calls := 0
		explainDocTypeMismatch(context.Background(), &protocol.Request{Action: "pcb.components.list"}, &protocol.Response{OK: true}, foregroundDispatch("schematic", &calls))
		explainDocTypeMismatch(context.Background(), &protocol.Request{Action: "project.current"}, failed("EDA_CALL_FAILED", "x"), foregroundDispatch("schematic", &calls))
		if calls != 0 {
			t.Fatalf("probed %d times; only failed pcb/schematic actions may probe", calls)
		}
	})
}
