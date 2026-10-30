package powersim

import (
	"strings"
	"testing"
)

// A DNP part (KiCad "dnp" property → IR "dnp": true) is not fitted: a field board's
// R105 (0R to GND, DNP) grounded the oscillator enable in the simulation and
// a reviewer reported the FPGA clock disabled.
func TestBuildDesignSkipsDNP(t *testing.T) {
	doc, err := ParseConnectivity([]byte(`{"components":[
	  {"id":"cmp-R104","ref":"R104","device":{"name":"10k"},"pins":[{"number":"1","name":"1"},{"number":"2","name":"2"}]},
	  {"id":"cmp-R105","ref":"R105","dnp":true,"device":{"name":"0R"},"pins":[{"number":"1","name":"1"},{"number":"2","name":"2"}]},
	  {"id":"cmp-R106","ref":"R106","device":{"name":"1k"},"pins":[{"number":"1","name":"1"},{"number":"2","name":"2"}]}],
	 "nets":[{"id":"n1","name":"+3V3"},{"id":"n2","name":"OSC_EN"},{"id":"n3","name":"GND"}],
	 "connections":[{"componentId":"cmp-R104","pinNumber":"1","netId":"n1"},{"componentId":"cmp-R104","pinNumber":"2","netId":"n2"},
	  {"componentId":"cmp-R105","pinNumber":"1","netId":"n2"},{"componentId":"cmp-R105","pinNumber":"2","netId":"n3"},
	  {"componentId":"cmp-R106","pinNumber":"1","netId":"n2"},{"componentId":"cmp-R106","pinNumber":"2","netId":"n3"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := BuildDesign([]*ConnDoc{doc}, map[string]PartValues{"R106": {Value: "1k", DNP: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range d.Parts {
		if p.Ref == "R105" || p.Ref == "R106" {
			t.Fatalf("DNP part %s simulated", p.Ref)
		}
	}
	if len(d.Parts) != 1 || !strings.Contains(strings.Join(d.Sources, ";"), "R105, R106") {
		t.Fatalf("parts %d, sources %v", len(d.Parts), d.Sources)
	}
}
