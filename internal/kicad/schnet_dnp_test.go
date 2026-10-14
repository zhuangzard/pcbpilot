package kicad

import "testing"

func TestSchNetlistDNP(t *testing.T) {
	nl, err := ParseSchNetlist([]byte(`<export><components>
<comp ref="R104"><value>10k</value><libsource lib="L" part="R"/></comp>
<comp ref="R105"><value>0R</value><libsource lib="L" part="R"/><property name="dnp"/></comp>
</components><nets><net code="1" name="OSC_EN"><node ref="R104" pin="2"/><node ref="R105" pin="1"/></net></nets></export>`))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := nl.ToConnectivityDoc("p")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range doc.Components {
		got[c.Ref] = c.DNP
	}
	if got["R104"] || !got["R105"] {
		t.Fatalf("dnp = %v, want R105 only", got)
	}
}
