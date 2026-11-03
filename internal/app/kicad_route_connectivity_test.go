package app

import (
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// kicad-cli caps unconnected_items: the count is pcbnew's, and a DRC that
// lists fewer says so.
func TestBoardConnectivityEntry(t *testing.T) {
	capped := &kicad.DRCReport{Counts: map[string]int{"unconnected_items": 499}}
	if bc := boardConnectivityEntry(2011, capped); bc["unconnected"] != 2011 || bc["drcUnconnected"] != 499 || bc["drcTruncated"] != true {
		t.Fatalf("capped: %v", bc)
	}
	if bc := boardConnectivityEntry(499, capped); bc["drcTruncated"] != nil {
		t.Fatalf("equal counts are not truncated: %v", bc)
	}
	if bc := boardConnectivityEntry(3, nil); bc["unconnected"] != 3 || bc["drcUnconnected"] != nil || bc["drcTruncated"] != nil {
		t.Fatalf("no DRC report: %v", bc)
	}
}
