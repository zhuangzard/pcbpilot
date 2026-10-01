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
	if r.fixRate > 0 {
		return r.fixStart.Add(time.Duration(float64(r.work-r.fixWork) / r.fixRate * float64(time.Second)))
	}
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

// fixupWorkRate converts a fix-up's budget seconds into search work on the
// wall clock (A* expansions per second: the bench's BENCH_WORK, about this
// class of machine's search speed).
const fixupWorkRate = 3e6

// onWorkClock runs an electrical or safety fix-up (via-array completion) on
// its own budget of search work instead of the time left in the routing
// budget: whether a current-carrying transition gets its alternatives must
// not depend on how long the negotiation took or how loaded the machine is.
// Inside, now() counts work from the fix-up's start (at the run's WorkRate,
// else fixupWorkRate) and r.deadline is the fix-up's own budget; the
// routing deadline is restored afterwards.
func (r *router) onWorkClock(budget time.Duration, f func()) {
	if r.fixRate > 0 {
		f()
		return
	}
	rate := r.opt.WorkRate
	if rate <= 0 {
		rate = fixupWorkRate
	}
	deadline := r.deadline
	r.fixStart, r.fixWork = r.now(), r.work
	r.fixRate = rate
	r.deadline = r.fixStart.Add(budget)
	defer func() {
		// The virtual clock keeps counting the fix-up's work; the wall clock
		// moved on by itself.
		r.fixRate = 0
		r.deadline = deadline
	}()
	f()
}
