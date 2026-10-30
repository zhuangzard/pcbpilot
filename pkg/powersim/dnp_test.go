package powersim

import (
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/population"
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
	// R106: the values file says dnp, the schematic flag does not — the flag
	// decides (pkg/population): simulated, with the warning.
	d, warns, err := BuildDesign([]*ConnDoc{doc}, map[string]PartValues{"R106": {Value: "1k", DNP: true}})
	if err != nil {
		t.Fatal(err)
	}
	refs := map[string]bool{}
	for _, p := range d.Parts {
		refs[p.Ref] = true
	}
	if refs["R105"] || !refs["R104"] || !refs["R106"] || len(d.Parts) != 2 {
		t.Fatalf("simulated %v", refs)
	}
	if !strings.Contains(strings.Join(d.Sources, ";"), "left out: R105") || strings.Contains(strings.Join(d.Sources, ";"), "R106") {
		t.Fatalf("sources %v", d.Sources)
	}
	if w := population.ValuesConflict("R106", true, false); !strings.Contains(strings.Join(warns, "\n"), w) {
		t.Fatalf("warnings %v lack %q", warns, w)
	}
	// A values "dnp" that agrees with the flag says nothing.
	_, warns, _ = BuildDesign([]*ConnDoc{doc}, map[string]PartValues{"R105": {DNP: true}})
	if strings.Contains(strings.Join(warns, "\n"), "values file") {
		t.Fatalf("agreeing values dnp warned: %v", warns)
	}
}
