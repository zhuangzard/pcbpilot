package daemon

import (
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Extension points for the v0.7 console (internal/console). Everything here is
// additive: the daemon's own routes and dispatch path are unchanged, and a
// daemon without a console behaves exactly as before.

// ActionEvent is the public, payload-free view of one audited action — what the
// console's live stream shows. Payload and result bodies are deliberately left
// out (size and privacy); the audit JSONL keeps them.
type ActionEvent struct {
	Timestamp    time.Time `json:"ts"`
	RequestID    string    `json:"requestId"`
	WindowID     string    `json:"windowId,omitempty"`
	ClientID     string    `json:"clientId,omitempty"`
	Action       string    `json:"action"`
	OK           bool      `json:"ok"`
	DurationMs   int64     `json:"durationMs"`
	ErrorCode    string    `json:"errorCode,omitempty"`
	ErrorMsg     string    `json:"errorMsg,omitempty"`
	ProjectUUID  string    `json:"projectUuid,omitempty"`
	ProjectName  string    `json:"projectName,omitempty"`
	DocumentUUID string    `json:"documentUuid,omitempty"`
	DocumentType string    `json:"documentType,omitempty"`
	OutputDir    string    `json:"outputDir,omitempty"`
	// Result carries a few small verdict fields (DRC passed / violation count,
	// saved) so the console can show a project's final status without the
	// full result body.
	Result map[string]any `json:"result,omitempty"`
}

type serverExt struct {
	mu       sync.Mutex
	routes   []extRoute
	sinks    []func(ActionEvent)
	port     atomic.Int64
	started  atomic.Int64 // unix nanos
	sinkOnce sync.Once
	stops    []func()
}

type extRoute struct {
	pattern string
	handler http.Handler
}

func (e *serverExt) bound(port int) {
	e.port.Store(int64(port))
	e.started.Store(time.Now().UnixNano())
}

// Handle mounts an extra handler on the daemon mux. Call before Run.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.ext.mu.Lock()
	defer s.ext.mu.Unlock()
	s.ext.routes = append(s.ext.routes, extRoute{pattern, h})
}

func (s *Server) mountExtensions(mux *http.ServeMux) {
	s.ext.mu.Lock()
	defer s.ext.mu.Unlock()
	for _, r := range s.ext.routes {
		mux.Handle(r.pattern, r.handler)
	}
}

// OnShutdown registers a callback run when the daemon starts shutting down,
// before the HTTP server drains. Extensions with long-lived responses (the
// console's SSE stream) must end them here, or Shutdown waits out its whole
// deadline and the daemon exits with "context deadline exceeded".
func (s *Server) OnShutdown(fn func()) {
	s.ext.mu.Lock()
	s.ext.stops = append(s.ext.stops, fn)
	s.ext.mu.Unlock()
}

func (s *Server) runShutdownHooks() {
	s.ext.mu.Lock()
	stops := append([]func(){}, s.ext.stops...)
	s.ext.mu.Unlock()
	for _, fn := range stops {
		fn()
	}
}

// OnActivity registers a callback that receives every audited action as it is
// appended (live stream). Callbacks must not block.
func (s *Server) OnActivity(fn func(ActionEvent)) {
	s.ext.mu.Lock()
	s.ext.sinks = append(s.ext.sinks, fn)
	s.ext.mu.Unlock()
	s.ext.sinkOnce.Do(func() {
		if s.audit == nil {
			return
		}
		s.audit.sink = func(e auditEntry) {
			a := activityFrom(e)
			s.ext.mu.Lock()
			sinks := append([]func(ActionEvent){}, s.ext.sinks...)
			s.ext.mu.Unlock()
			for _, fn := range sinks {
				fn(a)
			}
		}
	})
}

// Port is the bound port (0 before Run binds).
func (s *Server) Port() int { return int(s.ext.port.Load()) }

// StartedAt is when Run bound its port (zero before).
func (s *Server) StartedAt() time.Time {
	n := s.ext.started.Load()
	if n == 0 {
		return time.Time{}
	}
	return time.Unix(0, n)
}

// AutosaveDebounce reports the configured autosave debounce (0 = off).
func (s *Server) AutosaveDebounce() time.Duration {
	if s.autosave == nil {
		return 0
	}
	return s.opts.AutosaveDebounce
}

// verdictKeys are the small result fields copied into ActionEvent.Result.
var verdictKeys = []string{"passed", "saved", "ok", "count", "total", "verdict", "status"}

func activityFrom(e auditEntry) ActionEvent {
	a := ActionEvent{
		Timestamp: e.Timestamp, RequestID: e.RequestID, WindowID: e.WindowID, ClientID: e.ClientID,
		Action: e.Action, OK: e.OK, DurationMs: e.DurationMs, ErrorCode: e.ErrorCode, ErrorMsg: e.ErrorMsg,
		ProjectUUID: e.ProjectUUID, ProjectName: e.ProjectName, DocumentUUID: e.DocumentUUID,
		DocumentType: e.DocumentType, OutputDir: e.OutputDir,
	}
	if e.Result != nil {
		for _, k := range verdictKeys {
			if v, ok := e.Result[k]; ok {
				switch v.(type) {
				case bool, float64, int, int64, string:
					if a.Result == nil {
						a.Result = map[string]any{}
					}
					a.Result[k] = v
				}
			}
		}
		if v, ok := e.Result["violations"].([]any); ok {
			if a.Result == nil {
				a.Result = map[string]any{}
			}
			a.Result["violations"] = len(v)
		}
	}
	return a
}
