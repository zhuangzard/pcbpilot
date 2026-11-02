package postsim

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// The shape of a real KiCad run (B10): the snapshot names each footprint by
// its value (name = device = value, "source": "kicad"), sim power records
// both the schematic MPN and the Value. A holder whose MPN is not its value
// (3034-style holder, CR2032 value) and ESD diodes valued "ESD" with a TVS
// MPN must not be reported as differing; a changed value still is.
func TestSimBoardKiCadSnapshotShape(t *testing.T) {
	board := []byte(`{"source":"kicad","copperLayers":2,"components":[
	 {"primitiveId":"u-bt","uuid":"u-bt","designator":"BT1","name":"CR2032","device":"CR2032","value":"CR2032",
	  "footprint":"Battery:Holder","layer":1,"side":"top","x":0,"y":0,"rotation":0,"bbox":{"minX":0,"minY":0,"maxX":10,"maxY":10},
	  "pads":[{"padNumber":"1","net":"VBAT","layer":1,"x":1,"y":1,"width":5,"height":5},{"padNumber":"2","net":"GND","layer":1,"x":8,"y":1,"width":5,"height":5}]},
	 {"primitiveId":"u-d1","uuid":"u-d1","designator":"D1","name":"ESD","device":"ESD","value":"ESD",
	  "footprint":"Diode:SOD","layer":1,"side":"top","x":20,"y":0,"rotation":0,"bbox":{"minX":20,"minY":0,"maxX":30,"maxY":10},
	  "pads":[{"padNumber":"1","net":"SIG","layer":1,"x":21,"y":1,"width":5,"height":5},{"padNumber":"2","net":"GND","layer":1,"x":28,"y":1,"width":5,"height":5}]},
	 {"primitiveId":"u-r1","uuid":"u-r1","designator":"R1","name":"4k7","device":"4k7","value":"4k7",
	  "footprint":"R:R_0402","layer":1,"side":"top","x":40,"y":0,"rotation":0,"bbox":{"minX":40,"minY":0,"maxX":50,"maxY":10},
	  "pads":[{"padNumber":"1","net":"SIG","layer":1,"x":41,"y":1,"width":5,"height":5},{"padNumber":"2","net":"GND","layer":1,"x":48,"y":1,"width":5,"height":5}]}]}`)
	b, err := ParseBoard(board)
	if err != nil {
		t.Fatal(err)
	}
	if !b.KiCad {
		t.Fatal(`"source": "kicad" not recognised`)
	}
	res := &powersim.Result{Scenario: "typical", Nets: map[string]*powersim.NetResult{}, Parts: map[string]*powersim.PartResult{
		"BT1": {Model: "load", MPN: "HOLDER-3034", Value: "CR2032"},
		"D1":  {Model: "esd", MPN: "TVS-1E10", Value: "ESD"},
		"R1":  {Model: "resistor", MPN: "RES-0402-4K7", Value: "4.7k"},
	}}
	bom := func() string {
		for _, f := range crossCheck(b, []*powersim.Result{res}) {
			if f.Kind == "sim-board-bom" || f.Kind == "sim-board-mismatch" {
				return f.Message
			}
		}
		return ""
	}
	if m := bom(); m != "" {
		t.Fatalf("MPN compared with the KiCad value: %s", m)
	}
	res.Parts["R1"].Value = "47k"
	if m := bom(); !strings.Contains(m, "R1: sim 47k, board 4k7") {
		t.Fatalf("value change not reported: %q", m)
	}
}
