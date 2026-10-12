package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// kicadChannelsReport is the JSON of `kicad channels` (v0.9.2 experiment E1).
type kicadChannelsReport struct {
	PCB         string              `json:"pcb"`
	Params      channelParams       `json:"params"`
	Connections []connectionChannel `json:"connections,omitempty"`
	Proxy       placementProxy      `json:"proxy"`
	// AUC per tightness metric: P(a blocked pad is tighter than a random other
	// connected pad); 0.5 = no signal.
	AUC      map[string]float64 `json:"auc,omitempty"`
	Blocked  int                `json:"blockedPads"`
	Universe int                `json:"universePads"`
}

func newKicadChannelsCmd(stdout, stderr io.Writer) *cobra.Command {
	var pcb, out, intent string
	var summaries []string
	var withCopper bool
	var cp channelParams
	c := &cobra.Command{
		Use:   "channels",
		Short: "Escape-channel metrics of a board's pads (placement-only proxy; experiment E1/E2)",
		Long: `Measures, for every connected pad, the room it has to leave its part: free
corridors in 8 directions, directions where a via still fits, the gap to the
nearest foreign-net copper and the courtyard density around it — offline, no
router. --summary names the summary.json of a route run; its unrouted
connections are tabulated against those metrics and each metric's AUC for
separating the blocked pads from all others is reported (0.5 = no signal).
Without --with-copper, existing tracks and vias are ignored (placement only).`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			rep, err := runKicadChannels(pcb, intent, summaries, withCopper, cp)
			if err != nil {
				return err
			}
			printChannels(stdout, rep)
			if out != "" {
				blob, _ := json.MarshalIndent(rep, "", "  ")
				return os.WriteFile(out, append(blob, '\n'), 0o644)
			}
			return nil
		},
	}
	c.Flags().StringVar(&pcb, "pcb", "", "input .kicad_pcb (required; never modified)")
	c.Flags().StringVar(&out, "out", "", "report JSON")
	c.Flags().StringVar(&intent, "intent", "", "intent.json: per-net track width / clearance")
	c.Flags().StringArrayVar(&summaries, "summary", nil, "route summary.json whose routeResult.unrouted are the blocked connections (repeatable)")
	c.Flags().BoolVar(&withCopper, "with-copper", false, "count the board's existing tracks and vias as obstacles")
	c.Flags().Float64Var(&cp.TrackMil, "track-mil", 6, "corridor track width")
	c.Flags().Float64Var(&cp.ReachMil, "reach-mil", 120, "longest corridor run examined from the pad edge")
	c.Flags().Float64Var(&cp.StepMil, "step-mil", 4, "run resolution")
	c.Flags().Float64Var(&cp.DensityMil, "density-mil", 150, "half side of the density square")
	return c
}

func runKicadChannels(pcb, intent string, summaries []string, withCopper bool, cp channelParams) (*kicadChannelsReport, error) {
	if pcb == "" {
		return nil, fmt.Errorf("--pcb is required")
	}
	raw, err := kicad.Snapshot(pcb)
	if err != nil {
		return nil, err
	}
	var s boardSnapshot
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, err
	}
	var env *escapeEnv
	if intent != "" {
		in, err := loadDesignIntent(intent)
		if err != nil {
			return nil, err
		}
		env = kicadEscapeEnv(&s, raw, intentRequirements(in))
	} else {
		env = kicadEscapeEnv(&s, raw, nil)
	}
	if !withCopper {
		env.tracks, env.vias = nil, nil
	}
	def := defaultChannelParams(env)
	if cp.TrackMil <= 0 {
		cp.TrackMil = def.TrackMil
	}
	if cp.ReachMil <= 0 {
		cp.ReachMil = def.ReachMil
	}
	if cp.StepMil <= 0 {
		cp.StepMil = def.StepMil
	}
	if cp.DensityMil <= 0 {
		cp.DensityMil = def.DensityMil
	}
	cp.ViaDiaMil = def.ViaDiaMil
	rep := &kicadChannelsReport{PCB: pcb, Params: cp}
	rep.Proxy, _ = env.scorePlacement(cp)

	var blocked []routeConn
	for _, f := range summaries {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		var sm struct {
			RouteResult struct {
				Unrouted []routeConn `json:"unrouted"`
			} `json:"routeResult"`
		}
		if err := json.Unmarshal(b, &sm); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		blocked = append(blocked, sm.RouteResult.Unrouted...)
	}
	rep.Connections = env.channelsFor(blocked, true, cp)

	// AUC over every connected pad on the board.
	bad := map[string]bool{}
	for _, c := range blocked {
		bad[c.From], bad[c.To] = true, true
	}
	metrics := map[string]func(padChannel) float64{
		"-viaDirs":  func(c padChannel) float64 { return -float64(c.ViaDirs) },
		"-freeDirs": func(c padChannel) float64 { return -float64(c.FreeDirs) },
		"-bestRun":  func(c padChannel) float64 { return -c.BestRun },
		"-minGap":   func(c padChannel) float64 { return -math.Min(c.MinGap, 1000) },
		"density":   func(c padChannel) float64 { return c.Density },
	}
	pos, neg := map[string][]float64{}, map[string][]float64{}
	for _, q := range env.pads {
		if q.Net == "" {
			continue
		}
		ch := env.channelOf(q, cp)
		rep.Universe++
		isBad := bad[ch.Pad]
		if isBad {
			rep.Blocked++
		}
		for name, f := range metrics {
			if isBad {
				pos[name] = append(pos[name], f(ch))
			} else {
				neg[name] = append(neg[name], f(ch))
			}
		}
	}
	rep.AUC = map[string]float64{}
	for name := range metrics {
		if a := auc(pos[name], neg[name]); !math.IsNaN(a) {
			rep.AUC[name] = math.Round(a*1000) / 1000
		}
	}
	return rep, nil
}

func printChannels(w io.Writer, r *kicadChannelsReport) {
	p := r.Proxy
	fmt.Fprintf(w, "proxy: %d fine-pitch IC(s), %d pads measured, trapped %d, tight %d, mean viaDirs %.2f, min gap %.1f mil\n",
		p.DenseICCount, p.PadsMeasured, len(p.Trapped), len(p.Tight), p.MeanViaDirs, p.MinGapMil)
	if len(p.Trapped) > 0 {
		fmt.Fprintf(w, "  trapped: %s\n", strings.Join(p.Trapped, " "))
	}
	if len(r.Connections) > 0 {
		fmt.Fprintf(w, "\n%-44s %-8s %5s %5s %7s %7s %5s | %-8s %5s %5s %7s %7s %5s | dist\n", "blocked connection", "pad", "via", "free", "run", "gap", "dens", "pad", "via", "free", "run", "gap", "dens")
		cell := func(c *padChannel) string {
			if c == nil {
				return fmt.Sprintf("%-8s %5s %5s %7s %7s %5s", "-", "-", "-", "-", "-", "-")
			}
			return fmt.Sprintf("%-8s %5d %5d %7.0f %7.1f %5.2f", c.Pad, c.ViaDirs, c.FreeDirs, c.BestRun, math.Min(c.MinGap, 999), c.Density)
		}
		for _, c := range r.Connections {
			fmt.Fprintf(w, "%-44s %s | %s | %.0f\n", c.Conn, cell(c.From), cell(c.To), c.DistMil)
		}
	}
	if len(r.AUC) > 0 {
		names := make([]string, 0, len(r.AUC))
		for n := range r.AUC {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(w, "\nAUC (blocked-end pads %d vs %d other connected pads; 0.5 = no signal):\n", r.Blocked, r.Universe-r.Blocked)
		for _, n := range names {
			fmt.Fprintf(w, "  %-10s %.3f\n", n, r.AUC[n])
		}
	}
}
