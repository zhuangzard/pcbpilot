//go:build windows

package pcbauto

import (
	"syscall"
	"time"
)

// processCPUTime is this process's user + kernel CPU time so far
// (Windows has no getrusage; GetProcessTimes reports 100 ns units).
func processCPUTime() time.Duration {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var creation, exit, kernel, user syscall.Filetime
	if err := syscall.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return 0
	}
	ticks := func(f syscall.Filetime) int64 { return int64(f.HighDateTime)<<32 | int64(f.LowDateTime) }
	return time.Duration((ticks(kernel) + ticks(user)) * 100)
}
