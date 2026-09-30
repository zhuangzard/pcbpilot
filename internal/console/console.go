// Package console is the v0.7 local web cockpit served by the pcbpilot daemon
// at /ui (static single page, embedded) and /api (JSON + SSE).
//
// It is read-mostly: it shows what the system is doing (daemon, versions,
// windows, every project worked on, live actions, design-flow timeline,
// simulation rounds, reports, agent runs) and writes only console-owned state —
// pcbpilot.project.json, the resource library, decision-card answers and its
// own registries. It never edits an EDA project: all EDA writes stay on the
// daemon's typed-action path, and nothing here bypasses a check.
//
// Security: loopback Host only (DNS-rebinding guard), same-origin Origin
// check, a per-install token (~/.pcbpilot/console.token) via header or an
// HttpOnly SameSite=Strict cookie (+ CSRF header on writes), no CORS.
package console

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/daemon"
)

//go:embed ui
var uiFS embed.FS

// DaemonInfo is the daemon identity shown on the monitor page.
type DaemonInfo struct {
	PID              int       `json:"pid"`
	Version          string    `json:"version"`
	Host             string    `json:"host"`
	Port             int       `json:"port"`
	StartedAt        time.Time `json:"startedAt"`
	UptimeSec        int64     `json:"uptimeSec"`
	AutosaveDebounce string    `json:"autosaveDebounce"`
	Autosave         bool      `json:"autosave"`
	Service          any       `json:"service,omitempty"`
}

// Options wires the console to its daemon.
type Options struct {
	Home     string // pcbpilot state dir (default Home())
	UserHome string // for skill/MCP discovery (default os.UserHomeDir)
	Version  string
	Host     string
	AuditDir string
	// Health returns the daemon's /health JSON (read through the daemon's own
	// endpoint so fields other branches add — e.g. update state — flow through).
	Health func(ctx context.Context) (json.RawMessage, error)
	// Daemon returns pid/port/uptime/autosave.
	Daemon func() DaemonInfo
	// Service returns the login-service status (optional).
	Service func() any
	// BackfillDays bounds the startup audit backfill (default 60).
	BackfillDays int
	Now          func() time.Time
}

// Console is the mounted cockpit.
type Console struct {
	opts      Options
	home      string
	token     string
	bus       *bus
	asks      *askQueue
	runs      *runStore
	reg       *registry
	done      chan struct{}
	heartbeat time.Duration

	actMu sync.Mutex
	acts  []activityLike // recent activity (ring) for sessions + first paint

	backfillMu   sync.Mutex
	backfillDone bool
	backfillAt   time.Time

	scanMu    sync.Mutex
	scanCache map[string]scanEntry
}

type scanEntry struct {
	at   time.Time
	arts []Artifact
	err  error
}

const maxActs = 2000

// New builds a console and creates the token if needed.
func New(opts Options) (*Console, error) {
	if opts.Home == "" {
		opts.Home = Home()
	}
	if opts.UserHome == "" {
		opts.UserHome, _ = os.UserHomeDir()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.BackfillDays <= 0 {
		opts.BackfillDays = 60
	}
	tok, err := EnsureToken(opts.Home)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(opts.Home, "console")
	c := &Console{opts: opts, home: opts.Home, token: tok, bus: newBus(1000), done: make(chan struct{}),
		heartbeat: 15 * time.Second, scanCache: map[string]scanEntry{}}
	c.asks = newAskQueue(filepath.Join(dir, "decisions.jsonl"), opts.Now, func(q *Question) { c.bus.publish("ask", q) })
	c.runs = newRunStore(filepath.Join(dir, "runs.jsonl"), opts.Now)
	c.reg = newRegistry(filepath.Join(dir, "registry.json"))
	c.loadRecentActivity()
	return c, nil
}

// Token returns the install token.
func (c *Console) Token() string { return c.token }

// Observe is the daemon activity sink (daemon.Server.OnActivity).
func (c *Console) Observe(a daemon.ActionEvent) {
	c.actMu.Lock()
	c.acts = append(c.acts, a)
	if len(c.acts) > maxActs {
		c.acts = c.acts[len(c.acts)-maxActs:]
	}
	c.actMu.Unlock()
	c.reg.observeActivity(a)
	c.bus.publish("activity", a)
}

// Start runs the background work: audit backfill, then periodic registry
// saves. It returns immediately; Stop ends it.
func (c *Console) Start() {
	go func() {
		if c.opts.AuditDir != "" {
			_ = c.reg.backfill(c.opts.AuditDir, time.Duration(c.opts.BackfillDays)*24*time.Hour, c.opts.Now(), c.done)
		}
		c.backfillMu.Lock()
		c.backfillDone, c.backfillAt = true, c.opts.Now()
		c.backfillMu.Unlock()
		c.persist()
		c.bus.publish("status", map[string]any{"backfill": "done"})
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-c.done:
				c.persist()
				return
			case <-t.C:
				c.persist()
			}
		}
	}()
}

func (c *Console) persist() {
	c.backfillMu.Lock()
	done := c.backfillDone
	c.backfillMu.Unlock()
	if done && c.opts.AuditDir != "" {
		now := c.opts.Now()
		c.reg.skipToEnd(c.opts.AuditDir, now)
		c.reg.skipToEnd(c.opts.AuditDir, now.Add(-24*time.Hour))
	}
	_ = c.reg.save(c.opts.Now())
}

// Stop ends background work and closes SSE streams.
func (c *Console) Stop() {
	select {
	case <-c.done:
	default:
		close(c.done)
	}
}

func (c *Console) daemonInfo() DaemonInfo {
	var d DaemonInfo
	if c.opts.Daemon != nil {
		d = c.opts.Daemon()
	}
	if d.Version == "" {
		d.Version = c.opts.Version
	}
	if d.Host == "" {
		d.Host = c.opts.Host
	}
	if !d.StartedAt.IsZero() {
		d.UptimeSec = int64(c.opts.Now().Sub(d.StartedAt).Seconds())
	}
	if c.opts.Service != nil {
		d.Service = c.opts.Service()
	}
	return d
}

// Handler serves /ui/… and /api/….
func (c *Console) Handler() http.Handler {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(uiFS, "ui")
	static := http.StripPrefix("/ui/", http.FileServer(http.FS(sub)))
	mux.Handle("/ui/", hostOnly(securityHeaders(static)))
	mux.Handle("/ui", http.RedirectHandler("/ui/", http.StatusFound))

	api := http.NewServeMux()
	api.HandleFunc("POST /api/session", c.handleSession)
	api.HandleFunc("GET /api/events", c.handleEvents)
	api.HandleFunc("GET /api/status", c.handleStatus)
	api.HandleFunc("GET /api/activity", c.handleActivity)
	api.HandleFunc("GET /api/projects", c.handleProjects)
	api.HandleFunc("GET /api/templates", c.handleTemplates)
	api.HandleFunc("POST /api/workdirs", c.handleAddWorkDir)
	api.HandleFunc("DELETE /api/workdirs/{id}", c.handleRemoveWorkDir)
	api.HandleFunc("GET /api/workdirs/{id}", c.handleWorkDir)
	api.HandleFunc("GET /api/workdirs/{id}/file", c.handleFile)
	api.HandleFunc("GET /api/workdirs/{id}/config", c.handleGetConfig)
	api.HandleFunc("PUT /api/workdirs/{id}/config", c.handlePutConfig)
	api.HandleFunc("POST /api/workdirs/{id}/config/init", c.handleInitConfig)
	api.HandleFunc("GET /api/workdirs/{id}/kb", c.handleKBList)
	api.HandleFunc("GET /api/workdirs/{id}/kb/search", c.handleKBSearch)
	api.HandleFunc("POST /api/workdirs/{id}/kb/upload", c.handleKBUpload)
	api.HandleFunc("GET /api/workdirs/{id}/kb/{doc}", c.handleKBShow)
	api.HandleFunc("POST /api/workdirs/{id}/kb/{doc}/tags", c.handleKBTags)
	api.HandleFunc("GET /api/runs", c.handleRuns)
	api.HandleFunc("POST /api/runs/events", c.handleRunEvent)
	api.HandleFunc("GET /api/ask", c.handleAskList)
	api.HandleFunc("POST /api/ask", c.handleAskCreate)
	api.HandleFunc("GET /api/ask/{id}", c.handleAskGet)
	api.HandleFunc("POST /api/ask/{id}/answer", c.handleAskAnswer)
	api.HandleFunc("POST /api/ask/{id}/cancel", c.handleAskCancel)
	mux.Handle("/api/", c.guard(api))
	return mux
}

// hostOnly applies the loopback Host check to static assets.
func hostOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "console answers loopback hosts only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// securityHeaders locks the page down: no framing, no remote resources.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; frame-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func jsonError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "error": map[string]string{"code": code, "message": msg}})
}
