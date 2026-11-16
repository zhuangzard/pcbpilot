package intent

import (
	"encoding/json"
	"strings"
	"testing"
)

func pairOf(it *Intent, a, b string) *Pair {
	for _, p := range it.Pairs {
		if p.A == a && p.B == b || p.A == b && p.B == a {
			return p
		}
	}
	return nil
}

// A declared distance raises a pair above the standard's number and
// records the source; a smaller one keeps the derived value; a
// declaration matching no pair is a warning; without declarations the
// pairs carry none of the new fields.
func TestDeclaredIsolationDistance(t *testing.T) {
	base := mainsIntent(t, nil)
	fn := pairOf(base, "domain:SELV_5V", "domain:ISO_24V")
	if fn == nil {
		t.Fatalf("pairs %+v", base.Pairs)
	}
	b, _ := json.Marshal(base.Pairs)
	for _, k := range []string{"clearanceSource", "creepageSource", "declaredClearanceMm", "derivedClearanceMm"} {
		if strings.Contains(string(b), k) {
			t.Fatalf("undeclared intent carries %s: %s", k, b)
		}
	}
	derivedClr, derivedCreep := fn.ClearanceMm, fn.CreepageMm

	// spec.isolation by a net of each domain: clearance 8 (above), creepage
	// 0.1 (below the derived): clearance declared, creepage raised to the
	// clearance (never below it).
	it := mainsIntent(t, func(s *Spec) {
		s.Isolation = []SpecIsolation{{A: "FIELD_24V", B: "+3V3", ClearanceMm: 8, CreepageMm: 0.1, Why: "design spec §2"}}
	})
	p := pairOf(it, "domain:SELV_5V", "domain:ISO_24V")
	if p.ClearanceMm != 8 || p.ClearanceSource != "declared" || p.DerivedClearanceMm != derivedClr || p.DeclaredClearanceMm != 8 {
		t.Fatalf("clearance %+v", p)
	}
	if p.CreepageMm != 8 || p.CreepageSource != "declared" || p.DerivedCreepageMm != derivedCreep {
		t.Fatalf("creepage %+v", p)
	}
	if !strings.Contains(strings.Join(p.Why, "\n"), "design spec §2") {
		t.Fatalf("why %v", p.Why)
	}
	// Other pairs untouched.
	for _, q := range it.Pairs {
		if q != p && q.ClearanceSource != "" {
			t.Fatalf("pair %s|%s got a declaration", q.A, q.B)
		}
	}

	// spec.domains clearanceMm below the derived value: derived wins.
	it = mainsIntent(t, func(s *Spec) {
		s.Isolation = []SpecIsolation{{A: "domain:ISO_24V", B: "domain:SELV_5V", ClearanceMm: derivedClr / 2}}
	})
	p = pairOf(it, "domain:SELV_5V", "domain:ISO_24V")
	if p.ClearanceMm != derivedClr || p.ClearanceSource != "derived" || p.CreepageSource != "derived" {
		t.Fatalf("smaller declaration: %+v", p)
	}

	// Unmatched declaration: a warning finding.
	it = mainsIntent(t, func(s *Spec) {
		s.Isolation = []SpecIsolation{{A: "NO_SUCH_NET", B: "+3V3", ClearanceMm: 8}}
	})
	warned := false
	for _, f := range it.Findings {
		warned = warned || f.Kind == "declared-isolation-unmatched" && f.Severity == "warn"
	}
	if !warned {
		t.Fatalf("no unmatched warning: %+v", it.Findings)
	}
}

func TestDeclaredIsolationSpecDomain(t *testing.T) {
	it := mainsIntent(t, func(s *Spec) {
		s.Domains = append(s.Domains, SpecDomain{Kind: "isolated-secondary", Nets: []string{"FIELD_24V"}, ClearanceMm: 9, CreepageMm: 10})
	})
	n := 0
	for _, p := range it.Pairs {
		if strings.Contains(p.A+p.B, "ISO_24V") {
			n++
			if p.ClearanceMm < 9 || p.CreepageMm < 10 || p.DeclaredCreepageMm != 10 {
				t.Fatalf("pair %+v", p)
			}
		}
	}
	if n == 0 {
		t.Fatalf("no pair with the field domain: %+v", it.Pairs)
	}
}

func TestParseSpecIsolation(t *testing.T) {
	if _, err := ParseSpec([]byte(`{"isolation":[{"a":"X","b":"Y","clearanceMm":8,"creepageMm":8,"why":"spec"}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"isolation":[{"a":"X","b":"","clearanceMm":8}]}`,
		`{"isolation":[{"a":"X","b":"Y"}]}`,
		`{"isolation":[{"a":"X","b":"Y","clearanceMm":-1}]}`,
		`{"domains":[{"kind":"SELV","nets":["X"],"clearanceMm":-2}]}`,
	} {
		if _, err := ParseSpec([]byte(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

// A spec.domains declaration whose domain is in no pair warns even when an
// unrelated pair carries an equal or larger declared distance.
func TestDeclaredIsolationUnmatchedDomainWarns(t *testing.T) {
	it := mainsIntent(t, func(s *Spec) {
		s.Isolation = []SpecIsolation{{A: "FIELD_24V", B: "+3V3", ClearanceMm: 12}}
		s.Domains = append(s.Domains, SpecDomain{Kind: "SELV", Nets: []string{"NO_SUCH_NET"}, ClearanceMm: 5})
	})
	warned := false
	for _, f := range it.Findings {
		warned = warned || f.Kind == "declared-isolation-unmatched" && strings.Contains(f.Message, "spec.domains[")
	}
	if !warned {
		t.Fatalf("no warning for the unmatched spec.domains declaration: %+v", it.Findings)
	}
}

// A pair's grade stated in spec.isolation replaces the derived one: a
// lower grade gives the lower distances, says so in the pair and is a
// warning; it needs a why, and a grade outside the list is refused.
func TestDeclaredPairInsulation(t *testing.T) {
	base := mainsIntent(t, nil)
	var strict *Pair
	for _, p := range base.Pairs {
		if insulRank[p.Insulation] >= 2 {
			strict = p
		}
	}
	if strict == nil {
		t.Fatalf("fixture has no reinforced pair: %+v", base.Pairs)
	}
	it := mainsIntent(t, func(s *Spec) {
		s.Isolation = []SpecIsolation{{A: strict.A, B: strict.B, Insulation: "basic", Why: "protective earth is the other means"}}
	})
	p := pairOf(it, strict.A, strict.B)
	if p.Insulation != "basic" || p.ClearanceMm >= strict.ClearanceMm || p.CreepageMm >= strict.CreepageMm {
		t.Fatalf("declared basic: %s %g/%g, derived %s %g/%g", p.Insulation, p.ClearanceMm, p.CreepageMm, strict.Insulation, strict.ClearanceMm, strict.CreepageMm)
	}
	if !strings.Contains(strings.Join(p.Why, "; "), "protective earth is the other means") {
		t.Fatalf("why %v", p.Why)
	}
	warned := false
	for _, f := range it.Findings {
		warned = warned || f.Kind == "declared-insulation-lowered" && f.Severity == "warn"
		if f.Kind == "declared-isolation-unmatched" {
			t.Fatalf("a matched declaration was reported unmatched: %+v", f)
		}
	}
	if !warned {
		t.Fatal("a lowered grade was not reported")
	}
	for _, q := range it.Pairs {
		if o := pairOf(base, q.A, q.B); q != p && (o == nil || o.Insulation != q.Insulation || o.ClearanceMm != q.ClearanceMm) {
			t.Fatalf("pair %s|%s changed", q.A, q.B)
		}
	}
	// The derived grade declared again changes nothing and warns nothing.
	it = mainsIntent(t, func(s *Spec) {
		s.Isolation = []SpecIsolation{{A: strict.A, B: strict.B, Insulation: strict.Insulation, Why: "as derived"}}
	})
	if p = pairOf(it, strict.A, strict.B); p.Insulation != strict.Insulation || p.ClearanceMm != strict.ClearanceMm {
		t.Fatalf("same grade: %+v", p)
	}
	for _, f := range it.Findings {
		if f.Kind == "declared-insulation-lowered" {
			t.Fatalf("no grade was lowered: %+v", f)
		}
	}
	// An explicit product-level count of two means: the pair's declaration
	// is the more specific one, and both the why and the warning say that
	// this pair counts one.
	two := func(s *Spec) { s.Standard.Name, s.Standard.MOP, s.Standard.MOPCount = "IEC60601-1", "MOPP", 2 }
	mbase := mainsIntent(t, two)
	var mp *Pair
	for _, q := range mbase.Pairs {
		if q.MOPCount == 2 {
			mp = q
		}
	}
	if mp == nil {
		t.Fatalf("fixture has no 2 x MOPP pair: %+v", mbase.Pairs)
	}
	it = mainsIntent(t, func(s *Spec) {
		two(s)
		s.Isolation = []SpecIsolation{{A: mp.A, B: mp.B, Insulation: "basic", Why: "earthed screen"}}
	})
	p = pairOf(it, mp.A, mp.B)
	if p.Insulation != "basic" || p.MOPCount != 1 || !strings.Contains(strings.Join(p.Why, "; "), "1 × MOPP for this pair instead of 2") {
		t.Fatalf("declared basic under 2 x MOPP: %s %d %v", p.Insulation, p.MOPCount, p.Why)
	}
	said := false
	for _, f := range it.Findings {
		said = said || f.Kind == "declared-insulation-lowered" && strings.Contains(f.Message, "1 × MOPP for this pair instead of 2")
	}
	if !said {
		t.Fatal("the lowered count of means was not reported")
	}
	it = mainsIntent(t, func(s *Spec) {
		two(s)
		s.Isolation = []SpecIsolation{{A: mp.A, B: mp.B, Insulation: mp.Insulation, Why: "as derived"}}
	})
	if p = pairOf(it, mp.A, mp.B); p.MOPCount != 2 || p.ClearanceMm != mp.ClearanceMm {
		t.Fatalf("same grade under 2 x MOPP: %+v", p)
	}
	if _, err := ParseSpec([]byte(`{"isolation":[{"a":"X","b":"Y","insulation":"basic","why":"PE"}]}`)); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		`{"isolation":[{"a":"X","b":"Y","insulation":"basic"}]}`,
		`{"isolation":[{"a":"X","b":"Y","insulation":"functional","why":"w"}]}`,
		`{"isolation":[{"a":"X","b":"Y","insulation":"none","why":"w"}]}`,
	} {
		if _, err := ParseSpec([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
