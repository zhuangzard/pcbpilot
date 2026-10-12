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

func newKicadPrebridgeCmd(stdout, stderr io.Writer) *cobra.Command {
	var pcb, out, intent, refs, place, planOut string
	var maxPitch float64
	var ripUp, dry bool
	c := &cobra.Command{
		Use:   "prebridge",
		Short: "Bridge same-net neighbour pads of fine-pitch ICs with locked copper before routing (v0.9.2 pre-route step)",
		Long: `On a 0.4 mm QFN two adjacent pads of one net are 0.2 mm apart: narrower than
a track plus two clearances, so no router can join them and each pad needs its
own escape. prebridge lays a short locked track from pad centre to pad centre
(width = the net's intent width, narrowed only as far as the clearance to the
neighbouring pads' copper demands and never below the intent minimum; a pair
where even the minimum does not fit is skipped and reported). --rip-up first
removes the unlocked copper (the route step would anyway), so the bridges are
planned on the bare board. The router then sees one pad group and routes the
remaining connections; locked copper survives rip-up and is fixed wiring.
The input is never modified.`,
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
				tmp, err := os.MkdirTemp("", "prebridge-")
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
			params := prebridgeParams{MaxPitchMil: maxPitch, Place: place,
				DefaultClearanceMil: def.Clearance, DefaultWidthMil: def.Width, DefaultMinMil: def.Min}
			if refs != "" {
				params.Refs = strings.Split(refs, ",")
			}
			plan := planPrebridges(&s, nets, params)
			blob, _ := json.MarshalIndent(plan, "", "  ")
			if planOut != "" {
				if err := os.WriteFile(planOut, append(blob, '\n'), 0o644); err != nil {
					return err
				}
			}
			fmt.Fprintf(stdout, "prebridge: %d bridge(s), %d skipped\n", len(plan.Bridges), len(plan.Skipped))
			for _, b := range plan.Bridges {
				fmt.Fprintf(stdout, "  %s.%s-%s %-10s %.2f mil (intent %.2f, fits %.2f)%s\n", b.Ref, b.PadA, b.PadB, b.Net, b.WidthMil, b.IntentMil, b.MaxFitMil, map[bool]string{true: " necked", false: ""}[b.Necked])
			}
			for _, k := range plan.Skipped {
				fmt.Fprintf(stdout, "  skipped %s.%s-%s %s: %s\n", k.Ref, k.PadA, k.PadB, k.Net, k.Reason)
			}
			if dry || len(plan.Bridges) == 0 {
				if out != "" && len(plan.Bridges) == 0 {
					return fmt.Errorf("no bridge planned: nothing written")
				}
				return nil
			}
			if _, err := t.Edit(src, prebridgeOps(plan), out); err != nil {
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
	c.Flags().StringVar(&out, "out", "", "output .kicad_pcb with the locked bridges")
	c.Flags().StringVar(&intent, "intent", "", "intent.json: per-net width / minimum / clearance (else the board rules)")
	c.Flags().StringVar(&refs, "ref", "", "only these designators, comma-separated (default: every IC with a fine row)")
	c.Flags().StringVar(&place, "place", "center", "bridge position along the pad: center | inner | outer")
	c.Flags().StringVar(&planOut, "plan", "", "write the plan (bridges, skipped pairs and why) as JSON")
	c.Flags().Float64Var(&maxPitch, "max-pitch-mil", 19.7, "largest pad centre distance bridged (19.7 mil = 0.5 mm)")
	c.Flags().BoolVar(&ripUp, "rip-up", false, "remove unlocked tracks and vias first and plan on the bare board")
	c.Flags().BoolVar(&dry, "dry-run", false, "print the plan only")
	return c
}

// prebridgeIntent returns the per-net table and the board-rule defaults.
func prebridgeIntent(s *boardSnapshot, intent string) (map[string]prebridgeNet, prebridgeNet) {
	def := prebridgeNet{Width: 7.874, Min: 5.9055, Clearance: 5.9055}
	if s.Rules != nil {
		if s.Rules.TrackWidthMil > 0 {
			def.Width = s.Rules.TrackWidthMil
		}
		if s.Rules.TrackWidthMinMil > 0 {
			def.Min = s.Rules.TrackWidthMinMil
		}
		if s.Rules.ClearanceMil > 0 {
			def.Clearance = s.Rules.ClearanceMil
		}
	}
	if def.Min > def.Width {
		def.Min = def.Width
	}
	return map[string]prebridgeNet{}, def
}

// loadIntentNets fills nets from an intent file (no-op for "").
func loadIntentNets(path string, nets map[string]prebridgeNet) error {
	if path == "" {
		return nil
	}
	in, err := loadDesignIntent(path)
	if err != nil {
		return err
	}
	for name, n := range in.Nets {
		if n == nil || n.WidthMil.Outer <= 0 {
			continue
		}
		nets[name] = prebridgeNet{Width: n.WidthMil.Outer, Min: n.WidthMil.Min, Clearance: n.ClearanceMil}
	}
	return nil
}
