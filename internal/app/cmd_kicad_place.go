package app

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// kicadPlaceOpts are the inputs of `pcbpilot kicad place`.
type kicadPlaceOpts struct {
	pcb, out, report         string
	mech, power, sim, intent string
	groups                   []string
	placeSeeds, candidates   int
	seed                     int64
	timeBudget               time.Duration
	haloMil, haloICMil       float64
	haloICSet                bool // --halo-ic-mil given explicitly (0 then means no halo on ICs)
}

// kicadPlaceSeed is one placement seed in the report.
type kicadPlaceSeed struct {
	pcbauto.SeedTrial
	LabelBlocked int                `json:"labelBlocked"`
	LoopIR       *float64           `json:"loopIR,omitempty"` // A·mil, with --intent
	File         string             `json:"file,omitempty"`   // written board (best + runners-up)
	Write        *kicad.PlaceResult `json:"write,omitempty"`
}

// kicadPlaceReport is the report JSON next to the placed board.
type kicadPlaceReport struct {
	PCB       string             `json:"pcb"`
	Out       string             `json:"out"`
	BestSeed  int64              `json:"bestSeed"`
	Labels    *pcbauto.LabelSpec `json:"labels,omitempty"`
	Fixed     []string           `json:"fixed,omitempty"` // locked, mounting holes, --mech fixed/edge
	Seeds     []kicadPlaceSeed   `json:"seeds"`
	Mechanics *pcbauto.Mechanics `json:"mechanics,omitempty"`
	// TimeBudget is --time-budget; BudgetHit is set when any seed's
	// placement was cut short by it (see each seed's metrics.timeBudgetHit).
	TimeBudget string `json:"timeBudget,omitempty"`
	BudgetHit  bool   `json:"budgetHit,omitempty"`
	// Verdict is fail when the best seed has an illegal placement (exit 6,
	// as kicad drc): Illegal names each one, ZoneFill shows whether its
	// zone has the room (best seed).
	Verdict  gate.Status        `json:"verdict"`
	Illegal  []string           `json:"illegal,omitempty"`
	ZoneFill []pcbauto.ZoneFill `json:"zoneFill,omitempty"`
	Notes    []string           `json:"notes,omitempty"`

	path string // where the report was written
}

// kicadPlaceDeps are the KiCad side effects (stubbed in unit tests).
type kicadPlaceDeps struct {
	snapshot func(pcb string) ([]byte, error)
	place    func(pcb string, poses map[string]kicad.Pose, out string) (*kicad.PlaceResult, error)
}

func newKicadPlaceCmd(stdout, stderr io.Writer) *cobra.Command {
	var o kicadPlaceOpts
	c := &cobra.Command{
		Use:   "place",
		Short: "Place a KiCad board's footprints with the pcbauto placer (best of several seeds)",
		Long: `Reads the board with KiCad's python (outline, holes, keep-out rule areas,
courtyards, designator size), places it like 'pcb auto run --place
--no-route' (electrical weights from --power/--sim/--intent, module
ownership from --groups, room for every designator at its own size, best
of --place-seeds seeds) and writes the poses back with pcbnew:

  --out Y.kicad_pcb (+ Y.kicad_pro)    best seed
  Y.seed-N.kicad_pcb (+ .kicad_pro)    runners-up (--candidates in total)
  Y.report.json                        metrics per seed, labelBlocked, loopIR

--mech constrains the model only (fixed / edge parts, keep-outs, zones,
holes, in mm from the KiCad outline's lower-left corner); the board outline
and holes in the written file stay KiCad's. A --mech board size must match
the KiCad outline (0.1 mm); board.replaceOutline is refused (change the
outline with kicad board outline first).
The input's .kicad_pro and .kicad_dru (net classes, custom rules) are
copied beside every written board. Locked footprints and mounting holes stay put, and so do footprints whose
designator is shared (two H1) or a placeholder (empty, REF**, G***, R?): they are
fixed obstacles, listed in the report notes. Moving parts invalidates the
copper: tracks and vias are removed and zones unfilled in the output.

--time-budget bounds the placement search in wall time (all seeds run in
parallel against the same deadline; snapshot and board writing come on
top). A seed the budget cuts short keeps its best placement so far, with
metrics as measured; the report flags it (budgetHit, per seed
metrics.timeBudgetHit) and stderr says so. Progress (seed, phase,
elapsed) goes to stderr as it happens and every 30 s.

An illegal placement in the best seed (courtyard overlap, outside the
board or its zone) fails the run: the boards are still written, the
report lists each one (illegal) and every zone's room (zoneFill: area
its parts need vs. what it offers), stderr warns, and the exit code is
6 like a failed kicad drc.`,
		Example: `  pcbpilot kicad place --pcb board.kicad_pcb --out placed/board.kicad_pcb
  pcbpilot kicad place --pcb board.kicad_pcb --out placed/board.kicad_pcb --mech mech.json --intent intent.json --place-seeds 8`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			o.haloICSet = cmd.Flags().Changed("halo-ic-mil")
			rep, err := runKicadPlace(o, kicadPlaceDeps{snapshot: kicad.Snapshot, place: kicad.Place}, stderr)
			if err != nil {
				return err
			}
			for _, s := range rep.Seeds {
				if s.File != "" {
					fmt.Fprintf(stdout, "wrote %s (seed %d, score %.1f, illegal %d, labelBlocked %d)\n", s.File, s.Seed, s.Score, s.Illegal, s.LabelBlocked)
				}
			}
			fmt.Fprintf(stdout, "report: %s\n", rep.path)
			if code := rep.Verdict.ExitCode(); code != 0 {
				// The boards are written (to inspect or hand-fix), but an
				// illegal placement is a failed gate, like kicad drc's.
				return exitCodeError{code}
			}
			return nil
		},
	}
	c.Flags().StringVar(&o.pcb, "pcb", "", "input .kicad_pcb (required; never modified)")
	c.Flags().StringVar(&o.out, "out", "", "output .kicad_pcb for the best seed (required)")
	c.Flags().StringVar(&o.report, "report", "", "report JSON (default: <out>.report.json)")
	c.Flags().StringVar(&o.mech, "mech", "", "mechanical spec JSON (pcb auto --mech; mm from the board's lower-left corner, y up)")
	c.Flags().StringVar(&o.power, "power", "", "power spec JSON (rails with voltage/currentA, diffPairs)")
	c.Flags().StringVar(&o.sim, "sim", "", "simulated per-pin currents (pcbpilot sim power JSON)")
	c.Flags().StringVar(&o.intent, "intent", "", "intent.json: declared net currents/classes weight the placement; the report adds loopIR per seed")
	c.Flags().StringArrayVar(&o.groups, "groups", nil, "schematic module ownership JSON (repeatable; see pcb auto run --groups)")
	c.Flags().IntVar(&o.placeSeeds, "place-seeds", 6, "run this many seeds (from --seed) in parallel and keep the best")
	c.Flags().IntVar(&o.candidates, "candidates", 3, "boards to write, best included (runners-up as <out>.seed-N.kicad_pcb)")
	c.Flags().Int64Var(&o.seed, "seed", 1, "first placement seed")
	c.Flags().DurationVar(&o.timeBudget, "time-budget", 8*time.Minute, "wall-time budget of the placement search (0 = none); a seed cut short returns its best so far, flagged in the report")
	c.Flags().Float64Var(&o.haloMil, "halo-mil", 0, "extra keep-clear around every movable part (mil per side): routing room on dense boards (the placer otherwise packs for wirelength only)")
	c.Flags().Float64Var(&o.haloICMil, "halo-ic-mil", 0, "keep-clear for parts with 6 or more pads (mil per side; replaces --halo-mil for them): room for the fan-out via ring")
	return c
}

// runKicadPlace is `kicad place`: snapshot → board → PlaceSeeds → write.
func runKicadPlace(o kicadPlaceOpts, deps kicadPlaceDeps, stderr io.Writer) (*kicadPlaceReport, error) {
	if o.pcb == "" || o.out == "" {
		return nil, fmt.Errorf("--pcb and --out are required")
	}
	if !strings.HasSuffix(o.out, ".kicad_pcb") {
		return nil, fmt.Errorf("--out must end in .kicad_pcb")
	}
	if mustAbs(o.pcb) == mustAbs(o.out) {
		return nil, fmt.Errorf("--out must differ from --pcb (the input is never modified)")
	}
	base := strings.TrimSuffix(o.out, ".kicad_pcb")
	if o.report == "" {
		o.report = base + ".report.json"
	}
	raw, err := deps.snapshot(o.pcb)
	if err != nil {
		return nil, err
	}
	b, fixed, kept, err := kicadBoard(raw)
	if err != nil {
		return nil, err
	}
	power, err := kicadPlacePower(o)
	if err != nil {
		return nil, err
	}
	rep := &kicadPlaceReport{PCB: o.pcb, Out: o.out, Fixed: fixed, path: o.report}
	for _, n := range kicadKeptNotes(kept) {
		rep.Notes = append(rep.Notes, n)
		fmt.Fprintf(stderr, "placement: %s\n", n)
	}
	var mc *pcbauto.Mechanics
	if o.mech != "" {
		mraw, err := os.ReadFile(o.mech)
		if err != nil {
			return nil, err
		}
		mech, err := pcbauto.ParseMech(mraw)
		if err != nil {
			return nil, err
		}
		if pcbauto.NeedsAutoFrame(mech) {
			return nil, fmt.Errorf("--mech autoSize: kicad place keeps the KiCad outline — give the board's size or drop autoSize")
		}
		if mech.Board.ReplaceOutline {
			// The written board keeps its Edge.Cuts, and legality is judged
			// on the model: a replaced model outline would pass parts that
			// sit outside the delivered board.
			return nil, fmt.Errorf("--mech board.replaceOutline: kicad place writes the KiCad outline unchanged — change the outline with `pcbpilot kicad board outline` first, then drop replaceOutline (a --mech board size must match the KiCad outline)")
		}
		if mc, err = pcbauto.ApplyMech(b, mech); err != nil {
			if strings.Contains(err.Error(), "differs from the board's outline") {
				return nil, fmt.Errorf("%w — kicad place keeps the KiCad outline: change it with `pcbpilot kicad board outline` first, or fix the --mech board size", err)
			}
			return nil, err
		}
		for ref := range mc.Fixed {
			if !slices.Contains(rep.Fixed, ref) {
				rep.Fixed = append(rep.Fixed, ref)
			}
		}
		slices.Sort(rep.Fixed)
		rep.Mechanics = mc
		if len(mech.Board.Outline) >= 3 || mech.Board.Width > 0 {
			rep.Notes = append(rep.Notes, "--mech board matches the KiCad outline: kept; --mech coordinates count from its lower-left corner")
		}
	}
	understand := func(bc *pcbauto.Board, an *pcbauto.Analysis) (*pcbauto.Circuit, error) {
		c := pcbauto.Understand(bc, an)
		for _, f := range o.groups {
			graw, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			gs, err := pcbauto.ParseGroups(graw)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f, err)
			}
			c.Notes = append(c.Notes, pcbauto.ApplyGroups(c, bc, an, gs)...)
		}
		return c, nil
	}
	// The same options as `pcb auto run --place --no-route`.
	popt := pcbauto.PlaceOptions{Seed: o.seed}
	if o.haloMil > 0 || o.haloICMil > 0 {
		popt.Halo = kicadPlaceHalo(b, o.haloMil, o.haloICMil, o.haloICSet || o.haloICMil > 0)
		rep.Notes = append(rep.Notes, fmt.Sprintf("halo: %.1f mil per side on every part, %.1f mil on parts with 6+ pads", o.haloMil, o.haloICMil))
	}
	if ls := kicadLabelSpec(raw); ls != nil {
		popt.Labels, rep.Labels = ls, ls
		fmt.Fprintf(stderr, "placement: keeping room for each designator (%.1f mil high, %.1f mil per character)\n", ls.Height, ls.CharW)
	} else {
		rep.Notes = append(rep.Notes, "no visible designator on silk: placed without designator room")
	}
	prep := func(bc *pcbauto.Board) (*pcbauto.Analysis, *pcbauto.Circuit, error) {
		a := pcbauto.Analyze(bc, power, nil)
		c, err := understand(bc, a)
		return a, c, err
	}
	prog := newPlaceProgress(stderr, placeProgressEvery)
	popt.Progress = prog.phase
	if o.timeBudget > 0 {
		// The anneal keeps its own 90 s clock, so a board that placed before
		// places the same; the budget is only the outer cap. A board above
		// placeLargeBoard movable parts is the exception: its anneal used
		// to outlive that clock altogether (legalisation took longer than
		// 90 s, which switched the clock off) and stops at 90 s now — far
		// short of its moves on 1443 parts (decaps a mean 970 mil from their
		// pins) — so it may run to 65 % of the budget instead. 75 % was
		// tried: on a loaded machine legalise-2 and polish then ran into the
		// deadline (decaps a mean 1355 mil out in one seed).
		if len(b.Parts)-len(rep.Fixed) > placeLargeBoard {
			popt.Timeout = max(90*time.Second, o.timeBudget*65/100)
		}
		popt.Deadline = time.Now().Add(o.timeBudget)
		rep.TimeBudget = o.timeBudget.String()
	}
	outs, trials, err := pcbauto.PlaceSeeds(b, prep, mc, popt, o.placeSeeds)
	prog.stop()
	if err != nil {
		return nil, err
	}
	var currents map[string]float64
	if o.intent != "" {
		in, err := loadDesignIntent(o.intent)
		if err != nil {
			return nil, err
		}
		currents = intentLoopCurrents(in)
	}
	written := map[int64]int{} // seed → index in outs
	for i := 0; i < len(outs) && i < max(o.candidates, 1); i++ {
		written[outs[i].Trial.Seed] = i
	}
	rep.BestSeed = outs[0].Trial.Seed
	rep.Notes = append(rep.Notes, outs[0].Result.Notes...)
	if err := os.MkdirAll(filepath.Dir(o.out), 0o755); err != nil {
		return nil, err
	}
	if n := kicadPlaceLibNote(o.pcb, o.out); n != "" {
		rep.Notes = append(rep.Notes, n)
	}
	for _, t := range trials {
		s := kicadPlaceSeed{SeedTrial: t, LabelBlocked: t.Metrics.LabelBlocked}
		if t.Err != "" {
			fmt.Fprintf(stderr, "placement seed %d: %s\n", t.Seed, t.Err)
			rep.Seeds = append(rep.Seeds, s)
			continue
		}
		fmt.Fprintf(stderr, "placement seed %d: wirelength %.1f in, critical excess %.0f mil, illegal %d, labels blocked %d, score %.1f\n",
			t.Seed, t.Metrics.WirelengthIn, t.Metrics.CriticalExcessMil, t.Illegal, t.Metrics.LabelBlocked, t.Score)
		if h := t.Metrics.TimeBudgetHit; h != "" {
			rep.BudgetHit = true
			fmt.Fprintf(stderr, "placement seed %d: WARNING time budget %s reached during %s: best placement so far, not a finished one\n", t.Seed, o.timeBudget, h)
		}
		i, ok := written[t.Seed]
		if currents != nil {
			// The trial's own board copy (only successful seeds have one).
			for _, oc := range outs {
				if oc.Trial.Seed == t.Seed {
					v := loopIR(snapshotOfBoard(oc.Board), currents)
					s.LoopIR = &v
				}
			}
		}
		if ok {
			s.File = o.out
			if i > 0 {
				s.File = fmt.Sprintf("%s.seed-%d.kicad_pcb", base, t.Seed)
			}
			if s.Write, err = deps.place(o.pcb, posesFromBoard(outs[i].Board, kept), s.File); err != nil {
				return nil, err
			}
			if _, err := copyKicadSidecars(o.pcb, s.File); err != nil {
				return nil, err
			}
		}
		rep.Seeds = append(rep.Seeds, s)
	}
	best := outs[0]
	rep.Illegal, rep.ZoneFill, rep.Verdict = best.Result.Illegal, best.Result.ZoneFill, gate.Pass
	for _, z := range rep.ZoneFill {
		fill := "none usable"
		if z.Fill != nil {
			fill = fmt.Sprintf("%.2f", *z.Fill)
		}
		if z.OverFull || z.Tight && z.Illegal > 0 {
			fmt.Fprintf(stderr, "placement: WARNING zone %s: parts need %.0f mm² (fixed parts %.0f), zone offers %.0f mm² (fill %s, %d illegal) — enlarge the zone or move parts out of it\n", z.Domain, z.NeedMm2, z.FixedMm2, z.OfferMm2, fill, z.Illegal)
		} else if z.Illegal > 0 {
			fmt.Fprintf(stderr, "placement: WARNING zone %s: %d illegal placement(s) although it has room (need %.0f of %.0f mm², fill %s)\n", z.Domain, z.Illegal, z.NeedMm2, z.OfferMm2, fill)
		}
	}
	if v, n := kicadPlaceVerdict(rep.Illegal, best.Trial.Illegal); v == gate.Fail {
		rep.Verdict = v
		show := rep.Illegal
		if len(show) > 12 {
			show = append(append([]string(nil), show[:12]...), fmt.Sprintf("… %d more in the report", len(rep.Illegal)-12))
		}
		fmt.Fprintf(stderr, "placement: WARNING best seed %d has %d illegal placement(s): %s\n", best.Trial.Seed, n, strings.Join(show, "; "))
		rep.Notes = append(rep.Notes, fmt.Sprintf("verdict fail: the best seed has %d illegal placement(s) (see illegal, zoneFill); the boards are written for inspection, not for routing", n))
	}
	if rep.BudgetHit {
		rep.Notes = append(rep.Notes, fmt.Sprintf("time budget %s reached: at least one seed returned its best placement so far (metrics.timeBudgetHit names the phase); raise --time-budget for a finished placement", o.timeBudget))
	}
	blob, _ := json.MarshalIndent(rep, "", "  ")
	if err := os.WriteFile(o.report, append(blob, '\n'), 0o644); err != nil {
		return nil, err
	}
	return rep, nil
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

// kicadPlacePower reads --power / --sim / --intent like pcb auto run.
func kicadPlacePower(o kicadPlaceOpts) (pcbauto.PowerSpec, error) {
	var power pcbauto.PowerSpec
	if o.power != "" {
		raw, err := os.ReadFile(o.power)
		if err != nil {
			return power, err
		}
		if err := json.Unmarshal(raw, &power); err != nil {
			return power, fmt.Errorf("power spec: %w", err)
		}
	}
	if o.sim != "" {
		raw, err := os.ReadFile(o.sim)
		if err != nil {
			return power, err
		}
		if power.Sim, err = pcbauto.ParseSim(raw); err != nil {
			return power, err
		}
	}
	if o.intent != "" {
		raw, err := os.ReadFile(o.intent)
		if err != nil {
			return power, err
		}
		if power.Intent, err = pcbauto.ParseIntent(raw); err != nil {
			return power, err
		}
	}
	return power, nil
}

// kicadLabelSpec is labelSpecFromBoard with KiCad's glyph height: a KiCad
// text box spans the line pitch (1.6 mm for 1 mm text), so the height
// across the baseline is the designator field's text height plus its
// stroke (KiCad's default stroke when the field has none: 15 %). The
// per-character advance stays the measured box length.
func kicadLabelSpec(raw []byte) *pcbauto.LabelSpec {
	ls := labelSpecFromBoard(raw)
	if ls == nil {
		return nil
	}
	var snap boardSnapshot
	if json.Unmarshal(raw, &snap) != nil {
		return ls
	}
	font := projectDesignatorFont(snap.Silk)
	stroke := 0.0
	for _, t := range snap.Silk {
		if isVisibleDesignator(t) && math.Abs(t.FontSize-font) <= 0.05 && t.LineWidth > stroke {
			stroke = t.LineWidth
		}
	}
	if stroke <= 0 {
		stroke = 0.15 * font
	}
	if h := font + stroke; font > 0 && h < ls.Height {
		ls.Height = h
	}
	return ls
}

// kicadSnapComp holds the snapshot extras FromSnapshot does not read.
type kicadSnapComp struct {
	Designator string      `json:"designator"`
	Footprint  string      `json:"footprint"`
	Courtyard  *layoutBBox `json:"courtyard"`
	Pads       []boardPad  `json:"pads"`
}

var reMountRef = regexp.MustCompile(`^M?H\d+$`)

// kicadBoard builds the placer's board from a KiCad snapshot: the courtyard
// (when the snapshot has one) is the part's body — KiCad's DRC judges
// courtyards, and the dump's silk-shrunk bbox would let them overlap — and
// mounting holes are fixed (KiCad models them as footprints). Returns the
// fixed designators.
//
// A designator on more than one footprint (two H1, several REF** logos)
// or a placeholder designator (empty, REF**, G***, R?) cannot be posed by name:
// such parts enter the model as "<designator>@kiid:<primitiveId>", fixed,
// so they stay where they are, keep their room, and get no pose. kept
// names exactly those generated refs (a real designator may contain
// "@kiid:" too and is placed as usual).
func kicadBoard(raw []byte) (*pcbauto.Board, []string, kicadKept, error) {
	raw, kept, err := kicadKeepShared(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	b, err := pcbauto.FromSnapshot(raw)
	if err != nil {
		return nil, nil, nil, err
	}
	var s struct {
		Components []kicadSnapComp `json:"components"`
		Copper     *struct {
			Fills []struct {
				ID     string `json:"primitiveId"`
				Source []any  `json:"source"`
			} `json:"fills"`
		} `json:"copper"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, nil, nil, err
	}
	// Board cutouts (the bridge's "edgecut-N" fills: holes in the Edge.Cuts
	// outline, e.g. `kicad board outline --slot`) enter FromSnapshot as a
	// round hole of the cutout's smaller side, so a 2 x 20 mm slot kept parts
	// out of 2 mm of it only. Keep the exact polygon instead.
	if s.Copper != nil {
		polys := map[string][]pcbauto.Point{}
		for _, f := range s.Copper.Fills {
			if strings.HasPrefix(f.ID, "edgecut-") {
				polys[f.ID] = kicadSourcePoints(f.Source)
			}
		}
		for _, h := range b.Holes {
			if p := polys[h.Name]; len(p) >= 3 && h.Owner == "" {
				h.Poly = p
			}
		}
	}
	var fixed []string
	for _, c := range s.Components {
		p := b.Part(c.Designator)
		if p == nil {
			continue
		}
		if cy := c.Courtyard; cy != nil && cy.MaxX > cy.MinX && cy.MaxY > cy.MinY {
			p.SetBody(pcbauto.Rect{MinX: cy.MinX, MinY: cy.MinY, MaxX: cy.MaxX, MaxY: cy.MaxY})
		}
		if isMountingHole(c) {
			p.Fixed = true
		}
		if p.Fixed {
			fixed = append(fixed, p.Ref)
		}
	}
	return b, fixed, kept, nil
}

// kicadSourcePoints reads the bridge's polygon source [x, y, "L", x, y, …]
// (mil, y-up; the bridge writes straight segments only).
func kicadSourcePoints(src []any) []pcbauto.Point {
	var nums []float64
	for _, v := range src {
		if f, ok := v.(float64); ok {
			nums = append(nums, f)
		}
	}
	pts := make([]pcbauto.Point, 0, len(nums)/2)
	for i := 0; i+1 < len(nums); i += 2 {
		pts = append(pts, pcbauto.Point{X: nums[i], Y: nums[i+1]})
	}
	return pts
}

// kicadKeptSep joins a shared or placeholder designator and the
// footprint's snapshot id into its unique, fixed model ref.
const kicadKeptSep = "@kiid:"

// reKicadPlaceholder matches bridge.py's PLACEHOLDER_REF: an unannotated
// footprint (REF**, R?) or a logo (G***).
var reKicadPlaceholder = regexp.MustCompile(`^[A-Za-z_]*[*?]+$`)

// kicadPlaceholderRef is bridge.py's is_placeholder: an empty designator
// counts too (it names no footprint, so it cannot be posed either).
func kicadPlaceholderRef(ref string) bool { return ref == "" || reKicadPlaceholder.MatchString(ref) }

// kicadKeepShared rewrites the snapshot for the placer: every component
// whose designator is shared or a placeholder gets the unique designator
// "<designator>@kiid:<primitiveId>" and is locked; footprint holes follow
// their owner by componentId.
func kicadKeepShared(raw []byte) ([]byte, kicadKept, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, nil, fmt.Errorf("snapshot: %w", err)
	}
	var comps []map[string]any
	if c, ok := top["components"]; ok {
		if err := json.Unmarshal(c, &comps); err != nil {
			return nil, nil, fmt.Errorf("snapshot components: %w", err)
		}
	}
	count := map[string]int{}
	for _, c := range comps {
		ref, _ := c["designator"].(string)
		count[ref]++
	}
	renamed := map[string]string{} // primitiveId → model ref
	kept := kicadKept{}
	for _, c := range comps {
		ref, _ := c["designator"].(string)
		id, _ := c["primitiveId"].(string)
		if count[ref] < 2 && !kicadPlaceholderRef(ref) {
			continue
		}
		if id == "" {
			return nil, nil, fmt.Errorf("snapshot: designator %s is shared or a placeholder and the footprint has no primitiveId", ref)
		}
		model := ref + kicadKeptSep + id
		if count[model] > 0 || kept[model].ID != "" {
			return nil, nil, fmt.Errorf("snapshot: the model ref %s for a kept %q footprint is already a designator on the board", model, ref)
		}
		c["designator"], c["locked"] = model, true
		renamed[id], kept[model] = model, kicadKeptPart{Designator: ref, ID: id}
	}
	if len(renamed) == 0 {
		return raw, kept, nil
	}
	var err error
	var holes []map[string]any
	if h, ok := top["footprintHoles"]; ok {
		if err := json.Unmarshal(h, &holes); err != nil {
			return nil, nil, fmt.Errorf("snapshot footprintHoles: %w", err)
		}
		for _, h := range holes {
			if id, _ := h["componentId"].(string); renamed[id] != "" {
				h["owner"] = renamed[id]
			}
		}
		if top["footprintHoles"], err = json.Marshal(holes); err != nil {
			return nil, nil, err
		}
	}
	if top["components"], err = json.Marshal(comps); err != nil {
		return nil, nil, err
	}
	out, err := json.Marshal(top)
	return out, kept, err
}

// kicadKeptPart is one footprint kicadKeepShared fixed: its snapshot
// designator and id.
type kicadKeptPart struct{ Designator, ID string }

// kicadKept maps each generated model ref to the footprint it stands for.
type kicadKept map[string]kicadKeptPart

// kicadKeptNotes explains the parts kicadKeepShared fixed, one note per
// designator (sorted).
func kicadKeptNotes(kept kicadKept) []string {
	ids := map[string][]string{}
	var order []string
	for _, k := range slices.Sorted(maps.Keys(kept)) {
		ref, id := kept[k].Designator, kept[k].ID
		if ids[ref] == nil {
			order = append(order, ref)
		}
		ids[ref] = append(ids[ref], id)
	}
	slices.Sort(order)
	var notes []string
	for _, ref := range order {
		why := fmt.Sprintf("designator %q is on %d footprints", ref, len(ids[ref]))
		if kicadPlaceholderRef(ref) {
			why = fmt.Sprintf("placeholder designator %q (%d footprint(s))", ref, len(ids[ref]))
		}
		notes = append(notes, fmt.Sprintf("%s: kept in place, not placed (ids %s); annotate unique designators to place them", why, strings.Join(ids[ref], ", ")))
	}
	return notes
}

// isMountingHole: a MountingHole library footprint, or an H<n>/MH<n> part
// with no connected pad.
func isMountingHole(c kicadSnapComp) bool {
	if strings.Contains(strings.ToLower(c.Footprint), "mountinghole") {
		return true
	}
	if !reMountRef.MatchString(c.Designator) {
		return false
	}
	for _, p := range c.Pads {
		if p.Net != "" {
			return false
		}
	}
	return true
}

// posesFromBoard converts the model (mil, y up) to KiCad poses (mil, y
// down). KiCad's orientation is CCW as drawn, the model's CCW in y-up: the
// angle carries over unchanged — for bottom parts too, since both rotate
// the already mirrored footprint rigidly about its anchor.
func posesFromBoard(b *pcbauto.Board, kept kicadKept) map[string]kicad.Pose {
	out := make(map[string]kicad.Pose, len(b.Parts))
	for _, p := range b.Parts {
		if _, ok := kept[p.Ref]; ok {
			continue // shared / placeholder designator: kept in place (kicadBoard)
		}
		side := "top"
		if p.Side == pcbauto.LayerBottom {
			side = "bottom"
		}
		out[p.Ref] = kicad.Pose{XMil: p.Pos.X, YMil: 0 - p.Pos.Y, RotationDeg: p.Rotation, Side: side}
	}
	return out
}

// snapshotOfBoard is the pad subset of a board dump loopIR reads.
func snapshotOfBoard(b *pcbauto.Board) *boardSnapshot {
	s := &boardSnapshot{}
	for _, p := range b.Parts {
		c := boardComp{Designator: p.Ref}
		for _, pd := range p.Pads {
			c.Pads = append(c.Pads, boardPad{Number: pd.Number, Net: pd.Net, X: pd.Box.C.X, Y: pd.Box.C.Y})
		}
		s.Components = append(s.Components, c)
	}
	return s
}

// kicadPlaceLibNote explains the project library tables a placed board in
// another folder does without: they name ${KIPRJMOD} libraries that are
// not there. The footprints are embedded in the board, so it opens, DRCs
// and routes; updating footprints from the library needs the project
// folder. "" when out shares pcb's folder or pcb has no table.
func kicadPlaceLibNote(pcb, out string) string {
	src, dst := filepath.Dir(mustAbs(pcb)), filepath.Dir(mustAbs(out))
	if src == dst {
		return ""
	}
	var have []string
	for _, t := range []string{"fp-lib-table", "sym-lib-table"} {
		if _, err := os.Stat(filepath.Join(src, t)); err == nil {
			have = append(have, t)
		}
	}
	if len(have) == 0 {
		return ""
	}
	return fmt.Sprintf("the placed board's folder has no %s (the project's tables point at ${KIPRJMOD} libraries in %s): footprints are embedded in the board, so DRC and routing work; to update footprints from the library, place with --out inside the project folder", strings.Join(have, " / "), src)
}

// kicadPlaceVerdict is the best seed's verdict and illegal count: the named
// list decides (overlaps, outside the board or the zone, inside a
// keep-out), the metrics' count is a backstop.
func kicadPlaceVerdict(illegal []string, metricIllegal int) (gate.Status, int) {
	if n := max(len(illegal), metricIllegal); n > 0 {
		return gate.Fail, n
	}
	return gate.Pass, 0
}

// placeLargeBoard is the movable-part count above which kicad place lets
// the anneal run past its 90 s clock, within --time-budget.
const placeLargeBoard = 1000

// placeProgressEvery is how often kicad place repeats every running seed's
// phase on stderr while nothing else is printed.
var placeProgressEvery = 30 * time.Second

// placeProgress prints each seed's phase changes on stderr (with the time
// since the placement started) and, every `every`, the phase every seed is
// still in: a 1500-part board places for minutes.
type placeProgress struct {
	mu    sync.Mutex
	w     io.Writer
	start time.Time
	cur   map[int64]string
	since map[int64]time.Time
	done  chan struct{}
	wg    sync.WaitGroup
}

func newPlaceProgress(w io.Writer, every time.Duration) *placeProgress {
	pp := &placeProgress{w: w, start: time.Now(), cur: map[int64]string{}, since: map[int64]time.Time{}, done: make(chan struct{})}
	if every > 0 {
		pp.wg.Add(1)
		go func() {
			defer pp.wg.Done()
			t := time.NewTicker(every)
			defer t.Stop()
			for {
				select {
				case <-pp.done:
					return
				case <-t.C:
					pp.tick()
				}
			}
		}()
	}
	return pp
}

func (pp *placeProgress) elapsed() string {
	return time.Since(pp.start).Round(time.Second).String()
}

// phase is PlaceOptions.Progress.
func (pp *placeProgress) phase(seed int64, phase string) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	pp.cur[seed], pp.since[seed] = phase, time.Now()
	fmt.Fprintf(pp.w, "placement seed %d: %s (elapsed %s)\n", seed, phase, pp.elapsed())
}

func (pp *placeProgress) tick() {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	var parts []string
	for _, s := range slices.Sorted(maps.Keys(pp.cur)) {
		parts = append(parts, fmt.Sprintf("seed %d %s %s", s, pp.cur[s], time.Since(pp.since[s]).Round(time.Second)))
	}
	if len(parts) > 0 {
		fmt.Fprintf(pp.w, "placement: elapsed %s; %s\n", pp.elapsed(), strings.Join(parts, ", "))
	}
}

func (pp *placeProgress) stop() {
	close(pp.done)
	pp.wg.Wait()
}

// kicadPlaceHalo is the uniform routing-room keep-clear of --halo-mil /
// --halo-ic-mil. a field board (353 parts, 4 layers): packed for wirelength only, the
// patient side left 140 SMD pins without a fan-out and 131 connections
// unrouted while a third of the board stayed empty.
func kicadPlaceHalo(b *pcbauto.Board, all, ic float64, icSet bool) map[string]float64 {
	h := map[string]float64{}
	for _, p := range b.Parts {
		v := all
		if icSet && len(p.Pads) >= 6 {
			v = ic
		}
		if v > 0 {
			h[p.Ref] = v
		}
	}
	return h
}
