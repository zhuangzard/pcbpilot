//go:build !windows

package app

import (
	"os"
	"testing"
)

func TestDescendantsIn(t *testing.T) {
	// 10 -> 11 -> 12, 10 -> 13; 20 is unrelated; a cycle must not loop
	parents := map[int]int{11: 10, 12: 11, 13: 10, 20: 1, 10: 12}
	got := descendantsIn(parents, 10)
	pos := map[int]int{}
	for i, p := range got {
		pos[p] = i
	}
	if len(got) != 3 || pos[11] > pos[12] {
		t.Fatalf("%v", got)
	}
	if _, ok := pos[20]; ok {
		t.Fatalf("unrelated process listed: %v", got)
	}
	if d := descendantsIn(parents, 99); len(d) != 0 {
		t.Fatalf("%v", d)
	}
}

// The table must know this very process and its parent, whichever source
// the platform offers (/proc or ps).
func TestProcessParents(t *testing.T) {
	if got := processParents()[os.Getpid()]; got != os.Getppid() {
		t.Fatalf("parent of %d: %d, want %d", os.Getpid(), got, os.Getppid())
	}
}
