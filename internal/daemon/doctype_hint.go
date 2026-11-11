package daemon

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

// docTypeProbeTimeout bounds the one light read taken after a failure.
const docTypeProbeTimeout = 3 * time.Second

// hostCallFailureCodes are the codes a handler produces when the host API
// throws — what a wrong-document null host object looks like. Daemon-side
// refusals (geometry guard, save confirmation) never reached the host and are
// not re-diagnosed.
var hostCallFailureCodes = map[string]bool{"EDA_CALL_FAILED": true, "INTERNAL_ERROR": true}

// explainDocTypeMismatch turns a failed pcb.*/schematic.* action that ran
// against the wrong foreground document into an actionable error.
//
// Upstream live report 2026-10-13: `pcb list` with schematic P1 in front failed as
// "Cannot read properties of null (reading 'map')", and the health tracker
// then advised a DEGRADED connector + doc reload — neither points at the real
// fix (`--doc PCB1`). Only failures pay for the probe: one document.current
// read. A read action on the wrong document mutated nothing, so it becomes
// PRECONDITION_REFUSED and stays out of write-health sampling; a mutating
// action keeps its code (zero mutation is not proven) and only gains the hint.
func explainDocTypeMismatch(ctx context.Context, req *protocol.Request, resp *protocol.Response, dispatch dispatchFn) {
	if resp == nil || resp.OK || resp.Error == nil || !hostCallFailureCodes[resp.Error.Code] {
		return
	}
	want := docTypeForAction(req.Action)
	if want == "" {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, docTypeProbeTimeout)
	defer cancel()
	cur, err := dispatch(probeCtx, protocol.Request{
		Envelope: protocol.Envelope{ID: req.ID + "_doctype", Type: protocol.TypeRequest, Version: "v1",
			WindowID: req.WindowID, CreatedAt: time.Now().UTC()},
		Action: "document.current", TimeoutMs: int(docTypeProbeTimeout / time.Millisecond),
	})
	if err != nil || cur == nil || !cur.OK {
		return
	}
	got, _ := cur.Result["documentType"].(string)
	if got == "" || got == want {
		return
	}
	uuid, _ := cur.Result["uuid"].(string)
	original := resp.Error.Code + ": " + resp.Error.Message
	if resp.Error.Detail != "" {
		original += " — " + resp.Error.Detail
	}
	resp.Error.Message = fmt.Sprintf("%s needs a %s document, but the foreground document is %s %s. "+
		"Retry with --doc set to the target %s name or uuid (list them with `pcbpilot doc ls`), or run `pcbpilot doc switch` to it first.",
		req.Action, want, got, strconv.Quote(uuid), want)
	resp.Error.Detail = "original error: " + original
	if !mutatesAction[req.Action] {
		resp.Error.Code = "PRECONDITION_REFUSED"
	}
}
