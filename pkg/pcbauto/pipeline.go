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
		drc := CheckDRC(b, an, st, rr.Tracks, rr.Vias)
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
	return res, nil
}
