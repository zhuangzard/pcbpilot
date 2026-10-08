package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"
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
	Notes     []string           `json:"notes,omitempty"`
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
holes); the board outline and holes in the written file stay KiCad's.
Locked footprints and mounting holes stay put. Moving parts invalidates the
copper: tracks and vias are removed and zones unfilled in the output.`,
		Example: `  pcbpilot kicad place --pcb board.kicad_pcb --out placed/board.kicad_pcb
  pcbpilot kicad place --pcb board.kicad_pcb --out placed/board.kicad_pcb --mech mech.json --intent intent.json --place-seeds 8`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := runKicadPlace(o, kicadPlaceDeps{snapshot: kicad.Snapshot, place: kicad.Place}, stderr)
			if err != nil {
				return err
			}
			for _, s := range rep.Seeds {
				if s.File != "" {
					fmt.Fprintf(stdout, "wrote %s (seed %d, score %.1f, illegal %d, labelBlocked %d)\n", s.File, s.Seed, s.Score, s.Illegal, s.LabelBlocked)
				}
			}
			fmt.Fprintf(stdout, "report: %s\n", o.report)
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
	b, fixed, err := kicadBoard(raw)
	if err != nil {
		return nil, err
	}
	power, err := kicadPlacePower(o)
	if err != nil {
		return nil, err
	}
	rep := &kicadPlaceReport{PCB: o.pcb, Out: o.out, Fixed: fixed}
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
		if mc, err = pcbauto.ApplyMech(b, mech); err != nil {
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
			rep.Notes = append(rep.Notes, "--mech outline constrains the model only; the written board keeps its KiCad Edge.Cuts")
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
	outs, trials, err := pcbauto.PlaceSeeds(b, prep, mc, popt, o.placeSeeds)
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
	for _, t := range trials {
		s := kicadPlaceSeed{SeedTrial: t, LabelBlocked: t.Metrics.LabelBlocked}
		if t.Err != "" {
			fmt.Fprintf(stderr, "placement seed %d: %s\n", t.Seed, t.Err)
			rep.Seeds = append(rep.Seeds, s)
			continue
		}
		fmt.Fprintf(stderr, "placement seed %d: wirelength %.1f in, critical excess %.0f mil, illegal %d, labels blocked %d, score %.1f\n",
			t.Seed, t.Metrics.WirelengthIn, t.Metrics.CriticalExcessMil, t.Illegal, t.Metrics.LabelBlocked, t.Score)
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
			if s.Write, err = deps.place(o.pcb, posesFromBoard(outs[i].Board), s.File); err != nil {
				return nil, err
			}
			if err := copyKicadPro(o.pcb, s.File); err != nil {
				return nil, err
			}
		}
		rep.Seeds = append(rep.Seeds, s)
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
func kicadBoard(raw []byte) (*pcbauto.Board, []string, error) {
	b, err := pcbauto.FromSnapshot(raw)
	if err != nil {
		return nil, nil, err
	}
	var s struct {
		Components []kicadSnapComp `json:"components"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, nil, err
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
	return b, fixed, nil
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
func posesFromBoard(b *pcbauto.Board) map[string]kicad.Pose {
	out := make(map[string]kicad.Pose, len(b.Parts))
	for _, p := range b.Parts {
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

// copyKicadPro copies X.kicad_pro next to out (same base name): KiCad reads
// the board's design rules and net classes from it.
func copyKicadPro(pcb, out string) error {
	src := strings.TrimSuffix(pcb, ".kicad_pcb") + ".kicad_pro"
	raw, err := os.ReadFile(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(strings.TrimSuffix(out, ".kicad_pcb")+".kicad_pro", raw, 0o644)
}
