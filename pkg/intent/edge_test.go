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
		`{"edge":{"domainInsulation":{"X":"functional"}}}`,
		`{"edge":{"footprintCutouts":"none"}}`,
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

// spec.edge.domainInsulation grades one domain (it wins over
// spec.edge.insulation; the stricter of two keys of the same domain
// counts), and spec.edge.footprintCutouts reaches the intent only as the
// owner's explicit "internal".
func TestSpecEdgeDomainGradeAndFootprintCutouts(t *testing.T) {
	withSafety(t)
	base := stressIntent(t, "flyback", "spec.json", nil)
	var mains *Domain
	for _, d := range base.Domains {
		if d.Kind == "mains" {
			mains = d
		}
	}
	if mains == nil || len(mains.Nets) == 0 {
		t.Fatal("no mains domain in the fixture")
	}
	bd := base.Edge.ByDomain[mains.ID]
	if bd.Insulation != "reinforced" || base.Edge.FootprintCutouts != "" {
		t.Fatalf("default: %+v cutouts %q", bd, base.Edge.FootprintCutouts)
	}
	it := stressIntent(t, "flyback", "spec.json", func(in *Input) {
		in.Spec.Edge = &SpecEdge{Insulation: "reinforced", DomainInsulation: map[string]string{mains.ID: "basic"}, FootprintCutouts: "internal"}
	})
	ed := it.Edge.ByDomain[mains.ID]
	if ed.Insulation != "basic" || ed.Mil >= bd.Mil || !strings.Contains(strings.Join(ed.Why, " "), "spec.edge.domainInsulation["+mains.ID+"] = basic") {
		t.Fatalf("domain grade: %+v (default %.1f mil)", ed, bd.Mil)
	}
	if it.Edge.FootprintCutouts != "internal" || !strings.Contains(strings.Join(it.Edge.Why, " "), "spec.edge.footprintCutouts = internal") {
		t.Fatalf("cutouts: %q %v", it.Edge.FootprintCutouts, it.Edge.Why)
	}
	// Two keys of one domain (its id and one of its nets): the stricter.
	two := stressIntent(t, "flyback", "spec.json", func(in *Input) {
		in.Spec.Edge = &SpecEdge{DomainInsulation: map[string]string{mains.ID: "basic", mains.Nets[0]: "reinforced"}, FootprintCutouts: "edge"}
	})
	if d := two.Edge.ByDomain[mains.ID]; d.Insulation != "reinforced" || d.Mil != bd.Mil {
		t.Fatalf("two keys: %+v", d)
	}
	if two.Edge.FootprintCutouts != "" {
		t.Fatalf("edge is the default and is not written: %q", two.Edge.FootprintCutouts)
	}
}

// A domainInsulation key equal to one domain's id and to a net of another
// grades only the domain of that id; a key that names several domains
// grades none.
func TestSpecEdgeDomainInsulationKeyNamesOneDomain(t *testing.T) {
	withSafety(t)
	base := stressIntent(t, "flyback", "spec.json", nil)
	var mains *Domain
	for _, d := range base.Domains {
		if d.Kind == "mains" {
			mains = d
		}
	}
	if mains == nil || len(mains.Nets) == 0 {
		t.Fatal("no mains domain in the fixture")
	}
	c := &ctx{out: &Intent{Standard: base.Standard}}
	other := &Domain{ID: "HVX", Kind: mains.Kind, WorkingVrms: mains.WorkingVrms, WorkingVpeak: mains.WorkingVpeak, Nets: []string{mains.ID, "SHARED"}}
	first := &Domain{ID: mains.ID, Kind: mains.Kind, WorkingVrms: mains.WorkingVrms, WorkingVpeak: mains.WorkingVpeak, Nets: []string{"AC_L", "SHARED"}}
	c.out.Domains = []*Domain{first, other}
	c.spec.Edge = &SpecEdge{DomainInsulation: map[string]string{mains.ID: "basic", "SHARED": "basic", "NOPE": "basic"}}
	c.buildEdge()
	e := c.out.Edge
	if e.ByDomain[mains.ID].Insulation != "basic" {
		t.Fatalf("the domain of that id: %+v", e.ByDomain[mains.ID])
	}
	if e.ByDomain["HVX"].Insulation != "reinforced" {
		t.Fatalf("a net named like another domain's id must not lower its domain: %+v", e.ByDomain["HVX"])
	}
	// The ambiguous key still raises: with spec.edge.insulation basic, both
	// domains it names go up to reinforced.
	c.spec.Edge = &SpecEdge{Insulation: "basic", DomainInsulation: map[string]string{"SHARED": "reinforced"}}
	c.buildEdge()
	for _, id := range []string{mains.ID, "HVX"} {
		if c.out.Edge.ByDomain[id].Insulation != "reinforced" {
			t.Fatalf("%s: an ambiguous key must still raise: %+v", id, c.out.Edge.ByDomain[id])
		}
	}
	why := strings.Join(e.Why, "\n")
	for _, want := range []string{"domainInsulation[SHARED] names several domains", "domainInsulation[NOPE] ignored: no domain or net", "not the net of that name in HVX"} {
		if !strings.Contains(why, want) {
			t.Fatalf("missing %q in:\n%s", want, why)
		}
	}
}
