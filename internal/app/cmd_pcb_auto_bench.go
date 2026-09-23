package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// benchRow is one (board, placement variant) measurement.
type benchRow struct {
	Board      string              `json:"board"`
	Variant    string              `json:"variant"` // human | engine
	Score      float64             `json:"layoutScore"`
	Dims       map[string]float64  `json:"dimensions"`
	WLIn       float64             `json:"weightedWirelengthIn"`
	DecapMil   float64             `json:"decapToPinMil"`
	HotLoopMil float64             `json:"hotLoopPerimeterMil"`
	ChainInv   int                 `json:"chainInversions"`
	ChainExMil float64             `json:"chainExcessMil"`
	Joint      *pcbauto.JointScore `json:"joint,omitempty"`
	RudyOver   float64             `json:"rudyOverflow"`
	RudyMax    float64             `json:"rudyMaxUtil"`
	Overlaps   int                 `json:"overlaps"`
	Completion float64             `json:"routeCompletion"`
	Routed     int                 `json:"routed"`
	Conn       int                 `json:"connections"`
	Vias       int                 `json:"vias"`
	DRC        int                 `json:"drcViolations"`
	PlaceMs    int64               `json:"placeMillis"`
	RouteMs    int64               `json:"routeMillis"`
	Note       string              `json:"note,omitempty"`
}

// newPcbAutoBenchCmd compares human placements with engine placements on the
// same yardsticks: the 9-dimension layout-score, the placer's weighted
// wirelength, decap-to-pin distance, and the router's completion/vias.
func newPcbAutoBenchCmd(stdout, stderr io.Writer) *cobra.Command {
	var outPath string
	var noRoute bool
	var seed int64
	var timeout time.Duration
	var variants []string
	var dumpDir string
	var moves int
	var congestion float64
	var loops int
	c := &cobra.Command{
		Use:   "bench <board.json>...",
		Short: "Benchmark engine placement against the human placement on real boards (layout-score, wirelength, routability)",
		Long: `For each 'pcb dump' snapshot:
  human   — the placement as captured
  engine  — locked parts, connectors, mounting parts and antennas kept (the
            enclosure contract), every
            other part placed from scratch by the pcbauto placer
Both are scored by the same 9-dimension layout-score, the placer's weighted
wirelength and decap-to-pin distance, and (unless --no-route) routed by the
pcbauto router with the board's real layer count. Offline; nothing touches
the editor.`,
		Example: `  pcbpilot pcb auto bench internal/app/testdata/boards/*.json --out bench.json
  pcbpilot pcb auto bench board.json --no-route`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var rows []benchRow
			for _, path := range args {
				if strings.HasSuffix(path, ".expect.json") || strings.HasSuffix(path, ".spec.json") {
					continue
				}
				raw, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				name := strings.TrimSuffix(filepath.Base(path), ".json")
				for _, v := range variants {
					row, err := benchOne(cmd.Context(), name, v, raw, seed, noRoute, timeout, dumpDir, moves, congestion, loops)
					if err != nil {
						fmt.Fprintf(stderr, "%s/%s: %v\n", name, v, err)
						continue
					}
					fmt.Fprintf(stderr, "%-30s %-6s score %5.1f tidy %3.0f prot %3.0f rout %3.0f  wl %6.1fin  decap %4.0fmil  loop %4.0fmil  chain inv %d ex %5.0fmil  rudy %5.1f/%4.2f  ovl %d  place %5.1fs  route %5.1f%% vias %d drc %d  joint %s\n",
						name, v, row.Score, row.Dims["tidy"], row.Dims["protection"], row.Dims["routable"], row.WLIn, row.DecapMil, row.HotLoopMil, row.ChainInv, row.ChainExMil, row.RudyOver, row.RudyMax, row.Overlaps, float64(row.PlaceMs)/1000, row.Completion, row.Vias, row.DRC, jointText(row.Joint))
					rows = append(rows, row)
				}
			}
			writeBenchTable(stdout, rows)
			if outPath != "" {
				data, _ := json.MarshalIndent(rows, "", "  ")
				return os.WriteFile(outPath, data, 0o644)
			}
			return nil
		},
	}
	c.Flags().StringVar(&outPath, "out", "", "also write the rows as JSON")
	c.Flags().BoolVar(&noRoute, "no-route", false, "placement metrics only (fast)")
	c.Flags().Int64Var(&seed, "seed", 0, "placement seed")
	c.Flags().DurationVar(&timeout, "timeout", 3*time.Minute, "routing budget per variant")
	c.Flags().IntVar(&loops, "loops", 0, "engine: place↔route loop passes (0 = single placement, then route)")
	c.Flags().Float64Var(&congestion, "congestion", 0, "RUDY congestion weight during annealing (0 = default, negative = off)")
	c.Flags().IntVar(&moves, "moves", 0, "annealing moves per movable part (0 = placer default)")
	c.Flags().StringVar(&dumpDir, "dump-dir", "", "write each engine placement as <board>.engine.json (a pcb dump snapshot) for layout-score / inspection")
	c.Flags().StringSliceVar(&variants, "variants", []string{"human", "engine"}, "which placements to measure")
	return c
}

func benchOne(ctx context.Context, name, variant string, raw []byte, seed int64, noRoute bool, timeout time.Duration, dumpDir string, moves int, congestion float64, loops int) (benchRow, error) {
	row := benchRow{Board: name, Variant: variant}
	var loopOut *pcbauto.Result
	b, err := pcbauto.FromSnapshot(raw)
	if err != nil {
		return row, err
	}
	an := pcbauto.Analyze(b, pcbauto.PowerSpec{}, nil)
	circ := pcbauto.Understand(b, an)
	snapRaw := raw
	switch variant {
	case "human":
	case "engine":
		for _, p := range b.Parts {
			if k := circ.Kinds[p.Ref]; k == pcbauto.KindConnector || k == pcbauto.KindMechanical || k == pcbauto.KindAntenna {
				p.Fixed = true
			}
		}
		start := time.Now()
		popt := pcbauto.PlaceOptions{Seed: seed, Moves: moves, Congestion: congestion}
		var pr *pcbauto.PlaceResult
		if loops > 0 && !noRoute {
			lr, err := pcbauto.PlaceRoute(ctx, b, an, circ, nil, popt,
				pcbauto.Options{Stack: pcbauto.StackOptions{Force: b.CopperLayers}, Route: pcbauto.RouteOptions{Timeout: timeout}},
				pcbauto.LoopOptions{Passes: loops})
			if err != nil {
				return row, err
			}
			pr, loopOut = lr.Place, lr.Result
			var parts []string
			for _, p := range lr.Passes {
				parts = append(parts, fmt.Sprintf("p%d %.1f%%/%d→%.1f", p.Pass, p.Completion, p.DRC, p.Joint))
			}
			row.Note = "loop best p" + fmt.Sprint(lr.Best) + ": " + strings.Join(parts, ", ")
		} else {
			var err error
			if pr, err = pcbauto.Place(b, an, circ, nil, popt); err != nil {
				return row, err
			}
		}
		row.PlaceMs = time.Since(start).Milliseconds()
		row.Overlaps = pr.Metrics.Overlaps
		if snapRaw, err = pcbauto.ExportPlacedSnapshot(raw, b); err != nil {
			return row, err
		}
		if dumpDir != "" {
			if err := os.WriteFile(filepath.Join(dumpDir, name+".engine.json"), snapRaw, 0o644); err != nil {
				return row, err
			}
		}
	default:
		return row, fmt.Errorf("unknown variant %q", variant)
	}
	row.WLIn = pcbauto.WeightedWirelength(b, an, circ) / 1000
	row.DecapMil, _ = pcbauto.DecapDistance(b, an, circ)
	row.HotLoopMil, _ = pcbauto.HotLoopStats(b, an, circ)
	row.ChainInv, row.ChainExMil, _ = pcbauto.ChainStats(b, circ)
	if cm := pcbauto.Congestion(b, an); cm != nil {
		row.RudyOver, row.RudyMax = cm.Overflow, cm.MaxUtil
	}
	snap, err := loadBoardSnapshotFile(bytes.NewReader(snapRaw))
	if err != nil {
		return row, err
	}
	rep := analyzeLayoutScore(snap, nil, layoutScoreOpts{})
	row.Score = rep.Overall
	// The joint gate uses layout-lint's courtyard proxy (pad union, calibrated
	// on five good boards), the same for human and engine placements.
	lintOverlaps := 0
	for _, f := range rep.Blocking {
		if f.Type == "component-overlap" {
			lintOverlaps++
		}
	}
	row.Dims = map[string]float64{}
	for _, d := range rep.Dimensions {
		if d.Status != "skipped" {
			row.Dims[d.ID] = d.Score
		}
	}
	if variant == "human" {
		row.Overlaps = countOverlaps(b)
	}
	if noRoute {
		return row, nil
	}
	out := loopOut
	if out == nil {
		rctx, cancel := context.WithTimeout(ctx, 3*timeout)
		defer cancel()
		var err error
		out, err = pcbauto.Run(rctx, b, pcbauto.Options{Stack: pcbauto.StackOptions{Force: b.CopperLayers}, Route: pcbauto.RouteOptions{Timeout: timeout}})
		if err != nil {
			row.Note = err.Error()
			return row, nil
		}
	}
	s := out.Route.Stats
	row.Completion, row.Routed, row.Conn = s.Completion, s.Routed, s.Connections
	row.Vias = s.Vias + s.FanoutVias
	row.DRC = len(out.DRC.Violations)
	row.RouteMs = s.Millis
	row.Joint = pcbauto.Joint(b, out.Analysis, pcbauto.Understand(b, out.Analysis), out.Stackup, out.Route, out.DRC,
		pcbauto.JointOptions{PlacementScore: placementOnlyScore(row.Dims), Overlaps: lintOverlaps})
	return row, nil
}

func jointCells(j *pcbauto.JointScore) string {
	if j == nil {
		return "– | – | – | – | – | –"
	}
	cell := func(g string) string {
		if v, ok := j.Groups[g]; ok {
			return fmt.Sprintf("%.0f", v)
		}
		return "–"
	}
	return fmt.Sprintf("%.1f | %.2f | %.0f | %s | %s | %s", j.Overall, j.CompletionFactor, j.Quality, cell("electrical"), cell("efficiency"), cell("placement"))
}

func jointText(j *pcbauto.JointScore) string {
	if j == nil {
		return "–"
	}
	return fmt.Sprintf("%.1f (×%.2f q%.0f e%.0f f%.0f p%.0f)", j.Overall, j.CompletionFactor, j.Quality,
		j.Groups["electrical"], j.Groups["efficiency"], j.Groups["placement"])
}

// placementOnlyScore is the mean of the layout-score dimensions routing cannot
// see (the joint score measures protection, decap and routability on copper).
func placementOnlyScore(dims map[string]float64) float64 {
	sum, n := 0.0, 0
	for _, id := range []string{"tidy", "compact", "edge-io", "partition", "clearance", "flow-order"} {
		if v, ok := dims[id]; ok {
			sum += v
			n++
		}
	}
	if n == 0 {
		return -1
	}
	return sum / float64(n)
}

// countOverlaps counts same-side body overlaps (through-hole parts occupy both sides).
func countOverlaps(b *pcbauto.Board) int {
	n := 0
	for i, p := range b.Parts {
		for _, q := range b.Parts[i+1:] {
			if (p.Side == q.Side || tht(p) || tht(q)) && p.Body().OverlapArea(q.Body()) > 1 {
				n++
			}
		}
	}
	return n
}

func tht(p *pcbauto.Part) bool {
	for _, pd := range p.Pads {
		if pd.Layer == pcbauto.LayerMulti {
			return true
		}
	}
	return false
}

func writeBenchTable(w io.Writer, rows []benchRow) {
	dims := map[string]bool{}
	for _, r := range rows {
		for d := range r.Dims {
			dims[d] = true
		}
	}
	var dl []string
	for d := range dims {
		dl = append(dl, d)
	}
	sort.Strings(dl)
	fmt.Fprintf(w, "| board | placement | score | %s | WL in | decap mil | hot loop mil | chain inversions | chain excess mil | overlaps | route %% | vias | DRC | joint | ×compl | quality | elec | eff | place |\n", strings.Join(dl, " | "))
	fmt.Fprintf(w, "|---|---|---|%s---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n", strings.Repeat("---|", len(dl)))
	for _, r := range rows {
		var ds []string
		for _, d := range dl {
			if v, ok := r.Dims[d]; ok {
				ds = append(ds, fmt.Sprintf("%.0f", v))
			} else {
				ds = append(ds, "–")
			}
		}
		fmt.Fprintf(w, "| %s | %s | %.1f | %s | %.1f | %.0f | %.0f | %d | %.0f | %d | %.1f | %d | %d | %s |\n",
			r.Board, r.Variant, r.Score, strings.Join(ds, " | "), r.WLIn, r.DecapMil, r.HotLoopMil, r.ChainInv, r.ChainExMil, r.Overlaps, r.Completion, r.Vias, r.DRC, jointCells(r.Joint))
	}
}
