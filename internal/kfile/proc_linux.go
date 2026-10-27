//go:build linux

package kfile

import (
	"os"
	"strconv"
	"strings"
)

// SystemProcs lists processes from /proc/<pid>/comm.
func SystemProcs() ([]Proc, error) {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var out []Proc
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile("/proc/" + e.Name() + "/comm")
		if err != nil {
			continue // exited meanwhile
		}
		out = append(out, Proc{PID: pid, Name: strings.TrimSpace(string(b))})
	}
	return out, nil
}
