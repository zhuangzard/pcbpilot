package console

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SimRun is one simulation output with its headline metrics.
type SimRun struct {
	Kind    string             `json:"kind"` // sim-power | sim-analog | sim-post
	Path    string             `json:"path"`
	ModTime time.Time          `json:"modTime"`
	Status  string             `json:"status,omitempty"`
	Metrics map[string]float64 `json:"metrics"`
	Labels  map[string]string  `json:"labels,omitempty"`
	// Delta is metric − previous run of the same kind (same metric keys only).
	Delta    map[string]float64 `json:"delta,omitempty"`
	Previous string             `json:"previous,omitempty"`
	Error    string             `json:"error,omitempty"`
}

var simCache = struct {
	sync.Mutex
	m map[string]cachedSim
}{m: map[string]cachedSim{}}

type cachedSim struct {
	mod  time.Time
	size int64
	run  SimRun
}

// simRuns parses every sim artifact (cached by path+mtime+size) and computes
// deltas against the previous run of the same kind, oldest first.
func simRuns(dir string, arts []Artifact) []SimRun {
	var out []SimRun
	for _, a := range arts {
		if a.Kind != "sim-power" && a.Kind != "sim-analog" && a.Kind != "sim-post" {
			continue
		}
		out = append(out, parseSim(filepath.Join(dir, filepath.FromSlash(a.Path)), a))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ModTime.Before(out[j].ModTime) })
	prev := map[string]*SimRun{}
	for i := range out {
		r := &out[i]
		if p := prev[r.Kind]; p != nil {
			r.Previous = p.Path
			for k, v := range r.Metrics {
				if pv, ok := p.Metrics[k]; ok {
					if d := round4(v - pv); d != 0 {
						if r.Delta == nil {
							r.Delta = map[string]float64{}
						}
						r.Delta[k] = d
					}
				}
			}
		}
		prev[r.Kind] = r
	}
	return out
}

func parseSim(path string, a Artifact) SimRun {
	st, err := os.Stat(path)
	if err != nil {
		return SimRun{Kind: a.Kind, Path: a.Path, ModTime: a.ModTime, Error: err.Error()}
	}
	simCache.Lock()
	c, ok := simCache.m[path]
	simCache.Unlock()
	if ok && c.mod.Equal(st.ModTime()) && c.size == st.Size() {
		r := c.run
		r.Path = a.Path
		return r
	}
	r := SimRun{Kind: a.Kind, Path: a.Path, ModTime: a.ModTime, Metrics: map[string]float64{}, Labels: map[string]string{}}
	b, err := os.ReadFile(path)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	switch a.Kind {
	case "sim-power":
		parsePower(b, &r)
	case "sim-analog":
		parseAnalog(b, &r)
	case "sim-post":
		parsePost(b, &r)
	}
	simCache.Lock()
	simCache.m[path] = cachedSim{st.ModTime(), st.Size(), r}
	simCache.Unlock()
	return r
}

func parsePower(b []byte, r *SimRun) {
	var doc struct {
		Results []struct {
			Scenario  string `json:"scenario"`
			Converged bool   `json:"converged"`
			Nets      map[string]struct {
				Voltage  float64 `json:"voltage"`
				CurrentA float64 `json:"currentA"`
				Role     string  `json:"role"`
			} `json:"nets"`
			Parts map[string]struct {
				PowerW float64 `json:"powerW"`
			} `json:"parts"`
			Warnings []any `json:"warnings"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		r.Error = err.Error()
		return
	}
	conv := 0
	warns := 0
	for i, res := range doc.Results {
		if res.Converged {
			conv++
		}
		warns += len(res.Warnings)
		if i == 0 { // first scenario (typical) carries the rail table
			r.Labels["scenario"] = res.Scenario
			total := 0.0
			for net, n := range res.Nets {
				if n.Role == "power" {
					r.Metrics["V("+net+")"] = round4(n.Voltage)
					r.Metrics["I("+net+") A"] = round4(n.CurrentA)
				}
			}
			for _, p := range res.Parts {
				if p.PowerW > 0 {
					total += p.PowerW
				}
			}
			r.Metrics["Σ part power W"] = round4(total)
		}
	}
	r.Metrics["scenarios"] = float64(len(doc.Results))
	r.Metrics["converged"] = float64(conv)
	r.Metrics["warnings"] = float64(warns)
	r.Status = "PASS"
	if conv < len(doc.Results) {
		r.Status = "FAIL"
	} else if warns > 0 {
		r.Status = "WARN"
	}
}

func parseAnalog(b []byte, r *SimRun) {
	var doc struct {
		Summary struct {
			Blocks, Simulated, Targets, Met, Failing, Changes int
			Status                                            string
		} `json:"summary"`
		Findings []struct {
			Severity string `json:"severity"`
		} `json:"findings"`
		Ngspice any `json:"ngspice"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		r.Error = err.Error()
		return
	}
	s := doc.Summary
	r.Metrics["blocks"] = float64(s.Blocks)
	r.Metrics["simulated"] = float64(s.Simulated)
	r.Metrics["targets"] = float64(s.Targets)
	r.Metrics["met"] = float64(s.Met)
	r.Metrics["failing"] = float64(s.Failing)
	r.Metrics["planned changes"] = float64(s.Changes)
	countSeverities(r, doc.Findings)
	r.Status = strings.ToUpper(s.Status)
}

func parsePost(b []byte, r *SimRun) {
	var doc struct {
		Verdict struct {
			Status string `json:"status"`
		} `json:"verdict"`
		Thermal struct {
			MaxBoardC float64 `json:"maxBoardC"`
			TotalW    float64 `json:"totalW"`
		} `json:"thermal"`
		Nets []struct {
			Net      string  `json:"net"`
			WorstMV  float64 `json:"worstMV"`
			BudgetMV float64 `json:"budgetMV"`
		} `json:"nets"`
		Vias []struct {
			UsePct float64 `json:"usePct"`
		} `json:"vias"`
		Findings []struct {
			Severity string `json:"severity"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		r.Error = err.Error()
		return
	}
	r.Metrics["max board °C"] = round4(doc.Thermal.MaxBoardC)
	r.Metrics["total loss W"] = round4(doc.Thermal.TotalW)
	worst := 0.0
	for _, n := range doc.Nets {
		if n.BudgetMV > 0 {
			pct := n.WorstMV / n.BudgetMV * 100
			if pct > worst {
				worst = pct
				r.Labels["worst IR net"] = n.Net
			}
		}
		if n.WorstMV > 0 {
			r.Metrics["drop mV("+n.Net+")"] = round4(n.WorstMV)
		}
	}
	r.Metrics["worst IR % of budget"] = round4(worst)
	maxVia := 0.0
	for _, v := range doc.Vias {
		maxVia = math.Max(maxVia, v.UsePct)
	}
	r.Metrics["max via use %"] = round4(maxVia)
	countSeverities(r, doc.Findings)
	r.Status = strings.ToUpper(doc.Verdict.Status)
}

func countSeverities(r *SimRun, fs []struct {
	Severity string `json:"severity"`
}) {
	e, w := 0, 0
	for _, f := range fs {
		switch strings.ToLower(f.Severity) {
		case "error", "fail":
			e++
		case "warn", "warning":
			w++
		}
	}
	r.Metrics["findings error"] = float64(e)
	r.Metrics["findings warn"] = float64(w)
}

func round4(v float64) float64 { return math.Round(v*10000) / 10000 }

// ReportPkg is one design-report version package.
type ReportPkg struct {
	Dir         string `json:"dir"` // relative to the work dir
	Project     string `json:"project,omitempty"`
	Version     string `json:"version"`
	GeneratedAt string `json:"generatedAt"`
	Verdict     string `json:"verdict"`
	Files       int    `json:"files"`
	HTML        string `json:"html,omitempty"`
	Markdown    string `json:"markdown,omitempty"`
	Zip         string `json:"zip,omitempty"`
}

func reportPkgs(dir string, arts []Artifact) []ReportPkg {
	var out []ReportPkg
	for _, a := range arts {
		if a.Kind != "report" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(a.Path)))
		if err != nil {
			continue
		}
		var m struct {
			Project, Version, GeneratedAt, Verdict, Zip string
			Files                                       []struct{ Path, Role string }
		}
		if json.Unmarshal(b, &m) != nil {
			continue
		}
		pkgDir := filepath.ToSlash(filepath.Dir(filepath.FromSlash(a.Path)))
		p := ReportPkg{Dir: pkgDir, Project: m.Project, Version: m.Version, GeneratedAt: m.GeneratedAt, Verdict: m.Verdict, Files: len(m.Files)}
		for _, f := range m.Files {
			switch f.Role {
			case "report-html":
				p.HTML = pkgDir + "/" + f.Path
			case "report-md":
				p.Markdown = pkgDir + "/" + f.Path
			}
		}
		if p.HTML == "" {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(pkgDir), "report.html")); err == nil {
				p.HTML = pkgDir + "/report.html"
			}
		}
		if m.Zip != "" {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(pkgDir), "..", m.Zip)); err == nil {
				p.Zip = filepath.ToSlash(filepath.Join(filepath.Dir(filepath.FromSlash(pkgDir)), m.Zip))
			}
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GeneratedAt > out[j].GeneratedAt })
	return out
}
