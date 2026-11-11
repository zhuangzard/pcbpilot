//go:build !windows

package app

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// descendants lists every process below pid, parents before children
// (pgrep -P, one level at a time).
func descendants(pid int) []int {
	var all []int
	for queue := []int{pid}; len(queue) > 0 && len(all) < 4096; queue = queue[1:] {
		out, _ := exec.Command("pgrep", "-P", strconv.Itoa(queue[0])).Output()
		for _, f := range strings.Fields(string(out)) {
			if c, err := strconv.Atoi(f); err == nil && c > 1 {
				all = append(all, c)
				queue = append(queue, c)
			}
		}
	}
	return all
}

// killTreeOnCancel makes a cancelled or timed-out cmd take its descendants
// with it (a launcher script that starts the real program instead of
// exec'ing it, through any number of wrappers), deepest first. The command
// stays in pcbpilot's process group, so a Ctrl+C in the terminal still
// reaches it.
func killTreeOnCancel(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		tree := descendants(cmd.Process.Pid)
		for i := len(tree) - 1; i >= 0; i-- {
			_ = syscall.Kill(tree[i], syscall.SIGKILL)
		}
		return cmd.Process.Kill()
	}
}
