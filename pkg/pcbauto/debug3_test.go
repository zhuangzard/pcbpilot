package pcbauto

import (
	"os"
	"testing"
)

func TestDebugSides(t *testing.T) {
	f := os.Getenv("PCBAUTO_SIDES")
	if f == "" {
		t.Skip()
	}
	b := loadFixture(t, f)
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	bot, botCap, caps := 0, 0, 0
	for _, p := range b.Parts {
		if p.Side == LayerBottom {
			bot++
		}
		if c.Kinds[p.Ref] == KindCapacitor {
			caps++
			if p.Side == LayerBottom {
				botCap++
			}
		}
	}
	t.Logf("parts %d bottom %d caps %d bottomCaps %d", len(b.Parts), bot, caps, botCap)
	for _, bl := range c.Blocks {
		n := map[string]int{}
		for _, m := range bl.Members {
			n[m.Role]++
		}
		if len(bl.Members) > 8 && b.Part(bl.Core) != nil {
			core := b.Part(bl.Core)
			t.Logf("%s kind=%s pads=%d body=%.0fx%.0f members=%v", bl.ID, bl.Kind, len(core.Pads), core.Body().W(), core.Body().H(), n)
		}
	}
}
