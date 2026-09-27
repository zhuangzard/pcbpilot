package app

import (
	"os"
	"strings"
	"testing"
)

// The producer (pkg/intent, `pcbpilot intent derive`) and this consumer
// (`pcb rules apply`, `sch intent-annotate`) were built against one contract;
// the fixture is the producer's real output for the ESP32 mini so a field
// rename on either side fails here. Regenerate with:
//
//	pcbpilot intent derive --connectivity pkg/powersim/testdata/esp32mini/sch-*.json \
//	  --values pkg/powersim/testdata/esp32mini/values.json --out <this file>
func TestDerivedIntentParsesForRulesPush(t *testing.T) {
	raw, err := os.ReadFile("testdata/intent/esp32-mini.derived.intent.json")
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseDesignIntent(raw)
	if err != nil {
		t.Fatalf("derived intent rejected by the push consumer: %v", err)
	}
	if len(in.NetClasses) < 4 || len(in.Nets) < 10 {
		t.Fatalf("derived intent parsed thin: %d classes, %d nets", len(in.NetClasses), len(in.Nets))
	}
}

// Planning (not only parsing) must accept intent derive's partner-name
// diffPair convention: live 2026-09-27 the USB pair came out as two
// one-net "pairs" and rules apply refused with a conflict.
func TestDerivedIntentDiffPairPartnerConvention(t *testing.T) {
	raw, err := os.ReadFile("testdata/intent/esp32-mini.derived.intent.json")
	if err != nil {
		t.Fatal(err)
	}
	in, err := parseDesignIntent(raw)
	if err != nil {
		t.Fatal(err)
	}
	if in.Nets["USB_DM"].DiffPair != "USB_DP" {
		t.Skip("fixture no longer uses the partner convention")
	}
	p := &intentRulesPlan{}
	p.planDiffPairs(in, map[string]any{}, nil, func(string) bool { return true }, nil)
	for _, c := range p.Conflicts {
		if strings.HasPrefix(c.Item, "diffPair") {
			t.Fatalf("partner-convention pair rejected: %+v", c)
		}
	}
	if diffPairKey("USB_DM", "USB_DP") != "USB_D" {
		t.Fatal("pair key")
	}
}
