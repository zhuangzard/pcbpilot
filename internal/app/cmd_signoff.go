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
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/pkg/netname"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

type signoffOpts struct {
	board, intent, sim, review, manual, report, outDir, waivers string
	connectivity, values                                        []string
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
	f.StringArrayVar(&o.values, "values", nil, "part values with LCSC numbers (sch list JSON or {\"parts\":{ref:{value,mpn,lcsc}}}; repeatable)")
	f.StringVar(&o.review, "review", "", "design-stage review.json (review-panel)")
	f.StringVar(&o.manual, "manual", "", "board manual HTML")
	f.StringVar(&o.report, "report", "", "design report report.json")
	f.StringVar(&o.outDir, "out-dir", "", "output directory (default signoff/)")
	f.StringVar(&o.waivers, "waivers", "", "signed waivers JSON")
	return c
}

// signoffGate runs the release sign-off as one run gate ("signoff"): every
// failing sign-off gate is an item. kicad route and pcb auto route end with it.
func signoffGate(so signoffOpts, waivers []gateWaiver, stderr io.Writer) gateResult {
	g := gateResult{Gate: "signoff"}
	res, err := runSignoff(so, waivers, stderr)
	if err != nil {
		g.Detail = "signoff: " + err.Error()
		return g
	}
	g.Pass = res.Pass
	g.Detail = "pcbpilot signoff: " + filepath.Join(so.outDir, "signoff.md")
	for _, x := range res.Gates {
		if !x.Pass {
			g.Items = append(g.Items, x.Gate+": "+x.Detail)
		}
	}
	return g
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
	if len(o.values) == 0 && fileExists(filepath.Join(dir, "sch-values.json")) {
		o.values = []string{filepath.Join(dir, "sch-values.json")}
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
	// Identity is the net's identity (pkg/netname) when the intent names it
	// by its display name: KiCad's full hierarchical name on the board.
	Identity string `json:"identity,omitempty"`
	Status   string `json:"status"`
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
		ID       string `json:"id"`
		Name     string `json:"name"`
		FullName string `json:"fullName"` // net identity (kicad netlist)
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
			if c.Device.SupplierID != "" && (reLCSC.MatchString(c.Device.SupplierID) || lcsc[c.Ref] == "") {
				lcsc[c.Ref] = c.Device.SupplierID
			} else if _, ok := lcsc[c.Ref]; !ok {
				lcsc[c.Ref] = ""
			}
		}
	}
	for _, p := range o.values {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		vals, err := powersim.ParseValues(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		for ref, v := range vals {
			if v.LCSC != "" {
				lcsc[ref] = v.LCSC
			} else if _, ok := lcsc[ref]; !ok {
				lcsc[ref] = ""
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
	// Scope: every designator from the schematic and the board except holes,
	// fiducials, test points and logos; DNP and BOM / position-file
	// exclusions are not known here, so this can count more parts than
	// `kicad lcsc --check` (assembled parts only).
	pg.Detail = fmt.Sprintf("%d BOM component(s) checked (every schematic/board designator except H/MH/FID/TP/LOGO…; DNP and BOM/position-file exclusions not applied — `kicad lcsc --check` counts assembled parts only), %d without a valid LCSC number", nBOM, len(pg.Items))
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
		if name == "signoff-copper" {
			if why, nets := boardCopperMissing(snap); why != "" {
				// Nothing below the width or via floor is no evidence for a
				// net with nothing routed: cannot check, never a pass.
				g.Pass = false
				var items []string
				for i, n := range nets {
					if i == 30 {
						items = append(items, fmt.Sprintf("cannot check: … %d more unrouted net(s)", len(nets)-30))
						break
					}
					items = append(items, "cannot check: "+n)
				}
				if len(nets) == 0 {
					items = []string{"cannot check: " + why}
				}
				g.Items = append(items, g.Items...)
				g.Detail = "cannot check: " + why + " | " + g.Detail
			}
		}
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

	// 5. Continuity and traceability, on net identities (pkg/netname): the
	// schematic's fullName (else its name, resolved against the board's
	// names for documents without one), the board's own net names, and the
	// intent's names resolved against both.
	boardIDs := netname.NewResolver(boardNetIDs(snap))
	netIDs, shorts := signoffNetIdentities(docs, boardIDs)
	schPins := map[string]string{}
	netParts := map[string]map[string]bool{}
	for di, d := range docs {
		ref := map[string]string{}
		for _, c := range d.Components {
			ref[c.ID] = c.Ref
		}
		name := netIDs[di]
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
			if netname.IsUnconnected(v) {
				delete(board, k)
			}
		}
		g := padNetDiffGate(schPins, board, strings.Join(o.connectivity, ","), filepath.Join(o.outDir, "net-diff.json"))
		g.Gate = "signoff-continuity"
		if len(shorts) > 0 {
			g.Pass = false
			g.Items = append(shorts, g.Items...)
		}
		add(g)
	}
	boardPads := map[string]int{}
	for _, n := range boardPinNets(snap) {
		boardPads[n]++
	}
	schNets := make([]string, 0, len(netParts))
	for n := range netParts {
		schNets = append(schNets, n)
	}
	schIDs := netname.NewResolver(schNets)
	trace := gateResult{Gate: "signoff-trace"}
	for _, name := range in.sortedNetNames() {
		n := in.Nets[name]
		// Only an unknown name falls back to the board: an ambiguous one
		// (two sheets with this label) is never looked up elsewhere.
		ambiguous := ""
		id, m := schIDs.Resolve(name)
		if m == netname.Ambiguous {
			ambiguous = "ambiguous in the schematic: " + strings.Join(schIDs.Candidates(name), " or ")
		} else if m == netname.Unknown {
			if id, m = boardIDs.Resolve(name); m == netname.Ambiguous {
				ambiguous = "ambiguous on the board: " + strings.Join(boardIDs.Candidates(name), " or ")
			}
		}
		row := traceRow{Net: name, Role: n.Role, CurrentA: n.CurrentA, OuterMil: n.WidthMil.Outer, InnerMil: n.WidthMil.Inner,
			MinMil: n.WidthMil.Min, ClearanceMil: n.ClearanceMil, ViasPerTrans: n.ViasPerTransition, Domain: n.Domain,
			BoardPads: boardPads[id], Status: "ok"}
		if id != name {
			row.Identity = id
		}
		for p := range netParts[id] {
			row.Parts = append(row.Parts, p)
		}
		sort.Strings(row.Parts)
		for _, v := range schPins {
			if v == id {
				row.SchematicPins++
			}
		}
		switch {
		case ambiguous != "":
			row.Status = ambiguous
		case len(docs) > 0 && row.SchematicPins == 0:
			row.Status = "not in the schematic"
		case row.BoardPads == 0:
			row.Status = "not on the board"
		}
		if row.Status != "ok" && (n.WidthMil.Outer > 0 || ambiguous != "") {
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

// signoffNetIdentities gives each schematic net (per document, by net id)
// its identity: fullName, else its name resolved against the board's nets.
// Resolution is per schematic net, never per pin or per name. Documents
// (pages) merge by net name, the rule of sim power / intent derive
// (powersim.BuildDesign): equal names on different documents are one net —
// unless one document has that name on several net records, which then
// all stay distinct. When several distinct schematic nets (two "VCC"
// records, or "VCC" and "/pwr/VCC") would become one identity they keep
// distinct names — the partition comparison then shows the board short —
// and each such group is returned as a failure item.
func signoffNetIdentities(docs []signoffDoc, board *netname.Resolver) ([]map[string]string, []string) {
	type src struct {
		doc         int
		id, key, to string
	}
	var srcs []src
	idsByName := map[string]map[int]map[string]bool{} // name → document → net ids
	for di, d := range docs {
		for _, n := range d.Nets {
			key, ident := n.FullName, n.FullName
			if key == "" {
				if key = n.Name; key == "" {
					key = n.ID
				}
				if id, m := board.Resolve(key); m == netname.Found {
					ident = id
				} else {
					ident = key
				}
			}
			srcs = append(srcs, src{di, n.ID, key, ident})
			if idsByName[key] == nil {
				idsByName[key] = map[int]map[string]bool{}
			}
			if idsByName[key][di] == nil {
				idsByName[key][di] = map[string]bool{}
			}
			idsByName[key][di][n.ID] = true
		}
	}
	// label names the source net: its name when pages merge it by name,
	// else name + document + net id.
	label := func(s src) string {
		for _, ids := range idsByName[s.key] {
			if len(ids) > 1 {
				return fmt.Sprintf("%s (document %d net %s)", s.key, s.doc+1, s.id)
			}
		}
		return s.key
	}
	sources := map[string]map[string]bool{} // identity → distinct source nets
	for _, s := range srcs {
		if sources[s.to] == nil {
			sources[s.to] = map[string]bool{}
		}
		sources[s.to][label(s)] = true
	}
	out := make([]map[string]string, len(docs))
	for i := range out {
		out[i] = map[string]string{}
	}
	for _, s := range srcs {
		if len(sources[s.to]) > 1 {
			out[s.doc][s.id] = label(s)
			continue
		}
		out[s.doc][s.id] = s.to
	}
	var shorts []string
	for _, ident := range sortedKeysOf(sources) {
		if len(sources[ident]) > 1 {
			shorts = append(shorts, fmt.Sprintf("schematic nets %s all resolve to board net %s — distinct schematic nets on one board net (a short), not merged", strings.Join(sortedKeysOf(sources[ident]), ", "), ident))
		}
	}
	return out, shorts
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
		net := t.Net
		if t.Identity != "" {
			net += " = " + t.Identity
		}
		fmt.Fprintf(&b, "| %s | %s | %.3f | %.2f / %.2f / %.2f | %.2f | %d | %d | %s | %d | %s |\n", net, t.Role, t.CurrentA, t.OuterMil, t.InnerMil, t.MinMil, t.ClearanceMil, t.ViasPerTrans, t.SchematicPins, parts, t.BoardPads, t.Status)
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

// boardCopperMissing names the nets signoff-copper cannot judge: every net
// with two or more physically distinct pads but no track, arc, via or
// filled copper of its own. Copper on other nets (a GND pour, one stray
// track) is no evidence for an unrouted VCC or signal net, and the pads of
// one part count too (an IC's several supply or ground pins). Pads that
// share a number within one footprint are one pad: KiCad connects them
// internally. Footprint jumper pad groups are not in the snapshot (pcbnew's
// SWIG returns JumperPadGroups as an opaque object), so they are judged.
// "" when every judged net has copper.
func boardCopperMissing(snap *boardSnapshot) (string, []string) {
	c := snap.Copper
	if a := c.Availability["routing"]; a != "" && a != "available" && len(c.Lines)+len(c.Arcs) == 0 {
		return "the board snapshot carries no routing (copper availability " + a + "): take it with the copper included", nil
	}
	nk := netCaseKey(snap.kicadNames())
	copper := map[string]bool{}
	for _, list := range [][]any{c.Lines, c.Arcs, c.Vias, c.Poured, c.Fills} {
		for _, it := range list {
			if m, ok := it.(map[string]any); ok {
				if n, _ := m["net"].(string); n != "" {
					copper[nk(n)] = true
				}
			}
		}
	}
	type netPads struct {
		name  string
		pads  map[string]bool // footprint index + "|" + pad number: one per internally connected group
		parts map[string]bool
	}
	nets := map[string]*netPads{}
	for ci, comp := range snap.Components {
		for _, p := range comp.Pads {
			if p.Net == "" {
				continue
			}
			k := nk(p.Net)
			np := nets[k]
			if np == nil {
				np = &netPads{name: p.Net, pads: map[string]bool{}, parts: map[string]bool{}}
				nets[k] = np
			}
			// Keyed by the footprint itself (its index in the snapshot), not
			// its designator: two footprints both named R1 are two parts.
			part := strconv.Itoa(ci)
			np.pads[part+"|"+p.Number] = true
			np.parts[part] = true
		}
	}
	var missing []string
	judged := 0
	for k, np := range nets {
		if len(np.pads) < 2 {
			continue
		}
		judged++
		if !copper[k] {
			missing = append(missing, fmt.Sprintf("%s: %d pads on %d part(s), no track, via or filled copper", np.name, len(np.pads), len(np.parts)))
		}
	}
	if len(missing) == 0 {
		return "", nil
	}
	sort.Strings(missing)
	return fmt.Sprintf("%d of %d multi-pad net(s) have no copper on the board: route them before sign-off", len(missing), judged), missing
}
