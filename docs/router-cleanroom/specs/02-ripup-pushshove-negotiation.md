# Spec 02 — Rip-up, Push-and-Shove, Negotiated Congestion

Status: clean-room specification (draft 1)
Licence of this document and of any implementation derived from it: MIT (pcbpilot).
Provenance: written only from academic literature, encyclopaedic articles and the
user-facing documentation listed in §7. No source code of any GPL router
(fastroute, Freerouting, KiCad PNS, easyeda-pcb-router) was opened or consulted.

---

## 1. Purpose and scope

This spec defines the **conflict-resolution layer** of the pcbpilot autorouter: everything
that happens once a single-connection path finder (A* on a grid or on free-space tiles,
specified separately) exists. It covers:

- **Negotiated congestion** (PathFinder-style): let nets temporarily share resources,
  then raise the price of shared resources until every net settles on a legal path.
- **Rip-up and reroute (RRR)**: remove existing routes that block a connection and queue
  them for rerouting, with controls that prevent oscillation.
- **Push-and-shove**: locally displace already-placed traces and vias by the minimum
  amount needed to satisfy clearance, instead of ripping them up; plus the fallback
  **walkaround** (hug obstacles without moving them).
- **Net ordering, multi-start with shuffled orders, convergence criteria, pass limits,
  keeping the best state, rollback**, and the endgame strategy for reaching 100 %
  completion on dense boards.

Out of scope: the low-level path search and cost function (spec 01), geometry kernel /
clearance checks (spec 03), post-route optimisation (via minimisation, straightening;
spec 04). This spec only names the interfaces it needs from them.

---

## 2. Concepts and data structures

### 2.1 Vocabulary

| Term | Meaning (our definition) |
|---|---|
| Connection | One two-terminal edge of a net's spanning tree (pad→pad, pad→existing copper of same net). The unit of routing. |
| Route | The ordered list of segments (layer, polyline, width) and vias realising one connection. |
| Resource | A unit of routing capacity. In grid mode: a grid cell `(layer, x, y)` or a via site `(x, y, layerSpan)`. In shape mode: a free-space tile. |
| Occupancy `occ(r)` | Number of distinct **nets** whose current routes use resource `r` (with clearance inflation, §4). |
| Capacity `cap(r)` | How many nets may legally use `r`. 1 for a grid cell; may be >1 for coarse global-routing tiles. |
| Overuse | `max(0, occ(r) − cap(r))`. A board is legal when total overuse is 0 and DRC is clean. |
| Present cost `p(r)` | Penalty for using `r` *now*, grows with current occupancy and with the iteration's pressure factor. |
| History cost `h(r)` | Monotonically non-decreasing memory of how often `r` was overused in past iterations. |
| Fixed item | Pads, keep-outs, board edge, locked user tracks — never moved, never ripped. |
| Movable item | Autorouter-owned track segment or via. Can be shoved or ripped. |
| Rip count | Per-connection and per-net counters of how often they were ripped in the current pass/run. |

### 2.2 Core Go structures (suggested)

```go
type ResourceID uint32            // dense index into flat arrays (cache friendly)

type CongestionMap struct {
    Cap     []uint8               // capacity per resource
    Occ     []uint16              // current occupancy (distinct nets)
    Hist    []float32             // history cost h(r), starts at 1.0 (or 0 for additive form)
    Owners  [][]NetID             // small slice; usually 0..2 entries
}

type Connection struct {
    ID        ConnID
    Net       NetID
    From, To  Terminal
    Class     NetClassID          // width / clearance / via rules
    Route     *Route              // nil if unrouted
    RipCount  int                 // rips in this run
    Locked    bool                // user/fixed: never ripped
    Priority  float64             // ordering key, §3.4
}

type RouterState struct {
    Conns       []Connection
    Cong        CongestionMap
    Index       SpatialIndex       // R-tree / bucket grid of placed copper, spec 03
    Iter        int
    PresFac     float64
    BestSnapshot Snapshot          // §3.6 keep-best
    Rng         *rand.Rand        // seeded; determinism required
}
```

Determinism requirement: given the same board, parameters and seed, the router must
produce bit-identical output regardless of `GOMAXPROCS`. Parallel work is allowed only
if results are committed in a fixed order (the fastroute README describes the same goal
of identical results across thread counts [F1]).

### 2.3 Snapshots

A `Snapshot` stores, per connection, its route (or nil) plus the congestion arrays.
Routes are immutable values, so a snapshot is a slice of pointers plus copies of
`Occ`/`Hist` — O(#connections + #resources) memory. Snapshots enable rollback (§3.6)
and multi-start comparison (§3.5).

---

## 3. Algorithms

The router runs three phases in order. Each phase is optional and can be disabled.

```
Phase A  Negotiated congestion (global legalisation, many cheap iterations)
Phase B  Detailed RRR + push-and-shove (local, on real geometry)
Phase C  Endgame for the last unrouted connections (targeted rip-up search, multi-start)
```

Phase A is what makes dense boards converge; Phase B converts the result into DRC-clean
geometry and repairs what Phase A could not; Phase C chases the last 0.1–2 %.

### 3.1 Phase A — negotiated congestion (PathFinder)

Source idea: McMurchie & Ebeling, PathFinder [P1]; A* variant by Tessier [P2]; parameter
naming follows VPR's documented router options [P3]. Every net is routed by the normal
path finder, but overlaps with other nets are *allowed* at a price. After each iteration
prices on overused resources rise, so nets that have cheap alternatives move away and
the net that "needs" the resource most keeps it.

**Cost of entering resource `r` for connection `c`:**

```
cost(r) = (b(r) + h(r)) * p(r)                        // multiplicative form [P1]
b(r)    = base cost: length of the step * layer weight * direction penalty (spec 01)
p(r)    = 1 + presFac * max(0, occ(r) + 1 - cap(r))    // occ excludes c's own net
h(r)    = h_prev(r) + histFac * max(0, occ(r) - cap(r))   // updated once per iteration
```

Own-net sharing is free: a resource already used by another connection of the same net
counts as `occ` contribution 0 for that net (Steiner sharing).

**Pseudo-code:**

```
func Negotiate(state):
    presFac = presFacFirst                       // 0 → first iteration ignores others
    for iter = 1 .. maxIters:
        order = OrderConnections(state, iter)    // §3.4
        for c in order:
            if iter > 1 and !NeedsReroute(c): continue      // incremental mode
            RemoveRoute(c)                       // decrement occ along old path
            path = AStar(c, costFn = cost)       // never fails: overuse is allowed
            if path == nil: markUnroutable(c); continue     // truly blocked by fixed items
            Commit(c, path)                      // increment occ
        overuse = Σ_r max(0, occ(r)-cap(r))
        record(iter, overuse)
        if overuse == 0: return SUCCESS
        for r with occ(r) > cap(r): h(r) += histFac * (occ(r)-cap(r))
        presFac = (iter == 1) ? presFacInitial : presFac * presFacMult
        if Stalled(history of overuse, window = stallWindow): return STALLED
    return MAXITER
```

`NeedsReroute(c)` is true when c's path touches any overused resource, or when c is
unrouted. Rerouting only affected connections ("incremental rerouting") makes late
iterations cheap; Nair's original rip-up-and-reroute instead reroutes *every* net each
round so that non-congested nets also vacate space for neighbours [P4]. Implement both
and choose with `rerouteAll` (default: rerouteAll for iterations 1–3, then incremental).

**Parameters (pcbpilot defaults; ranges that should be swept in tests):**

| Param | Default | Range | Notes |
|---|---|---|---|
| `maxIters` | 50 | 20–200 | VPR exposes the same concept [P3] |
| `presFacFirst` | 0.0 | 0–0.5 | iteration 1 = shortest paths, overlap ignored |
| `presFacInitial` | 0.5 | 0.3–1.0 | |
| `presFacMult` | 1.5 | 1.2–2.0 | higher = faster convergence, longer wires |
| `presFacMax` | 1e5 | — | clamp to avoid float overflow |
| `histFac` | 1.0 | 0.2–2.0 | |
| `bboxMargin` | 3 grid pitches, grows ×2 when a connection fails twice | 1–∞ | limits A* search region |
| `stallWindow` | 8 iterations | 5–15 | |

**Stall detection:** declare STALLED if over the last `stallWindow` iterations the
minimum overuse has not decreased by at least 2 %, or overuse is oscillating between
the same values. On STALLED, proceed to Phase B anyway with the best snapshot (the one
with minimal `(unrouted, overuse, wirelength)` lexicographically).

**Complexity:** one iteration costs Σ over rerouted connections of A* cost,
O(N_bbox · log N_bbox) each, where N_bbox is the number of resources in the search box.
Total ≈ iters × that. Congestion updates are O(path length) per route plus O(#overused)
per iteration if overused resources are kept in a set.

### 3.2 Phase B — detailed rip-up and reroute with shove

Phase A works on a discretised resource model; Phase B works on true geometry with
real widths and clearances. It is a pass-based loop, in spirit similar to the escalating
pass scheme described in the Freerouting user documentation (early passes rip cheaply,
later passes make rip-up progressively more expensive; ripped nets wait for the next
pass) [F2].

```
func DetailedPasses(state):
    for pass = 1 .. maxPasses:
        ripCost = ripCostBase * pass                    // rip-up becomes dearer
        queue   = unrouted ∪ DRC-violating connections, ordered by §3.4
        rippedThisPass = set{}
        for c in queue:
            if c in rippedThisPass: continue            // wait for next pass
            res = RouteWithObstacles(c, ripCost)        // see below
            switch res.kind:
              case CLEAN:  Commit(c, res.path)
              case SHOVE:  if ApplyShove(res.shove) == OK { Commit(c, res.path) }
                           else fallthrough to RIP
              case RIP:    if CanRip(res.victims) {
                               for v in res.victims { Rip(v); rippedThisPass.add(v) }
                               Commit(c, res.path)
                           }
              case FAIL:   c.failCount++
        record pass metrics; keep-best snapshot (§3.6)
        if unrouted == 0 and drc == 0: return SUCCESS
        if Converged(pass): break                       // §3.7
```

**RouteWithObstacles(c, ripCost).** Runs A* where foreign movable copper is not a hard
wall but carries a cost: crossing a foreign route `v` costs `ripCost * w(v)` where
`w(v) = 1 + v.RipCount * ripGrowth + (v.Net.isWide ? wideFactor : 0)`. The result path
lists the set of foreign items it crosses (`victims`). Fixed items are always walls.
The search first checks whether a pure **walkaround** path exists within
`walkaroundSlack` (default 1.3) × the direct length; if so it returns CLEAN without
disturbing anyone.

**CanRip — anti-oscillation rules:**

1. Never rip locked/fixed items.
2. A net may be ripped at most `maxRipsPerNetPerPass` times per pass (default 1). The
   fastroute documentation reports that a rule of "rip a failing net only once" removed
   oscillation on large nets such as GND that a naive scheme would rip repeatedly [F1].
3. Rip only the **connection(s)** whose segments collide, not the whole net, unless the
   net's tree would become disconnected in an unrepairable way.
4. Reject a rip if `Σ victims' length > ripLengthRatio * len(c.path)` (default 4) — do not
   destroy a lot of routing to gain a little.
5. Taboo: `(c, v)` pairs that ripped each other in the last two passes may not repeat;
   this breaks 2-cycles.

### 3.3 Push-and-shove

Push-and-shove displaces existing movable copper so that a new segment fits, without
ripping anything. Public user documentation of interactive routers describes two basic
modes: shove colliding items, or walk around them [W1][K1][A1]. Everything below (the
depth-bounded cascade, the hull construction, the via drag and the relax-back step) is
pcbpilot's own design, built from the configuration-space idea of §4 [G1][G2]; the depth
limits and all other numbers in the parameter table are pcbpilot's own starting values
to be swept in tests, not values taken from any other router. Our algorithm:

```
func Shove(head Segment, world) (ShoveResult):
    // head: the new segment we want to place (with its clearance envelope)
    stack = [ (head, depth=0) ]
    moved = map[ItemID]Item{}               // tentative modifications (copy-on-write)
    for stack not empty:
        (item, d) = pop(stack)
        for obs in world.Colliding(item, clearanceFor(item, obs)):
            if obs.Fixed or obs.Net == head.Net: 
                if obs.Fixed: return FAIL(obs)     // cannot move pads/keep-outs/edge
                continue
            if d >= maxDepth(obs.kind): return FAIL(obs)
            newObs = PushAway(obs, item)          // see below
            if newObs == nil: return FAIL(obs)
            if exceeds(moved[obs], maxDisplacement): return FAIL(obs)
            moved[obs.ID] = newObs
            push(stack, (newObs, d+1))           // its new shape may collide further
    return OK(moved)
```

**PushAway(obs, pusher) for a trace segment:** compute the pusher's *hull* — the
configuration-space obstacle that the centreline of `obs` must avoid, i.e. the
Minkowski sum of the pusher shape with a disk of radius
`clearance(pusher.Class, obs.Class) + obs.width/2` [G1][G2]. We over-approximate it by
the smallest enclosing octilinear polygon so that the detour stays on 0/45/90° directions
(our choice, consistent with the octilinear track model of spec 01 §2.1). Replace the colliding portion of `obs`'s polyline by the walk along the
side of the hull that is closer to obs's original path (choose the side with smaller
added length; tie → side away from board centre). Then reconnect to obs's unchanged
neighbours with 45° jogs. Merge collinear segments.

**PushAway for a via:** translate the via along the vector from pusher's closest point
to the via centre by the penetration depth plus ε, snapped to the via grid. Attached
segments are stretched ("dragged"); their new shapes are pushed on the stack.

**Relax-back (optional, `relaxBack=true`):** after a successful shove, try to move each
moved item back toward its original shape in ≤ 3 binary-search steps, keeping the
furthest-back legal position. Reduces needless deformation. (pcbpilot design choice: a
plain bisection on the displacement, no external source.)

**Parameters:**

| Param | Default | Range |
|---|---|---|
| `maxDepthTrace` | 20 | 5–30 |
| `maxDepthVia` | 5 | 0–10 (0 = vias are walls, hug them) |
| `maxDisplacement` | 2 mm | 0.5–5 mm |
| `maxShovedItems` | 200 | — (abort guard) |
| `walkaroundSlack` | 1.3 | 1.1–2.0 |

**Complexity:** each collision query is O(log n + k) with the spatial index. Worst case
the stack visits `maxShovedItems` items, so a shove is O(M log n), M ≤ 200. All changes
are tentative (copy-on-write) until `ApplyShove` re-runs a final DRC on every moved item;
on any failure the whole shove is discarded atomically.

**Shove vs rip decision:** the autorouter tries in order: walkaround (no disturbance) →
shove (bounded disturbance, all nets stay routed) → rip (cost `ripCost`). This order is
also the order of decreasing "kindness" to already-good routes.

### 3.4 Net / connection ordering

Initial order key (lower = earlier), computed once per run:

1. **Class priority:** power/wide nets first (route on an empty board), as also described
   for Freerouting's ordering by trace width [F2]; then differential pairs / length-
   constrained nets; then ordinary signals.
2. Within a class: **shorter bounding box first** (short connections have few
   alternatives; long ones can detour). Tie-break: fewer alternative layers available
   (pins on one layer only) first; then stable ID.

Dynamic order for iteration/pass k > 1: sort by `(failCount desc, RipCount desc,
congestionTouched desc, staticKey)` — the hardest connections go first so they claim
space while it is free. In Phase A the order matters less (negotiation equalises), so
use the static key with a seeded shuffle within equal keys.

### 3.5 Multi-start with shuffled orders

If after Phase B `unrouted > 0`, run `K-1` further complete attempts (Phase A + B) in
parallel, each with a different seed that shuffles the order **within priority
classes** (never across class 1→3) and perturbs `presFacMult` by ±10 %. The fastroute
README/IMPROVEMENTS document describes rerunning with shuffled, seeded first-pass orders
and keeping the best [F1]. Our selection key, lexicographic:

```
(unrouted, drcViolations, overuseInPhaseA, viaCount, wirelength)
```

Defaults: `K = min(8, NumCPU)`, each attempt bounded by `attemptTimeBudget`
(default 60 s). Seeds = `baseSeed + i`. Results compared after all finish, so the winner
is independent of thread timing. Early exit: if any attempt reaches 0 unrouted and 0 DRC,
other attempts may be cancelled only if the winner rule is then "lowest i among
completed-clean attempts" — still deterministic.

### 3.6 Keeping already-good routes; keep-best and rollback

- **Lock-in:** a connection routed cleanly for `lockAfter` consecutive passes (default 3)
  with zero overuse gets `w(v)` multiplied by `lockFactor` (default 4), making it very
  expensive to rip. Users' locked tracks are infinite cost.
- **Keep-best:** after each iteration/pass, if the selection key (§3.5) improved, store a
  snapshot.
- **Rollback:** if a pass increases `unrouted` by more than `rollbackRatio` (default
  1.5×) or by more than `rollbackAbs` (default 5) relative to the best, restore the best
  snapshot, bump the seed and continue. The fastroute documentation describes undoing
  passes that make the board dramatically worse and resuming from the best state [F1].

### 3.7 Convergence criteria and pass limits

Stop Phase B when any of:

- `unrouted == 0 && drc == 0` (success);
- `pass == maxPasses` (default 30);
- no improvement of the best key for `noImprovePasses` (default 4) passes;
- slow-pass rule: a pass took longer than `slowPassSec` (default 20 s) and improved
  unrouted by < 2 % (this mirrors a stagnation guard described in [F1]);
- global wall-clock budget `timeBudget` exceeded (default 300 s).

### 3.8 Phase C — endgame for the last connections

For each remaining unrouted connection `c` (hardest first):

```
func Endgame(c):
    blockers = connections whose copper lies in c's failed A* frontier region,
               ranked by how many frontier expansions they stopped
    for subset in Subsets(top-B blockers, size 1..S):  // B = 6, S = 3 → ≤ 41 subsets
        snap = Snapshot()
        Rip(subset)
        if Route(c) == OK and RerouteAll(subset) == OK: commit; return OK
        Restore(snap)
    try: layer change (force a via pair near each terminal), relaxed bbox (whole board)
    return FAIL
```

This is a targeted local search; the Freerouting user documentation describes a similar
"examine the last blocked nets and test removing candidate sets" step [F2].
Complexity: ≤ Σ_{s≤S} C(B, s) reroute trials per connection.

---

## 4. Interaction with design rules

All rules are resolved by a `RuleOracle` (spec 03) and consumed here as numbers.

- **Per-net-class width/clearance:** a resource's occupancy in Phase A is computed with
  each net's **clearance-inflated footprint**: a route of width `w` on class `C` marks
  every grid cell within `w/2 + clr(C, *)_max` as used. For pairwise clearance
  `clr(A, B)` the max over classes present is used in Phase A (conservative); Phase B
  and the shove hull use the exact pairwise value.
- **Per-layer widths:** width is `width(class, layer)`; inner layers often allow a thinner
  (or require a different) width from outer layers. The A* step cost and footprint use
  the layer-specific value; a layer change via re-evaluates the footprint.
- **Neck-down at pins:** within `neckRadius` (default: pad's larger dimension) of a
  terminal, the route may use `neckWidth(class)` and the pad-to-track clearance from
  the class. Shove must never widen a neck segment beyond its stored width, and may not
  move a neck segment's pad-attached end.
- **Via sizes:** each class has a via definition (drill, pad diameter per layer span,
  via-to-via and via-to-track clearance). Via sites in Phase A have `cap = 1` and block a
  disk of `padDia/2 + clearance` on every spanned layer. Shove of vias respects
  `maxDepthVia`; blind/buried spans only allowed if the stack-up declares them.
- **Keep-outs:** route/via keep-outs (per layer) are Fixed items: infinite cost in all
  phases, never shoved, never ripped. Via keep-outs block via sites only.
- **Board-edge clearance:** the board outline inset by `edgeClearance + width/2` is a
  Fixed wall; same for slot/cut-outs and mounting holes (with their own clearance).
- **Locked user copper:** treated as Fixed but same-net connection to it is allowed.

---

## 5. Failure modes and handling

| Failure mode | Symptom | Handling |
|---|---|---|
| Oscillation | Unrouted count cycles (e.g. 12→19→12) | rip-once-per-net rule, taboo pairs, history cost, rollback to best |
| Large-net thrash | GND/VCC ripped every pass | power nets routed first; rip only colliding connection, not whole net; `wideFactor` cost |
| Phase A non-convergence | Overuse plateaus > 0 | stall detection → Phase B with best snapshot; multi-start with perturbed `presFacMult` |
| Wire-length blow-up | presFac too high, nets detour wildly | clamp `presFacMax`; Phase-B/optimizer re-straightens; acceptance limit on wirelength |
| Truly blocked terminal | A* fails even ignoring movable copper | mark `UNROUTABLE_FIXED`, report the blocking fixed item; do not rip anything for it |
| Shove cascade explosion | Shove touches hundreds of items | `maxShovedItems`, `maxDepth*`, `maxDisplacement` → FAIL → fall back to rip |
| Shove creates DRC elsewhere | moved item violates clearance to an untouched item | final atomic DRC on moved items; discard whole shove |
| Floating-point drift | hulls slightly overlapping | all geometry in integer nanometres; ε = 1 nm |
| Non-determinism | different results per run | seeded RNG, stable sort, fixed commit order, no map iteration order dependence |
| Time-out | budget hit | return best snapshot with explicit `unrouted` list and reasons |

---

## 6. Test scenarios and acceptance criteria

All boards synthetic, generated in Go test helpers; units mm; default class 0.2 mm
width / 0.2 mm clearance, via 0.6/0.3 mm, 2 layers unless noted.

| # | Scenario | Purpose | Acceptance |
|---|---|---|---|
| T1 | 4 nets that cross pairwise in a 10×10 mm 1-layer area where a planar solution exists only if two nets detour | Negotiation | 100 % completion, 0 DRC, Phase A converges in ≤ 10 iters |
| T2 | Same as T1 on 2 layers | Via use | 100 %, ≤ 2 vias total |
| T3 | Channel of exact capacity: 8 parallel nets through a gap that fits exactly 8 tracks | Capacity/clearance accuracy | 100 %, 0 DRC; with 9 nets → 8 routed, 1 reported unroutable, no crash, < 2 s |
| T4 | Shove: existing track; insert a new track whose straight path is 0.1 mm too close | Push-and-shove | new track routed, old track displaced ≤ 0.5 mm, 0 DRC, 0 rips |
| T5 | Shove chain: 6 parallel tracks; new track forces 6-deep push | Depth limit | succeeds with maxDepthTrace ≥ 6; with maxDepthTrace = 3 → falls back to rip/walkaround, 0 DRC |
| T6 | Via pushed by trace, via next to keep-out | Fixed obstacles | via never enters keep-out; 0 DRC |
| T7 | Oscillation bait: two long nets competing for one corridor, alternative exists 3× longer | Anti-oscillation | converges ≤ 5 passes; no net ripped > 1×/pass |
| T8 | 0.5 mm-pitch QFP-48 fan-out to 40 random sinks, 4 layers, inner width 0.15 / outer 0.2, neck-down 0.12 | Rules | 100 %, 0 DRC, all neck segments ≤ neckRadius |
| T9 | Dense random: 120 two-pin nets, 50×50 mm, 2 layers, utilisation ~60 % | Completion | ≥ 99 % single start; 100 % with K = 8 multi-start; runtime ≤ 30 s on 8 cores |
| T10 | Determinism: T9 run with GOMAXPROCS = 1, 4, 16 | Determinism | byte-identical output |
| T11 | Board-edge: pads 0.3 mm from edge with edgeClearance 0.25 | Edge rule | no copper closer than 0.25 mm to outline |
| T12 | Locked user tracks crossing the natural path | Keep good routes | locked copper unchanged (hash equal) |

Global metrics recorded for every test (and for regression on real boards):
completion % (routed/total connections), DRC violation count (must be 0 for every
committed state), via count, total wirelength vs. sum of Manhattan lower bounds (target
ratio ≤ 1.35 on T9), Phase A iterations, passes, rips, shove count, wall-clock time.
Regression rule: on the benchmark set no metric may regress by > 5 % without a recorded
justification.

---

## 7. Sources

- [P1] L. McMurchie and C. Ebeling, "PathFinder: A Negotiation-Based Performance-Driven
  Router for FPGAs," Proc. 3rd ACM Int. Symp. on FPGAs, 1995.
  https://dl.acm.org/doi/10.1145/201310.201328 ;
  summary: https://sites.lafayette.edu/cadapps/main-page/pathfinder-fpga-routing-algorithm/
- [P2] R. Tessier, "Negotiated A* Routing for FPGAs," 1998.
  http://www.ecs.umass.edu/ece/tessier/fpd98.pdf
- [P3] VPR (Verilog-to-Routing) command-line documentation, router options
  (max_router_iterations, first_iter_pres_fac, initial_pres_fac, pres_fac_mult, acc_fac,
  bb_factor). https://docs.verilogtorouting.org/en/latest/vpr/command_line_usage/
  (option names only; default values in this spec are pcbpilot's own choices).
- [P4] R. Nair, "A Simple Yet Effective Technique for Global Wiring," IEEE Trans. CAD,
  1987. https://ieeexplore.ieee.org/document/1270260/ ; discussion in R. Devereux-Smith
  et al., "Strategic Rip-Up and Reroute," https://dl.acm.org/doi/10.1145/3716368.3735162
  (authors/venue not verified by the audit; background only, nothing in this spec depends on it)
- [P5] US Patent 5,825,659, "Method for local rip-up and reroute of signal paths in an IC
  design." https://patents.google.com/patent/US5825659
- [W1] Wikipedia, "Routing (electronic design automation)" — push-and-shove section.
  https://en.wikipedia.org/wiki/Routing_(electronic_design_automation)
- [K1] KiCad documentation (user manual, interactive router modes: shove / walkaround).
  https://github.com/KiCad/kicad-doc/blob/master/src/pcbnew/pcbnew_interactive_router.adoc
  (user documentation, not source code)
- [A1] Altium, "Push and Shove Router: How it Works and Why You Need It."
  https://resources.altium.com/p/push-and-shove-router-how-it-works-and-why-you-need-it
- [F1] fastroute README and docs/IMPROVEMENTS.md (user-facing feature descriptions only):
  https://github.com/parisxmas/fastroute ,
  https://github.com/parisxmas/fastroute/blob/main/docs/IMPROVEMENTS.md
- [F2] Freerouting user documentation, "How the Autorouter Works."
  https://freerouting.org/freerouting/autorouter-algorithm
- [G1] T. Lozano-Pérez, "Spatial Planning: A Configuration Space Approach," *IEEE Trans.
  Computers* C-32(2), 1983 — growing obstacles by the moving object's shape.
- [G2] M. de Berg, O. Cheong, M. van Kreveld, M. Overmars, *Computational Geometry:
  Algorithms and Applications*, 3rd ed., Springer, 2008, ch. 13 (Minkowski sums).
- IPC-2221 (Generic Standard on Printed Board Design) — conductor spacing context for
  class clearances.

---

## 8. Changelog

- 2026-10-06, clean-room audit: §3.3 now states that the shove cascade, hull, via drag,
  relax-back step and all depth/displacement numbers are pcbpilot's own design and
  values, and cites the configuration-space sources [G1][G2] for the hull; renamed
  "springback" to "relax-back" and "free-space rooms" to "free-space tiles" (neutral
  wording); removed an unsourced claim about typical trace/via nesting depths; marked
  the Devereux-Smith reference as unverified.
