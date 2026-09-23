package daemon

import "github.com/zhuangzard/pcbpilot/internal/protocol"

// Workflow stage files are retained for historical reporting and compatibility
// with `pcbpilot workflow` / `pcbpilot pcb stage`. They are no longer an
// authorization mechanism for typed actions. These no-op hooks intentionally
// remain source-compatible with older integrations while dispatch proceeds from
// live document evidence.
func (s *Server) checkStageGate(_ *protocol.Request) *protocol.Response { return nil }

// A successful action no longer cascades into stage invalidation or demands a
// new sign-off. Explicit workflow commands may still edit their own records.
func (s *Server) maybeInvalidateStage(_ *protocol.Request, _ *protocol.Response) {}
