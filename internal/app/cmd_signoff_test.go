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

// A hierarchical KiCad board carries KiCad's full net names
// ("/pwr/VCC", "/io/SIG{slash}A" for the label "SIG/A" on sheet io); the
// schematic connectivity (kicad netlist) names them by display name with
// the identity in fullName, an older document by display name only, and
// the intent by display name. One identity rule (pkg/netname): continuity
// sees no renamed net and the trace finds every rule on the board.
func TestSignoffHierarchicalNetIdentity(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(raw), `"VCC"`, `"/pwr/VCC"`)
	s = strings.ReplaceAll(s, `"SIG_A"`, `"/io/SIG{slash}A"`)
	board := filepath.Join(dir, "board.json")
	if err := os.WriteFile(board, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{
		"VCC":{"role":"power","currentA":0.5,"widthMil":{"outer":10,"inner":10,"min":8}},
		"SIG/A":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6}},
		"GND":{"role":"ground","widthMil":{"outer":10,"inner":10,"min":8}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, withFull := range []bool{true, false} {
		net := func(id, name, full string) map[string]any {
			m := map[string]any{"id": id, "name": name}
			if withFull {
				m["fullName"] = full
			}
			return m
		}
		doc := map[string]any{"schemaVersion": "1.4",
			"components": []any{
				map[string]any{"id": "r1", "ref": "R1", "device": map[string]any{"deviceUuid": "x"}, "pins": []any{map[string]any{"number": "1"}, map[string]any{"number": "2"}}},
				map[string]any{"id": "j1", "ref": "J1", "device": map[string]any{"deviceUuid": "y"}, "pins": []any{map[string]any{"number": "1"}, map[string]any{"number": "2"}}},
			},
			"nets": []any{net("n1", "VCC", "/pwr/VCC"), net("n2", "SIG/A", "/io/SIG{slash}A"), net("n3", "GND", "GND")},
			"connections": []any{
				map[string]any{"componentId": "r1", "pinNumber": "1", "netId": "n1"},
				map[string]any{"componentId": "r1", "pinNumber": "2", "netId": "n2"},
				map[string]any{"componentId": "j1", "pinNumber": "1", "netId": "n1"},
				map[string]any{"componentId": "j1", "pinNumber": "2", "netId": "n3"},
			}}
		b, _ := json.Marshal(doc)
		conn := filepath.Join(dir, "conn.json")
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
		if c := got["signoff-continuity"]; !c.Pass || len(c.Info) != 0 {
			t.Errorf("fullName %v: continuity %+v (no net may read as renamed)", withFull, c)
		}
		if tr := got["signoff-trace"]; !tr.Pass {
			t.Errorf("fullName %v: trace %+v — false \"not on the board\"", withFull, tr)
		}
		for _, row := range res.Traceability {
			want := map[string]string{"VCC": "/pwr/VCC", "SIG/A": "/io/SIG{slash}A", "GND": ""}[row.Net]
			if row.Identity != want || row.BoardPads == 0 || row.SchematicPins == 0 {
				t.Errorf("fullName %v: row %+v, want identity %q on the board and in the schematic", withFull, row, want)
			}
		}
	}
}

// An unrouted board has nothing below the width or via floor, which is no
// evidence: signoff-copper says it cannot check and fails.
func TestSignoffCopperUnroutedCannotCheck(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var snap map[string]any
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	cu := snap["copper"].(map[string]any)
	for _, k := range []string{"lines", "arcs", "vias", "pours", "poured", "fills"} {
		cu[k] = []any{}
	}
	board := filepath.Join(dir, "unrouted.json")
	b, _ := json.Marshal(snap)
	if err := os.WriteFile(board, b, 0o644); err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{"VCC":{"role":"power","currentA":0.5,"widthMil":{"outer":10,"inner":10,"min":8}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	copperGate := func(board string) gateResult {
		t.Helper()
		var stderr bytes.Buffer
		res, err := runSignoff(signoffOpts{board: board, intent: intent, outDir: filepath.Join(dir, "so")}, nil, &stderr)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range res.Gates {
			if g.Gate == "signoff-copper" {
				return g
			}
		}
		t.Fatal("no signoff-copper gate")
		return gateResult{}
	}
	g := copperGate(board)
	if g.Pass || len(g.Items) == 0 || !strings.HasPrefix(g.Items[0], "cannot check: the board has no copper") || !strings.HasPrefix(g.Detail, "cannot check") {
		t.Fatalf("unrouted board: %+v", g)
	}
	// The routed fixture is judged on its copper, not called uncheckable.
	if g := copperGate(filepath.Join("..", "kicad", "testdata", "snapshot.json")); strings.Contains(g.Detail, "cannot check") {
		t.Fatalf("routed board: %+v", g)
	}
}
