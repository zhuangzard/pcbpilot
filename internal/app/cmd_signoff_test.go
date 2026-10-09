package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignoffOffline(t *testing.T) {
	dir := t.TempDir()
	board := filepath.Join("..", "kicad", "testdata", "snapshot.json")
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{
		"VCC":{"role":"power","currentA":0.5,"widthMil":{"outer":10,"inner":10,"min":8}},
		"GND":{"role":"ground","widthMil":{"outer":10,"inner":10,"min":8}},
		"NOWHERE":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	conn := filepath.Join(dir, "conn.json")
	doc := map[string]any{"schemaVersion": "1.4",
		"components": []any{
			map[string]any{"id": "r1", "ref": "R1", "device": map[string]any{"deviceUuid": "x", "supplierId": "C25804"}, "pins": []any{map[string]any{"number": "1"}, map[string]any{"number": "2"}}},
			map[string]any{"id": "j1", "ref": "J1", "device": map[string]any{"deviceUuid": "y"}, "pins": []any{map[string]any{"number": "1"}, map[string]any{"number": "2"}}},
		},
		"nets": []any{map[string]any{"id": "n1", "name": "VCC"}, map[string]any{"id": "n2", "name": "SIG_A"}, map[string]any{"id": "n3", "name": "GND"}},
		"connections": []any{
			map[string]any{"componentId": "r1", "pinNumber": "1", "netId": "n1"},
			map[string]any{"componentId": "r1", "pinNumber": "2", "netId": "n2"},
			map[string]any{"componentId": "j1", "pinNumber": "1", "netId": "n1"},
			map[string]any{"componentId": "j1", "pinNumber": "2", "netId": "n3"},
		}}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(conn, b, 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	res, err := runSignoff(signoffOpts{board: board, intent: intent, connectivity: []string{conn}, outDir: filepath.Join(dir, "so")}, nil, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]gateResult{}
	for _, g := range res.Gates {
		got[g.Gate] = g
	}
	if res.Pass {
		t.Fatal("no review / sim / manual / report: the sign-off must fail")
	}
	for _, name := range []string{"signoff-review", "signoff-parts", "signoff-safety", "signoff-copper", "signoff-ir", "signoff-continuity", "signoff-trace", "signoff-artifacts"} {
		if _, ok := got[name]; !ok {
			t.Errorf("gate %s missing", name)
		}
	}
	// J1 has no LCSC; R1 (schematic C25804) is fine.
	if p := got["signoff-parts"]; p.Pass || len(p.Items) != 1 || !strings.HasPrefix(p.Items[0], "J1:") {
		t.Fatalf("parts: %+v", p)
	}
	if c := got["signoff-continuity"]; !c.Pass {
		t.Fatalf("continuity: %+v", c)
	}
	if tr := got["signoff-trace"]; tr.Pass || len(tr.Items) != 1 || !strings.Contains(tr.Items[0], "NOWHERE") {
		t.Fatalf("trace: %+v", tr)
	}
	if !got["signoff-safety"].Pass || got["signoff-review"].Pass || got["signoff-artifacts"].Pass {
		t.Fatalf("safety/review/artifacts: %+v %+v %+v", got["signoff-safety"], got["signoff-review"], got["signoff-artifacts"])
	}
	md, err := os.ReadFile(filepath.Join(dir, "so", "signoff.md"))
	if err != nil || !strings.Contains(string(md), "| VCC | power | 0.500 |") {
		t.Fatalf("traceability table: %v\n%s", err, md)
	}
}
