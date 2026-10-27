package kicad

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// bridgeLiveTools: KiCad's python and kicad-cli, or a skip. Live tests run only
// with PCBPILOT_KICAD_LIVE=1.
func bridgeLiveTools(t *testing.T) *Tools {
	t.Helper()
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 (needs KiCad's python)")
	}
	tools, err := Locate()
	if err != nil {
		t.Skip(err)
	}
	if os.Getenv(EnvPython) == "" {
		t.Setenv(EnvPython, tools.Python) // place.py runs with the same python
	}
	return tools
}

// sharedTiny writes testdata/tiny.kicad_pcb into dir with three more copies
// of its mounting hole H1: one more "H1" and two "REF**" (a logo's
// placeholder designator), each with its own footprint uuid and position.
func sharedTiny(t *testing.T, dir string) string {
	t.Helper()
	src, err := os.ReadFile("testdata/tiny.kicad_pcb")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(src), "\r\n", "\n")
	start := strings.Index(s, "\t(footprint \"MountingHole:")
	end := strings.Index(s[start:], "\n\t)\n")
	if start < 0 || end < 0 {
		t.Fatal("fixture has no mounting hole footprint")
	}
	block := s[start : start+end+len("\n\t)\n")]
	dup := func(ref, uuid, at string) string {
		b := strings.Replace(block, `(uuid "78ac4046-a4ea-4708-9871-4a29dbc82cb6")`, `(uuid "`+uuid+`")`, 1)
		b = strings.Replace(b, "(at 46 36)", at, 1)
		return strings.Replace(b, `(property "Reference" "H1"`, `(property "Reference" "`+ref+`"`, 1)
	}
	extra := dup("H1", "00000000-0000-4000-8000-0000000000a1", "(at 4 36)") +
		dup("REF**", "00000000-0000-4000-8000-0000000000b1", "(at 4 4)") +
		dup("REF**", "00000000-0000-4000-8000-0000000000b2", "(at 46 4)")
	s = s[:start+end+len("\n\t)\n")] + extra + s[start+end+len("\n\t)\n"):]
	pcb := filepath.Join(dir, "shared.kicad_pcb")
	if err := os.WriteFile(pcb, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return pcb
}

type liveSnap struct {
	Components []struct {
		ID         string  `json:"primitiveId"`
		UUID       string  `json:"uuid"`
		Designator string  `json:"designator"`
		Duplicate  bool    `json:"duplicateDesignator"`
		Holder     bool    `json:"placeholder"`
		X          float64 `json:"x"`
		Y          float64 `json:"y"`
	} `json:"components"`
	Duplicates []struct {
		Designator  string   `json:"designator"`
		IDs         []string `json:"ids"`
		Placeholder bool     `json:"placeholder"`
	} `json:"duplicateDesignators"`
	Silk []struct {
		ID     string `json:"ID"`
		CompID string `json:"CompID"`
	} `json:"silk"`
	Copper struct {
		Lines []struct {
			Net string `json:"net"`
		} `json:"lines"`
	} `json:"copper"`
	Partial      []string `json:"partial"`
	Placeholders []string `json:"placeholderDesignators"`
}

func readLiveSnap(t *testing.T, tools *Tools, pcb string) liveSnap {
	t.Helper()
	raw, err := tools.Snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	var s liveSnap
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Footprints that share a designator stay separate in the snapshot (own id
// and uuid, listed in duplicateDesignators) and their silk fields do not
// collide.
func TestBridgeSnapshotSharedDesignatorsLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	s := readLiveSnap(t, tools, sharedTiny(t, t.TempDir()))
	ids := map[string]bool{}
	n := map[string]int{}
	for _, c := range s.Components {
		if ids[c.ID] || c.ID == "" || c.UUID == "" {
			t.Fatalf("component id %q (uuid %q) empty or repeated", c.ID, c.UUID)
		}
		ids[c.ID] = true
		n[c.Designator]++
		if c.Duplicate != (c.Designator == "H1" || c.Designator == "REF**") || c.Holder != (c.Designator == "REF**") {
			t.Fatalf("%s: duplicateDesignator %v placeholder %v", c.Designator, c.Duplicate, c.Holder)
		}
	}
	if n["H1"] != 2 || n["REF**"] != 2 {
		t.Fatalf("designators %v, want H1 x2 and REF** x2", n)
	}
	if len(s.Duplicates) != 2 || s.Duplicates[0].Designator != "H1" || len(s.Duplicates[0].IDs) != 2 ||
		s.Duplicates[1].Designator != "REF**" || !s.Duplicates[1].Placeholder {
		t.Fatalf("duplicateDesignators %+v", s.Duplicates)
	}
	silk := map[string]bool{}
	for _, l := range s.Silk {
		if l.CompID != "" && strings.HasPrefix(l.ID, l.CompID+"#") {
			if silk[l.ID] {
				t.Fatalf("silk id %s repeated", l.ID)
			}
			silk[l.ID] = true
		}
	}
	if len(silk) != 2*len(s.Components) {
		t.Fatalf("%d distinct designator/value silk ids, want %d", len(silk), 2*len(s.Components))
	}
	if len(s.Placeholders) != 1 || s.Placeholders[0] != "REF**" {
		t.Fatalf("placeholderDesignators %q", s.Placeholders)
	}
	if !strings.Contains(strings.Join(s.Partial, "\n"), "H1 x2") {
		t.Fatalf("partial notes %q do not name the shared H1", s.Partial)
	}
}

// One unknown net, missing id or ambiguous designator rejects the whole
// edit: every problem is listed and the board is not saved, also in place.
func TestBridgeEditRejectsUnknownNetsAndIDsLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	dir := t.TempDir()
	pcb := sharedTiny(t, dir)
	before, _ := os.ReadFile(pcb)
	out := filepath.Join(dir, "out.kicad_pcb")
	track := func(net string) map[string]any {
		return map[string]any{"net": net, "layer": 1, "startX": 100, "startY": -100, "endX": 300, "endY": -100, "lineWidth": 10}
	}
	bad := map[string]any{
		"addTracks": []any{track("GND"), track("NO_SUCH_NET")},
		"addVias":   []any{map[string]any{"net": "gnd", "x": 200, "y": -200}},
		"setWidth":  []any{map[string]any{"id": "no-such-track", "width": 12}},
		"setText":   []any{map[string]any{"id": "no-such-text", "x": 0, "y": 0}},
	}
	_, err := tools.Edit(pcb, bad, out)
	if err == nil {
		t.Fatal("edit with an unknown net was accepted")
	}
	for _, want := range []string{"net NO_SUCH_NET: not on the board", "net gnd: not on the board (did you mean GND?)", "no-such-track", "no-such-text", "board not saved"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q: %v", want, err)
		}
	}
	if _, serr := os.Stat(out); serr == nil {
		t.Fatal("rejected edit wrote the output board")
	}
	// In place: the input stays byte for byte.
	if _, err := tools.Edit(pcb, map[string]any{"addTracks": []any{track("NO_SUCH_NET")}}, pcb); err == nil {
		t.Fatal("in-place edit with an unknown net was accepted")
	}
	if after, _ := os.ReadFile(pcb); !bytes.Equal(before, after) {
		t.Fatal("rejected in-place edit changed the board")
	}
	// A layer the 2-layer board does not have.
	tr := track("GND")
	tr["layer"] = 15
	if _, err := tools.Edit(pcb, map[string]any{"addTracks": []any{tr}}, out); err == nil || !strings.Contains(err.Error(), "layer 15") {
		t.Fatalf("inner layer on a 2-layer board: %v", err)
	}
	// An ambiguous designator: rejected with its candidates; by id it works.
	_, err = tools.Edit(pcb, map[string]any{"padZoneNone": []any{map[string]any{"ref": "H1", "pad": ""}}}, out)
	if err == nil || !strings.Contains(err.Error(), "ambiguous: 2 footprints") {
		t.Fatalf("padZoneNone on the shared H1: %v", err)
	}
	if _, err := tools.Edit(pcb, map[string]any{"padZoneNone": []any{map[string]any{"id": "00000000-0000-4000-8000-0000000000a1", "pad": ""}}}, out); err != nil {
		t.Fatalf("padZoneNone by id: %v", err)
	}
	// A good edit lands on its net.
	if _, err := tools.Edit(pcb, map[string]any{"addTracks": []any{track("GND")}}, out); err != nil {
		t.Fatal(err)
	}
	s := readLiveSnap(t, tools, out)
	gnd := 0
	for _, l := range s.Copper.Lines {
		if l.Net == "" {
			t.Fatal("a track on no net")
		}
		if l.Net == "GND" {
			gnd++
		}
	}
	if gnd != 2 { // the fixture's track + the added one
		t.Fatalf("%d GND tracks, want 2", gnd)
	}
}

func TestBridgePoursRejectUnknownNetLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	dir := t.TempDir()
	pcb := sharedTiny(t, dir)
	out := filepath.Join(dir, "poured.kicad_pcb")
	_, err := tools.Pours(pcb, []Pour{{Net: "GND", Layer: 2}, {Net: "NO_SUCH_NET", Layer: 1}}, out)
	if err == nil || !strings.Contains(err.Error(), "net NO_SUCH_NET") {
		t.Fatalf("pour on an unknown net: %v", err)
	}
	if _, serr := os.Stat(out); serr == nil {
		t.Fatal("rejected pours wrote the output board")
	}
}

// Placing by a shared designator is refused (candidates listed, nothing
// written); the uuid key moves exactly that footprint.
func TestPlaceSharedDesignatorLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	dir := t.TempDir()
	pcb := sharedTiny(t, dir)
	out := filepath.Join(dir, "placed.kicad_pcb")
	_, err := Place(pcb, map[string]Pose{"H1": {XMil: 1000, YMil: 1000}, "NOPE1": {}}, out)
	var pe *PlaceError
	if !errors.As(err, &pe) || len(pe.Ambiguous) != 1 || pe.Ambiguous[0].Ref != "H1" || len(pe.Ambiguous[0].Candidates) != 2 ||
		len(pe.Missing) != 1 || pe.Missing[0].Ref != "NOPE1" {
		t.Fatalf("place by the shared H1: %v", err)
	}
	if _, serr := os.Stat(out); serr == nil {
		t.Fatal("rejected placement wrote the output board")
	}
	const second = "00000000-0000-4000-8000-0000000000a1"
	// Two records for one footprint: refused too.
	_, err = PlaceTargets(pcb, []PoseTarget{{PoseFootprint: PoseFootprint{Ref: "J1"}}, {PoseFootprint: PoseFootprint{UUID: "a17e7d90-6115-4f2e-bcd2-745186e45464"}}}, out)
	if !errors.As(err, &pe) || len(pe.PosedTwice) != 1 || !strings.Contains(err.Error(), "address the same footprint") {
		t.Fatalf("J1 by designator and uuid: %v", err)
	}
	// A record must name its footprint one way only.
	_, err = PlaceTargets(pcb, []PoseTarget{{PoseFootprint: PoseFootprint{Ref: "R1", UUID: second}}}, out)
	if !errors.As(err, &pe) || len(pe.Invalid) != 1 {
		t.Fatalf("ref and uuid in one record: %v", err)
	}
	res, err := PlaceTargets(pcb, []PoseTarget{{PoseFootprint: PoseFootprint{UUID: second}, Pose: Pose{XMil: 1000, YMil: 1000, Side: "top"}},
		{PoseFootprint: PoseFootprint{Ref: "R1"}, Pose: Pose{XMil: 600, YMil: 700, Side: "top"}}}, out)
	if err != nil || res.Moved != 2 {
		t.Fatalf("place by uuid: %+v %v", res, err)
	}
	s := readLiveSnap(t, tools, out)
	for _, c := range s.Components {
		if c.Designator != "H1" {
			continue
		}
		moved := c.X == 1000 && c.Y == -1000
		if moved != (c.UUID == second) {
			t.Fatalf("H1 %s at (%v, %v): only uuid %s may move", c.UUID, c.X, c.Y, second)
		}
	}

	// A real designator spelled "uuid:<the second H1's uuid>": a ref selector
	// (and the legacy designator-keyed map) moves that footprint, a uuid
	// selector the H1 — the namespaces never mix.
	src, _ := os.ReadFile(pcb)
	odd := "uuid:" + second
	renamed := strings.Replace(string(src), `(property "Reference" "R2"`, `(property "Reference" "`+odd+`"`, 1)
	oddPcb := filepath.Join(dir, "odd.kicad_pcb")
	if renamed == string(src) || os.WriteFile(oddPcb, []byte(renamed), 0o644) != nil {
		t.Fatal("could not rename R2")
	}
	check := func(what string, res *PlaceResult, err error, refMoves bool) {
		t.Helper()
		if err != nil || res.Moved != 1 {
			t.Fatalf("%s: %+v %v", what, res, err)
		}
		for _, c := range readLiveSnap(t, tools, out).Components {
			at := c.X == 1500 && c.Y == -500
			if c.Designator == odd && at != refMoves || c.UUID == second && at == refMoves {
				t.Fatalf("%s: %s (uuid %s) at (%v, %v)", what, c.Designator, c.UUID, c.X, c.Y)
			}
		}
	}
	p := Pose{XMil: 1500, YMil: 500, Side: "top"}
	res, err = PlaceTargets(oddPcb, []PoseTarget{{PoseFootprint: PoseFootprint{Ref: odd}, Pose: p}}, out)
	check("ref "+odd, res, err, true)
	res, err = Place(oddPcb, map[string]Pose{odd: p}, out)
	check("legacy key "+odd, res, err, true)
	res, err = PlaceTargets(oddPcb, []PoseTarget{{PoseFootprint: PoseFootprint{UUID: second}, Pose: p}}, out)
	check("uuid "+second, res, err, false)
}

// Layer ids must be exact integers (int(1.9) would be F.Cu, True == 1), a
// silk text layer must be 3 or 4, and numeric fields must be numbers: a bad
// one anywhere rejects the whole batch with the board unchanged, in place
// too.
func TestBridgeRejectsInexactLayersAndBadFieldsLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	dir := t.TempDir()
	pcb := sharedTiny(t, dir)
	before, _ := os.ReadFile(pcb)
	unchanged := func(what string) {
		t.Helper()
		if after, _ := os.ReadFile(pcb); !bytes.Equal(before, after) {
			t.Fatalf("%s: rejected in-place write changed the board", what)
		}
	}
	track := func(layer any) map[string]any {
		return map[string]any{"net": "GND", "layer": layer, "startX": 100, "startY": -100, "endX": 300, "endY": -100, "lineWidth": 10}
	}
	text := func(layer any) map[string]any {
		m := map[string]any{"text": "X", "x": 500, "y": -500}
		if layer != nil {
			m["layer"] = layer
		}
		return m
	}
	cases := []struct {
		name string
		ops  map[string]any
		want string
	}{
		{"track layer 1.9", map[string]any{"addTracks": []any{track(1.9)}}, "layer 1.9: not an integer"},
		{"track layer true", map[string]any{"addTracks": []any{track(true)}}, "layer True: not an integer"},
		{"track layer \"1\"", map[string]any{"addTracks": []any{track("1")}}, "layer '1': not an integer"},
		{"valid track + text layer 1", map[string]any{"addTracks": []any{track(1)}, "addTexts": []any{text(1)}}, "text layer 1: not 3"},
		{"text layer 3.5", map[string]any{"addTexts": []any{text(3.5)}}, "text layer 3.5: not 3"},
		{"text layer true", map[string]any{"addTexts": []any{text(true)}}, "text layer True: not 3"},
		{"track width string", map[string]any{"addTracks": []any{map[string]any{"net": "GND", "layer": 1, "startX": 0, "startY": 0, "endX": 1, "endY": 1, "lineWidth": "10"}}}, "lineWidth: '10' is not a number"},
		{"via without x", map[string]any{"addVias": []any{map[string]any{"net": "GND", "y": -200}}}, "addVias[0].x"},
		{"text without text", map[string]any{"addTexts": []any{map[string]any{"x": 1, "y": 1}}}, "addTexts[0].text"},
		{"valid track + setWidth object", map[string]any{"addTracks": []any{track(1)}, "setWidth": map[string]any{}}, "setWidth: a dict, not a list"},
		{"valid track + hideTexts string", map[string]any{"addTracks": []any{track(1)}, "hideTexts": ""}, "hideTexts: a str, not a list"},
		{"valid track + net list", map[string]any{"addTracks": []any{track(1), map[string]any{"net": []any{"GND"}, "layer": 1, "startX": 0, "startY": 0, "endX": 1, "endY": 1, "lineWidth": 8}}}, "not a net name string"},
		{"valid track + id object", map[string]any{"addTracks": []any{track(1)}, "setText": []any{map[string]any{"id": map[string]any{"a": 1}, "x": 0, "y": 0}}}, "is not a non-empty string"},
	}
	for _, c := range cases {
		out := filepath.Join(dir, "out.kicad_pcb")
		_, err := tools.Edit(pcb, c.ops, out)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "board not saved") {
			t.Fatalf("%s: %v, want %q", c.name, err, c.want)
		}
		if _, serr := os.Stat(out); serr == nil {
			t.Fatalf("%s: rejected edit wrote the output board", c.name)
		}
		if _, err := tools.Edit(pcb, c.ops, pcb); err == nil {
			t.Fatalf("%s: in-place edit accepted", c.name)
		}
		unchanged(c.name)
	}
	// Absent text layer = F.Silkscreen, 4 = B.Silkscreen: accepted.
	if _, err := tools.Edit(pcb, map[string]any{"addTexts": []any{text(nil), text(4)}}, filepath.Join(dir, "texts.kicad_pcb")); err != nil {
		t.Fatalf("valid text layers: %v", err)
	}
	for _, layer := range []any{1.9, true, "zones-object"} {
		out := filepath.Join(dir, "poured.kicad_pcb")
		raw, _ := json.Marshal(map[string]any{"zones": []any{map[string]any{"net": "GND", "layer": 2}, map[string]any{"net": "GND", "layer": layer}}})
		if layer == "zones-object" { // a container that is not a list
			raw, _ = json.Marshal(map[string]any{"zones": map[string]any{"net": "GND", "layer": 2}})
		}
		spec := filepath.Join(dir, "pours.json")
		if err := os.WriteFile(spec, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := tools.RunBridge("pours", pcb, spec, out); err == nil || !strings.Contains(err.Error(), "not an integer") && !strings.Contains(err.Error(), "zones: a dict, not a list") {
			t.Fatalf("pour layer %v: %v", layer, err)
		}
		if _, serr := os.Stat(out); serr == nil {
			t.Fatalf("pour layer %v: rejected pours wrote the output board", layer)
		}
		if _, err := tools.RunBridge("pours", pcb, spec, pcb); err == nil {
			t.Fatalf("pour layer %v: in-place accepted", layer)
		}
		unchanged("pours")
	}
	// netclasses: a non-numeric intent width is refused before any change.
	reqs := filepath.Join(dir, "reqs.json")
	raw, _ := json.Marshal(map[string]any{"pcbpilotReqs": 2, "nets": map[string]any{"GND": map[string]any{"outerMil": "wide"}}})
	if err := os.WriteFile(reqs, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := tools.RunBridge("netclasses", pcb, reqs, pcb); err == nil || !strings.Contains(err.Error(), "outerMil") {
		t.Fatalf("netclasses with a string width: %v", err)
	}
	unchanged("netclasses")
}

// twinTiny writes tiny.kicad_pcb with its mounting hole H1 carrying a
// silk graphic text, an exact twin of that footprint (same uuid, same
// designator, so ids fall back to fp:<uuid>:<n>) 40 mm to the left, and a
// board text on F.Silkscreen.
func twinTiny(t *testing.T, dir string) string {
	t.Helper()
	src, err := os.ReadFile("testdata/tiny.kicad_pcb")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(src), "\r\n", "\n")
	start := strings.Index(s, "\t(footprint \"MountingHole:")
	end := strings.Index(s[start:], "\n\t)\n") + len("\n\t)\n")
	block := strings.Replace(s[start:start+end], "\t\t(duplicate_pad_numbers_are_jumpers no)\n",
		"\t\t(fp_text user \"HOLE\" (at 0 2 0) (layer \"F.SilkS\") (uuid \"00000000-0000-4000-8000-0000000000c1\")\n"+
			"\t\t\t(effects (font (size 1 1) (thickness 0.15))))\n\t\t(duplicate_pad_numbers_are_jumpers no)\n", 1)
	twin := strings.Replace(block, "(at 46 36)", "(at 6 36)", 1)
	s = s[:start] + block + twin + s[start+end:]
	seg := strings.Index(s, "\t(segment")
	s = s[:seg] + "\t(gr_text \"BOARD\" (at 25 5 0) (layer \"F.SilkS\") (uuid \"00000000-0000-4000-8000-0000000000d1\")\n" +
		"\t\t(effects (font (size 1 1) (thickness 0.15))))\n" + s[seg:]
	pcb := filepath.Join(dir, "twin.kicad_pcb")
	if err := os.WriteFile(pcb, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	return pcb
}

type twinSnap struct {
	Components []struct {
		ID         string  `json:"primitiveId"`
		Designator string  `json:"designator"`
		X          float64 `json:"x"`
	} `json:"components"`
	Holes []struct {
		ComponentID string  `json:"componentId"`
		X           float64 `json:"x"`
	} `json:"footprintHoles"`
	Silk []struct {
		ID     string `json:"ID"`
		CompID string `json:"CompID"`
		Text   string `json:"Text"`
		Hidden bool   `json:"Hidden"`
		BBox   struct{ MinX, MaxX, MinY, MaxY float64 }
	} `json:"silk"`
}

func readTwin(t *testing.T, tools *Tools, pcb string) twinSnap {
	t.Helper()
	raw, err := tools.Snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	var s twinSnap
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

// Same uuid and same designator: fp:<uuid>:<n> ids, and the silk texts and
// holes of each twin follow that twin; edit addresses one twin's texts and
// pads by those ids, and board / footprint graphic texts are editable.
func TestBridgeTwinFootprintsLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	dir := t.TempDir()
	pcb := twinTiny(t, dir)
	s := readTwin(t, tools, pcb)
	x := map[string]float64{}
	for _, c := range s.Components {
		if c.Designator == "H1" {
			x[c.ID] = c.X
		}
	}
	const u = "78ac4046-a4ea-4708-9871-4a29dbc82cb6"
	first, second := "fp:"+u+":1", "fp:"+u+":2"
	if len(x) != 2 || x[first] == 0 && x[second] == 0 {
		t.Fatalf("H1 ids %v, want %s and %s", x, first, second)
	}
	for _, h := range s.Holes {
		if xx, ok := x[h.ComponentID]; ok && math.Abs(h.X-xx) > 0.01 {
			t.Fatalf("hole of %s at x %v, its footprint at %v", h.ComponentID, h.X, xx)
		}
	}
	var hole []string // the twins' graphic text ids
	board := ""
	for _, l := range s.Silk {
		if xx, ok := x[l.CompID]; ok {
			if cx := (l.BBox.MinX + l.BBox.MaxX) / 2; math.Abs(cx-xx) > 100 {
				t.Fatalf("silk %s (%q) at x %.0f, its footprint %s at %.0f", l.ID, l.Text, cx, l.CompID, xx)
			}
			if l.Text == "HOLE" {
				hole = append(hole, l.ID)
			}
		}
		if l.Text == "BOARD" {
			board = l.ID
		}
	}
	if len(hole) != 2 || hole[0] == hole[1] || board == "" {
		t.Fatalf("graphic text ids %v, board text %q", hole, board)
	}
	out := filepath.Join(dir, "edited.kicad_pcb")
	// KiCad keeps no hidden state for a graphic text: refused, not lost.
	if _, err := tools.Edit(pcb, map[string]any{"hideTexts": []any{hole[1]}}, out); err == nil || !strings.Contains(err.Error(), "graphic text") {
		t.Fatalf("hiding a graphic text: %v", err)
	}
	_, err := tools.Edit(pcb, map[string]any{
		"setText": []any{map[string]any{"id": second + "#ref", "x": 300, "y": -1000}, map[string]any{"id": board, "x": 1000, "y": -300},
			map[string]any{"id": hole[1], "x": 500, "y": -1200}},
		"hideTexts":   []any{second + "#ref"},
		"padZoneNone": []any{map[string]any{"id": second, "pad": ""}},
	}, out)
	if err != nil {
		t.Fatal(err)
	}
	// SaveBoard re-sorts footprints, so fp:<uuid>:<n> is only stable for an
	// unchanged file: identify the twins in the result by position.
	e := readTwin(t, tools, out)
	edited := map[string]bool{} // output id → is the twin we edited
	for _, c := range e.Components {
		if c.Designator == "H1" {
			edited[c.ID] = math.Abs(c.X-x[second]) < 0.01
		}
	}
	moved, hidden := 0, 0
	for _, l := range e.Silk {
		cx, cy := (l.BBox.MinX+l.BBox.MaxX)/2, (l.BBox.MinY+l.BBox.MaxY)/2
		if l.ID == board && (math.Abs(cx-1000) > 1 || math.Abs(cy+300) > 1) {
			t.Fatalf("board text at (%.1f, %.1f)", cx, cy)
		}
		mine, ok := edited[l.CompID]
		if !ok {
			continue
		}
		var at bool
		switch {
		case l.ID == l.CompID+"#ref":
			at = math.Abs(cx-300) < 1 && math.Abs(cy+1000) < 1
			if l.Hidden != mine {
				t.Fatalf("%s hidden=%v: only the edited twin's designator may hide", l.ID, l.Hidden)
			}
			if l.Hidden {
				hidden++
			}
		case l.Text == "HOLE":
			at = math.Abs(cx-500) < 1 && math.Abs(cy+1200) < 1
		default:
			continue
		}
		if at != mine {
			t.Fatalf("%s (%q) at (%.1f, %.1f): only the edited twin's texts may move", l.ID, l.Text, cx, cy)
		}
		if at {
			moved++
		}
	}
	if moved != 2 || hidden != 1 {
		t.Fatalf("moved %d text(s), hid %d; want the edited twin's designator and graphic text moved, its designator hidden", moved, hidden)
	}
}

// Two tracks with one uuid: the snapshot names the shared id, and setWidth
// on it is refused with nothing saved (a dict by uuid used to widen one of
// them and save).
func TestBridgeDuplicateTrackUUIDLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	dir := t.TempDir()
	src, err := os.ReadFile("testdata/tiny.kicad_pcb")
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ReplaceAll(string(src), "\r\n", "\n")
	start := strings.Index(s, "\t(segment")
	end := strings.Index(s[start:], "\n\t)\n") + len("\n\t)\n")
	seg := s[start : start+end]
	twin := strings.Replace(strings.Replace(seg, "(start 10.8 8)", "(start 10.8 12)", 1), "(end 22.5 18.1)", "(end 22.5 22.1)", 1)
	s = s[:start+end] + twin + s[start+end:]
	pcb := filepath.Join(dir, "duptrack.kicad_pcb")
	if err := os.WriteFile(pcb, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	raw, err := tools.Snapshot(pcb)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Copper struct {
			Lines []struct {
				ID string `json:"primitiveId"`
			} `json:"lines"`
			Dup []string `json:"duplicateIds"`
		} `json:"copper"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	const id = "b93f23a7-11a1-4363-9345-fd2db67dc1ac"
	if len(snap.Copper.Lines) != 2 || snap.Copper.Lines[0].ID != snap.Copper.Lines[1].ID {
		t.Skipf("KiCad did not keep the repeated track uuid on load: %+v", snap.Copper.Lines)
	}
	if len(snap.Copper.Dup) != 1 || snap.Copper.Dup[0] != id {
		t.Fatalf("copper.duplicateIds %v", snap.Copper.Dup)
	}
	before, _ := os.ReadFile(pcb)
	out := filepath.Join(dir, "wide.kicad_pcb")
	ops := map[string]any{"setWidth": []any{map[string]any{"id": id, "width": 20}}}
	if _, err := tools.Edit(pcb, ops, out); err == nil || !strings.Contains(err.Error(), "ambiguous: 2 tracks/vias share that uuid") {
		t.Fatalf("setWidth on a shared uuid: %v", err)
	}
	if _, serr := os.Stat(out); serr == nil {
		t.Fatal("rejected edit wrote the output board")
	}
	if _, err := tools.Edit(pcb, ops, pcb); err == nil {
		t.Fatal("in-place setWidth on a shared uuid accepted")
	}
	if after, _ := os.ReadFile(pcb); !bytes.Equal(before, after) {
		t.Fatal("rejected in-place edit changed the board")
	}
}

// interfCopy copies KiCad's interf_u demo board + project into dir (the demo
// itself is read-only); a skip when the demos are not installed.
func interfCopy(t *testing.T, dir string) string {
	t.Helper()
	demos := kicadDemosDir()
	if demos == "" {
		t.Skipf("KiCad demos not found (set %s)", EnvKiCadDemos)
	}
	for _, ext := range []string{".kicad_pcb", ".kicad_pro"} {
		b, err := os.ReadFile(filepath.Join(demos, "interf_u", "interf_u"+ext))
		if err != nil {
			t.Skipf("interf_u demo: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "interf_u"+ext), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dir, "interf_u.kicad_pcb")
}

// interf_u's project sets min hole 0.5 mm, min via diameter 0.9 mm. The
// pcbpilot netclasses used to carry KiCad's built-in 0.6 / 0.3 mm via, so
// every routed via of an intent net failed DRC (drill_out_of_range,
// via_diameter). Every class and every via padstack of the DSN the router
// reads must now meet the board's minimums, and an insulation-domain class
// must not override the nets' own sizes.
func TestBridgeNetclassViasMeetBoardMinimumsLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	dir := t.TempDir()
	pcb := interfCopy(t, dir)
	reqs := map[string]NetRequirement{
		"/ACK":  {OuterMil: 10, InnerMil: 10, ClearanceMil: 8},
		"/BIT0": {OuterMil: 12, InnerMil: 12, ClearanceMil: 8, ViaDiaMil: 23.62, ViaDrillMil: 11.81}, // 0.6 / 0.3 mm intent
		"/BIT1": {OuterMil: 12, InnerMil: 12, ClearanceMil: 8, ViaDiaMil: 70.87, ViaDrillMil: 31.5},  // 1.8 / 0.8 mm intent
	}
	iso := []map[string]any{{"a": "lo", "b": "hi", "aNets": []string{"/BIT2"}, "bNets": []string{"/BIT3"}, "clearanceMm": 1.0}}
	out := filepath.Join(dir, "classed.kicad_pcb")
	res, err := tools.NetclassesIso(pcb, reqs, iso, nil, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatched) > 0 {
		t.Fatalf("effective sizes differ from the written classes: %v", res.Mismatched)
	}
	const minDia, minDrill = 0.9 / 0.0254, 0.5 / 0.0254
	vias := map[string][2]float64{}
	for _, c := range res.Classes {
		if c.ViaDiameterMil+1e-3 < minDia || c.ViaDrillMil+1e-3 < minDrill {
			t.Errorf("class %s via %.2f/%.2f mil below the board minimums %.2f/%.2f", c.Name, c.ViaDiameterMil, c.ViaDrillMil, minDia, minDrill)
		}
		for _, n := range c.Nets {
			vias[n] = [2]float64{c.ViaDiameterMil, c.ViaDrillMil}
		}
	}
	// Default is 1.4 / 0.6 mm: kept for /ACK and the small intent via; the
	// larger intent via wins for /BIT1.
	near := func(a, b float64) bool { return math.Abs(a-b) < 0.05 }
	if v := vias["/ACK"]; !near(v[0], 1.4/0.0254) || !near(v[1], 0.6/0.0254) {
		t.Errorf("/ACK via %v, want the Default class 1.4/0.6 mm", v)
	}
	if v := vias["/BIT0"]; !near(v[0], 1.4/0.0254) || !near(v[1], 0.6/0.0254) {
		t.Errorf("/BIT0 via %v: a smaller intent via must not lower the class", v)
	}
	if v := vias["/BIT1"]; !near(v[0], 70.87) || !near(v[1], 31.5) {
		t.Errorf("/BIT1 via %v, want the intent 1.8/0.8 mm", v)
	}
	dsn := filepath.Join(dir, "classed.dsn")
	if _, err := tools.ExportDSN(out, dsn); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ps := regexp.MustCompile(`Via\[\d+-\d+\]_(\d+):(\d+)_um`).FindAllStringSubmatch(string(text), -1)
	if len(ps) == 0 {
		t.Fatal("DSN declares no via padstack")
	}
	for _, m := range ps {
		d, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		if d < 900 || h < 500 {
			t.Errorf("DSN via padstack %s below min via 900 um / min hole 500 um", m[0])
		}
	}
	// The domain class carries only its name: /BIT2 keeps Default's sizes.
	snap, err := tools.Snapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Rules struct {
			ViaDrillMil    float64 `json:"viaDrillMil"`
			ViaDiameterMil float64 `json:"viaDiameterMil"`
		} `json:"rules"`
		Netclasses []struct {
			Name          string   `json:"name"`
			TrackWidthMil *float64 `json:"trackWidthMil"`
		} `json:"netclasses"`
	}
	if err := json.Unmarshal(snap, &s); err != nil {
		t.Fatal(err)
	}
	for _, c := range s.Netclasses {
		if strings.HasPrefix(c.Name, "PPD_") && c.TrackWidthMil != nil {
			t.Errorf("domain class %s sets its own track width %.2f mil", c.Name, *c.TrackWidthMil)
		}
	}
	if s.Rules.ViaDrillMil+1e-3 < minDrill || s.Rules.ViaDiameterMil+1e-3 < minDia {
		t.Errorf("snapshot rules via %.2f/%.2f below the board minimums", s.Rules.ViaDiameterMil, s.Rules.ViaDrillMil)
	}
}

// A project whose Default via (0.6 / 0.3 mm) is itself below its minimums,
// plus a class with a diameter only: every net outside the intent gets a
// floored effective via (PPV_ class), so no DSN padstack stays below them.
func TestBridgeNetclassFloorsLowDefaultLive(t *testing.T) {
	tools := bridgeLiveTools(t)
	dir := t.TempDir()
	pcb := interfCopy(t, dir)
	pro := strings.TrimSuffix(pcb, ".kicad_pcb") + ".kicad_pro"
	raw, err := os.ReadFile(pro)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	ns := doc["net_settings"].(map[string]any)
	classes := ns["classes"].([]any)
	for _, c := range classes {
		if m := c.(map[string]any); m["name"] == "Default" {
			m["via_diameter"], m["via_drill"] = 0.6, 0.3
		}
	}
	ns["classes"] = append(classes, map[string]any{"name": "DiaOnly", "via_diameter": 1.0, "priority": 1})
	ns["netclass_patterns"] = append(anyList(ns["netclass_patterns"]), map[string]any{"netclass": "DiaOnly", "pattern": "/BIT5"})
	b, _ := json.MarshalIndent(doc, "", "  ")
	if err := os.WriteFile(pro, b, 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "classed.kicad_pcb")
	res, err := tools.NetclassesIso(pcb, map[string]NetRequirement{"/ACK": {OuterMil: 10, InnerMil: 10, ClearanceMil: 8}}, nil, nil, out)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Mismatched) > 0 {
		t.Fatalf("effective sizes differ from the written classes / floors: %v", res.Mismatched)
	}
	if len(res.ViaFloored) == 0 {
		t.Fatalf("no via floor reported: %+v", res)
	}
	dsn := filepath.Join(dir, "classed.dsn")
	if _, err := tools.ExportDSN(out, dsn); err != nil {
		t.Fatal(err)
	}
	text, _ := os.ReadFile(dsn)
	ps := regexp.MustCompile(`Via\[\d+-\d+\]_(\d+):(\d+)_um`).FindAllStringSubmatch(string(text), -1)
	seen := map[string]bool{}
	for _, m := range ps {
		d, _ := strconv.Atoi(m[1])
		h, _ := strconv.Atoi(m[2])
		seen[m[1]+":"+m[2]] = true
		if d < 900 || h < 500 || d < h+100 {
			t.Errorf("DSN via padstack %s below min via 900 um / hole 500 um / ring 50 um", m[0])
		}
	}
	if !seen["1000:500"] {
		t.Errorf("the diameter-only class should give 1000:500, padstacks %v (floored %v)", seen, res.ViaFloored)
	}
}

func anyList(v any) []any {
	l, _ := v.([]any)
	return l
}
