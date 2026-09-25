package app

import (
	"errors"
	"fmt"
	"testing"
)

func TestSchematicConflictFingerprintTakesTheLastNamedConflict(t *testing.T) {
	cases := map[string]string{
		"candidate search budget exhausted: last naming conflict: net PA7 has no safe naming lead for island at pin 2 (-40,30)": "naming:PA7:2",
		"annealing placer: cannot route direct net USB_DP between physical islands USB_DP:4@-170,-65":                         "route:USB_DP",
		"local search failed: component R_EN (R5) placement search candidate-budget after 21 candidates":                      "place:R_EN",
		"component C1 (C1) placement search ...: last observed terminal conflict: net GND has no safe naming lead for island at pin 9 (1,2)": "naming:GND:9",
		"candidate search budget exhausted": "",
	}
	for msg, want := range cases {
		if got := schematicConflictFingerprint(errors.New(msg)); got != want {
			t.Fatalf("%q -> %q want %q", msg, got, want)
		}
	}
	if schematicConflictFingerprint(fmt.Errorf("zone: %w", errors.New("net X has no safe naming lead for island at pin 1 (0,0)"))) != "naming:X:1" {
		t.Fatal("wrapped errors must be fingerprinted")
	}
}
