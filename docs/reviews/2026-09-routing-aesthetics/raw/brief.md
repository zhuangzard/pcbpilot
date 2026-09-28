# Design review brief: PCB layout & routing AESTHETICS as a scored + generated objective (pcbpilot)

You are one of several independent senior reviewers (20+ yr PCB layout experience + algorithms). Answer in Chinese (technical terms may stay English). Be concrete and implementable.

## Goal (from the product owner)
Routing from schematic → PCB must be not only electrically correct (EMI/SI/PI, DRC) but also *beautiful*: tidy, clean, symmetric, consistent — the look of a professional commercial board. Aesthetics must (1) be a measurable, scored objective folded into the composite (joint) score, and (2) actively drive generation so the placer/router *produce* prettier layouts and routes, not just grade them afterwards. Electrical/safety still dominates; aesthetics must never trade away EMI/creepage/current capacity.

## Current system (repo: Go, pkg/pcbauto = placer+router+post; internal/app = scoring)
- Router: grid A* on octilinear grid; cost: 90° bend = 2·grid, 45° bend = 0.4·grid, no acute turns; post-process `stringPull` (farthest clear octilinear segment) + `chamfer` (90°→45° corners up to 3 track widths). HS retry at finer grid. Per-net widths from intent (current), diff pairs, isolation bands, via arrays, edge bands.
- Joint score (pkg/pcbauto/joint.go): overall = gate × completion² × quality; quality = weighted geometric mean of electrical 45% (hot-loop, decap loop, ESD stub, RF feed, diff/HS), efficiency 25% (detour ratio routed/pad-MST, vias per connection), placement 30% (from layout-score).
- Layout-score (internal/app/pcb_layoutscore.go) 9 dims w/ weights: routable 1.5, clearance 1.5, edgeIO 1.2, partition 1.2, protection 1.0, rf 1.0, flowOrder 0.8, compact 0.8, tidy 0.5. Tidy (pcb_score_tidy.go) = report-only: grid-landing (5 mil), orientation consistency within groups, designator side consistency, silkscreen font size consistency, array pitch uniformity. No auto-fix path for most tidy findings.
- NO routing-aesthetic metrics exist yet (no bend density, no parallel-bus combing, no spacing uniformity, no layer-direction discipline, no symmetry, no pad-entry quality, no track-to-pad-centre alignment, no neck/stub/zigzag detection, no copper "visual balance").
- Golden-board calibration harness exists (`make layout-calibrate`): known-good real boards must score high, negative controls must be flagged. Fixture bench: 5 real boards (mipi, bbclaw, szpi esp32s3, rk3568 4L, k230 6L).
- Sample routed board preview: esp32-routed-preview.svg (in working dir if available).

## Questions — please answer each
1. Taxonomy: list the concrete aesthetic criteria for (a) placement and (b) routing (and silkscreen/copper pours) that expert reviewers actually judge. For each: an exact computable metric on geometry (tracks as polylines with width/layer/net, vias, pads, component bbox/rotation, outline), its normalisation 0–100, and the threshold/ramp you'd use. Flag which ones conflict with electrical goals and how to resolve (priority rules).
2. Scoring integration: how to fold these into the joint score (new group "aesthetics"? weights? geometric vs arithmetic mean? gating?). How to avoid gaming (e.g. fewer bends by leaving nets unrouted). How to calibrate against golden boards and human ranking (pairwise preference data?).
3. Generation — placement: algorithms to produce aligned rows/columns, consistent orientation, symmetric blocks, uniform arrays, mirrored identical channels, grid snapping — inside or after the placer (constraint legalisation, LP/ILP alignment, "snap-to-alignment-lines" post-pass, symmetry detection from schematic/net topology).
4. Generation — routing: cost-function terms in A* (bend, layer-direction preference H/V per layer, via penalty, alignment to neighbours, keep-parallel-to-bus bonus), post-route beautification passes (bus combing/equal spacing, jog removal, zigzag straightening, pad-entry cleanup (enter along pad axis, no acute pad entries, centred), 45° miter uniformity, consistent neck-downs, teardrops, arc corners option, via alignment/grid, spreading to fill channel evenly vs hugging), rip-up-and-reroute ordering for aesthetics; topological/rubber-band routing vs grid A*.
5. Evaluate visual/AI judgement: should we also use a VLM rendering-based critic (render SVG/PNG → model scores/critiques) as a secondary signal? Risks, how to keep it honest (never replaces geometry metrics).
6. Prioritised plan: top 10 items by (impact on perceived beauty)/(effort), with acceptance tests (offline fixtures, golden boards, before/after metrics). Keep it to what can be built in Go in this codebase.

Output: a structured markdown report (≤ ~2500 Chinese chars + tables allowed). No code edits.
