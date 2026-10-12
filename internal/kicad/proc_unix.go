//go:build !windows

package kicad

import (
	"os/exec"
	"syscall"
	"time"
)

// ownGroup puts cmd in its own process group and makes a context timeout kill
// the whole group (a hung pcbnew / kicad-cli under macOS TCC ignores a plain
// SIGTERM to the leader and may leave children behind).
func ownGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
}
