//go:build !windows

package app

import (
	"os/exec"
	"strconv"
)

// killTreeOnCancel makes a cancelled or timed-out cmd take its children with
// it (a launcher script that starts the real program instead of exec'ing
// it). The command stays in pcbpilot's process group, so a Ctrl+C in the
// terminal still reaches it.
func killTreeOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		_ = exec.Command("pkill", "-KILL", "-P", strconv.Itoa(cmd.Process.Pid)).Run()
		return cmd.Process.Kill()
	}
}
