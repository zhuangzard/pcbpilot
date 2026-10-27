package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var hierBoardNets = []string{"/pic_sockets/VCC_PIC", "/VPP{slash}MCLR", "GND", "/a/SDA", "/b/SDA", "unconnected-(J1-Pad1)"}

// Intent nets named by display name resolve to the hierarchical board's
// identities everywhere the intent names a net; the loaded intent is not
// changed, and an absent name stays.
func TestIntentOnBoardResolvesDisplayNames(t *testing.T) {
	in := &designIntent{
		Nets: map[string]*intentNet{
			"VCC_PIC":  {Role: "power", DiffPair: "VPP/MCLR"},
			"VPP/MCLR": {Role: "signal"},
			"GND":      {Role: "ground"},
			"NOWHERE":  {Role: "signal"},
		},
		NetClasses: []intentNetClass{{Name: "PWR", Nets: []string{"VCC_PIC", "GND"}}},
		Domains:    []intentDomain{{ID: "LV", Nets: []string{"VCC_PIC", "VPP/MCLR"}}},
	}
	out, renamed, err := in.onBoard(hierBoardNets)
	if err != nil {
		t.Fatal(err)
	}
	if len(renamed) != 2 {
		t.Fatalf("renamed %v", renamed)
	}
	for _, n := range []string{"/pic_sockets/VCC_PIC", "/VPP{slash}MCLR", "GND", "NOWHERE"} {
		if out.Nets[n] == nil {
			t.Errorf("net %s missing: %v", n, out.Nets)
		}
	}
	if out.Nets["/pic_sockets/VCC_PIC"].DiffPair != "/VPP{slash}MCLR" {
		t.Errorf("diff pair not resolved: %+v", out.Nets["/pic_sockets/VCC_PIC"])
	}
	if got := out.NetClasses[0].Nets; got[0] != "/pic_sockets/VCC_PIC" || got[1] != "GND" {
		t.Errorf("netclass nets %v", got)
	}
	if got := out.Domains[0].Nets; got[1] != "/VPP{slash}MCLR" {
		t.Errorf("domain nets %v", got)
	}
	if in.Nets["VCC_PIC"] == nil || in.NetClasses[0].Nets[0] != "VCC_PIC" || in.Nets["VCC_PIC"].DiffPair != "VPP/MCLR" {
		t.Error("the receiver must not change")
	}
}

// Two intent rules that name one board net under two aliases (different
// widths), a rule onto an intent net already named by the identity, and a
// rule name that fits two board nets are errors — never merged in map
// order, never guessed.
func TestIntentOnBoardRejectsCollisionsAndAmbiguity(t *testing.T) {
	for name, nets := range map[string]map[string]*intentNet{
		"display + identity": {"VCC_PIC": {WidthMil: intentWidth{Outer: 20}}, "/pic_sockets/VCC_PIC": {WidthMil: intentWidth{Outer: 8}}},
		"two aliases":        {"VPP/MCLR": {WidthMil: intentWidth{Outer: 20}}, "/VPP/MCLR": {WidthMil: intentWidth{Outer: 8}}},
		"ambiguous":          {"SDA": {WidthMil: intentWidth{Outer: 6}}},
	} {
		_, _, err := (&designIntent{Nets: nets}).onBoard(hierBoardNets)
		if err == nil || !strings.Contains(err.Error(), "fix:") {
			t.Errorf("%s: want an error with a fix hint, got %v", name, err)
		}
	}
}

// signoff-trace: the schematic has SDA on two sheets, the board only one of
// them; intent "SDA" is ambiguous in the schematic and must fail, not fall
// back to the board's single /a/SDA.
func TestSignoffTraceAmbiguousIsNotUnknown(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	board := filepath.Join(dir, "board.json")
	if err := os.WriteFile(board, []byte(strings.ReplaceAll(string(raw), `"SIG_A"`, `"/a/SDA"`)), 0o644); err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{"SDA":{"role":"signal","widthMil":{"outer":6,"inner":6,"min":6}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"schemaVersion": "1.4",
		"components": []any{
			map[string]any{"id": "r1", "ref": "R1", "device": map[string]any{"deviceUuid": "x"}, "pins": []any{map[string]any{"number": "1"}, map[string]any{"number": "2"}}},
			map[string]any{"id": "j1", "ref": "J1", "device": map[string]any{"deviceUuid": "y"}, "pins": []any{map[string]any{"number": "1"}, map[string]any{"number": "2"}}},
		},
		"nets": []any{
			map[string]any{"id": "n1", "name": "VCC", "fullName": "VCC"},
			map[string]any{"id": "n2", "name": "/a/SDA", "fullName": "/a/SDA"},
			map[string]any{"id": "n3", "name": "/b/SDA", "fullName": "/b/SDA"},
		},
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
	for _, g := range res.Gates {
		if g.Gate == "signoff-trace" && (g.Pass || len(g.Items) != 1 || !strings.Contains(g.Items[0], "ambiguous in the schematic")) {
			t.Fatalf("trace %+v: want an ambiguity failure", g)
		}
	}
	if len(res.Traceability) != 1 || res.Traceability[0].Identity != "" || res.Traceability[0].BoardPads != 0 {
		t.Fatalf("row %+v: SDA must not be matched to the board's /a/SDA", res.Traceability)
	}
}
