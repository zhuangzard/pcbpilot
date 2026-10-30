package pcbauto

import (
	"os"
	"testing"
)

// kicad route pours a board-outline zone per net/layer; on an insulated
// board it must stay inside its own domain (a field board: the patient GND pour
// covered the SELV strip, 1030 isolation errors).
func TestClipPoursToIso(t *testing.T) {
	in := loadIntent(t, "iso-mains-selv.intent.json")
	raw, err := os.ReadFile("testdata/iso-violation.dump.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	selv := ""
	for net, d := range an.Iso.NetDomain {
		if d != "" && an.Iso.NetDomain["GND"] == d {
			selv = net
		}
	}
	if selv == "" {
		t.Fatal("setup: GND has no domain")
	}
	out, notes, err := ClipPoursToIso(raw, in, []PlaneRegion{{Net: "GND", Layer: LayerTop}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Polys) == 0 || len(notes) == 0 {
		t.Fatalf("pour not clipped: %+v notes %v", out, notes)
	}
	// No kept region may contain a pad of the other domain.
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			d := an.Iso.NetDomain[pd.Net]
			if d == "" || d == an.Iso.NetDomain["GND"] {
				continue
			}
			for _, poly := range out[0].Polys {
				if PolyContains(poly, pd.Box.C) {
					t.Fatalf("clipped GND pour still covers %s.%s (%s)", p.Ref, pd.Number, pd.Net)
				}
			}
		}
	}
}
