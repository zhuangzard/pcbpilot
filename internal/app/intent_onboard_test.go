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
	// The same, reached through the other references: the rule net under
	// its display name, its exact identity in a net class / domain / block
	// / diff pair, or an ambiguous name in a block.
	vcc := map[string]*intentNet{"VCC_PIC": {WidthMil: intentWidth{Outer: 20}}}
	for name, in := range map[string]*designIntent{
		"netclass identity": {Nets: vcc, NetClasses: []intentNetClass{{Name: "N", Nets: []string{"/pic_sockets/VCC_PIC"}}}},
		"domain identity":   {Nets: vcc, Domains: []intentDomain{{ID: "D", Nets: []string{"/pic_sockets/VCC_PIC"}}}},
		"block identity":    {Nets: vcc, Blocks: []intentBlock{{ID: "B", Nets: []string{"/pic_sockets/VCC_PIC"}}}},
		"diff pair alias":   {Nets: map[string]*intentNet{"/VPP{slash}MCLR": {DiffPair: "VPP/MCLR"}}},
		"block ambiguous":   {Nets: vcc, Blocks: []intentBlock{{ID: "B", Nets: []string{"SDA"}}}},
	} {
		if _, _, err := in.onBoard(hierBoardNets); err == nil {
			t.Errorf("%s: want an error, nothing rewritten", name)
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

// Legacy connectivity (no fullName) with two distinct schematic nets "VCC"
// and "/pwr/VCC" on a board that has them as one net /pwr/VCC: resolving
// each name on its own would merge them and hide the short. Continuity
// fails and names it.
func TestSignoffContinuityHiddenShortLegacyNames(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	board := filepath.Join(dir, "board.json")
	if err := os.WriteFile(board, []byte(strings.ReplaceAll(string(raw), `"VCC"`, `"/pwr/VCC"`)), 0o644); err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{"GND":{"role":"ground"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"schemaVersion": "1.4",
		"components": []any{
			map[string]any{"id": "r1", "ref": "R1", "device": map[string]any{"deviceUuid": "x"}, "pins": []any{map[string]any{"number": "1"}, map[string]any{"number": "2"}}},
			map[string]any{"id": "j1", "ref": "J1", "device": map[string]any{"deviceUuid": "y"}, "pins": []any{map[string]any{"number": "1"}, map[string]any{"number": "2"}}},
		},
		"nets": []any{map[string]any{"id": "n1", "name": "VCC"}, map[string]any{"id": "n4", "name": "/pwr/VCC"},
			map[string]any{"id": "n2", "name": "SIG_A"}, map[string]any{"id": "n3", "name": "GND"}},
		"connections": []any{
			map[string]any{"componentId": "r1", "pinNumber": "1", "netId": "n1"},
			map[string]any{"componentId": "r1", "pinNumber": "2", "netId": "n2"},
			map[string]any{"componentId": "j1", "pinNumber": "1", "netId": "n4"},
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
		if g.Gate == "signoff-continuity" && (g.Pass || len(g.Items) == 0 || !strings.Contains(g.Items[0], "/pwr/VCC, VCC all resolve to board net /pwr/VCC")) {
			t.Fatalf("continuity %+v: want the hidden short reported", g)
		}
	}
}

// Same-name schematic nets: two "VCC" records on one document are two nets
// (shorted on the board → continuity fails); one "VCC" on each of two
// documents is one net merged by name (sim power's page rule) → passes.
func TestSignoffContinuitySameNameNets(t *testing.T) {
	dir := t.TempDir()
	board := filepath.Join("..", "kicad", "testdata", "snapshot.json")
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{"GND":{"role":"ground"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	comp := func(id, ref string) map[string]any {
		return map[string]any{"id": id, "ref": ref, "device": map[string]any{"deviceUuid": "x"}, "pins": []any{map[string]any{"number": "1"}, map[string]any{"number": "2"}}}
	}
	conn := func(c, pin, net string) map[string]any {
		return map[string]any{"componentId": c, "pinNumber": pin, "netId": net}
	}
	net := func(id, name string) map[string]any { return map[string]any{"id": id, "name": name} }
	write := func(name string, doc map[string]any) string {
		doc["schemaVersion"] = "1.4"
		b, _ := json.Marshal(doc)
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	continuity := func(conns []string) gateResult {
		var stderr bytes.Buffer
		res, err := runSignoff(signoffOpts{board: board, intent: intent, connectivity: conns, outDir: filepath.Join(dir, "so")}, nil, &stderr)
		if err != nil {
			t.Fatal(err)
		}
		for _, g := range res.Gates {
			if g.Gate == "signoff-continuity" {
				return g
			}
		}
		t.Fatal("no continuity gate")
		return gateResult{}
	}
	one := write("one.json", map[string]any{
		"components":  []any{comp("r1", "R1"), comp("j1", "J1")},
		"nets":        []any{net("n1", "VCC"), net("n4", "VCC"), net("n2", "SIG_A"), net("n3", "GND")},
		"connections": []any{conn("r1", "1", "n1"), conn("r1", "2", "n2"), conn("j1", "1", "n4"), conn("j1", "2", "n3")}})
	if g := continuity([]string{one}); g.Pass || len(g.Items) == 0 || !strings.Contains(g.Items[0], "VCC (document 1 net n1), VCC (document 1 net n4)") {
		t.Errorf("two VCC records on one document, shorted on the board: %+v", g)
	}
	p1 := write("p1.json", map[string]any{
		"components":  []any{comp("r1", "R1")},
		"nets":        []any{net("a", "VCC"), net("n2", "SIG_A")},
		"connections": []any{conn("r1", "1", "a"), conn("r1", "2", "n2")}})
	p2 := write("p2.json", map[string]any{
		"components":  []any{comp("j1", "J1")},
		"nets":        []any{net("b", "VCC"), net("n3", "GND")},
		"connections": []any{conn("j1", "1", "b"), conn("j1", "2", "n3")}})
	if g := continuity([]string{p1, p2}); !g.Pass {
		t.Errorf("VCC on two documents is one net: %+v", g)
	}
}
