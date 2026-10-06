# Spec 04 — Fan-out, Escape Routing and Rule Semantics

Status: clean-room specification for the pcbpilot autorouter (Go, MIT licence).
Sources were limited to papers, standards, the Specctra language reference, vendor and fab application notes, and the user-facing README/docs of other routers. No router source code was read. Section 7 lists the sources.

---

## 1. Purpose and scope

This spec covers the parts of the router that run **before** and **around** the main maze/negotiated router:

1. **Fan-out / escape routing**: getting every pin of a dense part (fine-pitch QFP/QFN, BGA) out to a point where the general router can reach it. This means a short stub plus a via (dog-bone), or a stub to the edge of the pin field.
2. **Rule resolution**: one function that answers "what width, clearance and via apply to this object, on this layer, next to that object?" It follows Specctra DSN precedence, and it adds per-layer widths, neck-down at pins, board-edge clearance and keep-outs.
3. **Intent-derived constraints**: turning "this net carries 3 A" or "this net is at 230 VAC" into width, clearance and via-count rules the router consumes.
4. **Via arrays** for high-current nets.
5. **SES output**: how routed geometry is written back.

Out of scope: the core path search (spec 01/02), rip-up and reroute (spec 03), length matching and differential pairs (other specs). These subsystems call into this one.

---

## 2. Concepts and data structures

All coordinates are integer nanometres (`int64`) to avoid float drift. DSN/SES values are converted using the file's `resolution`.

```go
type LayerID int16
type NetID  int32
type ClassID int32

type LayerKind uint8 // Signal, Power(plane), Mixed
type Layer struct {
    ID LayerID; Name string; Kind LayerKind
    Outer bool        // top or bottom copper
    CopperUm float64  // finished copper thickness, e.g. 35 outer, 17.5 inner
}

// One rule set: only fields that are set override lower levels.
type RuleSet struct {
    Width      *int64           // nominal track width
    MinWidth   *int64           // neck-down floor
    Clearance  map[ClrType]int64 // per clearance type (see below)
    UseVia     []string         // allowed via padstack names, preference order
    ViaArray   *ViaArraySpec
    NeckZone   *int64           // max neck-down length measured from a pad edge
}

// Clearance type: an object pair, or one of the special kinds.
type ObjKind uint8 // Pin, SMD, Via, Wire, Area(keepout/boundary), TestPoint
type ClrType struct{ A, B ObjKind; Special uint8 } // A<=B canonical; Special=SameNetSMDVia, SameNetViaVia, ...
```

**Rule scopes.** The Specctra reference sets this order, lowest precedence first: pcb < layer < class < class-layer < group_set < … < net < net-layer < group < … < fromto < fromto-layer < class_class < class_class-layer < padstack < region < class-region < net-region < class_class-region [Specctra §"Routing and Placement Rule Hierarchies"]. pcbpilot implements a subset. Each scope is a `RuleSet`, keyed by a scope key:

| Precedence (low→high) | Scope key | Source |
|---|---|---|
| 0 | `pcb` | `(structure (rule …))` |
| 1 | `layer L` | `(layer L (rule …))` |
| 2 | `class C` | `(class C … (rule …))` |
| 3 | `class C, layer L` | `(class C … (layer_rule L (rule …)))` |
| 4 | `net N` | `(net N (rule …))` |
| 5 | `net N, layer L` | `(net N (layer_rule L …))` |
| 6 | `class_class {C1,C2}` | `(class_class (classes C1 C2) (rule …))` |
| 7 | `class_class {C1,C2}, layer L` | `layer_rule` inside class_class |
| 8 | `region R` (+class/net) | `(structure (keepout/region …) (rule …))` |
| 9 | `intent` (pcbpilot) | derived from current/voltage; see §4.6 |

The intent scope does not *override* explicit rules. Its values are merged as **floors**: the final width is `max(explicitWidth, intentWidth)`, and the same applies to clearance. The board designer can make a rule stricter but can never make it unsafe. This is a pcbpilot policy decision and not Specctra semantics.

**Escape problem objects.**

```go
type PinField struct {          // a dense component
    Pitch      int64
    Rows, Cols int
    Pads       [][]*PadRef        // nil where depopulated
    Kind       FieldKind          // BGA, QFP, QFN
}
type EscapeSlot struct {         // BGA: the "tile" between 4 balls (a dog-bone via site)
    R, C int; Center Point; ViaFits bool
}
type EscapeResult struct {
    Net NetID; Pad *PadRef
    Stub []Segment; Via *ViaPlacement; ExitLayer LayerID
    ExitPoint Point             // hand-off point for the general router
}
```

**Channel capacity.** Take two adjacent pads with pad size `d` at pitch `p`. The free gap is `g = p − d`. The number of tracks of width `w` and clearance `s` that fit through the gap is

```
cap(g, w, s) = max(0, floor((g − s) / (w + s)))
```

The pad-to-wire clearance `s_pw` can differ from wire-to-wire `s_ww`. In that case the general form is `floor((g − 2·s_pw + s_ww) / (w + s_ww))`.
A **dog-bone via fits** in the tile between four balls if the diagonal gap `p·√2 − d ≥ D_via + 2·s_pv`, where `s_pv` is pad-to-via clearance and `D_via` is the via land diameter.
Industry guidance agrees with this: dog-bone fan-out works down to about 0.8 mm pitch with standard vias. Around 0.5 mm and below, via-in-pad or microvias are normally needed [JLCPCB BGA guide; AtlasPCB].

---

## 3. Algorithms

### 3.1 Pipeline

```
RouteBoard(board):
  rules  := BuildRuleResolver(board.dsnRules, board.intent)          // §3.6
  fields := DetectPinFields(board.components)                         // §3.2
  order  := OrderFields(fields)   // densest/largest first
  for f in order: EscapeField(f, rules)                               // §3.3–3.4
  MarkEscapesAsSoftFixed()        // the main router may rip them up, at a high cost
  MainRoute(board, rules)         // other specs
  NeckDownPass(); ViaArrayPass(); CleanupTails()                      // §3.5, §3.7
  WriteSES()                                                          // §3.8
```

### 3.2 Detecting pin fields

A component gets escape treatment if one of these holds:

- it has ≥ 16 pads on a regular 2-D lattice (BGA), or
- it has ≥ 4 pads per side in a row (QFP/QFN/SOIC fine pitch), **and** the pitch is ≤ `escapePitchThreshold`.

The default threshold is 0.65 mm for peripheral packages and 1.0 mm for BGA; the configurable range is 0.3–1.27 mm.
Lattice detection: sort the pad centres. Take the most common non-zero dx and dy as the pitch. Map each pad to `(round(dy/p), round(dx/p))`. Accept the lattice if ≥ 90 % of pads land within `p/10` of a lattice point. Cost is O(n log n).

### 3.3 Peripheral packages (QFP/QFN/SOIC)

Pins are already on the package edge, so escaping is a **stub-out** problem:

```
EscapePeripheral(f):
  for each side S, for each pad P on S (in order along the side):
    if P.net is unconnected or a plane net with a via allowed: handle the plane case below
    dir := outward normal of S
    w   := rules.WidthAt(P.net, P.layer, nearPad=true)   // neck-down width, see §3.5
    L   := max(stubMin, padLength/2 + s_pw)               // default stubMin = 0.25 mm
    stub := segment(P.center, P.center + dir*L)           // width w
    if stub conflicts: try widths w..minWidth in 4 steps; else leave unescaped
  plane nets (GND/VCC with a plane layer):
    place a via at P.center + dir*(L + D_via/2 + s_pv) and connect it with the stub
  QFN exposed/thermal pad: never route through it; it is a keep-out for other nets
```

Fanning out in staggered rows avoids via collisions when vias are placed outside a fine-pitch QFP. The via of pad *i* goes at distance `L₁` if *i* is even and at `L₂ = L₁ + D_via + s_vv` if *i* is odd. Use this only when `pitch < D_via + s_vv`.
Cost: O(pads × attempts).

### 3.4 BGA escape

There are two strategies, chosen by geometry.

**A. Dog-bone fan-out (pitch where `ViaFits` is true).**

```
EscapeBGA_DogBone(f):
  // 1. Outer rings escape on the top layer with no via.
  k := number of rings escapable on the top layer
       = 1 + cap(p − d, w, s)       // ring 0 always, plus one ring per track that fits between balls
  for each pad in rings < k: route a straight or 45° stub out of the field on the top layer
       (assign channels left to right per side; this is the ordered-escape case)
  // 2. Inner pads use dog-bone vias in quadrant directions.
  for each inner pad P at (r,c):
     quad := the quadrant of P relative to the field centre
     slot := the tile at (r + dr(quad), c + dc(quad))   // diagonal toward the outside, e.g. (+½,+½)
     if slot is taken or !slot.ViaFits: try the other 3 diagonals, nearest-to-outside first
     place a via at slot.center; a 45° stub from the pad edge to the via, at neck-down width
  // 3. Unconnected pads: no via (saves space). Plane-net pads: via to the plane, no further routing.
  // 4. Inner layers: run §3.4.B on each signal layer to carry the vias out of the field.
```

The quadrant pattern (vias pointing away from the centre) leaves a clear cross-shaped channel along the field's centre lines. Many layout guides recommend this for routing the inner layers [AtlasPCB; PCBSync BGA guide].

**Via-in-pad avoidance.** Default `allowViaInPad=false`. The Specctra counterpart is the `via_at_smd` rule plus `(attach on (use_via V))` [Specctra, attach descriptor]. If the pitch is too small for a dog-bone (`!ViaFits`), the router does not fall back to via-in-pad on its own. It reports `ErrEscapeNeedsVIP` with the minimum via that would fit. Via-in-pad is used only if the rules allow it (`via_at_smd on`), with the padstack named in `use_via`. Reason: via-in-pad needs filled and capped vias, which cost more and must appear on the fab drawing.

**B. Network-flow escape (per layer).** This is a model in the style of Yan & Wong [DAC'09]. The goal is to bring the largest number of pins (or vias from the layer above) to the field boundary on one layer, without crossings.

Graph construction for one layer:

```
nodes:
  for each pin/via that must escape on this layer: source node s_i (supply 1)
  for each tile (gap between 4 balls): tile node T, split into T_in→T_out with capacity tileCap
  for each channel between 2 adjacent balls: edge between the 2 neighbouring tiles, capacity cap(p−d,w,s)
  super-sink t; every boundary tile connects to t with capacity = its outward channel capacity
edges:
  s_i → the 4 tiles around pin i (capacity 1, cost = stub length)
  tile ↔ tile through a channel (capacity = channel cap, cost = p)
  tileCap = how many wires can turn inside the tile. Diagonal capacity:
     floor((p·√2 − d − s) / (w + s)); this is the "diagonal capacity" fix from Yan & Wong
solve: min-cost max-flow (successive shortest path with potentials)
```

Node splitting with a tile capacity is the key fix in the "correct" model. Without it, the flows through a tile can need more diagonal room than really exists, and the result cannot be legalised [Yan & Wong 2009]. Successive shortest path costs O(F · E log V), where F is the number of escaping pins. For a 30×30 BGA, V ≈ 2k and E ≈ 8k, so this runs in milliseconds.

**Flow decomposition to geometry.** Peel unit paths from the flow (DFS on edges with positive flow, decrement as you go). Inside each channel, assign the k wires passing through it to evenly spaced track positions. Order them by the side they enter from, so that no wires cross. If two paths would cross inside a tile, swap their continuations; this is valid because both are unit flows of interchangeable nets. Then convert each path to 45°/90° segments.

**Escape ordering (net order at the boundary).** If the far-side component wants nets in a set order (a bus to a connector), plain max-flow can scramble that order. Ordered escape routing [Kong, Yan & Wong; Ozdal & Wong] fixes it. pcbpilot uses a cheap heuristic and leaves the full ILP aside:
1. Compute the target boundary order from the angle of each net's *destination* as seen from the field centre.
2. Give each boundary edge to sink t an extra cost: `λ · |rank(net) − rank(boundary slot)|`. This needs a multi-commodity view. The approximation is to run the flow one side at a time, assigning nets to slots greedily in angle order.
3. Default λ = 0 (unordered); turn it on with `escapeOrdered=true` per field.

**Layer assignment across layers.** Escape layer by layer, from the outside inwards: the top layer takes the outer rings, the next signal layer takes the next rings, and so on [Ozdal & Wong, simultaneous escape + layer assignment]. Pins that cannot escape on layer L push their dog-bone via down to layer L+1. Stop when all are escaped or the layers run out. Report the rest.

Parameters (defaults; range). These are pcbpilot's own starting values, except where a row cites a source:

| Parameter | Default | Range |
|---|---|---|
| `escapePitchThreshold` BGA / periph | 1.0 / 0.65 mm | 0.3–1.27 |
| `stubMin` | 0.25 mm | 0.1–1.0 |
| `allowViaInPad` | false | bool |
| `escapeOrdered` | false | bool |
| `escapeMaxLayers` | all signal layers | 1–N |
| `escapeCostWeight` (rip-up cost for soft-fixed escapes in the main router) | 10× | 1–100× |

### 3.5 Neck-down at pins

A wide net (power, or high current) often cannot reach a fine-pitch pad at full width. pcbpilot allows a **neck-down zone**: inside a distance `NeckZone` from the pad edge, the width may drop to `MinWidth`.

```
WidthAt(net, layer, distFromPadEdge):
  w    := Resolve(net, layer).Width
  wmin := Resolve(net, layer).MinWidth  (defaults to w, i.e. no neck-down)
  zone := Resolve(net, layer).NeckZone  (default: 1.0 × pad's longer side; range 0–3 mm)
  if distFromPadEdge <= zone: return clamp(padNarrowSide, wmin, w)   // never wider than the pad
  return w

NeckDownPass():   // after main routing
  for each segment touching a pad at width < w: confirm its length inside the zone ≤ zone
  for each failed connection of a wide net: retry once with neck-down allowed
       (a "necked retry"); the retry never goes below MinWidth
```

Hard constraints:
- A neck-down never goes below `max(MinWidth, fabMinWidth)`.
- A neck-down is **not allowed** for nets with controlled impedance (the width is set by the stackup) or with `intent.noNeckDown`.
- For a current-carrying net, the necked section must still meet the ampacity check for its length. Short necks (< 1 mm) heat up less, but pcbpilot takes the safe route: the necked width must carry the current with ΔT ≤ 2 × the target ΔT, otherwise use a via array or a wider pad.

fastroute's public docs list a similar user-level feature: a neck-down width floor that applies at pins, at fan-out and on retries [fastroute IMPROVEMENTS.md]. This spec only takes the idea, not the design.

### 3.6 Rule resolver

```
Resolve(net, layer) RuleSet:
  cls := classOf(net)
  out := empty
  for scope in [pcb, layer(L), class(cls), class(cls)+L, net, net+L, intent]:   // low→high
     out.mergeFrom(scope)   // a field that is set overrides; intent merges as max()
  out.Width = max(out.Width, layerMinWidth(L))   // fab capability per layer
  return out

ClearanceBetween(objA, objB, layer) int64:
  t := ClrType{kind(A), kind(B)} canonicalised; same net → use the same_net types or 0
  c := lookup t, then the generic default "clearance" at each scope, highest scope wins:
       class_class(cls(A),cls(B))+L > class_class > net(A|B)+L > net > class+L > class > layer > pcb
  For two different nets, take the max of the values resolved for each side
       (a conservative symmetric rule)
  apply region overrides if either object's centre lies in a region with rules
  c = max(c, intentClearance(A,B))       // §4.6
  return c
```

Cache the results in a `map[[4]int32]int64` keyed by (classA, classB, layer, type). Lookups are O(1) after warm-up. Specctra clearance types are `<obj>_<obj>` pairs over {pin, smd, via, wire, area, testpoint}, plus special kinds: `smd_via_same_net`, `via_via_same_net`, `buried_via_gap`, `antipad_gap`, `pad_to_turn_gap`, `smd_to_turn_gap` [Specctra, clearance_type]. pcbpilot supports all pair types and `smd_via_same_net` / `via_via_same_net`. It parses the others and warns that it ignores them.

`use_via`: a net, class or pcb scope can list allowed via padstacks. The router tries them in the listed order, smallest last, unless the net is high current (then largest first). `(use_array <template> rows cols)` requests a via array [Specctra, circuit descriptor].

### 3.7 Via arrays for high current

```
ViasNeeded(I, via) = ceil(I / I_via(via))
I_via(via): treat the barrel as a conductor with cross-section A = π·(D_drill + t)·t
            (t = plating thickness, default 25 µm) and apply the IPC-2221 internal-layer formula
            I = 0.024·ΔT^0.44·A^0.725 (A in mil², ΔT in °C)
ViaArrayPass():
  for each layer transition on a net with intent current I > I_via:
    n := ViasNeeded(I, via) (at least use_array rows×cols if given)
    place n vias on a grid with pitch = D_via + s_vv(same net), centred on the original via,
       inside the copper the track can widen to; shrink the grid if a via collides
    if fewer than n fit: report ErrViaArrayShort (with how many were placed)
```

The barrel-as-conductor model is a conservative engineering approximation used by many via-current calculators. It is marked as an approximation and can be tuned with `viaAmpacityFactor` (default 1.0).

### 3.8 SES output

Write a Specctra session file:

```
(session <name>
  (base_design <dsn file>)
  (routes
    (resolution um 10)
    (parser (host_cad "pcbpilot") (host_version "<ver>"))
    (library_out (padstack <via name> (shape (circle <layer> <dia>)) ...))   // only for new/array vias
    (network_out
      (net <name>
        (wire (path <layer> <width> x1 y1 x2 y2 ...))      // one per polyline of constant width
        (via <padstack> x y)
      ))))
```

Structure taken from the session_file and route descriptors in [Specctra]. Rules:
- A neck-down creates a new `wire` at each change of width.
- Coordinates are rounded to the resolution, and the rounding must not break connectivity: snap shared endpoints once and reuse the same value.
- Fixed or pre-existing wires are written with `(type protect)` only if the router changed them; otherwise they are left out.
- Output must be deterministic: sort nets by name, then wires by first point.

---

## 4. Interaction with design rules

### 4.1 Per-net-class width and clearance
Class rules come from DSN `(class …)` or from the host EDA's net classes. The escape phase uses each net's **neck-down width** inside the field and its **full width** outside the field boundary. Channel capacities in §3.4 are computed per class, so a channel can fit two 0.1 mm signals but only one 0.2 mm power stub. The flow solver uses the most common escaping class to set capacities. Nets of a wider class use an edge cost multiplier and consume `ceil(w_net / w_ref)` units of capacity.

### 4.2 Per-layer width (outer vs inner copper)
Inner copper is usually thinner (e.g. 0.5 oz ≈ 17.5 µm vs 1 oz ≈ 35 µm outer after plating), and it cools worse: the IPC-2221 constant is k = 0.024 inside vs 0.048 outside. For the same current, inner tracks must therefore be much wider [IPC-2221 calculators]. The resolver computes the intent width **per layer** from `Layer.CopperUm` and `Layer.Outer` (§4.6) and writes it as the `class+layer` scope. This is the same thing DSN `layer_rule` expresses [Specctra layer_rule example].

### 4.3 Neck-down at pins
See §3.5. The zone length is measured along the track from the pad *edge* (not the centre). This keeps the rule independent of pad size.

### 4.4 Via sizes
The via is chosen from `use_via` and must satisfy: drill ≥ fab minimum, annular ring ≥ fab minimum, and land diameter small enough for the escape geometry (the `ViaFits` check). The escape phase may use a smaller "escape via" (`escapeVia` padstack) than the general router if `use_via` lists one.

### 4.5 Keep-outs and board edge
- `(keepout …)`, `(via_keepout …)` and `(wire_keepout …)` areas are obstacles of kind Area. Wire-to-area clearance uses the `area_wire` type, and via-to-area uses `area_via`.
- **Board-edge clearance** treats the board boundary polygon (and every cut-out or slot) as an Area object. The default is 0.25 mm, taken from the host design rule if one is given. Note: fastroute's docs list the edge clearance being read from the host design rule instead of a hard-coded default as a fix [fastroute IMPROVEMENTS.md]. Escape stubs and vias near the board edge must pass this check too.
- Copper-pour areas belonging to a plane net are not obstacles to their own net.

### 4.6 Intent-derived constraints

Inputs per net, from pcbpilot's intent layer: `CurrentA`, `TempRiseC` (default 10 °C), `VoltageV` (peak working voltage relative to the other net, or to ground), `Coated` (default false), `AltitudeM` (default < 3050 m).

**Current → width (per layer).**
```
k := 0.048 if layer.Outer else 0.024
A_mil2 := (I / (k · ΔT^0.44))^(1/0.725)
t_mil  := layer.CopperUm / 25.4
w      := A_mil2 / t_mil   (mil) → nm
intentWidth[net][layer] = roundUp(w, 1 µm)
```
[IPC-2221 formula as published by calculator sites.] Example: 2 A, ΔT = 10 °C, 35 µm outer → about 0.78 mm; the same current on 17.5 µm inner → about 4.1 mm. This is why the router should prefer outer layers for high-current nets. It does so by adding a layer cost of `intentWidth[L] / defaultWidth` in path search.

**Voltage → clearance.** Use the IPC-2221 Table 6-1 spacing as a step function of voltage. B1 is for internal conductors and B2 for external uncoated ones (sea level to 3050 m). Selected rows: 0–15 V gives 0.05 mm (B1) and 0.1 mm (B2). 301–500 V gives 0.25 mm (B1) and 2.5 mm (B2). Above 500 V, add a per-volt increment (B2: 0.005 mm/V) [indipcb; ProtoExpress]. pcbpilot ships the full table as data and selects B1 for inner layers and B2 (or A6/B4 when `Coated`) for outer layers. The clearance between nets *a* and *b* uses `V = |V_a − V_b|` when both are known, otherwise max(V_a, V_b). These values are **floors** on the resolved clearance (§3.6). They are not creepage requirements under safety standards such as IEC 62368. The router flags nets with mains intent so that a human can review them.

---

## 5. Failure modes and handling

| Failure | Detection | Handling |
|---|---|---|
| Pitch too small for a dog-bone and VIP not allowed | `!ViaFits` for all 4 diagonals | `ErrEscapeNeedsVIP{minViaDia}`; leave the pin unescaped; the main router still tries it |
| Flow < number of pins on the last layer | max-flow value | report the unescaped pins; suggest more layers / a smaller via / a narrower escape width |
| Flow paths cross after decomposition | crossing check per tile | swap the continuations; if the conflict persists, rip up the lower-priority path and send it to the next layer |
| Escape stubs block the main router | main router cost on soft-fixed segments | the main router may rip them up at `escapeCostWeight`; the escape is redone with that pin excluded |
| Unused fan-out stubs left after routing | net connectivity check | `CleanupTails` removes the stub and via of any pad that ended up routed another way (fastroute's docs flag stale fan-out tails as a known problem) |
| Neck-down needed below MinWidth | WidthAt clamp | the connection fails, with the reason `neck_below_min` |
| Via array does not fit | grid placement | place as many as fit; `ErrViaArrayShort`; DRC-level warning, not an error |
| Inner-layer intent width larger than the channel | width > max channel | ban the layer for that net (`use_layer` excludes it) |
| Conflicting rules (explicit width < intent width) | resolver | intent wins (floor); warn once per net |
| Unknown DSN clearance type | parser | warn and ignore; never crash |
| SES rounding breaks a joint | post-write connectivity reparse | snap with a shared-endpoint table; test round-trip |

---

## 6. Test scenarios and acceptance criteria

All boards are generated in Go test fixtures (`testdata/escape/*.dsn`), 2–6 layers.

| # | Board | Expectation |
|---|---|---|
| T1 | BGA 10×10, 1.0 mm pitch, 0.5 mm pads, 0.1/0.1 mm rules, 4 layers, 100 % signal | 100 % escaped; 0 DRC; ≤ 36 vias (at least the outer 2 rings, 64 pads, escape on top with no via); runtime < 200 ms |
| T2 | BGA 16×16, 0.8 mm, 0.4 mm pads, via 0.45/0.2 mm, 6 layers | ≥ 98 % escaped; 0 DRC; quadrant-pattern via directions ≥ 95 % of vias |
| T3 | BGA 12×12, 0.5 mm pitch, VIP disallowed | `ErrEscapeNeedsVIP` raised for inner pins; ring 0 escaped; no via-in-pad present |
| T4 | Same as T3 with `via_at_smd on` + `use_via micro` | 100 % escaped; every inner via concentric with its pad |
| T5 | QFP-100, 0.5 mm pitch, GND plane | all pads stubbed; GND pads have a via to the plane; staggered vias with no via-via violation |
| T6 | Flow model check: a 6×6 field where only the diagonal capacity limits | the flow equals the hand-computed optimum; geometry has 0 crossings (guards against the "incorrect model" error) |
| T7 | Ordered escape: a 1×16 row of pins to a 16-pin connector, `escapeOrdered=true` | boundary order matches the target; 0 crossings in the main route |
| T8 | 3 A net from a QFN pin to a connector, 35 µm outer / 17.5 µm inner | outer width ≥ IPC value; necked section ≤ zone and ≥ MinWidth; routed on an outer layer |
| T9 | 5 A net through a layer change, via 0.3 mm drill | via array with ≥ `ViasNeeded` vias; 0 same-net via-via violations |
| T10 | 230 VAC net next to a 3.3 V net, outer layer, uncoated | clearance ≥ the Table 6-1 B2 value for the 151–300 V band; flagged for review |
| T11 | Board edge 0.3 mm rule, BGA placed 1 mm from the edge | no copper within 0.3 mm of the edge or slot |
| T12 | DSN precedence: pcb width 0.2, class 0.15, class layer_rule inner 0.25, net override 0.3 | resolver returns 0.3 on all layers for the net and 0.25 on inner layers for the other class members |
| T13 | SES round-trip | re-importing the SES into the DSN model gives identical connectivity; deterministic byte output on two runs |

Global acceptance metrics, reported by `go test -run Escape -bench`:
- Completion: escaped pins / pins needing escape ≥ 98 % on T1/T2. Full board completion after the main route is no worse than with escape disabled.
- DRC violations: 0 on all scenarios (checked by the independent DRC in spec 06).
- Via count: at most 1 via per escaped inner BGA pin, except via arrays.
- Wirelength: total escape stub length ≤ 1.2 × the sum of the Manhattan pad-to-boundary lower bounds.
- Runtime: escape of a 1 000-ball BGA in < 1 s on one core; resolver lookup < 50 ns after caching.

---

## 7. Sources

1. Cadence, *SPECCTRA Design Language Reference*, Product Version 10.0, May 2000. Rule precedence hierarchy, clearance_descriptor/clearance_type, class, class_class, layer_rule, circuit/use_via/use_array, attach/via_at_smd, session_file, routes, network_out. https://cdn.hackaday.io/files/1666717130852064/specctra.pdf
2. T. Yan, M. D. F. Wong, "A correct network flow model for escape routing," *Proc. 46th DAC*, 2009, pp. 332–335. https://www.researchgate.net/publication/224585568_A_correct_network_flow_model_for_escape_routing
3. M. M. Ozdal, M. D. F. Wong, "Simultaneous escape routing and layer assignment for dense PCBs," *Proc. ICCAD*, 2004. Also *IEEE TCAD* 25(8), 2006.
4. H. Kong, T. Yan, M. D. F. Wong, "Optimal simultaneous pin assignment and escape routing for dense PCBs," *Proc. ASP-DAC*, 2010. Also "Ordered escape routing using network flow and optimization model," https://ieeexplore.ieee.org/document/7081209/
5. "Simultaneous Escape Routing using Network Flow Optimization," *Malaysian Journal of Computer Science*. https://ejournal.um.edu.my/index.php/MJCS/article/download/7006/4659/15036
6. "Ordered Escape Routing with Consideration of Differential Pair and Blockage," *ACM TODAES*. https://dl.acm.org/doi/10.1145/3185783
7. IPC-2221 (Generic Standard on Printed Board Design): conductor current formula and Table 6-1 spacing. Secondary descriptions: https://www.7pcb.com/trace-width-calculator ; https://standardclarity.com/standards/ipc-2221/ ; https://indipcb.com/blog/ipc-2221-clearance-spacing ; https://www.protoexpress.com/blog/ipc-2221-circuit-board-design/
8. BGA fan-out practice (dog-bone vs via-in-pad by pitch): https://jlcpcb.com/blog/bga-pcb-design-complete-guide-layout-and-routing-guidelines ; https://www.atlaspcb.com/blog/bga-fanout-routing-strategies-dog-bone-via-in-pad/ ; https://pcbsync.com/bga-pcb-design/
9. fastroute user-facing docs (feature descriptions only: neck-down floor, edge clearance from host rules, fan-out tail cleanup, class-pair clearances): https://github.com/parisxmas/fastroute (README) and docs/IMPROVEMENTS.md
10. R. K. Ahuja, T. L. Magnanti, J. B. Orlin, *Network Flows: Theory, Algorithms, and Applications*, Prentice Hall, 1993. Min-cost flow by successive shortest paths with potentials.

---

## 8. Changelog

- 2026-10-06, clean-room audit: no copied text or source-derived identifiers found;
  stated that the §3.4 parameter defaults are pcbpilot's own values.
