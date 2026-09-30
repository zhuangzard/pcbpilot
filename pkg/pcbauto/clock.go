package pcbauto

import (
	"os"
	"strconv"
	"sync"
	"time"
)

// The router's clock. Every routing decision that depends on time — the
// search deadline, the negotiation stall test, the post-routing budget, the
// re-couple and via-fix budgets — reads now(). By default it is the wall
// clock (the routing budget of a real run is a time budget). With
// RouteOptions.WorkRate > 0 it is virtual: it advances by one second per
// WorkRate units of search work (A* expansions and unreachability-flood
// steps), so two runs of the same board give the same result whatever the
// machine load — the fixture bench's deterministic mode. The virtual clock
// changes no routing rule, only what "out of time" means.
func (r *router) now() time.Time {
	if r.opt.WorkRate <= 0 {
		return time.Now()
	}
	return r.clockStart.Add(time.Duration(float64(r.work) / r.opt.WorkRate * float64(time.Second)))
}

// VirtualClock reports the deterministic mode (PCBPILOT_BENCH_WORK set):
// callers holding a wall-clock deadline over a routing run (the CLI) widen
// it, since only the virtual budget may decide what runs.
func VirtualClock() bool { return envWorkRate() > 0 }

// envWorkRate is PCBPILOT_BENCH_WORK (A* expansions per budget second):
// set, every Route call without an explicit WorkRate runs on the virtual
// clock — the fixture bench (make fixture-bench-det) and the stress suites
// become reproducible under any machine load. Unset or invalid: 0 (wall
// clock). Read once.
var envWorkRate = sync.OnceValue(func() float64 {
	v, err := strconv.ParseFloat(os.Getenv("PCBPILOT_BENCH_WORK"), 64)
	if err != nil || v < 0 {
		return 0
	}
	return v
})
