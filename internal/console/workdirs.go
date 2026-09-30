package console

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/daemon"
	"github.com/zhuangzard/pcbpilot/pkg/projectconfig"
)

type activityLike = daemon.ActionEvent

// A work dir is a local project folder: where the agent runs the CLI, keeps
// intent/sim/post/report artifacts, pcbpilot.project.json and resources/.
// Registered work dirs live in ~/.pcbpilot/console/workdirs.json (shared with
// the CLI: `project-config init` and `console projects add` register too).

// WorkDir is one registered folder.
type WorkDir struct {
	ID      string    `json:"id"`
	Dir     string    `json:"dir"`
	Name    string    `json:"name,omitempty"`
	AddedAt time.Time `json:"addedAt"`
	// Source: cli | console | audit (discovered from an audit outputDir that
	// holds a pcbpilot.project.json)
	Source string `json:"source,omitempty"`
}

// WorkDirID is a stable short id for a folder path.
func WorkDirID(dir string) string {
	h := sha256.Sum256([]byte(filepath.Clean(dir)))
	return hex.EncodeToString(h[:6])
}

type workDirsFile struct {
	SchemaVersion int       `json:"schemaVersion"`
	Dirs          []WorkDir `json:"dirs"`
}

var workDirsMu sync.Mutex

// WorkDirsPath is the registry path under a pcbpilot home.
func WorkDirsPath(home string) string { return filepath.Join(home, "console", "workdirs.json") }

// LoadWorkDirs reads the registered work dirs.
func LoadWorkDirs(home string) ([]WorkDir, error) {
	b, err := os.ReadFile(WorkDirsPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f workDirsFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	return f.Dirs, nil
}

// RegisterWorkDir adds (or refreshes) a folder. Only existing directories are
// accepted; the path is stored absolute and symlink-resolved.
func RegisterWorkDir(home, dir, name, source string) (WorkDir, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return WorkDir{}, err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	st, err := os.Stat(abs)
	if err != nil {
		return WorkDir{}, err
	}
	if !st.IsDir() {
		return WorkDir{}, fmt.Errorf("%s is not a directory", abs)
	}
	workDirsMu.Lock()
	defer workDirsMu.Unlock()
	dirs, err := LoadWorkDirs(home)
	if err != nil {
		return WorkDir{}, err
	}
	wd := WorkDir{ID: WorkDirID(abs), Dir: abs, Name: name, AddedAt: time.Now().UTC(), Source: source}
	for i, d := range dirs {
		if d.ID == wd.ID {
			if name != "" {
				dirs[i].Name = name
			}
			return dirs[i], saveWorkDirs(home, dirs)
		}
	}
	if wd.Name == "" {
		wd.Name = filepath.Base(abs)
	}
	dirs = append(dirs, wd)
	return wd, saveWorkDirs(home, dirs)
}

// UnregisterWorkDir forgets a folder (files are untouched).
func UnregisterWorkDir(home, id string) error {
	workDirsMu.Lock()
	defer workDirsMu.Unlock()
	dirs, err := LoadWorkDirs(home)
	if err != nil {
		return err
	}
	var keep []WorkDir
	found := false
	for _, d := range dirs {
		if d.ID == id {
			found = true
			continue
		}
		keep = append(keep, d)
	}
	if !found {
		return errNotFound
	}
	return saveWorkDirs(home, keep)
}

func saveWorkDirs(home string, dirs []WorkDir) error {
	path := WorkDirsPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(workDirsFile{SchemaVersion: 1, Dirs: dirs}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ── artifact scan ─────────────────────────────────────────────────────────

// Artifact is one recognised design artifact in a work dir.
type Artifact struct {
	Kind      string    `json:"kind"` // intent | sim-power | sim-analog | sim-post | feedback | plan | report | report-index | check | drc | board | board-routed | project-config
	Step      string    `json:"step"` // design-flow step it evidences
	Path      string    `json:"path"` // relative to the work dir
	ModTime   time.Time `json:"modTime"`
	Bytes     int64     `json:"bytes"`
	SHA256    string    `json:"sha256,omitempty"`
	Generator string    `json:"generator,omitempty"`
	Aliases   []string  `json:"aliases,omitempty"`
}

var generatorRe = regexp.MustCompile(`"generator"\s*:\s*"([^"]+)"`)

// kindByGenerator maps the "generator" field to a kind and step.
var kindByGenerator = map[string][2]string{
	"pcbpilot intent derive":   {"intent", "S6.5"},
	"pcbpilot sim power":       {"sim-power", "S6.5"},
	"pcbpilot sim analog":      {"sim-analog", "S5.5"},
	"pcbpilot sim post-layout": {"sim-post", "P10.5"},
	"pcbpilot report design":   {"report", "P11"},
}

// skipDirs are never descended.
var skipDirs = map[string]bool{".git": true, "node_modules": true, ".pcbpilot-kb": true, "resources": true, "vendor": true, "__pycache__": true}

const (
	scanMaxDepth = 6
	scanMaxFiles = 20000
)

// scanArtifacts walks a work dir (bounded) and classifies JSON artifacts by
// their generator header or well-known name. Identical content (sha256) under
// several paths is reported once with aliases.
func scanArtifacts(dir string) ([]Artifact, error) {
	var out []Artifact
	files := 0
	bySHA := map[string]int{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if path == dir {
				return err
			}
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			if path != dir && (skipDirs[d.Name()] || strings.Count(rel, string(filepath.Separator)) >= scanMaxDepth) {
				return filepath.SkipDir
			}
			return nil
		}
		files++
		if files > scanMaxFiles {
			return filepath.SkipAll
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".json") {
			return nil
		}
		kind, step, gen := classify(path, name)
		if kind == "" {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		a := Artifact{Kind: kind, Step: step, Path: filepath.ToSlash(rel), ModTime: info.ModTime().UTC(), Bytes: info.Size(), Generator: gen}
		if kind != "board" && info.Size() < 64<<20 {
			a.SHA256 = fileSHA(path)
			if i, ok := bySHA[a.SHA256]; ok && a.SHA256 != "" {
				// Prefer the original over a report package's data/ copy as
				// the primary path (the copy keeps the original's content but
				// a later mtime).
				if packagedCopy(out[i].Path) && !packagedCopy(a.Path) {
					a.Aliases = append(out[i].Aliases, out[i].Path)
					out[i] = a
				} else {
					out[i].Aliases = append(out[i].Aliases, a.Path)
				}
				return nil
			}
			bySHA[a.SHA256] = len(out)
		}
		out = append(out, a)
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.Before(out[j].ModTime) })
	return out, err
}

var packagedRe = regexp.MustCompile(`(^|/)v\d+/data/`)

// packagedCopy reports a path inside a design-report package (vN/data/…).
func packagedCopy(p string) bool { return packagedRe.MatchString(p) }

func fileSHA(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// classify peeks at the head of a JSON file.
func classify(path, name string) (kind, step, gen string) {
	if name == projectconfig.FileName {
		return "project-config", "", ""
	}
	f, err := os.Open(path)
	if err != nil {
		return "", "", ""
	}
	defer f.Close()
	head := make([]byte, 4096)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if m := generatorRe.FindSubmatch(head); m != nil {
		gen = string(m[1])
		if ks, ok := kindByGenerator[gen]; ok {
			if ks[0] == "report" && name == "index.json" {
				return "report-index", "P11", gen
			}
			if ks[0] == "report" && name != "manifest.json" {
				return "", "", "" // report.json duplicates the manifest
			}
			return ks[0], ks[1], gen
		}
	}
	switch {
	case bytes.Contains(head, []byte(`"source": "pcb auto run"`)) || bytes.Contains(head, []byte(`"source":"pcb auto run"`)):
		return "feedback", "P7", ""
	case name == "plan.json" && bytes.Contains(head, []byte(`"joint"`)) || name == "plan.json" && bytes.HasPrefix(bytes.TrimSpace(head), []byte(`{`)) && bytes.Contains(head, []byte(`"result"`)) && bytes.Contains(head, []byte(`"circuit"`)):
		return "plan", "P7", ""
	case bytes.Contains(head, []byte(`"passed"`)) && bytes.Contains(head, []byte(`"danglingEnds"`)):
		return "check", "P10", "" // pcb check --json
	case bytes.Contains(head, []byte(`"violations"`)) && bytes.Contains(head, []byte(`"passed"`)):
		return "drc", "P10", ""
	case bytes.Contains(head, []byte(`"components"`)) && bytes.Contains(head, []byte(`"primitiveId"`)):
		if strings.Contains(name, "routed") {
			return "board-routed", "P8", "" // pcb auto engine result, not a live readback
		}
		return "board", "P10", ""
	}
	return "", "", ""
}

// ── timeline ──────────────────────────────────────────────────────────────

// StepStatus is one design-flow step on the timeline.
type StepStatus struct {
	ID       string    `json:"id"`
	Title    string    `json:"title"`
	Domain   string    `json:"domain"`
	Status   string    `json:"status"` // done | implied | pending | skipped
	Reason   string    `json:"reason,omitempty"`
	At       time.Time `json:"at,omitempty"`
	Evidence []string  `json:"evidence,omitempty"`
}

// workflowStepOf maps the legacy workflow stage (diagnostic only) to the step
// whose completion it records.
var workflowStepOf = map[string]string{
	"imported":            "P1",
	"placement_ready":     "P5",
	"placement_confirmed": "P6",
	"outline_confirmed":   "P2",
	"pre_route_passed":    "P6",
	"routing_authorized":  "P7",
	"post_route_checked":  "P10",
}

// buildTimeline derives step status from artifacts, workflow events and the
// project config. It is evidence, not a gate: a step without an artifact is
// "pending" (no evidence found), which is not the same as "not done".
func buildTimeline(cfg *projectconfig.Config, arts []Artifact, wfEvents []wfEvent) []StepStatus {
	ev := map[string]*StepStatus{}
	var out []StepStatus
	for _, s := range projectconfig.Steps {
		out = append(out, StepStatus{ID: s.ID, Title: s.Title, Domain: s.Domain, Status: "pending"})
	}
	for i := range out {
		ev[out[i].ID] = &out[i]
	}
	mark := func(step string, at time.Time, what string) {
		st := ev[step]
		if st == nil {
			return
		}
		if st.Status == "pending" {
			st.Status = "evidence"
		}
		if at.After(st.At) {
			st.At = at
		}
		if len(st.Evidence) < 8 {
			st.Evidence = append(st.Evidence, what)
		}
	}
	for _, a := range arts {
		if a.Step != "" {
			mark(a.Step, a.ModTime, a.Kind+": "+a.Path)
		}
	}
	for _, e := range wfEvents {
		if step := workflowStepOf[e.Stage]; step != "" && (e.Action == "confirm" || e.Action == "gate-pass" || e.Action == "force") {
			mark(step, e.At, "workflow "+e.Action+" "+e.Stage)
		}
	}
	// A later step with evidence implies earlier steps ran (flow order), but
	// only as "done (implied)" — shown distinctly from direct evidence.
	last := -1
	for i := range out {
		if out[i].Status == "evidence" {
			last = i
		}
	}
	for i := 0; i < last; i++ {
		if out[i].Status == "pending" {
			out[i].Status = "implied"
			out[i].Reason = "no own artifact; a later step has evidence"
		}
	}
	for i := range out {
		if out[i].Status == "evidence" {
			out[i].Status = "done"
		}
		if cfg != nil && !cfg.StepEnabled(out[i].ID) {
			reason := ""
			for _, s := range cfg.SkippedSteps() {
				if s.ID == out[i].ID {
					reason = s.Reason
				}
			}
			if out[i].Status == "done" {
				out[i].Reason = "skipped by config but evidence exists: " + reason
				continue
			}
			out[i].Status, out[i].Reason = "skipped", reason
		}
	}
	return out
}

type wfEvent struct {
	Stage  string
	Action string
	At     time.Time
}
