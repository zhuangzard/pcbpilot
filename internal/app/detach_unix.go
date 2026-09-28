//go:build !windows

package app

import (
	"os/exec"
	"syscall"
)

// detachProcess starts cmd in its own session so a service restart of the
// parent (launchd kills the job's process group) cannot kill it.
func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

// execReplace replaces this process image (used after a self-update).
func execReplace(path string, argv, env []string) error {
	return syscall.Exec(path, argv, env)
}
