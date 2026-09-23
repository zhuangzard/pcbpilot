package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

func schFrameFixture() (schFrameSpec, *workflow.SchModuleFrame, schFrameSurvey) {
	f := schFrameSpec{ID: "POWER", Title: "POWER", Rect: layoutBBox{MinX: 100, MinY: 100, MaxX: 400, MaxY: 300}, TitleX: 110, TitleY: 290, FontSize: 20, Color: "#AA00AA", LineType: 1}
	r := &workflow.SchModuleFrame{RectangleID: "rect", TextID: "text", PlanHash: f.hash()}
	s := schFrameSurvey{Rectangles: map[string]map[string]any{"rect": {"id": "rect", "x": 100.0, "y": 300.0, "width": 300.0, "height": 200.0, "rotation": 0.0, "cornerRadius": 0.0, "color": "#aa00aa", "lineWidth": 1.0, "lineType": 1.0, "fillStyle": "None"}}, Texts: map[string]map[string]any{"text": {"id": "text", "content": "POWER", "x": 110.0, "y": 290.0, "rotation": 0.0, "fontSize": 20.0, "alignMode": 1.0, "color": "#AA00AA", "bold": false, "italic": false, "underLine": false, "bbox": map[string]any{"minX": 110.0, "minY": 270.0, "maxX": 180.0, "maxY": 290.0}}}}
	return f, r, s
}

func TestSchFrameReadbackIsStrict(t *testing.T) {
	f, r, s := schFrameFixture()
	if err := matchSchFrame(f, r, s); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(schFrameSurvey)
	}{
		{"solid instead of dashed", func(s schFrameSurvey) { s.Rectangles["rect"]["lineType"] = 0.0 }},
		{"missing rotation", func(s schFrameSurvey) { delete(s.Rectangles["rect"], "rotation") }},
		{"wrong frame position", func(s schFrameSurvey) { s.Rectangles["rect"]["y"] = 100.0 }},
		{"wrong frame color", func(s schFrameSurvey) { s.Rectangles["rect"]["color"] = "#000000" }},
		{"wrong title color", func(s schFrameSurvey) { s.Texts["text"]["color"] = "#000000" }},
		{"wrong font unit", func(s schFrameSurvey) { s.Texts["text"]["fontSize"] = 0.2 }},
		{"missing font", func(s schFrameSurvey) { delete(s.Texts["text"], "fontSize") }},
		{"wrong text", func(s schFrameSurvey) { s.Texts["text"]["content"] = "OLD" }},
		{"wrong rendered alignment", func(s schFrameSurvey) { s.Texts["text"]["bbox"].(map[string]any)["minX"] = 120.0 }},
		{"no bbox", func(s schFrameSurvey) { delete(s.Texts["text"], "bbox") }},
		{"overflow text", func(s schFrameSurvey) { s.Texts["text"]["bbox"].(map[string]any)["maxX"] = 450.0 }},
		{"missing frame", func(s schFrameSurvey) { delete(s.Rectangles, "rect") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, r, s := schFrameFixture()
			tc.change(s)
			if matchSchFrame(f, r, s) == nil {
				t.Fatal("bad/missing field accepted as verified")
			}
		})
	}
}

func TestSchFrameNativeNormalizationStillProvesAppearance(t *testing.T) {
	f, r, s := schFrameFixture()
	s.Rectangles["rect"]["fillStyle"] = nil
	s.Rectangles["rect"]["fillColor"] = "none"
	s.Texts["text"]["alignMode"] = 2.0
	if err := matchSchFrame(f, r, s); err != nil {
		t.Fatal(err)
	}
	s.Rectangles["rect"]["fillColor"] = nil
	if err := matchSchFrame(f, r, s); err == nil {
		t.Fatal("unspecified default fill must not be accepted as transparent")
	}
}

func TestSchFrameLostResponseNeedsUniqueNewPair(t *testing.T) {
	f, _, s := schFrameFixture()
	pending := &workflow.SchModuleFrame{PlanHash: f.hash(), Pending: true, BeforeRectangles: []string{"user-rect"}, BeforeTexts: []string{"user-text"}}
	r, err := recoverSchFrame(f, pending, s)
	if err != nil || r.RectangleID != "rect" || r.TextID != "text" || r.Pending {
		t.Fatalf("recover landed exact pair: %+v %v", r, err)
	}
	pending.BeforeRectangles = append(pending.BeforeRectangles, "rect")
	if _, err = recoverSchFrame(f, pending, s); err == nil {
		t.Fatal("must not adopt pre-existing user graphics")
	}
	pending.BeforeRectangles = nil
	s.Rectangles["second"] = s.Rectangles["rect"]
	if _, err = recoverSchFrame(f, pending, s); err == nil {
		t.Fatal("ambiguous writes must not be adopted")
	}
	delete(s.Rectangles, "second")
	delete(s.Texts, "text")
	if _, err = recoverSchFrame(f, pending, s); err == nil {
		t.Fatal("partial write must not be verified")
	}
}

func TestSchFrameParserAcceptsLayoutAndRejectsTypos(t *testing.T) {
	f, _, _ := schFrameFixture()
	p := schFrameDocument{SchemaVersion: 1, DocumentID: "doc", Frames: []schFrameSpec{f}}
	raw, _ := json.Marshal(p)
	if _, err := parseSchFrameDocument(raw); err != nil {
		t.Fatal(err)
	}
	var layout map[string]any
	_ = json.Unmarshal(raw, &layout)
	layout["placements"] = []any{}
	layout["wires"] = []any{}
	layout["flags"] = []any{}
	layout["expectedPinNets"] = map[string]any{}
	raw, _ = json.Marshal(layout)
	if _, err := parseSchFrameDocument(raw); err != nil {
		t.Fatal(err)
	}
	layout["frames"].([]any)[0].(map[string]any)["fontSiz"] = 20
	raw, _ = json.Marshal(layout)
	if _, err := parseSchFrameDocument(raw); err == nil {
		t.Fatal("unknown style field must fail")
	}
	if _, err := parseSchFrameSurvey(map[string]any{"texts": []any{}}); err == nil {
		t.Fatal("absent rectangles must not mean empty")
	}
}

func TestRequireFullExecutionCannotSkipOrRetarget(t *testing.T) {
	pb := playbook{Version: 1, RequireFullExecution: true, Meta: playbookMeta{Name: "guarded frames", Doc: "doc", Project: "project"}, Steps: []playbookStep{{ID: "read", Action: "schematic.components.list"}}}
	raw, _ := json.Marshal(pb)
	path := filepath.Join(t.TempDir(), "apply.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"--resume"}, {"--from", "read"}, {"--to", "read"}, {"--doc", "other"}, {"--project", "other"}} {
		var out, errs bytes.Buffer
		argv := append([]string{"sch", "apply", path, "--dry-run"}, args...)
		if code := Run(argv, &out, &errs); code == 0 || !(strings.Contains(errs.String(), "full execution") || strings.Contains(errs.String(), "guarded plan target")) {
			t.Fatalf("%v should stop offline: %d %s", args, code, errs.String())
		}
	}
}

func TestSchFrameAdapterValidatesPlannedTitleOccupancy(t *testing.T) {
	f, r, s := schFrameFixture()
	f.TitleLayout = &schFrameTitleLayout{Width: 75, Height: 20, Clearance: 5, Obstacles: []layoutBBox{{110, 200, 200, 250}}}
	p := schFrameDocument{SchemaVersion: 1, DocumentID: "doc", Frames: []schFrameSpec{f}}
	raw, _ := json.Marshal(p)
	if _, err := parseSchFrameDocument(raw); err != nil {
		t.Fatal(err)
	}
	if err := matchSchFrame(f, r, s); err != nil {
		t.Fatal(err)
	}
	s.Texts["text"]["bbox"].(map[string]any)["maxX"] = 186.0
	if err := matchSchFrame(f, r, s); err == nil {
		t.Fatal("adapter accepted native title outside its reserved envelope")
	}
	f.TitleLayout.Obstacles = append(f.TitleLayout.Obstacles, layoutBBox{120, 275, 160, 280})
	raw, _ = json.Marshal(p)
	if _, err := parseSchFrameDocument(raw); err == nil {
		t.Fatal("colliding title data must fail before an API call")
	}
}

func TestPowerLayoutConsumesSavedTitleMetrics(t *testing.T) {
	fixture := powerLayoutFixture(t, 0, 0, 20, 0)
	fixture["titleMetrics"] = map[string]any{"title": "POWER / AMS1117-3.3", "fontSize": 20, "width": 189.022171, "height": 20}
	plan, err := planPowerLayout(powerLayoutBytes(t, fixture), powerLayoutTestOptions())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Frames[0].TitleLayout.Width != 190 {
		t.Fatal("snapshot native title measurement was ignored")
	}
	raw, _ := json.Marshal(plan)
	if _, err := parseSchFrameDocument(raw); err != nil {
		t.Fatal(err)
	}
	fixture["titleMetrics"].(map[string]any)["title"] = "different title"
	if _, err := planPowerLayout(powerLayoutBytes(t, fixture), powerLayoutTestOptions()); err == nil {
		t.Fatal("mismatched title measurement accepted")
	}
}
