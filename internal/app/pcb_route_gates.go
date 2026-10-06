package app

// pcb_route_gates.go — the post-route gate of `pcb auto route`. Every check
// is computed by code from the live board and the intent; the run fails when
// any gate fails. The router (fastroute) never sees the simulation or the
// schematic, so this is where the routed copper is held to them.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// gateResult is one post-route gate.
type gateResult struct {
	Gate   string   `json:"gate"`
	Pass   bool     `json:"pass"`
	Detail string   `json:"detail"`
	Items  []string `json:"items,omitempty"`
	// Waived lists the failing items a signed waiver accepted.
	Waived []string `json:"waived,omitempty"`
}

// gateWaiver is a human decision to accept one known failure: every failing
// item of Gate that contains Match is accepted, with Reason and By recorded
// in the summary. Waivers never apply to a gate that failed without items.
type gateWaiver struct {
	Gate   string `json:"gate"`
	Match  string `json:"match"`
	Reason string `json:"reason"`
	By     string `json:"by"`
}

// applyWaivers turns a failing gate into a pass only when every one of its
// items is matched by a waiver for that gate.
func applyWaivers(g *gateResult, ws []gateWaiver) {
	if g.Pass || len(g.Items) == 0 {
		return
	}
	var waived []string
	for _, it := range g.Items {
		hit := ""
		for _, w := range ws {
			if w.Gate == g.Gate && w.Match != "" && strings.Contains(it, w.Match) {
				hit = fmt.Sprintf("%s — waived: %s (%s)", it, w.Reason, w.By)
				break
			}
		}
		if hit == "" {
			return
		}
		waived = append(waived, hit)
	}
	g.Pass, g.Waived = true, waived
}

// neckZoneMil: a track narrower than its net's required width is a pin
// neck-down only when one end lies within this distance of a same-net pad's
// copper (and it is not below widthMil.min).
const neckZoneMil = 50

// widthViolation is a routed track that does not meet its net's intent.
type widthViolation struct {
	Net       string  `json:"net"`
	Layer     int     `json:"layer"`
	ID        string  `json:"primitiveId"`
	WidthMil  float64 `json:"widthMil"`
	NeededMil float64 `json:"neededMil"`
	Reason    string  `json:"reason"`
}

// checkIntentWidths compares every routed track of a net with intent
// requirements: outer width on layers 1/2, inner width on inner layers,
// never below widthMil.min, and below the full width only inside a pin
// neck-down zone.
func checkIntentWidths(tracks []specctra.Track, pads []boardPad, reqs map[string]specctra.NetRequirement) []widthViolation {
	byNet := map[string][]boardPad{}
	for _, p := range pads {
		byNet[strings.ToUpper(p.Net)] = append(byNet[strings.ToUpper(p.Net)], p)
	}
	var out []widthViolation
	for _, t := range tracks {
		r, ok := reqs[t.Net]
		if !ok {
			continue
		}
		need := r.OuterMil
		if t.Layer >= 15 {
			need = r.InnerMil
		}
		if t.Width+specctraEps >= need {
			continue
		}
		v := widthViolation{Net: t.Net, Layer: t.Layer, ID: t.ID, WidthMil: t.Width, NeededMil: need}
		switch {
		case r.MinMil > 0 && t.Width+specctraEps < r.MinMil:
			v.Reason = fmt.Sprintf("below the net's minimum %.2f mil", r.MinMil)
		case !nearSameNetPad(t, byNet[strings.ToUpper(t.Net)]):
			v.Reason = "narrower than required away from any pin (not a neck-down)"
		default:
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Net != out[j].Net {
			return out[i].Net < out[j].Net
		}
		return out[i].ID < out[j].ID
	})
	return out
}

const specctraEps = 0.05

func nearSameNetPad(t specctra.Track, pads []boardPad) bool {
	for _, p := range pads {
		if p.Layer != t.Layer && p.Layer != pcbLayerMulti {
			continue
		}
		for _, e := range [][2]float64{{t.X1, t.Y1}, {t.X2, t.Y2}} {
			d := math.Hypot(math.Max(math.Abs(e[0]-p.X)-p.W/2, 0), math.Max(math.Abs(e[1]-p.Y)-p.H/2, 0))
			if d <= neckZoneMil {
				return true
			}
		}
	}
	return false
}

// summarizeWidthViolations groups violations per net for the summary.
func summarizeWidthViolations(vs []widthViolation) []string {
	type agg struct {
		n           int
		worst, need float64
		layer       int
		reason      string
	}
	m := map[string]*agg{}
	var nets []string
	for _, v := range vs {
		a := m[v.Net]
		if a == nil {
			a = &agg{worst: v.WidthMil, need: v.NeededMil, layer: v.Layer, reason: v.Reason}
			m[v.Net] = a
			nets = append(nets, v.Net)
		}
		a.n++
		if v.WidthMil < a.worst {
			a.worst, a.need, a.layer, a.reason = v.WidthMil, v.NeededMil, v.Layer, v.Reason
		}
	}
	sort.Strings(nets)
	out := make([]string, len(nets))
	for i, n := range nets {
		a := m[n]
		out[i] = fmt.Sprintf("%s: %d track(s), worst %.2f < %.2f mil on layer %d (%s)", n, a.n, a.worst, a.need, a.layer, a.reason)
	}
	return out
}

// postRouteGates runs every gate on the live board after the post-import
// checks (which already saved, reloaded, rebuilt pours and ran DRC).
func postRouteGates(cfg *appConfig, window, intentPath string, post *postImportSummary, simVerdict string, simReasons []string, unresolved *specctra.Reconcile, waivers []gateWaiver, stderr io.Writer) ([]gateResult, bool) {
	var gates []gateResult
	add := func(g gateResult) {
		applyWaivers(&g, waivers)
		gates = append(gates, g)
		mark := "PASS"
		if !g.Pass {
			mark = "FAIL"
		}
		fmt.Fprintf(stderr, "post-route gate %-18s %s  %s\n", g.Gate, mark, g.Detail)
	}

	// 0. The board carries every segment and via of the routed session.
	g0 := gateResult{Gate: "session-reconcile", Pass: unresolved == nil, Detail: "board matches the routed session"}
	if unresolved != nil {
		g0.Detail = fmt.Sprintf("%d segment(s) and %d via(s) of the session missing, %d track(s) on non-copper layers",
			len(unresolved.MissingTracks), len(unresolved.MissingVias), len(unresolved.Stray))
		for _, t := range unresolved.MissingTracks {
			g0.Items = append(g0.Items, fmt.Sprintf("missing %s L%d (%.1f,%.1f)-(%.1f,%.1f)", t.Net, t.Layer, t.X1, t.Y1, t.X2, t.Y2))
		}
		for _, v := range unresolved.MissingVias {
			g0.Items = append(g0.Items, fmt.Sprintf("missing via %s (%.1f,%.1f)", v.Net, v.X, v.Y))
		}
	}
	add(g0)

	// 1. Native DRC.
	g := gateResult{Gate: "native-drc", Pass: post != nil && post.DRCTotal == 0 && post.DRCPassed}
	if post != nil {
		g.Detail = fmt.Sprintf("%d violation(s)", post.DRCTotal)
		for k, v := range post.DRCCounts {
			g.Items = append(g.Items, fmt.Sprintf("%s: %d", k, v))
		}
		sort.Strings(g.Items)
	} else {
		g.Detail = "not run"
	}
	add(g)

	// 2. Pad-net diff against the schematic.
	g = gateResult{Gate: "pad-net-diff", Detail: "not run (needs --sch-connectivity)"}
	if post != nil {
		if d, ok := post.PadNetDiff.(map[string]any); ok {
			if okv, has := d["ok"].(bool); has {
				g.Pass = okv
				g.Detail = fmt.Sprintf("ok=%v", okv)
				if c, ok := d["counts"].(map[string]any); ok {
					for k, v := range c {
						if n, _ := v.(float64); n > 0 {
							g.Items = append(g.Items, fmt.Sprintf("%s: %v", k, v))
						}
					}
					sort.Strings(g.Items)
				}
			}
		}
	}
	add(g)

	in, err := loadDesignIntent(intentPath)
	if err != nil {
		add(gateResult{Gate: "intent", Detail: err.Error()})
		return gates, false
	}

	// 3. Native rules still match the intent.
	call := func(action string, payload any) (map[string]any, error) {
		res, err := requestAction(cfg, action, window, payload)
		if err != nil {
			return nil, err
		}
		if res.Result == nil {
			return map[string]any{}, nil
		}
		return res.Result, nil
	}
	rrep, rerr := runIntentRules(in, "check", false, call, io.Discard)
	g = gateResult{Gate: "intent-rules", Pass: rerr == nil && rrep.Verified, Detail: rrep.Status}
	if rerr != nil {
		g.Detail += ": " + rerr.Error()
	}
	add(g)

	// 4 + 5 read one fresh snapshot.
	snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{withRules: true, withLayers: true, withCopper: true, withFootprintHoles: true})
	if err != nil || snap.Copper == nil {
		add(gateResult{Gate: "intent-widths", Detail: fmt.Sprintf("board snapshot unreadable: %v", err)})
		return gates, false
	}
	var tracks []specctra.Track
	if err := decodeAny(snap.Copper.Lines, &tracks); err != nil {
		add(gateResult{Gate: "intent-widths", Detail: "decode tracks: " + err.Error()})
		return gates, false
	}
	var pads []boardPad
	for _, c := range snap.Components {
		pads = append(pads, c.Pads...)
	}
	vs := checkIntentWidths(tracks, pads, intentRequirements(in))
	add(gateResult{Gate: "intent-widths", Pass: len(vs) == 0,
		Detail: fmt.Sprintf("%d track(s) below the intent width outside pin neck-downs or below the minimum", len(vs)),
		Items:  summarizeWidthViolations(vs)})

	raw, err := json.Marshal(snap)
	if err != nil {
		add(gateResult{Gate: "pcb-check-intent", Detail: err.Error()})
		return gates, false
	}
	rep := &pcbCheckReport{}
	var errs []string
	edgeIntent, _ := loadEdgeIntent(intentPath)
	for _, f := range []func() error{
		func() error { return addEdgeFindings(rep, raw, edgeIntent, "") },
		func() error { return addIsolationFindings(rep, raw, intentPath) },
		func() error { return addViaCurrentFindings(rep, raw, intentPath) },
	} {
		if err := f(); err != nil {
			errs = append(errs, err.Error())
		}
	}
	var items []string
	for _, f := range rep.Findings {
		if f.Level == "ERROR" {
			items = append(items, f.Type+": "+f.Message)
		}
	}
	items = append(items, errs...)
	add(gateResult{Gate: "pcb-check-intent", Pass: len(items) == 0,
		Detail: fmt.Sprintf("%d error(s) (copper-to-edge, isolation, via current vs intent)", len(items)), Items: items})

	// 6. Post-layout simulation on the live copper.
	g = gateResult{Gate: "post-layout-sim", Pass: simVerdict == "pass" || simVerdict == "warn", Detail: "verdict " + simVerdict}
	if !g.Pass {
		g.Items = simReasons
	}
	if simVerdict == "" {
		g.Detail = "not run (needs --sim)"
	}
	add(g)

	pass := true
	for _, g := range gates {
		pass = pass && g.Pass
	}
	return gates, pass
}
