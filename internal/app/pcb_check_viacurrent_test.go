package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Offline `pcb check --board dump --intent`: one 0.3 mm via carrying the
// intent's 3 A is a via-current ERROR with the numbers; the sized 5-via
// array passes.
func TestPcbCheckViaCurrentOffline(t *testing.T) {
	dir := t.TempDir()
	intentPath := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intentPath, []byte(`{"nets":{"VOUT":{"role":"power","currentA":3,"widthMil":{"outer":55}}},
		"copper":{"tempRiseC":10,"outerOz":1,"innerOz":0.5,"viaDrillMil":12,"viaDiaMil":24}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	dump := func(vias []map[string]any) string {
		d := map[string]any{"copperLayers": 2, "components": []any{},
			"rules": map[string]any{"clearanceMil": 6, "trackWidthMil": 10, "trackWidthMinMil": 5, "viaDrillMil": 12, "viaDiameterMil": 24, "copperToEdgeMil": 10},
			"copper": map[string]any{"lines": []any{
				map[string]any{"net": "VOUT", "layer": 1, "lineWidth": 55, "startX": 0, "startY": 0, "endX": 500, "endY": 0, "primitiveId": "t1"},
				map[string]any{"net": "VOUT", "layer": 2, "lineWidth": 55, "startX": 500, "startY": 0, "endX": 1000, "endY": 0, "primitiveId": "t2"},
			}, "vias": vias}}
		raw, _ := json.Marshal(d)
		p := filepath.Join(dir, "board.json")
		if err := os.WriteFile(p, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	run := func(board string) (pcbCheckReport, error) {
		var out, errb bytes.Buffer
		err := runPcbCheckIntent(nil, "", 3, nil, intentPath, board, true, true, &out, &errb)
		var rep pcbCheckReport
		if jerr := json.Unmarshal(out.Bytes(), &rep); jerr != nil {
			t.Fatalf("%v: %s %s", jerr, out.String(), errb.String())
		}
		return rep, err
	}
	rep, err := run(dump([]map[string]any{{"net": "VOUT", "x": 500, "y": 0, "holeDiameter": 11.81, "diameter": 23.62, "primitiveId": "v1"}}))
	if err == nil || rep.Summary.ViaCurr != 1 || rep.Summary.Errors != 1 {
		t.Fatalf("single 0.3 mm via at 3 A: err=%v summary=%+v", err, rep.Summary)
	}
	f := rep.Findings[0]
	if f.Type != "via-current" || f.Level != "ERROR" || !strings.Contains(f.Message, "carry 0.73 A") || !strings.Contains(f.Message, "for 3.00 A") || len(f.Primitives) != 1 {
		t.Fatalf("finding %+v", f)
	}
	var arr []map[string]any
	for i := 0; i < 5; i++ {
		arr = append(arr, map[string]any{"net": "VOUT", "x": 440 + 30*i, "y": 0, "holeDiameter": 12, "diameter": 24})
	}
	if rep, err := run(dump(arr)); err != nil || rep.Summary.ViaCurr != 0 {
		t.Fatalf("5 × 12/24 array: err=%v %+v", err, rep.Findings)
	}
}
