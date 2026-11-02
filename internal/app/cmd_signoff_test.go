package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
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

// A net with nothing routed has nothing below the width or via floor, which
// is no evidence: signoff-copper names every multi-part net without copper
// of its own and fails, whatever copper other nets have (a GND pour, a
// stray track); a fully routed board is judged on its copper.
func TestSignoffCopperUnroutedCannotCheck(t *testing.T) {
	dir := t.TempDir()
	raw, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	intent := filepath.Join(dir, "intent.json")
	if err := os.WriteFile(intent, []byte(`{"nets":{"VCC":{"role":"power","currentA":0.5,"widthMil":{"outer":10,"inner":10,"min":8}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// edit builds a variant of the routed fixture (R1: VCC/SIG_A, J1:
	// VCC/GND; VCC tracks + via, a SIG_A track, a GND pour).
	edit := func(name string, f func(snap map[string]any, cu map[string]any)) string {
		t.Helper()
		var snap map[string]any
		if err := json.Unmarshal(raw, &snap); err != nil {
			t.Fatal(err)
		}
		f(snap, snap["copper"].(map[string]any))
		path := filepath.Join(dir, name+".json")
		b, _ := json.Marshal(snap)
		if err := os.WriteFile(path, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	keep := func(list any, net string) []any {
		var out []any
		for _, it := range list.([]any) {
			if it.(map[string]any)["net"] == net {
				out = append(out, it)
			}
		}
		return out
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
	unrouted := func(g gateResult) []string {
		var nets []string
		for _, it := range g.Items {
			if n, ok := strings.CutPrefix(it, "cannot check: "); ok {
				nets = append(nets, strings.SplitN(n, ":", 2)[0])
			}
		}
		return nets
	}
	// addPart appends a footprint (a copy of the first one) with the pads
	// given as (number, net).
	addPart := func(snap map[string]any, ref string, pads [][2]string) {
		comps := snap["components"].([]any)
		u := map[string]any{}
		for k, v := range comps[0].(map[string]any) {
			u[k] = v
		}
		u["designator"], u["primitiveId"] = ref, "fp-"+ref
		var ps []any
		for i, p := range pads {
			ps = append(ps, map[string]any{"padNumber": p[0], "net": p[1], "layer": 1, "x": 900.0 + 50*float64(i), "y": -600.0, "width": 40.0, "height": 40.0})
		}
		u["pads"] = ps
		snap["components"] = append(comps, u)
	}
	cases := []struct {
		name string
		f    func(snap, cu map[string]any)
		want string // unrouted nets, space-separated
	}{
		{"bare", func(_, cu map[string]any) {
			for _, k := range []string{"lines", "arcs", "vias", "pours", "poured", "fills"} {
				cu[k] = []any{}
			}
		}, "VCC"},
		// The GND pour and a SIG_A track stay; VCC loses its copper.
		{"unrelated-copper", func(_, cu map[string]any) {
			cu["lines"], cu["vias"] = keep(cu["lines"], "SIG_A"), []any{}
		}, "VCC"},
		// A third part makes SIG_A and GND multi-part nets: SIG_A has its
		// track, GND loses its pour, VCC stays routed.
		{"partial", func(snap, cu map[string]any) {
			comps := snap["components"].([]any)
			u := map[string]any{}
			for k, v := range comps[0].(map[string]any) {
				u[k] = v
			}
			u["designator"], u["primitiveId"] = "U9", "fp-u9"
			u["pads"] = []any{
				map[string]any{"padNumber": "1", "net": "SIG_A", "layer": 1, "x": 900.0, "y": -300.0, "width": 40.0, "height": 40.0},
				map[string]any{"padNumber": "2", "net": "GND", "layer": 1, "x": 950.0, "y": -300.0, "width": 40.0, "height": 40.0},
			}
			snap["components"] = append(comps, u)
			cu["poured"], cu["pours"] = []any{}, []any{}
		}, "GND"},
		// One part, two supply pads with different numbers, no copper on
		// that net: judged (an IC's VCC pins still need copper).
		{"one-part-two-pads", func(snap, cu map[string]any) {
			addPart(snap, "U8", [][2]string{{"1", "VDD_IC"}, {"4", "VDD_IC"}})
		}, "VDD_IC"},
		// Two footprints that share the designator R5 are still two parts:
		// pad 1 of each on an unrouted net is judged.
		{"duplicate-designator", func(snap, cu map[string]any) {
			addPart(snap, "R5", [][2]string{{"1", "DUP_NET"}, {"2", "GND"}})
			addPart(snap, "R5", [][2]string{{"1", "DUP_NET"}, {"2", "GND"}})
			cu["poured"], cu["pours"] = []any{}, []any{}
		}, "DUP_NET GND"},
		// Same pad number twice in one footprint: KiCad connects them
		// internally, one pad, nothing to route.
		{"one-part-same-number", func(snap, cu map[string]any) {
			addPart(snap, "U8", [][2]string{{"5", "VDD_IC"}, {"5", "VDD_IC"}})
		}, ""},
	}
	for _, c := range cases {
		if c.want == "" {
			if g := copperGate(edit(c.name, c.f)); strings.Contains(g.Detail, "cannot check") {
				t.Errorf("%s: judged: %+v", c.name, g)
			}
			continue
		}
		g := copperGate(edit(c.name, c.f))
		if got := strings.Join(unrouted(g), " "); g.Pass || got != c.want || !strings.HasPrefix(g.Detail, "cannot check") {
			t.Errorf("%s: unrouted %q, want %q: %+v", c.name, got, c.want, g)
		}
	}
	// The routed fixture is judged on its copper, not called uncheckable.
	if g := copperGate(filepath.Join("..", "kicad", "testdata", "snapshot.json")); strings.Contains(g.Detail, "cannot check") {
		t.Fatalf("routed board: %+v", g)
	}
}

// kicad lcsc --check names why footprints are not assembled, so its count
// can be read against signoff-parts (which applies no exclusions).
func TestLcscAssemblySummary(t *testing.T) {
	got := lcscAssemblySummary([]kicad.Part{{Ref: "R1"}, {Ref: "C9", DNP: true}, {Ref: "H1", ExcludeBOM: true, ExcludePos: true}, {Ref: "TP1", ExcludePos: true}})
	if got != "4 footprints, 1 assembled (3 not assembled: 1 DNP, 1 excluded from BOM, 1 excluded from the position file)" {
		t.Fatalf("%q", got)
	}
	if got := lcscAssemblySummary([]kicad.Part{{Ref: "R1"}}); got != "1 footprints, 1 assembled" {
		t.Fatalf("%q", got)
	}
}
