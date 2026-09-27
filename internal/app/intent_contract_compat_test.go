package app

import (
	"os"
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
