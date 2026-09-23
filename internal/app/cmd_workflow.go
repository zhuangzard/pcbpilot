package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

// cmd_workflow.go — compatibility viewer/recorder for the former project-level
// workflow state machine. It may calculate the old next step for existing
// scripts, but normal actions never consult its result.

// newWorkflowCmd builds the `workflow` group.
func newWorkflowCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	var window string
	wf := &cobra.Command{
		Use:   "workflow",
		Short: "Deprecated project checklist: init / status / advance / confirm / reset / pages",
		Long: `Compatibility interface for the former per-project PCB workflow.

Historical stages:
  imported → placement_ready → placement_confirmed → outline_confirmed
           → pre_route_passed → routing_authorized

No stage is an execution permission. Route, placement, outline and raw typed
actions run from live project data regardless of these records. Mutations no
longer cascade through confirmations, and legacy --force options are no-ops.
Use connectivity, geometric checks, DRC and save/reload/readback as evidence.

State lives at ~/.pcbpilot/workflow/<project>.json (PCBPILOT_WORKFLOW_DIR
to override). Existing scripts may inspect or append it; ` + "`status`" + ` and
` + "`advance`" + ` describe only the deprecated checklist and do not control
other commands.`,
	}
	wf.PersistentFlags().StringVar(&window, "window", "", "EasyEDA window ID (else use --project)")

	wf.AddCommand(newWorkflowInitCmd(cfg, &window, stdout, stderr))
	wf.AddCommand(newWorkflowStatusCmd(cfg, &window, stdout, stderr))
	wf.AddCommand(newWorkflowAdvanceCmd(cfg, &window, stdout, stderr))
	wf.AddCommand(newWorkflowConfirmCmd(cfg, &window, stdout, stderr))
	wf.AddCommand(newWorkflowResetCmd(cfg, &window, stdout))
	wf.AddCommand(newWorkflowPagesCmd(cfg, &window, stdout, stderr))
	return wf
}

// workflowFacts is what the live document actually contains — the ground truth
// the marker is reconciled against.
type workflowFacts struct {
	Components  int  `json:"components"`
	OutlineSegs int  `json:"outlineSegments"`
	RoutedLines int  `json:"routedLines"`
	Reachable   bool `json:"reachable"`
}

// pullWorkflowFacts reads the live document counts (best-effort per field).
func pullWorkflowFacts(cfg *appConfig, window string) (workflowFacts, error) {
	var f workflowFacts
	res, err := requestAction(cfg, "pcb.components.list", window, nil)
	if err != nil {
		return f, err
	}
	f.Reachable = true
	if comps, ok := res.Result["components"].([]any); ok {
		f.Components = len(comps)
	}
	if ores, oerr := requestAction(cfg, "pcb.outline.get", window, nil); oerr == nil {
		f.OutlineSegs = int(asFloat(ores.Result["outline"])) + int(asFloat(ores.Result["segments"])) + int(asFloat(ores.Result["arcs"]))
	}
	if lres, lerr := requestAction(cfg, "pcb.line.list", window, nil); lerr == nil {
		if lines, ok := lres.Result["lines"].([]any); ok {
			f.RoutedLines = len(lines)
		} else {
			f.RoutedLines = int(asFloat(lres.Result["count"]))
		}
	}
	return f, nil
}

// workflowNext computes the single next command for the current state + facts.
// This is the "default continue" the issue asks for: whatever stage the user
// cut in at, the flow tells them (and the agent) exactly what comes next.
func workflowNext(st *pcbStageState, f workflowFacts) (next, why string) {
	switch {
	case st.Assembly == nil:
		return "pcbpilot pcb stage set-assembly --profile hand-solder|reflow",
			"assembly profile not set (P2 precondition, issue #99)"
	case f.Reachable && f.Components == 0:
		return "pcbpilot pcb import-changes",
			"the PCB has no components yet (P1)"
	case !st.Has(stagePlacementConfirmed):
		if st.Layout == nil || st.Layout.TightPairs != 0 || st.Layout.AccessBlocked != 0 {
			return "pcbpilot pcb layout-lint --gate",
				"legacy P2 record has no compatible layout diagnostic snapshot"
		}
		return "pcbpilot pcb stage confirm-layout --note \"...\"",
			"legacy P2 placement record is absent (does not block PCB actions)"
	case f.Reachable && f.OutlineSegs == 0:
		return "pcbpilot pcb outline-fit",
			"no board outline yet (P3)"
	case !st.Has(stageOutlineConfirmed):
		return "pcbpilot pcb stage confirm-outline --note \"...\"",
			"legacy P3 outline record is absent (does not block PCB actions)"
	case !st.Has(stagePreRoutePassed):
		return "pcbpilot pcb layout-lint --gate",
			"legacy P6 diagnostic record is absent (does not block routing)"
	case f.RoutedLines == 0:
		return "pcbpilot pcb route-short   (or autoroute)",
			"legacy checklist reached the routing step (P7; diagnostic only)"
	case !st.Has(stagePostRouteChecked):
		return "pcbpilot workflow advance   (runs the pcb-check gate)",
			"board is routed but the post-route check gate (布完必查) has not passed (P7.9): ERRORs + power-not-poured + width-under-spec must be zero"
	default:
		return "pcbpilot pcb silk-align && pcb drc && pcb save   (P9/P10 delivery)",
			"post-route check passed — proceed to silkscreen / native DRC / save / export"
	}
}

// runPostRouteCheckGate runs the pcb-check audit as the post_route_checked
// mechanical gate (布完必查). Gate criteria: zero hard ERRORs, zero
// power-not-poured, zero width-under-spec — the power-discipline trio the
// 5V-thin-track regression proved agents ignore when it is only a WARN. Other
// WARN/INFO findings (coupling, fiducial, silk…) are reported but do NOT block:
// gating on them would make the gate impossible on dense boards. On pass the
// stage is confirmed with a CheckGateSummary snapshot; on fail the offending
// findings (with their 规范 § references) are printed and the stage stays
// unconfirmed.
//
// Documented exemptions — without them the tools contradict each other and the
// flow deadlocks; exempt findings are still printed, flagged with their reason:
//   - issue #114: a power net that `pcb power-planes` itself decided to route
//     as tracks (no inner plane left for it — State.PowerTracksNets);
//   - issue #117: a power net power-planes poured onto a layer it then flipped
//     to 内电层/PLANE (State.PlanePouredNets) — pour.list cannot see PLANE-layer
//     pours after a reload (#110), so re-flagging it would demand re-running
//     the very command that poured it;
//   - INFO-level power-not-poured (the check's own #110 plane-invisibility
//     downgrade) never blocks.
func runPostRouteCheckGate(cfg *appConfig, window, project string, st *pcbStageState, f workflowFacts, stdout, stderr io.Writer) error {
	rep, err := gatherPcbCheckReport(cfg, window, 0, nil, stderr)
	if err != nil {
		return fmt.Errorf("post-route check gate: %w", err)
	}
	exemptReason := func(fd pcbCheckFinding) string {
		switch {
		case st.IsPowerTracksNet(fd.Net):
			return "exempt: power-planes routed it as tracks (#114)"
		case st.IsPlanePouredNet(fd.Net):
			return "exempt: power-planes poured it into an inner PLANE — invisible to pour.list after reload (#110/#117); verify with `pcb drc` Connection=0"
		case fd.Level == "INFO":
			return "non-blocking INFO (#110 plane-invisibility downgrade)"
		default:
			return ""
		}
	}
	pnpBlocking, pnpExempt := splitPowerNotPoured(rep.Findings, func(net string) bool {
		return st.IsPowerTracksNet(net) || st.IsPlanePouredNet(net)
	})
	for _, fd := range pnpExempt {
		fmt.Fprintf(stderr, "   %-5s %-17s %s  (%s)\n",
			"INFO", fd.Type, fd.Message, exemptReason(fd))
	}
	blocking := rep.Summary.Errors + len(pnpBlocking) + rep.Summary.WidthUnderSpec
	if blocking > 0 {
		fmt.Fprintf(stderr, "❌ post-route check gate FAILED — %d blocking finding(s) (ERROR=%d powerNotPoured=%d widthUnderSpec=%d):\n",
			blocking, rep.Summary.Errors, len(pnpBlocking), rep.Summary.WidthUnderSpec)
		for _, fd := range rep.Findings {
			if fd.Type == "power-not-poured" && exemptReason(fd) != "" {
				continue // already printed above as exempt
			}
			if fd.Level == "ERROR" || fd.Type == "power-not-poured" || fd.Type == "width-under-spec" {
				fmt.Fprintf(stderr, "   %-5s %-17s %s\n", fd.Level, fd.Type, fd.Message)
			}
		}
		return errActionFailed
	}
	// The snapshot records the EFFECTIVE (post-exemption) power-not-poured count —
	// what the gate actually judged on.
	st.Check = &pcbCheckGateSummary{
		Errors: rep.Summary.Errors, Warnings: rep.Summary.Warnings,
		WidthUnderSpec: rep.Summary.WidthUnderSpec, PowerNotPoured: len(pnpBlocking),
		Tracks: f.RoutedLines, At: time.Now().Format(time.RFC3339),
	}
	note := fmt.Sprintf("pcb check: 0 blocking (warnings=%d, tracks=%d)", rep.Summary.Warnings, f.RoutedLines)
	if len(pnpExempt) > 0 {
		note += fmt.Sprintf(", %d power-not-poured exempt (power-planes verdicts / plane invisibility)", len(pnpExempt))
	}
	st.Confirm(stagePostRouteChecked, "gate-pass", note)
	if err := savePcbStageState(st); err != nil {
		return fmt.Errorf("persist post_route_checked: %w", err)
	}
	fmt.Fprintf(stderr, "✓ post-route check gate passed (0 ERROR / 0 power-not-poured / 0 width-under-spec; %d non-blocking warning(s)) — post_route_checked confirmed\n",
		rep.Summary.Warnings)
	return nil
}

// reconcileWorkflow re-syncs the marker with the live document: fingerprint
// drift auto-invalidates stale confirmations; obvious inconsistencies are
// reported. It never auto-CONFIRMS anything — human sign-offs stay human.
func reconcileWorkflow(cfg *appConfig, window string, st *pcbStageState, f workflowFacts) (notes []string) {
	drift, derr := verifyStageFingerprints(cfg, window, st)
	if derr != nil {
		notes = append(notes, fmt.Sprintf("fingerprint verify failed: %v", derr))
		return notes
	}
	if len(drift) > 0 {
		_ = savePcbStageState(st)
		notes = append(notes, drift...)
	}
	// placement_ready is mechanical (parts exist) — safe to auto-sync both ways.
	if f.Components > 0 && !st.Has(stagePlacementReady) {
		st.Confirm(stagePlacementReady, "reconcile", fmt.Sprintf("%d components on board", f.Components))
		_ = savePcbStageState(st)
		notes = append(notes, fmt.Sprintf("placement_ready auto-set (%d components on board)", f.Components))
	}
	if f.Reachable && f.Components == 0 {
		if cleared := st.InvalidateFrom(stagePlacementReady, "reconcile: board has no components"); len(cleared) > 0 {
			_ = savePcbStageState(st)
			notes = append(notes, "board is empty — cleared "+strings.Join(stageNames(cleared), ", "))
		}
	}
	if f.Reachable && f.OutlineSegs == 0 && st.Has(stageOutlineConfirmed) {
		if cleared := st.InvalidateFrom(stageOutlineConfirmed, "reconcile: board outline is gone"); len(cleared) > 0 {
			_ = savePcbStageState(st)
			notes = append(notes, "outline is gone — cleared "+strings.Join(stageNames(cleared), ", "))
		}
	}
	// Routing that predates the historical record is informational only.
	if f.RoutedLines > 0 && !st.Has(stageRoutingAuthorized) {
		notes = append(notes, fmt.Sprintf(
			"%d routed line(s) exist while the legacy routing checklist is incomplete — board data is kept; inspect live copper and DRC",
			f.RoutedLines))
	}
	// Post-route record drift (WEAK: track count only — there is no routing
	// fingerprint yet). Copper changed since the historical snapshot, so refresh
	// that compatibility record when the user explicitly asks to reconcile.
	if st.Has(stagePostRouteChecked) && st.Check != nil && st.Check.Tracks != f.RoutedLines {
		if cleared := st.InvalidateFrom(stagePostRouteChecked,
			fmt.Sprintf("routed copper changed since the check gate (%d → %d tracks)", st.Check.Tracks, f.RoutedLines)); len(cleared) > 0 {
			_ = savePcbStageState(st)
			notes = append(notes, fmt.Sprintf(
				"routing changed since the post-route check (%d → %d tracks) — `workflow advance` will re-run the check gate",
				st.Check.Tracks, f.RoutedLines))
		}
	}
	return notes
}

func stageNames(stages []pcbStage) []string {
	out := make([]string, len(stages))
	for i, s := range stages {
		out[i] = string(s)
	}
	return out
}

// ── init ────────────────────────────────────────────────────────────────────

func newWorkflowInitCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize (or restart) the deprecated project checklist",
		Long: `Create a fresh compatibility record for the project (or wipe an existing
one back to 'imported'). This does not initialize, authorize or reset the board.`,
		Args:    cobra.NoArgs,
		Example: `  pcbpilot workflow init --project ceshi`,
		RunE: func(cmd *cobra.Command, args []string) error {
			project := stageKeyBestEffort(cfg, *window)
			if strings.TrimSpace(project) == "" {
				return fmt.Errorf("workflow init needs a project identity: pass --project or connect a window")
			}
			st, err := loadPcbStageState(project)
			if err != nil {
				// init may recover from a corrupt state file: start fresh.
				fmt.Fprintf(stderr, "⚠️  existing state unreadable (%v) — starting fresh\n", err)
				st = &pcbStageState{Project: project}
			}
			st.InvalidateFrom(stagePlacementReady, "workflow init")
			st.Confirm(stageImported, "init", "workflow init")
			if err := savePcbStageState(st); err != nil {
				return err
			}
			fmt.Fprintf(stderr, "✓ workflow initialized for %q → %s\n", project, workflow.Path(project))
			facts, ferr := pullWorkflowFacts(cfg, *window)
			if ferr == nil {
				next, why := workflowNext(st, facts)
				fmt.Fprintf(stdout, "next: %s\n  (%s)\n", next, why)
			}
			return nil
		},
	}
}

// ── status ──────────────────────────────────────────────────────────────────

func newWorkflowStatusCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var asJSON, reconcile bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show deprecated checklist state; --reconcile compares it with the live document",
		Long: `Print the persisted compatibility state and its historical next step.

Also renders the layout-quality snapshot confirm-layout recorded (#167):
overall score, weakest dimensions, skipped-dimension count and timestamp.

--reconcile additionally pulls the LIVE document (component count, outline,
routed lines), re-verifies the confirmation fingerprints, auto-invalidates
historical records that drifted, and reports inconsistencies. This changes only
the checklist file; it never changes or blocks the board. It also re-scores the layout and diffs it per dimension
against the stored quality snapshot — a dimension that dropped ≥5 points is
flagged; one that went scored→skipped is reported as "lost measurability",
never as a drop to 0 (没测≠没变). Quality findings are advisory only and
never affect the exit code. Use it whenever you (or anyone) edited outside
the historical checklist.`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot workflow status --project ceshi
  pcbpilot workflow status --project ceshi --reconcile`,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, uuid, _ := resolveStageIdentity(cfg, *window)
			if strings.TrimSpace(project) == "" {
				project = cfg.project
			}
			st, err := loadPcbStageState(project)
			if err != nil {
				return err
			}
			// 身份绑定 + 同名重建体检:状态文件按名字分文件,同名重建会让几个工程
			// 的页记账堆进一份文件,而症状是**静默的错误数据**(跨页匹配的分母被
			// 污染),不是报错 —— 所以必须在这里主动说出来。只报不删。
			bind := st.Bind(uuid)
			reportStateIdentity(project, bind, stderr)
			// 质量快照必须在 reconcile 前取:reconcileWorkflow 里的 InvalidateFrom
			// 会把 st.Layout(连同快照)在内存里清掉,而 diff 的意义正是「与上次
			// 签字时比」(#167 消费侧)。
			prevQuality := stateQuality(st)
			var facts workflowFacts
			var notes, qualityNotes []string
			if reconcile {
				facts, err = pullWorkflowFacts(cfg, *window)
				if err != nil {
					return fmt.Errorf("reconcile needs a connected window: %w", err)
				}
				notes = reconcileWorkflow(cfg, *window, st, facts)
				qualityNotes = reconcileQualityNotes(cfg, *window, prevQuality)
			}
			gate := checkRouteGate(st, false, false, "")
			next, why := workflowNext(st, facts)
			if asJSON {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				out := map[string]any{
					"project":           st.Project,
					"confirmed":         st.Confirmed,
					"assembly":          st.Assembly,
					"layoutGate":        st.Layout,
					"routeAllowed":      gate.Allowed,
					"missing":           gate.Missing,
					"next":              next,
					"nextReason":        why,
					"compatibilityOnly": true,
				}
				if reconcile {
					out["facts"] = facts
					out["reconcileNotes"] = notes
					out["qualityNotes"] = qualityNotes
				}
				return enc.Encode(out)
			}
			fmt.Fprintf(stdout, "legacy workflow checklist — project %q\n", stageProjectLabel(project))
			for _, s := range pcbStageOrder {
				mark := "○"
				if st.Has(s) {
					mark = "●"
				}
				fmt.Fprintf(stdout, "  %s %s\n", mark, s)
			}
			// 上次 confirm-layout 落的多维质量快照(#167 消费侧):没有也要说一行,
			// 否则 write-only 的快照等于没有。
			for _, l := range qualitySnapshotLines(prevQuality) {
				fmt.Fprintf(stdout, "  %s\n", l)
			}
			if reconcile {
				fmt.Fprintf(stdout, "  document: %d components, %d outline segment(s), %d routed line(s)\n",
					facts.Components, facts.OutlineSegs, facts.RoutedLines)
				for _, n := range notes {
					fmt.Fprintf(stdout, "  ⚠️  %s\n", n)
				}
				// 快照 vs 实时的逐维 diff(行内自带 ⚠️/·/✓ 标记):只展示+提示,
				// 不影响退出码 —— status 不是新的门。
				for _, n := range qualityNotes {
					fmt.Fprintf(stdout, "  %s\n", n)
				}
			}
			if gate.Allowed {
				fmt.Fprintln(stdout, "  legacy checklist: complete for routing (diagnostic only)")
			} else {
				fmt.Fprintf(stdout, "  legacy checklist: incomplete — missing %s (does not block actions)\n", strings.Join(gate.Missing, ", "))
			}
			fmt.Fprintf(stdout, "legacy next: %s\n  (%s)\n", next, why)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit as JSON")
	c.Flags().BoolVar(&reconcile, "reconcile", false, "compare with the live document and refresh only the deprecated checklist record")
	return c
}

// ── advance ─────────────────────────────────────────────────────────────────

func newWorkflowAdvanceCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var minScore, maxCrossings int
	c := &cobra.Command{
		Use:   "advance",
		Short: "Advance the deprecated checklist and print its historical next step",
		Long: `Walk the deprecated checklist forward for compatibility:

  1. reconcile the marker with the live document (fingerprints, counts);
  2. if the next step is MECHANICAL (the layout-lint routability gate), run it;
  3. print the exact next command.

Historical layout/outline records are never auto-created. This command may exit
non-zero when its own legacy checklist is incomplete; that exit code does not
authorize or refuse any layout, outline, routing or typed action.

Use direct factual checks for new automation rather than this compatibility exit
code.`,
		Args:    cobra.NoArgs,
		Example: `  pcbpilot workflow advance --project ceshi`,
		RunE: func(cmd *cobra.Command, args []string) error {
			project, err := resolveStageProject(cfg, *window)
			if err != nil {
				return fmt.Errorf("workflow advance needs a connected window: %w", err)
			}
			st, err := loadPcbStageState(project)
			if err != nil {
				return err
			}
			facts, err := pullWorkflowFacts(cfg, *window)
			if err != nil {
				return err
			}
			for _, n := range reconcileWorkflow(cfg, *window, st, facts) {
				fmt.Fprintf(stderr, "⚠️  %s\n", n)
			}

			// Mechanical acceptance: the routability gate can be run here. It
			// persists pre_route_passed on pass (see runPcbLayoutLint).
			runnableGate := st.Assembly != nil && facts.Components > 0 &&
				(!st.Has(stagePreRoutePassed) || st.Layout == nil)
			if runnableGate {
				fmt.Fprintf(stderr, "→ running `pcb layout-lint --gate` (min-score %d, max-crossings %d)\n", minScore, maxCrossings)
				if lerr := runPcbLayoutLint(cfg, *window, 0, false, pcbLayoutGateOpts{
					gate: true, project: project, minScore: minScore, maxCrossings: maxCrossings,
				}, stdout, stderr); lerr != nil {
					fmt.Fprintf(stderr, "❌ gate failed: %v\n", lerr)
					// fall through — next below will point at fixing the layout
				}
				// reload: the gate may have confirmed pre_route_passed
				if reloaded, rerr := loadPcbStageState(project); rerr == nil {
					st = reloaded
				}
			}

			// Mechanical acceptance #2: the post-route check gate (布完必查).
			// Runs once the board is routed and every earlier stage is in.
			//
			// A gate that REJECTS (blocking findings) and a gate that cannot RUN
			// (connection/report failure) are both hard stops: advance must exit
			// non-zero or a `set -e` script / CI loop walks straight on with a
			// sick board — exactly what issue #113 caught. The gate prints its
			// own diagnosis (findings, or the ❌ below), so the deferred error is
			// the silent errActionFailed; `next` is still printed first because
			// the caller needs to know what to fix.
			gateBlocked := false
			if st.Has(stagePreRoutePassed) && facts.RoutedLines > 0 && !st.Has(stagePostRouteChecked) {
				fmt.Fprintf(stderr, "→ running the post-route `pcb check` gate (ERROR / power-not-poured / width-under-spec must be 0)\n")
				if gerr := runPostRouteCheckGate(cfg, *window, project, st, facts, stdout, stderr); gerr != nil {
					gateBlocked = true
					if gerr != errActionFailed {
						fmt.Fprintf(stderr, "❌ post-route check gate could not run: %v\n", gerr)
					}
				}
				if reloaded, rerr := loadPcbStageState(project); rerr == nil {
					st = reloaded
				}
			}

			next, why := workflowNext(st, facts)
			fmt.Fprintf(stdout, "next: %s\n  (%s)\n", next, why)
			if workflowAdvanceBlocked(gateBlocked, next) {
				return errActionFailed
			}
			return nil
		},
	}
	c.Flags().IntVar(&minScore, "min-score", 60, "minimum routability score for the gate")
	c.Flags().IntVar(&maxCrossings, "max-crossings", 8, "maximum ratline crossings for the gate (-1 = unlimited)")
	return c
}

// workflowAdvanceBlocked decides `advance`'s exit code (issue #113): the flow is
// blocked — so the command must exit NON-ZERO — when either
//
//	(a) a mechanical gate rejected or could not run (gateBlocked), or
//	(b) the next step is a human sign-off advance must never self-approve.
//
// Both can hold at once, so this is one OR, not two return paths that could
// cancel out. Pure, so the contract scripts rely on (`set -e` must stop here) is
// unit-testable without a live window.
func workflowAdvanceBlocked(gateBlocked bool, next string) bool {
	if gateBlocked {
		return true
	}
	return strings.Contains(next, "confirm-layout") ||
		strings.Contains(next, "confirm-outline") ||
		strings.Contains(next, "set-assembly")
}

// ── confirm ─────────────────────────────────────────────────────────────────

func newWorkflowConfirmCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var note string
	c := &cobra.Command{
		Use:   "confirm <layout|outline>",
		Short: "Record a historical layout/outline checklist entry (no execution permission)",
		Args:  cobra.ExactArgs(1),
		Example: `  pcbpilot workflow confirm layout --project ceshi --note "USB-C out, antenna top"
  pcbpilot workflow confirm outline --project ceshi --note "40×25mm"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			switch strings.ToLower(strings.TrimSpace(args[0])) {
			case "layout":
				return runStageConfirmLayout(cfg, *window, note, stderr)
			case "outline":
				return runStageConfirmOutline(cfg, *window, note, stderr)
			default:
				return fmt.Errorf("confirm target must be layout or outline (got %q)", args[0])
			}
		},
	}
	c.Flags().StringVar(&note, "note", "", "what was reviewed/confirmed (recorded in the audit trail)")
	return c
}

// ── reset ───────────────────────────────────────────────────────────────────

func newWorkflowResetCmd(cfg *appConfig, window *string, stdout io.Writer) *cobra.Command {
	var from string
	var all bool
	c := &cobra.Command{
		Use:     "reset",
		Short:   "Clear deprecated checklist entries (or --all)",
		Long:    "Clear only the historical workflow record. This does not change, reset or unlock the PCB.",
		Args:    cobra.NoArgs,
		Example: `  pcbpilot workflow reset --all --project ceshi`,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := loadPcbStageState(stageKeyBestEffort(cfg, *window))
			if err != nil {
				return err
			}
			var cleared []pcbStage
			if all {
				cleared = st.InvalidateFrom(stagePlacementReady, "workflow reset --all")
			} else {
				target := pcbStage(strings.TrimSpace(from))
				if pcbStageRank(target) < 0 {
					return fmt.Errorf("--from must be one of %v (or use --all)", pcbStageOrder)
				}
				cleared = st.InvalidateFrom(target, "workflow reset --from "+from)
			}
			if err := savePcbStageState(st); err != nil {
				return err
			}
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"ok": true, "cleared": stageNames(cleared), "compatibilityOnly": true})
		},
	}
	c.Flags().StringVar(&from, "from", "", "stage to clear from (inclusive)")
	c.Flags().BoolVar(&all, "all", false, "clear all confirmations")
	return c
}
