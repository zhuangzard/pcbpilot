package pcbauto

import (
	"os"
	"testing"
	"time"
)

func TestDebugPlaceTime(t *testing.T) {
	f := os.Getenv("PCBAUTO_TIME")
	if f == "" {
		t.Skip()
	}
	b := loadFixture(t, f)
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	for _, p := range b.Parts {
		if k := c.Kinds[p.Ref]; k == KindConnector || k == KindMechanical || k == KindAntenna {
			p.Fixed = true
		}
	}
	start := time.Now()
	_, _ = Place(b, an, c, nil, PlaceOptions{Timeout: 20 * time.Second})
	t.Logf("place %v", time.Since(start))
}
