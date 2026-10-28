//go:build windows

package app

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

// ctrlBreakDeliverable reports whether this process has a console that
// Ctrl+Break can be sent through: GetConsoleProcessList fails (returns 0)
// only when no console is attached (a detached test run).
func ctrlBreakDeliverable() (bool, string) {
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleProcessList")
	if err := proc.Find(); err != nil {
		return false, err.Error()
	}
	var ids [16]uint32
	n, _, err := proc.Call(uintptr(unsafe.Pointer(&ids[0])), uintptr(len(ids)))
	if n == 0 {
		return false, "no console attached: " + err.Error()
	}
	return true, ""
}

// fakeInterrupts replaces pcbpilot's Ctrl+C source and exit for one test:
// pre interrupts are queued at registration (before fastroute starts), the
// returned send delivers more; exited reports whether pcbpilot would exit.
func fakeInterrupts(t *testing.T, pre int, grace time.Duration) (send func(), exited *atomic.Bool) {
	t.Helper()
	if ok, why := ctrlBreakDeliverable(); !ok {
		t.Skipf("Ctrl+Break cannot be delivered: %s", why)
	}
	var ch atomic.Value
	exited = &atomic.Bool{}
	savedN, savedS, savedG, savedE := fastrouteNotifyInterrupt, fastrouteStopInterrupt, fastrouteInterruptGrace, fastrouteInterruptExit
	t.Cleanup(func() {
		fastrouteNotifyInterrupt, fastrouteStopInterrupt, fastrouteInterruptGrace, fastrouteInterruptExit = savedN, savedS, savedG, savedE
	})
	fastrouteNotifyInterrupt = func(c chan<- os.Signal) {
		ch.Store(c)
		for i := 0; i < pre; i++ {
			c <- os.Interrupt
		}
	}
	fastrouteStopInterrupt = func(chan<- os.Signal) {}
	fastrouteInterruptGrace = grace
	// pcbpilot's os.Exit, as a panic the test recovers (runInterruptible):
	// nothing after it runs, no further fastroute round.
	fastrouteInterruptExit = func() { exited.Store(true); panic(errFakeInterruptExit) }
	return func() { ch.Load().(chan<- os.Signal) <- os.Interrupt }, exited
}

type fakeExit struct{}

var errFakeInterruptExit = fakeExit{}

// runInterruptible runs f, returning when it ends or pcbpilot would exit.
func runInterruptible(t *testing.T, f func()) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil && r != errFakeInterruptExit {
			panic(r)
		}
	}()
	f()
}

func waitFile(t *testing.T, p string) {
	t.Helper()
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if _, err := os.Stat(p); err == nil {
			return
		}
	}
	t.Fatalf("fake fastroute never got ready (%s)", p)
}

// A Ctrl+C at pcbpilot is forwarded as Ctrl+Break: fastroute writes its best
// board and pcbpilot then exits as interrupted.
func TestFastrouteCtrlCForwardedGracefully(t *testing.T) {
	dir := t.TempDir()
	bin := fakeRouterBin(t, "fastroute")
	t.Setenv(envFakeFRMode, "wait-stop")
	ready := filepath.Join(dir, "ready")
	t.Setenv(envFakeFRReady, ready)
	send, exited := fakeInterrupts(t, 0, 30*time.Second)
	go func() { waitFile(t, ready); send() }()
	var log strings.Builder
	runInterruptible(t, func() {
		runFastroute(fastrouteOpts{bin: bin, timeout: time.Minute}, "b.dsn", filepath.Join(dir, "b"), &log)
	})
	if !exited.Load() {
		t.Fatalf("pcbpilot did not exit after the forwarded Ctrl+C\n%s", log.String())
	}
	for _, f := range []string{"b.r0.ses", "b.r0-report.json"} { // the best board fastroute wrote on the stop
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Fatalf("forwarded Ctrl+C: %s not written: %v\n%s", f, err, log.String())
		}
	}
}

// A child that receives the stop but keeps running is killed after the
// grace period instead of blocking until the routing timeout.
func TestFastrouteCtrlCGraceKillsStubbornChild(t *testing.T) {
	dir := t.TempDir()
	bin := fakeRouterBin(t, "fastroute")
	t.Setenv(envFakeFRMode, "ignore-stop")
	ready := filepath.Join(dir, "ready")
	t.Setenv(envFakeFRReady, ready)
	send, exited := fakeInterrupts(t, 0, time.Second)
	go func() { waitFile(t, ready); send() }()
	var log strings.Builder
	start := time.Now()
	runInterruptible(t, func() {
		runFastroute(fastrouteOpts{bin: bin, timeout: 10 * time.Minute}, "b.dsn", filepath.Join(dir, "b"), &log)
	})
	if d := time.Since(start); d > 20*time.Second || !exited.Load() || !strings.Contains(log.String(), "did not stop within") {
		t.Fatalf("stubborn child: %s, exited %v\n%s", d, exited.Load(), log.String())
	}
}

// A second Ctrl+C kills fastroute at once, without waiting out the grace.
func TestFastrouteSecondCtrlCKills(t *testing.T) {
	dir := t.TempDir()
	bin := fakeRouterBin(t, "fastroute")
	t.Setenv(envFakeFRMode, "ignore-stop")
	ready := filepath.Join(dir, "ready")
	t.Setenv(envFakeFRReady, ready)
	send, exited := fakeInterrupts(t, 0, 10*time.Minute)
	go func() { waitFile(t, ready); send(); time.Sleep(200 * time.Millisecond); send() }()
	var log strings.Builder
	start := time.Now()
	runInterruptible(t, func() {
		runFastroute(fastrouteOpts{bin: bin, timeout: 10 * time.Minute}, "b.dsn", filepath.Join(dir, "b"), &log)
	})
	if d := time.Since(start); d > 20*time.Second || !exited.Load() {
		t.Fatalf("second Ctrl+C: %s, exited %v\n%s", d, exited.Load(), log.String())
	}
}

// The handler is in place before fastroute starts: an interrupt that comes
// in before the start is not lost (pcbpilot would have died, orphaning
// fastroute) but forwarded once the child runs, and the child is stopped.
func TestFastrouteCtrlCBeforeStartIsForwarded(t *testing.T) {
	dir := t.TempDir()
	bin := fakeRouterBin(t, "fastroute")
	t.Setenv(envFakeFRMode, "ignore-stop")
	_, exited := fakeInterrupts(t, 1, time.Second)
	// The fake logs a line to calls the moment it starts: the handler must be
	// registered while that file does not exist yet.
	calls := filepath.Join(dir, "calls")
	t.Setenv(envFakeCalls, calls)
	register := fastrouteNotifyInterrupt
	var startedFirst atomic.Bool
	fastrouteNotifyInterrupt = func(c chan<- os.Signal) {
		if _, err := os.Stat(calls); err == nil {
			startedFirst.Store(true)
		}
		register(c)
	}
	var log strings.Builder
	start := time.Now()
	runInterruptible(t, func() {
		runFastroute(fastrouteOpts{bin: bin, timeout: 10 * time.Minute}, "b.dsn", filepath.Join(dir, "b"), &log)
	})
	if startedFirst.Load() {
		t.Fatal("the interrupt handler was installed after fastroute started")
	}
	if d := time.Since(start); d > 20*time.Second || !exited.Load() || !strings.Contains(log.String(), "interrupted") {
		t.Fatalf("pre-start Ctrl+C: %s, exited %v\n%s", d, exited.Load(), log.String())
	}
}
