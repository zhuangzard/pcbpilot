package app

// cmd_signoff.go — `pcbpilot signoff`: the one release sign-off, identical
// for EasyEDA (pcb auto route / pcb gate outputs) and KiCad (kicad route
// outputs). It reads only files — the board snapshot (board-final.json,
// the pcb dump shape both backends write), the intent, the sim, the
// schematic connectivity, the design-review record, the board manual and
// the design report — and fails non-zero unless every core selling point
// holds (user decision 2026-10-09):
//
//	signoff-review      Codex + Kimi + Claude design-stage review passed
//	signoff-parts       every BOM line has a valid LCSC part number
//	signoff-safety      insulation (clearance, creepage, slots) and
//	                    copper-to-edge per pkg/safety via the intent
//	signoff-copper      per-segment trace widths, no neck-down below
//	                    widthMil.min, via groups vs current (segment basis)
//	signoff-ir          post-layout simulation verdict (IR drop, opens,
//	                    via current, heat)
//	signoff-continuity  schematic pin→net partition = board pad nets
//	signoff-trace       every intent rule traceable to its schematic net
//	                    and parts and to the board (table in signoff.md)
//	signoff-artifacts   post.json, board manual (with its simulation
//	                    section) and design report present

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

type signoffOpts struct {
	board, intent, sim, review, manual, report, outDir, waivers string
	connectivity                                                []string
}

func newSignoffCmd(stdout, stderr io.Writer) *cobra.Command {
	var o signoffOpts
	var runDir string
	c := &cobra.Command{
		Use:   "signoff",
		Short: "Release sign-off (EasyEDA and KiCad alike): review, LCSC parts, safety, per-segment copper, IR, schematic↔PCB continuity and traceability, manual + report — non-zero unless all pass",
		Long: `One hard sign-off for a finished board, computed offline from files, the
same for EasyEDA (pcb auto route / pcb gate --out-dir) and KiCad (kicad route
--out-dir) runs:

  signoff-review      review-panel design stage passed (review.json)
  signoff-parts       every BOM component carries a valid LCSC number (C…);
                      mounting holes / fiducials / test points / logos skipped
  signoff-safety      intent isolation (clearance, creepage, slots — IEC 60664-1 /
                      62368-1 / 60601-1 MOOP/MOPP / 61010-1 via pkg/safety) and
                      copper-to-edge on the board
  signoff-copper      per-segment trace width (IPC-2221/2152 from each segment's
                      simulated current), never below widthMil.min, via groups vs
                      current
  signoff-ir          post-layout simulation verdict pass|warn
  signoff-continuity  schematic connectivity pin→net partition == board pad nets
  signoff-trace       every intent net with a rule is a schematic net with parts and
                      a board net (traceability table in signoff.md)
  signoff-artifacts   post.json, the board manual HTML with its simulation section,
                      and the design report (report.json) exist

--run-dir fills the defaults: <run-dir>/board-final.json, review-design/review.json,
manual/*.html (newest), report/v*/report.json (newest), sch-connectivity.json.
Results: --out-dir/{signoff.json, signoff.md, post.json}. Signed --waivers apply.`,
		Args:    cobra.NoArgs,
		Example: `  pcbpilot signoff --run-dir route/ --intent intent.json --sim sim.json --connectivity sch.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if runDir != "" {
				o.fillFromRunDir(runDir)
			}
			waivers, err := loadWaivers(o.waivers)
			if err != nil {
				return err
			}
			if o.outDir == "" {
				o.outDir = "signoff"
			}
			res, err := runSignoff(o, waivers, stderr)
			if err != nil {
				return err
			}
			_ = writeJSON(stdout, res)
			if !res.Pass {
				return fmt.Errorf("signoff failed: %s", failedGates(map[string]any{"gates": res.Gates}))
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&runDir, "run-dir", "", "a pcb auto route / pcb gate / kicad route --out-dir: default for every input below")
	f.StringVar(&o.board, "board", "", "board snapshot (board-final.json, pcb dump --include-copper shape)")
	f.StringVar(&o.intent, "intent", "", "intent.json (required)")
	f.StringVar(&o.sim, "sim", "", "sim.json (required: post-layout simulation)")
	f.StringArrayVar(&o.connectivity, "connectivity", nil, "schematic connectivity JSON (sch connectivity / kicad netlist; repeat per page) — required")
	f.StringVar(&o.review, "review", "", "design-stage review.json (review-panel)")
	f.StringVar(&o.manual, "manual", "", "board manual HTML")
	f.StringVar(&o.report, "report", "", "design report report.json")
	f.StringVar(&o.outDir, "out-dir", "", "output directory (default signoff/)")
	f.StringVar(&o.waivers, "waivers", "", "signed waivers JSON")
	return c
}

func (o *signoffOpts) fillFromRunDir(dir string) {
	def := func(p *string, v string) {
		if *p == "" && v != "" && fileExists(v) {
			*p = v
		}
	}
	def(&o.board, filepath.Join(dir, "board-final.json"))
	def(&o.review, filepath.Join(dir, "review-design", "review.json"))
	def(&o.manual, newestGlob(filepath.Join(dir, "manual", "*.html")))
	def(&o.report, newestGlob(filepath.Join(dir, "report", "v*", "report.json")))
	if len(o.connectivity) == 0 && fileExists(filepath.Join(dir, "sch-connectivity.json")) {
		o.connectivity = []string{filepath.Join(dir, "sch-connectivity.json")}
	}
	if o.outDir == "" {
		o.outDir = filepath.Join(dir, "signoff")
	}
}

// newestGlob is the most recently modified match ("" when none).
func newestGlob(pattern string) string {
	ms, _ := filepath.Glob(pattern)
	best, bt := "", int64(0)
	for _, m := range ms {
		if st, err := os.Stat(m); err == nil && st.ModTime().UnixNano() >= bt {
			best, bt = m, st.ModTime().UnixNano()
		}
	}
	return best
}

type signoffResult struct {
	Pass         bool         `json:"pass"`
	Gates        []gateResult `json:"gates"`
	Traceability []traceRow   `json:"traceability"`
	Inputs       signoffOpts  `json:"-"`
}

type traceRow struct {
	Net           string   `json:"net"`
	Role          string   `json:"role"`
	CurrentA      float64  `json:"currentA"`
	OuterMil      float64  `json:"outerMil"`
	InnerMil      float64  `json:"innerMil"`
	MinMil        float64  `json:"minMil"`
	ClearanceMil  float64  `json:"clearanceMil"`
	ViasPerTrans  int      `json:"viasPerTransition,omitempty"`
	Domain        string   `json:"domain,omitempty"`
	SchematicPins int      `json:"schematicPins"`
	Parts         []string `json:"parts"`
	BoardPads     int      `json:"boardPads"`
	Status        string   `json:"status"`
}

var (
	reLCSC       = regexp.MustCompile(`^C[0-9]+$`)
	reNonBOMRefs = regexp.MustCompile(`^(H|MH|FID|FD|MARK|TP|LOGO|NT|REF\*\*)[0-9]*$`)
)

// signoffDoc is the subset of the connectivity IR the sign-off reads.
type signoffDoc struct {
	Components []struct {
		ID     string `json:"id"`
		Ref    string `json:"ref"`
		Device struct {
			Name       string `json:"name"`
			SupplierID string `json:"supplierId"`
		} `json:"device"`
	} `json:"components"`
	Nets []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"nets"`
	Connections []struct {
		ComponentID string `json:"componentId"`
		PinNumber   string `json:"pinNumber"`
		NetID       string `json:"netId"`
	} `json:"connections"`
}

func runSignoff(o signoffOpts, waivers []gateWaiver, stderr io.Writer) (*signoffResult, error) {
	if err := os.MkdirAll(o.outDir, 0o755); err != nil {
		return nil, err
	}
	res := &signoffResult{Inputs: o}
	add := func(g gateResult) {
		applyWaivers(&g, waivers)
		res.Gates = append(res.Gates, g)
		fmt.Fprintf(stderr, "signoff %-19s %v  %s\n", g.Gate, map[bool]string{true: "PASS", false: "FAIL"}[g.Pass], g.Detail)
	}
	missing := func(gate, what string) { add(gateResult{Gate: gate, Detail: "input missing: " + what}) }

	// 1. Design review.
	if o.review == "" {
		missing("signoff-review", "review.json (review-panel --stage design)")
	} else {
		var rec reviewRecord
		b, err := os.ReadFile(o.review)
		if err != nil || json.Unmarshal(b, &rec) != nil {
			add(gateResult{Gate: "signoff-review", Detail: fmt.Sprintf("review.json unreadable: %v", err)})
		} else {
			g := gateResult{Gate: "signoff-review", Pass: rec.Stage == "design" && rec.Gate.Pass,
				Detail: fmt.Sprintf("stage %s, %s", rec.Stage, rec.Gate.Detail), Items: rec.Gate.Items, Waived: rec.Gate.Waived}
			if rec.Stage != "design" {
				g.Items = append(g.Items, "the record is stage "+rec.Stage+", not design")
			}
			add(g)
		}
	}

	in, err := loadDesignIntent(o.intent)
	if err != nil {
		return nil, fmt.Errorf("--intent: %w", err)
	}
	if o.board == "" {
		return nil, fmt.Errorf("--board (board-final.json) is required")
	}
	raw, err := os.ReadFile(o.board)
	if err != nil {
		return nil, err
	}
	snap, err := loadBoardSnapshotFile(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	var docs []signoffDoc
	for _, p := range o.connectivity {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		if _, err := powersim.ParseConnectivity(b); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		var d signoffDoc
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		docs = append(docs, d)
	}

	// 2. Parts (LCSC) — schematic first, the board's own field as fallback.
	lcsc := map[string]string{}
	for _, c := range snap.Components {
		lcsc[c.Designator] = ""
	}
	var boardLCSC struct {
		Components []struct {
			Designator string `json:"designator"`
			LCSC       string `json:"lcsc"`
		} `json:"components"`
	}
	_ = json.Unmarshal(raw, &boardLCSC)
	for _, c := range boardLCSC.Components {
		if c.LCSC != "" {
			lcsc[c.Designator] = c.LCSC
		}
	}
	for _, d := range docs {
		for _, c := range d.Components {
			if c.Device.SupplierID != "" {
				lcsc[c.Ref] = c.Device.SupplierID
			} else if _, ok := lcsc[c.Ref]; !ok {
				lcsc[c.Ref] = ""
			}
		}
	}
	var refs []string
	for r := range lcsc {
		refs = append(refs, r)
	}
	sort.Strings(refs)
	pg := gateResult{Gate: "signoff-parts"}
	nBOM := 0
	for _, r := range refs {
		if reNonBOMRefs.MatchString(r) || strings.HasPrefix(r, "#") {
			continue
		}
		nBOM++
		switch v := strings.TrimSpace(lcsc[r]); {
		case v == "":
			pg.Items = append(pg.Items, r+": no LCSC part number")
		case !reLCSC.MatchString(v):
			pg.Items = append(pg.Items, fmt.Sprintf("%s: LCSC %q is not a C-number", r, v))
		}
	}
	pg.Pass = len(pg.Items) == 0 && nBOM > 0
	pg.Detail = fmt.Sprintf("%d BOM component(s), %d without a valid LCSC number", nBOM, len(pg.Items))
	add(pg)

	// 3–4. Safety, copper, IR: the shared snapshot gates on the sim run.
	qo := qualityGateOpts{intent: o.intent, sim: o.sim, outDir: o.outDir, widthBasis: "segment", source: "signoff (" + o.board + ")"}
	verdict, reasons := "", []string(nil)
	var gs *gateSim
	if o.sim == "" {
		missing("signoff-ir", "--sim (post-layout simulation)")
	} else if ps, err := postSimForGates(o.board, qo, map[string]any{}, stderr); err != nil {
		verdict, reasons = "fail", []string{err.Error()}
	} else {
		gs, verdict, reasons = ps, ps.res.Verdict.Status, ps.res.Verdict.Reasons
	}
	var sg []gateResult
	var ok bool
	if gs != nil {
		ok = snapshotIntentGates(snap, in, o.intent, verdict, reasons, gs.segNeed, gs.viaOK, true, func(g gateResult) { sg = append(sg, g) })
	} else {
		ok = snapshotIntentGates(snap, in, o.intent, verdict, reasons, nil, nil, true, func(g gateResult) { sg = append(sg, g) })
	}
	byName := map[string]gateResult{}
	for _, g := range sg {
		byName[g.Gate] = g
	}
	combine := func(name string, parts ...string) {
		g := gateResult{Gate: name, Pass: ok}
		var details []string
		for _, p := range parts {
			x, has := byName[p]
			if !has {
				continue
			}
			g.Pass = g.Pass && x.Pass
			details = append(details, p+": "+x.Detail)
			for _, it := range x.Items {
				g.Items = append(g.Items, p+": "+it)
			}
			g.Info = append(g.Info, x.Info...)
		}
		g.Detail = strings.Join(details, " | ")
		add(g)
	}
	combine("signoff-safety", "isolation", "copper-to-edge")
	if gs != nil {
		combine("signoff-copper", "intent-widths", "intent-lengths", "via-current")
		combine("signoff-ir", "post-layout-sim")
	} else {
		combine("signoff-copper", "intent-widths", "intent-lengths", "via-current")
		res.Gates[len(res.Gates)-1].Pass = false
		res.Gates[len(res.Gates)-1].Items = append(res.Gates[len(res.Gates)-1].Items, "per-segment basis needs --sim")
	}

	// 5. Continuity and traceability.
	schPins := map[string]string{}
	netParts := map[string]map[string]bool{}
	for _, d := range docs {
		ref := map[string]string{}
		for _, c := range d.Components {
			ref[c.ID] = c.Ref
		}
		name := map[string]string{}
		for _, n := range d.Nets {
			name[n.ID] = n.Name
		}
		for _, c := range d.Connections {
			n := name[c.NetID]
			if n == "" {
				n = c.NetID
			}
			schPins[ref[c.ComponentID]+"."+c.PinNumber] = n
			if netParts[n] == nil {
				netParts[n] = map[string]bool{}
			}
			netParts[n][ref[c.ComponentID]] = true
		}
	}
	if len(docs) == 0 {
		missing("signoff-continuity", "--connectivity (sch connectivity / kicad netlist)")
	} else {
		// Unconnected single pins are not nets on the board side either.
		board := boardPinNets(snap)
		for k, v := range board {
			if strings.HasPrefix(v, "unconnected-(") {
				delete(board, k)
			}
		}
		g := padNetDiffGate(schPins, board, strings.Join(o.connectivity, ","), filepath.Join(o.outDir, "net-diff.json"))
		g.Gate = "signoff-continuity"
		add(g)
	}
	boardPads := map[string]int{}
	for _, n := range boardPinNets(snap) {
		boardPads[n]++
	}
	trace := gateResult{Gate: "signoff-trace"}
	for _, name := range in.sortedNetNames() {
		n := in.Nets[name]
		row := traceRow{Net: name, Role: n.Role, CurrentA: n.CurrentA, OuterMil: n.WidthMil.Outer, InnerMil: n.WidthMil.Inner,
			MinMil: n.WidthMil.Min, ClearanceMil: n.ClearanceMil, ViasPerTrans: n.ViasPerTransition, Domain: n.Domain,
			BoardPads: boardPads[name], Status: "ok"}
		for p := range netParts[name] {
			row.Parts = append(row.Parts, p)
		}
		sort.Strings(row.Parts)
		for _, v := range schPins {
			if v == name {
				row.SchematicPins++
			}
		}
		switch {
		case len(docs) > 0 && row.SchematicPins == 0:
			row.Status = "not in the schematic"
		case row.BoardPads == 0:
			row.Status = "not on the board"
		}
		if row.Status != "ok" && n.WidthMil.Outer > 0 {
			trace.Items = append(trace.Items, name+": "+row.Status)
		}
		res.Traceability = append(res.Traceability, row)
	}
	trace.Pass = len(trace.Items) == 0 && len(docs) > 0
	trace.Detail = fmt.Sprintf("%d intent net(s) traced schematic → rule → board (table in %s)", len(res.Traceability), filepath.Join(o.outDir, "signoff.md"))
	if len(docs) == 0 {
		trace.Items = append(trace.Items, "no schematic connectivity: rules cannot be traced to schematic nets")
	}
	add(trace)

	// 6. Artifacts.
	ag := gateResult{Gate: "signoff-artifacts"}
	if !fileExists(filepath.Join(o.outDir, "post.json")) {
		ag.Items = append(ag.Items, "post.json (post-layout simulation) missing")
	}
	if o.manual == "" || !fileExists(o.manual) {
		ag.Items = append(ag.Items, "board manual HTML missing")
	} else if b, err := os.ReadFile(o.manual); err == nil {
		h := string(b)
		// pkg/boardmanual: the simulation chapter is id="s3"; without post.json
		// it carries the simNoPost note instead of the post-layout results.
		if !strings.Contains(h, `id="s3"`) || strings.Contains(h, "未提供设计后仿真") || strings.Contains(h, "No post-layout simulation (post.json)") {
			ag.Items = append(ag.Items, "board manual has no post-layout simulation section ("+o.manual+")")
		}
	}
	if o.report == "" || !fileExists(o.report) {
		ag.Items = append(ag.Items, "design report (report.json) missing")
	}
	ag.Pass = len(ag.Items) == 0
	ag.Detail = fmt.Sprintf("post.json, manual %s, report %s", dashIfEmpty(o.manual), dashIfEmpty(o.report))
	add(ag)

	res.Pass = true
	for _, g := range res.Gates {
		res.Pass = res.Pass && g.Pass
	}
	_ = writeJSONFile(filepath.Join(o.outDir, "signoff.json"), res)
	_ = os.WriteFile(filepath.Join(o.outDir, "signoff.md"), []byte(signoffMarkdown(res)), 0o644)
	return res, nil
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func signoffMarkdown(r *signoffResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Sign-off — %s\n\n| gate | result | detail |\n|---|---|---|\n", map[bool]string{true: "PASS", false: "FAIL"}[r.Pass])
	for _, g := range r.Gates {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", g.Gate, map[bool]string{true: "PASS", false: "FAIL"}[g.Pass], strings.ReplaceAll(g.Detail, "|", "/"))
	}
	b.WriteString("\n## Traceability: intent rule ↔ schematic net / parts ↔ board\n\n| net | role | current A | outer / inner / min mil | clearance mil | vias/transition | schematic pins | parts | board pads | status |\n|---|---|---|---|---|---|---|---|---|---|\n")
	for _, t := range r.Traceability {
		parts := strings.Join(t.Parts, " ")
		if len(t.Parts) > 8 {
			parts = strings.Join(t.Parts[:8], " ") + fmt.Sprintf(" … (+%d)", len(t.Parts)-8)
		}
		fmt.Fprintf(&b, "| %s | %s | %.3f | %.2f / %.2f / %.2f | %.2f | %d | %d | %s | %d | %s |\n", t.Net, t.Role, t.CurrentA, t.OuterMil, t.InnerMil, t.MinMil, t.ClearanceMil, t.ViasPerTrans, t.SchematicPins, parts, t.BoardPads, t.Status)
	}
	for _, g := range r.Gates {
		if len(g.Items) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n### %s\n\n", g.Gate)
		for _, it := range g.Items {
			fmt.Fprintf(&b, "- %s\n", it)
		}
	}
	return b.String()
}
