package pcbauto

import (
	"os"
	"testing"
)

// Which obstacles sit on the voids of a BGA: pads of other parts, by side.
func TestDebugVoidBlockers(t *testing.T) {
	if os.Getenv("PCBAUTO_VOIDS") == "" {
		t.Skip()
	}
	b := loadFixture(t, "lckfb-rk3568-4layer.json")
	for _, g := range detectBGAs(b) {
		if g.part.Ref != "U4" {
			continue
		}
		voids := bgaVoids(g)
		_, dia, _ := bgaViaClass(g, b.Rules)
		r := dia/2 + b.Rules.Clearance
		bySide := map[string]int{}
		parts := map[string]int{}
		free := 0
		for _, v := range voids {
			hit := ""
			for _, p := range b.Parts {
				if p == g.part {
					continue
				}
				for _, pd := range p.Pads {
					if pd.Box.Dist(v) < r {
						hit = map[int]string{LayerTop: "top", LayerBottom: "bottom", LayerMulti: "tht"}[pd.Layer]
						parts[p.Ref]++
						break
					}
				}
				if hit != "" {
					break
				}
			}
			if hit == "" {
				free++
			} else {
				bySide[hit]++
			}
		}
		t.Logf("U4 voids %d: free %d, blocked by %v; via Ø %.1f (r+clr %.1f); top blockers %v", len(voids), free, bySide, dia, r, topN(parts, 8))
	}
}

func topN(m map[string]int, n int) map[string]int {
	out := map[string]int{}
	for len(out) < n && len(m) > 0 {
		best, bv := "", -1
		for k, v := range m {
			if _, ok := out[k]; !ok && v > bv {
				best, bv = k, v
			}
		}
		if best == "" {
			break
		}
		out[best] = bv
		if len(out) == len(m) {
			break
		}
	}
	return out
}
