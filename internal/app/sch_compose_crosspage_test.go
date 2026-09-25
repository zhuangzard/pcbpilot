package app

import (
	"strings"
	"testing"
)

// F2 (2026-09-25 E2E): page 1 of a multi-page compose stopped at the strict gate
// (its cross-page ports had no partner yet) and the protected save never ran.

func TestComposeSavesBeforeStrictGate(t *testing.T) {
	for _, matching := range []bool{false, true} {
		p, before := composeApplyFixture(t, matching)
		pb, err := schCompositionPlaybook(p, composeApplyBytes(t, before), !matching)
		if err != nil {
			t.Fatal(err)
		}
		checkIndex, _ := composeStep(t, pb, "wire-tree-check")
		saveIndex, _ := composeStep(t, pb, "save-composition")
		gateIndex, gate := composeStep(t, pb, "strict-schematic-gate")
		if !(checkIndex < saveIndex && saveIndex < gateIndex) {
			t.Fatalf("matching=%v: want wire-tree-check < save < gate, got %d/%d/%d", matching, checkIndex, saveIndex, gateIndex)
		}
		if _, ok := gate.Flags["defer-cross-page-drc"]; ok {
			t.Fatal("single-page (no net port) queue must keep the plain strict gate")
		}
	}
}

func TestComposeDefersDrcOnlyWithCrossPagePorts(t *testing.T) {
	p, before := composeApplyFixture(t, false)
	p.Layout.Flags[0].Kind = "net_port_bi"
	pb, err := schCompositionPlaybook(p, composeApplyBytes(t, before), true)
	if err != nil {
		t.Fatal(err)
	}
	_, gate := composeStep(t, pb, "strict-schematic-gate")
	if gate.Flags["defer-cross-page-drc"] != true || gate.Flags["strict"] != true {
		t.Fatalf("page with net ports must gate with deferral: %+v", gate.Flags)
	}
}

func TestPlanDrcCrossPageDeferral(t *testing.T) {
	port := func(net, page string) any {
		return map[string]any{"componentType": "netport", "net": net, "pageUuid": page}
	}
	raw := []any{
		port("EN", "P1"), port("IO0", "P1"), port("IO0", "P1"), port("ESP_TXD", "P1"),
		port("ESP_TXD", "P2"), // partner exists
		map[string]any{"componentType": "netflag", "net": "GND", "pageUuid": "P1"},
		map[string]any{"componentType": "netport", "net": "LOST"}, // no page tag: ignored
	}
	d := planDrcCrossPageDeferral(raw, "P1")
	if strings.Join(d.Unmatched, ",") != "EN,IO0" || d.Ports != 3 {
		t.Fatalf("got %+v", d)
	}
	if d := planDrcCrossPageDeferral(raw, ""); d.Ports != 0 {
		t.Fatal("unknown active page must not defer anything")
	}
}

func TestDeferDrcForCrossPagePorts(t *testing.T) {
	d := drcCrossPageDeferral{Unmatched: []string{"EN", "IO0", "ESP_RXD", "ESP_TXD"}, Ports: 4}
	warn := func(w int) drcReport {
		return drcReport{Passed: false, Strict: true, Summary: &drcSummary{Warn: w, Total: w}}
	}
	if _, ok := deferDrcForCrossPagePorts(warn(4), true, d); !ok {
		t.Fatal("E2E case: 4 warns, 4 unmatched ports must defer")
	}
	if _, ok := deferDrcForCrossPagePorts(warn(5), true, d); ok {
		t.Fatal("more warns than unmatched ports: a real problem hides in there")
	}
	if _, ok := deferDrcForCrossPagePorts(warn(2), false, d); ok {
		t.Fatal("non-strict gate never blocks on warns; nothing to defer")
	}
	errRep := warn(1)
	errRep.Summary.Error = 1
	if _, ok := deferDrcForCrossPagePorts(errRep, true, d); ok {
		t.Fatal("error-level DRC must never be deferred")
	}
	if _, ok := deferDrcForCrossPagePorts(warn(1), true, drcCrossPageDeferral{}); ok {
		t.Fatal("all partners present: nothing may be deferred")
	}
	if _, ok := deferDrcForCrossPagePorts(drcReport{Strict: true}, true, d); ok {
		t.Fatal("no counts: cannot bound the warns, must not defer")
	}
}
