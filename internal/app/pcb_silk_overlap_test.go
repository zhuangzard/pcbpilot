package app

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// loadCeshiSilk loads the 2026-09-25 ceshi E2E readback: every designator after
// `pcb silk-align` had reported 0 unresolved, plus 3 hidden attributes.
func loadCeshiSilk(t *testing.T) []pcbSilkText {
	t.Helper()
	raw, err := os.ReadFile("testdata/silk/ceshi-e2e-20260925-designators.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Silk []pcbSilkText `json:"silk"`
	}
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatal(err)
	}
	return fx.Silk
}

func pairKeys(ps [][2]string) []string {
	var out []string
	for _, p := range ps {
		a, b := p[0], p[1]
		if b < a {
			a, b = b, a
		}
		out = append(out, a+"/"+b)
	}
	sort.Strings(out)
	return out
}

// The live dump had seven overlapping designator pairs that `pcb check` could
// not see (no silk-to-silk rule). The new rule must list exactly those.
func TestFindSilkOverlap_CeshiReadback(t *testing.T) {
	silk := loadCeshiSilk(t)
	got := findSilkOverlap(silk)
	var names [][2]string
	for _, f := range got {
		if f.Type != "silk-overlap" || f.Level != "WARN" || len(f.Primitives) != 2 {
			t.Fatalf("unexpected finding shape: %+v", f)
		}
		parts := strings.Fields(strings.TrimPrefix(f.Message, "designators "))
		names = append(names, [2]string{parts[0], parts[2]})
	}
	want := []string{"C1/U4", "C2/C3", "C4/C5", "C6/R3", "C7/R6", "R5/R9", "R8/U3"}
	if k := pairKeys(names); !reflect.DeepEqual(k, want) {
		t.Fatalf("silk-overlap pairs = %v, want %v", k, want)
	}

	// Wired into the full check with its own summary counter.
	rep := analyzePcbCheckFull(nil, nil, nil, nil, silk, 0, nil)
	if rep.Summary.SilkOverlap != 7 {
		t.Fatalf("summary silkOverlap = %d, want 7", rep.Summary.SilkOverlap)
	}
	var buf strings.Builder
	renderPcbCheckReport(rep, &buf)
	if !strings.Contains(buf.String(), "silkOverlap=7") {
		t.Fatalf("summary line must carry silkOverlap=7:\n%s", buf.String())
	}
}

// Hidden attributes, box-less texts, free strings and opposite silk layers are
// not designator-vs-designator overlaps; edge contact is not an overlap.
func TestDesignatorSilkOverlaps_Scope(t *testing.T) {
	box := func(x0, y0, x1, y1 float64) *pcbRect { return &pcbRect{x0, y0, x1, y1} }
	d := func(id, text string, layer int, bb *pcbRect) pcbSilkText {
		return pcbSilkText{ID: id, Kind: "attribute", Key: "Designator", Text: text, Layer: layer, BBox: bb}
	}
	silk := []pcbSilkText{
		d("a", "C2", 3, box(563.3, 539.8, 616.5, 584.8)),
		d("b", "C3", 3, box(541.4, 539.1, 594.6, 584.1)), // overlaps C2
		d("c", "C9", 4, box(541.4, 539.1, 594.6, 584.1)), // bottom silk: no
		d("e", "R1", 3, box(616.5, 539.8, 660, 584.8)),   // touches C2's edge: no
		{ID: "f", Kind: "attribute", Key: "Designator", Text: "R2", Layer: 3, BBox: box(560, 540, 600, 580), Hidden: true},
		{ID: "g", Kind: "string", Text: "LOGO", Layer: 3, BBox: box(560, 540, 600, 580)},
		{ID: "h", Kind: "attribute", Key: "Footprint", Text: "C0805", Layer: 3, BBox: box(560, 540, 600, 580)},
	}
	got := pairKeys(silkPairsOf(designatorSilkOverlaps(silk)))
	if !reflect.DeepEqual(got, []string{"C2/C3"}) {
		t.Fatalf("pairs = %v, want [C2/C3]", got)
	}
}

// fakeSilkBoard is an offline board for the convergence loop: pcb.silk.align
// "places" each in-scope designator via a per-round script, pcb.silk.list
// returns the boxes as the connector would.
type fakeSilkBoard struct {
	boxes  map[string][4]float64
	moves  []map[string][4]float64 // per align call; missing ref = stays put
	calls  int
	gotRef [][]string
}

func (f *fakeSilkBoard) act(action string, p map[string]any) (map[string]any, error) {
	switch action {
	case "pcb.silk.align":
		var refs []string
		if r, ok := p["refs"].([]string); ok {
			refs = r
		}
		f.gotRef = append(f.gotRef, refs)
		if f.calls < len(f.moves) {
			for d, b := range f.moves[f.calls] {
				if len(refs) == 0 || contains(refs, d) {
					f.boxes[d] = b
				}
			}
		}
		f.calls++
		// the connector's own verdict — deliberately "all clean", like the live run
		return map[string]any{"aligned": float64(len(f.boxes)), "unresolved": float64(0)}, nil
	case "pcb.silk.list":
		var texts []any
		keys := make([]string, 0, len(f.boxes))
		for k := range f.boxes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, d := range keys {
			b := f.boxes[d]
			texts = append(texts, map[string]any{
				"primitiveId": "attr_" + d, "kind": "attribute", "key": "Designator", "text": d,
				"layer": float64(3), "valueVisible": true,
				"bbox": map[string]any{"minX": b[0], "minY": b[1], "maxX": b[2], "maxY": b[3]},
			})
		}
		return map[string]any{"texts": texts}, nil
	}
	return nil, fmt.Errorf("unexpected action %s", action)
}

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}

// The live failure: the connector claimed 0 unresolved while C2/C3 overlapped.
// The loop must judge on the readback, re-align ONLY the overlapping pair with
// everything else frozen, and stop once the readback is clean.
func TestSilkAlignConverge_RealignsOnlyReadbackOverlaps(t *testing.T) {
	b := &fakeSilkBoard{
		boxes: map[string][4]float64{
			"C2": {100, 100, 150, 145}, "C3": {300, 100, 350, 145}, "R1": {600, 100, 650, 145},
		},
		moves: []map[string][4]float64{
			{"C2": {563.3, 539.8, 616.5, 584.8}, "C3": {541.4, 539.1, 594.6, 584.1}}, // pass 1: overlap
			{"C3": {541.4, 460, 594.6, 505}},                                         // pass 2: C3 moves down
		},
	}
	rep, err := runSilkAlignConverge(b.act, map[string]any{"spacing": 2.0}, nil, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Converged || len(rep.UnresolvedPairs) != 0 || len(rep.Rounds) != 2 {
		t.Fatalf("want converged after 2 rounds, got %+v", rep)
	}
	if got := pairKeys(rep.Rounds[0].Overlaps); !reflect.DeepEqual(got, []string{"C2/C3"}) {
		t.Fatalf("round 1 readback overlaps = %v, want [C2/C3] (the connector said 0 unresolved)", got)
	}
	if b.gotRef[0] != nil {
		t.Fatalf("round 1 must use the caller's full scope, got refs %v", b.gotRef[0])
	}
	if !reflect.DeepEqual(b.gotRef[1], []string{"C2", "C3"}) {
		t.Fatalf("round 2 must re-align only the overlapping pair, got %v", b.gotRef[1])
	}
}

// Never converging must say so with the readback pairs — not "0 unresolved".
func TestSilkAlignConverge_ReportsReadbackPairsWhenStuck(t *testing.T) {
	b := &fakeSilkBoard{boxes: map[string][4]float64{
		"C2": {563.3, 539.8, 616.5, 584.8}, "C3": {541.4, 539.1, 594.6, 584.1}, "U9": {0, 0, 40, 40},
	}}
	rep, err := runSilkAlignConverge(b.act, nil, []string{"C2", "C3"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Converged || len(rep.Rounds) != 3 || b.calls != 3 {
		t.Fatalf("want 3 rounds, not converged; got %+v (calls %d)", rep, b.calls)
	}
	if got := pairKeys(rep.UnresolvedPairs); !reflect.DeepEqual(got, []string{"C2/C3"}) {
		t.Fatalf("unresolvedPairs = %v, want [C2/C3]", got)
	}
	if !strings.Contains(rep.Note, "silk-set") {
		t.Fatalf("note must name the way out: %q", rep.Note)
	}
}

// --refs scope: a pre-existing overlap between two out-of-scope labels is not
// this run's to fix, and an out-of-scope partner is never moved.
func TestSilkAlignConverge_RespectsScope(t *testing.T) {
	b := &fakeSilkBoard{boxes: map[string][4]float64{
		"C2": {0, 0, 50, 45}, "U4": {20, 0, 70, 45}, // in-scope C2 overlaps frozen U4
		"R5": {500, 0, 550, 45}, "R9": {520, 0, 570, 45}, // both out of scope
	}}
	b.moves = []map[string][4]float64{{}, {"C2": {0, 200, 50, 245}}}
	rep, err := runSilkAlignConverge(b.act, nil, []string{"C2"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Converged {
		t.Fatalf("R5/R9 are outside --refs and must not block convergence: %+v", rep)
	}
	for _, r := range b.gotRef {
		if !reflect.DeepEqual(r, []string{"C2"}) {
			t.Fatalf("only C2 may ever be re-aligned, got %v", r)
		}
	}
}
