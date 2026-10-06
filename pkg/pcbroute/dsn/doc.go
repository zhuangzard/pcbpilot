// Package dsn reads and writes the Specctra files of routing engine v2
// (pkg/pcbroute): DSN into a board and its rules, pcbauto Board to DSN for the
// bench, and SES out and back in (spec 04 §2, §3.8; PLAN.md M3).
//
// The S-expression reader is reused from internal/pcb/specctra by import
// (PLAN.md Q12).
//
// What Read puts where (design choices; the specs leave them open):
//
//   - Units: the DSN (unit …), else the (resolution …) unit, rounded to nm.
//   - Layers are numbered in file order (Specctra stack order); the first and
//     last are outer. The DSN has no copper weight, so outer layers get 35 µm
//     and inner ones 17.5 µm (spec 04 §2 example values).
//   - A pin becomes one Pad item per shape and per run of adjacent layers with
//     equal shapes; all items of a pin share Ref "component-pin". Pins on no
//     net have net 0. A back-side component is mirrored in x before its
//     rotation (counter-clockwise degrees) and its layers are reversed.
//   - Rect pads stay geom.Rect (half-open, so the closed pad is covered); a
//     rotated rect becomes a Poly. A path is one capsule per segment. A
//     polygon's aperture width is ignored.
//   - The boundary is an Edge item, a (plane …) a Zone item of its net; both
//     are Fixed. Shapes on "signal"/"pcb" use geom.AllLayers.
//   - Every keep-out goes to the rules with its kind; a full (keepout …) is
//     also a board Keepout item so spatial queries meet it.
//   - Wiring: (type fix) and (type protect) wires and vias are Fixed (spec 03
//     §4.7); other wiring is loaded as ordinary routed copper.
//   - Rules: see rules.go. The via catalogue holds the structure (via …) and
//     every use_via padstack; a DSN padstack carries no drill, so Drill is 0.
package dsn
