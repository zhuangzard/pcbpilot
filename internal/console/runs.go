package console

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Runs are units of agent or CLI work reported to POST /api/runs/events.
// v0.7 producers: the pcbpilot CLI itself (long offline commands: sim, report,
// intent, kb, pcb auto — see app/console_hook.go). v0.8 adds the local-agent
// bridge (Claude Code / Codex sessions, their subagents and tool calls).

// RunEvent is one posted event.
type RunEvent struct {
	RunID   string    `json:"runId"`
	Parent  string    `json:"parentRunId,omitempty"`
	Agent   string    `json:"agent,omitempty"` // cli | claude-code | codex | …
	Kind    string    `json:"kind"`            // start | progress | tool | subagent | end
	Title   string    `json:"title,omitempty"`
	Command string    `json:"command,omitempty"`
	Cwd     string    `json:"cwd,omitempty"`
	Project string    `json:"project,omitempty"`
	Status  string    `json:"status,omitempty"` // for end: ok | failed | cancelled
	Detail  string    `json:"detail,omitempty"`
	At      time.Time `json:"at"`
}

// Run is the folded state of one run.
type Run struct {
	ID        string     `json:"id"`
	Parent    string     `json:"parentRunId,omitempty"`
	Agent     string     `json:"agent"`
	Title     string     `json:"title,omitempty"`
	Command   string     `json:"command,omitempty"`
	Cwd       string     `json:"cwd,omitempty"`
	Project   string     `json:"project,omitempty"`
	Status    string     `json:"status"` // running | ok | failed | cancelled | stale
	StartedAt time.Time  `json:"startedAt"`
	EndedAt   time.Time  `json:"endedAt,omitempty"`
	Events    []RunEvent `json:"events,omitempty"`
	Detail    string     `json:"detail,omitempty"`
}

const (
	maxRunEvents = 50
	maxRuns      = 300
	staleRun     = 6 * time.Hour
)

type runStore struct {
	mu      sync.Mutex
	runs    map[string]*Run
	logPath string
	now     func() time.Time
}

func newRunStore(logPath string, now func() time.Time) *runStore {
	s := &runStore{runs: map[string]*Run{}, logPath: logPath, now: now}
	s.loadLog()
	return s
}

// loadLog restores the most recent finished runs so history survives restarts.
func (s *runStore) loadLog() {
	b, err := os.ReadFile(s.logPath)
	if err != nil {
		return
	}
	lines := strings.Split(string(b), "\n")
	if len(lines) > maxRuns {
		lines = lines[len(lines)-maxRuns:]
	}
	for _, l := range lines {
		var r Run
		if json.Unmarshal([]byte(l), &r) == nil && r.ID != "" {
			cp := r
			s.runs[r.ID] = &cp
		}
	}
}

func (s *runStore) apply(ev RunEvent) (*Run, error) {
	ev.RunID = strings.TrimSpace(ev.RunID)
	if ev.RunID == "" || len(ev.RunID) > 128 {
		return nil, errors.New("runId is required (≤128 bytes)")
	}
	switch ev.Kind {
	case "start", "progress", "tool", "subagent", "end":
	default:
		return nil, errors.New("kind must be start|progress|tool|subagent|end")
	}
	if ev.At.IsZero() {
		ev.At = s.now().UTC()
	}
	ev.Detail = truncate(ev.Detail, 2000)
	ev.Title = truncate(ev.Title, 300)
	ev.Command = truncate(ev.Command, 1000)
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[ev.RunID]
	if r == nil {
		r = &Run{ID: ev.RunID, Agent: orStr(ev.Agent, "unknown"), Status: "running", StartedAt: ev.At}
		s.runs[ev.RunID] = r
	}
	if ev.Parent != "" {
		r.Parent = ev.Parent
	}
	for _, f := range []struct {
		dst *string
		v   string
	}{{&r.Title, ev.Title}, {&r.Command, ev.Command}, {&r.Cwd, ev.Cwd}, {&r.Project, ev.Project}} {
		if f.v != "" {
			*f.dst = f.v
		}
	}
	if ev.Detail != "" {
		r.Detail = ev.Detail
	}
	r.Events = append(r.Events, ev)
	if len(r.Events) > maxRunEvents {
		r.Events = r.Events[len(r.Events)-maxRunEvents:]
	}
	if ev.Kind == "end" {
		r.Status = orStr(ev.Status, "ok")
		r.EndedAt = ev.At
		s.appendLog(r)
	}
	s.pruneLocked()
	cp := *r
	return &cp, nil
}

func (s *runStore) appendLog(r *Run) {
	if s.logPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.logPath), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(s.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	cp := *r
	cp.Events = nil // the log keeps the summary; events stay in memory
	_ = json.NewEncoder(f).Encode(cp)
}

func (s *runStore) pruneLocked() {
	if len(s.runs) <= maxRuns {
		return
	}
	var all []*Run
	for _, r := range s.runs {
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].StartedAt.After(all[j].StartedAt) })
	for _, r := range all[maxRuns:] {
		if r.Status != "running" {
			delete(s.runs, r.ID)
		}
	}
}

func (s *runStore) list() []Run {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	out := make([]Run, 0, len(s.runs))
	for _, r := range s.runs {
		cp := *r
		if cp.Status == "running" {
			last := cp.StartedAt
			if n := len(cp.Events); n > 0 {
				last = cp.Events[n-1].At
			}
			if now.Sub(last) > staleRun {
				cp.Status = "stale"
			}
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := out[i].Status == "running", out[j].Status == "running"
		if ri != rj {
			return ri
		}
		return out[i].StartedAt.After(out[j].StartedAt)
	})
	return out
}

func orStr(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// Session is a cluster of CLI invocations discovered from the activity
// stream: one host (+ optional client label) with gaps under sessionGap.
// Each CLI command is its own process, so a session approximates "one agent
// working"; the v0.8 bridge replaces the approximation with real session ids.
type Session struct {
	ID        string    `json:"id"`
	Host      string    `json:"host"`
	Label     string    `json:"label,omitempty"`
	Project   string    `json:"project,omitempty"`
	Started   time.Time `json:"started"`
	Last      time.Time `json:"last"`
	Commands  int       `json:"commands"` // distinct CLI processes
	Actions   int       `json:"actions"`
	Failures  int       `json:"failures"`
	LastError string    `json:"lastError,omitempty"`
	Active    bool      `json:"active"`
	LastAct   string    `json:"lastAction"`
}

const sessionGap = 5 * time.Minute

func discoverSessions(acts []activityLike, now time.Time) []Session {
	sort.Slice(acts, func(i, j int) bool { return acts[i].Timestamp.Before(acts[j].Timestamp) })
	type key struct{ host, label string }
	open := map[key]*Session{}
	pids := map[*Session]map[string]bool{}
	var out []*Session
	for _, a := range acts {
		if a.ClientID == "" {
			continue
		}
		parts := strings.SplitN(a.ClientID, ":", 3)
		k := key{host: parts[0]}
		if len(parts) == 3 {
			k.label = parts[2]
		}
		s := open[k]
		if s == nil || a.Timestamp.Sub(s.Last) > sessionGap {
			s = &Session{ID: k.host + "@" + a.Timestamp.UTC().Format("150405"), Host: k.host, Label: k.label, Started: a.Timestamp}
			open[k] = s
			pids[s] = map[string]bool{}
			out = append(out, s)
		}
		s.Last = a.Timestamp
		s.Actions++
		s.LastAct = a.Action
		if a.ProjectName != "" {
			s.Project = a.ProjectName
		}
		if !a.OK {
			s.Failures++
			s.LastError = a.Action + ": " + truncate(a.ErrorMsg, 160)
		}
		pids[s][a.ClientID] = true
	}
	res := make([]Session, 0, len(out))
	for i := len(out) - 1; i >= 0; i-- {
		s := out[i]
		s.Commands = len(pids[s])
		s.Active = now.Sub(s.Last) < sessionGap
		res = append(res, *s)
	}
	return res
}
