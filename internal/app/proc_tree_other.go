//go:build !windows

package app

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// processParents maps every process to its parent: from /proc where there
// is one (no external program needed), else from POSIX ps.
func processParents() map[int]int {
	parents := map[int]int{}
	if stats, _ := filepath.Glob("/proc/[0-9]*/stat"); len(stats) > 0 {
		for _, st := range stats {
			b, err := os.ReadFile(st)
			if err != nil {
				continue // the process ended meanwhile
			}
			// "pid (comm) state ppid …": comm may hold spaces and brackets
			s := string(b)
			i := strings.LastIndexByte(s, ')')
			f := strings.Fields(s[i+1:])
			pid, perr := strconv.Atoi(filepath.Base(filepath.Dir(st)))
			if i < 0 || len(f) < 2 || perr != nil {
				continue
			}
			if ppid, err := strconv.Atoi(f[1]); err == nil {
				parents[pid] = ppid
			}
		}
		return parents
	}
	out, _ := exec.Command("ps", "-A", "-o", "pid=,ppid=").Output()
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		if e1 == nil && e2 == nil {
			parents[pid] = ppid
		}
	}
	return parents
}

// descendantsIn lists every process below pid in parents, a parent always
// before its children.
func descendantsIn(parents map[int]int, pid int) []int {
	children := map[int][]int{}
	for c, p := range parents {
		children[p] = append(children[p], c)
	}
	var all []int
	seen := map[int]bool{pid: true}
	for queue := []int{pid}; len(queue) > 0; queue = queue[1:] {
		for _, c := range children[queue[0]] {
			if c > 1 && !seen[c] {
				seen[c] = true
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
		tree := descendantsIn(processParents(), cmd.Process.Pid)
		for i := len(tree) - 1; i >= 0; i-- {
			_ = syscall.Kill(tree[i], syscall.SIGKILL)
		}
		return cmd.Process.Kill()
	}
}
