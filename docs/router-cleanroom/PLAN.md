# pcbpilot router rewrite: phase-2 plan

> Status: plan, 2026-10-06. Author: clean-room planner. Process rules: [CLEANROOM.md](CLEANROOM.md).
> Inputs: [gap-analysis.md](gap-analysis.md), specs [01](specs/01-core-search.md), [02](specs/02-ripup-pushshove-negotiation.md),
> [03](specs/03-post-route-optimization.md), [04](specs/04-fanout-escape-rules.md), and pcbpilot's own MIT code.
> No source code of fastroute, Freerouting, KiCad or easyeda-pcb-router was consulted to write this plan.

---

## 1. Goal and success criterion

**Goal.** Build an MIT router engine, `pkg/pcbroute` (engine name `v2`), that beats fastroute on the benchmark
protocol of gap-analysis §4.

**Success criterion.** Every criterion of gap-analysis §4.6 holds on the median of 3 runs. The test runs on the
Gas Module V5 compact board (T4), the other field variants, and the fixture set T1–T3. It is judged by one
referee, pcbpilot's `CheckDRCStrict` plus `checkConnectivity` plus the intent gates. The criteria are:

| # | Criterion | Threshold |
|---|---|---|
| C1 | completion (`completion_all`) | ≥ fastroute on every board; 100 % wherever fastroute reaches 100 % |
| C2 | correctness | `drc_strict` = 0 on every board; T4 native EasyEDA DRC = 0 and pad-net diff = 0 |
| C3 | intent gates | every gate that passes for fastroute passes for v2, with no extra failing items |
| C4 | runtime | total over T1–T4 ≤ fastroute; each board ≤ 1.25× fastroute; Gas V5 time-to-100 % ≤ 432 s on the reference machine |
| C5 | quality | vias ≤ 1.10× and wire length ≤ 1.05× fastroute per board; geometric mean ≤ 1.0× |
| C6 | determinism | completion range ≤ 0.5 points over runs; byte-identical output for the same seed at any `GOMAXPROCS` |

C1–C6 must hold on **two consecutive full bench runs on different days**. The default engine changes only after
that, and only with the user's explicit approval (milestone M17).

**Until then (user decision 2026-10-06):** every pcbpilot release routes with **fastroute behind the hard
gates**, as it does today: `pcb auto route` / `pcb auto run --router fastroute`, followed by intent gates, pours,
native DRC and pad-net diff. Users can call `v2` only through an explicit opt-in (`--router v2`,
`RouteOptions.Engine = "v2"`). The legacy internal router keeps working unchanged, as `--router internal`.

**Interim gates.** gap-analysis §4.6 calls these M1–M3. Here they are renamed G1–G3 so they are not confused
with the milestones.

| Gate | Condition | Reached at |
|---|---|---|
| G1 | T1 + T2 = 100 %, DRC 0, ≤ 2× fastroute time | M11 |
| G2 | Gas V5 compact = 100 %, native DRC 0, pad-net diff 0, ≤ 600 s | M15 |
| G3 | T3 (BGA) within 5 points of fastroute | M16 |
| GA | full acceptance C1–C6 | M17 |

## 2. Target architecture

### 2.1 Package layout (all new code MIT, all under `pkg/pcbroute`)

`pkg/pcbrouting` (the existing single-layer kernel used by the crystal tools) is **not** touched and is not a
base for v2. The near-identical name is deliberate, following gap-analysis §5.1. Every v2 package doc says
"engine v2" to avoid confusion.

| Package | Responsibility | Specs | Depends on |
|---|---|---|---|
| `pcbroute/geom` | int64-nm primitives (`Pt`, `Rect`, `Seg`(capsule), `Circle`, `Poly`, octagon), exact distance kernels, orientation predicates, STR-packed static R-tree, dynamic bucket grid | 01 §2.1–2.3; 03 §2 (spatial index) | stdlib |
| `pcbroute/rules` | one rule resolver: DSN scope precedence, class-pair and type-pair clearances, per-layer widths, neck-down, via catalogue, keep-out/edge, region rules, intent floors (current→width, voltage→clearance), cache | 04 §2, §3.5–3.6, §4; 01 §4; 02 §4; 03 §4 | geom |
| `pcbroute/board` | routing database: items (pad, track, via, keep-out, edge, zone) with net/layer/fixed/soft-fixed flags; nets → connections (spanning-tree edges); union-find connectivity; copy-on-write transactions `Begin/Commit/Rollback`; immutable snapshots | 02 §2.2–2.3; 03 §2 (Transaction, Connection) | geom, rules |
| `pcbroute/dsn` | DSN reader → `board`+`rules`; writer pcbauto `Board`→DSN (bench input); SES writer; SES reader for round-trip tests (reuses `internal/pcb/specctra` S-expression code) | 04 §2 (rule scopes), §3.8; gap §4.2(A) | board, rules |
| `pcbroute/tile` | corner-stitched planes per (layer, inflation key); conservative rectilinear inflation (staircases); incremental insert/delete; via planes | 01 §2.4, §3.3 (via plane) | geom, rules |
| `pcbroute/search` | tile A* for one two-terminal connection: cost model, octile+via heuristic, via moves, budget; corridor embedding (funnel → octilinearise → verify → replan); neck-down plane switch; `Cost` hook for congestion | 01 §3, §4, §5 | tile, board, rules |
| `pcbroute/tree` | multi-terminal nets: connection order (MST / nearest-to-tree), own-net copper as target, T-junction attach | 01 §1 (scope note) | search, board |
| `pcbroute/global` | coarse GCell resource model for Phase A (capacity per GCell edge and layer, derived from the fixed-obstacle tile planes) | 02 §2.1 ("cap > 1 for coarse global tiles"), §3.1; decision D1 | tile, rules |
| `pcbroute/negotiate` | Phase A PathFinder (present/history cost, incremental vs reroute-all, stall), ordering (static/dynamic), keep-best, rollback, multi-start driver | 02 §3.1, §3.4–3.7 | global, tree, board |
| `pcbroute/shove` | walkaround, push-and-shove (octagon hulls, trace push, via push+drag), relax-back, atomic apply | 02 §3.3 | board, geom, rules |
| `pcbroute/rrr` | Phase B detailed passes (walkaround → shove → rip), anti-oscillation (CanRip, taboo, lock-in); Phase C endgame (blocker subsets) | 02 §3.2, §3.6, §3.8 | search, shove, negotiate, board |
| `pcbroute/optimize` | scoring (unclamped, per-net incremental), dangling cleanup, pull-tight, chamfer, via minimisation (local + KL/FM global), reroute-improve, Steiner shift, tail cleanup, rounds | 03 §3.1–3.5, §3.8; 04 §5 (CleanupTails) | board, search (interface), rules |
| `pcbroute/tune` | meanders (length groups), diff-pair coupling regeneration and skew compensation | 03 §3.6–3.7 | optimize, board |
| `pcbroute/escape` | pin-field detection, peripheral stub-out, BGA dog-bone (quadrant), per-layer min-cost-flow escape with node-split tiles, ordered escape heuristic, layer assignment, neck-down pass, via arrays | 04 §2, §3.1–3.5, §3.7 | board, rules, geom |
| `pcbroute` (root) | `Route(ctx, Input, Options) (*Output, error)`: pipeline, budgets, `Session` (checkpoint/resume), stats, adapters from/to `pcbauto` types | gap §5.2; 04 §3.1 | all above |
| `pkg/routerbench`, `cmd/routerbench` | benchmark harness, referee, JSONL rows, Markdown table; `make router-bench` | gap §4 | pcbroute, pcbauto, internal/pcb/specctra |

The packages form a DAG with no cycles. `search` sees congestion only through an injected `CostFn`. `optimize`
reroutes through an injected `Router` interface, so it can be built and tested before `search` exists.

### 2.2 Core data structures

- **Units.** Everything inside `pkg/pcbroute` is `int64` nanometres. Floats appear only in costs. Conversion to
  pcbauto's mil `float64` happens in one adapter file, `pcbroute/pcbauto_adapter.go`. The adapter rounds track
  end points to a shared-endpoint table, so a joint never splits (04 §3.8 rule, applied to both directions).
- **Item** (board): `{ID, Kind, Net, Layer(s), Shape geom.Shape, Width, Fixed, SoftFixed, Owner ConnID}`. Items
  are immutable values. An edit replaces them.
- **Connection** (board): spec 02 §2.2 `Connection` plus `Route *Route` (`[]Item` of tracks and vias).
- **DB** (board): `items []Item`, a per-layer `geom.Index` (static R-tree for fixed items, bucket grid for routed
  ones), a net→connections map, and union-find. `Begin()` returns a `Txn` with a copy-on-write overlay.
  `Snapshot()` copies route pointers and the congestion arrays (02 §2.3).
- **Plane** (tile): corner-stitched tiles `{Rect, Kind(Space|Solid), Owners []NetID, stitches [4]*Tile}`, keyed by
  `(layer, InflationKey{halfWidth, clearanceRow})`.
- **Resolver** (rules): `Width(net, layer, distFromPadEdge)`, `Clearance(a, b ItemRef, layer)`,
  `Vias(net) []ViaType`, `Neck(net)`, `Edge()`, `Keepouts(layer)`, `IntentFloors(net, layer)`. Cached as in 04 §3.6.

### 2.3 Public interfaces

```go
// package pcbroute  (engine v2)
type Input struct {
    Board   *pcbauto.Board; Stackup *pcbauto.Stackup; Plan *pcbauto.Analysis
    Planes  []pcbauto.PlaneRegion   // decided by pcbauto, unchanged
    Fixed   []pcbauto.Track; FixedVias []pcbauto.Via
}
type Options struct {
    Budget time.Duration; WorkRate float64 // virtual clock, reuses pcbauto clock semantics
    Seed int64; Starts int                 // Starts is a fixed count; never derived from NumCPU (D7)
    Threads int; Resume *Session; Phases PhaseMask
}
type Output struct {
    Tracks []pcbauto.Track; Vias []pcbauto.Via; Unrouted []pcbauto.Unrouted
    Stats  pcbauto.RouteStats; Session *Session; Report Report // per-phase metrics
}
func Route(ctx context.Context, in Input, opt Options) (*Output, error)
func RouteDSN(ctx context.Context, dsn []byte, opt Options) (ses []byte, out *Output, err error) // bench path

// package search
type Request struct{ Net NetID; Sources, Targets []ItemRef; Layers LayerSet; Budget int }
type CostFn func(t *tile.Tile, layer int) float64       // spec 02 present/history hook; 0 by default
func Route(db *board.DB, rs *rules.Resolver, req Request, cost CostFn) (Path, Stats, error) // ErrNoPath | ErrBudget

// package optimize
type Router interface{ Reroute(txn *board.Txn, c board.ConnID, w Weights) bool }
func Run(db *board.DB, rs *rules.Resolver, r Router, b Budget) (Report, error)
```

M0 froze these signatures in the package `doc.go` files with these adjustments: `board.DB`, `board.Txn` and
`rules.Resolver` are interfaces, so they are passed by value, not by pointer; `search.Route` takes the read-only
`board.View`, and `Request` lists `board.ItemID`s (there is no separate `ItemRef`); layers are `geom.LayerID`.
In §2.2, `board.Item` has no separate `Width` (a track's width is its `geom.Seg`'s `2·HalfW`), and
`rules.Resolver.Width(net, layer)` is the nominal width while `Neck(net, layer, pad)` returns the 04 §3.5 floor
and zone, from which callers compute `WidthAt`.

### 2.4 Pipeline (engine v2)

```
Route(in):
  db, rs := build(in)                       // or dsn.Read for the bench path
  escape.Fanout(db, rs)                     // soft-fixed stubs/vias (04 §3.1); rip cost escapeCostWeight
  negotiate.PhaseA(db, rs, global grid)     // corridors + order (02 §3.1)
  rrr.PhaseB(db, rs)                        // real geometry; walkaround → shove → rip (02 §3.2)
  rrr.PhaseC(db, rs)                        // endgame for the last connections (02 §3.8)
  if unrouted > 0: negotiate.MultiStart(...) // Starts attempts, deterministic winner (02 §3.5)
  optimize.Run(...); tune.Run(...)          // 03 §3 order (a)–(h)
  escape.NeckDownPass; escape.ViaArrays; optimize.CleanupTails
  gate: CheckDRCStrict via adapter. A violation is a BUG report plus a local rollback
        to the last clean snapshot, never a silent net deletion (gap §5.4)
  return Output + Session
```

### 2.5 Reuse of pcbpilot code

| Reused | How |
|---|---|
| `pcbauto.CheckDRCStrict`, `checkConnectivity` | the referee in the bench and the final gate. It stays independent of `pcbroute/geom`, so the two checks are diverse |
| `pcbauto.Analyze`/`NetPlan`, `electrical.go`, `viasize.go`, `pkg/intent`, `pcb_route_gates.go`, `CheckViaCurrent` | input to `rules` (intent floors); acceptance gates in the bench |
| `pcbauto` stack-up decision, plane split, plane simulation | planes are decided before v2 runs; simulation validates pour connectivity in the referee |
| `bga.go` detection, void geometry, min-cost-flow | `escape` may call or port the MIT code; node-split tile capacity (04 §3.4.B) is new |
| `internal/pcb/specctra` (`FixDSN`, `ApplyNetRequirements`, `ParseSES`, `ParseFixedWiring`, sexpr) | bench I/O; `dsn` reuses the S-expression reader |
| `clock.go` virtual clock | deterministic budgets (`WorkRate`) |
| beautify gate, `MicroFix` | stay as pcbauto pipeline stages after any engine |

### 2.6 Cross-spec decisions (resolving contradictions between the specs)

The four specs were written in parallel, so their cross-references disagree. The **package and spec mapping
above is authoritative**. Specifically:

| # | Conflict | Decision | Reason |
|---|---|---|---|
| D0 | Cross-references: spec 02 calls the geometry kernel and rules "spec 03" and post-route "spec 04"; spec 03 calls DRC "Spec 04"; spec 04 calls RRR "spec 03" and DRC "spec 06" | geometry and index = 01 §2.3 → `geom`; rules = 04 §3.6 → `rules`; RRR = 02; post-route = 03; DRC referee = `pcbauto.CheckDRCStrict` | one file per topic, as listed in §2.1 |
| D1 | Phase A "resource" in shape mode is undefined (02 §2.1 only hints at coarse tiles) | Phase A runs on a coarse uniform GCell grid (default 1 mm, or 4 × dominant track pitch). Edge capacity per layer is computed from fixed-obstacle tile planes. Detailed search (01) then runs per connection with a corridor bonus and the foreign routed copper as Solid | standard global/detail split [Sherwani ch. 5–6]; keeps tile planes free of "shared" copper; Phase B handles the rest |
| D2 | Neck-down zone: 01 = 0.5 mm; 02 = larger pad dimension; 03 = half-diagonal + 0.5 mm; 04 = 1.0 × longer pad side from the pad **edge** | `rules.Neck` from 04 §3.5 is the only source; every package asks the resolver | one rule object (gap §2(d) "Weak") |
| D3 | Edge clearance default: 01 = 0.3 mm, 04 = 0.25 mm or the host rule | host/DSN rule first (pcbpilot `dsn-fix` writes 20/30 mil), otherwise 0.25 mm | 04 §4.5 matches the fixed host behaviour |
| D4 | Clearance between two classes: 03 = max(A, B, layer min); 04 = scope hierarchy, then max of both sides | 04 §3.6 | it includes 03's rule as a special case |
| D5 | Selection key for attempts: lexicographic (02 §3.5) vs scalar score (03 §3.8) | 02 key chooses between router attempts; 03 score decides optimizer edits | different decisions, both needed |
| D6 | 01 forbids 135° turns (acute); 03 treats acute corners as warnings | search never creates acute corners; optimizer only meets them in imported or fixed copper and warns | 01 §3.2 |
| D7 | 02 §3.5 `K = min(8, NumCPU)` breaks cross-machine determinism | `Options.Starts` is a fixed count (default 8). Threads change speed only | 02 §2.2 determinism requirement |
| D8 | Blind/buried/micro vias (01 §3.5, 04) vs through-only fixtures | v2.0 implements through vias only, with the via catalogue type in place; blind/buried is a later milestone if a fixture needs it | no bench board uses them |
| D9 | Arcs (03 §3.2 optional arc mode) | off; SES is written with chords only | EasyEDA import path and the bench do not need arcs |

## 3. Milestones

Conventions:

- Every milestone is one implementer agent, one PR, and one package (or a disjoint file set).
- "Bench" means `make router-bench` (built in M5) on the stated tiers. The row hashes go in the PR.
- Unit-test IDs refer to the spec's §6 table (for example `01-T3` means spec 01 test T3).
- fastroute numbers come from `testdata/router-bench/baseline.jsonl` (committed by M5, metric rows only).

### M0: Skeleton, interfaces, clean-room tooling (lead, sequential, first)

- **Implements:** §2.1–2.3 of this plan; CLEANROOM.md §6.
- **Files:** `pkg/pcbroute/{doc.go, types.go}` and a `doc.go` with frozen exported interface stubs in each
  sub-package (`geom.Shape`, `rules.Resolver` interface, `board.DB`/`Txn` interfaces, `search.Request`/`CostFn`,
  `optimize.Router`); `scripts/cleanroom-check.sh`; CI hook in `Makefile` (`make cleanroom-check`);
  `docs/router-cleanroom/EXPOSURE-LOG.md` (empty table).
- **Tests:** `go build ./pkg/pcbroute/...`; the check script passes on the M0 commit and fails on a fixture commit
  without trailers.
- **Bench acceptance:** none (infrastructure).
- **Depends on:** nothing.

### M1: `geom`: primitives, kernels, spatial index

- **Implements:** 01 §2.1–2.3, §5 (overflow, determinism); 03 §2 `Clear` query semantics.
- **Files:** `pkg/pcbroute/geom/{point.go, shapes.go, dist.go, predicates.go, rtree.go, bucket.go, index.go}` + tests.
- **Unit tests:** every distance kernel against a brute-force float64 oracle on 10⁵ random cases (error ≤ 1 nm);
  45° segments keep |dx| == |dy| exactly; R-tree `Nearby` equals a linear scan; bucket insert/delete/query
  equals a linear scan; `Collides` with a net filter; overflow assertion beyond ±2³¹ nm.
- **Bench acceptance (micro):** load all copper of Gas V5 (from the M3 reader, or a JSON dump of the pcbauto
  board until then): static index build ≤ 50 ms; 10⁶ `Collides` queries ≤ 1 s on one core.
- **Depends on:** M0. **Parallel group:** P1.

### M2: `rules`: resolver and intent floors

- **Implements:** 04 §2 (RuleSet, scopes), §3.5 (`WidthAt`), §3.6 (resolver, clearance types, `use_via`), §4.1–4.6
  (per-layer width, keep-out/edge, intent current→width and voltage→clearance floors); 01 §4; decisions D2–D4.
- **Files:** `pkg/pcbroute/rules/{ruleset.go, scope.go, resolve.go, clearance.go, neck.go, intent.go, ipc2221.go,
  from_pcbauto.go}` + tests. `from_pcbauto.go` builds the resolver from `pcbauto.Analysis`/`NetPlan`/`Stackup`.
  The DSN builder lives in M3 and calls the exported scope API.
- **Unit tests:** 04-T8 (width part), 04-T10, 04-T12; IPC-2221 width table spot checks (2 A / 10 °C / 35 µm ≈ 0.78
  mm); cached lookup < 50 ns (benchmark); intent floor never lowers an explicit rule.
- **Bench acceptance:** on all fixtures, `rules` built from pcbauto `Analysis` gives the same width and clearance
  per net as `pcbauto` (`NetPlan`) within 1 µm. Every difference is listed and explained.
- **Depends on:** M0 (uses only `geom` scalar types). **Parallel group:** P1.

### M3: `dsn`: reader, writer, SES

- **Implements:** gap §4.2(A); 04 §2 (rule scopes from DSN), §3.8 (SES output, shared-endpoint snapping,
  determinism); 03 §4.7 (protect/fix wires as fixed).
- **Files:** `pkg/pcbroute/dsn/{read.go, write.go, ses.go, rules.go}` + `testdata/`. Reuse
  `internal/pcb/specctra/sexpr.go` by import. If `pkg` must not import `internal` by policy, move the generic
  S-expression code to `pkg/pcbroute/dsn/sexpr.go` (pcbpilot's own MIT code; no clean-room issue).
- **Unit tests:** 04-T13 (SES round-trip, byte-deterministic); DSN → board → DSN → board fixpoint on every
  fixture; parse the Gas V5 fixed DSN without errors.
- **Bench acceptance:** for Gas V5 and every fixture, the reader's pad, net and connection counts equal those that
  fastroute reports for the same file in its JSON report (black box, CLEANROOM §4). Gas V5: 462–463 connections.
- **Depends on:** M0; M2's exported scope API (stubbed in M0). **Parallel group:** P1.

### M4: `board`: routing database

- **Implements:** 02 §2.1–2.3 (connections, snapshots, determinism); 03 §2 (transactions, connection anchors),
  §5 (union-find check after edits).
- **Files:** `pkg/pcbroute/board/{db.go, item.go, net.go, connect.go, unionfind.go, txn.go, snapshot.go}` + tests.
- **Unit tests:** transaction commit/rollback restores byte-identical state; nested shove-style overlays; snapshot
  restore; connectivity after random add/remove equals a brute-force BFS; deterministic iteration order (no map
  order).
- **Bench acceptance:** load fastroute's Gas V5 SES (black box) plus the DSN into the DB. Union-find connectivity
  must equal `pcbauto.checkConnectivity` on the same copper. This proves the DB and the referee agree before any
  routing exists.
- **Depends on:** M1, M2 interfaces. **Parallel group:** P2.

### M5: `routerbench`: harness, referee, baseline

- **Implements:** gap §4.1–4.7 completely. It covers fastroute rows (single thread, continuation rounds,
  `crashed` status), legacy-router rows, v2 rows (wired once M8 lands), metrics, A/B interleaving and machine
  records.
- **Files:** `pkg/routerbench/{bench.go, referee.go, fastroute.go, legacy.go, metrics.go, table.go}`,
  `cmd/routerbench/main.go`, `Makefile` target `router-bench`, `testdata/router-bench/{inputs.sha256,
  baseline.jsonl}`, `.gitignore` entry `out/router-bench/` (not ignored today; required by CLEANROOM §4). The Gas
  DSN is read from `PCBPILOT_BENCH_DSN` and never committed.
- **Unit tests:** the referee on a hand-made board with known violations; fastroute missing ⇒ rows `skipped`;
  the JSONL schema is stable.
- **Bench acceptance:** commit `baseline.jsonl` with fastroute and legacy rows for T1–T4 (3 runs each). The
  fastroute Gas V5 row must reproduce the field record: 100 %, `drc_strict` 0, time within ±20 % of 432 s on
  this machine (otherwise record the machine factor). Legacy Gas V5 shows the ≈97 % signal completion seen in
  the field.
- **Depends on:** M3 (Board→DSN writer). **Parallel group:** P2.

### M6: `tile`: corner-stitched planes

- **Implements:** 01 §2.4 (planes, maximal horizontal strips, staircase inflation, own-net passability), §3.3
  (via planes), §5 (local staircase refinement), complexity notes in §3.3.
- **Files:** `pkg/pcbroute/tile/{plane.go, stitch.go, insert.go, delete.go, inflate.go, viaplane.go, cache.go}` + tests.
- **Unit tests:** stitch invariants after random insert/delete (every tile has 4 consistent neighbours, strips
  are maximal, the tiles cover the area exactly once); conservativeness property (no Space-tile point lies within
  the exact inflated distance of an obstacle, checked with `geom`); point location equals brute force; local
  refinement opens 01-T3's 0.65 mm gap.
- **Bench acceptance (micro):** Gas V5 4 layers × all inflation keys: plane build ≤ 300 ms total; incremental
  insert of one routed track ≤ 50 µs median.
- **Depends on:** M1, M2. **Parallel group:** P2.

### M7: `search`: tile A* and embedding

- **Implements:** 01 §3.1–3.5, §4 (neck-down plane switch, via types, keep-outs, edge, own net), §5 (all rows).
- **Files:** `pkg/pcbroute/search/{node.go, heap.go, cost.go, heuristic.go, astar.go, via.go, embed.go,
  funnel.go, octi.go, verify.go, errors.go}` + tests.
- **Unit tests:** 01-T1 … 01-T14 (T11 is the Lee/Hadlock oracle; T13 is determinism at GOMAXPROCS 1/8).
- **Bench acceptance:** sequential routing only (no rip-up), driven by a test-only loop over connections:
  `drc_strict` = 0 on **every** board of T1–T4 (correct by construction); 01-T12 numbers (≥ 95 % on the synthetic
  board, ≤ 5 s); Gas V5 single sequential pass recorded (expected 80–92 %) with median search time per connection
  ≤ 5 ms.
- **Depends on:** M4, M6. **Parallel group:** P3.

### M8: `tree` + engine skeleton (`pcbroute.Route`, `RouteDSN`, adapter)

- **Implements:** 01 §1 scope note (multi-terminal through repeated two-terminal search with own-net targets);
  gap §5.2 (Input/Output/Options); §2.2 adapter; SES output path.
- **Files:** `pkg/pcbroute/tree/{order.go, grow.go}`, `pkg/pcbroute/{route.go, route_dsn.go, pcbauto_adapter.go,
  session.go(stub), stats.go}`; `pkg/routerbench/v2.go` (adds v2 rows).
- **Unit tests:** a 5-pin net builds a tree with ≤ 1.15 × the rectilinear MST length on an empty board; the
  adapter's nm↔mil round-trip keeps joints (connectivity unchanged); `RouteDSN` writes SES that `specctra.ParseSES`
  reads.
- **Bench acceptance:** first v2 rows in `results.jsonl` for T1–T4 with `drc_strict` = 0 everywhere. T1 completion
  ≥ 85 %; Gas V5 ≥ 85 % in ≤ 120 s.
- **Depends on:** M5, M7. **Sequential.**

### M9: `global` + `negotiate`: Phase A, ordering, keep-best, rollback

- **Implements:** 02 §3.1 (PathFinder, incremental vs `rerouteAll`, stall), §3.4 (ordering), §3.6 (keep-best,
  rollback), §3.7 (convergence), §4 (conservative footprints in Phase A); decision D1.
- **Files:** `pkg/pcbroute/global/{gcell.go, capacity.go}`, `pkg/pcbroute/negotiate/{cong.go, pathfinder.go,
  order.go, snapshot.go, stall.go}` + tests; `search` gets a corridor `CostFn`.
- **Unit tests:** 02-T1, 02-T2, 02-T3, 02-T7; the parameter sweep in §3.1 runs as a table test; determinism.
- **Bench acceptance:** T1 ≥ 98 %, T2 ≥ 95 %, Gas V5 ≥ legacy's referee completion on the same input
  (`completion_all`), all with `drc_strict` = 0 and runtime ≤ 3× fastroute.
- **Depends on:** M8. **Sequential.**

### M10: `shove`: walkaround and push-and-shove

- **Implements:** 02 §3.3 (hull, trace push, via push and drag, relax-back, limits, atomic apply), §4 (neck
  segments not widened, pad-attached ends fixed; via shove respects `maxDepthVia`; keep-outs never entered), §5
  (cascade guard, DRC elsewhere).
- **Files:** `pkg/pcbroute/shove/{hull.go, push_trace.go, push_via.go, walkaround.go, relaxback.go, apply.go}` + tests.
- **Unit tests:** 02-T4, 02-T5, 02-T6, 02-T12; property test: after any successful shove, `geom` exact checks
  give 0 violations among moved items and between moved and untouched items.
- **Bench acceptance (offline, before M11):** "shove repair" mode on legacy-router output for T1/T2. For every
  connection the legacy final gate dropped, try to insert it with shove only. Report how many it recovers, with
  `drc_strict` = 0 (target: ≥ 30 % of them). This measures shove alone, on real geometry.
- **Depends on:** M4 (+ M1, M2). **Parallel group:** P3 (independent of M6–M9).

### M11: `rrr`: Phase B passes, Phase C endgame, multi-start

- **Implements:** 02 §3.2 (RouteWithObstacles, CanRip rules 1–5), §3.5 (multi-start, deterministic winner, D7),
  §3.6 (lock-in), §3.7, §3.8 (endgame subsets), §5 (oscillation, large-net thrash, time-out).
- **Files:** `pkg/pcbroute/rrr/{passes.go, obstacles.go, canrip.go, taboo.go, endgame.go}`,
  `pkg/pcbroute/negotiate/multistart.go` + tests.
- **Unit tests:** 02-T7 (with Phase B), 02-T8, 02-T9, 02-T10, 02-T11.
- **Bench acceptance:** **gate G1**: T1 + T2 = 100 %, `drc_strict` 0, ≤ 2× fastroute time. Gas V5 ≥ 99.5 %.
- **Depends on:** M9, M10. **Sequential.**

### M12: `optimize`: score and geometric passes

- **Implements:** 03 §2 (transactions, budget), §3.1 (pull-tight A+B), §3.2 (chamfer; arcs off, D9), §3.3 (via
  minimisation: local, merge, shortcut, KL/FM global), §3.4 (reroute-improve through the `Router` interface),
  §3.5 (Steiner shift), §3.8 (score), §4, §5; 04 §5 `CleanupTails`.
- **Files:** `pkg/pcbroute/optimize/{score.go, round.go, dangling.go, pulltight.go, chamfer.go, vias.go,
  vias_global.go, reroute.go, steiner.go, tails.go}` + tests.
- **Unit tests:** 03-T1 … 03-T8, 03-T12 … 03-T15 (T14 idempotence; T15 fixed copper byte-identical).
- **Bench acceptance:** (a) on fastroute's own SES for T1–T4 (black-box input, CLEANROOM §4), the optimizer
  never makes the score worse and keeps `drc_strict` 0 and completion unchanged. This shows the optimizer is safe
  on foreign copper. (b) On v2 output (after M11 is merged): vias −15 % median, wire length −5 % median, and
  vias ≤ 1.10× and wire length ≤ 1.05× fastroute on T1 + T2.
- **Depends on:** M4 (a, with a mock `Router`); M11 for part (b). **Parallel group:** P3 (start), finished after M11.

### M13a: `escape`: detection, peripheral, dog-bone, neck pass, via arrays

- **Implements:** 04 §3.1–3.3, §3.4.A (quadrant dog-bone, `ViaFits`, via-in-pad policy), §3.5 (neck-down pass,
  necked retry), §3.7 (via arrays), §4.4–4.5, §5 rows 1, 4, 6, 7.
- **Files:** `pkg/pcbroute/escape/{detect.go, peripheral.go, dogbone.go, neck.go, viaarray.go, softfix.go}` + tests.
- **Unit tests:** 04-T1, 04-T3, 04-T4, 04-T5, 04-T8, 04-T9, 04-T11.
- **Bench acceptance:** Gas V5 U8 GND pins escape **without** hand-made `--escapes` (bench setting "no
  pre-escapes" for both routers). After M11, T3 completion is ≥ 10 points higher than v2 with escape disabled.
- **Depends on:** M4, M2. **Parallel group:** P3.

### M13b: `escape`: network-flow escape, ordered escape, layer assignment

- **Implements:** 04 §3.4.B (node-split tile capacity, successive shortest paths with potentials, decomposition,
  crossing swap), ordered-escape heuristic, layer assignment from outside in, §5 rows 2–3.
- **Files:** `pkg/pcbroute/escape/{flowgraph.go, mcmf.go, decompose.go, ordered.go, layers.go}` + tests. The
  `bga.go` min-cost-flow code may be ported (pcbpilot MIT).
- **Unit tests:** 04-T2, 04-T6, 04-T7; a 1 000-ball BGA escapes in < 1 s.
- **Bench acceptance:** the 6L synthetic BGA board completes ≥ 90 % (legacy with BGA on: 84.8 %). After M11, K230
  and RK3568 are ≥ 5 points above M13a alone.
- **Depends on:** M13a. **Parallel group:** P4.

### M14: `tune`: length groups and differential pairs

- **Implements:** 03 §3.6 (meanders, exact last bump, tolerance), §3.7 (follower regeneration, skew bumps near the
  source, accordion for pairs); intent length and skew requirements from `pcbauto`/`pkg/intent`.
- **Files:** `pkg/pcbroute/tune/{length.go, meander.go, pair.go, skew.go}` + tests.
- **Unit tests:** 03-T9, 03-T10, 03-T11.
- **Bench acceptance:** criterion C3: on every board with length or pair intent, every `checkIntentLengths`
  gate passes for v2 wherever it passes for fastroute, with no extra failing items.
- **Depends on:** M12. **Parallel group:** P4.

### M15: Integration, session resume, CLI, live T4 (gate G2)

- **Implements:** §2.4 pipeline order; gap §1.1/§5.2 (dispatcher `RouteOptions.Engine = "legacy" | "v2"`, default
  `legacy`); `Session` checkpoint/resume (gap §2(b) "Missing"); `pcb auto run --router v2` and
  `pcb auto route --router v2` beside `fastroute`, with the same hard gates. The pcbauto pipeline does **not** run
  stack escalation or grid retries for v2 (gap §5.2).
- **Files:** `pkg/pcbroute/{pipeline.go, session.go}`; `pkg/pcbauto/router.go` (dispatcher only, ≤ 30 lines);
  `pkg/pcbauto/pipeline.go` (skip the retry multiplication for v2); `internal/app/cmd_pcb_auto*.go` (flag);
  `docs/cli/pcb.md`, `docs/pcbauto.md` (opt-in documentation).
- **Unit tests:** resume from a session gives the same final result as an uninterrupted run with the same total
  budget (virtual clock); the dispatcher's default is still `legacy`; CLI help.
- **Bench acceptance:** **gate G2**: Gas V5 compact 100 %, `drc_strict` 0; live EasyEDA import, pour rebuild,
  native DRC 0, pad-net diff 0; ≤ 600 s.
- **Depends on:** M11, M12, M13a, M14 (M13b optional). **Sequential.**

### M16: Performance and BGA closing (gate G3)

- **Implements:** profiling-driven work within the existing specs. Examples: tile-plane incremental updates,
  heuristic target index (01 §3.2 R-tree nearest target), parallel multi-start with a fixed commit order
  (02 §2.2), search budget tuning, and parameter sweeps (02 §3.1 ranges, 03 weights). No new algorithms without a
  spec edit (CLEANROOM §5).
- **Files:** existing packages only; `docs/router-cleanroom/tuning.md` (sweep results).
- **Bench acceptance:** **gate G3**: T3 within 5 points of fastroute; per-board runtime ≤ 1.25× fastroute; Gas V5
  time-to-100 % ≤ 432 s; determinism C6.
- **Depends on:** M15, M13b. **Sequential** (it may be split by package if profiles show independent hot spots).

### M17: Acceptance run and default-switch decision (gate GA)

- **Implements:** §1 C1–C6 on two consecutive full bench runs on different days. The results go in
  `docs/router-cleanroom/ACCEPTANCE.md`, with the row hashes and machine record.
- **Change:** only after the user's explicit approval, `pcb auto run` and `pcb auto route` default to `v2`. fastroute
  remains selectable for one release.
- **Depends on:** M16. **Sequential.**

## 4. Phase-2 staffing

```
P1 (parallel, 3 agents):  M1 geom | M2 rules | M3 dsn                  (after M0 by the lead)
P2 (parallel, 3 agents):  M4 board (M1) | M5 routerbench (M3) | M6 tile (M1,M2)
P3 (parallel, 4 agents):  M7 search (M4,M6) | M10 shove (M4) | M12 optimize part a (M4) | M13a escape (M4,M2)
sequential spine:         M8 tree+engine (M5,M7) → M9 negotiate (M8) → M11 rrr (M9,M10)
P4 (parallel, 3 agents):  M12 part b (M11) | M13b flow escape (M13a) | M14 tune (M12)
sequential tail:          M15 integration → M16 performance → M17 acceptance
```

- **Critical path:** M0 → M1 → M6 → M7 → M8 → M9 → M11 → M15 → M16 → M17. Staffing should put the strongest
  implementers on M6, M7 and M11.
- **Conflict avoidance:** each milestone owns its package directory. Shared files are touched only by named
  milestones: `Makefile` (M0, M5), `pkg/pcbauto/*.go` (M15 only), `internal/app/**` (M15 only), and M0's interface
  stubs. An interface change after M0 needs a CRO-approved PR of its own before dependants rebase.
- **Reviewers:** each PR is reviewed by an agent from a different parallel slot, which saves agent count. The
  reviewer runs CLEANROOM §7 and the spec tests.
- **Worktrees:** one git worktree per parallel agent on a branch `router-v2/<milestone>`, merged into `dev` in
  dependency order.
- **Exposure precondition:** only agents that have stated they are unexposed (CLEANROOM §8.6) may take
  implementer slots.

## 5. Risks and open questions

### 5.1 Risks

| # | Risk | Impact | Mitigation |
|---|---|---|---|
| R1 | Corner-stitching bugs: stitch corruption, non-maximal strips, slow deletes | search wrong or slow; blocks the critical path | M6 invariant property tests; staircase conservativeness test; fallback design: run `search` on a bucket-grid free-space plane behind the same interface (01 §2.5 notes the trade-off) |
| R2 | Embedding failures in narrow gaps (staircase over-approximation, octilinearisation) | completion loss on fine pitch | 01 §5 replan ladder; local `StairStep` refinement; tracked as a per-board `replans` metric |
| R3 | Phase A on GCells (D1) gives corridors that the detailed search cannot realise | non-convergence like the legacy router's (gap §3.1) | capacities computed from exact free space; corridor is a cost bonus, not a hard wall; Phase B/C repair; measured by M9's bench |
| R4 | Shove geometry robustness (hull walking, via drag chains) | DRC escapes or a slow Phase B | atomic apply with exact recheck; limits (02 §3.3); M10 property tests; fall back to rip |
| R5 | Runtime: fastroute does Gas V5 in 432 s, while legacy needs 594–1840 s, mostly from pipeline multiplication | C4 fails | v2 runs once with session resume; no escalation or grid retries (M15); virtual-clock profiling (M16) |
| R6 | Determinism versus parallelism | C6 fails; flaky bench | fixed `Starts` (D7), seeded RNG, stable sorts, fixed commit order; GOMAXPROCS 1/8 tests in every milestone |
| R7 | LLM implementers reproduce code memorised from training data | licence contamination that the exposure log cannot catch | specs as the only input; reviewers ask where every constant comes from; optional similarity audit by a **separate auditor** who is not an implementer. The auditor runs an automated code-similarity tool over our code against the forbidden code and reports only our file and line. The auditor is then exposed and is excluded from implementing (CLEANROOM §8) |
| R8 | Referee mismatch with native EasyEDA DRC: a via touching only a pour is not counted as connected in desktop 3.2.149 (gap §3.2) | G2 passes offline but fails live | bench column `plane-dependent`; live T4 confirmation in M15; plane-terminating connections end on real copper by default |
| R9 | nm (v2) ↔ mil float (pcbauto) rounding breaks joints or creates sub-µm violations | false DRC or opens in the referee | one adapter with a shared-endpoint table (§2.2); M8 round-trip test; `MicroFix` stays available |
| R10 | fastroute 0.1.7 crashes in parallel mode | unfair or invalid comparison | compare single-thread against single-thread; record `crashed` (gap §4.3) |
| R11 | Scope creep (blind vias, arcs, length-aware search) | schedule | D8 and D9 defer them; new features need a spec edit plus a CRO decision |

### 5.2 Open questions (merged from the specs, the gap analysis and this plan)

| # | Question | Owner / when |
|---|---|---|
| Q-L1 | Legal: is the "ideas from GPL projects' user docs, no code" approach enough for MIT release? Should a lawyer review CLEANROOM.md before M17? | user, before M17 |
| Q-L2 | Which existing sessions or agents (if any) have opened `_ext/` or `easyeda-upstream/`? Record in EXPOSURE-LOG.md before P1 starts | user, before M1 |
| Q-L3 | Is the similarity audit (R7) wanted? If yes, who acts as the exposed auditor? | user, before M15 |
| Q1 | D1 GCell size and capacity formula: 1 mm or 4 × pitch; capacity from tile free width or from IPC channel formula (04 §2 `cap(g,w,s)`) | M9 implementer → spec 02 edit |
| Q2 | Should Phase A congestion also feed the tile search directly (01 §3.2 `congestion(tile)` hook), or only through GCell corridors? | M9, measured |
| Q3 | Plane and pour nets: how v2 terminates connections on planes, and how the referee counts them against native DRC (R8) | M15 + spec 04 edit |
| Q4 | Differential pairs: is post-route regeneration (03 §3.7) enough to match fastroute's "pairs routed first", or is pair-as-unit search needed (legacy `pairroute.go` idea)? | after M14 bench; spec edit if needed |
| Q5 | Length matching during search (Ozdal & Wong) vs post-route meanders only (03 §3.6). Decide only if C3 fails | after M14 |
| Q6 | Neck-down for controlled-impedance classes: 01 and 04 both forbid it. Confirm the intent flag name and the DSN mapping | **Decided (M2):** `rules.Intent.NoNeckDown`, set when intent `widthMil.min` ≥ `widthMil.outer` (same test as `specctra.forbidsNeckdown`) or for impedance / diff / RF nets; DSN maps it as class `MinWidth = Width`, so `Neck.Zone = 0` (no new keyword) |
| Q7 | Default for `walkaroundSlack` (1.3) and `ripLengthRatio` (4), which are pcbpilot's own guesses | M11 sweep |
| Q8 | Via-in-pad: only with `via_at_smd on`. Does any fixture's DSN from `pcb dsn-fix` set it? | M13a |
| Q9 | Escape ordering λ (04 §3.4) default off. Turn it on for connector-bound buses? | M13b bench |
| Q10 | Should pre-route escapes (`--escapes`) be given to both routers in the headline comparison, or to neither? Both are benched; which row is headline? | user, at M5 |
| Q11 | Reference machine for C4's 432 s (the field record's machine vs the bench machine); is a machine factor acceptable? | user, at M5 |
| Q12 | Can `pkg/pcbroute/dsn` import `internal/pcb/specctra` (it is legal in Go within the module), or should the S-expression code move to `pkg/`? | **Decided by the lead in M0 (2026-10-06): import.** `pkg/pcbroute/dsn` imports `internal/pcb/specctra`; no code moves. The S-expression reader there is unexported today, so M3 adds a small exported read API in `specctra` (pcbpilot's own MIT code, no clean-room issue) instead of copying it. Reason: one parser for EasyEDA DSN quirks; a `pkg/` move would only matter if pcbroute were imported from outside the module, which nothing needs |
| Q13 | Arc support (D9) if a future EasyEDA import path accepts arcs | post-M17 |
| Q14 | Blind, buried and micro-vias (D8): which future board needs them? | post-M17 |

## Sources

- pcbpilot: `docs/router-cleanroom/gap-analysis.md` (benchmark protocol §4, module boundaries §5, field results
  §3), specs 01–04 and their source lists; `pkg/pcbauto/{router.go, drc.go, pipeline.go, bga.go, clock.go}`,
  `internal/pcb/specctra/*.go`, `pkg/pcbrouting/routing.go` (package doc), `Makefile`.
- N. A. Sherwani, *Algorithms for VLSI Physical Design Automation*, 3rd ed., Kluwer, 1999. Global vs detailed
  routing; GCell capacity models (basis of D1).
- L. McMurchie, C. Ebeling, "PathFinder: A Negotiation-Based Performance-Driven Router for FPGAs," FPGA 1995
  (Phase A, cited through spec 02).
- J. K. Ousterhout, "Corner Stitching: A Data-Structuring Technique for VLSI Layout Tools," *IEEE TCAD* 3(1),
  1984 (tile planes, risk R1).
- T. Yan, M. D. F. Wong, "A Correct Network Flow Model for Escape Routing," DAC 2009 (M13b, cited through spec 04).
- Free Software Foundation, GPL FAQ, "GPLOutput" entry, https://www.gnu.org/licenses/gpl-faq.html#GPLOutput
  (black-box use of fastroute output, see CLEANROOM.md §4).
- Wikipedia, "Clean-room design," https://en.wikipedia.org/wiki/Clean-room_design (process background).
