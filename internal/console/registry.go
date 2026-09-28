package console

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

// The project registry is the console's memory of every EDA project the
// system has worked on. It is derived — never authoritative — from the audit
// log (backfilled incrementally with a per-file byte cursor) and from the live
// activity stream, and persisted to ~/.pcbpilot/console/registry.json so the
// history survives daemon restarts.

// RegistrySchemaVersion of registry.json.
const RegistrySchemaVersion = 1

// activeGap is the largest pause counted as continuous work (active time).
const activeGap = 10 * time.Minute

// ProjectRecord is one EDA project's history.
type ProjectRecord struct {
	Key         string               `json:"key"`
	UUID        string               `json:"uuid,omitempty"`
	Name        string               `json:"name,omitempty"`
	FirstSeen   time.Time            `json:"firstSeen"`
	LastSeen    time.Time            `json:"lastSeen"`
	ActiveMs    int64                `json:"activeMs"`
	Actions     int                  `json:"actions"`
	Failures    int                  `json:"failures"`
	Writes      int                  `json:"writes"`
	LastAction  string               `json:"lastAction,omitempty"`
	LastOK      bool                 `json:"lastOk"`
	LastError   *ErrRec              `json:"lastError,omitempty"`
	LastDRC     *DRCRec              `json:"lastDrc,omitempty"`
	LastSave    time.Time            `json:"lastSave,omitempty"`
	Docs        map[string]DocRec    `json:"docs,omitempty"`
	Clients     map[string]time.Time `json:"clients,omitempty"`
	Windows     map[string]time.Time `json:"windows,omitempty"`
	OutputDirs  map[string]time.Time `json:"outputDirs,omitempty"`
	Attribution string               `json:"attribution"` // context | derived | unattributed
}

// ErrRec is a failed action.
type ErrRec struct {
	At     time.Time `json:"at"`
	Action string    `json:"action"`
	Code   string    `json:"code,omitempty"`
	Msg    string    `json:"msg,omitempty"`
}

// DRCRec is the last native DRC outcome.
type DRCRec struct {
	At         time.Time `json:"at"`
	Type       string    `json:"type"` // pcb | schematic
	Passed     bool      `json:"passed"`
	Violations int       `json:"violations"`
}

// DocRec is one document seen in a project.
type DocRec struct {
	Type     string    `json:"type,omitempty"`
	LastSeen time.Time `json:"lastSeen"`
}

type registryFile struct {
	SchemaVersion int                       `json:"schemaVersion"`
	UpdatedAt     time.Time                 `json:"updatedAt"`
	Cursor        map[string]int64          `json:"auditCursor"`
	WindowMap     map[string]windowProject  `json:"windowMap"`
	Names         map[string]string         `json:"names"` // uuid → name
	Projects      map[string]*ProjectRecord `json:"projects"`
}

type windowProject struct {
	UUID string    `json:"uuid"`
	At   time.Time `json:"at"`
}

type registry struct {
	mu    sync.Mutex
	path  string
	data  registryFile
	dirty bool
	// mutates is the catalog's mutating-action set (Writes counter).
	mutates map[string]bool
}

func newRegistry(path string) *registry {
	r := &registry{path: path, mutates: map[string]bool{}}
	for _, a := range protocol.AllActions() {
		if a.Mutates {
			r.mutates[a.Name] = true
		}
	}
	r.data = registryFile{SchemaVersion: RegistrySchemaVersion, Cursor: map[string]int64{},
		WindowMap: map[string]windowProject{}, Names: map[string]string{}, Projects: map[string]*ProjectRecord{}}
	if b, err := os.ReadFile(path); err == nil {
		var f registryFile
		if json.Unmarshal(b, &f) == nil && f.SchemaVersion == RegistrySchemaVersion {
			if f.Cursor == nil {
				f.Cursor = map[string]int64{}
			}
			if f.WindowMap == nil {
				f.WindowMap = map[string]windowProject{}
			}
			if f.Names == nil {
				f.Names = map[string]string{}
			}
			if f.Projects == nil {
				f.Projects = map[string]*ProjectRecord{}
			}
			r.data = f
		}
	}
	return r
}

// auditRow is the subset of an audit JSONL row the registry reads. Payload is
// skipped; Result is decoded only for the few actions that carry identity or
// a verdict.
type auditRow struct {
	TS           time.Time       `json:"ts"`
	WindowID     string          `json:"windowId"`
	ClientID     string          `json:"clientId"`
	Action       string          `json:"action"`
	OK           bool            `json:"ok"`
	ErrorCode    string          `json:"errorCode"`
	ErrorMsg     string          `json:"errorMsg"`
	ProjectUUID  string          `json:"projectUuid"`
	ProjectName  string          `json:"projectName"`
	DocumentUUID string          `json:"documentUuid"`
	DocumentType string          `json:"documentType"`
	OutputDir    string          `json:"outputDir"`
	Result       json.RawMessage `json:"result"`
}

// observe folds one row into the registry. Attribution order: the row's own
// project context (v0.7+ audit rows), else the window's last known project
// (learned from project.current / document.current results), else
// "unattributed".
func (r *registry) observe(row auditRow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dirty = true
	d := &r.data
	// Learn identity from results.
	if row.OK && len(row.Result) > 0 {
		switch row.Action {
		case "project.current":
			var res struct {
				UUID, FriendlyName, Name string
			}
			if json.Unmarshal(row.Result, &res) == nil && res.UUID != "" {
				name := res.FriendlyName
				if name == "" {
					name = res.Name
				}
				d.Names[res.UUID] = name
				if row.WindowID != "" {
					d.WindowMap[row.WindowID] = windowProject{res.UUID, row.TS}
				}
			}
		case "document.current":
			var res struct {
				ParentProjectUUID string `json:"parentProjectUuid"`
			}
			if json.Unmarshal(row.Result, &res) == nil && res.ParentProjectUUID != "" && row.WindowID != "" {
				d.WindowMap[row.WindowID] = windowProject{res.ParentProjectUUID, row.TS}
			}
		}
	}
	// Daemon-local rows (system.health, unknown-action rejections) have no
	// window and no project: they belong to no project's history.
	if row.ProjectUUID == "" && row.WindowID == "" {
		return
	}
	uuid, name, attribution := row.ProjectUUID, row.ProjectName, "context"
	if uuid != "" && name != "" {
		d.Names[uuid] = name
	}
	if uuid != "" && row.WindowID != "" {
		d.WindowMap[row.WindowID] = windowProject{uuid, row.TS}
	}
	if uuid == "" && row.WindowID != "" {
		if wp, ok := d.WindowMap[row.WindowID]; ok {
			uuid, attribution = wp.UUID, "derived"
		}
	}
	if name == "" && uuid != "" {
		name = d.Names[uuid]
	}
	key := uuid
	if key == "" {
		key, attribution = "unattributed", "unattributed"
	}
	p := d.Projects[key]
	if p == nil {
		p = &ProjectRecord{Key: key, UUID: uuid, FirstSeen: row.TS, Attribution: attribution}
		d.Projects[key] = p
	}
	if name != "" {
		p.Name = name
	}
	if attribution == "context" || p.Attribution == "" {
		p.Attribution = attribution
	}
	if row.TS.Before(p.FirstSeen) {
		p.FirstSeen = row.TS
	}
	if !p.LastSeen.IsZero() {
		if gap := row.TS.Sub(p.LastSeen); gap > 0 && gap < activeGap {
			p.ActiveMs += gap.Milliseconds()
		}
	}
	// Rows can arrive out of order (live stream during a backfill): "last"
	// fields only move forward in time.
	latest := !row.TS.Before(p.LastSeen)
	if latest {
		p.LastSeen = row.TS
		p.LastAction, p.LastOK = row.Action, row.OK
	}
	p.Actions++
	if r.mutates[row.Action] {
		p.Writes++
	}
	if !row.OK {
		p.Failures++
		if p.LastError == nil || !row.TS.Before(p.LastError.At) {
			p.LastError = &ErrRec{At: row.TS, Action: row.Action, Code: row.ErrorCode, Msg: truncate(row.ErrorMsg, 300)}
		}
	}
	if row.OK && strings.HasSuffix(row.Action, ".drc.check") && len(row.Result) > 0 {
		var res struct {
			Passed     *bool             `json:"passed"`
			Violations []json.RawMessage `json:"violations"`
		}
		if json.Unmarshal(row.Result, &res) == nil && res.Passed != nil && (p.LastDRC == nil || !row.TS.Before(p.LastDRC.At)) {
			p.LastDRC = &DRCRec{At: row.TS, Type: strings.SplitN(row.Action, ".", 2)[0], Passed: *res.Passed, Violations: len(res.Violations)}
		}
	}
	if row.OK && strings.HasSuffix(row.Action, ".save") && row.TS.After(p.LastSave) {
		p.LastSave = row.TS
	}
	if row.DocumentUUID != "" {
		if p.Docs == nil {
			p.Docs = map[string]DocRec{}
		}
		p.Docs[row.DocumentUUID] = DocRec{Type: row.DocumentType, LastSeen: row.TS}
	}
	p.Clients = touch(p.Clients, row.ClientID, row.TS, 30)
	p.Windows = touch(p.Windows, row.WindowID, row.TS, 20)
	p.OutputDirs = touch(p.OutputDirs, row.OutputDir, row.TS, 20)
}

// observeActivity folds a live event (payload-free) into the registry.
func (r *registry) observeActivity(a activityLike) {
	row := auditRow{TS: a.Timestamp, WindowID: a.WindowID, ClientID: a.ClientID, Action: a.Action, OK: a.OK,
		ErrorCode: a.ErrorCode, ErrorMsg: a.ErrorMsg, ProjectUUID: a.ProjectUUID, ProjectName: a.ProjectName,
		DocumentUUID: a.DocumentUUID, DocumentType: a.DocumentType, OutputDir: a.OutputDir}
	if a.Result != nil {
		res := map[string]any{}
		for k, v := range a.Result {
			res[k] = v
		}
		// Activity carries the violation COUNT; rebuild an array of that length.
		if n, ok := res["violations"].(int); ok {
			res["violations"] = make([]int, n)
		}
		row.Result, _ = json.Marshal(res)
	}
	r.observe(row)
}

func touch(m map[string]time.Time, k string, at time.Time, max int) map[string]time.Time {
	if k == "" {
		return m
	}
	if m == nil {
		m = map[string]time.Time{}
	}
	if at.After(m[k]) {
		m[k] = at
	}
	if len(m) > max {
		type kv struct {
			k string
			t time.Time
		}
		var all []kv
		for k, v := range m {
			all = append(all, kv{k, v})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].t.After(all[j].t) })
		for _, e := range all[max:] {
			delete(m, e.k)
		}
	}
	return m
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// backfill reads audit files newer than maxAge from their cursors. Rows are
// streamed; a partial last line is left for the next pass.
func (r *registry) backfill(dir string, maxAge time.Duration, now time.Time, stop <-chan struct{}) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	cutoff := now.Add(-maxAge).UTC().Format("2006-01-02")
	for _, e := range entries {
		n := e.Name()
		if strings.HasSuffix(n, ".jsonl") && len(n) == len("2006-01-02.jsonl") && n[:10] >= cutoff {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		select {
		case <-stop:
			return nil
		default:
		}
		if err := r.backfillFile(filepath.Join(dir, n), n, stop); err != nil {
			continue
		}
	}
	return nil
}

func (r *registry) backfillFile(path, name string, stop <-chan struct{}) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	r.mu.Lock()
	off := r.data.Cursor[name]
	r.mu.Unlock()
	if off > st.Size() {
		off = 0 // file was rewritten
	}
	if off == st.Size() {
		return nil
	}
	if _, err := f.Seek(off, 0); err != nil {
		return err
	}
	// Read only up to the size at open: rows appended later reach the registry
	// through the live stream (see skipToEnd).
	br := bufio.NewReaderSize(io.LimitReader(f, st.Size()-off), 1<<20)
	n := 0
	for {
		line, err := br.ReadBytes('\n')
		if err != nil {
			break // EOF or partial line: resume from off next time
		}
		off += int64(len(line))
		var row auditRow
		if json.Unmarshal(line, &row) == nil && row.Action != "" {
			r.observe(row)
		}
		n++
		if n%2000 == 0 {
			r.mu.Lock()
			r.data.Cursor[name] = off
			r.mu.Unlock()
			select {
			case <-stop:
				return nil
			default:
			}
		}
	}
	r.mu.Lock()
	r.data.Cursor[name] = off
	r.dirty = true
	r.mu.Unlock()
	return nil
}

// skipToEnd moves a day's cursor to EOF. Once the startup backfill is done,
// every newer row was folded in from the live stream, so the next start must
// not read it again.
func (r *registry) skipToEnd(dir string, day time.Time) {
	name := day.UTC().Format("2006-01-02") + ".jsonl"
	st, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		return
	}
	r.mu.Lock()
	r.data.Cursor[name] = st.Size()
	r.dirty = true
	r.mu.Unlock()
}

// save writes registry.json when dirty.
func (r *registry) save(now time.Time) error {
	r.mu.Lock()
	if !r.dirty {
		r.mu.Unlock()
		return nil
	}
	r.data.UpdatedAt = now.UTC()
	// Prune stale window mappings (windows are ephemeral page loads).
	for w, wp := range r.data.WindowMap {
		if now.Sub(wp.At) > 14*24*time.Hour {
			delete(r.data.WindowMap, w)
		}
	}
	b, err := json.Marshal(r.data)
	r.dirty = false
	r.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o700); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

// snapshot returns copies of all records, newest activity first.
func (r *registry) snapshot() []ProjectRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ProjectRecord, 0, len(r.data.Projects))
	for _, p := range r.data.Projects {
		cp := *p
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	return out
}
