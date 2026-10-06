# Spec 03 — Post-Route Optimization

Status: clean-room specification for the pcbpilot router (Go, MIT).
This spec was written using only academic papers, textbooks and user-facing documentation, as listed in Section 7. It contains no GPL source code and nothing derived from it. All pseudo-code here is original.

---

## 1. Purpose and scope

The global/detailed router (Specs 01–02) produces a result that is *legal* (connected and DRC-clean) but rarely *good*. Typical leftovers are staircase jogs from grid search, detours that were only needed while other nets were missing, extra vias from layer hopping, and high-speed nets with no length or coupling control.

Post-route optimization takes a fully or partially routed board and changes it so that a cost score goes down. It must never:

1. disconnect a net that was connected,
2. add a DRC violation (clearance, width, via, keep-out or board edge),
3. go over its time budget.

In scope:

- **Pull-tight (string pulling):** removes unneeded corners and shortens each trace run.
- **Corner smoothing:** changes 90° corners and acute corners into 45° chamfers. An optional arc mode is included.
- **Via minimization:** removes vias by moving a segment chain to another layer, and merges via pairs that are redundant.
- **Rip-and-reroute improvement:** reroutes one connection at a time and keeps the result only when the score improves.
- **Length matching:** adds serpentine (meander) tuning to reach a target length or to match lengths within a group.
- **Differential pairs:** coupled routing, gap control, and skew compensation.
- **Final scoring and reporting.**

Out of scope: initial routing, placement, and impedance field solving. Spec 03 uses target widths and gaps for impedance and does not compute them.

## 2. Concepts and data structures

**Track.** A polyline `Track{Net, Layer, Width, Pts []Point}` with integer nanometre coordinates. Every vertex has a *corner angle*: the turn between the incoming and outgoing segment, from 0° to 180°.

**Via.** `Via{Net, Pos, Drill, Pad, FromLayer, ToLayer}`. A via with only two layers involved is a *through-transition*. A via that touches copper on only one layer is a *dangling via* and is always removed.

**Connection.** One edge of a net's routed tree between two *anchors*. An anchor is a pad, a via, or a T-junction. Optimizers work on one connection at a time. The anchors stay fixed unless the pass says otherwise.

**Spatial index.** A layer-aware R-tree or uniform bucket grid over all copper and keep-outs, with each shape inflated by its own clearance. The required query is `Clear(shape, net, layer) bool`. It returns true when no foreign-net object is within `clearance(netclassA, netclassB, layer)`. Every move in this spec is validated through this one query. This keeps DRC semantics the same as in Spec 04, the DRC engine.

**Transaction.** Every edit is staged as `Edit{Removed []Obj, Added []Obj}`. The edit is applied to a copy-on-write view, checked, and then either committed or rolled back. This is the "clone, test, keep only if no violation" discipline that fastroute's docs also describe for tuning [F1].

**Score.** A scalar with lower values better. It is computed incrementally per net (see §3.8).

**Budget.** `Budget{Deadline time.Time, MaxRejectStreak int}`. Every loop checks `time.Now() < Deadline` at the top of each iteration. The rule is: an expired budget returns the best state committed so far and never a half-applied edit.

## 3. Algorithms

Recommended pass order (one *round*): (a) dangling cleanup, (b) pull-tight, (c) via reduction, (d) pull-tight again, (e) rip-up/reroute improvement, (f) corner smoothing, (g) length tuning and diff-pair skew, (h) final score. Repeat (b)–(f) while the score improves by more than `epsRound` (default 0.2 %) and budget remains. Tuning (g) runs last because any later shortening would undo it.

### 3.1 Pull-tight (string pulling)

Idea: a route is a taut string held by its anchors and pressed against obstacles. The shortest path that keeps the same homotopy class among obstacles can be found by "funnel" string pulling [L1, H1]. Freerouting's manual describes the same user-visible effect: corners are removed for as long as the shorter line keeps clearance [FR1].

We use a practical, grid-free version made of two stages. Stage A removes vertices greedily. Stage B slides vertices along obstacle boundaries.

```
func PullTight(t *Track, idx SpatialIndex, allowAny bool) bool:
  changed := false
  // Stage A: shortcutting, farthest-reach first
  i := 0
  out := [t.Pts[0]]
  while i < len(t.Pts)-1:
     j := len(t.Pts)-1
     while j > i+1:
        seg := Segment(t.Pts[i], t.Pts[j])
        if (allowAny or IsOctilinear(seg)) and idx.Clear(Inflate(seg, t.Width/2), t.Net, t.Layer):
           break
        j--
     out.append(t.Pts[j]); if j > i+1 { changed = true }
     i = j
  t.Pts = out
  // Stage B: corner sliding (handles octilinear-only mode)
  for each interior vertex v with neighbours a, c:
     for d in {toward a, toward c} step = max(grid/2, 1µm), binary search:
        try moving v along d to v'; replace (a,v,c) by (a,v',c) or by (a, k, c)
        where k = octilinear knee of a→c if one exists
        accept if total length decreases and all new segments Clear
  merge collinear consecutive segments
  return changed
```

- Complexity: Stage A uses O(n²) clearance queries in the worst case per track (n = vertices, usually < 50). Each query costs O(log N) with the R-tree. Stage B runs O(n · log(1/step)) queries.
- `allowAny` defaults to false (octilinear only: 0/45/90/135°). With `allowAny = true` the router may emit any-angle segments when the board style allows it.
- For a two-point shortcut between a and c under octilinear rules, try both "knee" variants: diagonal first or straight first. This is the standard two-bend octilinear connection [S1]. Keep the shorter clear one.
- Pull-tight must keep the **pad entry rule** (§4.3). The first and last segments may not be removed when a neck-down or a perpendicular escape is required.

### 3.2 Corner smoothing (45° chamfers, optional arcs)

Every 90° corner or sharper corner at vertex v (angle between the incoming and outgoing direction ≤ 90°) gets replaced by a chamfer.

```
func Chamfer(t *Track, idx, cmin, cmaxFrac):
  for each interior vertex v (a→v→c):
     if TurnAngle(a,v,c) < 90°: continue     // already obtuse (e.g. 135°)
     L := min(|av|, |vc|) * cmaxFrac         // default cmaxFrac = 0.5
     for c := L; c >= cmin; c /= 2:          // cmin default = 2*Width
        p := v + unit(a - v)*c ; q := v + unit(c - v)*c
        if idx.Clear(Seg(p,q) inflated) : replace v by p,q ; break
```

- Acute corners (< 90°) are always refined. If no chamfer fits, the corner is reported as a warning (`acute-corner`) and not as an error.
- Arc mode (optional, default off) replaces the chamfer by a tangent arc with radius `r = c·tan(θ/2)`. Clearance is checked with an arc-to-shape distance query. If the export format cannot represent arcs, arcs are flattened to chords with sagitta ≤ 1 µm.
- Complexity: O(n · log(L/cmin)) queries per track.

### 3.3 Via minimization by layer reassignment

Background: after routing, the number of vias can often be reduced by changing layer assignments without moving geometry. This is the classic constrained via minimization (CVM) problem. It is NP-hard in general, but good heuristics exist on a *conflict graph* [V1, V2, V3].

Model per net (local version, cheap):

1. Split each connection into *runs*: maximal same-layer polylines between vias or anchors.
2. For each via that joins run A (layer La) and run B (layer Lb), try to remove it in one of two ways:
   - Move B to La as it is (same geometry): `Clear(B on La)`, and B's far anchor must be reachable on La (SMD pad on La, through-hole pad, or another via).
   - Or move A to Lb (the symmetric case).
3. If the move succeeds, delete the via, merge the runs, and run PullTight on the merged run.

Global version (optional, for boards with many vias): build a graph where the vertices are runs and the edges are vias. A layer label per run induces a cost of (vias + conflicts). Each run's feasible layers are the layers on which `Clear` holds and its anchors are reachable. Solve with iterative improvement in the style of Kernighan–Lin / Fiduccia–Mattheyses: flip one run's layer, accept if the cost drops, keep a tabu list of the last k = 8 flips. For 2-layer boards this is the bipartite/max-cut view [V1].

```
func ReduceVias(net, idx, budget):
  improved := true
  while improved and budget.ok():
    improved = false
    for via in net.Vias sorted by (shortest adjacent run first):
       for (run, target) in [(B, La), (A, Lb)]:
          if AnchorsReachable(run, target) and idx.Clear(run on target):
             commit(remove via; move run); improved = true; break
```

Also apply:
- **Via merge.** Two vias of the same net with centres < `pad + clearance` apart, joined by a short stub, become one via.
- **Via-in-place shortcut.** A sequence L1 → via → L2 → via → L1 whose L2 part is shorter than `viaDetourMax` (default 3 mm) is tested as a direct L1 segment via pull-tight on the merged L1 path.

Complexity: O(V · R) clearance queries per sweep (V = vias, R = run length in segments). Converges in ≤ 3 sweeps on typical boards.

### 3.4 Rip-up and reroute improvement (greedy, budgeted)

The fastroute docs describe an optimizer that tests many candidate rip-ups and applies the best ones greedily. Only the ripped nets are rerouted, and the optimizer stops after about 10 consecutive rejections [F1]. Freerouting describes a similar loop: remove one connection, reroute it, and keep it only if the board improved [FR2]. The general approach is standard negotiated rip-up-and-reroute in the style of PathFinder [P1].

```
func ImproveByReroute(board, budget, maxRejects=10):
  cands := connections sorted by descending (length/manhattan ratio + 2*vias)
  rejects := 0
  for c in cands while budget.ok() and rejects < maxRejects:
     before := Score(board)            // incremental: only nets touched
     snap := board.Begin()
     board.Remove(c)
     ok := DetailRoute(c, costWeights{via: high}, window = bbox(c) inflated 2 mm)
     if ok: PullTight(c); ReduceVias(c.net)
     if ok and Score(board) < before - eps: board.Commit(); rejects = 0
     else: board.Rollback(snap); rejects++
```

Skip connections that were already unrouted when the pass started. Their retry belongs to the router and not to the optimizer [F1]. Parameters: `maxRejects` (default 10, range 3–50), `window inflation` (1–5 mm), `eps` (score units equal to 0.05 mm of wire).

### 3.5 Wirelength reduction

Wirelength drops through 3.1, 3.3 and 3.4. In addition, a **T-junction Steiner shift** applies: for a net tree with a junction vertex J joining three branches, move J toward the geometric median of its three neighbours, then re-pull-tight. This is the Fermat-point improvement for Steiner trees [S2]. Accept only if the total length drops and every segment is still clear.

### 3.6 Length matching with meanders

Definitions:
- `len(net)` is the sum of segment lengths plus, for each via, the dielectric height crossed. The fastroute docs describe measuring trace length plus via stackup height [F1]. Pad-to-die length can be added as an optional per-pin value.
- A *tuning group* G has a target `T` (explicit, or `max len` in G) and a tolerance `tol` (default 0.1 mm, range 0.01–2 mm).

Meander geometry (trombone/serpentine): a straight segment of length `s` gets a series of U-shaped bumps. Each bump has amplitude `A` (perpendicular height) and pitch `p` (centre-to-centre spacing of the parallel legs). One full bump adds about `2A` of extra length. Each leg becomes A long, and the top and bottom connectors stay about the same length. Use 45° chamfers on all bump corners (§3.2) or arcs. Rules of thumb from the literature: keep the gap between parallel legs of the same net ≥ 3–4× the trace width (or ≥ 4× the dielectric height) to limit self-coupling, which shortens the effective delay [M1, M2]. Prefer few large bumps over many small ones.

```
func Tune(net, target, tol, idx, p Params):
  deficit := target - len(net)
  if deficit <= tol: return OK
  segs := net's straight segments not in pad-escape zones, sorted by length desc
  for s in segs while deficit > tol:
     for side in {left, right}:
        for A := p.Amax; A >= p.Amin; A -= p.Astep:
           n := floor((|s| - 2*p.margin) / p.pitch)     // max bumps that fit
           need := ceil(deficit / (2*A))
           k := min(n, need)
           shape := MeanderPolyline(s, side, A, p.pitch, k)
           shape = AdjustLastBump(shape, deficit)       // shrink last bump's A
           if idx.Clear(shape inflated, net, layer): commit; deficit -= added; goto next s
  if deficit > tol: report "tune-short" with remaining deficit
```

Defaults: `Amin = 3·w`, `Amax = 10·w` (w = trace width), `pitch = 2·gap + 2·w` with `gap = 3·w`, `margin = pitch` from each segment end. The final bump amplitude is solved exactly so the residual is ≤ 1 µm. Accordion-style tuning on *diff pairs* applies the same bump to both members, with the outer member's amplitude increased by the pair pitch so that the gap is kept.

Academic context: Ozdal and Wong treat length matching as resource allocation during routing (Lagrangian relaxation) [M3]. Later work handles dense meander regions after routing with ILP [M4] and any-angle obstacle-aware tuning [M5]. We use the simpler post-route local insertion above. When local tuning fails, the documented fallback is a reroute of the net with a soft minimum-length cost, which is out of scope here.

Complexity: O(S · (Amax−Amin)/Astep · 2) clearance checks per net, where S is the number of candidate segments.

### 3.7 Differential pairs: coupled routing and skew

Pair rule: `w` (width), `g` (edge-to-edge gap), `uncoupledMax` (default 3 mm in total), `skewMax` (default 0.1 mm length or a time equivalent).

Coupled routing as a post-route step: if the pair was routed as two independent nets, rebuild it by keeping the **leader** (P) and regenerating the follower (N) as an offset of P's centreline by `±(w+g)`. Offsetting uses a standard polyline offset with miter handling. At 45° corners, the inner member's offset vertex is pulled in and the outer one is pushed out. Try both offset sides and both leader choices, and keep the variant with the best score and no violations. The fastroute docs describe the same "try both, keep if better" policy [F1]. Pad fan-out zones near the pins are uncoupled. Connect them with the shortest clear segments and count them toward `uncoupledMax`.

Skew compensation:
1. Compute `Δ = len(P) − len(N)`.
2. If |Δ| > skewMax, add small single-ended bumps ("sawtooth" or small trombone) to the shorter member. Place them **close to the source of the mismatch**, i.e. next to the bend that caused it, so the pair re-phases quickly. This is the guidance in signal-integrity references [M2].
3. Bump amplitude ≤ g so that the coupling is only locally disturbed.

Pair length tuning (group matching) then treats the pair as one object of length `(len(P)+len(N))/2` and uses accordion meanders (§3.6).

### 3.8 Scoring

The score is used both for accept/reject decisions and for reporting:

```
Score = Wunrouted * unroutedConnections        (1e6 per connection)
      + Wdrc      * drcViolations              (1e5 each; optimizer never accepts >0 delta)
      + Wvia      * viaCount                   (default = equivalent of 1.5 mm wire)
      + Wlen      * totalWirelength_mm         (1.0)
      + Wcorner   * (acuteCorners*5 + rightCorners*1)
      + Wtune     * sum over groups of max(0, |len - target| - tol) (mm, weight 20)
      + Wpair     * (skewExcess_mm*20 + uncoupled_mm*2)
```

All weights above are pcbpilot's own starting values (to be swept in tests); none is taken from another router. The score is **unclamped**: never floor it at zero. The fastroute docs note that clamping stopped acceptance of improvements on boards that were already heavily penalised [F1]. Also report a normalised 0–100 quality index for users, computed separately from the raw score. Keep per-net contributions so that a candidate edit re-scores only the touched nets: O(touched) instead of O(board).

## 4. Interaction with design rules

All passes get rules from one resolver `Rules(netA, netB, layer, region)` so that they never use different values.

1. **Per-net-class width/clearance.** A track's width is `max(class.width, layer.minWidth)`. The clearance between objects is `max(classA.clr, classB.clr, layer.minClr)`. A rule area (region) can override it. Pull-tight and meanders use the clearance from the pair matrix and never a global value.
2. **Per-layer widths.** Outer and inner copper may have different widths. Examples: a 50 Ω target gives a wider microstrip on outer layers and a narrower stripline on inner layers, and IPC-2221 current tables give different allowable widths for internal and external conductors [I1]. Via reduction (§3.3) **must re-width the moved run** to the target layer's width and re-check clearance with the new width. If the result does not fit, the move is rejected.
3. **Neck-down at pins.** Within `neckLen` (default: pad's half-diagonal + 0.5 mm) of a fine-pitch pad, width may drop to `class.neckWidth`. Optimizers treat the neck segment as fixed geometry: pull-tight may shorten it but not remove it. Meanders and chamfers never go into the neck zone.
4. **Via sizes.** Every new or kept via uses `class.via` (drill/pad). The micro-via or blind/buried span rule must be legal in the stackup. Via merge (§3.3) is only allowed when the two vias have the same padstack and span.
5. **Keep-outs.** Track keep-outs, via keep-outs and copper keep-outs are inserted into the index as foreign objects with zero clearance (the keep-out boundary is the hard edge). A via keep-out blocks via merge or relocation even when track copper may pass.
6. **Board edge.** The outline and slots are obstacles with clearance `edgeClr` (default 0.3 mm, or by the fab rule). Meanders near the edge are tested with the full inflated shape.
7. **Locked/fixed copper.** Objects marked fixed (user-routed, Specctra `protect`/`fix` types per the DSN format [D1]) are never modified. They are still obstacles.

## 5. Failure modes and handling

| Failure | Detection | Handling |
|---|---|---|
| Edit causes a DRC violation | `Clear` false on staged edit | Roll back. Never commit a partial edit. |
| Edit breaks connectivity | Union-find check on the net after edit | Roll back. Log it as a bug, because staged edits should never do this. |
| Oscillation (A undoes B) | Score did not improve over one round | Stop rounds. Tabu list on flips (§3.3). |
| Budget exhausted mid-pass | Deadline check at loop top | Return last committed state with `partial=true` in the report. |
| Tuning cannot reach target | Deficit > tol after all segments | Report `tune-short(net, deficit)`. Leave best partial meander. Optional reroute fallback. |
| Tuning overshoots | Deficit < −tol | Shrink last bump, or remove the bump with the smallest effect. |
| Diff pair cannot couple (obstacle between) | Offset follower fails Clear | Keep the original N. Add the gap to the uncoupled length. Warn if > uncoupledMax. |
| Layer move violates per-layer width | Re-width check | Reject move. Via stays. |
| Acute corner cannot be chamfered | No chamfer size fits | Keep it and emit an `acute-corner` warning. |
| Floating-point drift | Integer nm coordinates, exact orientation predicates | Snap every generated vertex to 1 nm. Octilinear directions use integer offsets. |
| Huge board (time) | Candidate count | Process nets by descending score contribution, so the expensive nets are fixed first. |

Every pass must be **idempotent on a fixed point**: running the same pass twice in a row changes nothing the second time. This is a test property.

## 6. Test scenarios and acceptance criteria

Each case is a small synthetic board in the internal format (or DSN). Metrics come from the scorer and the independent DRC (Spec 04). Runtime limits are for a single core on a 2024 laptop.

| # | Scenario | Setup | Acceptance |
|---|---|---|---|
| T1 | Staircase removal | 1 net, 2 pads 20 mm apart diagonally, routed as a 1-cell staircase on a 0.1 mm grid | After pull-tight: ≤ 3 segments, length ≤ 1.01 × octilinear optimum, 0 DRC |
| T2 | Obstacle hug | T1 plus a 5×5 mm keep-out on the straight line | Path wraps the keep-out at exactly the clearance distance (±1 µm), length ≤ 1.02 × optimum, 0 DRC |
| T3 | Right-angle chamfer | 10 tracks, each with an L-shape | 0 corners ≤ 90° remaining, 0 DRC, length decreases |
| T4 | Via removal | 2-layer, net routed top→via→bottom→via→top with bottom free of conflicts | Via count 2 → 0, length not increased by > 1 % |
| T5 | Via removal blocked | Same as T4 but a foreign track on top overlaps the bottom run | Vias unchanged, 0 DRC |
| T6 | Per-layer width | 4-layer, inner width 0.1 mm, outer 0.15 mm, run moved inner→outer in a gap of 0.32 mm with 0.1 mm clearance | Move rejected (0.15 + 2·0.1 > 0.32 fails), or done with correct width if gap allows. Never wrong width |
| T7 | Neck-down preserved | 0.5 mm pitch QFN escape with neck 0.1 mm / normal 0.2 mm | Neck segments present after all passes, 0 DRC |
| T8 | Reroute improvement | 20-net 2-layer board with 3 deliberate detours | Total length −10 % or better, vias not increased, completion unchanged (100 %), 0 DRC, < 2 s |
| T9 | Length group | 8-bit bus, lengths 30–42 mm, target max, tol 0.1 mm, free space beside traces | All 8 within ±0.1 mm, 0 DRC, meander gap ≥ 3w, < 1 s |
| T10 | Tuning infeasible | Same as T9 but no space | Report `tune-short` per net with exact deficit, board unchanged elsewhere, 0 DRC |
| T11 | Diff pair skew | Pair with 4 × 45° bends all in one direction, 0.6 mm initial skew | Skew ≤ 0.1 mm, gap = g ±1 µm in coupled parts, uncoupled ≤ 3 mm, 0 DRC |
| T12 | Edge clearance | Meander candidate segment 0.5 mm from board edge, edgeClr 0.3 mm | No copper within 0.3 mm of edge |
| T13 | Budget | 2 000-connection board, budget 3 s | Returns ≤ 3.2 s, `partial` flag set if unfinished, score ≤ input score, 0 new DRC |
| T14 | Idempotence | Run full pipeline twice on T8 output | Second run: 0 edits |
| T15 | Fixed copper | T8 with 5 tracks marked fixed | Fixed tracks byte-identical after optimization |

Global acceptance (regression suite of 10 real-ish boards):
- completion % never decreases,
- new DRC violations = 0,
- via count reduced ≥ 15 % median versus raw router output,
- wirelength reduced ≥ 5 % median,
- all tuning groups that are feasible end within tolerance,
- runtime ≤ the configured budget plus 5 %.

## 7. Sources

- [L1] D. T. Lee, F. P. Preparata, "Euclidean shortest paths in the presence of rectilinear barriers," *Networks* 14(3), 1984. The funnel / string-pulling shortest path in a triangulated corridor.
- [H1] J. Hershberger, J. Snoeyink, "Computing minimum length paths of a given homotopy class," *Computational Geometry* 4(2), 1994.
- [S1] Wikipedia, "Octilinear" / X-architecture routing; and N. Sherwani, *Algorithms for VLSI Physical Design Automation*, 3rd ed., Kluwer, 1999 (routing models, via minimization chapter).
- [S2] Wikipedia, "Fermat point" and "Steiner tree problem," https://en.wikipedia.org/wiki/Steiner_tree_problem
- [V1] "An efficient approach to multi-layer layer assignment with application to via minimization," ACM DAC 1997, https://dl.acm.org/doi/10.1145/266021.266294
- [V2] "Layer assignment for multi-layer PCB and VLSI routing," *J. Chinese Inst. Engineers* 14(4), 1991, https://www.tandfonline.com/doi/abs/10.1080/02533839.1991.9677348
- [V3] B. W. Kernighan, S. Lin, "An efficient heuristic procedure for partitioning graphs," *Bell Syst. Tech. J.*, 1970; C. M. Fiduccia, R. M. Mattheyses, DAC 1982 (iterative improvement used for layer flips).
- [P1] L. McMurchie, C. Ebeling, "PathFinder: a negotiation-based performance-driven router for FPGAs," FPGA 1995.
- [M1] "A New Slant on Matched-Length Routing," ICD, 2012, https://www.icd.com.au/articles/Matched_Length_Routing_PCB-Jan2012.pdf
- [M2] E. Bogatin, *Signal and Power Integrity — Simplified*, 3rd ed., Prentice Hall, 2018 (serpentine self-coupling, diff-pair skew compensation near the source).
- [M3] M. M. Ozdal, M. D. F. Wong, "A length-matching routing algorithm for high-performance printed circuit boards," *IEEE TCAD* 25(12):2784–2794, 2006, https://www.researchgate.net/publication/3225949
- [M4] "Post-route alleviation of dense meander segments in high-performance PCBs," arXiv:1705.04983; and ILP version arXiv:1705.04984.
- [M5] "Obstacle-aware length-matching routing for any-direction traces in PCB," DAC 2024, https://doi.org/10.1145/3649329.3655915
- [M6] "A unified PCB routing algorithm with complicated constraints and differential pairs," ASP-DAC 2021, https://dl.acm.org/doi/10.1145/3394885.3431568
- [I1] IPC-2221B, *Generic Standard on Printed Board Design* (conductor width/current for internal vs external layers, electrical clearance).
- [F1] fastroute user documentation, docs/IMPROVEMENTS.md (feature descriptions only: greedy optimizer, `--tune`, `--pairs`, unclamped score), https://github.com/parisxmas/fastroute (README and docs).
- [FR1] Freerouting Reference Manual — Routing / Routing Options (pull-tight description), https://freerouting.org/freerouting/manual/routing-options and https://freerouting.org/freerouting/manual/assist-manual-routing
- [FR2] Freerouting, "How the Autorouter Works" (user-level optimizer description), https://freerouting.org/freerouting/autorouter-algorithm
- [D1] Cadence, *SPECCTRA Design Language Reference*, Product Version 10.0, 2000 — wire `type` attribute (`protect`, `fix`, …) in the wiring and session descriptors. Same document as spec 04 source 1.

## 8. Changelog

- 2026-10-06, clean-room audit: cited the Specctra language reference [D1] for the
  `protect`/`fix` wire types instead of an unspecified "public DSN references"; stated
  that the §3.8 score weights are pcbpilot's own values.
