package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/internal/spec"
	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

// cmd_pcb_stage.go — compatibility access to the historical persisted PCB
// checklist. Records remain inspectable for existing scripts, but no stage value
// authorizes or blocks a PCB action. Current work uses live geometry,
// connectivity, DRC and save/reload/readback evidence instead.

// newPcbStageCmd builds the `pcb stage` group.
func newPcbStageCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	stage := &cobra.Command{
		Use:   "stage",
		Short: "Deprecated PCB checklist records: status / confirm-layout / confirm-outline / reset",
		Long: `Compatibility interface for historical PCB stage records.

These records used to act as routing permissions. They no longer authorize,
refuse, unlock or invalidate any typed action, composite command or raw daemon
request. Use live object readback, connectivity, geometry checks and DRC to make
the current decision. ` + "`pcb layout-lint --gate`" + ` is also a compatibility
diagnostic and does not write a routing permission.

Historical progression:
  imported → placement_ready → placement_confirmed → outline_confirmed
           → pre_route_passed → routing_authorized

Existing automation may continue to read or append these records. The record
commands validate their own historical ladder for compatibility, but their result
has no effect on layout, outline, routing, save or verification commands.`,
	}
	stage.AddCommand(newPcbStageStatusCmd(cfg, window, stdout))
	stage.AddCommand(newPcbStageSetAssemblyCmd(cfg, window, stdout, stderr))
	stage.AddCommand(newPcbStageConfirmTierCmd(cfg, window, stdout, stderr))
	stage.AddCommand(newPcbStageConfirmLayoutCmd(cfg, window, stdout, stderr))
	stage.AddCommand(newPcbStageConfirmOutlineCmd(cfg, window, stdout, stderr))
	stage.AddCommand(newPcbStageResetCmd(cfg, window, stdout))
	return stage
}

// newPcbStageConfirmTierCmd confirms one placement tier (issue #125): the
// design-flow ladder 档1 孔/结构件 → 档2 边缘接口件 → 档3 主芯片+RF → 档4 卫星件,
// mechanized. Each tier records its designators + a pose hash of exactly those
// parts, so tiers invalidate independently and confirm-layout can refuse to
// seal a placement whose tiers were never reviewed.
func newPcbStageConfirmTierCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var parts []string
	var note string
	var empty bool
	c := &cobra.Command{
		Use:   "confirm-tier <1|2|3|4>",
		Short: "Record one historical placement tier (does not lock parts or authorize actions)",
		Long: `Record a tier in the deprecated placement checklist:

  tier 1  孔/结构件        mounting holes & mechanical parts
  tier 2  边缘接口件        edge connectors — orientation MUST be user-confirmed
  tier 3  主芯片+RF        main ICs and RF (antenna keep-out reviewed)
  tier 4  卫星件           satellites (decoupling, pull-ups — auto-place output)

Tier N requires tiers 1..N-1 confirmed first. --parts names the tier's
designators (tier 4 may omit it: default = every part no earlier tier claimed);
--empty records a deliberately empty tier (e.g. a board with no RF). Each
confirm stores a pose hash of exactly that tier's parts — moving them later
invalidates that tier and everything after it, but NOT the earlier tiers.
` + "`confirm-layout`" + ` refuses until all 4 tiers are confirmed and every part is
claimed by a tier (--force <reason> bypasses that compatibility check). These
records do not lock parts and are not read by placement or routing commands.`,
		Args: cobra.ExactArgs(1),
		Example: `  pcbpilot pcb stage confirm-tier 1 --parts H1,H2,H3,H4 --note "M3 四角孔"
  pcbpilot pcb stage confirm-tier 2 --parts J1,USB1 --note "USB-C 开口朝外,用户已确认"
  pcbpilot pcb stage confirm-tier 3 --parts U1,U2 --note "天线 keepout 已留"
  pcbpilot pcb stage confirm-tier 4              # 其余全部 = 卫星件
  pcbpilot pcb stage confirm-tier 3 --empty --note "无 RF 器件"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			n := 0
			if _, err := fmt.Sscanf(strings.TrimSpace(args[0]), "%d", &n); err != nil || n < 1 || n > workflowTierCount {
				return fmt.Errorf("tier must be 1..%d", workflowTierCount)
			}
			return runStageConfirmTier(cfg, *window, n, parts, empty, note, stderr)
		},
	}
	c.Flags().StringArrayVar(&parts, "parts", nil, "designators this tier covers (comma-separated, repeatable); tier 4 default = all unclaimed parts")
	c.Flags().StringVar(&note, "note", "", "what was reviewed (orientation, keep-out …) — recorded in the audit trail")
	c.Flags().BoolVar(&empty, "empty", false, "record a deliberately empty tier (e.g. no RF parts on this board)")
	return c
}

// runStageConfirmTier implements the per-tier sign-off.
func runStageConfirmTier(cfg *appConfig, window string, n int, parts []string, empty bool, note string, stderr io.Writer) error {
	project, err := resolveStageProject(cfg, window)
	if err != nil {
		return fmt.Errorf("confirm-tier needs a connected window (the sign-off is fingerprinted against the live placement): %w", err)
	}
	st, err := loadPcbStageState(project)
	if err != nil {
		return err
	}
	for prev := 1; prev < n; prev++ {
		if st.Tier(prev) == nil {
			fmt.Fprintf(stderr, "❌ tier %d (%s) is not confirmed yet — the ladder is ordered: confirm it first (`pcb stage confirm-tier %d`)\n",
				prev, workflowTierName(prev), prev)
			return errActionFailed
		}
	}
	poses, err := pullLayoutPoses(cfg, window)
	if err != nil {
		return fmt.Errorf("confirm-tier: %w", err)
	}
	// Drift check on the ALREADY-confirmed earlier tiers: their sign-off must
	// still describe the live board before stacking a new tier on top.
	if drift := verifyTierFingerprints(st, poses); len(drift) > 0 {
		_ = savePcbStageState(st)
		for _, d := range drift {
			fmt.Fprintf(stderr, "⚠️  %s\n", d)
		}
		fmt.Fprintln(stderr, "❌ earlier tier(s) drifted — re-confirm them before this one")
		return errActionFailed
	}
	live := make([]string, 0, len(poses))
	for _, p := range poses {
		if strings.TrimSpace(p.Designator) != "" {
			live = append(live, p.Designator)
		}
	}
	claimed := st.ClaimedTiers()
	// Re-confirming tier n replaces its old claim set.
	for d, t := range claimed {
		if t == n {
			delete(claimed, d)
		}
	}
	designators, err := resolveTierParts(n, parts, empty, live, claimed)
	if err != nil {
		fmt.Fprintf(stderr, "❌ confirm-tier %d: %v\n", n, err)
		return errActionFailed
	}
	tc := &stageTierConfirm{At: nowRFC3339(), Note: note, Empty: empty}
	if !empty {
		hash, missing := tierPoseHash(poses, designators)
		if len(missing) > 0 { // cannot happen (resolved against live), belt-and-braces
			return fmt.Errorf("confirm-tier %d: parts vanished mid-flight: %s", n, strings.Join(missing, ","))
		}
		tc.Designators = designators
		tc.Hash = hash
	}
	// A (re-)confirmed tier invalidates everything stacked on top of it — later
	// tiers reviewed the board as it was, and the seal must be re-issued.
	st.InvalidateTiersFrom(n+1, fmt.Sprintf("tier %d (re)confirmed", n))
	st.ConfirmTier(n, tc)
	if err := savePcbStageState(st); err != nil {
		return err
	}
	if empty {
		fmt.Fprintf(stderr, "✓ tier %d (%s) confirmed EMPTY for %q\n", n, workflowTierName(n), project)
	} else {
		fmt.Fprintf(stderr, "✓ tier %d (%s) confirmed for %q — %d part(s): %s\n",
			n, workflowTierName(n), project, len(designators), strings.Join(designators, ","))
	}
	if n == workflowTierCount {
		if un := unclaimedParts(live, st.ClaimedTiers()); len(un) > 0 {
			fmt.Fprintf(stderr, "⚠️  %d part(s) claimed by NO tier: %s — the legacy confirm-layout record remains incomplete\n",
				len(un), strings.Join(un, ","))
		} else {
			fmt.Fprintln(stderr, "  all parts claimed — ready for `pcb stage confirm-layout`")
		}
	}
	return nil
}

// stageKeyBestEffort resolves the workflow state key: the live window's project
// identity when reachable, else the raw --project value. Read-only/offline
// commands (status / reset / set-assembly) use this so they still work without
// a connected window; the confirm commands require the live resolution (they
// need the window for fingerprints anyway).
func stageKeyBestEffort(cfg *appConfig, window string) string {
	if p, err := resolveStageProject(cfg, window); err == nil {
		return p
	}
	return cfg.project
}

// newPcbStageStatusCmd prints the current stage state (confirmed set + gate).
func newPcbStageStatusCmd(cfg *appConfig, window *string, stdout io.Writer) *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:     "status",
		Short:   "Show deprecated checklist records and their historical readiness calculation",
		Args:    cobra.NoArgs,
		Example: `  pcbpilot pcb stage status --project ceshi`,
		RunE: func(cmd *cobra.Command, args []string) error {
			project := stageKeyBestEffort(cfg, *window)
			st, err := loadPcbStageState(project)
			if err != nil {
				return err
			}
			gate := checkRouteGate(st, false, false, "")
			if asJSON {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"project":            st.Project,
					"confirmed":          st.Confirmed,
					"assembly":           st.Assembly,
					"placementTiers":     st.PlacementTiers,
					"layoutGate":         st.Layout,
					"layoutFingerprint":  st.LayoutFP,
					"outlineFingerprint": st.OutlineFP,
					"routeAllowed":       gate.Allowed,
					"missing":            gate.Missing,
					"compatibilityOnly":  true,
				})
			}
			fmt.Fprintf(stdout, "PCB legacy checklist — project %q\n", stageProjectLabel(project))
			if st.Assembly != nil {
				fmt.Fprintf(stdout, "  assembly: %s, min gap %.1fmil, large-pad access %.1fmil\n",
					st.Assembly.Profile, st.Assembly.MinGapMil, st.Assembly.LargePadAccessMil)
			} else {
				fmt.Fprintln(stdout, "  assembly: ❌ not set (`pcb stage set-assembly`)")
			}
			for _, s := range pcbStageOrder {
				mark := "○"
				if st.Has(s) {
					mark = "●"
				}
				fmt.Fprintf(stdout, "  %s %s\n", mark, s)
				// The tier ladder (issue #125) lives inside the placement stage.
				if s == stagePlacementConfirmed {
					for n := 1; n <= workflowTierCount; n++ {
						tc := st.Tier(n)
						switch {
						case tc == nil:
							fmt.Fprintf(stdout, "      ○ tier %d %s\n", n, workflowTierName(n))
						case tc.Empty:
							fmt.Fprintf(stdout, "      ● tier %d %s — EMPTY (%s)\n", n, workflowTierName(n), tc.Note)
						default:
							fmt.Fprintf(stdout, "      ● tier %d %s — %d part(s) @ %s\n", n, workflowTierName(n), len(tc.Designators), tc.At)
						}
					}
				}
			}
			if st.Layout != nil {
				fmt.Fprintf(stdout, "  historical layout diagnostic: score %d (%s), %d crossings, %d tight, %d access-blocked @ %s\n",
					st.Layout.Score, st.Layout.Verdict, st.Layout.CrossingCount,
					st.Layout.TightPairs, st.Layout.AccessBlocked, st.Layout.At)
			}
			if st.LayoutFP != nil {
				fmt.Fprintf(stdout, "  layout fingerprint: %d parts @ %s\n", st.LayoutFP.Count, st.LayoutFP.At)
			}
			if st.OutlineFP != nil {
				fmt.Fprintf(stdout, "  outline fingerprint: recorded @ %s\n", st.OutlineFP.At)
			}
			if gate.Allowed {
				fmt.Fprintln(stdout, "  legacy checklist: complete for routing (diagnostic only)")
			} else {
				fmt.Fprintf(stdout, "  legacy checklist: incomplete — missing %s (does not block actions)\n", strings.Join(gate.Missing, ", "))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit the state as JSON")
	return c
}

// newPcbStageSetAssemblyCmd persists assembly spacing metadata used as a default
// by legacy diagnostics and by placement spacing calculations.
func newPcbStageSetAssemblyCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var profile string
	var minGap, largePadGap float64
	c := &cobra.Command{
		Use:   "set-assembly",
		Short: "Persist assembly spacing metadata for compatibility diagnostics",
		Args:  cobra.NoArgs,
		Example: `  pcbpilot pcb stage set-assembly --profile hand-solder --min-gap 40 --large-pad-access 60 --project ceshi
  pcbpilot pcb stage set-assembly --profile reflow --project ceshi`,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile = strings.ToLower(strings.TrimSpace(profile))
			if profile != "hand-solder" && profile != "reflow" {
				return fmt.Errorf("--profile must be hand-solder or reflow")
			}
			if profile == "hand-solder" {
				if minGap == 0 {
					minGap = 40
				}
				if minGap < 40 {
					return fmt.Errorf("hand-solder --min-gap must be >=40mil")
				}
				if largePadGap == 0 {
					largePadGap = 60
				}
				if largePadGap < minGap {
					return fmt.Errorf("--large-pad-access must be >= --min-gap")
				}
			}
			st, err := loadPcbStageState(stageKeyBestEffort(cfg, *window))
			if err != nil {
				return err
			}
			st.Assembly = &pcbAssemblyProfile{Profile: profile, MinGapMil: minGap,
				LargePadAccessMil: largePadGap, At: time.Now().Format(time.RFC3339)}
			st.InvalidateFrom(stagePlacementConfirmed, "assembly profile changed")
			if err := savePcbStageState(st); err != nil {
				return err
			}
			fmt.Fprintf(stderr, "✓ assembly profile set: %s (min-gap %.1fmil, large-pad %.1fmil)\n",
				profile, minGap, largePadGap)
			return nil
		},
	}
	c.Flags().StringVar(&profile, "profile", "", "assembly process: hand-solder | reflow (required)")
	c.Flags().Float64Var(&minGap, "min-gap", 0, "general component gap in mil (hand-solder default/minimum 40)")
	c.Flags().Float64Var(&largePadGap, "large-pad-access", 0, "iron access corridor for large pads in mil (hand-solder default 60)")
	_ = c.MarkFlagRequired("profile")
	return c
}

// stageProjectLabel yields a display label when --project is empty.
func stageProjectLabel(p string) string {
	if strings.TrimSpace(p) == "" {
		return "(active window)"
	}
	return p
}

// newPcbStageConfirmLayoutCmd confirms placement_ready + placement_confirmed —
// the P2 human sign-off that the placement (bbox, edge-part orientation, antenna
// keep-out) is what the user wants. Requires a live window: the confirmation is
// pinned to the CURRENT placement by fingerprint, so a later out-of-band move
// (GUI drag / exec_js / another agent) is detected and invalidates it.
func newPcbStageConfirmLayoutCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var note, force, specPath string
	var minScore float64
	c := &cobra.Command{
		Use:   "confirm-layout",
		Short: "Record the placement in the deprecated checklist",
		Long: `Record a historical placement review. Before recording,
review the real bbox (` + "`pcb list --include-bbox`" + `), board size, edge-part
orientation (connector openings / antenna end facing out), antenna keep-out, and
the ` + "`pcb layout-lint`" + ` result. This sets placement_ready + placement_confirmed
and stores a fingerprint of the live placement (designator/x/y/rotation/layer):
legacy checklist readers can compare it with later geometry. This record does not
lock parts, authorize routing, or block any PCB command.`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot pcb stage confirm-layout --project ceshi --note "USB-C opening out, antenna at top edge"
  pcbpilot pcb stage confirm-layout --force "两件小板无分档必要" --project ceshi`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStageConfirmLayoutForced(cfg, *window, note, force, specPath, minScore, stderr)
		},
	}
	c.Flags().StringVar(&note, "note", "", "what was reviewed/confirmed (recorded in the audit trail)")
	c.Flags().StringVar(&force, "force", "", "bypass the deprecated record ladder with a reason; does not affect PCB actions")
	c.Flags().StringVar(&specPath, "spec", "", "S0 spec JSON — unlocks the intent dimensions of the recorded quality snapshot (flow-order, internal connectors)")
	c.Flags().Float64Var(&minScore, "min-score", 0,
		"skip creating this compatibility record when layout-score is below this (0 = always record).\n"+
			"Deliberately opt-in: the nine dimensions' weights and thresholds are still\n"+
			"calibration seeds (#167 LEARNING), and gating on an uncalibrated ruler would\n"+
			"manufacture more false blocks than it catches real problems")
	return c
}

// runStageConfirmLayout is the P2 sign-off implementation, shared by
// `pcb stage confirm-layout` and `workflow confirm layout` (no tier bypass).
func runStageConfirmLayout(cfg *appConfig, window, note string, stderr io.Writer) error {
	return runStageConfirmLayoutForced(cfg, window, note, "", "", 0, stderr)
}

func runStageConfirmLayoutForced(cfg *appConfig, window, note, forceReason, specPath string, minScore float64, stderr io.Writer) error {
	project, err := resolveStageProject(cfg, window)
	if err != nil {
		return fmt.Errorf("confirm-layout needs a connected window (the confirmation is fingerprinted against the live placement): %w", err)
	}
	st, err := loadPcbStageState(project)
	if err != nil {
		return err
	}
	poses, err := pullLayoutPoses(cfg, window)
	if err != nil {
		return fmt.Errorf("confirm-layout: %w", err)
	}
	if len(poses) == 0 {
		fmt.Fprintln(stderr, "❌ the active PCB has no components — nothing to confirm (run `pcb import-changes` first)")
		return errActionFailed
	}

	// Tier ladder gate FIRST (issue #125): the 档1-4 sign-offs happen during
	// placement and are the structural prerequisite for the final seal —
	// confirm-layout can no longer cover all four tiers in one unreviewed
	// stroke. --force <reason> bypasses (audited).
	if drift := verifyTierFingerprints(st, poses); len(drift) > 0 {
		_ = savePcbStageState(st)
		for _, d := range drift {
			fmt.Fprintf(stderr, "⚠️  %s\n", d)
		}
	}
	var tierGaps []string
	for n := 1; n <= workflowTierCount; n++ {
		if st.Tier(n) == nil {
			tierGaps = append(tierGaps, fmt.Sprintf("tier %d (%s)", n, workflowTierName(n)))
		}
	}
	var live []string
	for _, p := range poses {
		if strings.TrimSpace(p.Designator) != "" {
			live = append(live, p.Designator)
		}
	}
	unclaimed := unclaimedParts(live, st.ClaimedTiers())
	if len(tierGaps) > 0 || len(unclaimed) > 0 {
		if strings.TrimSpace(forceReason) == "" {
			if len(tierGaps) > 0 {
				fmt.Fprintf(stderr, "❌ placement tier ladder incomplete — missing %s; confirm each (`pcb stage confirm-tier <n> --parts …`), or --force <reason> (audited)\n",
					strings.Join(tierGaps, ", "))
			}
			if len(unclaimed) > 0 {
				fmt.Fprintf(stderr, "❌ %d part(s) claimed by NO tier: %s — assign them (`pcb stage confirm-tier 4` re-claims the rest), or --force <reason>\n",
					len(unclaimed), strings.Join(unclaimed, ","))
			}
			return errActionFailed
		}
		st.Confirm(stagePlacementConfirmed, "force", fmt.Sprintf("tier ladder bypassed: %s (missing %s; unclaimed %d)",
			forceReason, strings.Join(tierGaps, ","), len(unclaimed)))
		fmt.Fprintf(stderr, "⚠️  tier ladder bypassed on --force (reason: %s) — missing %s, %d unclaimed part(s); recorded in the audit trail\n",
			forceReason, strings.Join(tierGaps, ","), len(unclaimed))
	}

	if st.Assembly == nil {
		fmt.Fprintln(stderr, "❌ set the assembly profile first (`pcb stage set-assembly --profile hand-solder|reflow`)")
		return errActionFailed
	}
	if st.Layout == nil || st.Layout.TightPairs != 0 || st.Layout.MinGapMil < st.Assembly.MinGapMil {
		fmt.Fprintf(stderr, "❌ placement assembly gate not passed — run `pcb layout-lint --gate` using the persisted %s profile first\n", st.Assembly.Profile)
		return errActionFailed
	}
	if st.Layout.AccessBlocked != 0 {
		fmt.Fprintf(stderr, "❌ %d component(s) have no %.1fmil iron-access side — free at least one flank per part (`pcb layout-lint --gate` lists them), then re-gate\n",
			st.Layout.AccessBlocked, st.Layout.AccessMil)
		return errActionFailed
	}

	// 多维布局质量(#167)：拍一张快照记进状态，并把逐维分摊给签字的人看。
	//
	// 为什么**默认不拦**：九维的权重和阈值现在大多还是「待校准初值」（各维实现里
	// 都诚实标了），拿一把没校准的尺子做硬门，制造的假阻塞会比拦下的真问题多。
	// 这与项目已有的教训一致——降级 ≠ 删除，Tier2 不担契约硬门。
	// 想要严格的人显式传 --min-score；不传就只记录 + 显示。
	quality, qerr := captureLayoutQuality(cfg, window, specPath, minScore)
	if qerr != nil {
		// 打分失败绝不阻断签字：它是质量表不是硬门，而硬门（layout-lint --gate）
		// 上面已经过了。
		fmt.Fprintf(stderr, "⚠️  layout quality snapshot skipped (%v)\n", qerr)
	} else if quality != nil {
		st.Layout.Quality = quality
		if minScore > 0 && quality.Overall < minScore {
			fmt.Fprintf(stderr, "❌ layout quality %.1f is below the requested --min-score %.1f (%d dimension(s) scored, %d skipped)\n",
				quality.Overall, minScore, quality.ScoredDims, quality.SkippedDims)
			for _, line := range weakestQualityLines(quality, 3) {
				fmt.Fprintf(stderr, "   %s\n", line)
			}
			fmt.Fprintln(stderr, "   run `pcbpilot pcb layout-score --all` for the per-component attribution")
			return errActionFailed
		}
	}

	fp := workflowNewFingerprint(workflowHashLayout(poses), len(poses))
	st.Confirm(stagePlacementReady, "confirm", note)
	st.Confirm(stagePlacementConfirmed, "confirm", note)
	st.LayoutFP = fp
	if err := savePcbStageState(st); err != nil {
		return err
	}
	// The confirmation summary the user signs off on (issue #99 item 7).
	fmt.Fprintf(stderr, "✓ placement confirmed for %q — fingerprinted %d parts\n", project, fp.Count)
	fmt.Fprintf(stderr, "  assembly %s · min gap %.1fmil · tight pairs %d · iron-access blocked %d (corridor %.1fmil) · lint score %d\n",
		st.Assembly.Profile, st.Layout.MinGapMil, st.Layout.TightPairs,
		st.Layout.AccessBlocked, st.Layout.AccessMil, st.Layout.Score)
	if quality != nil {
		fmt.Fprintf(stderr, "  布局质量 %.1f/100 [%s] — %d 维参与加权，%d 维未测\n",
			quality.Overall, quality.Verdict, quality.ScoredDims, quality.SkippedDims)
		for _, line := range weakestQualityLines(quality, 3) {
			fmt.Fprintf(stderr, "    %s\n", line)
		}
	}
	return nil
}

// captureLayoutQuality 拉一张实时快照跑多维打分，落成可存的摘要。
//
// 它是 best-effort 的：任何失败都返回 error 让调用方降级成一行警告，绝不阻断
// confirm-layout —— 质量表读不出来不该拦住一次合法的签字。
func captureLayoutQuality(cfg *appConfig, window, specPath string, minScore float64) (*workflow.QualitySummary, error) {
	snap, err := fetchBoardSnapshot(cfg, window, boardSnapshotOpts{withSilk: true, withRules: true, withLayers: true})
	if err != nil {
		return nil, err
	}
	var s0 *spec.Spec
	if specPath != "" {
		raw, rerr := os.ReadFile(specPath)
		if rerr != nil {
			return nil, fmt.Errorf("read spec: %w", rerr)
		}
		if s0, err = spec.Parse(raw); err != nil {
			return nil, err
		}
	}
	rep := analyzeLayoutScore(snap, s0, layoutScoreOpts{minScore: minScore})
	dims := make(map[string]float64, len(rep.Dimensions))
	for _, d := range rep.Dimensions {
		if d.Status == dimSkipped {
			continue // 跳过的维不进快照：存一个 0 会在下次对比时假装"退化了"
		}
		dims[d.ID] = d.Score
	}
	return &workflow.QualitySummary{
		Overall: rep.Overall, Verdict: rep.Verdict, Dimensions: dims,
		ScoredDims: rep.ScoredDims, SkippedDims: rep.SkippedDims,
		MinScore: minScore, At: time.Now().UTC().Format(time.RFC3339),
	}, nil
}

// weakestQualityLines 挑最弱的几维排成人读行 —— 签字时最该看的就是这几行。
func weakestQualityLines(q *workflow.QualitySummary, n int) []string {
	type kv struct {
		id string
		v  float64
	}
	var all []kv
	for id, v := range q.Dimensions {
		all = append(all, kv{id, v})
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v < all[j].v
		}
		return all[i].id < all[j].id
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make([]string, 0, len(all))
	for _, e := range all {
		title := dimensionTitles[e.id]
		if title == "" {
			title = e.id
		}
		out = append(out, fmt.Sprintf("最弱维 %s(%s) %.1f", title, e.id, e.v))
	}
	return out
}

// newPcbStageConfirmOutlineCmd confirms outline_confirmed — the P3 board-frame
// sign-off (board size, edge-part protrusion, mounting-hole clearance).
func newPcbStageConfirmOutlineCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var note string
	c := &cobra.Command{
		Use:   "confirm-outline",
		Short: "Record the board outline in the deprecated checklist",
		Long: `Record a historical review of the board outline / frame. This compatibility
command requires the legacy placement record first, because the outline is fit to
the placement. The placement fingerprint is compared here, so a move
since confirm-layout sends you back to P2. Review board dimensions,
edge-connector protrusion (~0.5–1mm past the edge) and mounting-hole clearance
before recording. The result does not authorize or block any PCB operation.`,
		Args:    cobra.NoArgs,
		Example: `  pcbpilot pcb stage confirm-outline --project ceshi --note "40×25mm, USB-C 0.8mm proud"`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStageConfirmOutline(cfg, *window, note, stderr)
		},
	}
	c.Flags().StringVar(&note, "note", "", "what was reviewed/confirmed (recorded in the audit trail)")
	return c
}

// runStageConfirmOutline is the P3 sign-off implementation, shared by
// `pcb stage confirm-outline` and `workflow confirm outline`.
func runStageConfirmOutline(cfg *appConfig, window, note string, stderr io.Writer) error {
	project, err := resolveStageProject(cfg, window)
	if err != nil {
		return fmt.Errorf("confirm-outline needs a connected window (the confirmation is fingerprinted against the live outline): %w", err)
	}
	st, err := loadPcbStageState(project)
	if err != nil {
		return err
	}
	// Re-verify the placement fingerprint: an out-of-band move since
	// confirm-layout must send the flow back to P2, not ride into P3.
	drift, derr := verifyStageFingerprints(cfg, window, st)
	if derr != nil {
		return fmt.Errorf("confirm-outline: %w", derr)
	}
	if len(drift) > 0 {
		_ = savePcbStageState(st)
		for _, d := range drift {
			fmt.Fprintf(stderr, "⚠️  %s\n", d)
		}
	}
	if !st.Has(stagePlacementConfirmed) {
		fmt.Fprintf(stderr, "❌ confirm the placement first (`pcb stage confirm-layout`) — the outline is fit to it.\n")
		return errActionFailed
	}
	fp, err := pullOutlineFingerprint(cfg, window)
	if err != nil {
		return fmt.Errorf("confirm-outline: %w", err)
	}
	if fp.Count == 0 {
		fmt.Fprintln(stderr, "❌ the active PCB has no board outline — draw one first (`pcb outline-fit` / pcb.outline.set)")
		return errActionFailed
	}
	st.Confirm(stageOutlineConfirmed, "confirm", note)
	st.OutlineFP = fp
	if err := savePcbStageState(st); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "✓ outline confirmed for %q — fingerprint recorded\n", project)
	return nil
}

// newPcbStageResetCmd clears only the deprecated compatibility record.
func newPcbStageResetCmd(cfg *appConfig, window *string, stdout io.Writer) *cobra.Command {
	var from string
	var all bool
	c := &cobra.Command{
		Use:   "reset",
		Short: "Clear deprecated checklist entries (or --all)",
		Long: `Clear historical checklist entries. --from <stage> clears that entry and
the later entries; --all wipes only the compatibility record back to imported.
This does not change, reset or unlock the PCB.`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot pcb stage reset --all --project ceshi
  pcbpilot pcb stage reset --from placement_confirmed --project ceshi`,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := loadPcbStageState(stageKeyBestEffort(cfg, *window))
			if err != nil {
				return err
			}
			var cleared []pcbStage
			if all {
				cleared = st.InvalidateFrom(stagePlacementReady, "manual reset --all")
			} else {
				target := pcbStage(strings.TrimSpace(from))
				if pcbStageRank(target) < 0 {
					return fmt.Errorf("--from must be one of %v (or use --all)", pcbStageOrder)
				}
				cleared = st.InvalidateFrom(target, "manual reset --from "+from)
			}
			if err := savePcbStageState(st); err != nil {
				return err
			}
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			out := make([]string, len(cleared))
			for i, c := range cleared {
				out[i] = string(c)
			}
			return enc.Encode(map[string]any{"ok": true, "cleared": out, "compatibilityOnly": true})
		},
	}
	c.Flags().StringVar(&from, "from", "", "stage to clear from (inclusive)")
	c.Flags().BoolVar(&all, "all", false, "clear all confirmations")
	return c
}
