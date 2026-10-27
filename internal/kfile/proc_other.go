//go:build !windows && !linux

package kfile

import (
	"os/exec"
	"strconv"
	"strings"
)

// SystemProcs lists processes with `ps -A -o pid= -o comm=` (macOS, BSD).
func SystemProcs() ([]Proc, error) {
	b, err := exec.Command("ps", "-A", "-o", "pid=", "-o", "comm=").Output()
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, ln := range strings.Split(string(b), "\n") {
		pid, name, ok := strings.Cut(strings.TrimSpace(ln), " ")
		n, err := strconv.Atoi(pid)
		if !ok || err != nil {
			continue
		}
		out = append(out, Proc{PID: n, Name: strings.TrimSpace(name)})
	}
	return out, nil
}
