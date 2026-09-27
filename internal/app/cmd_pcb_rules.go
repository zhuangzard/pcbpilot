package app

// cmd_pcb_rules.go — `pcb rules apply|check --intent intent.json`: push the
// schematic's electrical intent into the host's own rule system (native net
// classes, per-class track/clearance/via rules, differential pairs) BEFORE
// layout/routing, so EasyEDA's DRC and router enforce the same numbers the
// intent was derived with. Only existing typed actions are used:
//
//	read : pcb.nets.list, pcb.config.get, pcb.constraint.list
//	write: pcb.net_class.create, pcb.net_class.add_nets, pcb.drc.rules.set
//	       (complete rule configuration + netRules, rollback + exact readback),
//	       pcb.differential_pair.create
//
// Idempotent: the plan is a diff of desired vs live; only differences are
// written. After writing, a fresh read must plan zero writes or the command
// exits non-zero (unverified).

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// intentRulesCaller is the action transport (requestAction in production).
type intentRulesCaller func(action string, payload any) (map[string]any, error)

type intentRulesWrite struct {
	Action   string `json:"action"`
	Target   string `json:"target"`
	Verified bool   `json:"verified"`
	Error    string `json:"error,omitempty"`
}

type intentRulesReport struct {
	Mode      string             `json:"mode"` // dry-run | apply | check
	Intent    string             `json:"intent"`
	IntentSHA string             `json:"intentSha256"`
	Plan      *intentRulesPlan   `json:"plan"`
	Writes    []intentRulesWrite `json:"writes"`
	Final     *intentRulesPlan   `json:"final,omitempty"`
	Verified  bool               `json:"verified"`
	Status    string             `json:"status"`
	Next      []string           `json:"next,omitempty"`
}

func readIntentLiveState(call intentRulesCaller) (intentRulesSnapshot, map[string]bool, []intentLiveDiffPair, error) {
	var snap intentRulesSnapshot
	netsRes, err := call("pcb.nets.list", nil)
	if err != nil {
		return snap, nil, nil, fmt.Errorf("pcb.nets.list: %w", err)
	}
	rawNets, ok := netsRes["nets"].([]any)
	if !ok {
		return snap, nil, nil, fmt.Errorf("pcb.nets.list: result has no nets array")
	}
	nets := map[string]bool{}
	for _, n := range rawNets {
		switch v := n.(type) {
		case string:
			nets[v] = true
		case map[string]any:
			if s := asString(v["net"]); s != "" {
				nets[s] = true
			} else if s := asString(v["name"]); s != "" {
				nets[s] = true
			}
		}
	}
	cfgRes, err := call("pcb.config.get", nil)
	if err != nil {
		return snap, nil, nil, fmt.Errorf("pcb.config.get: %w", err)
	}
	b, _ := json.Marshal(cfgRes)
	if err := json.Unmarshal(b, &snap); err != nil || snap.RuleConfiguration == nil || snap.Classes == nil || snap.NetRules == nil {
		return snap, nil, nil, fmt.Errorf("pcb.config.get: ruleConfiguration/classes/netRules unavailable")
	}
	conRes, err := call("pcb.constraint.list", nil)
	if err != nil {
		return snap, nil, nil, fmt.Errorf("pcb.constraint.list: %w", err)
	}
	var pairs []intentLiveDiffPair
	if raw, ok := conRes["differentialPairs"].([]any); ok {
		b, _ := json.Marshal(raw)
		_ = json.Unmarshal(b, &pairs)
	}
	return snap, nets, pairs, nil
}

func planFromLive(in *designIntent, call intentRulesCaller) (*intentRulesPlan, error) {
	snap, nets, pairs, err := readIntentLiveState(call)
	if err != nil {
		return nil, err
	}
	return planIntentRules(in, snap, nets, pairs), nil
}

var errIntentRulesUnverified = errors.New("intent rules not verified")

// runIntentRules executes check / dry-run / apply and always fills report.
func runIntentRules(in *designIntent, mode string, strict bool, call intentRulesCaller, warn io.Writer) (*intentRulesReport, error) {
	rep := &intentRulesReport{Mode: mode, IntentSHA: in.sourceSHA, Writes: []intentRulesWrite{}}
	plan, err := planFromLive(in, call)
	if err != nil {
		rep.Status = "read-failed"
		return rep, err
	}
	rep.Plan = plan
	if len(plan.Conflicts) > 0 {
		rep.Status = "conflict"
		rep.Next = []string{"resolve every plan.conflicts item (intent or live state), then re-run; nothing was written"}
		return rep, fmt.Errorf("%d conflict(s) in intent vs live rules; nothing written", len(plan.Conflicts))
	}
	switch mode {
	case "check":
		rep.Verified = plan.PendingWrites == 0 && (!strict || len(plan.Unsupported) == 0)
		if rep.Verified {
			rep.Status = "in-sync"
			return rep, nil
		}
		rep.Status = "drift"
		if plan.PendingWrites == 0 {
			rep.Status = "unsupported-items"
		}
		rep.Next = []string{"pcbpilot pcb rules apply --intent <intent.json> --dry-run", "pcbpilot pcb rules apply --intent <intent.json>"}
		return rep, fmt.Errorf("%w: %d pending write(s), %d unsupported item(s)", errIntentRulesUnverified, plan.PendingWrites, len(plan.Unsupported))
	case "dry-run":
		rep.Status = "planned"
		if plan.PendingWrites == 0 {
			rep.Status = "in-sync"
		}
		return rep, nil
	}

	// apply
	if plan.PendingWrites == 0 {
		rep.Final, rep.Verified, rep.Status = plan, true, "in-sync"
		return rep, nil
	}
	fail := func(w intentRulesWrite, err error) (*intentRulesReport, error) {
		w.Error = err.Error()
		rep.Writes = append(rep.Writes, w)
		rep.Status = "unverified"
		rep.Next = []string{"pcbpilot pcb config get (inspect actual state)", "pcbpilot pcb rules check --intent <intent.json>"}
		return rep, fmt.Errorf("%w: %s %s: %v", errIntentRulesUnverified, w.Action, w.Target, err)
	}
	for _, c := range plan.classOps() {
		action, payload := "pcb.net_class.create", map[string]any{"name": c.Name, "nets": c.Nets}
		if c.Action == "add_nets" {
			action, payload = "pcb.net_class.add_nets", map[string]any{"name": c.Name, "nets": c.Add}
		}
		w := intentRulesWrite{Action: action, Target: c.Name}
		res, err := call(action, payload)
		if err != nil {
			return fail(w, err)
		}
		if res["verified"] != true || res["partial"] == true {
			return fail(w, fmt.Errorf("readback did not confirm membership"))
		}
		w.Verified = true
		rep.Writes = append(rep.Writes, w)
	}
	current := plan
	if len(plan.classOps()) > 0 {
		if current, err = planFromLive(in, call); err != nil {
			return fail(intentRulesWrite{Action: "pcb.config.get", Target: "re-plan after class writes"}, err)
		}
		if len(current.Conflicts) > 0 || len(current.PendingBindings) > 0 || len(current.classOps()) > 0 {
			rep.Final = current
			return fail(intentRulesWrite{Action: "pcb.config.get", Target: "re-plan after class writes"},
				fmt.Errorf("class writes did not produce the expected netRules entries (%d conflicts, %d pending bindings)", len(current.Conflicts), len(current.PendingBindings)))
		}
	}
	if len(current.RuleChanges) > 0 || len(current.Bindings) > 0 {
		payload := map[string]any{"ruleConfiguration": current.ruleConfiguration}
		if len(current.Bindings) > 0 {
			payload["netRules"] = current.netRules
		}
		fmt.Fprintln(warn, knownBugPCBConfig)
		w := intentRulesWrite{Action: "pcb.drc.rules.set", Target: fmt.Sprintf("%d rule change(s), %d binding(s)", len(current.RuleChanges), len(current.Bindings))}
		res, err := call("pcb.drc.rules.set", payload)
		if err != nil {
			return fail(w, err)
		}
		if res["verified"] != true || res["partial"] == true || res["writeFailed"] == true {
			return fail(w, fmt.Errorf("rules readback not verified (rolledBack=%v)", res["rolledBack"]))
		}
		w.Verified = true
		rep.Writes = append(rep.Writes, w)
	}
	for _, d := range current.diffPairCreates() {
		w := intentRulesWrite{Action: "pcb.differential_pair.create", Target: d.Name}
		res, err := call("pcb.differential_pair.create", map[string]any{"name": d.Name, "positiveNet": d.Positive, "negativeNet": d.Negative})
		if err != nil {
			return fail(w, err)
		}
		if res["verified"] != true {
			return fail(w, fmt.Errorf("pair not confirmed by readback"))
		}
		w.Verified = true
		rep.Writes = append(rep.Writes, w)
	}
	final, err := planFromLive(in, call)
	if err != nil {
		return fail(intentRulesWrite{Action: "pcb.config.get", Target: "final readback"}, err)
	}
	rep.Final = final
	if final.PendingWrites != 0 || len(final.Conflicts) > 0 {
		rep.Status = "unverified"
		rep.Next = []string{"inspect final.ruleChanges/bindings: the host did not keep what was written"}
		return rep, fmt.Errorf("%w: fresh readback still plans %d write(s)", errIntentRulesUnverified, final.PendingWrites)
	}
	rep.Verified, rep.Status = true, "applied"
	rep.Next = []string{"pcbpilot pcb save", "pcbpilot doc reload", "pcbpilot pcb rules check --intent <intent.json>  (persistence proof)"}
	return rep, nil
}

func newPcbRulesCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	group := &cobra.Command{
		Use:   "rules",
		Short: "Apply / check schematic electrical intent as native PCB rules (net classes, widths, clearances, vias, diff pairs)",
		Long: `Map intent.json (the schematic electrical-intent contract) onto EasyEDA's own
rule system so native DRC and routing enforce it before layout/routing:

  netClasses[] + nets[].netClass → native net class (create / add missing nets;
                                   extra live members are preserved)
  class track width              → Physics.Track "PP_<class>" (copy of the default
                                   rule; default/min/max; outer vs inner layers)
  class clearance                → Safe Spacing "PP_<class>" (copper×copper cells
                                   raised to ≥ clearance; others = default)
  class via drill/diameter       → Via Size "PP_<class>"
  netRules                       → class + every member bound to those rules
  nets[].diffPair                → differential pair constraint; an unambiguous
                                   width/gap updates the single global
                                   Differential Pair rule (impedance is advisory)
  pairs[] (domain clearance)     → reported as unsupported/planned (net-by-net
                                   rule schema not captured live yet)

Desired values are recomputed from the default rule + intent on every run, so a
replay writes nothing. apply writes only differences, then re-reads and must
plan zero writes (else non-zero exit, "unverified"). Save + reload + check to
prove persistence.`,
	}
	for _, mode := range []string{"apply", "check"} {
		mode := mode
		var intentPath string
		var dryRun, strict bool
		c := &cobra.Command{
			Use:  mode,
			Args: cobra.NoArgs,
			Short: map[string]string{
				"apply": "Write the differences between intent and live rules (--dry-run: plan only)",
				"check": "Read-only: diff intent vs live rules; non-zero when anything would be written",
			}[mode],
			Example: map[string]string{
				"apply": "  pcbpilot pcb rules apply --intent intent.json --dry-run --project ceshi --doc PCB1\n  pcbpilot pcb rules apply --intent intent.json --project ceshi --doc PCB1",
				"check": "  pcbpilot pcb rules check --intent intent.json --project ceshi --doc PCB1\n  pcbpilot pcb rules check --intent intent.json --strict   # unsupported items also fail",
			}[mode],
			RunE: func(cmd *cobra.Command, args []string) error {
				in, err := loadDesignIntent(intentPath)
				if err != nil {
					return err
				}
				m := mode
				if mode == "apply" && dryRun {
					m = "dry-run"
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
				rep, runErr := runIntentRules(in, m, strict, call, stderr)
				rep.Intent = intentPath
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(rep); err != nil {
					return err
				}
				if runErr != nil {
					fmt.Fprintf(stderr, "pcb rules %s: %v\n", m, runErr)
					return errActionFailed
				}
				if rep.Plan != nil && len(rep.Plan.Unsupported) > 0 {
					fmt.Fprintf(stderr, "pcb rules %s: %d intent item(s) have no host rule (see plan.unsupported)\n", m, len(rep.Plan.Unsupported))
				}
				return nil
			},
		}
		c.Flags().StringVar(&intentPath, "intent", "", "intent.json (schematic electrical-intent contract) (required)")
		_ = c.MarkFlagRequired("intent")
		if mode == "apply" {
			c.Flags().BoolVar(&dryRun, "dry-run", false, "read live state and print the plan without writing")
		} else {
			c.Flags().BoolVar(&strict, "strict", false, "also fail when intent items have no host rule (plan.unsupported)")
		}
		group.AddCommand(c)
	}
	return group
}
