package kicad

import (
	"slices"
	"testing"
)

func TestCompareVersionDirsIsNumeric(t *testing.T) {
	v := func(x string) string { return "/K/Python.framework/Versions/" + x + "/bin/python3" }
	m := []string{v("3.10"), v("3.9"), v("3.11")}
	slices.SortFunc(m, compareVersionDirs)
	if m[0] != v("3.9") || m[2] != v("3.11") {
		t.Fatalf("order %v", m)
	}
}
