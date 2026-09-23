package app

// This file keeps the source-level helpers introduced while stale PCB reads
// were a hard daemon refusal. Stale reads are now returned with staleRisk
// evidence, so none of these helpers sends a forceReason or unlocks anything.
// Final, authoritative workflows still save, reload, and read back.

import (
	"errors"
	"fmt"
	"time"
)

// staleReadCode remains understood so a current CLI can explain a response
// from an older daemon, but current daemons no longer emit this refusal.
const staleReadCode = "STALE_READ"

var errStaleRead = errors.New(staleReadCode)

type actionError struct {
	Action  string
	Code    string
	Message string
	Detail  string
}

func (e *actionError) Error() string {
	return fmt.Sprintf("%s failed: %s", e.Action, e.Message)
}

func (e *actionError) Is(target error) bool {
	return target == errStaleRead && e.Code == staleReadCode
}

func isStaleRead(err error) bool { return errors.Is(err, errStaleRead) }

func staleReadNextStep(what string) string {
	return fmt.Sprintf("%s 被旧版 daemon 以 STALE_READ 拒绝；升级 daemon，或保存后运行 `pcbpilot doc reload` 再回读", what)
}

// staleReadOptIn is retained for source compatibility. It deliberately returns
// the original config and never puts a bypass reason on the wire.
func staleReadOptIn(cfg *appConfig, _ string) *appConfig { return cfg }

// setDispatchStaleReadReason is retained for in-process apply callers. The
// returned restore function is intentionally a no-op.
func setDispatchStaleReadReason(_ string) (restore func()) { return func() {} }

// staleReadForceReason is kept as a narrow compatibility seam for tests and
// older call sites. A stale-risk advisory is evidence, not a permission state.
func staleReadForceReason(_ *appConfig, _ string, _ any) string { return "" }

func requestReadAfterWrite(cfg *appConfig, action, window string, payload any, _ string) (*actionResult, error) {
	return requestAction(cfg, action, window, payload)
}

func requestReadAfterWriteTimed(cfg *appConfig, action, window string, payload any, _ string, timeout time.Duration) (*actionResult, error) {
	return requestActionTimed(cfg, action, window, payload, timeout)
}
