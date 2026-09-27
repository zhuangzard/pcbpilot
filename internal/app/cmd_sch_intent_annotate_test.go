package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRenderIntentAnnotationESP32(t *testing.T) {
	in := mustIntent(t)
	lines := renderIntentAnnotation(in)
	if !strings.HasPrefix(lines[0], intentAnnotateTag+" ELECTRICAL INTENT sha256:"+in.sourceSHA[:12]) {
		t.Fatalf("header %q", lines[0])
	}
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"BLOCKS\n  PWR_IN power-input J1: 5V screw terminal OR USB VBUS → +5V",
		"  BUCK_3V3 buck U4: 5V→3.318V sync buck, 0.52 A peak",
		"  +3V3: 3.318/3.35 V | 0.52 A | 20/12/12 mil | POWER | 8 mil | x1",
		"  SW: 5/6 V | 0.52 A | 20/20/16 mil | SWITCH | 10 mil | x0",
		"  GND: 0/0 V | 0.6 A | 20/20/12 mil | GND | 6 mil | x2",
		"DIFF PAIRS\n  USB_D: USB_DP/USB_DM 90 ohm diff, w 8 mil, gap 6 mil",
		"  POWER: 5V_TERM,VBUS,+5V,+3V3 | track 20 | clr 8 | via 12/24 mil",
		"FINDINGS\n  [warn] SW node must stay short and on one layer -> place L1 next to U4 pin SW",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in\n%s", want, joined)
		}
	}
	// power rails before switch before ground; signals are not rails
	if strings.Index(joined, "  +3V3:") > strings.Index(joined, "  SW:") || strings.Index(joined, "  SW:") > strings.Index(joined, "  GND:") || strings.Contains(joined, "  TXD0:") {
		t.Fatalf("rail order/filter wrong:\n%s", joined)
	}
	if strings.Join(renderIntentAnnotation(in), "\n") != joined {
		t.Fatal("rendering must be deterministic")
	}
}

func TestFindFreeTextAreaAvoidsObstacles(t *testing.T) {
	region := layoutBBox{MinX: 0, MinY: 0, MaxX: 400, MaxY: 300}
	obs := []layoutBBox{{MinX: 0, MinY: 150, MaxX: 250, MaxY: 300}}
	x, top, ok := findFreeTextArea(region, obs, 100, 50, 10, 10)
	if !ok || x < 250 || top-50 < 0 {
		t.Fatalf("x=%v top=%v ok=%v", x, top, ok)
	}
	r := layoutBBox{MinX: x, MinY: top - 50, MaxX: x + 100, MaxY: top}
	if bboxesOverlap(r, obs[0]) {
		t.Fatal("overlaps")
	}
	if _, _, ok := findFreeTextArea(region, []layoutBBox{region}, 100, 50, 10, 10); ok {
		t.Fatal("full region must fail")
	}
}

type fakeSchTextHost struct {
	mu        sync.Mutex
	texts     []map[string]any
	next      int
	writes    []string
	unverify  bool
	parts     []any
	wires     []any
	deleteNop bool
}

func newFakeSchTextHost() *fakeSchTextHost {
	return &fakeSchTextHost{
		texts: []map[string]any{{"primitiveId": "user1", "content": "NOTE: hand text", "x": 600.0, "y": 500.0}},
		parts: []any{
			map[string]any{"primitiveId": "sheet", "componentType": "sheet", "x": 0.0, "y": 0.0, "bbox": map[string]any{"minX": 0.0, "minY": 0.0, "maxX": 1170.0, "maxY": 825.0}},
			map[string]any{"primitiveId": "U1", "designator": "U1", "componentType": "part", "x": 100.0, "y": 750.0, "bbox": map[string]any{"minX": 20.0, "minY": 650.0, "maxX": 300.0, "maxY": 810.0}},
		},
		wires: []any{map[string]any{"x0": 300.0, "y0": 700.0, "x1": 700.0, "y1": 700.0, "net": "N1"}},
	}
}

func (h *fakeSchTextHost) handle(action string, p map[string]any) (map[string]any, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch action {
	case "schematic.text.list":
		var out []any
		for _, t := range h.texts {
			out = append(out, jsonClone(t))
		}
		return map[string]any{"texts": out, "count": len(out)}, nil
	case "schematic.components.list":
		return map[string]any{"components": jsonClone(h.parts), "count": len(h.parts), "wires": jsonClone(h.wires), "wiresAvailable": true}, nil
	case "schematic.titleblock.get":
		return map[string]any{"showTitleBlock": true, "titleBlockData": map[string]any{"Blade Width": map[string]any{"value": "10"}}}, nil
	case "schematic.text.create":
		h.writes = append(h.writes, action)
		h.next++
		id := fmt.Sprintf("txt%d", h.next)
		h.texts = append(h.texts, map[string]any{"primitiveId": id, "content": p["content"], "x": p["x"], "y": p["y"]})
		return map[string]any{"primitiveId": id, "verified": !h.unverify}, nil
	case "schematic.primitives.delete":
		h.writes = append(h.writes, action)
		ids := map[string]bool{}
		for _, id := range asStrSlice(p["primitiveIds"]) {
			ids[id] = true
		}
		if !h.deleteNop {
			var keep []map[string]any
			for _, t := range h.texts {
				if !ids[asString(t["primitiveId"])] {
					keep = append(keep, t)
				}
			}
			h.texts = keep
		}
		return map[string]any{"total": len(ids)}, nil
	}
	return nil, fmt.Errorf("unexpected %s", action)
}

func (h *fakeSchTextHost) serve() (*appConfig, func()) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			fmt.Fprint(w, `{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`)
		case "/action":
			var body struct {
				Action  string         `json:"action"`
				Payload map[string]any `json:"payload"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			res, err := h.handle(body.Action, body.Payload)
			if err != nil {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": map[string]any{"code": "X", "message": err.Error()}})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": res})
		default:
			http.NotFound(w, r)
		}
	}))
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	return &appConfig{host: host, ports: port + "-" + port}, srv.Close
}

func runAnnotateCLI(t *testing.T, h *fakeSchTextHost, args ...string) (intentAnnotateReport, error) {
	t.Helper()
	cfg, stop := h.serve()
	defer stop()
	var stdout, stderr bytes.Buffer
	cmd := newSchCmd(cfg, &stdout, &stderr)
	cmd.SetOut(&stderr)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"intent-annotate"}, args...))
	err := cmd.Execute()
	var rep intentAnnotateReport
	if jerr := json.Unmarshal(stdout.Bytes(), &rep); jerr != nil {
		t.Fatalf("stdout not a report: %v\n%s\n%s", jerr, stdout.String(), stderr.String())
	}
	return rep, err
}

func TestSchIntentAnnotateApplyReplayReplace(t *testing.T) {
	h := newFakeSchTextHost()
	journal := filepath.Join(t.TempDir(), "j.json")
	rep, err := runAnnotateCLI(t, h, "--intent", esp32IntentFixture, "--journal", journal)
	if err != nil || rep.Status != "applied" || !rep.Verified {
		t.Fatalf("apply err=%v rep=%+v", err, rep)
	}
	n := len(rep.Lines)
	if len(rep.Created) != n || len(h.writes) != n {
		t.Fatalf("created=%d writes=%d lines=%d", len(rep.Created), len(h.writes), n)
	}
	// placed inside the border, clear of U1, the wire, the user text and the title block
	pl := rep.Placement
	block := layoutBBox{MinX: pl.X, MinY: pl.Top - pl.Height, MaxX: pl.X + pl.Width, MaxY: pl.Top}
	for _, o := range []layoutBBox{{MinX: 20, MinY: 650, MaxX: 300, MaxY: 810}, {MinX: 300, MinY: 700, MaxX: 700, MaxY: 700.01}, {MinX: 600, MinY: 496, MaxX: 700, MaxY: 512}, {MinX: 1170 - 702, MinY: 0, MaxX: 1170, MaxY: 198}} {
		if bboxesOverlap(block, o) {
			t.Fatalf("block %+v overlaps %+v", block, o)
		}
	}
	if block.MinX < 10 || block.MaxY > 815 || block.MinY < 10 {
		t.Fatalf("block outside border: %+v", block)
	}
	if rep.Created[1].Y != rep.Created[0].Y-12 || rep.Created[0].Content != rep.Lines[0] {
		t.Fatalf("line layout %+v", rep.Created[:2])
	}
	var j intentAnnotateJournal
	raw, _ := os.ReadFile(journal)
	_ = json.Unmarshal(raw, &j)
	if len(j.Texts) != n || len(j.PendingDelete) != 0 {
		t.Fatalf("journal %+v", j)
	}

	// identical replay: no writes
	h.writes = nil
	rep, err = runAnnotateCLI(t, h, "--intent", esp32IntentFixture, "--journal", journal)
	if err != nil || rep.Status != "in-sync" || len(h.writes) != 0 {
		t.Fatalf("replay err=%v status=%s writes=%v", err, rep.Status, h.writes)
	}

	// changed intent: exactly the journaled texts are replaced; user text survives
	rawIntent, _ := os.ReadFile(esp32IntentFixture)
	changed := filepath.Join(t.TempDir(), "intent.json")
	_ = os.WriteFile(changed, bytes.Replace(rawIntent, []byte("0.52 A peak"), []byte("0.60 A peak"), 1), 0o644)
	oldIDs := idsOf(j.Texts)
	h.writes = nil
	rep, err = runAnnotateCLI(t, h, "--intent", changed, "--journal", journal)
	if err != nil || rep.Status != "applied" {
		t.Fatalf("replace err=%v rep=%+v", err, rep)
	}
	if strings.Join(rep.Deleted, ",") != strings.Join(oldIDs, ",") {
		t.Fatalf("deleted %v want %v", rep.Deleted, oldIDs)
	}
	ids := map[string]bool{}
	for _, tx := range h.texts {
		ids[asString(tx["primitiveId"])] = true
	}
	if !ids["user1"] || len(h.texts) != n+1 {
		t.Fatalf("texts after replace: %v", h.texts)
	}
}

func TestSchIntentAnnotateDryRunWritesNothing(t *testing.T) {
	h := newFakeSchTextHost()
	journal := filepath.Join(t.TempDir(), "j.json")
	rep, err := runAnnotateCLI(t, h, "--intent", esp32IntentFixture, "--journal", journal, "--dry-run")
	if err != nil || rep.Status != "planned" || len(rep.Created) != len(rep.Lines) || len(h.writes) != 0 {
		t.Fatalf("err=%v rep=%+v writes=%v", err, rep, h.writes)
	}
	if _, err := os.Stat(journal); !os.IsNotExist(err) {
		t.Fatal("dry-run must not write a journal")
	}
}

func TestSchIntentAnnotateRefusesEditedTextsAndReportsOrphans(t *testing.T) {
	h := newFakeSchTextHost()
	journal := filepath.Join(t.TempDir(), "j.json")
	if _, err := runAnnotateCLI(t, h, "--intent", esp32IntentFixture, "--journal", journal); err != nil {
		t.Fatal(err)
	}
	h.texts[1]["content"] = "hand edited"
	h.writes = nil
	rep, err := runAnnotateCLI(t, h, "--intent", esp32IntentFixture, "--journal", journal)
	if err == nil || rep.Status != "journal-mismatch" || len(h.writes) != 0 {
		t.Fatalf("edited text must stop the run: err=%v status=%s writes=%v", err, rep.Status, h.writes)
	}

	// a tagged header from a lost journal is reported, never deleted
	h2 := newFakeSchTextHost()
	h2.texts = append(h2.texts, map[string]any{"primitiveId": "stale", "content": intentAnnotateTag + " ELECTRICAL INTENT old", "x": 900.0, "y": 600.0})
	rep, err = runAnnotateCLI(t, h2, "--intent", esp32IntentFixture, "--journal", filepath.Join(t.TempDir(), "new.json"))
	if err != nil || strings.Join(rep.Orphans, ",") != "stale" {
		t.Fatalf("err=%v orphans=%v", err, rep.Orphans)
	}
	found := false
	for _, tx := range h2.texts {
		found = found || tx["primitiveId"] == "stale"
	}
	if !found {
		t.Fatal("orphan deleted")
	}
}

func TestSchIntentAnnotateUnverifiedExitsNonZeroAndJournalsIDs(t *testing.T) {
	h := newFakeSchTextHost()
	h.unverify = true
	journal := filepath.Join(t.TempDir(), "j.json")
	rep, err := runAnnotateCLI(t, h, "--intent", esp32IntentFixture, "--journal", journal)
	if err == nil || rep.Status != "unverified" {
		t.Fatalf("err=%v status=%s", err, rep.Status)
	}
	var j intentAnnotateJournal
	raw, _ := os.ReadFile(journal)
	_ = json.Unmarshal(raw, &j)
	if len(j.Texts) != 1 || j.Texts[0].PrimitiveID != "txt1" {
		t.Fatalf("the created id must be journaled for cleanup: %+v", j)
	}
	// delete that silently keeps the old block → non-zero
	h2 := newFakeSchTextHost()
	j2 := filepath.Join(t.TempDir(), "j.json")
	if _, err := runAnnotateCLI(t, h2, "--intent", esp32IntentFixture, "--journal", j2); err != nil {
		t.Fatal(err)
	}
	h2.deleteNop = true
	rawIntent, _ := os.ReadFile(esp32IntentFixture)
	changed := filepath.Join(t.TempDir(), "intent.json")
	_ = os.WriteFile(changed, bytes.Replace(rawIntent, []byte("status LED"), []byte("status LED 2"), 1), 0o644)
	if rep, err := runAnnotateCLI(t, h2, "--intent", changed, "--journal", j2); err == nil || rep.Status != "unverified" {
		t.Fatalf("surviving old texts must fail: err=%v status=%s", err, rep.Status)
	}
}

func TestSchIntentAnnotateExplicitPlacementAndFlagPairing(t *testing.T) {
	h := newFakeSchTextHost()
	journal := filepath.Join(t.TempDir(), "j.json")
	rep, err := runAnnotateCLI(t, h, "--intent", esp32IntentFixture, "--journal", journal, "--x", "40", "--y", "600", "--dry-run")
	if err != nil || rep.Placement.Source != "explicit" || rep.Created[0].X != 40 || rep.Created[0].Y != 588 {
		t.Fatalf("err=%v placement=%+v first=%+v", err, rep.Placement, rep.Created[0])
	}
	cfg, stop := h.serve()
	defer stop()
	var out bytes.Buffer
	cmd := newSchCmd(cfg, &out, &out)
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"intent-annotate", "--intent", esp32IntentFixture, "--x", "40"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("--x without --y must fail")
	}
}
