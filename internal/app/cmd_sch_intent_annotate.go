package app

// cmd_sch_intent_annotate.go — `sch intent-annotate`: write the electrical
// design intent INTO the schematic ("画原理图时就把电气意图写进去") in a
// non-invasive, reversible way.
//
// EasyEDA's official API has no net-level attribute (sch_Net is read-only and
// sch_PrimitiveWire.modify only takes line/net/color/width/type), so per-net
// attributes are not possible. Instead ONE grouped text block (one text
// primitive per line, created through the typed schematic.text.create) lists
// per-block function summaries and per-rail V/I/width/class, placed in free
// sheet area (inner border − title block − parts − wires − other texts).
//
// Reversible: every created primitiveId is journaled; a re-run deletes exactly
// the journaled texts (verified to still be texts with the journaled content)
// and writes the new block. An identical re-run writes nothing. Parts, wires
// and pin connections are never touched.

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

const intentAnnotateTag = "[pcbpilot:intent]"

type intentAnnotateText struct {
	PrimitiveID string  `json:"primitiveId"`
	Content     string  `json:"content"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
}

type intentAnnotateJournal struct {
	Version       int                  `json:"version"`
	Page          string               `json:"page"`
	IntentSHA     string               `json:"intentSha256"`
	Texts         []intentAnnotateText `json:"texts"`
	PendingDelete []string             `json:"pendingDelete,omitempty"`
}

type intentAnnotatePlacement struct {
	X      float64 `json:"x"`
	Top    float64 `json:"top"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Source string  `json:"source"` // auto | explicit | journal
}

type intentAnnotateReport struct {
	Mode      string                   `json:"mode"` // dry-run | apply
	Page      string                   `json:"page,omitempty"`
	Journal   string                   `json:"journal"`
	IntentSHA string                   `json:"intentSha256"`
	Lines     []string                 `json:"lines"`
	Placement *intentAnnotatePlacement `json:"placement,omitempty"`
	Replace   []string                 `json:"replace"`
	Created   []intentAnnotateText     `json:"created"`
	Deleted   []string                 `json:"deleted"`
	Orphans   []string                 `json:"orphanTaggedTexts,omitempty"`
	Warnings  []string                 `json:"warnings"`
	Verified  bool                     `json:"verified"`
	Status    string                   `json:"status"`
}

// ── pure: rendering ───────────────────────────────────────────────────────

func fmtNum(v float64) string {
	s := fmt.Sprintf("%.3f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		return "0"
	}
	return s
}

var intentRailRoleOrder = map[string]int{"power": 0, "switch": 1, "ground": 2}

// renderIntentAnnotation turns intent into the annotation lines. Deterministic.
func renderIntentAnnotation(in *designIntent) []string {
	sha := in.sourceSHA
	if len(sha) > 12 {
		sha = sha[:12]
	}
	lines := []string{fmt.Sprintf("%s ELECTRICAL INTENT sha256:%s (generated - re-run `pcbpilot sch intent-annotate` to update)", intentAnnotateTag, sha)}
	if len(in.Blocks) > 0 {
		lines = append(lines, "BLOCKS")
		for _, b := range in.Blocks {
			head := b.ID
			if b.Function != "" {
				head += " " + b.Function
			}
			if b.Core != "" {
				head += " " + b.Core
			}
			lines = append(lines, fmt.Sprintf("  %s: %s", head, strings.TrimSpace(b.Summary)))
		}
	}
	var rails []string
	for _, n := range in.sortedNetNames() {
		if _, ok := intentRailRoleOrder[in.Nets[n].Role]; ok {
			rails = append(rails, n)
		}
	}
	sort.SliceStable(rails, func(i, j int) bool {
		return intentRailRoleOrder[in.Nets[rails[i]].Role] < intentRailRoleOrder[in.Nets[rails[j]].Role]
	})
	if len(rails) > 0 {
		lines = append(lines, "RAILS  net: V nom/peak | I | width out/in/min mil | class | clearance | vias/transition")
		for _, name := range rails {
			n := in.Nets[name]
			cls := n.NetClass
			if cls == "" {
				cls = "-"
			}
			lines = append(lines, fmt.Sprintf("  %s: %s/%s V | %s A | %s/%s/%s mil | %s | %s mil | x%d",
				name, fmtNum(n.Voltage.Nom), fmtNum(n.Voltage.Peak), fmtNum(n.CurrentA),
				fmtNum(n.WidthMil.Outer), fmtNum(n.WidthMil.Inner), fmtNum(n.WidthMil.Min), cls, fmtNum(n.ClearanceMil), n.ViasPerTransition))
		}
	}
	pairs := map[string][]string{}
	for _, n := range in.sortedNetNames() {
		if dp := in.Nets[n].DiffPair; dp != "" {
			pairs[dp] = append(pairs[dp], n)
		}
	}
	if len(pairs) > 0 {
		lines = append(lines, "DIFF PAIRS")
		names := make([]string, 0, len(pairs))
		for k := range pairs {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			ns := pairs[k]
			if len(ns) == 2 && diffPolarity(ns[0]) == -1 && diffPolarity(ns[1]) == 1 {
				ns = []string{ns[1], ns[0]}
			}
			first := in.Nets[ns[0]]
			line := fmt.Sprintf("  %s: %s", k, strings.Join(ns, "/"))
			if first.ImpedanceOhm > 0 {
				line += fmt.Sprintf(" %s ohm diff", fmtNum(first.ImpedanceOhm))
			}
			if first.WidthMil.Outer > 0 {
				line += fmt.Sprintf(", w %s mil", fmtNum(first.WidthMil.Outer))
			}
			if first.PairGapMil > 0 {
				line += fmt.Sprintf(", gap %s mil", fmtNum(first.PairGapMil))
			}
			lines = append(lines, line)
		}
	}
	if len(in.NetClasses) > 0 {
		lines = append(lines, "NET CLASSES  (pushed to PCB by `pcbpilot pcb rules apply`)")
		for _, c := range in.NetClasses {
			line := fmt.Sprintf("  %s: %s | track %s | clr %s", c.Name, strings.Join(c.Nets, ","), fmtNum(c.TrackMil), fmtNum(c.ClearanceMil))
			if c.ViaDiaMil > 0 {
				line += fmt.Sprintf(" | via %s/%s", fmtNum(c.ViaDrillMil), fmtNum(c.ViaDiaMil))
			}
			lines = append(lines, line+" mil")
		}
	}
	if len(in.Pairs) > 0 {
		lines = append(lines, "ISOLATION")
		for _, p := range in.Pairs {
			line := fmt.Sprintf("  %s <-> %s: clr %s mm, creepage %s mm", p.A, p.B, fmtNum(p.ClearanceMm), fmtNum(p.CreepageMm))
			if p.SlotRequired {
				line += ", slot"
			}
			lines = append(lines, line)
		}
	}
	var notes []string
	for _, f := range in.Findings {
		if f.Severity != "warn" && f.Severity != "error" {
			continue
		}
		s := fmt.Sprintf("  [%s] %s", f.Severity, strings.TrimSpace(f.Message))
		if f.Suggestion != "" {
			s += " -> " + strings.TrimSpace(f.Suggestion)
		}
		notes = append(notes, s)
	}
	if len(notes) > 0 {
		lines = append(lines, "FINDINGS")
		if len(notes) > 6 {
			notes = append(notes[:6], fmt.Sprintf("  ... %d more in intent.json", len(notes)-6))
		}
		lines = append(lines, notes...)
	}
	for i, l := range lines {
		lines[i] = strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return ' '
			}
			return r
		}, l)
	}
	return lines
}

// ── pure: placement ───────────────────────────────────────────────────────

type intentAnnotateMetrics struct {
	LineHeight, CharWidth, Margin float64
}

func textWidthEstimate(s string, charW float64) float64 {
	w := 0.0
	for _, r := range s {
		if r >= 0x2E80 {
			w += 2 * charW
		} else {
			w += charW
		}
	}
	return w
}

func blockSize(lines []string, m intentAnnotateMetrics) (float64, float64) {
	w := 0.0
	for _, l := range lines {
		w = math.Max(w, textWidthEstimate(l, m.CharWidth))
	}
	return w, float64(len(lines)) * m.LineHeight
}

func bboxesOverlap(a, b layoutBBox) bool {
	return a.MinX < b.MaxX && b.MinX < a.MaxX && a.MinY < b.MaxY && b.MinY < a.MaxY
}

// findFreeTextArea scans the region top-left first (reading order) for a
// w×h rectangle clear of every obstacle (each already inflated by margin).
func findFreeTextArea(region layoutBBox, obstacles []layoutBBox, w, h, margin, step float64) (float64, float64, bool) {
	for top := region.MaxY - margin; top-h >= region.MinY+margin; top -= step {
		for x := region.MinX + margin; x+w <= region.MaxX-margin; x += step {
			r := layoutBBox{MinX: x, MinY: top - h, MaxX: x + w, MaxY: top}
			clear := true
			for _, o := range obstacles {
				if bboxesOverlap(r, o) {
					clear = false
					break
				}
			}
			if clear {
				return x, top, true
			}
		}
	}
	return 0, 0, false
}

// layoutAnnotationTexts positions line i at (x, top − (i+1)·lineHeight):
// y-up canvas, text anchored at its lower-left.
func layoutAnnotationTexts(lines []string, x, top float64, m intentAnnotateMetrics) []intentAnnotateText {
	out := make([]intentAnnotateText, len(lines))
	for i, l := range lines {
		out[i] = intentAnnotateText{Content: l, X: round2(x), Y: round2(top - float64(i+1)*m.LineHeight)}
	}
	return out
}

func inflate(b layoutBBox, d float64) layoutBBox {
	return layoutBBox{MinX: b.MinX - d, MinY: b.MinY - d, MaxX: b.MaxX + d, MaxY: b.MaxY + d}
}

// ── IO ────────────────────────────────────────────────────────────────────

var journalNameSanitizer = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func defaultIntentAnnotateJournal(page string) string {
	key := page
	if key == "" {
		key = "active-page"
	}
	dir, ok := artifactOutputDir()
	if !ok {
		dir = "."
	}
	return filepath.Join(dir, ".pcbpilot", "intent-annotate", journalNameSanitizer.ReplaceAllString(key, "_")+".json")
}

func readIntentAnnotateJournal(path string) (*intentAnnotateJournal, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var j intentAnnotateJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("journal %s: %w", path, err)
	}
	return &j, nil
}

func writeIntentAnnotateJournal(path string, j *intentAnnotateJournal) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(j, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

type intentAnnotateOptions struct {
	page, journal string
	dryRun        bool
	explicitXY    bool
	x, top        float64
	fontSize      float64
	metrics       intentAnnotateMetrics
}

func liveTexts(call intentRulesCaller) (map[string]intentAnnotateText, []intentAnnotateText, error) {
	res, err := call("schematic.text.list", nil)
	if err != nil {
		return nil, nil, fmt.Errorf("schematic.text.list: %w", err)
	}
	raw, ok := res["texts"].([]any)
	if !ok {
		return nil, nil, fmt.Errorf("schematic.text.list: result has no texts array")
	}
	byID := map[string]intentAnnotateText{}
	var all []intentAnnotateText
	for _, t := range raw {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		it := intentAnnotateText{PrimitiveID: asString(m["primitiveId"]), Content: asString(m["content"]), X: asFloat(m["x"]), Y: asFloat(m["y"])}
		byID[it.PrimitiveID] = it
		all = append(all, it)
	}
	return byID, all, nil
}

func runIntentAnnotate(in *designIntent, opt intentAnnotateOptions, call intentRulesCaller) (*intentAnnotateReport, error) {
	rep := &intentAnnotateReport{Mode: "apply", Page: opt.page, Journal: opt.journal, IntentSHA: in.sourceSHA,
		Replace: []string{}, Created: []intentAnnotateText{}, Deleted: []string{}, Warnings: []string{}}
	if opt.dryRun {
		rep.Mode = "dry-run"
	}
	lines := renderIntentAnnotation(in)
	rep.Lines = lines
	m := opt.metrics

	journal, err := readIntentAnnotateJournal(opt.journal)
	if err != nil {
		rep.Status = "journal-unreadable"
		return rep, err
	}
	byID, allTexts, err := liveTexts(call)
	if err != nil {
		rep.Status = "read-failed"
		return rep, err
	}
	journaled := map[string]bool{}
	var old []intentAnnotateText
	if journal != nil {
		if journal.Page != "" && opt.page != "" && journal.Page != opt.page {
			rep.Status = "journal-mismatch"
			return rep, fmt.Errorf("journal %s belongs to page %s, not %s", opt.journal, journal.Page, opt.page)
		}
		for _, id := range append(append([]string{}, journal.PendingDelete...), idsOf(journal.Texts)...) {
			journaled[id] = true
			live, ok := byID[id]
			if !ok {
				rep.Warnings = append(rep.Warnings, "journaled text "+id+" is already gone")
				continue
			}
			if !journalOwns(journal, live) {
				rep.Status = "journal-mismatch"
				return rep, fmt.Errorf("text %s no longer has the journaled content — edited by hand? refusing to delete it", id)
			}
			old = append(old, live)
		}
	}
	for _, t := range allTexts {
		if strings.HasPrefix(t.Content, intentAnnotateTag) && !journaled[t.PrimitiveID] {
			rep.Orphans = append(rep.Orphans, t.PrimitiveID)
		}
	}
	if len(rep.Orphans) > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d tagged intent header(s) not in this journal (another journal/page, or journal lost); left untouched — delete explicitly with `sch prim-delete --ids` after checking", len(rep.Orphans)))
	}

	// Idempotent replay: same lines at the journaled position → no writes.
	if journal != nil && len(journal.PendingDelete) == 0 && len(old) == len(journal.Texts) && len(old) == len(lines) {
		same := true
		for i, t := range journal.Texts {
			if t.Content != lines[i] {
				same = false
				break
			}
		}
		if same && len(journal.Texts) > 0 && (!opt.explicitXY ||
			(floatsClose(journal.Texts[0].X, round2(opt.x)) && floatsClose(journal.Texts[0].Y, round2(opt.top-m.LineHeight)))) {
			w, h := blockSize(lines, m)
			rep.Placement = &intentAnnotatePlacement{X: journal.Texts[0].X, Top: journal.Texts[0].Y + m.LineHeight, Width: w, Height: h, Source: "journal"}
			rep.Verified, rep.Status = true, "in-sync"
			return rep, nil
		}
	}

	// Placement.
	w, h := blockSize(lines, m)
	var x, top float64
	source := "explicit"
	if opt.explicitXY {
		x, top = opt.x, opt.top
	} else {
		source = "auto"
		region, obstacles, warns, err := annotateObstacles(call, journaled, m)
		rep.Warnings = append(rep.Warnings, warns...)
		if err != nil {
			rep.Status = "read-failed"
			return rep, err
		}
		var ok bool
		x, top, ok = findFreeTextArea(region, obstacles, w, h, m.Margin, 10)
		if !ok {
			rep.Status = "no-free-area"
			return rep, fmt.Errorf("no free %.0f×%.0f area inside the sheet border; pass --x/--y explicitly or enlarge the sheet", w, h)
		}
	}
	rep.Placement = &intentAnnotatePlacement{X: round2(x), Top: round2(top), Width: round2(w), Height: round2(h), Source: source}
	want := layoutAnnotationTexts(lines, x, top, m)
	rep.Replace = idsOf(old)
	if opt.dryRun {
		rep.Created = want
		rep.Status = "planned"
		return rep, nil
	}

	// Apply: create new → journal → delete old → journal → verify.
	j := &intentAnnotateJournal{Version: 1, Page: opt.page, IntentSHA: in.sourceSHA, PendingDelete: idsOf(old)}
	persist := func() error { return writeIntentAnnotateJournal(opt.journal, j) }
	fail := func(err error) (*intentAnnotateReport, error) {
		_ = persist()
		rep.Status = "unverified"
		return rep, fmt.Errorf("%w: %v", errIntentRulesUnverified, err)
	}
	for _, t := range want {
		payload := map[string]any{"x": t.X, "y": t.Y, "content": t.Content}
		if opt.fontSize > 0 {
			payload["fontSize"] = opt.fontSize
		}
		res, err := call("schematic.text.create", payload)
		if err != nil {
			return fail(fmt.Errorf("schematic.text.create: %w", err))
		}
		id := asString(res["primitiveId"])
		if id == "" {
			return fail(fmt.Errorf("schematic.text.create returned no primitiveId"))
		}
		t.PrimitiveID = id
		j.Texts = append(j.Texts, t)
		rep.Created = append(rep.Created, t)
		if err := persist(); err != nil {
			return fail(fmt.Errorf("journal: %w", err))
		}
		if res["verified"] != true {
			return fail(fmt.Errorf("text %s not confirmed by readback", id))
		}
	}
	if len(old) > 0 {
		res, err := call("schematic.primitives.delete", map[string]any{"primitiveIds": idsOf(old)})
		if err != nil {
			return fail(fmt.Errorf("schematic.primitives.delete: %w", err))
		}
		_ = res
	}
	after, _, err := liveTexts(call)
	if err != nil {
		return fail(err)
	}
	var survivors []string
	for _, t := range old {
		if _, ok := after[t.PrimitiveID]; ok {
			survivors = append(survivors, t.PrimitiveID)
		} else {
			rep.Deleted = append(rep.Deleted, t.PrimitiveID)
		}
	}
	j.PendingDelete = survivors
	for _, t := range j.Texts {
		live, ok := after[t.PrimitiveID]
		if !ok || live.Content != t.Content {
			return fail(fmt.Errorf("created text %s missing or changed on fresh readback", t.PrimitiveID))
		}
	}
	if len(survivors) > 0 {
		return fail(fmt.Errorf("old annotation text(s) still present after delete: %s", strings.Join(survivors, ",")))
	}
	if err := persist(); err != nil {
		return fail(fmt.Errorf("journal: %w", err))
	}
	rep.Verified, rep.Status = true, "applied"
	return rep, nil
}

func idsOf(ts []intentAnnotateText) []string {
	out := make([]string, 0, len(ts))
	for _, t := range ts {
		out = append(out, t.PrimitiveID)
	}
	return out
}

// journalOwns: the live text still carries the content we wrote (pending-delete
// ids were journaled by id only on an interrupted run; accept the tag or any
// journaled line content).
func journalOwns(j *intentAnnotateJournal, live intentAnnotateText) bool {
	for _, t := range j.Texts {
		if t.PrimitiveID == live.PrimitiveID {
			return t.Content == live.Content
		}
	}
	return true // pendingDelete entry: created by us in a previous run
}

// annotateObstacles reads the sheet region and every occupied rectangle.
func annotateObstacles(call intentRulesCaller, skip map[string]bool, m intentAnnotateMetrics) (layoutBBox, []layoutBBox, []string, error) {
	var warns []string
	res, err := call("schematic.components.list", map[string]any{"includeBBox": true, "includeWires": true})
	if err != nil {
		return layoutBBox{}, nil, nil, fmt.Errorf("schematic.components.list: %w", err)
	}
	comps, err := parseLayoutComps(res)
	if err != nil {
		return layoutBBox{}, nil, nil, err
	}
	var sheet *layoutBBox
	var obstacles []layoutBBox
	for _, c := range comps {
		if c.BBox == nil {
			continue
		}
		if c.ComponentType == "sheet" {
			if sheet == nil {
				sheet = c.BBox
			}
			continue
		}
		obstacles = append(obstacles, inflate(*c.BBox, m.Margin))
	}
	if sheet == nil {
		return layoutBBox{}, nil, nil, fmt.Errorf("no sheet bbox on this page; pass --x/--y explicitly")
	}
	for _, s := range buildWireSegments(res) {
		b := layoutBBox{MinX: math.Min(s.X0, s.X1), MinY: math.Min(s.Y0, s.Y1), MaxX: math.Max(s.X0, s.X1), MaxY: math.Max(s.Y0, s.Y1)}
		obstacles = append(obstacles, inflate(b, m.Margin))
	}
	if avail, ok := res["wiresAvailable"].(bool); ok && !avail {
		warns = append(warns, "wires unavailable; placement avoids parts only")
	}
	_, texts, err := liveTexts(call)
	if err != nil {
		return layoutBBox{}, nil, nil, err
	}
	for _, t := range texts {
		if skip[t.PrimitiveID] {
			continue
		}
		b := layoutBBox{MinX: t.X, MinY: t.Y - m.LineHeight*0.3, MaxX: t.X + textWidthEstimate(t.Content, m.CharWidth), MaxY: t.Y + m.LineHeight}
		obstacles = append(obstacles, inflate(b, m.Margin))
	}
	region := *sheet
	var showTB *bool
	var tbData map[string]any
	if tb, err := call("schematic.titleblock.get", nil); err == nil {
		if v, ok := tb["showTitleBlock"].(bool); ok {
			showTB = &v
		}
		tbData, _ = tb["titleBlockData"].(map[string]any)
	} else {
		warns = append(warns, "titleblock.get failed; using the sheet bbox and a default title-block keep-out")
	}
	g := deriveSheetGeometry(sheet, showTB)
	if border, _ := deriveSheetBorder(sheet, tbData); border.BBox != nil {
		region = *border.BBox
	}
	for _, k := range g.Keepouts {
		obstacles = append(obstacles, inflate(*k.BBox, m.Margin))
	}
	return region, obstacles, warns, nil
}

func newSchIntentAnnotateCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var intentPath, page, journal string
	var dryRun bool
	var x, y, fontSize float64
	m := intentAnnotateMetrics{LineHeight: 12, CharWidth: 6, Margin: 10}
	c := &cobra.Command{
		Use:   "intent-annotate",
		Short: "Write the electrical intent (block summaries, rail V/I/width/class) into the schematic as one replaceable text block",
		Long: `Write intent.json into the schematic so the drawing itself carries the design
intent: per-block function summary, per-rail voltage/current/width/net class,
diff pairs, net classes, isolation pairs and warn/error findings.

EasyEDA has no net-level attribute API, so this is ONE grouped text block (one
text primitive per line, typed schematic.text.create) in free sheet area:
inside the inner border, outside the title block, parts, wires and other text.
--x/--y pins the block's top-left instead.

Non-invasive and reversible: parts, wires and pin connections are never
touched. Every created primitiveId is journaled (default
.pcbpilot/intent-annotate/<page>.json); a re-run deletes exactly the journaled
texts (refusing if their content was edited) and writes the new block; an
identical re-run writes nothing. Tagged headers that are not in the journal
are reported, never deleted. Save afterwards (autosave also fires).`,
		Example: `  pcbpilot sch intent-annotate --intent intent.json --page <page-uuid> --dry-run --project ceshi
  pcbpilot sch intent-annotate --intent intent.json --page <page-uuid> --project ceshi
  pcbpilot sch intent-annotate --intent intent.json --page <page-uuid> --x 40 --y 780 --project ceshi`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			in, err := loadDesignIntent(intentPath)
			if err != nil {
				return err
			}
			if cmd.Flags().Changed("x") != cmd.Flags().Changed("y") {
				return fmt.Errorf("--x and --y must be given together")
			}
			for _, v := range []float64{m.LineHeight, m.CharWidth} {
				if !(v > 0) || math.IsInf(v, 0) {
					return fmt.Errorf("--line-height and --char-width must be positive")
				}
			}
			if page != "" {
				cfg.doc = page
			}
			if journal == "" {
				journal = defaultIntentAnnotateJournal(page)
			}
			call := func(action string, payload any) (map[string]any, error) {
				res, err := requestAction(cfg, action, *window, payload)
				if err != nil {
					return nil, err
				}
				if res.Result == nil {
					return map[string]any{}, nil
				}
				return res.Result, nil
			}
			opt := intentAnnotateOptions{page: page, journal: journal, dryRun: dryRun, explicitXY: cmd.Flags().Changed("x"),
				x: x, top: y, fontSize: fontSize, metrics: m}
			rep, runErr := runIntentAnnotate(in, opt, call)
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(rep); err != nil {
				return err
			}
			if runErr != nil {
				fmt.Fprintf(stderr, "sch intent-annotate: %v\n", runErr)
				return errActionFailed
			}
			return nil
		},
	}
	c.Flags().StringVar(&intentPath, "intent", "", "intent.json (schematic electrical-intent contract) (required)")
	_ = c.MarkFlagRequired("intent")
	c.Flags().StringVar(&page, "page", "", "schematic page UUID to annotate (sets --doc; default: active page)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "read the page and print lines + placement without writing")
	c.Flags().StringVar(&journal, "journal", "", "journal of created text IDs (default .pcbpilot/intent-annotate/<page>.json)")
	c.Flags().Float64Var(&x, "x", 0, "explicit block left x (schematic units, with --y)")
	c.Flags().Float64Var(&y, "y", 0, "explicit block top y (y-up, with --x)")
	c.Flags().Float64Var(&fontSize, "font-size", 0, "text font size (default: host default)")
	c.Flags().Float64Var(&m.LineHeight, "line-height", m.LineHeight, "line pitch in schematic units (calibrate to the font)")
	c.Flags().Float64Var(&m.CharWidth, "char-width", m.CharWidth, "estimated character width for free-area search")
	return c
}
