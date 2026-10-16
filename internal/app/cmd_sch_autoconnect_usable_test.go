package app

import (
	"strings"
	"testing"
)

// Live 2026-10-16: the W25Q64's right-side labels were staggered to offset 98 and
// ended past the usable sheet edge (out-of-sheet ERROR) although offset 18 fit.
// A label outside the printable area is a hard reject, like the title block.
func TestScoreCandidateRejectsLabelOutsideUsableSheet(t *testing.T) {
	rot := 0.0
	pin := acPin{X: 1022, Y: 415, Designator: "U1", PinNumber: "5", PinRotation: &rot}
	usable := schUsableArea(layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825})
	scene := acScene{Usable: &usable}
	rules := defaultAutoconnectRules()
	hard := func(c acCandidate) bool {
		for _, r := range c.Reasons {
			if strings.Contains(r.Desc, "usable sheet") {
				return true
			}
		}
		return false
	}
	if c := scoreCandidate(pin, "right", 98, "netport", "MCU_N10", scene, rules); !hard(c) {
		t.Fatalf("a label ending past the usable edge must be hard-rejected: %+v", c)
	}
	if c := scoreCandidate(pin, "right", 18, "netport", "MCU_N10", scene, rules); hard(c) {
		t.Fatalf("a label inside the usable sheet must not be rejected for it: %+v", c)
	}
}
