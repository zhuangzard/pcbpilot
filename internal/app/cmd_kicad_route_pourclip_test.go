package app

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// A planned pour clipped to nothing fails the pour-clip gate by name; all
// kept passes.
func TestPourClipGate(t *testing.T) {
	if g := pourClipGate(4, nil, []string{"GND on layer 1 clipped"}); !g.Pass || len(g.Items) != 0 {
		t.Fatalf("all kept: %+v", g)
	}
	g := pourClipGate(4, []pcbauto.PlaneRegion{{Net: "+3V3", Layer: 16}}, nil)
	if g.Pass || len(g.Items) != 1 || !strings.Contains(g.Items[0], "+3V3 on layer 16") || !strings.Contains(g.Detail, "1 of 4") {
		t.Fatalf("lost pour: %+v", g)
	}
}
