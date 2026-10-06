package app

// pcb_widen.go — `pcb widen`: widen the tracks of chosen nets (e.g. valve
// drain nets) up to a maximum width wherever the clearance to other-net copper
// allows. Ported from the Gas Module V5 widen_drv.py, which was used after a
// fastroute route; the router keeps every net at its class width, and
// high-current nets want more copper where there is room.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

type widenVia struct {
	Net      string  `json:"net"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Diameter float64 `json:"diameter"`
}

type widenOp struct {
	Track    specctra.Track `json:"track"`
	NewWidth float64        `json:"newWidth"`
	// SteppedBack records the DRC guard's reductions (from→to).
	SteppedBack []string `json:"steppedBack,omitempty"`
}

// planWiden returns the tracks of nets that can grow by more than 2 mil.
// Free space is measured from points every ~4 mil along the track to the
// edges of other-net tracks on the same layer (including tracks widened
// earlier in this plan), other-net vias and other-net pads on the same layer
// or MULTI. Pads use their axis-aligned rectangle (90° rotations swap W/H).
func planWiden(tracks []specctra.Track, vias []widenVia, pads []boardPad, nets map[string]bool, maxMil, clearanceMil float64) []widenOp {
	return planWidenTo(tracks, vias, pads, func(t specctra.Track) float64 {
		if nets[t.Net] {
			return maxMil
		}
		return 0
	}, clearanceMil, 2)
}

// planWidenToIntent grows every track below its net's intent width (outer /
// inner by layer) towards it, as far as the clearance allows — a fastroute
// neck-down left mid-run (Gas Module v11 A: GND at 10.82 mil, 84 mil from
// any pad). Tracks it cannot bring to full width stay listed by the gate.
func planWidenToIntent(tracks []specctra.Track, vias []widenVia, pads []boardPad, reqs map[string]specctra.NetRequirement, clearanceMil float64) []widenOp {
	return planWidenTo(tracks, vias, pads, func(t specctra.Track) float64 {
		r, ok := reqs[t.Net]
		if !ok {
			return 0
		}
		need := r.OuterMil
		if t.Layer >= 15 {
			need = r.InnerMil
		}
		if t.Width+specctraEps >= need {
			return 0
		}
		return need
	}, clearanceMil, 0.1)
}

// planWidenTo widens each track towards target(t) (0 = leave it), limited by
// the free space to other nets' copper minus clearanceMil; only gains above
// minGain are kept.
func planWidenTo(tracks []specctra.Track, vias []widenVia, pads []boardPad, target func(specctra.Track) float64, clearanceMil, minGain float64) []widenOp {
	type wide struct {
		a, b  [2]float64
		layer int
		width float64
		net   string
	}
	var widened []wide
	var ops []widenOp
	for _, t := range tracks {
		goal := target(t)
		if goal <= 0 || t.Locked {
			continue
		}
		a, b := [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}
		n := max(6, int(math.Hypot(b[0]-a[0], b[1]-a[1])/4))
		samples := make([][2]float64, n+1)
		for k := 0; k <= n; k++ {
			f := float64(k) / float64(n)
			samples[k] = [2]float64{a[0] + (b[0]-a[0])*f, a[1] + (b[1]-a[1])*f}
		}
		minTo := func(d func(q [2]float64) float64) float64 {
			m := math.Inf(1)
			for _, q := range samples {
				m = math.Min(m, d(q))
			}
			return m
		}
		free := math.Inf(1)
		for _, o := range tracks {
			if o.Net == t.Net || o.Layer != t.Layer {
				continue
			}
			oa, ob := [2]float64{o.X1, o.Y1}, [2]float64{o.X2, o.Y2}
			free = math.Min(free, minTo(func(q [2]float64) float64 { return segDist(q, oa, ob) })-o.Width/2)
		}
		for _, w := range widened {
			if w.layer == t.Layer && w.net != t.Net {
				free = math.Min(free, minTo(func(q [2]float64) float64 { return segDist(q, w.a, w.b) })-w.width/2)
			}
		}
		for _, v := range vias {
			if v.Net != t.Net {
				free = math.Min(free, minTo(func(q [2]float64) float64 { return math.Hypot(q[0]-v.X, q[1]-v.Y) })-v.Diameter/2)
			}
		}
		for _, p := range pads {
			if p.Net == t.Net || (p.Layer != t.Layer && p.Layer != pcbLayerMulti) {
				continue
			}
			// Distance to the pad rectangle in its own frame (any rotation).
			sn, cs := math.Sincos(-p.Rotation * math.Pi / 180)
			free = math.Min(free, minTo(func(q [2]float64) float64 {
				dx, dy := q[0]-p.X, q[1]-p.Y
				lx, ly := dx*cs-dy*sn, dx*sn+dy*cs
				return math.Hypot(math.Max(math.Abs(lx)-p.W/2, 0), math.Max(math.Abs(ly)-p.H/2, 0))
			}))
		}
		nw := math.Floor(math.Min(goal, 2*(free-clearanceMil))*100) / 100
		if nw > t.Width+minGain {
			ops = append(ops, widenOp{Track: t, NewWidth: nw})
			widened = append(widened, wide{a: a, b: b, layer: t.Layer, width: nw, net: t.Net})
		}
	}
	return ops
}

func segDist(p, a, b [2]float64) float64 {
	dx, dy := b[0]-a[0], b[1]-a[1]
	l2 := dx*dx + dy*dy
	t := 0.0
	if l2 > 0 {
		t = math.Max(0, math.Min(1, ((p[0]-a[0])*dx+(p[1]-a[1])*dy)/l2))
	}
	return math.Hypot(p[0]-a[0]-t*dx, p[1]-a[1]-t*dy)
}

// decodeAny re-decodes a []any snapshot list into typed rows.
func decodeAny(src []any, dst any) error {
	raw, err := json.Marshal(src)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, dst)
}

// widenNets plans and (unless dryRun) applies planWiden on the live board.
// The originals are deleted, then recreated wider (see replaceTracks).
func widenNets(cfg *appConfig, window string, nets map[string]bool, maxMil, clearanceMil float64, dryRun bool, stderr io.Writer) ([]widenOp, error) {
	return widenLive(cfg, window, func(tr []specctra.Track, v []widenVia, p []boardPad, clr float64) []widenOp {
		return planWiden(tr, v, p, nets, maxMil, clr)
	}, fmt.Sprintf("max %.1f mil", maxMil), clearanceMil, dryRun, stderr)
}

// widenToIntent grows every under-width track of the intent's nets towards
// its layer's intent width where clearance allows.
func widenToIntent(cfg *appConfig, window string, in *designIntent, stderr io.Writer) ([]widenOp, error) {
	reqs := intentRequirements(in)
	return widenLive(cfg, window, func(tr []specctra.Track, v []widenVia, p []boardPad, clr float64) []widenOp {
		return planWidenToIntent(tr, v, p, reqs, clr)
	}, "to the intent width", 0, false, stderr)
}

// widenLive reads the board (after save + reload), plans with plan using the
// board clearance plus specctra.ClearanceMarginMil (EasyEDA's DRC rounds a
// few hundredths tighter: a widened drain track measured 5.93 < 6.0 mil on
// Gas Module v11), and replaces the tracks.
func widenLive(cfg *appConfig, window string, plan func([]specctra.Track, []widenVia, []boardPad, float64) []widenOp, what string, clearanceMil float64, dryRun bool, stderr io.Writer) ([]widenOp, error) {
	if !dryRun {
		// The ids below are deleted: read them from a reloaded board.
		if err := saveAndReload(cfg, window); err != nil {
			return nil, err
		}
	}
	snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{withCopper: true, withLayers: true, withRules: clearanceMil <= 0})
	if err != nil {
		return nil, err
	}
	if snap.Copper == nil {
		return nil, fmt.Errorf("board copper unreadable")
	}
	if clearanceMil <= 0 {
		clearanceMil = 8
		if snap.Rules != nil && snap.Rules.ClearanceMil > 0 {
			clearanceMil = snap.Rules.ClearanceMil
		}
	}
	clearanceMil += specctra.ClearanceMarginMil
	var tracks []specctra.Track
	var vias []widenVia
	if err := decodeAny(snap.Copper.Lines, &tracks); err != nil {
		return nil, fmt.Errorf("decode tracks: %w", err)
	}
	if err := decodeAny(snap.Copper.Vias, &vias); err != nil {
		return nil, fmt.Errorf("decode vias: %w", err)
	}
	var pads []boardPad
	for _, c := range snap.Components {
		pads = append(pads, c.Pads...)
	}
	// Never widen copper on a layer the board does not have (importer
	// leftovers on 21/22, Gas Module v9): it is reconciled, not kept.
	valid := map[int]bool{}
	for _, l := range specctra.CopperLayerIDs(max(snap.CopperLayers, 2)) {
		valid[l] = true
	}
	kept := tracks[:0]
	for _, t := range tracks {
		if valid[t.Layer] {
			kept = append(kept, t)
		}
	}
	tracks = kept
	ops := plan(tracks, vias, pads, clearanceMil)
	fmt.Fprintf(stderr, "widen: %d track(s) can grow (%s, clearance %.2f mil)\n", len(ops), what, clearanceMil)
	if dryRun || len(ops) == 0 {
		return ops, nil
	}
	fixes := make([]specctra.TrackFix, len(ops))
	for i, op := range ops {
		t := op.Track
		fixes[i] = specctra.TrackFix{Delete: t, Create: []specctra.NewTrack{{Net: t.Net, Layer: t.Layer, X1: t.X1, Y1: t.Y1, X2: t.X2, Y2: t.Y2, Width: op.NewWidth}}}
	}
	if _, failures, err := replaceTracks(cfg, window, fixes); err != nil {
		return ops, err
	} else if len(failures) > 0 {
		return ops, fmt.Errorf("widen: %d track(s) could not be recreated: %v", len(failures), failures)
	}
	// DRC guard: the planner's pad model (axis-aligned W×H) is coarser than
	// EasyEDA's (rotated, polygon, rounded corners) — Gas Module v12 A: two
	// widened GND tracks measured 5.84 < 6.0 mil to an SMD pad. Any widened
	// track in a clearance violation steps back half way, then to its old
	// width, with native DRC after each step.
	for round := 0; round < 2; round++ {
		if err := saveAndReload(cfg, window); err != nil {
			return ops, err
		}
		// Pours must flow around the wider tracks before DRC judges them:
		// unrebuilt pours made 43 of 46 widened drain tracks look like
		// clearance violations and step back (Gas Module v13 A).
		if _, err := requestActionTimed(cfg, "pcb.pour.rebuild", window, map[string]any{}, 20*time.Minute); err != nil {
			return ops, fmt.Errorf("pour rebuild before the widen DRC check: %w", err)
		}
		res, err := requestActionTimed(cfg, "pcb.drc.check", window, nil, 20*time.Minute)
		if err != nil {
			return ops, drcTimeoutHint(err, stderr)
		}
		bad := map[string]bool{}
		for _, v := range flattenDrcResult(res.Result).Violations {
			if strings.Contains(v.Rule, "Clearance") {
				for _, id := range v.Objs {
					bad[id] = true
				}
			}
		}
		live, err := listPcbTracks(cfg, window)
		if err != nil {
			return ops, err
		}
		back := widenStepBack(ops, live, bad, round == 1)
		if len(back) == 0 {
			break
		}
		fmt.Fprintf(stderr, "widen: %d widened track(s) in a DRC clearance violation step back (%s)\n", len(back), map[bool]string{false: "half way", true: "to the old width"}[round == 1])
		if _, failures, err := replaceTracks(cfg, window, back); err != nil {
			return ops, err
		} else if len(failures) > 0 {
			return ops, fmt.Errorf("widen step-back: %d track(s) could not be recreated: %v", len(failures), failures)
		}
		for i := range ops {
			for _, f := range back {
				if f.Delete.Net == ops[i].Track.Net && f.Delete.Layer == ops[i].Track.Layer && sameEnds(f.Delete, ops[i].Track) {
					ops[i].SteppedBack = append(ops[i].SteppedBack, fmt.Sprintf("%.2f→%.2f mil (DRC clearance)", f.Delete.Width, f.Create[0].Width))
					ops[i].NewWidth = f.Create[0].Width
				}
			}
		}
	}
	return ops, nil
}

// widenStepBack finds the live copies of widened tracks that a DRC
// clearance violation names and plans them narrower: half way back to the
// old width, or (final) all the way back.
func widenStepBack(ops []widenOp, live []specctra.Track, bad map[string]bool, final bool) []specctra.TrackFix {
	var fixes []specctra.TrackFix
	for _, op := range ops {
		if op.NewWidth <= op.Track.Width {
			continue
		}
		for _, t := range live {
			if !bad[t.ID] || t.Net != op.Track.Net || t.Layer != op.Track.Layer || !sameEnds(t, op.Track) {
				continue
			}
			w := op.Track.Width
			if !final {
				w = math.Floor((op.Track.Width+t.Width)/2*100) / 100
			}
			fixes = append(fixes, specctra.TrackFix{Delete: t, Create: []specctra.NewTrack{{Net: t.Net, Layer: t.Layer, X1: t.X1, Y1: t.Y1, X2: t.X2, Y2: t.Y2, Width: w}}})
		}
	}
	return fixes
}

func sameEnds(a, b specctra.Track) bool {
	near := func(x1, y1, x2, y2 float64) bool { return math.Hypot(x1-x2, y1-y2) <= specctra.MatchTolMil }
	return (near(a.X1, a.Y1, b.X1, b.Y1) && near(a.X2, a.Y2, b.X2, b.Y2)) || (near(a.X1, a.Y1, b.X2, b.Y2) && near(a.X2, a.Y2, b.X1, b.Y1))
}

func newPcbWidenCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var netsCSV string
	var maxMil, clearanceMil float64
	var dryRun, noPost bool
	c := &cobra.Command{
		Use:   "widen",
		Short: "Widen the tracks of chosen nets up to --max-mil where clearance allows",
		Long: `Widen every unlocked track of --net up to --max-mil, limited by the free space to
other-net tracks, vias and pads (minus --clearance-mil, default the live
clearance rule). Only tracks that gain more than 2 mil change. Then pour
rebuild → save → reload → pour rebuild → native DRC (--no-post skips).`,
		Args:    cobra.NoArgs,
		Example: `  pcbpilot pcb widen --net SV1_DRV,SV2_DRV,SV3_DRV,PV1_DRV --max-mil 40 --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			nets := map[string]bool{}
			for _, n := range strings.Split(netsCSV, ",") {
				if n = strings.TrimSpace(n); n != "" {
					nets[n] = true
				}
			}
			if len(nets) == 0 {
				return fmt.Errorf("--net is required")
			}
			ops, err := widenNets(cfg, *window, nets, maxMil, clearanceMil, dryRun, stderr)
			out := map[string]any{"dryRun": dryRun, "widened": ops}
			if err != nil {
				_ = writeJSON(stdout, out)
				return err
			}
			if !dryRun && !noPost && len(ops) > 0 {
				post, perr := postImportChecks(cfg, *window, nil, "", stderr)
				out["post"] = post
				if perr != nil {
					_ = writeJSON(stdout, out)
					return perr
				}
			}
			return writeJSON(stdout, out)
		},
	}
	c.Flags().StringVar(&netsCSV, "net", "", "nets to widen, comma-separated (required)")
	c.Flags().Float64Var(&maxMil, "max-mil", 40, "maximum width (mil)")
	c.Flags().Float64Var(&clearanceMil, "clearance-mil", 0, "clearance kept to other-net copper (mil; 0 = live rule, else 8)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "plan only")
	c.Flags().BoolVar(&noPost, "no-post", false, "skip pour rebuild / save / reload / DRC")
	return c
}

// irWidenRounds bounds the IR closure of pcb auto route.
const irWidenRounds = 2

var irDropRe = regexp.MustCompile(`^(\S+) drops ([0-9.]+) mV .* over the ([0-9.]+) mV budget`)

// irOverBudget returns net → drop/budget for the post-layout sim's IR-drop
// failures when the other failing gates are at most intent-widths (nil
// otherwise: widening cannot fix a DRC or connectivity failure, and it helps
// a width shortfall too — Gas Module V5 B v20 skipped the closure because a
// +12V width failure sat next to the drop failures).
func irOverBudget(summary map[string]any) map[string]float64 {
	gates, _ := summary["gates"].([]gateResult)
	var out map[string]float64
	for _, g := range gates {
		// Silkscreen and the manual never touch copper: they must not hold
		// the closure back (v21 B: SV1 31.65 of 30 mV, silk failing too).
		if g.Pass || g.Gate == "intent-widths" || g.Gate == "silkscreen" || g.Gate == "board-manual" {
			continue
		}
		if g.Gate != "post-layout-sim" {
			return nil
		}
		for _, it := range g.Items {
			m := irDropRe.FindStringSubmatch(it)
			if m == nil {
				return nil
			}
			drop, _ := strconv.ParseFloat(m[2], 64)
			budget, _ := strconv.ParseFloat(m[3], 64)
			if budget <= 0 {
				return nil
			}
			if out == nil {
				out = map[string]float64{}
			}
			out[m[1]] = math.Max(out[m[1]], drop/budget)
		}
	}
	return out
}

// planWidenIR grows every track of an over-budget net by its drop/budget
// ratio plus 15 % (resistance ∝ 1/width), up to maxMil, as far as the
// clearance allows.
func planWidenIR(tracks []specctra.Track, vias []widenVia, pads []boardPad, ratios map[string]float64, maxMil, clearanceMil float64) []widenOp {
	return planWidenTo(tracks, vias, pads, func(t specctra.Track) float64 {
		r, ok := ratios[t.Net]
		if !ok {
			return 0
		}
		return math.Min(t.Width*r*1.15, maxMil)
	}, clearanceMil, 0.5)
}
