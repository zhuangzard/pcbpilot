package app

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

// pcb_stage_state.go — CLI adapter over historical internal/workflow records.
//
// Stages, persistence, readiness calculations, and fingerprints live in
// internal/workflow. State is global per project and remains readable by the
// legacy `workflow` / `pcb stage` commands; it no longer authorizes or blocks
// typed actions. The old cwd-relative .pcbpilot/pcb-stage file remains a fallback.
//
// Explicit stage commands can still compare fingerprints stored at confirm time
// with live placement/outline geometry for historical reporting.

// Aliases keep the app-side names stable over the shared workflow types.
type (
	pcbStage             = workflow.Stage
	pcbStageEvent        = workflow.Event
	pcbStageState        = workflow.State
	pcbAssemblyProfile   = workflow.AssemblyProfile
	pcbLayoutGateSummary = workflow.GateSummary
	pcbCheckGateSummary  = workflow.CheckGateSummary
	routeGate            = workflow.Gate
	stageFingerprint     = workflow.Fingerprint
	stageComponentPose   = workflow.ComponentPose
	stageTierConfirm     = workflow.TierConfirm
)

// Tier ladder aliases (issue #125).
const workflowTierCount = workflow.PlacementTierCount

func workflowTierName(n int) string { return workflow.TierNames[n] }

func nowRFC3339() string { return time.Now().Format(time.RFC3339) }

func workflowHashLayout(poses []stageComponentPose) string { return workflow.HashLayout(poses) }

func workflowNewFingerprint(hash string, count int) *stageFingerprint {
	return workflow.NewFingerprint(hash, count)
}

const (
	stageImported           = workflow.StageImported
	stagePlacementReady     = workflow.StagePlacementReady
	stagePlacementConfirmed = workflow.StagePlacementConfirmed
	stageOutlineConfirmed   = workflow.StageOutlineConfirmed
	stagePreRoutePassed     = workflow.StagePreRoutePassed
	stageRoutingAuthorized  = workflow.StageRoutingAuthorized
	stagePostRouteChecked   = workflow.StagePostRouteChecked
)

var pcbStageOrder = workflow.Order

func pcbStageRank(s pcbStage) int { return workflow.Rank(s) }

func loadPcbStageState(project string) (*pcbStageState, error) { return workflow.Load(project) }
func savePcbStageState(s *pcbStageState) error                 { return workflow.Save(s) }

func checkRouteGate(s *pcbStageState, force, forceUnsafe bool, reason string) routeGate {
	return workflow.CheckRouteGate(s, force, forceUnsafe, reason)
}

// resolveStageProject yields the project key the workflow record is filed under:
// the explicit --project when given, else the live window's project identity.
func resolveStageProject(cfg *appConfig, window string) (string, error) {
	if strings.TrimSpace(cfg.project) != "" {
		if workflow.Exists(cfg.project) {
			return cfg.project, nil // 零往返的常规路径不变
		}
		// 字面键没有状态文件:可能记在同一工程的另一个身份下(名字 ↔ uuid,F3)。
		name, uuid, lerr := resolveStageIdentityLive(cfg, window)
		if lerr != nil {
			return cfg.project, nil
		}
		return stageKeyAlias(cfg.project, name, uuid, workflow.Exists), nil
	}
	key, _, err := resolveStageIdentityLive(cfg, window)
	return key, err
}

// resolveStageIdentity resolves BOTH halves of the project identity:
//
//	key  — the state FILE key. Priority unchanged on purpose: an explicit
//	       --project string always wins, or people could not locate the state
//	       they just wrote by the name they typed.
//	uuid — the LIVE project uuid, which is what makes "same name, different
//	       project" (a deleted-and-re-created `ceshi`) detectable at all. The
//	       file name cannot carry that: the name is identical by construction.
//
// The uuid is best-effort: when the window cannot be reached (or --project was
// given and the daemon is down) it comes back empty and every consumer degrades
// to the pre-identity behaviour (no stamping, no cross-page narrowing) rather
// than failing. Costs one light `project.current` read — the same probe the
// daemon uses for liveness — even when --project already pinned the key.
//
// Callers that must stay strictly OFFLINE (`pcbpilot spec backfill --project X`)
// keep using resolveStageProject and fall back to State.ProjectUUID for scoping.
func resolveStageIdentity(cfg *appConfig, window string) (key, uuid string, err error) {
	if strings.TrimSpace(cfg.project) != "" {
		name, live, lerr := resolveStageIdentityLive(cfg, window)
		if lerr != nil {
			// The typed name is authoritative for the key; an unreachable window
			// only costs us the uuid.
			return cfg.project, "", nil
		}
		return stageKeyAlias(cfg.project, name, live, workflow.Exists), live, nil
	}
	return resolveStageIdentityLive(cfg, window)
}

// stageKeyAlias keeps the typed --project key whenever it already has a state
// file. When it has none but the SAME live project is recorded under its other
// identity (uuid or friendly name), that record is used instead: `sch apply`
// queues route by uuid while people type `--project ceshi`, and reading the
// empty name-keyed record made `sch gate` lose every group ownership exemption
// (2026-09-25 E2E F3: 13 false "组间过近" by name, 0 by uuid). Aliases come only
// from the live window, never from string similarity.
func stageKeyAlias(typed, liveName, liveUUID string, exists func(string) bool) string {
	if exists(typed) {
		return typed
	}
	for _, alias := range []string{liveUUID, liveName} {
		if strings.TrimSpace(alias) != "" && alias != typed && exists(alias) {
			return alias
		}
	}
	return typed
}

// resolveStageIdentityLive asks the window who it is (name + uuid).
func resolveStageIdentityLive(cfg *appConfig, window string) (key, uuid string, err error) {
	res, err := requestAction(cfg, "project.current", window, nil)
	if err != nil {
		return "", "", fmt.Errorf("resolve project for workflow state: %w", err)
	}
	uuid = asString(res.Result["uuid"])
	if name := asString(res.Result["friendlyName"]); name != "" {
		return name, uuid, nil
	}
	if name := asString(res.Result["name"]); name != "" {
		return name, uuid, nil
	}
	if uuid != "" {
		return uuid, uuid, nil
	}
	return "", "", fmt.Errorf("resolve project for workflow state: window reports no project identity")
}

// reportStateIdentity prints the identity finding once, if there is one. It is
// deliberately a REPORT, not an action: auto-clearing would eat real state the
// day a project merely moved to another team space (new uuid, same work).
func reportStateIdentity(project string, bind workflow.BindResult, stderr io.Writer) {
	if stderr == nil {
		return
	}
	if msg := bind.Message(project); msg != "" {
		fmt.Fprintf(stderr, "⚠️  %s\n", msg)
	}
}

// warnForeignPages reports page bookkeeping that is PROVEN to belong to another
// project (same name, different uuid). Data-driven, so it keeps saying it on
// every run until the user prunes — unlike the bind-time finding, which is only
// observable the one time the uuid changes.
func warnForeignPages(project string, st *pcbStageState, stderr io.Writer) {
	if stderr == nil || st == nil {
		return
	}
	foreign := st.ForeignPages(st.BoundUUID())
	if len(foreign) == 0 {
		return
	}
	fmt.Fprintf(stderr, "⚠️  工程状态 %q 里有 %d 页属于别的工程(同名重建的残留:%s)——"+
		"它们**不参与**跨页匹配(spec 回填 / 分区打分),数据一个字节没动。"+
		"确认要清掉:`pcbpilot workflow pages --project %s --prune`\n",
		project, len(foreign), strings.Join(foreign, ", "), project)
}

// pullLayoutPoses reads the live placement poses (designator/x/y/rotation/layer)
// the layout fingerprint is derived from.
func pullLayoutPoses(cfg *appConfig, window string) ([]stageComponentPose, error) {
	res, err := requestAction(cfg, "pcb.components.list", window, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch placement for fingerprint: %w", err)
	}
	raw, _ := res.Result["components"].([]any)
	poses := make([]stageComponentPose, 0, len(raw))
	for _, ri := range raw {
		cm, ok := ri.(map[string]any)
		if !ok {
			continue
		}
		// layer is a NUMBER in the connector payload — asString() would coerce
		// it to "" and make a TOP↔BOTTOM flip invisible to the fingerprint
		// (issue #100 review). Accept number or string.
		layer := asString(cm["layer"])
		if layer == "" {
			if n, ok := asFloatOK(cm["layer"]); ok && n != 0 {
				layer = fmt.Sprintf("%.0f", n)
			}
		}
		poses = append(poses, stageComponentPose{
			Designator: asString(cm["designator"]),
			X:          asFloat(cm["x"]),
			Y:          asFloat(cm["y"]),
			Rotation:   asFloat(cm["rotation"]),
			Layer:      layer,
		})
	}
	return poses, nil
}

// pullLayoutFingerprint hashes the live placement.
func pullLayoutFingerprint(cfg *appConfig, window string) (*stageFingerprint, error) {
	poses, err := pullLayoutPoses(cfg, window)
	if err != nil {
		return nil, err
	}
	return workflow.NewFingerprint(workflow.HashLayout(poses), len(poses)), nil
}

// pullOutlineFingerprint hashes the live board outline snapshot (counts + bbox).
func pullOutlineFingerprint(cfg *appConfig, window string) (*stageFingerprint, error) {
	res, err := requestAction(cfg, "pcb.outline.get", window, nil)
	if err != nil {
		return nil, fmt.Errorf("fetch outline for fingerprint: %w", err)
	}
	snapshot := map[string]any{
		"outline":  res.Result["outline"],
		"segments": res.Result["segments"],
		"arcs":     res.Result["arcs"],
		"bbox":     res.Result["bbox"],
	}
	hash, err := workflow.HashJSON(snapshot)
	if err != nil {
		return nil, err
	}
	count := int(asFloat(res.Result["outline"])) + int(asFloat(res.Result["segments"])) + int(asFloat(res.Result["arcs"]))
	return workflow.NewFingerprint(hash, count), nil
}

// verifyStageFingerprints re-derives the placement/outline fingerprints from the
// live document and compares them to the ones stored at confirm time. A
// mismatch means the document changed after the historical confirmation (GUI
// drag, debug.exec_js, another agent). Returns human-readable drift notes for
// explicit stage/report commands; ordinary actions do not call this function.
func verifyStageFingerprints(cfg *appConfig, window string, st *pcbStageState) ([]string, error) {
	var drift []string
	if st.LayoutFP != nil && st.Has(stagePlacementConfirmed) {
		live, err := pullLayoutFingerprint(cfg, window)
		if err != nil {
			return nil, err
		}
		if live.Hash != st.LayoutFP.Hash {
			st.InvalidateFrom(stagePlacementConfirmed,
				fmt.Sprintf("placement fingerprint drift (confirmed %d parts @ %s, live %d parts)",
					st.LayoutFP.Count, st.LayoutFP.At, live.Count))
			drift = append(drift, fmt.Sprintf(
				"placement changed since confirm-layout (%d parts @ %s) — re-run `pcb stage confirm-layout`",
				live.Count, st.UpdatedAt))
		}
	}
	if st.OutlineFP != nil && st.Has(stageOutlineConfirmed) {
		live, err := pullOutlineFingerprint(cfg, window)
		if err != nil {
			return nil, err
		}
		if live.Hash != st.OutlineFP.Hash {
			st.InvalidateFrom(stageOutlineConfirmed, "outline fingerprint drift (board edge changed since confirm-outline)")
			drift = append(drift, "board outline changed since confirm-outline — re-run `pcb stage confirm-outline`")
		}
	}
	return drift, nil
}

// gateRouteCommand remains as a source-compatible helper for older composite
// commands. Workflow state is now historical diagnostic data: an absent,
// corrupt, or unconfirmed record never authorizes or refuses routing, and the
// legacy force flags do not mutate config or state.
func gateRouteCommand(_ *appConfig, _, _, _, _ string, _ io.Writer) error { return nil }

// ── placement tiers (issue #125) ────────────────────────────────────────────

// tierPoseHash hashes the poses of exactly the given designators (upper-case
// set), in HashLayout's canonical order. missing lists claimed designators no
// longer present on the board (deleted = drift).
func tierPoseHash(poses []stageComponentPose, designators []string) (hash string, missing []string) {
	want := map[string]bool{}
	for _, d := range designators {
		want[strings.ToUpper(d)] = true
	}
	var subset []stageComponentPose
	seen := map[string]bool{}
	for _, p := range poses {
		u := strings.ToUpper(p.Designator)
		if want[u] {
			subset = append(subset, p)
			seen[u] = true
		}
	}
	for _, d := range designators {
		if !seen[strings.ToUpper(d)] {
			missing = append(missing, d)
		}
	}
	return workflow.HashLayout(subset), missing
}

// verifyTierFingerprints re-derives each confirmed tier's pose hash from the
// live placement. A mismatch (moved / deleted part) invalidates that tier and
// every later one (and the placement_confirmed seal). Pure over the given
// poses; the caller persists. Returns human-readable drift notes.
func verifyTierFingerprints(st *pcbStageState, poses []stageComponentPose) []string {
	var drift []string
	for n := 1; n <= workflow.PlacementTierCount; n++ {
		tc := st.Tier(n)
		if tc == nil || tc.Empty {
			continue
		}
		hash, missing := tierPoseHash(poses, tc.Designators)
		if len(missing) > 0 {
			st.InvalidateTiersFrom(n, fmt.Sprintf("tier %d part(s) deleted: %s", n, strings.Join(missing, ",")))
			drift = append(drift, fmt.Sprintf(
				"tier %d (%s) part(s) no longer on the board: %s — re-run `pcb stage confirm-tier %d`",
				n, workflow.TierNames[n], strings.Join(missing, ","), n))
			break // later tiers were invalidated with it
		}
		if hash != tc.Hash {
			st.InvalidateTiersFrom(n, fmt.Sprintf("tier %d pose drift", n))
			drift = append(drift, fmt.Sprintf(
				"tier %d (%s) placement changed since its sign-off — re-run `pcb stage confirm-tier %d` (later tiers invalidated with it)",
				n, workflow.TierNames[n], n))
			break
		}
	}
	return drift
}

// resolveTierParts decides which designators tier n covers. Pure so it is unit
// testable: live is the board's designators (original case), claimed maps
// designator→owning tier.
//   - empty: declared empty tier — no parts, no hash.
//   - tiers 1–3: an explicit --parts list is required (the review IS per-part).
//   - tier 4: --parts optional; default = every live part no earlier tier claimed
//     (卫星件 = the rest, by definition).
//
// Errors: unknown designators, or parts already claimed by a DIFFERENT tier.
func resolveTierParts(n int, partsFlag []string, empty bool, live []string, claimed map[string]int) ([]string, error) {
	if empty {
		if len(partsFlag) > 0 {
			return nil, fmt.Errorf("--empty and --parts are mutually exclusive")
		}
		return nil, nil
	}
	liveSet := map[string]string{}
	for _, d := range live {
		liveSet[strings.ToUpper(d)] = d
	}
	var out []string
	if len(partsFlag) == 0 {
		if n != workflow.PlacementTierCount {
			return nil, fmt.Errorf("tier %d (%s) needs an explicit --parts list (or --empty) — the sign-off is per-part", n, workflow.TierNames[n])
		}
		for _, d := range live {
			u := strings.ToUpper(d)
			if t, ok := claimed[u]; ok && t != n {
				continue
			}
			out = append(out, u)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("tier 4 default (all unclaimed parts) resolved to nothing — every part is already claimed; pass --empty to record an empty tier")
		}
	} else {
		var unknown, conflict []string
		seen := map[string]bool{}
		for _, raw := range partsFlag {
			for _, d := range strings.Split(raw, ",") {
				d = strings.TrimSpace(d)
				if d == "" {
					continue
				}
				u := strings.ToUpper(d)
				if seen[u] {
					continue
				}
				seen[u] = true
				if _, ok := liveSet[u]; !ok {
					unknown = append(unknown, d)
					continue
				}
				if t, ok := claimed[u]; ok && t != n {
					conflict = append(conflict, fmt.Sprintf("%s(tier %d)", d, t))
					continue
				}
				out = append(out, u)
			}
		}
		if len(unknown) > 0 {
			return nil, fmt.Errorf("not on the board: %s (check `pcb list`)", strings.Join(unknown, ", "))
		}
		if len(conflict) > 0 {
			return nil, fmt.Errorf("already claimed by another tier: %s — a part belongs to exactly one tier (re-confirm that tier to change it)", strings.Join(conflict, ", "))
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("--parts resolved to no designators")
		}
	}
	sort.Strings(out)
	return out, nil
}

// unclaimedParts lists live designators no confirmed tier covers — a part added
// AFTER tier sign-offs would otherwise ride into placement_confirmed unreviewed.
func unclaimedParts(live []string, claimed map[string]int) []string {
	var out []string
	for _, d := range live {
		if _, ok := claimed[strings.ToUpper(d)]; !ok {
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}
