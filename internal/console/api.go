package console

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
	"github.com/zhuangzard/pcbpilot/pkg/kb"
	"github.com/zhuangzard/pcbpilot/pkg/projectconfig"
)

// ── status ────────────────────────────────────────────────────────────────

func (c *Console) health(ctx context.Context) (map[string]any, error) {
	if c.opts.Health == nil {
		return nil, errors.New("health source not wired")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	raw, err := c.opts.Health(ctx)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func windowsOf(h map[string]any) []map[string]any {
	var out []map[string]any
	if ws, ok := h["windows"].([]any); ok {
		for _, w := range ws {
			if m, ok := w.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

func (c *Console) handleStatus(w http.ResponseWriter, r *http.Request) {
	h, err := c.health(r.Context())
	resp := map[string]any{"daemon": c.daemonInfo(), "now": c.opts.Now().UTC()}
	if err != nil {
		resp["healthError"] = err.Error()
	} else {
		resp["health"] = h
		wins := windowsOf(h)
		resp["windows"] = wins
		resp["components"] = components(c.opts.Version, c.opts.UserHome, wins)
		// Pass through update/offline-retry state when the daemon reports it
		// (added by the self-update work; absent on older daemons).
		for _, k := range []string{"updates", "update", "offline"} {
			if v, ok := h[k]; ok {
				resp[k] = v
			}
		}
	}
	c.backfillMu.Lock()
	resp["console"] = map[string]any{
		"subscribers": c.bus.subscribers(), "pendingDecisions": c.asks.pending(),
		"backfillDone": c.backfillDone, "backfillAt": c.backfillAt, "auditDir": c.opts.AuditDir,
		"home": c.home,
	}
	c.backfillMu.Unlock()
	writeJSON(w, http.StatusOK, resp)
}

// ── activity ──────────────────────────────────────────────────────────────

func (c *Console) handleActivity(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 200, 1, maxActs)
	project := r.URL.Query().Get("project")
	c.actMu.Lock()
	var out []activityLike
	for i := len(c.acts) - 1; i >= 0 && len(out) < limit; i-- {
		a := c.acts[i]
		if project != "" && a.ProjectUUID != project && a.ProjectName != project {
			continue
		}
		out = append(out, a)
	}
	c.actMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"activity": out})
}

// loadRecentActivity seeds the in-memory ring from the tail of today's (and
// yesterday's) audit file so the monitor is populated right after a restart.
func (c *Console) loadRecentActivity() {
	if c.opts.AuditDir == "" {
		return
	}
	now := c.opts.Now()
	var rows []activityLike
	for _, day := range []time.Time{now.Add(-24 * time.Hour), now} {
		rows = append(rows, tailAudit(filepath.Join(c.opts.AuditDir, day.UTC().Format("2006-01-02")+".jsonl"), 8<<20, 500)...)
	}
	if len(rows) > maxActs {
		rows = rows[len(rows)-maxActs:]
	}
	c.acts = rows
}

func tailAudit(path string, maxBytes int64, maxRows int) []activityLike {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	off := st.Size() - maxBytes
	if off < 0 {
		off = 0
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil
	}
	b, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	if off > 0 && len(lines) > 0 {
		lines = lines[1:] // first line is partial
	}
	var out []activityLike
	for _, l := range lines {
		var row auditRow
		if json.Unmarshal([]byte(l), &row) != nil || row.Action == "" {
			continue
		}
		out = append(out, activityLike{Timestamp: row.TS, WindowID: row.WindowID, ClientID: row.ClientID, Action: row.Action,
			OK: row.OK, ErrorCode: row.ErrorCode, ErrorMsg: truncate(row.ErrorMsg, 300), ProjectUUID: row.ProjectUUID,
			ProjectName: row.ProjectName, DocumentUUID: row.DocumentUUID, DocumentType: row.DocumentType, OutputDir: row.OutputDir})
	}
	if len(out) > maxRows {
		out = out[len(out)-maxRows:]
	}
	return out
}

// ── projects (monitor) ────────────────────────────────────────────────────

// ProjectView is one row of the monitor's project table.
type ProjectView struct {
	ProjectRecord
	Status      string       `json:"status"` // running | idle | finished
	Connected   int          `json:"connectedWindows"`
	Host        string       `json:"host,omitempty"`
	Stage       string       `json:"stage,omitempty"`
	ActiveNow   int          `json:"activeClients"`
	WorkDirs    []WorkDirRef `json:"workDirs,omitempty"`
	Report      *ReportPkg   `json:"report,omitempty"`
	RunningRuns int          `json:"runningRuns"`
}

// WorkDirRef links a work dir to a project.
type WorkDirRef struct {
	ID  string `json:"id"`
	Dir string `json:"dir"`
}

const (
	runningWindow = 5 * time.Minute
	idleWindow    = 24 * time.Hour
)

func (c *Console) handleProjects(w http.ResponseWriter, r *http.Request) {
	now := c.opts.Now()
	h, _ := c.health(r.Context())
	wins := windowsOf(h)
	dirs, _ := LoadWorkDirs(c.home)
	runs := c.runs.list()
	var views []ProjectView
	linked := map[string]bool{}
	for _, rec := range c.reg.snapshot() {
		v := ProjectView{ProjectRecord: rec}
		for _, win := range wins {
			ctx, _ := win["context"].(map[string]any)
			if ctx != nil && rec.UUID != "" && ctx["projectUuid"] == rec.UUID {
				v.Connected++
				if ev, _ := win["easyedaVersion"].(string); ev != "" {
					v.Host = ev
				}
			}
		}
		for _, t := range rec.Clients {
			if now.Sub(t) < runningWindow {
				v.ActiveNow++
			}
		}
		switch {
		case v.Connected > 0 && now.Sub(rec.LastSeen) < runningWindow, now.Sub(rec.LastSeen) < runningWindow:
			v.Status = "running"
		case now.Sub(rec.LastSeen) < idleWindow || v.Connected > 0:
			v.Status = "idle"
		default:
			v.Status = "finished"
		}
		if rec.UUID != "" {
			if st, err := workflow.LoadAny(rec.UUID, rec.Name); err == nil {
				v.Stage = bestStage(st)
			}
		}
		for _, d := range dirs {
			if workDirLinked(d, rec) {
				v.WorkDirs = append(v.WorkDirs, WorkDirRef{d.ID, d.Dir})
				linked[d.ID] = true
				if arts, err := c.scan(d.Dir); err == nil {
					if pk := reportPkgs(d.Dir, arts); len(pk) > 0 && (v.Report == nil || pk[0].GeneratedAt > v.Report.GeneratedAt) {
						p := pk[0]
						v.Report = &p
					}
				}
			}
		}
		for _, run := range runs {
			if run.Status == "running" && (run.Project == rec.Name || run.Project == rec.UUID || cwdUnder(run.Cwd, v.WorkDirs)) {
				v.RunningRuns++
			}
		}
		views = append(views, v)
	}
	var wdViews []map[string]any
	for _, d := range dirs {
		item := map[string]any{"workDir": d, "linked": linked[d.ID]}
		if cfg, err := projectconfig.Load(d.Dir); err == nil {
			item["config"] = map[string]any{"name": cfg.Name, "template": cfg.Template, "edaProject": cfg.EDA.Project}
		}
		arts, err := c.scan(d.Dir)
		if err != nil {
			item["error"] = err.Error()
		}
		if n := len(arts); n > 0 {
			item["lastArtifact"] = arts[n-1]
		}
		pk := reportPkgs(d.Dir, arts)
		if len(pk) > 0 {
			item["report"] = pk[0]
		}
		item["status"], item["statusReason"] = workDirStatus(d.Dir, arts, pk, runs)
		wdViews = append(wdViews, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": views, "workDirs": wdViews, "windows": wins})
}

// workDirStatus summarises a work dir: running (a registered command runs
// inside it) > failed (newest report FAIL, or the newest command failed after
// it) > finished (newest report PASS / PASS with warnings) > in-progress
// (artifacts, no report) > empty.
func workDirStatus(dir string, arts []Artifact, reports []ReportPkg, runs []Run) (string, string) {
	var lastRun *Run
	for i := range runs {
		r := &runs[i]
		if !cwdUnder(r.Cwd, []WorkDirRef{{Dir: dir}}) {
			continue
		}
		if r.Status == "running" {
			return "running", "running: " + orStr(r.Title, r.ID)
		}
		if lastRun == nil || r.EndedAt.After(lastRun.EndedAt) {
			lastRun = r
		}
	}
	var rep *ReportPkg
	if len(reports) > 0 {
		rep = &reports[0]
	}
	repAt := time.Time{}
	if rep != nil {
		repAt, _ = time.Parse(time.RFC3339, rep.GeneratedAt)
	}
	if lastRun != nil && lastRun.Status == "failed" && lastRun.EndedAt.After(repAt) {
		return "failed", "last command failed: " + orStr(lastRun.Title, lastRun.ID) + " — " + lastRun.Detail
	}
	switch {
	case rep != nil && rep.Verdict == "FAIL":
		return "failed", "report " + rep.Version + " verdict FAIL"
	case rep != nil:
		return "finished", "report " + rep.Version + " " + rep.Verdict
	case len(arts) > 0:
		return "in-progress", fmt.Sprintf("%d artifacts, no report yet", len(arts))
	}
	return "empty", "no artifacts"
}

func cwdUnder(cwd string, dirs []WorkDirRef) bool {
	for _, d := range dirs {
		if cwd != "" && (cwd == d.Dir || strings.HasPrefix(cwd, d.Dir+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

// workDirLinked: config names the project, or the CLI ran inside the dir.
func workDirLinked(d WorkDir, rec ProjectRecord) bool {
	if cfg, err := projectconfig.Load(d.Dir); err == nil && cfg.EDA.Project != "" {
		if cfg.EDA.Project == rec.UUID || cfg.EDA.Project == rec.Name {
			return true
		}
	}
	for od := range rec.OutputDirs {
		if od == d.Dir || strings.HasPrefix(od, d.Dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// bestStage is the highest confirmed legacy workflow stage (diagnostic).
func bestStage(st *workflow.State) string {
	best := ""
	for _, s := range workflow.Order {
		if st.Confirmed[s] {
			best = string(s)
		}
	}
	return best
}

// scan caches artifact scans for 3 s per dir.
func (c *Console) scan(dir string) ([]Artifact, error) {
	c.scanMu.Lock()
	e, ok := c.scanCache[dir]
	c.scanMu.Unlock()
	if ok && c.opts.Now().Sub(e.at) < 3*time.Second {
		return e.arts, e.err
	}
	arts, err := scanArtifacts(dir)
	c.scanMu.Lock()
	c.scanCache[dir] = scanEntry{c.opts.Now(), arts, err}
	c.scanMu.Unlock()
	return arts, err
}

func (c *Console) workDir(w http.ResponseWriter, r *http.Request) (WorkDir, bool) {
	id := r.PathValue("id")
	dirs, err := LoadWorkDirs(c.home)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "WORKDIRS", err.Error())
		return WorkDir{}, false
	}
	for _, d := range dirs {
		if d.ID == id {
			return d, true
		}
	}
	jsonError(w, http.StatusNotFound, "NOT_FOUND", "unknown work dir "+id)
	return WorkDir{}, false
}

func (c *Console) handleAddWorkDir(w http.ResponseWriter, r *http.Request) {
	var body struct{ Dir, Name string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil || body.Dir == "" {
		jsonError(w, http.StatusBadRequest, "BAD_REQUEST", "body {dir, name?} required")
		return
	}
	if !filepath.IsAbs(body.Dir) {
		jsonError(w, http.StatusBadRequest, "BAD_REQUEST", "dir must be an absolute path")
		return
	}
	wd, err := RegisterWorkDir(c.home, body.Dir, body.Name, "console")
	if err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_DIR", err.Error())
		return
	}
	c.bus.publish("project", map[string]any{"workDir": wd.ID, "change": "added"})
	writeJSON(w, http.StatusOK, wd)
}

func (c *Console) handleRemoveWorkDir(w http.ResponseWriter, r *http.Request) {
	if err := UnregisterWorkDir(c.home, r.PathValue("id")); err != nil {
		jsonError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	c.bus.publish("project", map[string]any{"workDir": r.PathValue("id"), "change": "removed"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (c *Console) handleWorkDir(w http.ResponseWriter, r *http.Request) {
	d, ok := c.workDir(w, r)
	if !ok {
		return
	}
	resp := map[string]any{"workDir": d}
	cfg, cerr := projectconfig.Load(d.Dir)
	switch {
	case cerr == nil:
		resp["config"] = cfg
		resp["issues"] = cfg.Validate()
	case errors.Is(cerr, projectconfig.ErrNotFound):
		resp["config"] = nil
	default:
		resp["configError"] = cerr.Error()
		cfg = nil
	}
	arts, err := c.scan(d.Dir)
	if err != nil {
		resp["scanError"] = err.Error()
	}
	var wf []wfEvent
	if cfg != nil && cfg.EDA.Project != "" {
		if st, err := workflow.LoadAny(cfg.EDA.Project); err == nil {
			for _, e := range st.History {
				at, _ := time.Parse(time.RFC3339, e.At)
				wf = append(wf, wfEvent{Stage: string(e.Stage), Action: e.Action, At: at})
			}
			resp["workflowStage"] = bestStage(st)
		}
	}
	resp["timeline"] = buildTimeline(cfg, arts, wf)
	resp["artifacts"] = arts
	resp["sims"] = simRuns(d.Dir, arts)
	reps := reportPkgs(d.Dir, arts)
	resp["reports"] = reps
	resp["status"], resp["statusReason"] = workDirStatus(d.Dir, arts, reps, c.runs.list())
	if lib, err := kb.Open(d.Dir, resourcesOf(cfg)); err == nil {
		if cat, err := lib.Load(); err == nil {
			resp["kb"] = kbStats(cat)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func resourcesOf(cfg *projectconfig.Config) string {
	if cfg != nil && cfg.Resources != "" {
		return cfg.Resources
	}
	return "resources"
}

func kbStats(cat *kb.Catalog) map[string]any {
	st := map[string]int{}
	summarized := 0
	for _, d := range cat.Docs {
		st[d.Status]++
		if d.Summary != nil {
			summarized++
		}
	}
	return map[string]any{"docs": len(cat.Docs), "byStatus": st, "summarized": summarized}
}

// handleFile serves one file inside the work dir, read-only. Paths are
// cleaned, symlinks resolved and required to stay inside the dir; HTML is
// served under a sandbox CSP (opaque origin, no scripts) so a stored page can
// never call the API with the console cookie.
func (c *Console) handleFile(w http.ResponseWriter, r *http.Request) {
	d, ok := c.workDir(w, r)
	if !ok {
		return
	}
	rel := r.URL.Query().Get("path")
	if rel == "" || filepath.IsAbs(rel) || strings.Contains(rel, "\x00") {
		jsonError(w, http.StatusBadRequest, "BAD_PATH", "relative path required")
		return
	}
	full := filepath.Join(d.Dir, filepath.FromSlash(rel))
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		jsonError(w, http.StatusNotFound, "NOT_FOUND", "no such file")
		return
	}
	root, _ := filepath.EvalSymlinks(d.Dir)
	if relp, err := filepath.Rel(root, real); err != nil || relp == ".." || strings.HasPrefix(relp, ".."+string(filepath.Separator)) {
		jsonError(w, http.StatusForbidden, "OUTSIDE_WORKDIR", "path escapes the work dir")
		return
	}
	st, err := os.Stat(real)
	if err != nil || st.IsDir() {
		jsonError(w, http.StatusNotFound, "NOT_FOUND", "no such file")
		return
	}
	ct := mime.TypeByExtension(strings.ToLower(filepath.Ext(real)))
	if ct == "" {
		ct = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ct)
	// No frame-ancestors: other sites cannot load this at all (the
	// SameSite=Strict cookie is not sent cross-site → 401), and under
	// "sandbox" 'self' would match nothing.
	h.Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data:; style-src 'unsafe-inline'")
	h.Set("X-Content-Type-Options", "nosniff")
	if r.URL.Query().Get("download") == "1" {
		h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filepath.Base(real)}))
	}
	f, err := os.Open(real)
	if err != nil {
		jsonError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	defer f.Close()
	http.ServeContent(w, r, filepath.Base(real), st.ModTime(), f)
}

// ── project config ────────────────────────────────────────────────────────

func (c *Console) handleTemplates(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": projectconfig.SchemaVersion, "templates": projectconfig.Templates,
		"steps": projectconfig.Steps, "sims": projectconfig.Sims, "sections": projectconfig.Sections,
		"kinds": kb.Kinds,
	})
}

func (c *Console) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	d, ok := c.workDir(w, r)
	if !ok {
		return
	}
	cfg, err := projectconfig.Load(d.Dir)
	if errors.Is(err, projectconfig.ErrNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"config": nil})
		return
	}
	if err != nil {
		jsonError(w, http.StatusUnprocessableEntity, "CONFIG_INVALID", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"config": cfg, "issues": cfg.Validate()})
}

func (c *Console) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	d, ok := c.workDir(w, r)
	if !ok {
		return
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	cfg, err := projectconfig.Decode(b)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "CONFIG_INVALID", err.Error())
		return
	}
	if issues := cfg.Validate(); projectconfig.HasErrors(issues) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"ok": false, "issues": issues})
		return
	}
	if err := projectconfig.Save(d.Dir, cfg, "console"); err != nil {
		jsonError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	c.bus.publish("project", map[string]any{"workDir": d.ID, "change": "config"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": cfg, "issues": cfg.Validate()})
}

func (c *Console) handleInitConfig(w http.ResponseWriter, r *http.Request) {
	d, ok := c.workDir(w, r)
	if !ok {
		return
	}
	var body struct{ Template, Name, EDAProject string }
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	if _, err := projectconfig.Load(d.Dir); err == nil {
		jsonError(w, http.StatusConflict, "EXISTS", projectconfig.FileName+" already exists — edit it instead")
		return
	}
	cfg, err := projectconfig.New(orStr(body.Name, d.Name), body.Template)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_TEMPLATE", err.Error())
		return
	}
	cfg.EDA.Project = body.EDAProject
	if err := projectconfig.Save(d.Dir, cfg, "console"); err != nil {
		jsonError(w, http.StatusInternalServerError, "SAVE_FAILED", err.Error())
		return
	}
	c.bus.publish("project", map[string]any{"workDir": d.ID, "change": "config"})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": cfg, "issues": cfg.Validate()})
}

// ── resource library ──────────────────────────────────────────────────────

func (c *Console) library(w http.ResponseWriter, r *http.Request) (*kb.Library, WorkDir, bool) {
	d, ok := c.workDir(w, r)
	if !ok {
		return nil, d, false
	}
	cfg, _ := projectconfig.Load(d.Dir)
	lib, err := kb.Open(d.Dir, resourcesOf(cfg))
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "KB", err.Error())
		return nil, d, false
	}
	return lib, d, true
}

func (c *Console) handleKBList(w http.ResponseWriter, r *http.Request) {
	lib, _, ok := c.library(w, r)
	if !ok {
		return
	}
	cat, err := lib.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "KB", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"docs": cat.Docs, "stats": kbStats(cat)})
}

func (c *Console) handleKBSearch(w http.ResponseWriter, r *http.Request) {
	lib, _, ok := c.library(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	res, err := lib.Search(q.Get("q"), kb.SearchOptions{K: atoiDefault(q.Get("k"), 10, 1, 100), Kind: q.Get("kind"), Tag: q.Get("tag"), Doc: q.Get("doc")})
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "KB", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (c *Console) handleKBShow(w http.ResponseWriter, r *http.Request) {
	lib, _, ok := c.library(w, r)
	if !ok {
		return
	}
	cat, err := lib.Load()
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "KB", err.Error())
		return
	}
	d, err := cat.Find(r.PathValue("doc"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "NOT_FOUND", err.Error())
		return
	}
	resp := map[string]any{"doc": d}
	if r.URL.Query().Get("chunks") == "1" {
		chunks, _ := lib.Chunks(d.ID)
		if len(chunks) > 20 {
			chunks = chunks[:20]
		}
		resp["chunks"] = chunks
	}
	writeJSON(w, http.StatusOK, resp)
}

func (c *Console) handleKBTags(w http.ResponseWriter, r *http.Request) {
	lib, d, ok := c.library(w, r)
	if !ok {
		return
	}
	var body struct{ Add, Remove []string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	doc, err := lib.SetTags(r.PathValue("doc"), body.Add, body.Remove)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "KB", err.Error())
		return
	}
	c.bus.publish("project", map[string]any{"workDir": d.ID, "change": "kb"})
	writeJSON(w, http.StatusOK, map[string]any{"doc": doc})
}

// Upload limits: per file and per request.
const (
	maxUploadFile    = 200 << 20
	maxUploadRequest = 1 << 30
)

// uploadAllowed is the extension allowlist for console uploads.
var uploadAllowed = map[string]bool{
	".pdf": true, ".md": true, ".markdown": true, ".txt": true, ".csv": true, ".json": true, ".html": true, ".htm": true,
	".docx": true, ".png": true, ".jpg": true, ".jpeg": true, ".svg": true, ".step": true, ".stp": true, ".dxf": true,
	".zip": true, ".xlsx": true,
}

// handleKBUpload streams multipart files to a temp dir inside the index dir,
// then adds them (copy into resources/<kind>/, sanitized name, dedupe by
// sha256, index). Nothing is written outside the work dir.
func (c *Console) handleKBUpload(w http.ResponseWriter, r *http.Request) {
	lib, d, ok := c.library(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadRequest)
	mr, err := r.MultipartReader()
	if err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_REQUEST", "multipart/form-data required")
		return
	}
	kind, tags := "other", []string(nil)
	tmpDir := filepath.Join(d.Dir, kb.IndexDirName, "upload-tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		jsonError(w, http.StatusInternalServerError, "KB", err.Error())
		return
	}
	var results []kb.AddResult
	var rejected []map[string]string
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			jsonError(w, http.StatusBadRequest, "BAD_UPLOAD", err.Error())
			return
		}
		switch part.FormName() {
		case "kind":
			b, _ := io.ReadAll(io.LimitReader(part, 64))
			kind = strings.TrimSpace(string(b))
			if !kb.ValidKind(kind) {
				jsonError(w, http.StatusBadRequest, "BAD_KIND", "unknown kind "+kind)
				return
			}
			continue
		case "tags":
			b, _ := io.ReadAll(io.LimitReader(part, 1024))
			for _, t := range strings.Split(string(b), ",") {
				if t = strings.TrimSpace(t); t != "" {
					tags = append(tags, t)
				}
			}
			continue
		case "file":
		default:
			continue
		}
		name := kb.SanitizeName(part.FileName())
		if !uploadAllowed[strings.ToLower(filepath.Ext(name))] {
			rejected = append(rejected, map[string]string{"name": part.FileName(), "reason": "file type not allowed"})
			continue
		}
		tmp, err := os.CreateTemp(tmpDir, "up-*")
		if err != nil {
			jsonError(w, http.StatusInternalServerError, "KB", err.Error())
			return
		}
		n, err := io.Copy(tmp, io.LimitReader(part, maxUploadFile+1))
		tmp.Close()
		if err != nil || n > maxUploadFile {
			os.Remove(tmp.Name())
			reason := "exceeds 200 MB"
			if err != nil {
				reason = err.Error()
			}
			rejected = append(rejected, map[string]string{"name": part.FileName(), "reason": reason})
			continue
		}
		res, err := lib.Add([]string{tmp.Name()}, kb.AddOptions{Kind: kind, Tags: tags, Move: true, Name: name})
		os.Remove(tmp.Name())
		if err != nil {
			rejected = append(rejected, map[string]string{"name": part.FileName(), "reason": err.Error()})
			continue
		}
		for i := range res {
			res[i].Source = part.FileName()
		}
		results = append(results, res...)
	}
	c.bus.publish("project", map[string]any{"workDir": d.ID, "change": "kb"})
	writeJSON(w, http.StatusOK, map[string]any{"added": results, "rejected": rejected})
}

// ── runs & agents ─────────────────────────────────────────────────────────

func (c *Console) handleRuns(w http.ResponseWriter, r *http.Request) {
	c.actMu.Lock()
	acts := append([]activityLike(nil), c.acts...)
	c.actMu.Unlock()
	sessions := discoverSessions(acts, c.opts.Now())
	active := 0
	for _, s := range sessions {
		if s.Active {
			active++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runs": c.runs.list(), "sessions": sessions, "activeSessions": active,
		"bridge": map[string]string{"status": "planned", "version": "v0.8",
			"note": "Local-agent bridge (Claude Code / Codex sessions, subagents, tool calls, permission prompts) is designed in docs/console-design.md and not implemented in v0.7. Sessions below are inferred from CLI client ids in the audit stream."},
	})
}

func (c *Console) handleRunEvent(w http.ResponseWriter, r *http.Request) {
	var ev RunEvent
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&ev); err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	run, err := c.runs.apply(ev)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_EVENT", err.Error())
		return
	}
	c.bus.publish("run", run)
	writeJSON(w, http.StatusOK, run)
}

// ── decisions ─────────────────────────────────────────────────────────────

func (c *Console) handleAskList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"decisions": c.asks.list(atoiDefault(r.URL.Query().Get("limit"), 100, 1, 1000))})
}

func (c *Console) handleAskCreate(w http.ResponseWriter, r *http.Request) {
	var req AskRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<17)).Decode(&req); err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	q, err := c.asks.create(req)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_QUESTION", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, q)
}

// handleAskGet long-polls with ?wait=<seconds> (≤ 60).
func (c *Console) handleAskGet(w http.ResponseWriter, r *http.Request) {
	wait := time.Duration(atoiDefault(r.URL.Query().Get("wait"), 0, 0, 60)) * time.Second
	done := make(chan struct{})
	go func() {
		select {
		case <-r.Context().Done():
		case <-c.done:
		}
		close(done)
	}()
	q, err := c.asks.wait(r.PathValue("id"), wait, done)
	if err != nil {
		jsonError(w, http.StatusNotFound, "NOT_FOUND", "unknown decision (the daemon may have restarted)")
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (c *Console) handleAskAnswer(w http.ResponseWriter, r *http.Request) {
	var body struct{ Choice, Note, By string }
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		jsonError(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}
	by := body.By
	if by != "cli" {
		by = "console"
	}
	q, err := c.asks.answer(r.PathValue("id"), body.Choice, body.Note, by)
	if errors.Is(err, errNotFound) {
		jsonError(w, http.StatusNotFound, "NOT_FOUND", "unknown decision")
		return
	}
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": map[string]string{"code": "NOT_ANSWERABLE", "message": err.Error()}, "decision": q})
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func (c *Console) handleAskCancel(w http.ResponseWriter, r *http.Request) {
	q, err := c.asks.cancel(r.PathValue("id"))
	if err != nil {
		jsonError(w, http.StatusNotFound, "NOT_FOUND", "unknown decision")
		return
	}
	writeJSON(w, http.StatusOK, q)
}

func atoiDefault(s string, def, lo, hi int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
