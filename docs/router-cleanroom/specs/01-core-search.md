# Spec 01 — Core Routing Search (gridless, shape-based)

Status: clean-room specification, written only from academic literature, textbooks,
standards and user-facing documentation (see §7). No GPL router source code was
consulted. Target implementation: Go, package `router/search` (MIT).

---

## 1. Purpose and scope

This spec defines how pcbpilot finds **one legal copper path for one two-terminal
connection** on a multi-layer PCB, given everything already on the board. It covers:

- the geometric board model and the spatial index used for clearance queries;
- conversion of design rules into a *configuration space* by Minkowski inflation;
- free-space decomposition into rectangular tiles (corner-stitched planes);
- A* search over tiles across layers, including vias;
- embedding the tile corridor as a concrete orthogonal / 45-degree track;
- the cost model and its parameters.

Out of scope (separate specs): net ordering, rip-up-and-reroute / negotiated
congestion (spec 02), multi-terminal tree construction (spec 03 calls this search
repeatedly, treating already-connected copper of the same net as targets), post-route
optimisation, differential pairs and length tuning, Specctra DSN/SES I/O.

**Contract.** `Route(req) -> (Path, Stats, error)` where `req` names the net, a set
of source items and a set of target items (pads, vias or track pieces of the same
net), the allowed layers and the rule set. The result is a list of track segments
(per layer, with width) and vias that are DRC-clean against the board at call time,
or an error `ErrNoPath` / `ErrBudget`.

## 2. Concepts and data structures

### 2.1 Units and primitives

All coordinates are `int64` nanometres (1 nm resolution; 1 mil = 25 400 nm).
No floating point in geometric predicates; floats are allowed only in costs.

| Type | Meaning |
|---|---|
| `Pt{X,Y}` | point |
| `Rect{MinX,MinY,MaxX,MaxY}` | half-open axis-aligned box |
| `Seg{A,B Pt; HalfW int64}` | track piece = capsule (stadium) of radius `HalfW` around segment AB |
| `Poly` | simple polygon (pads, zones, keep-outs, board outline) |
| `Circle{C; R}` | round pad / via barrel land |
| `Item` | any of the above + `Layer`, `Net` (0 = no net), `Kind` (pad, via, track, keepout, edge, zone), `Fixed bool` |

A track segment's direction must be one of 8 octilinear directions (0°, 45°, …, 315°).
A 45° segment has |dx| == |dy| exactly (integer).

### 2.2 Configuration space (Minkowski inflation)

Routing a track of half-width `w` that must keep clearance `c` from an obstacle `O`
is equivalent to routing a **zero-width centreline** that avoids `O ⊕ Disk(w + c)`
(Minkowski sum). This is the standard configuration-space reduction from robot
motion planning (Lozano-Pérez 1983; de Berg et al., ch. 13). Consequently:

- inflation radius `r = w_track/2 + clearance(netClassA, netClassB, layer)`;
- for a via, `r_via = viaPad/2 + clearance`, applied on **every** layer the via spans;
- the inflated shape of a capsule is a wider capsule; of a polygon it is the polygon
  with rounded corners. For the tile plane (2.4) we use a **conservative rectilinear
  over-approximation** (below); exact checks always use true geometry (2.3).

Because `r` depends on the net class of the net being routed, inflated planes are
cached per `(layer, inflationKey)` where `inflationKey = (trackHalfWidth, clearanceRow)`.
Most boards have 1–4 distinct keys.

### 2.3 Spatial index (exact clearance queries)

Per copper layer, a two-part index:

1. **Static R-tree** (Guttman 1984), bulk-loaded with Sort-Tile-Recursive packing,
   for fixed items: pads, keep-outs, board edge segments, fixed tracks/zones. Node
   fan-out 16.
2. **Dynamic uniform bucket grid** for routed copper (changes on every route and
   rip-up). Bucket size = 4 × (default track width + default clearance), e.g. 1 mm.
   Each item is stored in every bucket its bounding box touches; deletion is O(k).

Queries (all with net filter "ignore items of my net, unless rule says otherwise"):

- `Collides(shape, layer, net, r) bool` — any foreign item within distance `< r`.
- `Nearby(rect, layer) []Item` — candidates for tile building.

Distance kernels needed: point–segment, segment–segment, segment–polygon,
circle–polygon; all in integer arithmetic with `int64` squared distances
(use `math/big` or 128-bit split only when coordinates exceed ±2^31 nm, i.e. 2.1 m —
not reachable on real boards, so assert instead).

### 2.4 Free-space decomposition: corner-stitched tile plane

For each `(layer, inflationKey)` we keep a **corner-stitched plane** (Ousterhout
1984): the layer area inside the inflated board outline is partitioned into
non-overlapping rectangles, each tagged `Space` or `Solid(owner nets)`. Each tile
stores four stitches (right-top, top-right, left-bottom, bottom-left neighbours).
Space tiles are kept **maximal horizontal strips** (merged left/right), which bounds
the number of tiles to O(number of solid tiles) and gives:

- point location in expected O(√n);
- enumeration of a tile's neighbours along one edge in O(neighbours);
- area enumeration in O(tiles touched).

**Rectilinear over-approximation of inflated shapes.** Each inflated obstacle is
inserted as a union of axis-aligned rectangles:

- axis-aligned capsule / rectangle pad: its inflated bounding box, with corner
  notches optionally cut back (see `CornerSteps`);
- 45° capsule or rotated pad: covered by a staircase of rectangles whose step height
  is `StairStep` (default `max(clearance/2, 25 µm)`); every staircase rectangle must
  fully contain the true inflated shape's slice (conservative);
- circle: staircase of `CornerSteps` (default 3) rectangles per quadrant.

Over-approximation can only make the search miss tight gaps; the embedding step
(§3.4) and the exact index (§2.3) are the source of truth. A tile is `Solid` for net
N's search unless every owner of that tile is net N (own-net copper is passable and
is also a valid target).

An A* node is a Space tile on a layer; moving between horizontally or vertically
adjacent Space tiles crosses a **portal** (their shared edge interval). This is the tile-expansion router idea (Margarino et al.
1987; Dion & Monier "Contour" 1995) rather than a fixed-grid Lee router.

### 2.5 Why not a fixed grid

A uniform grid of pitch `p` needs O(A/p²) cells and forces tracks onto multiples of
`p`; off-grid pins (metric parts on an imperial board, fine-pitch BGAs) become
unreachable or require tiny `p`, multiplying memory and run time. Tile planes have
size proportional to the number of obstacles, not to board area, allow any
coordinate, and naturally make a long empty channel one node. The price is more
complex geometry and the need for an explicit embedding step.

## 3. Algorithms

### 3.1 Search state

```
type Node struct {
    Tile   *Tile          // Space tile in plane (layer, key)
    Layer  int
    Entry  Pt             // representative entry point inside the portal
    Dir    Dir8 | None    // direction of arrival (for bend cost)
    G      float64        // cost so far
    Parent *Node
}
```

A node is identified by `(Tile, Layer, DirClass)` where `DirClass` ∈ {horizontal,
vertical, none}; keeping two classes per tile lets bend costs be counted without
exploding the state space.

### 3.2 Cost model

For a move from point `p` to point `q` on layer `L`:

```
step = len8(p,q) * layerFactor[L] * dirFactor(L, q-p)
     + bendCost(prevDir, newDir)
     + congestion(tile)            // 0 in this spec; hook for spec 02
len8(p,q) = max(|dx|,|dy|) + (√2-1)*min(|dx|,|dy|)   // octile distance
```

Via move (same XY, layer L1 → L2): `viaCost * (1 + 0.1*(|L2-L1|-1))` for a
through via; blind/buried vias use their own cost from the via catalogue.

| Parameter | Default | Range | Notes |
|---|---|---|---|
| `viaCost` | 50 × trackPitch (≈ 10 mm for 0.2 mm pitch) | 5–200 × pitch | dominant knob for via count |
| `dirFactor` along preferred dir | 1.0 | — | |
| `dirFactor` against preferred dir (inner layers) | 2.0 | 1.0–4.0 | outer layers default 1.2 |
| diagonal on a layer with H/V preference | 1.4 | 1.0–2.0 | |
| `bendCost` 45° | 0.5 × trackPitch | 0–5 × pitch | |
| `bendCost` 90° | 2 × trackPitch | 0–10 × pitch | 135° turns forbidden (acute angles) |
| `layerFactor` | 1.0 | 1.0–3.0 | raise for layers that should be avoided (e.g. reference planes allowed only as escape) |
| `MaxExpansions` | 200 000 | 10⁴–10⁷ | budget per call |
| `HeuristicWeight ε` | 1.0 | 1.0–1.5 | > 1 gives weighted A* (faster, ≤ ε × optimal) |

All defaults and ranges in this table are pcbpilot's own starting values, to be tuned by
the benchmark; none is taken from another router.

`trackPitch = trackWidth + clearance` of the net class. Preferred directions are
assigned per layer by the stack-up (alternating H/V on inner layers; outer layers
"any").

**Heuristic.** `h(n) = ε * [ minFactor * len8(n.Entry, nearestTargetBox) +
(n.Layer not in targetLayers ? minViaCost : 0) ]`, where `minFactor` is the smallest
`layerFactor*dirFactor` on any allowed layer. With ε = 1 this is admissible and
consistent, so A* returns the cost-optimal corridor (Hart, Nilsson & Raphael 1968).
`nearestTargetBox` uses the bounding boxes of target items; with many targets use the
closest box found by an R-tree nearest query.

Hadlock's detour-number search (Hadlock 1977) is the special case of unit costs,
no vias and ε=1 with Manhattan distance; Lee's wave expansion (Lee 1961) is h ≡ 0.
Both are useful as **test oracles** on tiny boards (§6) but are not the production
search.

### 3.3 Tile A*

```
func SearchTiles(req) (*Node, error):
  open  := binary heap keyed by f = G + h, ties broken by larger G, then insertion id
  best  := map[nodeKey]float64
  for each source item s, for each layer L that s occupies:
      for each Space tile t in plane(L,key) intersecting s's footprint-with-pin-escape:
          push Node{t, L, entry = closest point of s to t, Dir None, G 0}
  exp := 0
  while open not empty:
      n := pop(open)
      if n.G > best[key(n)] { continue }            // stale entry
      if touchesTarget(n.Tile, n.Layer):            // tile overlaps a target item
          return n, nil
      if exp++ ; exp > MaxExpansions { return nil, ErrBudget }

      // (a) planar moves: neighbours across each of the 4 edges
      for each neighbour Space tile m sharing an edge interval I with n.Tile:
          if len(I) == 0 { continue }               // corner contact only
          q := projectClamp(n.Entry, I)             // closest point on portal to entry
          q  = biasToward(q, I, target)            // slide along I toward target ≤ len(I)
          d  := dirOf(n.Entry, q)                   // snapped to nearest of 8 dirs
          relax(n, m, n.Layer, q, d, stepCost(n.Entry, q, n.Layer, n.Dir, d))

      // (b) layer moves: vias
      for each via type v allowed for the net, each layer L2 ≠ n.Layer in v.span:
          site := viaSite(n.Tile, n.Entry, v)       // point inside tile whose via
                                                    // footprint (inflated by via clearance)
                                                    // is free on ALL layers of v.span
          if site == nil { continue }
          t2 := locate(plane(L2,key), site)
          if t2 is Space: relax(n, t2, L2, site, None, cost(n.Entry→site)+viaCost(v))
  return nil, ErrNoPath

relax(n, m, L, q, d, c):
  g := n.G + c
  if g < best[key(m,L,d)] { best[...] = g; push(Node{m, L, q, d, g, n}) }
```

`viaSite` uses a dedicated **via plane** per via type: the tile plane of the layer,
inflated by `viaPad/2 + clearance` instead of `trackHalfWidth + clearance`, intersected
over all spanned layers. In practice: candidate site = `n.Entry` clamped into the
via-plane Space tile containing it; if that tile is Solid, try the 4 corners of
`n.Tile` shrunk inward and the tile centre; first free one wins. Confirm with an exact
`Collides(circle, L, net, viaClearance)` on each spanned layer.

**Complexity.** With T Space tiles per plane, K planes searched (layers) and average
degree D, A* is O((T·K·3) log(T·K·3)) time worst case, O(T·K) memory. Typical
4-layer board: T ≈ 3–10 × obstacle count, a single connection expands 10²–10⁴ nodes.
Plane construction is O(n log n) per plane via sorted insertion; after each committed
route only the affected area is updated (insert solid rectangles), O(k·√n).

### 3.4 Embedding: corridor to octilinear track

The A* result is a sequence of `(tile, layer, portal, via)` steps — a corridor, not a
track. Per layer run between vias:

1. **Funnel / string pulling.** Treat the consecutive portals as a channel and run the
   funnel algorithm (Lee & Preparata 1984 shortest path in a
   triangulated/rectangular channel) to get the shortest polyline through the
   portals. Rectangular corridor ⇒ corners of the polyline are portal endpoints.
2. **Octilinearise.** Replace each polyline edge `(a,b)` that is not octilinear by
   two octilinear segments: a diagonal of length `min(|dx|,|dy|)·√2` and a straight
   of length `||dx|-|dy||`. Choose the order (diagonal first or straight first) that
   stays inside the corridor; test both, prefer the one that keeps the larger
   minimum distance to corridor walls.
3. **Merge** collinear segments; drop zero-length ones.
4. **Pin attachment.** First/last segment enters the pad from its centre (or the
   nearest point of an existing same-net track) along the pad's long axis if possible;
   apply neck-down (§4) if the full width does not fit.
5. **Exact verification.** For every segment and via, `Collides(...)` against the
   exact index. If a segment fails (possible because of staircase approximation
   or octilinearisation), try in order: (a) the other diagonal/straight order;
   (b) insert a midpoint at the portal centre and re-octilinearise both halves;
   (c) mark the offending portal as blocked for this request and **re-run A***
   (at most `MaxReplans` = 4 times). Then fail with `ErrNoPath`.
6. Output segments with width and layer, vias with type and position.

Complexity of embedding: O(P) for P portals, plus O(P · log n) index queries.

### 3.5 Multi-layer details

- Planes exist only for layers in `req.AllowedLayers ∩ netClass.layers`.
- SMD pads exist on one outer layer; through-hole pads and existing vias are
  sources/targets on every layer they span (they act as free layer changes).
- Plane layers (solid pour reserved for GND/power) are excluded from signal
  routing; the via's antipad on a plane layer is a separate rule (§4).
- Blind/buried vias come from the stack-up's via catalogue; each type has a span
  `[L_top, L_bot]`, a pad and drill size and a cost multiplier (default 1.5 for
  blind, 2.0 for buried, 4.0 for micro-via stacking).

## 4. Interaction with design rules

All rules are resolved before search into a `RuleSet` lookup:

```
TrackHalfWidth(netClass, layer)          // outer vs inner layer widths differ
Clearance(classA, classB, layer, kindA, kindB)
ViaTypes(netClass) []Via                 // pad, drill, span, cost multiplier
NeckDown(netClass) (minWidth, maxLen)
EdgeClearance                            // copper to board outline / slots
```

- **Per-net-class width/clearance.** Inflation key depends on `(TrackHalfWidth,
  clearance row)`. When the clearance between two classes differs from the routed
  class's default (class-pair rule), the item is inserted into the plane with the
  **maximum** applicable clearance (conservative), and the exact check uses the
  precise pair value. IPC-2221B tables give minimum conductor spacing by voltage;
  pcbpilot uses them only as a lower bound when a class has no explicit clearance.
- **Per-layer widths.** Outer layers often use a wider track for the same
  impedance/current than inner layers (IPC-2221B charts: external conductors carry
  more current per cross-section). Each layer has its own plane and own
  `TrackHalfWidth`; a via changes the width at the via pad.
- **Neck-down at pins.** Within `maxLen` (default 0.5 mm, range 0–2 mm) of a pad
  edge the track may shrink to `minWidth` (default = manufacturer minimum, e.g.
  0.1 mm) to escape fine-pitch pads. Implementation: around each pad of the net,
  a **neck zone** (pad bbox grown by `maxLen`) is built into a separate narrow plane
  using `minWidth`; the search treats tiles inside the neck zone using the narrow
  plane and switches to the full-width plane outside it. Classes flagged
  `controlledImpedance` get `maxLen = 0` (no neck-down), matching the behaviour
  documented for fastroute's controlled-impedance mode.
- **Via sizes.** Via pad must be ≥ drill + 2 × annular ring (IPC-2221B); the router
  never invents sizes, it only picks from `ViaTypes`. Via-to-via and via-to-pad
  clearance use the via pad, on every spanned layer; hole-to-hole clearance is
  checked on the drill circle in a layer-independent "drill" index.
- **Keep-outs.** Keep-out polygons have flags {noTracks, noVias, noCopper} and a
  layer set. `noTracks` items go into track planes, `noVias` only into via planes.
- **Board-edge clearance.** The outline and internal slots are inserted as Solid
  with inflation `TrackHalfWidth + EdgeClearance` (default 0.3 mm, range 0.2–1.0 mm);
  everything outside the outline is Solid from construction.
- **Own net.** Copper of the routed net is never an obstacle and is always a valid
  target (T-junctions allowed). Copper of net 0 (no-connect) is always an obstacle.

## 5. Failure modes and handling

| Failure | Detection | Handling |
|---|---|---|
| Pin enclosed by obstacles (no Space tile touches pad) | no start nodes | retry with neck-down plane; else `ErrNoPath{reason: PinBlocked}` with the blocking item ids, so spec 02 can rip up |
| Gap exists but staircase approximation closed it | `ErrNoPath` while exact-geometry probe of the gap passes | `StairStep` halved locally around the pin/gap and plane rebuilt for that window (max 2 refinements) |
| Embedding violates DRC | §3.4 step 5 | alternate octilinearisation → midpoint split → block portal + replan (≤ 4) |
| Search explodes on a congested board | `MaxExpansions` hit | return `ErrBudget` with best partial node (closest to target) for diagnostics; caller may raise ε to 1.3 and retry once |
| Via site not found in narrow tile | `viaSite == nil` | skip; the via is attempted from neighbouring tiles naturally |
| Target unreachable on allowed layers | open set empty | `ErrNoPath`; report which layers were reachable |
| Acute angle created at a junction with existing copper | geometry check on the attach angle | add a short 45° stub or attach at the other end of the target segment |
| Numerical overflow | coordinates outside ±(2^30−1) nm | reject board at load time |
| Nondeterminism | — | heap ties broken by insertion counter; map iteration never drives order; same input ⇒ same output on any GOMAXPROCS |

## 6. Test scenarios and acceptance criteria

All boards are synthetic, generated in Go test code; rules unless stated: track 0.2 mm,
clearance 0.2 mm, via 0.6/0.3 mm, 2 layers (Top H-any, Bottom any), edge clearance 0.3 mm.

| # | Scenario | Acceptance |
|---|---|---|
| T1 | Two pads 20 mm apart, empty 30×10 mm board | routed; 0 vias; length ≤ 1.0 × straight distance + 0.01 mm; 1 segment |
| T2 | Same, pads offset by (20, 7) mm | 0 vias; length within 0.5 % of octile distance; ≤ 3 segments |
| T3 | Wall across the board on Top with a 0.65 mm gap (just ≥ 0.2 + 2×0.2 + margin) | routes through gap on Top, 0 vias, 0 DRC |
| T4 | Same wall, gap 0.55 mm (too narrow) and Bottom free | exactly 2 vias; 0 DRC |
| T5 | Wall on both layers, no gap | `ErrNoPath` in < 50 ms |
| T6 | Off-grid pins (metric pads at 0.635 mm offsets on 0.5 mm parts) | routed; 0 DRC (proves gridless) |
| T7 | 0.5 mm-pitch QFP pin escape with neck-down 0.12 mm | escape succeeds; full width resumes ≤ 0.5 mm from pad; 0 DRC |
| T8 | Same as T7 with `controlledImpedance` | either routed at full width or `ErrNoPath`; never a narrowed segment |
| T9 | 4-layer board, inner layers H/V preference, 10 random two-pin nets | ≥ 80 % of length on preferred direction for inner-layer runs |
| T10 | Keep-out `noVias` square covering the only via location | route avoids via in keep-out (either detour or `ErrNoPath`), never a via inside |
| T11 | Lee/Hadlock oracle: 50 random 40×40-cell boards, obstacles on a coarse grid, unit costs, no vias | tile A* cost == Lee shortest path cost on every board |
| T12 | Benchmark: 100×80 mm, 2 layers, 300 pads, 150 two-pin nets routed sequentially | completion ≥ 95 % with this spec alone (no rip-up); 0 DRC violations; mean vias/net ≤ 0.8; total wirelength ≤ 1.25 × sum of octile distances of routed nets; runtime ≤ 5 s on one core (Apple M-class) |
| T13 | Determinism: T12 run 5× with GOMAXPROCS 1 and 8 | byte-identical output |
| T14 | Board edge: pad 0.4 mm from outline | track never closer than 0.3 mm to outline |

DRC is measured by an independent checker (exact geometry, all clearance pairs,
edge clearance, hole-to-hole, acute angles) that shares only the primitive distance
kernels with the router; those kernels have their own unit tests against brute-force
float64 computation.

Metrics logged per call (`Stats`): expansions, tiles in plane, replans, time in
search / embedding / verification, cost breakdown (length, vias, bends).

## 7. Sources

1. C. Y. Lee, "An Algorithm for Path Connections and Its Applications", *IRE Trans.
   Electronic Computers*, EC-10(3), 1961 — wave-expansion maze routing.
2. F. O. Hadlock, "A Shortest Path Algorithm for Grid Graphs", *Networks* 7, 1977 —
   detour-number search.
3. P. E. Hart, N. J. Nilsson, B. Raphael, "A Formal Basis for the Heuristic
   Determination of Minimum Cost Paths", *IEEE Trans. SSC* 4(2), 1968 — A*,
   admissibility and consistency.
4. J. K. Ousterhout, "Corner Stitching: A Data-Structuring Technique for VLSI Layout
   Tools", *IEEE Trans. CAD* 3(1), 1984 — tile planes, stitches, point location.
5. A. Margarino, A. Romano, A. De Gloria, F. Curatelli, P. Antognetti, "A Tile-Expansion
   Router", *IEEE Trans. CAD* 6(4), 1987 — maze search over corner-stitched tiles.
6. J. Dion, L. Monier, "Contour: A Tile-based Gridless Router", DEC WRL Research
   Report 95/3, 1995 — gridless tile router, corridor then embedding.
7. J. Cong, J. Fang, K. Y. Khoo, "DUNE — A Multilayer Gridless Routing System",
   *IEEE Trans. CAD* 20(5), 2001.
   https://www.researchgate.net/publication/3224705_DUNE_-_A_multilayer_gridless_routing_system
8. W. Schiele, T. Krüger, K. Just, F. Kirsch, "A gridless router for industrial design rules",
   DAC 1990. https://www.semanticscholar.org/paper/6d2d436d00d931b6c4d2e3c4d14fb14fc203ead4
9. "A multi-layer gridless area routing algorithm based on non-uniform-grid graph".
   https://www.researchgate.net/publication/4166134 (authors and venue to be verified
   by the next spec editor; background only, no algorithm in this spec depends on it).
10. T. Lozano-Pérez, "Spatial Planning: A Configuration Space Approach", *IEEE Trans.
    Computers* C-32(2), 1983 — obstacle growing by Minkowski sum.
11. M. de Berg, O. Cheong, M. van Kreveld, M. Overmars, *Computational Geometry:
    Algorithms and Applications*, 3rd ed., Springer 2008 — ch. 13 (Minkowski sums,
    configuration space), ch. 10/5 (range searching).
12. D. T. Lee, F. P. Preparata, "Euclidean Shortest Paths in the Presence of Rectilinear
    Barriers", *Networks* 14, 1984 — shortest path through a channel (funnel).
13. A. Guttman, "R-trees: A Dynamic Index Structure for Spatial Searching", SIGMOD 1984;
    S. Leutenegger et al., "STR: A Simple and Efficient Algorithm for R-Tree Packing",
    ICDE 1997.
14. IPC-2221B, *Generic Standard on Printed Board Design*, IPC, 2012 — conductor
    spacing, current capacity, annular ring.
15. Speeding Edge, "PCB Routers and Routing Methods" (grid vs shape-based routers).
    https://www.speedingedge.com/PDF-Files/pcbrouters.pdf
16. "The Mathematics of PCB Trace Routing" (blog overview of searching over lazily
    grown convex free-space regions instead of grid cells). https://tinycomputers.io/posts/the-mathematics-of-pcb-trace-routing.html
17. fastroute README (user-facing feature description only: multi-start ordering,
    controlled-impedance classes without neck-down, class-pair clearances).
    https://github.com/parisxmas/fastroute
18. N. Sherwani, *Algorithms for VLSI Physical Design Automation*, 3rd ed., Kluwer
    1999 — ch. 6 maze routing, line-probe (Hightower, Mikami–Tabuchi), via cost.

## 8. Changelog

- 2026-10-06, clean-room audit: replaced the "expansion room" / "door" wording with
  neutral "free-space tile" / "portal" terms (portal is the funnel-algorithm term of
  Lee & Preparata); removed the "KiCad-style" remark on units; corrected author lists of
  sources 5 and 8; marked source 9 as unverified; stated that the §3.2 cost defaults
  are pcbpilot's own values. No algorithm changed.


## Changelog

- 2026-10-06 (spec-edit, from M1): the coordinate bound is ±(2^30−1) nm (≈ 1.07 m), not ±2^31. At 2^31 a cross product of two coordinate differences needs 65 bits; at 2^30 every kernel is exact in int64 with unsigned 128-bit products (`math/bits`), so no `math/big` path is needed.
