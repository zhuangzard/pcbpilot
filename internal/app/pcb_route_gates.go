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
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
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
// A track lying wholly inside its own net's poured copper on its layer is
// carried by the pour (the current flows in the plane, not the track):
// pourBacked reports those; it may be nil.
//
// segNeed, when set (--width-basis segment), may lower the requirement of a
// track to what its own simulated current needs (ok=false keeps the net's).
func checkIntentWidths(tracks []specctra.Track, pads []boardPad, reqs map[string]specctra.NetRequirement, pourBacked func(specctra.Track) bool, segNeed ...func(specctra.Track) (float64, bool)) []widthViolation {
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
		if len(segNeed) > 0 && segNeed[0] != nil {
			if own, ok := segNeed[0](t); ok && t.Width+specctraEps >= math.Max(own, r.MinMil) {
				continue
			}
		}
		v := widthViolation{Net: t.Net, Layer: t.Layer, ID: t.ID, WidthMil: t.Width, NeededMil: need}
		switch {
		case r.MinMil > 0 && t.Width+specctraEps < r.MinMil:
			v.Reason = fmt.Sprintf("below the net's minimum %.2f mil", r.MinMil)
		case pourBacked != nil && pourBacked(t):
			continue
		case !nearSameNetPad(t, byNet[strings.ToUpper(t.Net)]):
			v.Reason = "narrower than required away from any pin (not a neck-down) and not inside its net's pour"
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

// pouredLookup indexes materialized poured copper (pcb dump copper.poured:
// [{net, layer, fills:[{source}]}]) and answers whether a track's centre line
// lies wholly inside its net's pour on its layer (even-odd over the fill's
// contours, so pour cut-outs are outside).
func pouredLookup(poured []any) func(specctra.Track) bool {
	type key struct {
		net   string
		layer int
	}
	fills := map[key][][][]pcbauto.Point{}
	for _, it := range poured {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		layer, _ := strconv.Atoi(fmt.Sprint(m["layer"]))
		k := key{strings.ToUpper(fmt.Sprint(m["net"])), layer}
		fl, _ := m["fills"].([]any)
		for _, f := range fl {
			fm, ok := f.(map[string]any)
			if !ok {
				continue
			}
			if cs := pcbauto.SourceContours(fm["source"]); len(cs) > 0 {
				fills[k] = append(fills[k], cs)
			}
		}
	}
	inside := func(cs [][]pcbauto.Point, x, y float64) bool {
		in := false
		for _, c := range cs {
			for i, j := 0, len(c)-1; i < len(c); j, i = i, i+1 {
				if (c[i].Y > y) != (c[j].Y > y) && x < (c[j].X-c[i].X)*(y-c[i].Y)/(c[j].Y-c[i].Y)+c[i].X {
					in = !in
				}
			}
		}
		return in
	}
	return func(t specctra.Track) bool {
		fs := fills[key{strings.ToUpper(t.Net), t.Layer}]
		if len(fs) == 0 {
			return false
		}
		n := max(2, int(math.Hypot(t.X2-t.X1, t.Y2-t.Y1)/10))
		for k := 0; k <= n; k++ {
			f := float64(k) / float64(n)
			x, y := t.X1+(t.X2-t.X1)*f, t.Y1+(t.Y2-t.Y1)*f
			hit := false
			for _, cs := range fs {
				if inside(cs, x, y) {
					hit = true
					break
				}
			}
			if !hit {
				return false
			}
		}
		return true
	}
}

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
func postRouteGates(cfg *appConfig, window, intentPath string, post *postImportSummary, simVerdict string, simReasons []string, sessionChecked bool, unresolved *specctra.Reconcile, waivers []gateWaiver, segNeed func(specctra.Track) (float64, bool), stderr io.Writer) ([]gateResult, bool) {
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

	// 0. The board carries every segment and via of the routed session
	// (only when there is one; continuity is also judged by the post-layout
	// sim's open-path check).
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
	if sessionChecked {
		add(g0)
	}

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
	vs := checkIntentWidths(tracks, pads, intentRequirements(in), pouredLookup(snap.Copper.Poured), segNeed)
	basis := "net current"
	if segNeed != nil {
		basis = "each segment's simulated current below half the net current, else the net current"
	}
	add(gateResult{Gate: "intent-widths", Pass: len(vs) == 0,
		Detail: fmt.Sprintf("%d track(s) below the intent width (basis: %s) outside pin neck-downs and own-net pours, or below the minimum", len(vs), basis),
		Items:  summarizeWidthViolations(vs)})

	if items, n := checkIntentLengths(in, tracks); n > 0 {
		add(gateResult{Gate: "intent-lengths", Pass: len(items) == 0,
			Detail: fmt.Sprintf("%d length group(s)/pair(s): routed length spread within tolerance", n), Items: items})
	}

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

type qualityGateOpts struct {
	widthBasis                          string
	intent, sim, script, outDir, source string
	sch                                 []string
	waivers                             []gateWaiver
	sessionChecked                      bool
	unresolved                          *specctra.Reconcile
}

// runQualityGates: pour rebuild → save → reload → pour rebuild → native DRC →
// pad-net diff → live dump → sim post-layout → post-route gates. Results go
// into summary (post, postSim, gates, pass). err is an execution failure;
// pass=false a failed gate.
func runQualityGates(cfg *appConfig, window string, o qualityGateOpts, summary map[string]any, stderr io.Writer) (bool, error) {
	post, err := postImportChecks(cfg, window, o.sch, o.script, stderr)
	summary["post"] = post
	if err != nil {
		return false, err
	}
	snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{withRules: true, withLayers: true, withCopper: true, withFootprintHoles: true})
	if err != nil {
		return false, fmt.Errorf("board dump: %w", err)
	}
	boardPath := filepath.Join(o.outDir, "board-final.json")
	blob, _ := json.MarshalIndent(snap, "", "  ")
	if err := os.WriteFile(boardPath, append(blob, '\n'), 0o644); err != nil {
		return false, err
	}
	po := postSimOpts{board: boardPath, sim: o.sim, intent: o.intent,
		out: filepath.Join(o.outDir, "post.json"), report: filepath.Join(o.outDir, "post.md"), svgDir: filepath.Join(o.outDir, "heatmaps"),
		cell: 0.5, ambient: 25, hTop: 10, hBottom: 10, kxy: 0.3, kz: 0.3, plating: 0.7, viaDT: 10, margin: 1.2,
		source: o.source + " (pcb dump --include-copper)"}
	res, err := runPostSim(po, stderr)
	if err != nil {
		return false, fmt.Errorf("sim post-layout: %w", err)
	}
	ps := map[string]any{"verdict": res.Verdict.Status, "reasons": res.Verdict.Reasons, "out": po.out, "report": po.report}
	if res.Thermal != nil {
		ps["maxBoardC"] = res.Thermal.MaxBoardC
	}
	summary["postSim"] = ps
	var segNeed func(specctra.Track) (float64, bool)
	if o.widthBasis == "segment" {
		in, err := loadDesignIntent(o.intent)
		if err != nil {
			return false, err
		}
		segNeed = segmentWidthNeed(in, res)
	}
	gates, pass := postRouteGates(cfg, window, o.intent, post, res.Verdict.Status, res.Verdict.Reasons, o.sessionChecked, o.unresolved, o.waivers, segNeed, stderr)
	summary["gates"], summary["pass"] = gates, pass
	return pass, nil
}

func failedGates(summary map[string]any) string {
	gates, _ := summary["gates"].([]gateResult)
	var failed []string
	for _, g := range gates {
		if !g.Pass {
			failed = append(failed, g.Gate)
		}
	}
	return strings.Join(failed, ", ")
}

// loadWaivers reads and validates a signed waiver file ("" = none).
func loadWaivers(path string) ([]gateWaiver, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ws []gateWaiver
	if err := json.Unmarshal(raw, &ws); err != nil {
		return nil, fmt.Errorf("parse --waivers %s: %w", path, err)
	}
	for i, w := range ws {
		if w.Gate == "" || w.Match == "" || w.Reason == "" || w.By == "" {
			return nil, fmt.Errorf("waiver %d: gate, match, reason and by are all required", i)
		}
	}
	return ws, nil
}

func newPcbGateCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var intentPath, simPath, scriptPath, outDir, waiverPath, widthBasis string
	var schFiles []string
	c := &cobra.Command{
		Use:   "gate",
		Short: "Hard quality gate for a finished board: every schematic/simulation constraint of the intent, from code",
		Long: `Check a routed board on the live editor against the intent, however it was
routed (pcb auto route, autoroute, by hand). Pour rebuild → save → reload →
pour rebuild → native DRC → pad-net diff → live dump → sim post-layout, then:

  native-drc         0 violations
  pad-net-diff       0 differences against the schematic connectivity
  intent-rules       the board's native rules match the intent
  intent-widths      every track per net and layer >= widthMil outer/inner;
                     narrower only as a pin neck-down (within 50 mil of a
                     same-net pad) or inside its own net's pour, never below
                     widthMil.min
  pcb-check-intent   no ERROR: copper-to-edge, isolation, via current
  post-layout-sim    verdict not fail (IR drop, opens, via current, heat)

Any failing gate exits non-zero. --waivers takes signed {gate,match,reason,by}
entries; a gate passes only when every failing item is covered.
Results: --out-dir/{gate.json, board-final.json, post.json, post.md}.`,
		Args:    cobra.NoArgs,
		Example: `  pcbpilot pcb gate --intent intent.json --sim sim.json --sch-connectivity p1.json --sch-connectivity p2.json --out-dir gate/ --project P --doc PCB1_1`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if intentPath == "" || simPath == "" || len(schFiles) == 0 {
				return fmt.Errorf("--intent, --sim and --sch-connectivity are required")
			}
			if widthBasis != "net" && widthBasis != "segment" {
				return fmt.Errorf("--width-basis must be net or segment")
			}
			waivers, err := loadWaivers(waiverPath)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}
			summary := map[string]any{"intent": intentPath, "sim": simPath}
			pass, err := runQualityGates(cfg, *window, qualityGateOpts{intent: intentPath, sim: simPath, sch: schFiles, script: scriptPath,
				outDir: outDir, waivers: waivers, widthBasis: widthBasis, source: "live board (pcb gate)"}, summary, stderr)
			if f, ferr := os.Create(filepath.Join(outDir, "gate.json")); ferr == nil {
				_ = writeJSON(f, summary)
				f.Close()
			}
			_ = writeJSON(stdout, summary)
			if err != nil {
				return err
			}
			if !pass {
				return fmt.Errorf("gate failed: %s", failedGates(summary))
			}
			return nil
		},
	}
	c.Flags().StringVar(&intentPath, "intent", "", "intent.json (intent derive: schematic + simulation) — required")
	c.Flags().StringVar(&simPath, "sim", "", "sim.json (sim power) — required")
	c.Flags().StringArrayVar(&schFiles, "sch-connectivity", nil, "schematic connectivity JSON (repeat per page) — required")
	c.Flags().StringVar(&scriptPath, "pad-net-diff-script", "", "path to pad-net-diff.py (auto-detected if omitted)")
	c.Flags().StringVar(&outDir, "out-dir", "pcb-gate", "directory for gate.json, board-final.json and post-layout results")
	c.Flags().StringVar(&waiverPath, "waivers", "", "JSON list of signed waivers [{gate,match,reason,by}]")
	c.Flags().StringVar(&widthBasis, "width-basis", "segment", widthBasisHelp)
	return c
}

// checkIntentLengths compares routed track length per net within each intent
// length group (lengthTolMil) and each differential pair (maxSkewMil).
// Returns the failing groups and how many groups were checked.
func checkIntentLengths(in *designIntent, tracks []specctra.Track) ([]string, int) {
	length := map[string]float64{}
	for _, t := range tracks {
		length[t.Net] += math.Hypot(t.X2-t.X1, t.Y2-t.Y1)
	}
	type group struct {
		nets []string
		tol  float64
	}
	groups := map[string]*group{}
	for _, name := range in.sortedNetNames() {
		n := in.Nets[name]
		if n.LengthGroup != "" && n.LengthTolMil > 0 {
			g := groups["group "+n.LengthGroup]
			if g == nil {
				g = &group{tol: n.LengthTolMil}
				groups["group "+n.LengthGroup] = g
			}
			g.nets = append(g.nets, name)
			g.tol = math.Min(g.tol, n.LengthTolMil)
		}
		if n.DiffPair != "" && n.MaxSkewMil > 0 && name < n.DiffPair && in.Nets[n.DiffPair] != nil {
			groups["pair "+name+"/"+n.DiffPair] = &group{nets: []string{name, n.DiffPair}, tol: n.MaxSkewMil}
		}
	}
	var keys []string
	for k, g := range groups {
		if len(g.nets) >= 2 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var fails []string
	for _, k := range keys {
		g := groups[k]
		lo, hi := math.Inf(1), 0.0
		for _, n := range g.nets {
			lo, hi = math.Min(lo, length[n]), math.Max(hi, length[n])
		}
		if hi-lo > g.tol+specctraEps {
			fails = append(fails, fmt.Sprintf("%s: spread %.1f mil > %.1f mil (%.1f–%.1f)", k, hi-lo, g.tol, lo, hi))
		}
	}
	return fails, len(keys)
}

// segmentWidthNeed returns, per routed track, the IPC-2221 width its own
// worst simulated current needs (× 1.2 margin, intent copper weights and
// allowed rise). Only tracks carrying less than half their net's current
// get a per-segment requirement; trunks keep the net's (ok=false).
func segmentWidthNeed(in *designIntent, res *postsim.Result) func(specctra.Track) (float64, bool) {
	outerOz, innerOz, rise := 1.0, 0.5, 10.0
	if c := in.Copper; c != nil {
		if c.OuterOz > 0 {
			outerOz = c.OuterOz
		}
		if c.InnerOz > 0 {
			innerOz = c.InnerOz
		}
		if c.TempRiseC > 0 {
			rise = c.TempRiseC
		}
	}
	layerID := map[string]int{}
	if res.Stackup != nil {
		for _, l := range res.Stackup.Layers {
			layerID[l.Name] = l.ID
		}
	}
	type seg struct {
		layer int
		a, b  [2]float64
		amps  float64
	}
	byNet := map[string][]seg{}
	netAmps := map[string]float64{}
	for _, n := range res.Nets {
		netAmps[strings.ToUpper(n.Net)] = n.CurrentA
		for _, sg := range n.AllSegments {
			byNet[strings.ToUpper(n.Net)] = append(byNet[strings.ToUpper(n.Net)], seg{layerID[sg.Layer], [2]float64{sg.A.X, sg.A.Y}, [2]float64{sg.B.X, sg.B.Y}, sg.CurrentA})
		}
	}
	return func(t specctra.Track) (float64, bool) {
		key := strings.ToUpper(t.Net)
		if in.Nets[t.Net] != nil && in.Nets[t.Net].CurrentA > netAmps[key] {
			netAmps[key] = in.Nets[t.Net].CurrentA
		}
		a, b := [2]float64{t.X1, t.Y1}, [2]float64{t.X2, t.Y2}
		amps, found := 0.0, false
		for _, sg := range byNet[key] {
			if sg.layer != t.Layer {
				continue
			}
			if segDist(sg.a, a, b) <= specctra.MatchTolMil && segDist(sg.b, a, b) <= specctra.MatchTolMil {
				amps, found = math.Max(amps, sg.amps), true
			}
		}
		if !found || amps >= 0.5*netAmps[key] {
			return 0, false
		}
		oz := outerOz
		if t.Layer >= 15 {
			oz = innerOz
		}
		return pcbauto.TraceWidthForCurrent(amps*1.2, rise, oz, t.Layer >= 15), true
	}
}

// widthBasisHelp: segment is the default by the user's decision of
// 2026-10-06 ("按每段实际电流算").
const widthBasisHelp = "intent-widths requirement: segment (default; a track carrying < 50 % of the net current per post-layout sim needs the IPC width of its own current × 1.2, never below widthMil.min; trunks keep the net's) | net (every track carries the net's intent current)"
