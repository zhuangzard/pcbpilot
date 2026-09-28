package intent

import (
	"math"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Every intent states the board-edge distance; the flyback's mains domain
// keeps reinforced clearance/creepage to the accessible edge (pkg/safety),
// the SELV secondary the defaults.
func TestDeriveEdgeFlyback(t *testing.T) {
	withSafety(t)
	it := stressIntent(t, "flyback", "spec.json", nil)
	e := it.Edge
	if e == nil || e.EdgeKind != "routed" || e.OuterMil != 20 || e.InnerMil != 30 || e.VcutMil != 0 {
		t.Fatalf("edge %+v", e)
	}
	var mains *Domain
	for _, d := range it.Domains {
		if d.Kind == "mains" {
			mains = d
		}
		if d.Kind == "SELV" && e.ByDomain[d.ID] != nil {
			t.Fatalf("SELV domain %s got an edge distance", d.ID)
		}
	}
	if mains == nil {
		t.Fatal("no mains domain")
	}
	ed := e.ByDomain[mains.ID]
	if ed == nil || ed.Insulation != "reinforced" {
		t.Fatalf("mains edge %+v", ed)
	}
	want := math.Ceil(math.Max(ed.ClearanceMm, ed.CreepageMm)/0.0254*10) / 10
	if ed.Mil != want || ed.CreepageMm < 6 || ed.Mil < 236 {
		t.Fatalf("mains edge %.1f mil (clear %.2f creep %.2f), want %.1f", ed.Mil, ed.ClearanceMm, ed.CreepageMm, want)
	}
	if !strings.Contains(strings.Join(ed.Why, " "), "accessible") {
		t.Fatalf("why %v", ed.Why)
	}
	// The engine reads the same numbers (pcbauto.IntentEdge is the type).
	pi := &pcbauto.Intent{Edge: it.Edge}
	for _, d := range it.Domains {
		pi.Domains = append(pi.Domains, pcbauto.IntentDomain{ID: d.ID, Kind: d.Kind, Nets: d.Nets})
	}
	pol := pcbauto.EdgeFromIntent(pi, nil)
	if pol.NetReq("L") != ed.Mil || pol.NetReq("VOUT") != 0 {
		t.Fatalf("engine policy L %v VOUT %v", pol.NetReq("L"), pol.NetReq("VOUT"))
	}
}

// spec.edge: V-cut, overrides (never below the fab floor), the grade the
// enclosure lets the edge take, and a domain raise.
func TestSpecEdge(t *testing.T) {
	withSafety(t)
	for _, bad := range []string{
		`{"edge":{"edgeKind":"laser"}}`,
		`{"edge":{"outerMil":5}}`,
		`{"edge":{"edgeKind":"vcut","outerMil":12}}`,
		`{"edge":{"insulation":"functional"}}`,
		`{"edge":{"domainMil":{"X":-1}}}`,
	} {
		if _, err := ParseSpec([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	base := stressIntent(t, "flyback", "spec.json", nil)
	var mainsID string
	for _, d := range base.Domains {
		if d.Kind == "mains" {
			mainsID = d.ID
		}
	}
	it := stressIntent(t, "flyback", "spec.json", func(in *Input) {
		in.Spec.Edge = &SpecEdge{EdgeKind: "vcut", InnerMil: 40, Insulation: "basic", DomainMil: map[string]float64{"L": 400}}
	})
	e := it.Edge
	if e.EdgeKind != "vcut" || e.OuterMil != 20 || e.InnerMil != 40 || e.VcutMil != 20 {
		t.Fatalf("vcut edge %+v", e)
	}
	ed := e.ByDomain[mainsID]
	if ed.Insulation != "basic" || ed.Mil != 400 {
		t.Fatalf("basic + domainMil raise: %+v", ed)
	}
	// A domainMil below the insulation distance is ignored (noted).
	low := stressIntent(t, "flyback", "spec.json", func(in *Input) {
		in.Spec.Edge = &SpecEdge{DomainMil: map[string]float64{mainsID: 10}}
	})
	if d := low.Edge.ByDomain[mainsID]; d.Mil != base.Edge.ByDomain[mainsID].Mil || !strings.Contains(strings.Join(d.Why, " "), "ignored") {
		t.Fatalf("low domainMil: %+v", d)
	}
}
