package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

func newKicadEscapeStubsCmd(stdout, stderr io.Writer) *cobra.Command {
	var pcb, out, intent, refs, skipNets, planOut string
	var maxViaDirs int
	var minRun, reach float64
	var ripUp, dry bool
	c := &cobra.Command{
		Use:   "escape-stubs",
		Short: "Lock a short escape stub + via on tight fine-pitch IC pads before routing (v0.9.2 pre-route step, experiment)",
		Long: `For every connected pad of a fine-pitch IC the channel metrics (see 'kicad
channels') say whether it has room to leave its part. A pad with at most
--max-via-dirs via-capable directions (or a best free run below --min-run-mil)
gets a locked stub from its centre to a via: direction = the one with the
largest clearance surplus (away from the part body preferred), width = the
net's intent width necked only as far as the clearance to foreign copper
demands and never below the intent minimum, via = the board's rule via, which
must clear every foreign pad / track / via, keep off own-net SMD pads and keep
hole-to-hole distance to other vias. A pad where nothing fits is skipped and
the reason reported. Only the stub and the via are locked; the router still
does every connection. --rip-up first removes unlocked copper (the route step
would) and plans on the bare board. The input is never modified.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pcb == "" || (out == "" && !dry) {
				return fmt.Errorf("--pcb and --out (or --dry-run) are required")
			}
			if out != "" && mustAbs(pcb) == mustAbs(out) {
				return fmt.Errorf("--out must differ from --pcb")
			}
			t, err := kicad.Locate()
			if err != nil {
				return err
			}
			src := pcb
			if ripUp {
				tmp, err := os.MkdirTemp("", "escstubs-")
				if err != nil {
					return err
				}
				defer os.RemoveAll(tmp)
				src = filepath.Join(tmp, filepath.Base(pcb))
				if err := copyKicadPro(pcb, src); err != nil {
					return err
				}
				if _, err := t.RipUp(pcb, src); err != nil {
					return err
				}
			}
			raw, err := t.Snapshot(src)
			if err != nil {
				return err
			}
			var s boardSnapshot
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			nets, def := prebridgeIntent(&s, intent)
			if err := loadIntentNets(intent, nets); err != nil {
				return err
			}
			var env *escapeEnv
			if intent != "" {
				in, err := loadDesignIntent(intent)
				if err != nil {
					return err
				}
				env = kicadEscapeEnv(&s, raw, intentRequirements(in))
			} else {
				env = kicadEscapeEnv(&s, raw, nil)
			}
			p := escapeStubParams{MaxViaDirs: maxViaDirs, MinRunMil: minRun, ReachMil: reach,
				DefaultWidthMil: def.Width, DefaultMinMil: def.Min}
			if refs != "" {
				p.Refs = strings.Split(refs, ",")
			}
			if skipNets != "" {
				p.SkipNets = strings.Split(skipNets, ",")
			}
			cp := defaultChannelParams(env)
			plan := env.planEscapeStubs(nets, p, cp)
			blob, _ := json.MarshalIndent(plan, "", "  ")
			if planOut != "" {
				if err := os.WriteFile(planOut, append(blob, '\n'), 0o644); err != nil {
					return err
				}
			}
			fmt.Fprintf(stdout, "escape-stubs: %d stub(s), %d skipped\n", len(plan.Stubs), len(plan.Skipped))
			for _, st := range plan.Stubs {
				fmt.Fprintf(stdout, "  %-9s %-10s dir %3.0f° len %5.1f w %.2f (intent %.2f) margin %.1f [viaDirs %d, run %.0f]%s\n",
					st.Pad, st.Net, st.DirDeg, st.LenMil, st.WidthMil, st.IntentMil, st.MarginMil, st.ViaDirs, st.BestRun, map[bool]string{true: " necked", false: ""}[st.Necked])
			}
			for _, k := range plan.Skipped {
				fmt.Fprintf(stdout, "  skipped %-9s %-10s %s\n", k.Pad, k.Net, k.Reason)
			}
			if dry || len(plan.Stubs) == 0 {
				if out != "" && len(plan.Stubs) == 0 {
					return fmt.Errorf("no stub planned: nothing written")
				}
				return nil
			}
			if _, err := t.Edit(src, escapeStubOps(plan), out); err != nil {
				return err
			}
			if err := copyKicadPro(pcb, out); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "wrote %s\n", out)
			return nil
		},
	}
	c.Flags().StringVar(&pcb, "pcb", "", "input .kicad_pcb (required; never modified)")
	c.Flags().StringVar(&out, "out", "", "output .kicad_pcb with the locked stubs and vias")
	c.Flags().StringVar(&intent, "intent", "", "intent.json: per-net width / minimum / clearance (else the board rules)")
	c.Flags().StringVar(&refs, "ref", "", "only these designators, comma-separated (default: every fine-pitch IC)")
	c.Flags().StringVar(&skipNets, "skip-nets", "GND", "nets never stubbed, comma-separated")
	c.Flags().StringVar(&planOut, "plan", "", "write the plan (stubs, skipped pads and why) as JSON")
	c.Flags().IntVar(&maxViaDirs, "max-via-dirs", 2, "a pad with at most this many via-capable directions is tight")
	c.Flags().Float64Var(&minRun, "min-run-mil", 0, "a pad whose best free run is shorter than this is tight too (0 = off)")
	c.Flags().Float64Var(&reach, "reach-mil", 90, "longest stub examined (pad edge to via centre)")
	c.Flags().BoolVar(&ripUp, "rip-up", false, "remove unlocked tracks and vias first and plan on the bare board")
	c.Flags().BoolVar(&dry, "dry-run", false, "print the plan only")
	return c
}
