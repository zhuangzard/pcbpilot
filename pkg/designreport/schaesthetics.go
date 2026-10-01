package designreport

import (
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

// SchPage is one schematic page snapshot given to the report (--sch-snapshot).
type SchPage struct {
	Label    string
	Snapshot *schaes.Snapshot
}

// SchAestheticsSection is §6C "原理图美观度" (report.json key
// "schAesthetics"): report-only schematic aesthetics per page. It is an
// ADDITIONAL section: it never changes the verdict or any other section and
// is never recorded as missing.
type SchAestheticsSection struct {
	Profile     string       `json:"profile"`
	ProfileNote string       `json:"profileNote"`
	Weight      float64      `json:"weight"`
	Boundary    string       `json:"boundary"`
	Pages       []SchAesPage `json:"pages"`
	Priority    []string     `json:"priority"`
}

// SchAesPage is one page's result.
type SchAesPage struct {
	Label      string   `json:"label"`
	Source     string   `json:"source"`
	Score      string   `json:"score"`
	Verdict    string   `json:"verdict"`
	Wiring     string   `json:"wiring"`
	Layout     string   `json:"layout"`
	Labels     string   `json:"labels"`
	WiredShare string   `json:"wiredShare"`
	Measured   int      `json:"measured"`
	Skipped    int      `json:"skipped"`
	Metrics    []AesRow `json:"metrics"`
	Lanes      []KV     `json:"lanes,omitempty"`
	Notes      []string `json:"notes,omitempty"`
}

func (c *ctx) buildSchAesthetics() {
	if len(c.in.SchPages) == 0 {
		return // additional section: absent, not missing
	}
	// Same style word as the PCB side when the pcb auto run named a preset.
	var prof *schaes.Profile
	if pl := c.in.Plan; pl != nil && pl.Joint != nil && pl.Joint.Aesthetics != nil {
		switch n := pl.Joint.Aesthetics.Profile.Name; n {
		case "auto":
			prof = &schaes.Profile{Name: "auto"}
		default:
			if p, err := schaes.ProfileByName(n); err == nil && n != "" {
				prof = &p
			}
		}
	}
	s := &SchAestheticsSection{Weight: 0, Priority: schaes.Priority}
	grp := func(r *schaes.Report, g string) string {
		if v, ok := r.Groups[g]; ok {
			return f1(v)
		}
		return "—"
	}
	for _, pg := range c.in.SchPages {
		if pg.Snapshot == nil {
			continue
		}
		r := schaes.Analyze(pg.Snapshot, prof)
		if s.Profile == "" {
			s.Profile = r.Profile.Name
			s.ProfileNote = sprintf("计划权重 %s（Phase A 实际 0），落格 %s/%s units，对齐容差 %s，长线 %s units 或 >%d 交叉宜改标签，短于 %s units 的信号网宜用导线",
				f2(r.Profile.Weight), f1(r.Profile.GridUnits), f1(r.Profile.TargetGrid), f1(r.Profile.AlignTol), f1(r.Profile.LongWire), r.Profile.LongCrossings, f1(r.Profile.ShortLabel))
			if a := r.Profile.Auto; a != nil {
				s.ProfileNote += "；auto 选档：" + a.Reason
			}
			s.Boundary = r.Boundary
		}
		p := SchAesPage{Label: pg.Label, Source: r.Source, Score: f1(r.Score), Verdict: r.Verdict,
			Wiring: grp(r, schaes.GroupWiring), Layout: grp(r, schaes.GroupLayout), Labels: grp(r, schaes.GroupLabels),
			WiredShare: f3(r.WiredShare), Measured: r.Measured, Skipped: r.Skipped, Notes: r.Notes}
		for _, m := range r.Metrics {
			row := AesRow{ID: m.ID, Name: m.Name, Detail: m.Detail}
			if m.Skipped {
				row.Value, row.Score, row.Detail = "—", "—", "未测："+m.Reason
			} else {
				row.Value, row.Score = f3(m.Value), f1(m.Score)
			}
			p.Metrics = append(p.Metrics, row)
		}
		for _, l := range r.Lanes {
			p.Lanes = append(p.Lanes, KV{l.Candidate.Kind + " " + l.Candidate.Suggested, strings.Join(l.Candidate.Members, ", "), l.Note})
		}
		s.Pages = append(s.Pages, p)
	}
	if len(s.Pages) > 0 {
		c.rep.SchAesthetics = s
	}
}
