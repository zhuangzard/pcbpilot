//go:build windows

package app

import (
	"errors"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	detachedProcess       = 0x00000008
)

func detachProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup | detachedProcess, HideWindow: true}
}

func execReplace(string, []string, []string) error {
	return errors.New("exec not supported on windows")
}
