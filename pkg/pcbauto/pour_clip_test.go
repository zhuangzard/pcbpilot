package pcbauto

import (
	"encoding/json"
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
	out, lost, notes, err := ClipPoursToIso(raw, in, []PlaneRegion{{Net: "GND", Layer: LayerTop}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || len(out[0].Polys) == 0 || len(notes) == 0 || len(lost) != 0 {
		t.Fatalf("pour not clipped: %+v lost %+v notes %v", out, lost, notes)
	}
	// The kept pour still covers its own domain: some of GND's own pads
	// lie inside the kept regions (coverage retained, not just non-empty).
	own := 0
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Net != "GND" {
				continue
			}
			for _, poly := range out[0].Polys {
				if PolyContains(poly, pd.Box.C) {
					own++
					break
				}
			}
		}
	}
	if own == 0 {
		t.Fatal("the clipped GND pour covers none of GND's own pads")
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

// A planned pour with no room in its domain is clipped to nothing: it is
// returned in lost, never silently dropped.
func TestClipPoursToIsoReportsLostPour(t *testing.T) {
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
	// A GND pour drawn only around a pad of the partner domain.
	var foreign *Pad
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if d := an.Iso.NetDomain[pd.Net]; d != "" && d != an.Iso.NetDomain["GND"] && foreign == nil {
				foreign = pd
			}
		}
	}
	if foreign == nil {
		t.Fatal("setup: no pad of another domain")
	}
	c := foreign.Box.C
	sq := Rect{c.X - 30, c.Y - 30, c.X + 30, c.Y + 30}.Corners()
	out, lost, _, err := ClipPoursToIso(raw, in, []PlaneRegion{{Net: "GND", Layer: LayerTop, Polys: [][]Point{sq}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 || len(lost) != 1 || lost[0].Net != "GND" || lost[0].Layer != LayerTop {
		t.Fatalf("pour inside the partner domain: kept %+v lost %+v", out, lost)
	}
}

// No usable board outline in the snapshot: a pour of an insulated net is
// still clipped (within the parts' extent), never handed back as the
// whole-board pour; a pour of a net without a pair is unchanged.
func TestClipPoursToIsoWithoutOutline(t *testing.T) {
	in := loadIntent(t, "iso-mains-selv.intent.json")
	raw, err := os.ReadFile("testdata/iso-violation.dump.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "outline")
	noOutline, _ := json.Marshal(m)
	b, err := FromSnapshot(noOutline)
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Outline) >= 3 {
		t.Fatal("setup: the outline is still there")
	}
	an := Analyze(b, PowerSpec{Intent: in}, nil)
	out, lost, notes, err := ClipPoursToIso(noOutline, in, []PlaneRegion{{Net: "GND", Layer: LayerTop}, {Net: "NOT_A_NET", Layer: LayerTop}})
	if err != nil {
		t.Fatal(err)
	}
	var gnd *PlaneRegion
	for i := range out {
		switch out[i].Net {
		case "GND":
			gnd = &out[i]
		case "NOT_A_NET":
			if len(out[i].Polys) != 0 {
				t.Errorf("a pour without a pair was changed: %+v", out[i])
			}
		}
	}
	if gnd == nil || len(gnd.Polys) == 0 || len(lost) != 0 {
		t.Fatalf("GND pour not clipped without an outline: kept %+v lost %+v notes %v", out, lost, notes)
	}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if d := an.Iso.NetDomain[pd.Net]; d == "" || d == an.Iso.NetDomain["GND"] {
				continue
			}
			for _, poly := range gnd.Polys {
				if PolyContains(poly, pd.Box.C) {
					t.Fatalf("GND pour without an outline still covers %s.%s (%s)", p.Ref, pd.Number, pd.Net)
				}
			}
		}
	}
}
