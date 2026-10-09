package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// Routing backends of `kicad route` (--router fastroute|tracemaker|both).

const (
	routerFastroute  = "fastroute"
	routerTracemaker = "tracemaker"
	routerBoth       = "both"
)

func validRouter(s string) bool {
	return s == routerFastroute || s == routerTracemaker || s == routerBoth
}

func failedGateList(gs []gateResult) string {
	var f []string
	for _, g := range gs {
		if !g.Pass {
			f = append(f, g.Gate)
		}
	}
	return strings.Join(f, ", ")
}

func candidateSummary(cs []*routeCandidate) []map[string]any {
	var out []map[string]any
	for _, c := range cs {
		m := map[string]any{"router": c.Name, "tried": c.Tried, "fullGatesPass": c.FullPass, "quickPass": c.Quick, "newDrcErrors": c.NewDRC, "cancelled": c.Cancelled}
		if c.QuickNote != "" {
			m["quickNote"] = c.QuickNote
		}
		if c.Err != nil {
			m["error"] = c.Err.Error()
		}
		if c.Res != nil {
			m["unrouted"], m["seconds"], m["violations"] = c.Res.UnroutedCount, c.Res.Seconds, c.Res.Violations
		}
		out = append(out, m)
	}
	return out
}

// routeBackends prepares the DSN (the pre-route gate every backend shares),
// runs the chosen backend(s) and returns the candidates in the order they
// go through the full gates.
func (r *kicadRun) routeBackends(classed string, reqs map[string]kicad.NetRequirement, inSnap *boardSnapshot) ([]*routeCandidate, error) {
	o := &r.o
	dsnPath, floorMil, err := r.prepareRoute(classed, reqs, inSnap)
	if err != nil {
		return nil, err
	}
	var cands []*routeCandidate
	if o.router != routerTracemaker {
		cands = append(cands, r.fastrouteCandidate(classed, dsnPath))
	}
	if o.router != routerFastroute {
		cands = append(cands, r.tracemakerCandidate(classed, reqs, floorMil))
	}
	r.summary["routerMode"] = o.router
	if len(cands) == 1 {
		c := cands[0]
		c.Res, c.Err = c.exec(context.Background())
		r.lap(c.Name + " (route)")
		if c.Err != nil {
			return nil, c.Err
		}
		if c.Board, c.Err = c.commit(c.Res); c.Err != nil {
			return nil, c.Err
		}
		return cands, nil
	}
	winner := raceCandidates(context.Background(), cands, r.quickCheckCandidate)
	r.lap("routers (" + o.router + ")")
	for _, c := range cands {
		switch {
		case c == winner:
			fmt.Fprintf(r.stderr, "router %s passed the quick check first (%d unrouted, %.0f s); the others are fallbacks\n", c.Name, c.Res.UnroutedCount, c.Res.Seconds)
		case c.Err != nil:
			fmt.Fprintf(r.stderr, "router %s: %v\n", c.Name, c.Err)
		case c.Res != nil:
			fmt.Fprintf(r.stderr, "router %s: quick check failed (%s)\n", c.Name, c.QuickNote)
		}
	}
	return rankCandidates(cands, winner), nil
}

// rerunCandidate runs a candidate that was cancelled before it finished,
// because the winner failed a full gate and this is the next one to try.
func (r *kicadRun) rerunCandidate(c *routeCandidate) error {
	fmt.Fprintf(r.stderr, "router %s: rerunning (it was stopped when another router passed the quick check)\n", c.Name)
	res, err := c.exec(context.Background())
	if err != nil {
		c.Err = err
		return err
	}
	c.Res, c.Err, c.Cancelled = res, nil, false
	return nil
}

// useCandidate makes c's result the run's route (what the route-complete
// gate and the report read).
func (r *kicadRun) useCandidate(c *routeCandidate, all []*routeCandidate) {
	r.route = c.Res
	r.summary["router"] = c.Name
	r.summary["routeResult"] = c.Res
	fmt.Fprintf(r.stderr, "router %s: running the full gates on its result (%d unrouted)\n", c.Name, c.Res.UnroutedCount)
}

// quickCheckCandidate: complete and no DRC error the input board lacked.
func (r *kicadRun) quickCheckCandidate(c *routeCandidate) {
	if c.Res != nil && c.Res.UnroutedCount > 0 {
		c.Quick, c.QuickNote = quickCheck(c.Res, 0)
		return
	}
	rep, err := r.kt.DRC(c.Board, filepath.Join(r.work, "quick-"+c.Name+"-drc.json"))
	if err != nil {
		c.Quick, c.QuickNote = false, "DRC did not run: "+err.Error()
		return
	}
	for _, v := range rep.Violations {
		if !r.inputDRC[v.Rule+"|"+v.Message] {
			c.NewDRC++
		}
	}
	c.Quick, c.QuickNote = quickCheck(c.Res, c.NewDRC)
}

func (r *kicadRun) fastrouteCandidate(classed, dsnPath string) *routeCandidate {
	o := r.o
	base := strings.TrimSuffix(dsnPath, ".dsn")
	var ses string
	var runs []fastrouteRun
	c := &routeCandidate{Name: routerFastroute}
	c.exec = func(ctx context.Context) (*routeResult, error) {
		fo := o.fo
		fo.ctx = ctx
		var err error
		ses, runs, err = runFastroute(fo, dsnPath, base, &syncWriter{w: r.stderr})
		if err != nil {
			return nil, err
		}
		last := lastOK(runs)
		if last == nil {
			return nil, fmt.Errorf("fastroute produced no usable result")
		}
		res := fastrouteResult(last, fo, dsnPath)
		if len(res.Blocked) > 0 && r.escEnv != nil {
			res.PlacementHints = placementHints(res.Blocked, r.escEnv, intentRequirements(r.in))
		}
		return res, nil
	}
	c.commit = func(res *routeResult) (string, error) {
		r.summary["router"], r.summary["routerRuns"] = routerFastroute, runs
		r.summary["routerSettings"] = map[string]any{"multiStart": o.fo.multiStart, "threads": o.fo.threads, "optimizer": !o.fo.noOptimizer, "optimizerThresholdPct": o.fo.optThreshold}
		if last := lastOK(runs); last != nil {
			r.summary["routeFinal"] = last
			if last.Blocked > 0 {
				r.summary["blockedConnections"] = last.BlockedList
				fmt.Fprintf(r.stderr, "fastroute: %d of %d unrouted connection(s) blocked by geometry (--diagnose): placement / escapes must change\n", last.Blocked, last.Unrouted)
			}
		}
		r.summary["ses"] = ses
		for _, h := range res.PlacementHints {
			fmt.Fprintf(r.stderr, "placement hint: %s\n", h)
		}
		imported := r.next("imported")
		imp, err := r.kt.ImportSES(classed, ses, imported)
		r.summary["sesImport"] = imp
		if err != nil {
			return "", err
		}
		return r.addEscapes(imported, ses)
	}
	return c
}

func (r *kicadRun) tracemakerCandidate(classed string, reqs map[string]kicad.NetRequirement, floorMil float64) *routeCandidate {
	o := r.o
	dir := filepath.Join(o.outDir, "tracemaker")
	in := filepath.Join(dir, "in.kicad_pcb")
	out := filepath.Join(dir, "out.kicad_pcb")
	report := filepath.Join(dir, "report.json")
	cons := filepath.Join(dir, "constraints.json")
	c := &routeCandidate{Name: routerTracemaker}
	c.exec = func(ctx context.Context) (*routeResult, error) {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		if _, err := copyKicadBoard(classed, in); err != nil {
			return nil, err
		}
		if err := writeJSONFile(cons, tracemakerConstraints(reqs, floorMil, r.in)); err != nil {
			return nil, err
		}
		tmo := o.tm
		tmo.threads = o.fo.threads
		if tmo.threads == 0 && o.router == routerBoth {
			tmo.threads = 4 // leave cores to the other router
		}
		res, err := runTracemaker(ctx, tmo, in, out, cons, report, &syncWriter{w: r.stderr})
		if err != nil {
			return nil, err
		}
		if len(res.Blocked) > 0 && r.escEnv != nil {
			res.PlacementHints = placementHints(res.Blocked, r.escEnv, intentRequirements(r.in))
		}
		return res, nil
	}
	c.commit = func(res *routeResult) (string, error) {
		data, err := os.ReadFile(out)
		if err != nil {
			return "", err
		}
		board := r.next("tracemaker")
		if _, err := copyKicadBoard(classed, board); err != nil { // .kicad_pro / .kicad_dru beside it
			return "", err
		}
		if err := os.WriteFile(board, data, 0o644); err != nil {
			return "", err
		}
		// tracemaker routes with --soft-zones: the zone fills are stale.
		if _, err := r.kt.Fill(board); err != nil {
			return "", fmt.Errorf("zone refill after tracemaker: %w", err)
		}
		r.summary["tracemaker"] = map[string]any{"bin": o.tm.bin, "constraints": cons, "report": report, "result": res}
		return board, nil
	}
	return c
}
