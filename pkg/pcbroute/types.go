package pcbroute

import (
	"context"
	"errors"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Input is a placed board ready for routing. Planes are decided by pcbauto and
// are not changed. Fixed copper stays as it is.
type Input struct {
	Board     *pcbauto.Board
	Stackup   *pcbauto.Stackup
	Plan      *pcbauto.Analysis
	Planes    []pcbauto.PlaneRegion
	Fixed     []pcbauto.Track
	FixedVias []pcbauto.Via
}

// PhaseMask selects pipeline phases. 0 runs all of them.
type PhaseMask uint32

const (
	PhaseEscape PhaseMask = 1 << iota
	PhaseA                // negotiated congestion
	PhaseB                // detailed rip-up and reroute
	PhaseC                // endgame
	PhaseMultiStart
	PhaseOptimize
	PhaseTune
)

// DefaultStarts is the multi-start count when Options.Starts is 0 (decision D7).
const DefaultStarts = 8

// Options tune a run. Output depends only on the input, Seed, Starts, Budget
// and WorkRate: Threads changes speed, never the result.
type Options struct {
	Budget   time.Duration
	WorkRate float64 // > 0: virtual clock with pcbauto's clock semantics
	Seed     int64
	Starts   int // fixed count, never derived from the CPU count
	Threads  int
	Resume   *Session
	Phases   PhaseMask
}

// Session is the state needed to resume a run (M15). It is opaque.
type Session struct{}

// PhaseReport is one phase's metrics.
type PhaseReport struct {
	Phase    PhaseMask
	Duration time.Duration
	Routed   int
	Unrouted int
}

// Report lists the phases in the order they ran.
type Report struct{ Phases []PhaseReport }

// Output is the routed result in pcbauto units.
type Output struct {
	Tracks   []pcbauto.Track
	Vias     []pcbauto.Via
	Unrouted []pcbauto.Unrouted
	Stats    pcbauto.RouteStats
	Session  *Session
	Report   Report
}

var errNotImplemented = errors.New("pcbroute: engine v2 is not implemented yet (PLAN.md M8)")

// Route routes in.
func Route(ctx context.Context, in Input, opt Options) (*Output, error) {
	return nil, errNotImplemented
}

// RouteDSN routes a Specctra DSN design and returns the SES session.
func RouteDSN(ctx context.Context, dsn []byte, opt Options) (ses []byte, out *Output, err error) {
	return nil, nil, errNotImplemented
}
