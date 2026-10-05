package schaes

import (
	"strings"
	"testing"
)

func TestNativeBusName(t *testing.T) {
	for _, c := range []struct {
		cand BusCandidate
		want string
	}{
		{BusCandidate{Kind: "indexed", Key: "D", Suggested: "D[0:7]"}, "D[0:7]"},
		{BusCandidate{Kind: "spi", Key: "SPI1", Suggested: "SPI1_SPI"}, ""},
		{BusCandidate{Kind: "qspi", Key: "FLASH_QSPI", Suggested: "FLASH_QSPI_QSPI"}, ""},
		{BusCandidate{Kind: "uart", Key: "ESP", Suggested: "ESP_UART"}, ""},
		{BusCandidate{Kind: "uart", Key: "U0", Suggested: "U0_UART"}, ""},
		{BusCandidate{Kind: "i2c", Key: "", Suggested: "I2C_I2C"}, ""},
		{BusCandidate{Kind: "spi", Key: "", Suggested: "SPI_SPI"}, ""},
		{BusCandidate{Kind: "usb", Key: "USB", Suggested: "USB"}, ""},
		{BusCandidate{Kind: "mipi", Key: "CSI", Suggested: "CSI_LANES"}, ""},
	} {
		if got := NativeBusName(c.cand); got != c.want {
			t.Errorf("%+v: got %q want %q", c.cand, got, c.want)
		}
		// Live V3 3.2.149 (2026-10-04): the host only accepts NAME[a:b];
		// every group without a native name says why.
		if c.want == "" && NativeBusSkipReason(c.cand) == "" {
			t.Errorf("%+v: no skip reason", c.cand)
		}
	}
}

func TestBusMemberNets(t *testing.T) {
	nets := []string{"A_0", "A_1", "A_2", "D0", "D1", "D2", "D3", "SPI1_SCK", "SPI1_MOSI", "SPI1_MISO", "GND"}
	cands := DetectBusCandidates(nets)
	if m, ok := BusMemberNets("D[0:3]", cands, nets); !ok || strings.Join(m, ",") != "D0,D1,D2,D3" {
		t.Fatalf("D[0:3] → %v %v", m, ok)
	}
	if m, ok := BusMemberNets("A[0:2]", cands, nets); !ok || strings.Join(m, ",") != "A_0,A_1,A_2" {
		t.Fatalf("A[0:2] → %v", m)
	}
	// Protocol groups have no native bus name any more (host needs NAME[a:b]).
	if m, ok := BusMemberNets("SPI1", cands, nets); ok {
		t.Fatalf("SPI1 must not map to a native bus: %v", m)
	}
	if m, ok := BusMemberNets("D[0..5]", cands, nets); !ok || len(m) != 6 || m[5] != "D5" {
		t.Fatalf("D[0..5] → %v", m)
	}
	if _, ok := BusMemberNets("MISC", cands, nets); ok {
		t.Fatal("an unrelated name must not map to members")
	}
}

func busCheckSnap() *Snapshot {
	s := &Snapshot{Source: "layout", HasMarkers: true, HasPins: true, HasWires: true, HasBuses: true}
	p := Part{Ref: "U1", Box: Box{-30, -50, 30, 50}, HasBox: true}
	for i, n := range []string{"D0", "D1", "D2", "D3"} {
		y := 30 - 20*float64(i)
		p.Pins = append(p.Pins, Pin{Number: n, Net: n, X: 40, Y: y})
		s.Wires = append(s.Wires, Wire{Net: n, Pts: []Pt{{40, y}, {70, y}}, Implicit: true})
		s.Markers = append(s.Markers, Marker{Kind: KindNetPort, Net: n, Anchor: Pt{70, y}, Dir: "right", Box: predictMarkerBox(KindNetPort, n, Pt{70, y}, "right")})
	}
	s.Parts = []Part{p}
	s.Buses = []Bus{{ID: "b1", Name: "D[0:3]", Pts: [][]Pt{{{145, -30}, {145, 30}}}}}
	return s
}

func TestCheckBusesMembers(t *testing.T) {
	s := busCheckSnap()
	c := CheckBuses(s)
	if len(c) != 1 || !c[0].OK || len(c[0].Findings) != 0 {
		t.Fatalf("clean bus %+v", c)
	}
	if r := Analyze(s, nil); r.Lanes[0].NativeBus != "D[0:3]" || len(r.BusChecks) != 1 {
		t.Fatalf("N3 credit / report %+v", r.Lanes)
	}
	// A member only "connected" by the bus: no label of its own.
	s.Markers = s.Markers[1:]
	c = CheckBuses(s)
	if c[0].OK || c[0].Findings[0].Rule != "member-without-label" || c[0].Findings[0].Net != "D0" {
		t.Fatalf("label-less member %+v", c)
	}
	if r := Analyze(s, nil); r.Lanes[0].NativeBus != "" {
		t.Fatalf("N3 must not credit a bus standing in for a missing label: %+v", r.Lanes[0])
	}
	// A bus naming a member that is on no pin.
	s = busCheckSnap()
	s.Buses[0].Name = "D[0:4]"
	if c = CheckBuses(s); c[0].OK || c[0].Findings[0].Rule != "member-without-pin" {
		t.Fatalf("missing member %+v", c)
	}
	// A bus touching a wire: warned (the host may tap it), still not evidence.
	s = busCheckSnap()
	s.Buses[0].Pts = [][]Pt{{{60, -40}, {60, 40}}}
	c = CheckBuses(s)
	found := false
	for _, f := range c[0].Findings {
		found = found || f.Rule == "bus-touches-wire"
	}
	if !found || !c[0].OK {
		t.Fatalf("touching bus %+v", c)
	}
	// Unmapped name: warn.
	s = busCheckSnap()
	s.Buses[0].Name = "MISC"
	if c = CheckBuses(s); c[0].Findings[0].Rule != "bus-name-unmapped" {
		t.Fatalf("unmapped %+v", c)
	}
}
