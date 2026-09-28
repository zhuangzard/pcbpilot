package daemon

// Update awareness inside the daemon:
//
//   - activity: in-flight client actions, the last action time, and windows with
//     unsaved edits (a successful mutating action not yet followed by a
//     successful *.save — the daemon's autosave state). The self-updater only
//     applies a release when there is none of the above. The connector does not
//     report the editor's own dirty flag, so edits made by hand in the GUI are
//     not visible here.
//   - the connector version gate (user decision 2026-09-28): a window whose
//     connector release differs from the daemon release may only run the
//     read-only diagnosis actions until a matching connector connects. Dev
//     builds on either side are exempt; PCBPILOT_ALLOW_VERSION_SKEW=1 on the
//     daemon process disables the gate for development.

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/protocol"
	"github.com/zhuangzard/pcbpilot/internal/selfupdate"
)

// AllowVersionSkewEnv disables the connector version gate (development only).
const AllowVersionSkewEnv = "PCBPILOT_ALLOW_VERSION_SKEW"

// Activity is the daemon's view of what an update/restart would interrupt.
type Activity struct {
	InFlight       int       `json:"inFlight"`
	LastActionAt   time.Time `json:"lastActionAt,omitempty"`
	UnsavedWindows []string  `json:"unsavedWindows,omitempty"`
}

type activityTracker struct {
	mu      sync.Mutex
	last    time.Time
	unsaved map[string]bool
}

func newActivityTracker() *activityTracker {
	return &activityTracker{unsaved: map[string]bool{}}
}

func (a *activityTracker) touch(now time.Time) (idleFor time.Duration) {
	if a == nil {
		return -1
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.last.IsZero() {
		idleFor = now.Sub(a.last)
	} else {
		idleFor = -1
	}
	a.last = now
	return idleFor
}

// observe records a finished action on a window.
func (a *activityTracker) observe(req *protocol.Request, ok bool) {
	if a == nil || req == nil || !ok || req.WindowID == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case strings.HasSuffix(req.Action, ".save"):
		delete(a.unsaved, req.WindowID)
	case requestMutates(req):
		a.unsaved[req.WindowID] = true
	}
}

// Activity reports in-flight actions, the last action time and unsaved windows
// (restricted to windows still connected).
func (s *Server) Activity() Activity {
	var act Activity
	s.clientInflight.Range(func(_, v any) bool {
		if c, ok := v.(interface{ Load() int64 }); ok {
			act.InFlight += int(c.Load())
		}
		return true
	})
	if s.activity == nil || s.hub == nil {
		return act
	}
	live := map[string]bool{}
	for _, w := range s.hub.list() {
		live[w.WindowID] = true
	}
	s.activity.mu.Lock()
	act.LastActionAt = s.activity.last
	for id := range s.activity.unsaved {
		if live[id] {
			act.UnsavedWindows = append(act.UnsavedWindows, id)
		}
	}
	s.activity.mu.Unlock()
	sort.Strings(act.UnsavedWindows)
	return act
}

// connectorGateAllowed are the read-only diagnosis actions a misaligned
// connector may still run (system.page_reload lets the user reload after the
// import without touching EasyEDA by hand).
func connectorGateAllowed(action string) bool {
	if strings.HasPrefix(action, "system.") {
		return true
	}
	switch action {
	case "project.current", "document.current":
		return true
	}
	return false
}

func versionSkewAllowed() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(AllowVersionSkewEnv))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ConnectorMisaligned reports whether a connector release differs from the
// daemon release. Dev builds (git-describe stamps, -dev.N) and unknown
// versions on either side are never misaligned.
func ConnectorMisaligned(connector, daemonVersion string) bool {
	if !isCleanRelease(daemonVersion) || !isCleanRelease(strings.TrimSpace(connector)) {
		return false
	}
	return semverCore(connector) != semverCore(daemonVersion)
}

// connectorGate returns the refusal for a design action on a misaligned
// window, or nil.
func (s *Server) connectorGate(req *protocol.Request, connectorVersion string) *protocol.Response {
	if versionSkewAllowed() || connectorGateAllowed(req.Action) || !ConnectorMisaligned(connectorVersion, s.opts.Version) {
		return nil
	}
	want := semverCore(s.opts.Version)
	path := selfupdate.ConnectorPath(want)
	if _, err := os.Stat(path); err != nil {
		path = selfupdate.ReleaseAssetURL(want, selfupdate.ConnectorAsset) + " (not downloaded yet)"
	}
	steps := selfupdate.ConnectorImportSteps(path)
	detail := fmt.Sprintf("connector v%s ≠ pcbpilot v%s. Design actions are paused until the matching connector is imported "+
		"(this unblocks automatically when it reconnects — no restart). Tell the user:\n  1. %s\n  2. %s\n  3. %s",
		semverCore(connectorVersion), want, steps[0], steps[1], steps[2])
	resp := errorResponse(req.ID, "CONNECTOR_VERSION_MISMATCH",
		fmt.Sprintf("connector v%s does not match pcbpilot v%s — import the new connector first", semverCore(connectorVersion), want), detail)
	return &resp
}

// UpdatesProvider supplies the /health "updates" block and the flat
// updateAvailable {current, latest, releaseUrl} (nil when none). It receives
// the connected windows so it can add the connector-alignment notice.
type UpdatesProvider func(windows []Window) (updates any, available any)
