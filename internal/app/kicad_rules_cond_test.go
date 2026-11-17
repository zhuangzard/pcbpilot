package app

import "testing"

// A condition with a nested alternative or a negated class is decided for
// a net whose classes are known: the rule governs it or provably does not.
func TestDRUNeverGoverns(t *testing.T) {
	bus := &druItem{net: "VBUS", may: map[string]bool{"VBUS": true, "HOT": true, "PP3_W10": true}, sure: map[string]bool{"HOT": true}}
	sig := &druItem{net: "SIG", may: map[string]bool{"SIG": true, "PP1_W8": true}, sure: map[string]bool{}}
	rest := "(B.NetName != '' && !B.hasNetclass('HOT') && !B.hasNetclass('MID'))"
	for _, c := range []struct {
		cond     string
		bus, sig bool
	}{
		{"A.hasNetclass('OUT') && (B.hasNetclass('RTN') || B.hasNetclass('RTN2'))", true, true},
		{"A.hasNetclass('ISO') && (" + rest + " || B.hasNetclass('RTN'))", true, false},
		{"A.hasNetclass('HOT') && " + rest, false, false},
		{"A.hasNetclass('HOT') && B.hasNetclass('HOT') && A.NetName != B.NetName", false, true},
		{"A.hasNetclass('OUT') && B.memberOfFootprint('MH*')", false, false},
		{"A.hasNetclass('OUT') && B.hasNetclass('RTN') && B.memberOfFootprint('MH*')", true, true},
		{"A.hasNetclass('OUT') || B.memberOfFootprint('MH*')", false, false},
		{"!A.hasNetclass('HOT')", false, false},
		{"!(A.hasNetclass('HOT') || B.hasNetclass('HOT'))", true, false},
		{"A.hasNetclass('H*')", false, false},
		{"A.NetName == 'VBUS'", false, false},
		{"A.NetName == 'VBUS' && B.NetName == 'OTHER'", false, true},
		{"A.NetName != 'VBUS' && B.NetName != 'VBUS'", true, false},
		{"A.NetClass == 'HOT'", false, false},
		{"A.NetClass == 'HOT' && B.NetClass == 'HOT'", false, true},
		{"A.NetClass != 'HOT'", false, false},
		{"A.Type == 'Via'", false, false},
		{"!A.Type == 'Via'", false, false},
		{"", false, false},
	} {
		if got := druNeverGoverns(c.cond, bus); got != c.bus {
			t.Errorf("%q never governs the bus net: %v, want %v", c.cond, got, c.bus)
		}
		if got := druNeverGoverns(c.cond, sig); got != c.sig {
			t.Errorf("%q never governs the signal net: %v, want %v", c.cond, got, c.sig)
		}
	}
	// A class the net only may be in decides nothing, either way.
	maybe := &druItem{net: "N", may: map[string]bool{"N": true, "HOT": true}, sure: map[string]bool{}}
	for _, cond := range []string{"A.hasNetclass('HOT')", "!A.hasNetclass('HOT')"} {
		if druNeverGoverns(cond, maybe) {
			t.Errorf("%q was decided on a class the net only may be in", cond)
		}
	}
}
