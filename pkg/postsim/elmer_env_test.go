package postsim

import (
	"os"
	"path/filepath"
	"testing"
)

// A solver found outside PATH gets ELMER_HOME pointing at its own tree; a
// value the user set, or an unknown layout, is left alone.
func TestElmerEnvSetsHomeForUnpackedSolver(t *testing.T) {
	root := t.TempDir()
	for _, d := range []string{"bin", filepath.Join("share", "elmersolver")} {
		if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	bin := filepath.Join(root, "bin", "ElmerSolver.exe")
	base := []string{"PATH=x"}

	got := elmerEnv(bin, base)
	if len(got) != 2 || got[1] != "ELMER_HOME="+root {
		t.Fatalf("%v", got)
	}
	if len(base) != 1 {
		t.Fatal("the caller's environment was modified")
	}
	if got := elmerEnv(bin, []string{"PATH=x", "ELMER_HOME=/opt/elmer"}); len(got) != 2 || got[1] != "ELMER_HOME=/opt/elmer" {
		t.Fatalf("a user-set ELMER_HOME must win: %v", got)
	}
	if got := elmerEnv(filepath.Join(t.TempDir(), "ElmerSolver"), base); len(got) != 1 {
		t.Fatalf("unknown layout must not invent ELMER_HOME: %v", got)
	}
}
