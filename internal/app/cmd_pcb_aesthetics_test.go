package app

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// `pcb aesthetics` runs offline on a routed snapshot and reports weight 0.
func TestPcbAestheticsCmdOffline(t *testing.T) {
	var out, errb bytes.Buffer
	root := newRootCmd(&out, &errb)
	root.SetArgs([]string{"pcb", "aesthetics", "--board", "../../pkg/pcbauto/testdata/esp32-v05-fixed.routed.json", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("%v: %s", err, errb.String())
	}
	var rep struct {
		Weight   float64               `json:"weight"`
		Score    float64               `json:"score"`
		Metrics  []struct{ ID string } `json:"metrics"`
		Symmetry []any                 `json:"symmetry"`
	}
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Weight != 0 || rep.Score <= 0 || len(rep.Metrics) != 18 || rep.Symmetry == nil {
		t.Fatalf("unexpected report: %+v", rep)
	}
	out.Reset()
	root = newRootCmd(&out, &errb)
	root.SetArgs([]string{"pcb", "aesthetics", "--board", "testdata/boards/lckfb-k230-canmv.json"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "report-only, weight 0.00") || !strings.Contains(s, "symmetry groups (P4)") {
		t.Fatalf("text report missing header/symmetry:\n%s", s)
	}
}
