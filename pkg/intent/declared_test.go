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
