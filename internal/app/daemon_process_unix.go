//go:build !windows

package app

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func listenerPID(port int) int {
	out, err := exec.Command("lsof", "-nP", fmt.Sprintf("-iTCP:%d", port), "-sTCP:LISTEN", "-t").Output()
	if err != nil {
		return 0
	}
	pid := 0
	for _, field := range strings.Fields(string(out)) {
		n, err := strconv.Atoi(field)
		if err != nil || n <= 0 || (pid != 0 && n != pid) {
			return 0
		}
		pid = n
	}
	return pid
}

func pidCommand(pid int) string {
	if pid <= 0 {
		return "unknown"
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
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
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return err
	}
	for range 20 {
		time.Sleep(100 * time.Millisecond)
		if err := proc.Signal(syscall.Signal(0)); errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone) {
			return nil
		}
	}
	return proc.Kill()
}
