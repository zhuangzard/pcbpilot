package pcbauto

import (
	"context"
	"time"
)

// Options configure the end-to-end pipeline.
type Options struct {
	Power PowerSpec    `json:"power"`
	Stack StackOptions `json:"stack"`
	Route RouteOptions `json:"route"`
	// NoEscalate keeps the first stackup even when routing is incomplete.
	NoEscalate bool `json:"noEscalate,omitempty"`
}

// Result is everything the pipeline decided and produced.
type Result struct {
	Analysis *Analysis    `json:"analysis"`
	Stackup  *Stackup     `json:"stackup"`
	Route    *RouteResult `json:"route"`
	DRC      *DRCReport   `json:"drc"`
	Attempts []Attempt    `json:"attempts"`
}

// Attempt records one stackup variant tried by the pipeline.
type Attempt struct {
	Stack      string  `json:"stack"`
	Completion float64 `json:"completion"`
	Vias       int     `json:"vias"`
	Violations int     `json:"violations"`
	Millis     int64   `json:"millis"`
}

func stackLabel(s *Stackup) string {
	out := ""
	for i, l := range s.Stack {
		if i > 0 {
			out += "/"
		}
		out += l.Name
	}
	return out
}

// Run analyses the board, decides the stackup, routes, and — when a 4+ layer
// board does not complete on its outer layers — escalates the power plane to
// a mixed signal+pour layer and keeps the better result.
func Run(ctx context.Context, b *Board, opt Options) (*Result, error) {
	pre := Analyze(b, opt.Power, nil)
	st := DecideStackup(b, pre, opt.Stack)
	res := &Result{}
	try := func(st *Stackup) (*Analysis, *RouteResult, *DRCReport, error) {
		an := Analyze(b, opt.Power, st)
		start := time.Now()
		rr, err := Route(ctx, b, st, an, opt.Route)
		if err != nil {
			return nil, nil, nil, err
		}
		if n := MicroFix(b, an, st, rr); n > 0 {
			rr.Notes = append(rr.Notes, sprintf("micro-fix: %d sub-0.25 mil clearance shortfall(s) cleared by shifting or narrowing a track", n))
		}
		drc := CheckDRCStrict(b, an, st, rr.Tracks, rr.Vias)
		res.Attempts = append(res.Attempts, Attempt{Stack: stackLabel(st), Completion: rr.Stats.Completion,
			Vias: rr.Stats.Vias + rr.Stats.FanoutVias, Violations: len(drc.Violations), Millis: time.Since(start).Milliseconds()})
		return an, rr, drc, nil
	}
	an, rr, drc, err := try(st)
	if err != nil {
		return nil, err
	}
	res.Analysis, res.Stackup, res.Route, res.DRC = an, st, rr, drc
	if !opt.NoEscalate && rr.Stats.Completion < 97 && st.Layers >= 4 {
		alt := *st
		alt.Stack = append([]StackLayer(nil), st.Stack...)
		alt.Reasons = append([]string(nil), st.Reasons...)
		if alt.MixedPower() {
			an2, rr2, drc2, err := try(&alt)
			if err != nil {
				return nil, err
			}
			if rr2.Stats.Completion > rr.Stats.Completion {
				res.Analysis, res.Stackup, res.Route, res.DRC = an2, &alt, rr2, drc2
			}
		}
	}
	// A quick board whose high-speed nets came out with skew, plane-split
	// crossings or extra vias gets up to three passes on finer grids: the pair
	// geometry depends on the grid phase, and on the ESP32 mini (2026-09-25)
	// the default 3.2 mil grid put USB_DM across the IN2 power split 16 times
	// while 2.5 mil routed it clean — the E2E agent had to find that by hand.
	if !opt.NoEscalate && opt.Route.GridMil <= 0 && len(res.Attempts) > 0 && res.Attempts[0].Millis < 20000 {
		for _, k := range []float64{0.9, 0.8, 0.7} {
			if hsFindings(CheckSI(b, res.Analysis, res.Stackup, res.Route)) == 0 {
				break
			}
			fine := opt
			fine.Route.GridMil = k * defaultGrid(b.Rules)
			an2, rr2, drc2, err := func() (*Analysis, *RouteResult, *DRCReport, error) {
				an := Analyze(b, opt.Power, res.Stackup)
				start := time.Now()
				rr, err := Route(ctx, b, res.Stackup, an, fine.Route)
				if err != nil {
					return nil, nil, nil, err
				}
				if n := MicroFix(b, an, res.Stackup, rr); n > 0 {
					rr.Notes = append(rr.Notes, sprintf("micro-fix: %d sub-0.25 mil clearance shortfall(s) cleared by shifting or narrowing a track", n))
				}
				drc := CheckDRCStrict(b, an, res.Stackup, rr.Tracks, rr.Vias)
				res.Attempts = append(res.Attempts, Attempt{Stack: stackLabel(res.Stackup) + sprintf(" grid %.2f", fine.Route.GridMil), Completion: rr.Stats.Completion,
					Vias: rr.Stats.Vias + rr.Stats.FanoutVias, Violations: len(drc.Violations), Millis: time.Since(start).Milliseconds()})
				return an, rr, drc, nil
			}()
			if err != nil {
				return nil, err
			}
			if betterHS(b, rr2, drc2, an2, res) {
				res.Analysis, res.Route, res.DRC = an2, rr2, drc2
				res.Route.Notes = append(res.Route.Notes, sprintf("high-speed retry: finer %.2f mil grid kept (fewer skew/split/via findings)", fine.Route.GridMil))
			}
		}
	}
	return res, nil
}

// hsFindings counts the SI findings the joint score treats as defects.
func hsFindings(si *SIReport) int {
	n := 0
	for _, f := range si.Findings {
		switch f.Kind {
		case "skew", "split-crossing", "vias":
			n++
		}
	}
	return n
}

// betterHS keeps the retry only when it is no worse on completion and DRC
// and strictly better on high-speed findings.
func betterHS(b *Board, rr2 *RouteResult, drc2 *DRCReport, an2 *Analysis, res *Result) bool {
	c1, c2 := res.Route.Stats.Completion, rr2.Stats.Completion
	if c2 != c1 {
		return c2 > c1
	}
	if v1, v2 := len(res.DRC.Violations), len(drc2.Violations); v2 != v1 {
		return v2 < v1
	}
	return hsFindings(CheckSI(b, an2, res.Stackup, rr2)) < hsFindings(CheckSI(b, res.Analysis, res.Stackup, res.Route))
}
