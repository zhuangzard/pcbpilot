package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// `pcb check --intent --board` runs the isolation rule offline and gates
// --strict on a deliberately violating board.
func TestPcbCheckIntentOffline(t *testing.T) {
	const td = "../../pkg/pcbauto/testdata/"
	var out, errb bytes.Buffer
	err := runPcbCheckIntent(nil, "", 3, nil, td+"iso-mains-selv.intent.json", td+"iso-violation.dump.json", "", true, true, &out, &errb)
	if err == nil || !strings.Contains(err.Error(), "--strict") {
		t.Fatalf("strict gate: err=%v", err)
	}
	var rep pcbCheckReport
	if jerr := json.Unmarshal(out.Bytes(), &rep); jerr != nil {
		t.Fatal(jerr)
	}
	kinds := map[string]int{}
	for _, f := range rep.Findings {
		kinds[f.Type]++
	}
	if rep.Summary.Isolation == 0 || kinds["iso-clearance"] == 0 || kinds["iso-creepage"] == 0 || rep.Passed {
		t.Fatalf("summary %+v kinds %v", rep.Summary, kinds)
	}
	// Without --intent the offline board runs the copper-to-edge rule only.
	out.Reset()
	if err := runPcbCheckIntent(nil, "", 3, nil, "", td+"iso-violation.dump.json", "", false, true, &out, &errb); err != nil {
		t.Fatalf("--board without --intent: %v", err)
	}
	var edgeOnly pcbCheckReport
	if jerr := json.Unmarshal(out.Bytes(), &edgeOnly); jerr != nil {
		t.Fatal(jerr)
	}
	if edgeOnly.Edge == nil || edgeOnly.Summary.Isolation != 0 {
		t.Fatalf("edge-only offline run: %+v", edgeOnly.Summary)
	}
}
