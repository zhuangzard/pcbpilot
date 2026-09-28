package pcbauto

import "testing"

// Classification order: PCIe/SATA TXP are not Ethernet; a USB3 connector's
// D+/D- pair is USB 2.0; an intent interface wins over the name.
func TestClassifyHSOrder(t *testing.T) {
	for net, want := range map[string]string{
		"PCIE20_TXP": "PCIe", "SATA_TXP": "SATA", "USB3_HOST1_DM": "USB2", "USB3_OTG0_DP": "USB2", "USB3_HOST1_SSRX_N": "USB3",
		"HDMI_0P": "HDMI", "ETH_MDI0_P": "Ethernet", "TRD1_N": "Ethernet", "DDR_A_DQS0P": "DDR", "CH340_D+": "USB2", "FOO_P": "diff",
	} {
		if got := ClassifyHS(&NetPlan{Net: net, Role: RoleDiff}); got.Name != want {
			t.Errorf("%s: %s, want %s", net, got.Name, want)
		}
	}
	np := &NetPlan{Net: "DQS0_P", Role: RoleDiff, Interface: "DDR", MaxSkewMil: 3, MaxVias: 3, LengthTolMil: 20}
	if hc := ClassifyHS(np); hc.Name != "DDR" || hc.MaxSkewMil != 3 || hc.MaxVias != 3 || hc.GroupSkewMil != 20 {
		t.Fatalf("intent overrides: %+v", hc)
	}
	if hc := ClassifyHS(&NetPlan{Net: "DQ3", Role: RoleSignal, Interface: "DDR", LengthGroup: "BYTE0"}); hc == nil || hc.GroupSkewMil != 25 || hc.MaxSkewMil != 0 {
		t.Fatalf("single-ended group member: %+v", hc)
	}
	if hc := ClassifyHS(&NetPlan{Net: "DQ3", Role: RoleSignal}); hc != nil {
		t.Fatalf("plain signal classified %+v", hc)
	}
}

// CheckSI measures length groups (a pair = one unit at its mean length) and
// flags a track with no adjacent plane.
func TestCheckSIGroupsAndReference(t *testing.T) {
	an := &Analysis{ByNet: map[string]*NetPlan{}}
	add := func(np *NetPlan) { an.Nets = append(an.Nets, np); an.ByNet[np.Net] = np }
	add(&NetPlan{Net: "D0_P", Role: RoleDiff, PairWith: "D0_N", Interface: "HDMI", LengthGroup: "G", LengthTolMil: 50})
	add(&NetPlan{Net: "D0_N", Role: RoleDiff, PairWith: "D0_P", Interface: "HDMI", LengthGroup: "G", LengthTolMil: 50})
	add(&NetPlan{Net: "D1_P", Role: RoleDiff, PairWith: "D1_N", Interface: "HDMI", LengthGroup: "G", LengthTolMil: 50})
	add(&NetPlan{Net: "D1_N", Role: RoleDiff, PairWith: "D1_P", Interface: "HDMI", LengthGroup: "G", LengthTolMil: 50})
	tr := func(net string, l float64, layer int) Track {
		return Track{Net: net, Layer: layer, A: Point{0, 0}, B: Point{l, 0}, Width: 8, Kind: "route"}
	}
	rr := &RouteResult{Tracks: []Track{tr("D0_P", 1000, 1), tr("D0_N", 1002, 1), tr("D1_P", 1100, 1), tr("D1_N", 1104, 1)}}
	st := &Stackup{Layers: 4, Stack: []StackLayer{{ID: 1, Kind: KindSignal}, {ID: 15, Kind: KindPlane, Nets: []string{"GND"}}, {ID: 16, Kind: KindPlane, Nets: []string{"+3V3"}}, {ID: 2, Kind: KindSignal}}}
	si := CheckSI(&Board{}, an, st, rr)
	if len(si.Groups) != 1 || si.Groups[0].SpreadMil != 101 || si.Groups[0].TolMil != 50 || len(si.Groups[0].Units) != 2 {
		t.Fatalf("groups %+v", si.Groups)
	}
	n := 0
	for _, f := range si.Findings {
		if f.Kind == "group-skew" {
			n++
		}
		if f.Kind == "no-reference" {
			t.Fatalf("4-layer outer layer flagged: %+v", f)
		}
	}
	if n != 1 {
		t.Fatalf("group-skew findings %d: %+v", n, si.Findings)
	}
	st2 := &Stackup{Layers: 2, Stack: []StackLayer{{ID: 1, Kind: KindSignal}, {ID: 2, Kind: KindSignal}}}
	si = CheckSI(&Board{}, an, st2, rr)
	nr := 0
	for _, f := range si.Findings {
		if f.Kind == "no-reference" {
			nr++
		}
	}
	if nr != 4 {
		t.Fatalf("2-layer: %d no-reference findings, want 4", nr)
	}
}

// An intent HS net pays to run next to a split power plane; plain nets and
// boards without a split see an unweighted search.
func TestReferenceLayerCost(t *testing.T) {
	st := &Stackup{Layers: 4, Stack: []StackLayer{{ID: 1, Kind: KindSignal}, {ID: 15, Kind: KindPlane, Nets: []string{"GND"}},
		{ID: 16, Kind: KindPlane, Nets: []string{"+3V3", "VBUS"}}, {ID: 2, Kind: KindSignal}}}
	m := referenceLayerCost(st, &NetPlan{Net: "USB3_SSTX_P", Role: RoleDiff, Interface: "USB3"})
	if m == nil || m[0] != 1 || m[3] != splitRefCost {
		t.Fatalf("mul %v", m)
	}
	if referenceLayerCost(st, &NetPlan{Net: "USB3_SSTX_P", Role: RoleDiff}) != nil {
		t.Fatal("non-intent net weighted")
	}
	// USB2 / Ethernet (< 1 Gb/s) are not priced: the ESP32 mini's USB_DM
	// detoured round its USBLC6 when they were (esd stub 68 → 299 mil).
	for _, np := range []*NetPlan{{Net: "USB_DM", Role: RoleDiff, PairWith: "USB_DP", Interface: "USB"}, {Net: "ETH_MDI0_P", Role: RoleDiff, Interface: "ETH"}} {
		if m := referenceLayerCost(st, np); m != nil {
			t.Fatalf("%s (%s) weighted: %v", np.Net, np.Interface, m)
		}
	}
	st.Stack[2].Nets = []string{"+3V3"}
	if referenceLayerCost(st, &NetPlan{Net: "USB3_SSTX_P", Role: RoleDiff, Interface: "USB3"}) != nil {
		t.Fatal("no split plane: no weighting expected")
	}
}
