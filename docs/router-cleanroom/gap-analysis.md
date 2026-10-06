# pcbpilot built-in router: gap analysis for a clean-room rewrite

> Status: analysis, 2026-10-06. Author: gap-analyst agent (clean-room workflow).
> Scope: `pkg/pcbauto` routing (router, fan-out/planes, pairs, BGA, post-processing, DRC, pipeline).
>
> **Clean-room statement.** This document was written from pcbpilot's own MIT source, its docs and tests, published
> academic literature, and the *user-facing* documentation of fastroute (README, docs/IMPROVEMENTS.md). No source
> code of fastroute, Freerouting, KiCad's router or easyeda-pcb-router was opened, searched or fetched. pcbpilot's
> own wrapper around the fastroute binary (`internal/app/cmd_pcb_fastroute.go`, MIT) was read only for how the
> binary is invoked and how its JSON report is parsed. Everything below is in our own words; ideas from third-party
> docs are attributed in [Sources](#sources). Implementers of the rewrite must follow the same rules.

---

## 0. Summary

The current router is a **single-resolution uniform grid, 8-direction A\*, PathFinder-style negotiated congestion**
router with a large number of board-specific correctness patches (fan-out, plane simulation, exact-DRC repair,
final gate) bolted around it. It is **correct-by-gate** (it drops copper rather than ship a short) but not
**complete-by-construction**: the core search cannot move already-placed copper except by ripping whole nets, the
grid is fine (≈3.2 mil) so each search is expensive, and almost every remaining failure on dense boards is
"negotiation did not converge in the time budget" followed by the gate deleting routing.

Field result (Gas Module V5 compact, 100×80 mm, 4 layers, 199 parts): built-in **97.2 % (171/176) with 62 native
DRC connection errors**, versus fastroute **100 % / DRC 0 in 432 s** on the same placement. On BGA boards the
built-in router stays at **55–64 %** offline. The missing capabilities, in order of expected payoff:

1. Shape-based (gridless or multi-resolution) obstacle model, so a search step is cheap and exact.
2. Local conflict resolution that **moves** neighbours (push-and-shove with relax-back) instead of whole-net rip-up.
3. A post-route **optimizer** (pull-tight, via removal, corner smoothing, re-route-for-shorter) that runs on a
   partial board and is allowed to fix DRC rather than only delete.
4. Session-level continuation (checkpoint/resume) and multi-start with ordering perturbation.
5. Escape planning (BGA / fine pitch) driven by flow models, integrated with the main search rather than frozen.

---

## 1. Architecture of the current router

All references are `pkg/pcbauto/<file>:<line>` at commit on branch `dev`, 2026-10-06.

### 1.1 Entry points and pipeline

| Stage | Where | What it does |
|---|---|---|
| `Run` | pipeline.go:68 | Analyse → `DecideStackup` → route (`route1`) → `MicroFix` → power integrity → strict DRC. Tries pair-unit and leg-by-leg routing and keeps the better (pipeline.go:91–118). |
| Stack escalation | pipeline.go:121 | When completion < 97 % on ≥4 layers, converts the power plane into a mixed signal+pour layer and routes **the whole board again**, keeps the better. |
| Fine-grid retries | pipeline.go:154 | Quick boards with high-speed findings re-route on 0.9/0.8/0.7× grid. Each is a full re-route. |
| IR loop | pipeline.go:201, 229 | Re-routes power nets wider when the IR-drop budget is missed. |
| Beautify | pipeline.go:210 → beautify.go:1564 | Gated aesthetics pass, rolled back whole if anything electrical worsens. |
| `Route` | router.go:309 | Builds grid, nets, fan-out, BGA escape, plane split, negotiation, pour bridging, emit/repair/gate (router.go:309–407). |
| Place↔route loop | loop.go:51 (`PlaceRoute`), feedback.go:456 | Placement is re-run around failure points (up to 3 loops); each loop is a full route. |

Observation: one `pcb auto run` can invoke `Route` 2 (pairs) × 2 (stack escalation) × up to 6 (grid retries) ×
3 (placement loops) times. This multiplication is the main reason for the 594–1840 s per variant in the field
(§3), not the speed of a single search.

### 1.2 Grid and obstacle model

- **Uniform multi-layer occupancy grid** (grid.go:26). Per layer-cell: flags (hard / no-via), pad owner, a `use`
  counter of distinct routed nets, and a float history cost; 8×8 block summaries accelerate empty-neighbourhood
  queries (grid.go:36–41).
- **Claim model**: every copper object claims all cells within (half width + half clearance) of its centreline; two
  nets conflict iff they share a cell (grid.go:17–25, explained in docs/pcbauto.md "占位模型"). Disk radius is
  `ceil(r/g − ½)` so "no shared cell ⇒ exact clearance" along the axes (grid.go:84–90).
- **Pitch** is derived from the dominant signal class so that its claim radius is m+½ cells (router.go:425,
  `defaultGrid`): ≈3.2 mil for 10/6 rules. A 100×80 mm, 4-layer board is therefore ≈1230×980×4 ≈ 4.8 M nodes.
- Clearance per net class is supported via per-net radii and cached static maps (router.go:731, 750, 661).
- Exactness is *not* guaranteed off-axis; it is restored afterwards by `padsClear` (router.go:816), the exact-DRC
  repair loop and `MicroFix` (drc.go:522).

### 1.3 Search

- `search` (router.go:1356): A\* over (layer, x, y) with **8 directions**, no acute turns or reversals
  (router.go:1440–1447), octile heuristic to the targets' bounding box (router.go:1379–1384).
- Indexed binary heap with decrease-key (router.go:1280–1350); per-search stamps avoid clearing arrays.
- Layer change = through via at the current column, cost `ViaCostMil × viaCost(x,y)` (router.go:1515–1540);
  only through vias exist (no blind/buried/micro-via model).
- Windowed search: tight bounding box + 120 mil, + 400 mil, whole board (router.go:1710). Before searching, a
  relaxed **flood from the targets** proves unreachability and skips all three windows (router.go:2107,
  "先证明不可达再搜索" in docs/pcbauto.md).
- Multi-terminal nets: **sequential Steiner-like growth** from a root group (centroid-nearest, or the largest
  island when bridging) — each new connection searches from the whole current tree to all remaining groups
  (router.go:1594–1800). Daisy-chain mode for pairs/RF (router.go:1654–1660).
- Virtual clock: budgets measured in A\* expansions for deterministic benchmarking (clock.go:19, `WorkRate`).

### 1.4 Cost model

Per node (router.go:953 `cost`):

```
cost(node) = (1 + hist/area) × (1 + presFac × occ) × pairFactor
step       = g × {1 | √2 × 1.15 (diagonal on a preferred-direction layer)} × {WrongDirCost=1.6 off-direction}
           + 2g per 90° bend, 0.4g per 45° bend, optional jog / pad-axis penalties (router.go:1471–1494)
           × layerMul[l]  (reference-plane pricing for high-speed nets, router.go:2386)
via        = ViaCostMil (60 mil default) × viaCost(x,y) (array room, hole gaps, pair-via factor)
```

`occ` and `hist` are summed over the whole claim disk (router.go:880 `nodeCong`), so a step's cost already reflects
neighbouring nets. `strict` mode turns any overlap into +Inf (router.go:966).

### 1.5 Rip-up and negotiation

- `negotiate` (router.go:1960) is PathFinder [McMurchie & Ebeling 1995]: route all nets in order (priority, then
  short span first; pairs back to back, router.go:1872), then iterate: add history 0.5×(use−1) on over-used cells
  (router.go:2020), multiply present factor by 1.6 (router.go:2029), and **rip up and re-route every net still in
  conflict** (router.go:2034–2058).
- Stops on zero conflicts, deadline, `MaxIters`=40, or stall (two rounds < 3 % gain when a round costs > 1/10 of the
  budget, router.go:2001–2018).
- **Legalisation** (router.go:2063–2090): strict mode; lowest-priority conflicting nets are partially re-routed
  without overlap (`repairPartial`), up to 3 passes; whatever still conflicts is removed.
- Net granularity: the unit of rip-up is the whole net (or "the conflicting connection segments" in
  `repairPartial`); there is no push/shove of neighbours.

### 1.6 Vias

- Through vias only; sizes from `NetPlan.Via` or board rule (router.go:116 `rnet.viaDrill/viaDia`).
- Current-carrying nets get **via arrays** at transitions (viaarray.go:30); short arrays are fixed afterwards by a
  ladder of larger drills, banned sites, or no layer change (viafix.go:68, 119).
- Hole-to-hole gaps tracked in 50 mil buckets plus an O(1) blocked-cell map (router.go:198–203, 2262).

### 1.7 Planes, pours and fan-out

- `fanout` (plane.go:26): every SMD pad of a plane/pour net gets a fan-out via before signal routing, fine-pitch
  first; sites are rays away from the part body (plane.go:370). Fan-outs may **yield** to a blocked signal during
  legalisation unless pinned (BGA).
- `splitPlanes` (plane.go:703, `splitLayer` 766): Voronoi-like splitting of a plane layer between power nets on a
  coarse grid, traced into polygons.
- `simulate` (plane.go:1097): connectivity of a plane/pour after anti-pads and other-net claims, union-find of
  islands; `pourRepair` (plane.go:1343) bridges islands with routed copper (ground first, largest root).

### 1.8 Differential pairs and length

- Pairs route as a **unit**: leader first, follower with a constraint field one pitch away (pairroute.go:37, 59,
  114, 350), several leader/follower/free attempts kept by a quality score (pairroute.go:225–300).
- Undeclared pairs use a soft "pair field" (router.go:1907).
- After routing, `snapPairGaps` pulls the follower to the exact pitch (pairroute.go:533) and `tuneLengths`
  adds meanders for skew/length groups (tune.go:29, 152, 221).

### 1.9 BGA

- `detectBGAs` (bga.go:46), escape ring count from pitch/ball/rules (bga.go:105), dog-bone sites in voids
  (bga.go:420, 567), **min-cost max-flow** assignment of balls to voids (bga.go:657), via class selection
  (bga.go:531).
- `bgaEscape` (bga_escape.go:116): single-layer zero-conflict pre-routes from each ball to the array boundary,
  then **frozen as fixed copper**; released only in DRC repair (post.go:117–124).
- Off by default (`RouteOptions.BGA`, router.go:48–51) because it did not beat the generic fan-out.

### 1.10 Post-processing, DRC and the final gate

`emit` (post.go:18) runs, in order:

1. Per-net geometry: `splitRuns` → `compress` → `stringPull` (octilinear) → `chamfer` 90°→45° (post.go:430–535).
2. **Exact-DRC repair** (post.go:58–131), 3 rounds: `CheckDRC` → for each violating net, `repairLocal` (re-route
   only connections near the violation, post.go:641) or whole-net re-route with inflated clearance
   (post.go:315 `inflate`).
3. `snapPairGaps`, `tuneLengths` (post.go:137–139), `completeViaArrays`.
4. **Final gate** (post.go:148–205): any remaining violation either relocates a fan-out, or **deletes the whole
   routed net** (reason `drc-unrepairable`), or drops a fan-out via. Up to 4 passes.
5. `MicroFix` (drc.go:522) nudges/narrows sub-0.25 mil shortfalls after `Route` returns (pipeline.go:80).

`CheckDRC` (drc.go:102) is an exact geometric checker (pads, tracks, vias, holes, edge, keep-outs) with bounding-box
culling; `CheckDRCStrict` uses 0.01 mil tolerance; `checkConnectivity` (drc.go:424) computes disconnected groups.
These are reusable as an independent referee.

Beautify (beautify.go:102, gated in 1564): tee splitting, pad-stub removal, fan-out straightening, S-jog removal,
collinear merge, grid snapping; every edit is re-checked (beautify.go:705 `okEdit`) and the whole pass is rolled
back if any electrical/DRC figure worsens.

### 1.11 Tests and benchmarks

- `TestFixtureBench` (fixture_test.go:84–124): 5 real fixture boards (MIPI, bbclaw, ESP32-S3, RK3568, K230) from
  `internal/app/testdata/boards/*.json`, human placement, 4 min budget each; logs completion, vias, wirelength,
  iterations, work, DRC by kind, unrouted reasons, aesthetics.
- `make fixture-bench` (Makefile:39, wall-clock) and `make fixture-bench-det` (Makefile:51, virtual clock via
  `PCBPILOT_BENCH_WORK`).
- `TestDebugRouteSpeed` (bit-identical SHA-256 speed bench, see docs/pcbauto.md "布线提速").
- No assertions on completion beyond MIPI ≥ 80 % (fixture_test.go:67); the bench is log-only.

---

## 2. Gap analysis by topic

Legend: **exists** / **missing** / **weak**.

### (a) Core search

| | Assessment |
|---|---|
| Exists | 8-direction A\* on a 3-D grid; octile heuristic; indexed heap; windowed search with unreachability proof; Steiner growth from the tree; per-class claim radii; neck-down near pads and inside BGA fields; reference-plane layer pricing; virtual clock. |
| Missing | **Gridless / shape-based search** (tile, corner-stitched or visibility-graph expansion) — every search pays for ≈g-sized steps over tens of thousands of cells [Ohtsuki 1985; Hightower 1969; Margarino et al. 1987]. **Multi-resolution** (coarse global route → detailed route) [general global/detail split, e.g. Sherwani]. **Any-angle** expansion during search (only post-hoc string pulling). **Blind/buried/micro-vias** and via-in-pad as search moves. **Better Steiner topology** (e.g. KMB/1-Steiner style ordering instead of nearest-to-tree) [Kou et al. 1981; Hanan 1966]. **Admissible heuristic that includes via cost** when target is on another layer. |
| Weak | Exactness off-axis relies on later repair (§1.2). Step cost over a disk (`nodeCong`) is the hot path; still ~10⁹ expansions on K230. The heuristic targets a bbox, so multi-target searches expand widely. Cost constants (0.6 start, ×1.6, 0.5 history, 60 mil via) are hand-tuned single values per board. |

### (b) Rip-up / push-and-shove / negotiated congestion

| | Assessment |
|---|---|
| Exists | PathFinder negotiation with history and present cost; stall detection; strict legalisation; partial (connection-level) repair; pair units re-routed together; fan-out yielding; root swap for island bridging. |
| Missing | **Push-and-shove**: displacing neighbouring traces/vias by the minimum needed and springing them back, which is what interactive and Specctra-family routers use to finish the last few percent (described as a feature in the Freerouting user documentation [Freerouting docs] and in fastroute's README as "pull-tight/shove/optimizer" stages [fastroute README]). **Multi-start with order perturbation** (fastroute: re-run with shuffled connection orders while unrouted remain [fastroute README]). **Checkpoint / resume** of a partially routed session (fastroute `--initial-session`; pcbpilot already depends on it in `pcb auto route`). **Targeted rip-up** limited to the items actually blocking (fastroute docs: "smart rip-up" [fastroute IMPROVEMENTS]). **Topological / rubber-band** reasoning to keep detours short after shoving [Dai et al. 1991]. |
| Weak | Rip-up granularity is the net. Late in negotiation each round re-routes ~176 nets on RK3568 and does not converge (176→177→176→174, docs/pcbauto.md). Legalisation runs after the deadline with little time, then the final gate **deletes** nets — this is exactly how the Gas V5 result lost connections (97.2 %) and why native DRC saw connection errors on copper the internal checker had passed or partly removed. Routing order is fixed (priority, span) — no learning across rounds. |

### (c) Post-route optimization

| | Assessment |
|---|---|
| Exists | Octilinear string pull, 90°→45° chamfer, exact-DRC repair loop (local then whole-net), `MicroFix` nudges, pair-gap snapping, meander length tuning, via-array completion, gated beautify (straightening, S-jogs, merges, grid landing). |
| Missing | **Via minimisation** (remove a layer change when a same-layer path now exists). **Re-route-for-shorter / pull-tight on the whole board** after completion, iterated while it improves (fastroute docs: optimizer passes with rollback, partial-board optimization, time budget [fastroute IMPROVEMENTS]). **Tail removal** of unused fan-out stubs [fastroute IMPROVEMENTS]. **Repair that moves the other party** without full rip-up (only fan-out relocation exists). **Pass rollback by global score** inside the router (exists only in the pipeline/beautify layer). |
| Weak | Post-processing works on emitted polylines, separately from the grid state, so improvements cannot feed back into congestion. Final gate prefers deleting a net to finding a legal local fix; there is no "near-miss join" (fastroute docs mention joining near-miss ends to pads [fastroute IMPROVEMENTS]). |

### (d) Fan-out, escape and rules

| | Assessment |
|---|---|
| Exists | Plane fan-out with ray sites and yield; BGA detection, void computation, min-cost-flow void assignment, ganging, dog-bone via class selection, pre-routed escapes; neck-down; IPC-2221/2152 width sizing; per-net clearance; HV isolation fields and footprint relief; board-edge bands; keep-outs; footprint holes; via current arrays; intent requirements (width/length/skew); pour connectivity simulation. |
| Missing | **Ordered escape routing** that guarantees escape order compatible with the outside (network-flow / SAT formulations [Yan & Wong 2009; Ozdal & Wong 2005; Luo et al. 2010]). **Multi-layer escape planning** (assign balls to layers before escaping). **Escape integrated with negotiation** (escapes are frozen, released only on repair failure). **Fine-pitch non-BGA escape** (QFN/QFP with ground pins between signals; the fastroute flow needs hand-made `--escapes` for Gas V5 U8, all 14 GND pins). **Rule areas** (region-scoped clearances, e.g. 3.5/3.5 mil inside a BGA). **Length-matched routing during search** [Ozdal & Wong 2004] rather than meanders afterwards. |
| Weak | BGA path default-off (did not beat generic fan-out). Escape order mismatch blocked later routes (71.2 % → 62.1 % on the synthetic board). Rules are a mix of board rule + per-net plan; there is no single rule object queried by both search and DRC. |

---

## 3. Evidence of weakness

### 3.1 Recorded in the repository

| Source | Board | Result |
|---|---|---|
| docs/pcbauto.md "验证证据" (early) | ESP32-S3 4L | 75.4 % (214/284), 114 s; mixed-layer escalation dropped to 69.4 % |
| same | RK3568 4L | 30.4 %, **26 DRC** (timeout path), 266 s |
| same | K230 6L | 38.8 %, **9 DRC**, 271 s |
| docs/pcbauto.md "BGA" table (3 min) | 6L synthetic BGA | 56.1 % (BGA off) → 84.8 % (BGA on) |
| same | K230 | 48.7 % / 46.1 % |
| same | RK3568 | 42.4 % / 39.3 % |
| "布线提速" + stall stop (3 min) | K230 | 63.8 % (8 rounds) / 62.1 % |
| same | RK3568 | 55.8 % — negotiation stalled at ~176 conflicts |
| "局部修复" (fixed human placement) | ESP32-S3 / K230 / RK3568 / MIPI | 86.6–88.0 % / 63.1 % / 57.4 % / 100 % |
| Seven-board regression 2026-09-24 | ESP32-S3 engine placement | 77.1 % |
| same | K230 / RK3568 human placement | 51.1 % / 49.4 % |
| Measurement noise | RK3568 same config | 39.3 % vs 41.4 %; CPU contention 46.4–46.8 % (wall-clock budget) |
| Speed | K230 1 round | 909–1079 s → 64 s (deterministic bench), but the board still does not complete |

Root causes the repo itself names: remaining failures "are mostly timeouts — negotiation does not converge in 3
minutes" (docs/pcbauto.md "局部修复"); 95 % of K230 search work went into failed searches (before the
unreachability proof); escape order frozen too early.

### 3.2 Field results (Gas Module, 2026-10-06)

Recorded in `.agents/skills/pcbpilot/references/pcb-auto.md` (lines ≈365–372) and `pcb-routing.md` (lines ≈326–345):

| Board | Router | Completion | DRC | Time |
|---|---|---|---|---|
| Gas Module V5 compact, 100×80 mm, 4L, 199 parts, 462–463 connections, U8 fixed centre | fastroute 0.1.7 | **100 %** | native DRC **0**; `pcb check` 0 ERROR; pad-net diff 0 | **432 s** |
| same placement | pcbpilot internal | **97.2 % (171/176 routed connections as counted by the internal router)** | **62 native "connection" errors** | 594–1840 s per variant (recorded for the 150×110 mm predecessor with the same 199 parts) |
| Gas Module v9 | fastroute single-thread | completes | — | **102 s** (8-thread run crashed after 244 s) |
| Gas 150×110 mm, 4L, 199 parts, 463 connections | fastroute first route / multi-start 8 | — | — | ≈131 s / 510–566 s |

Notes for interpretation:
- The internal router's 171/176 counts **signal connections only** (plane/pour nets are excluded from the
  denominator, post.go:223); the 62 native connection errors include pour/plane connectivity that native EasyEDA
  DRC did not accept (via touching only a pour is not counted as connected in desktop 3.2.149,
  pcb-routing.md:236, 324). A fair comparison must therefore count **all pad-to-pad connectivity** with one referee
  (§4).
- The time gap is mostly pipeline multiplication (§1.1) plus fine-grid search; fastroute routes once and continues
  from its session.

### 3.3 Large BGA fixtures offline

RK3568 (4L, 2091 pads) and K230 (6L, 1709 pads): 55–64 % at best in a 3–4 min budget, with dozens of plane
disconnects; failures are spread over the board (escape + non-convergence), not a single hot spot.

---

## 4. Benchmark protocol: pcbpilot router vs fastroute (black box)

Goal: a reproducible, single-referee comparison. fastroute is executed as an external binary only
(`FASTROUTE_BIN`, installed by `scripts/install-fastroute.sh`); its source is never consulted.

### 4.1 Board set

| Tier | Board | Source | Commit to repo? |
|---|---|---|---|
| T1 small | `lckfb-mipi-3in1-adapter`, `bbclaw-ai-voice-terminal`, `reference-4stage-compact`, `degraded-4stage-compact` | `internal/app/testdata/boards/*.json` | yes (open source / synthetic) |
| T2 medium | `lckfb-szpi-esp32s3` (4L, 628 pads) | same | yes |
| T3 BGA | `lckfb-rk3568-4layer` (4L), `lckfb-k230-canmv` (6L), 6L synthetic BGA board | same / bga_test.go | yes |
| T4 field | Gas Module V5 compact (and v9, 150×110 variant) | `Gas_Control/GasControl_PCB/.pcbpilot/artifacts/*-fixed*.dsn` (local) | **no** — commercial; pass via `PCBPILOT_BENCH_DSN=<path>` like `EASYEDA_BENCH_BOARD` |

### 4.2 Same input for both routers

Two routes to an identical input; the protocol requires (A) and allows (B) until (A) exists:

- **(A) Offline, preferred.** Add an MIT `Board → DSN` writer (from the published Specctra DSN format description)
  in the new package. For every fixture: `Board` snapshot → DSN → `specctra.FixDSN` (dsnfix.go:64, same options as
  `pcb dsn-fix`: edge 20/30 mil, intent requirements via `ApplyNetRequirements`) → this exact file is the input to
  **both** routers. Our router gets it through a `DSN → Board` reader (also new) so neither side sees information
  the other lacks. Archive DSN + SHA-256 under `testdata/router-bench/`.
- **(B) Live export.** Open the fixture's project, `pcbpilot pcb export-dsn` → `pcbpilot pcb dsn-fix` (as listed in
  docs/cli/pcb.md:44). Same file to both routers; record the editor version.

Pre-route escapes (`--escapes`, auto GND escapes) are **inputs**: either both routers get them or neither. Bench
both settings and report separately.

### 4.3 Running

- fastroute: `fastroute -de in.dsn -do out.ses --report=r.json --diagnose --router.autorouter.max_threads=1
  --router.optimizer.max_threads=1 --max-time=<T>`; continuation rounds with `--initial-session` exactly as
  `runFastroute` does (cmd_pcb_fastroute.go:771), counted inside the same total time `T`. Single-threaded because
  0.1.7 crashes in parallel mode (pcb-routing.md:277–280); a crash is recorded as `crashed`, never as 0 unrouted.
- pcbpilot new router: same `T`, same DSN, one thread unless the comparison row is explicitly "parallel vs
  parallel". Record the old router (`pcbauto.Route` with `Stack.Force` = board layers, no escalation) as a baseline
  row.
- Budgets: T1 60 s, T2 300 s, T3 600 s, T4 600 s. Additionally run each board **uncapped** (until the router
  stops) to measure time-to-100 %.
- Repetitions: 3 runs per (board, router, setting) on an idle machine, runs interleaved A/B/A/B (docs/pcbauto.md:
  noise of ~2 points under load). Report median and range. The internal router additionally runs on the virtual
  clock (`PCBPILOT_BENCH_WORK`) for determinism checks; the cross-router comparison is wall time.
- Record: machine, OS, CPU, fastroute version + SHA256 of the binary, pcbpilot commit.

### 4.4 Single referee (both outputs judged by the same code)

1. Convert fastroute SES → tracks/vias with `specctra.ParseSES` (ses.go:33) plus fixed wiring
   (`ParseFixedWiring`, ses.go:84); convert to `pcbauto.Track/Via` in mil.
2. Our router's output is already `[]Track, []Via`.
3. Judge both with:
   - `pcbauto.CheckDRCStrict` (drc.go:108) — violations by kind (track-track, track-via, via-via, pad, hole, edge,
     keep-out, footprint-hole);
   - connectivity over **all** nets including GND/power (`checkConnectivity`, drc.go:424, with pours rebuilt by the
     plane simulation or excluded and reported as a separate "plane-dependent" column);
   - intent gates reused from `internal/app/pcb_route_gates.go`: `checkIntentWidths` (:93), `checkIntentLengths`
     (:566), via-current (`pcbauto.CheckViaCurrent`, viacheck.go:91), edge (`planEdgeCheck`), isolation
     (`isolationReport`) where the board has an intent.
4. T4 only, live confirmation: import both results into EasyEDA (`pcb autoroute` import path / `apply`), pour
   rebuild, save, reload, native DRC, pad-net diff — as `pcb auto route` already does.

### 4.5 Metrics (one JSON row per run)

| Metric | Definition |
|---|---|
| `completion_all` | connected pad pairs ÷ required pad pairs over all nets, computed by the referee (not self-reported) |
| `completion_signal` | same, signal nets only (comparable with historic internal numbers) |
| `drc_strict` | count + by-kind from `CheckDRCStrict` |
| `drc_native` | T4 live only |
| `vias` | routed vias (fan-out vias reported separately) |
| `wirelength_in` | total track length; also ratio to per-net Euclidean MST of pads |
| `runtime_s` | wall time to final output; `t100_s` = time when completion first reached 100 % (uncapped runs) |
| `peak_rss_mb` | from `/usr/bin/time -l` |
| `intent_gates` | pass/fail per gate; number of failing items |
| `determinism` | completion range over 3 runs |

### 4.6 Acceptance: "our router beats fastroute"

All of the following, on the median of 3 runs:

1. **Completion**: on every board, `completion_all` ≥ fastroute's; on every board where fastroute reaches 100 %,
   ours reaches 100 %. On T3, ≥ fastroute + 0 points (no regression allowed).
2. **Correctness**: `drc_strict` = 0 on every board; T4 native DRC = 0 and pad-net diff = 0.
3. **Intent**: every intent gate that passes for fastroute passes for ours, with no extra failing items
   (fastroute does not see intent; ours should strictly dominate here).
4. **Runtime**: total wall time over T1–T4 ≤ fastroute's; per board ≤ 1.25×; T4 time-to-100 % ≤ 432 s (the recorded
   fastroute figure) on the reference machine.
5. **Quality**: vias ≤ 1.10× and wirelength ≤ 1.05× fastroute's per board (geometric mean ≤ 1.0×).
6. **Determinism**: completion range ≤ 0.5 points across runs at fixed budget.

Interim milestones (to steer the rewrite, not to claim victory):
- M1: T1+T2 = 100 %, DRC 0, ≤ 2× fastroute time.
- M2: T4 Gas V5 compact = 100 %, native DRC 0, ≤ 600 s.
- M3: T3 within 5 points of fastroute; then full acceptance.

### 4.7 Harness location

`cmd/routerbench` (or `go test ./pkg/routerbench -run TestRouterBench`, build tag `bench`) writing
`out/router-bench/<date>/results.jsonl` and a Markdown table; a `make router-bench` target next to
`fixture-bench`. Fastroute missing ⇒ its rows are `skipped`, not failures.

---

## 5. Recommended module boundaries for the rewrite

### 5.1 New package

`pkg/pcbroute` (new; MIT; no imports from `internal/app`), with sub-packages:

| Package | Responsibility |
|---|---|
| `pkg/pcbroute/geom` | exact shapes (segment, arc-free octagon/rect/circle/polygon), distance, spatial index (R-tree or uniform bucket grid of shapes) |
| `pkg/pcbroute/rules` | one rule object: per net-class width/neck/clearance, per region rules, via classes, edge/hole/keep-out clearances; built from `pcbauto.Analysis`/`NetPlan` + intent |
| `pkg/pcbroute/board` | routing database: items (pads, tracks, vias, keep-outs, planes) with net, layer, fixed/locked flags; transactional edits (begin/commit/rollback) for shove and optimizer passes |
| `pkg/pcbroute/search` | maze search over a shape-based expansion (and/or coarse global grid → detailed); multi-target; via and layer moves; cost interface |
| `pkg/pcbroute/negotiate` | congestion/history bookkeeping, order and multi-start control, budgets on the virtual clock |
| `pkg/pcbroute/shove` | local displacement of traces/vias with relax-back and rollback |
| `pkg/pcbroute/escape` | fan-out and escape planning (BGA flow assignment reused/ported from `bga.go`), integrated as soft pre-routes |
| `pkg/pcbroute/optimize` | pull-tight, via removal, re-route-for-shorter, near-miss joins, tail removal — each pass transactional with a global score |
| `pkg/pcbroute/dsn` | MIT Specctra DSN reader/writer and SES writer (format from the published description); reuse `internal/pcb/specctra` S-expression helpers where generic |
| `pkg/routerbench` | the benchmark of §4 |

### 5.2 Interfaces with `pkg/pcbauto`

```go
// in pkg/pcbroute
type Input struct {
    Board   *pcbauto.Board     // placement, pads, keep-outs, holes, outline
    Stackup *pcbauto.Stackup
    Plan    *pcbauto.Analysis  // per-net width/clearance/via/pair/length intent
    Planes  []pcbauto.PlaneRegion // decided by pcbauto (unchanged)
    Fixed   []pcbauto.Track    // pre-existing / locked copper, escapes
}
type Options struct { Budget time.Duration; WorkRate float64; Seeds int; Threads int; Resume *Session }
type Output struct {
    Tracks []pcbauto.Track; Vias []pcbauto.Via; Unrouted []pcbauto.Unrouted
    Stats  pcbauto.RouteStats; Session *Session // resumable
}
func Route(ctx context.Context, in Input, opt Options) (*Output, error)
```

- Wire-in point: `pcbauto.Route` (router.go:309) becomes a dispatcher with `RouteOptions.Engine = "legacy" | "v2"`;
  default stays `legacy` until §4.6 is met. `pipeline.Run` keeps stack decision, escalation, IR loop, beautify and
  verdicts; once v2 is faster, the escalation/grid-retry multiplication (pipeline.go:121, 154) should be revisited
  rather than inherited.
- `pcb auto run --router v2` (CLI) next to `internal` and `fastroute`.

### 5.3 What to reuse as-is

| Reuse | From | Why |
|---|---|---|
| Exact DRC + connectivity | `CheckDRC`/`CheckDRCStrict`/`checkConnectivity` (drc.go:102–424) | independent referee and final gate |
| `MicroFix` | drc.go:522 | sub-0.25 mil touch-ups |
| Net analysis, widths, clearances, via sizing | `Analyze`, `NetPlan`, electrical.go, viasize.go | rule source |
| Intent requirements and gates | `pkg/intent`, `pcbauto/intent.go`, `internal/app/pcb_route_gates.go`, viacheck.go | acceptance gates |
| Stack decision, plane split, plane simulation | stackup.go, plane.go:703, 1097 | planes stay decided before routing; simulation validates pour connectivity |
| BGA detection, void geometry, min-cost-flow assignment, via class | bga.go:46–740 | sound, already flow-based |
| Isolation / HV / edge policy | isolation.go, hvrelief.go, edge.go | safety constraints exposed as rule regions |
| Pair quality, length groups | pairroute.go:225–300, tune.go:110 | scoring, not search |
| Virtual clock | clock.go | deterministic bench |
| Specctra fix/requirements/SES parse | internal/pcb/specctra | benchmark I/O (move generic parts to `pkg/pcbroute/dsn`) |
| Beautify gate | beautify.go | stays a pipeline stage after any engine |

### 5.4 What not to carry over

The uniform 3.2 mil claim grid as the primary obstacle model; whole-net rip-up as the only conflict tool; the final
gate's "delete the net" as the normal path; frozen escapes; repeated whole-board re-routes inside the pipeline.

---

## Sources

Academic / reference (bibliographic; page numbers to be verified by implementers):

- C. Y. Lee, "An Algorithm for Path Connections and Its Applications," *IRE Trans. Electronic Computers*, EC-10(3),
  1961 — maze routing.
- P. E. Hart, N. J. Nilsson, B. Raphael, "A Formal Basis for the Heuristic Determination of Minimum Cost Paths,"
  *IEEE Trans. Systems Science and Cybernetics*, 4(2), 1968 — A\*.
- D. W. Hightower, "A Solution to Line-Routing Problems on the Continuous Plane," *Proc. 6th Design Automation
  Workshop*, 1969 — line-search (gridless) routing.
- T. Ohtsuki, "Gridless Routers — New Wire Routing Algorithms Based on Computational Geometry," *Proc. Int. Conf.
  Circuits and Systems*, 1985.
- A. Margarino, A. Romano, A. De Gloria, F. Curatelli, P. Antognetti, "A Tile-Expansion Router," *IEEE TCAD* 6(4),
  1987 — shape/tile-based search.
- L. McMurchie, C. Ebeling, "PathFinder: A Negotiation-Based Performance-Driven Router for FPGAs," *Proc. ACM
  FPGA*, 1995 — negotiated congestion (basis of the current `negotiate`).
- R. Nair, "A Simple Yet Effective Technique for Global Wiring," *IEEE TCAD* 6(2), 1987 — rip-up and reroute.
- W. W.-M. Dai, T. Dayan, D. Staepelaere, "Topological Routing in SURF: Generating a Rubber-Band Sketch," *Proc.
  DAC*, 1991 — rubber-band / topological routing.
- M. Hanan, "On Steiner's Problem with Rectilinear Distance," *SIAM J. Appl. Math.* 14(2), 1966.
- L. Kou, G. Markowsky, L. Berman, "A Fast Algorithm for Steiner Trees," *Acta Informatica* 15, 1981.
- M. M. Ozdal, M. D. F. Wong, "Length-Matching Routing for High-Speed Printed Circuit Boards," *Proc. ICCAD*, 2003;
  and "A Length-Matching Routing Algorithm for High-Performance Printed Circuit Boards," *IEEE TCAD*, 2006.
- M. M. Ozdal, M. D. F. Wong, P. S. Honsinger, "An Escape Routing Framework for Dense Boards with High-Speed Design
  Constraints," *Proc. ICCAD*, 2005.
- T. Yan, M. D. F. Wong, "A Correct Network Flow Model for Escape Routing," *Proc. DAC*, 2009.
- L. Luo, T. Yan, Q. Ma, M. D. F. Wong, T. Shibuya, "B-Escape: A Simultaneous Escape Routing Algorithm Based on
  Boundary Routing," *Proc. ISPD*, 2010.
- N. A. Sherwani, *Algorithms for VLSI Physical Design Automation*, 3rd ed., Kluwer, 1999 — global vs detailed
  routing, maze and line-search routers.
- Wikipedia, "Autorouter", "A\* search algorithm", "Steiner tree problem" (accessed 2026-10-06).
- IPC-2221B, *Generic Standard on Printed Board Design*; IPC-2152, *Standard for Determining Current Carrying
  Capacity in Printed Board Design* — width/clearance rules already used by `pcbauto`.
- Cadence, *SPECCTRA Design Language Reference* (DSN/SES file format description).

Third-party user-facing documentation (ideas only, no code):

- fastroute README, https://github.com/parisxmas/fastroute — stages "pull-tight/shove/optimizer"; parallel
  autorouting; multi-start with shuffled connection order; diff pairs routed first; post-route meander length
  matching; neck-down floor; time limit preserving best result; resume from session; JSON report with unrouted
  diagnostics; ~4× faster than Freerouting on its benchmark suite (as claimed there).
- fastroute docs/IMPROVEMENTS.md, https://github.com/parisxmas/fastroute/blob/main/docs/IMPROVEMENTS.md — tail
  removal, greedy optimizer passes with rollback, plane-terminating paths, smart (limited) rip-up, stagnation
  detection, near-miss contact joining, partial-board optimization, optimizer time budgeting.
- Freerouting user documentation (https://freerouting.org, user manual) — push-and-shove interactive routing and
  batch autorouter + optimizer as user features.

pcbpilot internal (MIT, this repository):

- `pkg/pcbauto/*.go` (line references above), `docs/pcbauto.md`, `.agents/skills/pcbpilot/references/pcb-auto.md`,
  `.agents/skills/pcbpilot/references/pcb-routing.md`, `docs/cli/pcb.md`, `Makefile`, `internal/app/cmd_pcb_fastroute.go`
  (invocation/report parsing only), `internal/app/pcb_route_gates.go`, `internal/pcb/specctra/*.go`.
