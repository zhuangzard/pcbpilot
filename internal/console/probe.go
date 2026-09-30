package console

import (
	"context"
	"sync"
	"time"
)

// probe is a single-flight, cached background probe. /api/status must answer
// the moment the daemon does, even when a probe blocks: right after the login
// service is (re)bootstrapped `launchctl print` can hang for minutes, and a
// synchronous probe held every status request (and the SSE hello) hostage —
// the landing page sat on "连接 daemon…" until launchd settled.
//
// get never waits longer than its wait argument. A probe that is still running
// is reported as "checking" (no value yet), "stale" (last good value, refresh
// in flight) or "timeout" (running past its deadline); at most one run is in
// flight, so a hung probe never piles up processes or goroutines.
type probe struct {
	fn      func(ctx context.Context) (any, error)
	ttl     time.Duration // a value younger than this is served without a refresh
	timeout time.Duration // per-run deadline (passed as ctx; also the "timeout" verdict)
	now     func() time.Time

	mu        sync.Mutex
	val       any
	hasVal    bool
	at        time.Time // when val was produced
	err       error     // last run's error ("" after a success)
	endedAt   time.Time // when the last run finished (success or error)
	running   bool
	startedAt time.Time
	done      chan struct{} // closed when the in-flight run ends
}

// ProbeState is the freshness verdict shown next to a probed value.
type ProbeState struct {
	// State: ok | stale | checking | timeout | error
	State string    `json:"state"`
	At    time.Time `json:"at,omitzero"` // when the shown value was produced
	Error string    `json:"error,omitempty"`
}

func newProbe(fn func(ctx context.Context) (any, error), ttl, timeout time.Duration, now func() time.Time) *probe {
	if now == nil {
		now = time.Now
	}
	return &probe{fn: fn, ttl: ttl, timeout: timeout, now: now}
}

// get returns the freshest value available within wait, starting a refresh
// when the cached one is older than ttl.
func (p *probe) get(wait time.Duration) (any, ProbeState) {
	p.mu.Lock()
	// Within ttl of the last finished run nothing is re-run: a success is
	// served as is, a failure (e.g. launchctl killed at the deadline) is
	// retried only after ttl instead of on every request.
	if !p.running && !p.endedAt.IsZero() && p.ttl > 0 && p.now().Sub(p.endedAt) < p.ttl {
		p.mu.Unlock()
		return p.peek()
	}
	if !p.running {
		p.startLocked()
	}
	done := p.done
	p.mu.Unlock()

	if wait > 0 {
		t := time.NewTimer(wait)
		select {
		case <-done:
		case <-t.C:
		}
		t.Stop()
	}
	return p.peek()
}

// peek reports the current value and state without starting a run.
func (p *probe) peek() (any, ProbeState) {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := ProbeState{At: p.at}
	switch {
	case p.running && p.timeout > 0 && p.now().Sub(p.startedAt) > p.timeout:
		st.State = "timeout"
		if p.hasVal {
			st.Error = "refresh did not finish within " + p.timeout.String() + " — showing the last good value"
		} else {
			st.Error = "no answer within " + p.timeout.String() + " — still checking"
		}
	case p.running && p.hasVal:
		st.State = "stale"
	case p.running:
		st.State = "checking"
	case p.err != nil:
		st.State, st.Error = "error", p.err.Error()
	default:
		st.State = "ok"
	}
	if !p.hasVal {
		st.At = time.Time{}
		return nil, st
	}
	return p.val, st
}

func (p *probe) startLocked() {
	p.running, p.startedAt, p.done = true, p.now(), make(chan struct{})
	done := p.done
	go func() {
		ctx, cancel := context.Background(), context.CancelFunc(func() {})
		if p.timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, p.timeout)
		}
		v, err := p.fn(ctx)
		cancel()
		p.mu.Lock()
		if err == nil {
			p.val, p.hasVal, p.at = v, true, p.now()
		}
		p.err, p.endedAt = err, p.now()
		p.running = false
		p.mu.Unlock()
		close(done)
	}()
}
