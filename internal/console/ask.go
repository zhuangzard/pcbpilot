package console

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Decision cards: an agent posts a question with options (`pcbpilot ask`), the
// console shows it, the user answers, the waiting CLI returns the choice. The
// queue lives in daemon memory; every settled card is appended to
// ~/.pcbpilot/console/decisions.jsonl as the decision log.

// Option is one answer choice.
type Option struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

// Answer is the user's decision.
type Answer struct {
	Choice string    `json:"choice"`
	Note   string    `json:"note,omitempty"`
	By     string    `json:"by"` // console | cli | default
	At     time.Time `json:"at"`
}

// Question is one decision card.
type Question struct {
	ID        string    `json:"id"`
	Question  string    `json:"question"`
	Context   string    `json:"context,omitempty"`
	Options   []Option  `json:"options,omitempty"`
	Default   string    `json:"default,omitempty"`
	AllowFree bool      `json:"allowFree,omitempty"`
	Agent     string    `json:"agent,omitempty"`
	RunID     string    `json:"runId,omitempty"`
	Project   string    `json:"project,omitempty"`
	Step      string    `json:"step,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	ExpiresAt time.Time `json:"expiresAt"`
	Status    string    `json:"status"` // pending | answered | expired | cancelled
	Answer    *Answer   `json:"answer,omitempty"`
}

type askQueue struct {
	mu      sync.Mutex
	items   map[string]*Question
	waiters map[string]chan struct{}
	logPath string
	now     func() time.Time
	onEvent func(*Question)
}

func newAskQueue(logPath string, now func() time.Time, onEvent func(*Question)) *askQueue {
	return &askQueue{items: map[string]*Question{}, waiters: map[string]chan struct{}{}, logPath: logPath, now: now, onEvent: onEvent}
}

// AskRequest is the POST /api/ask body.
type AskRequest struct {
	Question   string   `json:"question"`
	Context    string   `json:"context,omitempty"`
	Options    []Option `json:"options,omitempty"`
	Default    string   `json:"default,omitempty"`
	AllowFree  bool     `json:"allowFree,omitempty"`
	Agent      string   `json:"agent,omitempty"`
	RunID      string   `json:"runId,omitempty"`
	Project    string   `json:"project,omitempty"`
	Step       string   `json:"step,omitempty"`
	TimeoutSec int      `json:"timeoutSec,omitempty"`
}

func (q *askQueue) create(req AskRequest) (*Question, error) {
	req.Question = strings.TrimSpace(req.Question)
	if req.Question == "" || len(req.Question) > 4000 {
		return nil, errors.New("question must be 1–4000 bytes")
	}
	if len(req.Context) > 20000 {
		return nil, errors.New("context too long (max 20000 bytes)")
	}
	if len(req.Options) > 12 {
		return nil, errors.New("at most 12 options")
	}
	if len(req.Options) == 0 && !req.AllowFree {
		return nil, errors.New("give options, or allowFree for a free-text answer")
	}
	seen := map[string]bool{}
	for i := range req.Options {
		o := &req.Options[i]
		if o.ID == "" {
			o.ID = fmt.Sprintf("%d", i+1)
		}
		if o.Label == "" {
			o.Label = o.ID
		}
		if seen[o.ID] {
			return nil, fmt.Errorf("duplicate option id %q", o.ID)
		}
		seen[o.ID] = true
	}
	if req.Default != "" && !seen[req.Default] && !req.AllowFree {
		return nil, fmt.Errorf("default %q is not an option id", req.Default)
	}
	timeout := time.Duration(req.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	if timeout > 24*time.Hour {
		timeout = 24 * time.Hour
	}
	now := q.now()
	item := &Question{ID: newID(), Question: req.Question, Context: req.Context, Options: req.Options,
		Default: req.Default, AllowFree: req.AllowFree, Agent: req.Agent, RunID: req.RunID, Project: req.Project,
		Step: req.Step, CreatedAt: now, ExpiresAt: now.Add(timeout), Status: "pending"}
	q.mu.Lock()
	q.items[item.ID] = item
	q.waiters[item.ID] = make(chan struct{})
	cp := *item
	q.mu.Unlock()
	q.emit(&cp)
	return &cp, nil
}

func (q *askQueue) emit(item *Question) {
	if q.onEvent != nil {
		q.onEvent(item)
	}
}

// answer settles a pending card.
func (q *askQueue) answer(id, choice, note, by string) (*Question, error) {
	q.mu.Lock()
	item, ok := q.items[id]
	if !ok {
		q.mu.Unlock()
		return nil, errNotFound
	}
	q.expireLocked(item)
	if item.Status != "pending" {
		cp := *item
		q.mu.Unlock()
		return &cp, fmt.Errorf("decision %s is already %s", id, item.Status)
	}
	valid := item.AllowFree && strings.TrimSpace(choice) != ""
	for _, o := range item.Options {
		if o.ID == choice {
			valid = true
		}
	}
	if !valid {
		q.mu.Unlock()
		return nil, fmt.Errorf("choice %q is not one of the options", choice)
	}
	item.Status = "answered"
	item.Answer = &Answer{Choice: choice, Note: note, By: by, At: q.now()}
	q.settleLocked(item)
	cp := *item
	q.mu.Unlock()
	q.emit(&cp)
	return &cp, nil
}

func (q *askQueue) cancel(id string) (*Question, error) {
	q.mu.Lock()
	item, ok := q.items[id]
	if !ok {
		q.mu.Unlock()
		return nil, errNotFound
	}
	if item.Status == "pending" {
		item.Status = "cancelled"
		q.settleLocked(item)
	}
	cp := *item
	q.mu.Unlock()
	q.emit(&cp)
	return &cp, nil
}

func (q *askQueue) expireLocked(item *Question) {
	if item.Status == "pending" && !q.now().Before(item.ExpiresAt) {
		item.Status = "expired"
		q.settleLocked(item)
	}
}

// settleLocked wakes waiters and logs the card.
func (q *askQueue) settleLocked(item *Question) {
	if ch, ok := q.waiters[item.ID]; ok {
		close(ch)
		delete(q.waiters, item.ID)
	}
	if q.logPath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(q.logPath), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(q.logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_ = json.NewEncoder(f).Encode(item)
}

func (q *askQueue) get(id string) (*Question, chan struct{}, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	item, ok := q.items[id]
	if !ok {
		return nil, nil, errNotFound
	}
	q.expireLocked(item)
	cp := *item
	return &cp, q.waiters[id], nil
}

// wait blocks until the card settles, it expires, or d elapses.
func (q *askQueue) wait(id string, d time.Duration, done <-chan struct{}) (*Question, error) {
	item, ch, err := q.get(id)
	if err != nil || item.Status != "pending" || ch == nil {
		return item, err
	}
	until := time.Until(item.ExpiresAt)
	if until < d {
		d = until
	}
	if d < 0 {
		d = 0
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-done:
	}
	item, _, err = q.get(id)
	if err == nil && item.Status == "expired" {
		q.emit(item)
	}
	return item, err
}

// list returns pending cards first, then the most recent settled ones.
func (q *askQueue) list(limit int) []*Question {
	q.mu.Lock()
	var out []*Question
	for _, it := range q.items {
		q.expireLocked(it)
		cp := *it
		out = append(out, &cp)
	}
	q.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		pi, pj := out[i].Status == "pending", out[j].Status == "pending"
		if pi != pj {
			return pi
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (q *askQueue) pending() int {
	n := 0
	for _, it := range q.list(0) {
		if it.Status == "pending" {
			n++
		}
	}
	return n
}

var errNotFound = errors.New("not found")

func newID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
