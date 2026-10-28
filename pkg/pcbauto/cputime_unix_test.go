//go:build unix

package pcbauto

import (
	"syscall"
	"time"
)

// processCPUTime is this process's user + system CPU time so far.
func processCPUTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano()) + time.Duration(ru.Stime.Nano())
}
