package app

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func listenerPID(port int) int {
	out, err := exec.Command("netstat.exe", "-ano", "-p", "tcp").Output()
	if err != nil {
		return 0
	}
	return windowsListenerPID(string(out), port)
}

func pidCommand(pid int) string {
	if pid <= 0 {
		return "unknown"
	}
	// PID is an integer, never interpolated user shell input.
	out, err := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-Process -Id "+strconv.Itoa(pid)+" -ErrorAction Stop).Path").Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func termPID(pid int) error {
	if pid <= 0 || pid == os.Getpid() {
		return fmt.Errorf("refusing invalid/self PID %d", pid)
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	defer proc.Release()
	// Windows does not support Unix SIGTERM/Signal(0). Kill only this verified
	// daemon process, never its process tree or every process named easyeda.exe.
	return proc.Kill()
}
