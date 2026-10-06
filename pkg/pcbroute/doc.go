// Package pcbroute is routing engine v2: a gridless, shape-based multi-layer PCB
// router. It is not pkg/pcbrouting (the single-layer kernel of the crystal
// tools); the similar name is deliberate and every v2 package says "engine v2".
//
// Route takes a pcbauto board whose placement, stack-up and planes are already
// decided and returns tracks, vias and the connections it could not route.
// RouteDSN does the same on a Specctra DSN file and returns an SES session; the
// router bench uses it. Inside, everything is int64 nanometres; conversion to
// pcbauto's mil coordinates happens in one adapter.
//
// Sub-packages, leaves first (PLAN.md §2.1): geom, rules, board, dsn, tile,
// search, tree, global, negotiate, shove, rrr, optimize, tune, escape. They form
// a DAG; search sees congestion only through an injected CostFn and optimize
// reroutes only through an injected Router.
//
// This package is written under docs/router-cleanroom/CLEANROOM.md: its only
// design inputs are the specs in docs/router-cleanroom/specs, the papers they
// cite and pcbpilot's own code. M0 freezes the exported types and signatures;
// M8 and M15 implement them.
package pcbroute
