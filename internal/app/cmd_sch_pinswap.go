package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// sch pin-swap compiles one applyable feedback item (pcb auto run →
// feedback.json) into a guarded `sch apply` playbook: disconnect the net's
// marker on the old pin, clear NC on the new pin, place the net label there,
// mark the vacated pin NC, save. Offline only — it never talks to EasyEDA.

// pinSwapPlan is the compiled result.
type pinSwapPlan struct {
	Item     *pcbauto.FeedbackItem `json:"item"`
	Diff     []string              `json:"diff"`
	Playbook *playbook             `json:"playbook"`
	Status   string                `json:"status"`
	Warnings []string              `json:"warnings,omitempty"`
}

func findFeedbackItem(fb *pcbauto.Feedback, id string) (*pcbauto.FeedbackItem, error) {
	var ids []string
	for _, it := range fb.Items {
		if it.ID == id {
			return it, nil
		}
		if it.Applyable {
			ids = append(ids, it.ID)
		}
	}
	return nil, fmt.Errorf("feedback item %q not found (applyable items: %s)", id, strings.Join(ids, ", "))
}

// buildPinSwapPlan compiles the item. sch, when non-nil, is the connectivity
// snapshot of the page holding the part: every swap is checked against it
// and the playbook gets before/after connectivity checkpoints.
func buildPinSwapPlan(it *pcbauto.FeedbackItem, sch *connectivity.Document) (*pinSwapPlan, error) {
	if !it.Applyable || len(it.Proposal.Swaps) == 0 {
		return nil, fmt.Errorf("item %s (%s) is not an applyable pin swap", it.ID, it.Kind)
	}
	out := &pinSwapPlan{Item: it, Status: "live-unverified"}
	swaps := append([]pcbauto.PinSwap(nil), it.Proposal.Swaps...)
	sort.Slice(swaps, func(i, j int) bool {
		if swaps[i].Ref != swaps[j].Ref {
			return swaps[i].Ref < swaps[j].Ref
		}
		return swaps[i].Net < swaps[j].Net
	})
	// A pin may lose one net and gain one; never gain two.
	gain := map[string]string{}
	for _, s := range swaps {
		k := s.Ref + "." + s.ToPin
		if prev, dup := gain[k]; dup {
			return nil, fmt.Errorf("%s would receive both %s and %s", k, prev, s.Net)
		}
		gain[k] = s.Net
	}
	type pinState struct {
		name string
		net  string
		nc   bool
		ok   bool
	}
	state := func(ref, pin string) pinState { return pinState{} }
	var after connectivity.Document
	if sch != nil {
		if err := sch.Validate(); err != nil {
			return nil, fmt.Errorf("--sch: %w", err)
		}
		netName := map[string]string{}
		netID := map[string]string{}
		for _, n := range sch.Nets {
			netName[n.ID] = n.Name
			netID[n.Name] = n.ID
		}
		comp := map[string]connectivity.Component{}
		for _, c := range sch.Components {
			comp[c.Ref] = c
		}
		conn := map[[2]string]string{}
		for _, c := range sch.Connections {
			conn[[2]string{c.ComponentID, c.PinNumber}] = netName[c.NetID]
		}
		state = func(ref, pin string) pinState {
			c, ok := comp[ref]
			if !ok {
				return pinState{}
			}
			for _, p := range c.Pins {
				if p.Number == pin {
					return pinState{name: p.Name, net: conn[[2]string{c.ID, pin}], nc: p.NoConnected, ok: true}
				}
			}
			return pinState{}
		}
		moved := map[string]bool{}
		for _, s := range swaps {
			moved[s.Ref+"."+s.FromPin] = true
		}
		for _, s := range swaps {
			from, to := state(s.Ref, s.FromPin), state(s.Ref, s.ToPin)
			if !from.ok || !to.ok {
				return nil, fmt.Errorf("%s.%s / %s.%s not on this page (--sch %s): pass the page that holds %s", s.Ref, s.FromPin, s.Ref, s.ToPin, sch.DocumentID, s.Ref)
			}
			if from.net != s.Net {
				return nil, fmt.Errorf("stale feedback: schematic %s.%s is on %q, feedback expects %q — re-run pcb auto run after re-importing", s.Ref, s.FromPin, from.net, s.Net)
			}
			if to.net != "" && !moved[s.Ref+"."+s.ToPin] {
				return nil, fmt.Errorf("stale feedback: target %s.%s already carries %q", s.Ref, s.ToPin, to.net)
			}
			if s.ToName != "" && to.name != "" && !strings.EqualFold(s.ToName, to.name) && !strings.EqualFold("IO"+strings.TrimPrefix(strings.ToUpper(s.ToName), "IO"), to.name) {
				out.Warnings = append(out.Warnings, fmt.Sprintf("%s.%s: capability table calls it %s, the symbol calls it %s — check the pin map", s.Ref, s.ToPin, s.ToName, to.name))
			}
			if _, ok := netID[s.Net]; !ok {
				return nil, fmt.Errorf("net %s not on this page", s.Net)
			}
		}
		// expected connectivity after the swap
		after = cloneSchPlanState(*sch)
		cid := map[string]string{}
		for _, c := range after.Components {
			cid[c.Ref] = c.ID
		}
		drop := map[[2]string]bool{}
		for _, s := range swaps {
			drop[[2]string{cid[s.Ref], s.FromPin}] = true
		}
		var kept []connectivity.Connection
		for _, c := range after.Connections {
			if !drop[[2]string{c.ComponentID, c.PinNumber}] {
				kept = append(kept, c)
			}
		}
		for _, s := range swaps {
			kept = append(kept, connectivity.Connection{ComponentID: cid[s.Ref], PinNumber: s.ToPin, NetID: netID[s.Net], Kind: "net_label"})
			setSchPlanPinState(&after, cid[s.Ref], s.ToPin, false, "")
		}
		after.Connections = kept
		for _, s := range swaps {
			if s.FromBecomesFree {
				setSchPlanPinState(&after, cid[s.Ref], s.FromPin, true, "")
			}
		}
		if err := after.Validate(); err != nil {
			return nil, fmt.Errorf("expected post-swap connectivity invalid: %w", err)
		}
	} else {
		out.Warnings = append(out.Warnings, "no --sch snapshot: pins, current nets and NC flags were not checked and the playbook has no connectivity checkpoints; pass --sch <page connectivity.json> (pcbpilot sch connectivity) before running it")
	}
	pinLabel := func(ref, pin, name string, st pinState) string {
		if name == "" {
			name = st.name
		}
		if name != "" {
			return fmt.Sprintf("%s.%s(%s)", ref, pin, name)
		}
		return ref + "." + pin
	}
	for _, s := range swaps {
		from, to := state(s.Ref, s.FromPin), state(s.Ref, s.ToPin)
		toWas := "(NC)"
		if sch == nil && !s.ToWasFree {
			toWas = "(swapped)"
		}
		if to.net != "" {
			toWas = to.net
		}
		out.Diff = append(out.Diff, fmt.Sprintf("- %s: %s", pinLabel(s.Ref, s.FromPin, s.FromName, from), s.Net))
		if s.FromBecomesFree {
			out.Diff = append(out.Diff, fmt.Sprintf("+ %s: (NC)", pinLabel(s.Ref, s.FromPin, s.FromName, from)))
		}
		out.Diff = append(out.Diff, fmt.Sprintf("- %s: %s", pinLabel(s.Ref, s.ToPin, s.ToName, to), toWas))
		out.Diff = append(out.Diff, fmt.Sprintf("+ %s: %s", pinLabel(s.Ref, s.ToPin, s.ToName, to), s.Net))
	}

	zero, stop := 0, false
	pb := &playbook{Version: 1, RequireFullExecution: true,
		Meta:     playbookMeta{Name: "Pin swap " + it.ID, Description: it.Title + " — generated offline from feedback.json; live-unverified"},
		Defaults: stepPolicy{Retry: &zero, ContinueOnError: &stop}, Steps: []playbookStep{}}
	if sch != nil {
		pb.Meta.Project, pb.Meta.Doc = sch.ProjectID, sch.DocumentID
		before := cloneSchPlanState(*sch)
		pb.Steps = append(pb.Steps, playbookStep{ID: "check-before", Action: "schematic.read", Payload: map[string]any{"includeCheck": false}, ExpectedConnectivity: &before})
	}
	for _, s := range swaps {
		pb.Steps = append(pb.Steps, playbookStep{ID: fmt.Sprintf("disconnect-%s-%s", s.Ref, s.FromPin), Action: "schematic.pin.disconnect",
			Payload: map[string]any{"designator": s.Ref, "pin": s.FromPin}})
	}
	for _, s := range swaps {
		if sch != nil && !state(s.Ref, s.ToPin).nc {
			continue
		}
		if sch == nil && !s.ToWasFree {
			continue
		}
		st := playbookStep{ID: fmt.Sprintf("clear-nc-%s-%s", s.Ref, s.ToPin), Action: "schematic.pin.set_no_connect",
			Payload: map[string]any{"designator": s.Ref, "pins": []string{s.ToPin}, "noConnected": false}}
		if sch != nil {
			st.Assert = map[string]string{"$.notApplied": "len==0"}
		} else {
			st.OnFail = "continue" // NC state unknown without --sch
		}
		pb.Steps = append(pb.Steps, st)
	}
	for _, s := range swaps {
		pb.Steps = append(pb.Steps, playbookStep{ID: fmt.Sprintf("connect-%s-%s", s.Ref, s.ToPin), Run: "sch autoconnect",
			Flags: map[string]any{"pin": s.Ref + ":" + s.ToPin, "net": s.Net, "kind": "net_label", "strict": true, "offset-min": 10, "offset-max": 80, "offset-step": 5, "offset-cap": 300}})
	}
	for _, s := range swaps {
		if !s.FromBecomesFree {
			continue
		}
		pb.Steps = append(pb.Steps, playbookStep{ID: fmt.Sprintf("set-nc-%s-%s", s.Ref, s.FromPin), Action: "schematic.pin.set_no_connect",
			Payload: map[string]any{"designator": s.Ref, "pins": []string{s.FromPin}, "noConnected": true}, Assert: map[string]string{"$.notApplied": "len==0"}})
	}
	pb.Steps = append(pb.Steps, playbookStep{ID: "save", Action: "schematic.save"})
	if sch != nil {
		pb.Steps = append(pb.Steps, playbookStep{ID: "check-after", Action: "schematic.read", Payload: map[string]any{"includeCheck": false}, ExpectedConnectivity: &after})
	}
	out.Playbook = pb
	return out, nil
}

func newSchPinSwapCmd(stdout, stderr io.Writer) *cobra.Command {
	var planPath, item, schPath, outPath string
	var dryRun bool
	c := &cobra.Command{
		Use:   "pin-swap",
		Short: "Compile a pin-swap item of feedback.json into a guarded sch apply playbook (offline; live-unverified)",
		Long: `Reads feedback.json from 'pcbpilot pcb auto run' / 'pcb feedback' and turns one
applyable item (mcu-pin-swap / connector-pin-swap) into a version-1 playbook for
'pcbpilot sch apply': disconnect the net marker on each old pin, clear NC on the
new pin, place a net label there (sch autoconnect --strict), mark vacated pins NC,
save. With --sch (the 'pcbpilot sch connectivity' snapshot of the page holding the
part) every swap is checked against the live pin/net/NC state and the playbook
gets before/after connectivity checkpoints.

Nothing is executed here. A pin swap changes the schematic: show the diff to the
user and wait for confirmation (same rule as a layout change); after 'sch apply'
run sch gate / connectivity, re-import to PCB, pcb pad-net-diff, then re-run
pcb auto run. A pin wired directly to another symbol (no stub+label) is not
handled by schematic.pin.disconnect — the playbook stops there; re-plan by hand.`,
		Example: `  pcbpilot sch pin-swap --plan out/feedback.json --item FB01 --dry-run
  pcbpilot sch pin-swap --plan out/feedback.json --item FB01 --sch u1-page.json --out swap.playbook.json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if planPath == "" || item == "" {
				return fmt.Errorf("--plan and --item are required")
			}
			raw, err := os.ReadFile(planPath)
			if err != nil {
				return err
			}
			var fb pcbauto.Feedback
			if err := json.Unmarshal(raw, &fb); err != nil {
				return fmt.Errorf("%s: %w", planPath, err)
			}
			if fb.SchemaVersion != 1 {
				return fmt.Errorf("%s: not a feedback.json (schemaVersion %d)", planPath, fb.SchemaVersion)
			}
			it, err := findFeedbackItem(&fb, item)
			if err != nil {
				return err
			}
			var sch *connectivity.Document
			if schPath != "" {
				sraw, err := os.ReadFile(schPath)
				if err != nil {
					return err
				}
				var d connectivity.Document
				if err := json.Unmarshal(sraw, &d); err != nil {
					return fmt.Errorf("%s: %w", schPath, err)
				}
				sch = &d
			}
			plan, err := buildPinSwapPlan(it, sch)
			if err != nil {
				return err
			}
			fmt.Fprintf(stderr, "pin-swap %s (%s), live-unverified — schematic diff:\n", it.ID, it.Title)
			for _, d := range plan.Diff {
				fmt.Fprintf(stderr, "  %s\n", d)
			}
			for _, w := range plan.Warnings {
				fmt.Fprintf(stderr, "warning: %s\n", w)
			}
			if it.ExpectedGain != nil {
				fmt.Fprintf(stderr, "expected gain (%s): %s\n", it.ExpectedGain.Method, it.ExpectedGain.Summary)
			}
			fmt.Fprintln(stderr, "next: user confirms the diff → pcbpilot sch apply <playbook> → sch gate / connectivity → PCB re-import → pcb pad-net-diff → pcb auto run")
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if dryRun {
				return enc.Encode(plan)
			}
			if outPath == "" {
				return enc.Encode(plan.Playbook)
			}
			f, err := os.Create(outPath)
			if err != nil {
				return err
			}
			defer f.Close()
			fe := json.NewEncoder(f)
			fe.SetIndent("", "  ")
			if err := fe.Encode(plan.Playbook); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "wrote %s (%d steps, not executed)\n", outPath, len(plan.Playbook.Steps))
			return nil
		},
	}
	c.Flags().StringVar(&planPath, "plan", "", "feedback.json (required)")
	c.Flags().StringVar(&item, "item", "", "feedback item id, e.g. FB01 (required)")
	c.Flags().StringVar(&schPath, "sch", "", "connectivity snapshot of the page holding the part (pcbpilot sch connectivity): validates the swap and adds before/after checkpoints")
	c.Flags().StringVar(&outPath, "out", "", "write the playbook here (default: print it)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print the diff, warnings and playbook as one JSON object; write nothing")
	return c
}
