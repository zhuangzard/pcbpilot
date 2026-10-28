//go:build windows

package app

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
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
// Ctrl+C. The first one asks fastroute to write its best board and stop,
// then pcbpilot exits like an interrupted console program; a second one
// kills fastroute at once.
func runFastrouteCmd(cmd *exec.Cmd, stderr io.Writer) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt)
	defer signal.Stop(sig)
	done := make(chan struct{})
	interrupted := make(chan bool, 1)
	go func() {
		n := 0
		for {
			select {
			case <-done:
				interrupted <- n > 0
				return
			case <-sig:
				n++
				if n == 1 && ctrlBreak(cmd.Process.Pid) == nil {
					fmt.Fprintln(stderr, "fastroute: interrupted — writing its best board so far (interrupt again to kill it)")
					continue
				}
				_ = cmd.Process.Kill()
			}
		}
	}()
	err := cmd.Wait()
	close(done)
	if <-interrupted {
		fmt.Fprintln(stderr, "pcbpilot: interrupted")
		os.Exit(int(0xC000013A)) // STATUS_CONTROL_C_EXIT, what Ctrl+C gives a console program
	}
	return err
}
