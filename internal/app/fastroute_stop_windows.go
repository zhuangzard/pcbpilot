//go:build windows

package app

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// Windows has no SIGINT for another process: os.Process.Signal(os.Interrupt)
// is unsupported, so a timed-out fastroute used to be killed after
// WaitDelay without writing its best board. fastroute documents Ctrl+Break
// as the same graceful stop as SIGTERM / --max-time (fastroute --help), so
// it runs in its own console process group and gets CTRL_BREAK_EVENT.

const ctrlBreakEvent = 1 // CTRL_BREAK_EVENT

var procGenerateConsoleCtrlEvent = syscall.NewLazyDLL("kernel32.dll").NewProc("GenerateConsoleCtrlEvent")

// ctrlBreak sends Ctrl+Break to the process group pid leads. It needs a
// console shared with that group; without one (a detached daemon) it fails.
func ctrlBreak(pid int) error {
	if err := procGenerateConsoleCtrlEvent.Find(); err != nil {
		return err
	}
	if r, _, err := procGenerateConsoleCtrlEvent.Call(ctrlBreakEvent, uintptr(pid)); r == 0 {
		return err
	}
	return nil
}

// Seams for the interrupt-forwarding tests: where Ctrl+C reaches pcbpilot,
// how long fastroute gets to write its board after a forwarded Ctrl+C, and
// how pcbpilot ends afterwards.
var (
	fastrouteNotifyInterrupt = func(c chan<- os.Signal) { signal.Notify(c, os.Interrupt) }
	fastrouteStopInterrupt   = func(c chan<- os.Signal) { signal.Stop(c) }
	fastrouteInterruptGrace  = 30 * time.Second
	fastrouteInterruptExit   = func() { os.Exit(int(0xC000013A)) } // STATUS_CONTROL_C_EXIT, what Ctrl+C gives a console program
)

// prepareFastrouteCmd: a new process group (the only target Ctrl+Break can
// be sent to without hitting pcbpilot itself); on cancel or timeout it gets
// Ctrl+Break, or — when no console allows that — is killed at once, with a
// log line saying the best board was not written.
func prepareFastrouteCmd(cmd *exec.Cmd, stderr io.Writer) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	cmd.Cancel = func() error {
		if err := ctrlBreak(cmd.Process.Pid); err != nil {
			fmt.Fprintf(stderr, "fastroute: cannot send Ctrl+Break (%v); killing it — its best board so far is not written\n", err)
			return cmd.Process.Kill()
		}
		return nil
	}
}

// runFastrouteCmd runs cmd and forwards a Ctrl+C / Ctrl+Break typed at
// pcbpilot: a process in its own group no longer receives the console's
// Ctrl+C. The handler is installed before the child starts, so an interrupt
// can never end pcbpilot and orphan fastroute; one that arrives before the
// start is forwarded once the child runs. The first interrupt asks fastroute
// to write its best board and stop within fastrouteInterruptGrace (killed
// after that); a second one kills it at once. pcbpilot then exits like an
// interrupted console program.
func runFastrouteCmd(cmd *exec.Cmd, stderr io.Writer) error {
	sig := make(chan os.Signal, 2)
	fastrouteNotifyInterrupt(sig)
	defer fastrouteStopInterrupt(sig)
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan struct{})
	interrupted := make(chan bool, 1)
	go func() {
		n := 0
		var grace <-chan time.Time
		var timer *time.Timer
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		for {
			select {
			case <-done:
				interrupted <- n > 0
				return
			case <-grace:
				fmt.Fprintf(stderr, "fastroute: did not stop within %s of the interrupt; killing it\n", fastrouteInterruptGrace)
				_ = cmd.Process.Kill()
				grace = nil
			case <-sig:
				n++
				if n == 1 {
					err := ctrlBreak(cmd.Process.Pid)
					if err == nil {
						fmt.Fprintf(stderr, "fastroute: interrupted — writing its best board so far (up to %s; interrupt again to kill it)\n", fastrouteInterruptGrace)
						timer = time.NewTimer(fastrouteInterruptGrace)
						grace = timer.C
						continue
					}
					fmt.Fprintf(stderr, "fastroute: interrupted, cannot send Ctrl+Break (%v); killing it\n", err)
				}
				_ = cmd.Process.Kill()
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	if <-interrupted {
		fmt.Fprintln(stderr, "pcbpilot: interrupted")
		fastrouteInterruptExit()
	}
	return err
}
