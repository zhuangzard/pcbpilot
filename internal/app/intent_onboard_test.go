package app

import "testing"

// Intent nets named by display name resolve to the hierarchical board's
// identities everywhere the intent names a net; the loaded intent is not
// changed, and an ambiguous or absent name stays.
func TestIntentOnBoardResolvesDisplayNames(t *testing.T) {
	in := &designIntent{
		Nets: map[string]*intentNet{
			"VCC_PIC":  {Role: "power", DiffPair: "VPP/MCLR"},
			"VPP/MCLR": {Role: "signal"},
			"GND":      {Role: "ground"},
			"SDA":      {Role: "signal"}, // on two sheets: ambiguous
			"NOWHERE":  {Role: "signal"},
		},
		NetClasses: []intentNetClass{{Name: "PWR", Nets: []string{"VCC_PIC", "GND"}}},
		Domains:    []intentDomain{{ID: "LV", Nets: []string{"VCC_PIC", "VPP/MCLR"}}},
	}
	out, renamed := in.onBoard([]string{"/pic_sockets/VCC_PIC", "/VPP{slash}MCLR", "GND", "/a/SDA", "/b/SDA", "unconnected-(J1-Pad1)"})
	if len(renamed) != 2 {
		t.Fatalf("renamed %v", renamed)
	}
	for _, n := range []string{"/pic_sockets/VCC_PIC", "/VPP{slash}MCLR", "GND", "SDA", "NOWHERE"} {
		if out.Nets[n] == nil {
			t.Errorf("net %s missing: %v", n, out.Nets)
		}
	}
	if out.Nets["/pic_sockets/VCC_PIC"].DiffPair != "/VPP{slash}MCLR" {
		t.Errorf("diff pair not resolved: %+v", out.Nets["/pic_sockets/VCC_PIC"])
	}
	if got := out.NetClasses[0].Nets; got[0] != "/pic_sockets/VCC_PIC" || got[1] != "GND" {
		t.Errorf("netclass nets %v", got)
	}
	if got := out.Domains[0].Nets; got[1] != "/VPP{slash}MCLR" {
		t.Errorf("domain nets %v", got)
	}
	if in.Nets["VCC_PIC"] == nil || in.NetClasses[0].Nets[0] != "VCC_PIC" || in.Nets["VCC_PIC"].DiffPair != "VPP/MCLR" {
		t.Error("the receiver must not change")
	}
}
