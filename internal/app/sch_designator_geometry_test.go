package app

import (
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
)

func TestCeshiDesignatorGeometryRegression(t *testing.T) {
	b, err := os.ReadFile("testdata/ceshi-designator-geometry.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Components []any          `json:"components"`
		Visual     map[string]any `json:"visual"`
		Excluded   []any          `json:"excludedAttributes"`
	}
	if err = json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	c, err := parseLayoutComps(map[string]any{"components": f.Components})
	if err != nil {
		t.Fatal(err)
	}
	// Real long model names can overlap/cross frames; they must have no effect.
	f.Visual["designators"] = append(f.Visual["designators"].([]any), f.Excluded...)
	if fs := schVisualSurveyFindings(f.Visual, c, nil); len(fs) != 0 {
		t.Fatalf("real valid labels/property exemption: %+v", fs)
	}
	for _, row := range f.Visual["designators"].([]any) {
		m := row.(map[string]any)
		if m["value"] == "Q2" {
			m["bbox"] = map[string]any{"minX": 1135.0, "minY": 630.0, "maxX": 1145.0, "maxY": 638.0}
		}
	}
	fs := schVisualSurveyFindings(f.Visual, c, nil)
	if len(fs) != 1 || fs[0].Type != "designator-out-of-frame" || fs[0].Designator != "Q2" {
		t.Fatalf("negative control escaped: %+v", fs)
	}
}

func TestDesignatorOwnOutlineToleranceIsBounded(t *testing.T) {
	for _, tc := range []struct {
		own   bool
		depth float64
		want  bool
	}{{true, 0.5, false}, {true, 0.51, true}, {false, 0.5, true}} {
		s, c, d := designatorGeometryFixture()
		d[0].BBox = &layoutBBox{MinX: 40, MinY: 60 - tc.depth, MaxX: 49, MaxY: 68}
		if !tc.own {
			other := c[0]
			other.ID = "other"
			other.ComponentType = "netport"
			c = append(c, other)
		}
		fs := schDesignatorFindings(s, d, c, nil)
		if (len(fs) > 0) != tc.want {
			t.Fatalf("%+v got %+v", tc, fs)
		}
	}
}

func TestDesignatorPartialBBoxIsUnverified(t *testing.T) {
	v := map[string]any{"designators": []any{map[string]any{"id": "d", "parentId": "q", "key": "Designator", "bbox": map[string]any{"maxX": 10.0, "maxY": 10.0}}}}
	d, err := parseSchDesignatorGeometry(v)
	if err != nil {
		t.Fatal(err)
	}
	if len(d) != 1 || d[0].BBox != nil {
		t.Fatal("missing bbox fields became zero coordinates")
	}
}

func designatorGeometryFixture() (schFrameSurvey, []layoutComp, []schDesignatorGeometry) {
	v := true
	return schFrameSurvey{Rectangles: map[string]map[string]any{"zone": {"x": 0.0, "y": 100.0, "width": 100.0, "height": 100.0, "rotation": 0.0, "color": "#AA00AA"}}},
		[]layoutComp{{ID: "q", Designator: "Q1", ComponentType: "part", BBox: &layoutBBox{MinX: 40, MinY: 40, MaxX: 50, MaxY: 60}}},
		[]schDesignatorGeometry{{ID: "ref", ParentID: "q", Key: "Designator", Value: "Q1", Visible: &v, BBox: &layoutBBox{MinX: 55, MinY: 50, MaxX: 65, MaxY: 58}}}
}

func TestDesignatorGeometryContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*schFrameSurvey, *[]layoutComp, *[]schDesignatorGeometry)
		want   string
	}{
		{"clean", func(*schFrameSurvey, *[]layoutComp, *[]schDesignatorGeometry) {}, ""},
		{"model excluded even without geometry", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) {
			*d = append(*d, schDesignatorGeometry{Key: "Name", Value: "S8050 J3Y(RANGE:200-350)", ParentID: "q"})
		}, ""},
		{"property overlapping everything excluded", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) {
			*d = append(*d, schDesignatorGeometry{ID: "prop", Key: "Value", ParentID: "q", BBox: &layoutBBox{MinX: -999, MinY: -999, MaxX: 999, MaxY: 999}})
		}, ""},
		{"missing inventory", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { *d = nil }, "designator-geometry-unavailable"},
		{"hidden", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { v := false; (*d)[0].Visible = &v }, "designator-geometry-unavailable"},
		{"unknown visibility", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { (*d)[0].Visible = nil }, "designator-geometry-unavailable"},
		{"unknown bbox", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { (*d)[0].BBox = nil }, "designator-geometry-unavailable"},
		{"zero bbox", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { (*d)[0].BBox = &layoutBBox{} }, "designator-geometry-unavailable"},
		{"nan bbox", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { (*d)[0].BBox.MinX = math.NaN() }, "designator-geometry-unavailable"},
		{"identity mismatch", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { (*d)[0].Value = "Q2" }, "designator-geometry-unavailable"},
		{"wrong parent", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { (*d)[0].ParentID = "other" }, "designator-geometry-unavailable"},
		{"duplicate id", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { *d = append(*d, (*d)[0]) }, "designator-geometry-unavailable"},
		{"outside", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) {
			(*d)[0].BBox.MinX = 101
			(*d)[0].BBox.MaxX = 110
		}, "designator-out-of-frame"},
		{"body collision", func(_ *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) { (*d)[0].BBox.MinX = 45 }, "designator-overlap"},
		{"wrong frame cannot legalize label", func(s *schFrameSurvey, _ *[]layoutComp, d *[]schDesignatorGeometry) {
			s.Rectangles["other"] = map[string]any{"x": 110.0, "y": 100.0, "width": 100.0, "height": 100.0, "rotation": 0.0, "color": "#AA00AA"}
			(*d)[0].BBox.MinX = 120
			(*d)[0].BBox.MaxX = 130
		}, "designator-out-of-frame"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, c, d := designatorGeometryFixture()
			tc.change(&s, &c, &d)
			fs := schDesignatorFindings(s, d, c, nil)
			if tc.want == "" {
				if len(fs) > 0 {
					t.Fatalf("false positive: %+v", fs)
				}
				return
			}
			for _, f := range fs {
				if f.Type == tc.want {
					if !checkLevelBlocks(f.Level, true) {
						t.Fatal("not gated")
					}
					return
				}
			}
			t.Fatalf("want %s, got %+v", tc.want, fs)
		})
	}
}

func TestDesignatorCollisionSurfaces(t *testing.T) {
	s, c, d := designatorGeometryFixture()
	c = append(c, layoutComp{ID: "port", ComponentType: "netport", BBox: d[0].BBox})
	s.Texts = map[string]map[string]any{"title": {"bbox": map[string]any{"minX": 55.0, "minY": 50.0, "maxX": 65.0, "maxY": 58.0}}}
	w := []schGroupWire{{ID: "wire", Points: []float64{40, 54, 80, 54}}}
	fs := schDesignatorFindings(s, d, c, w)
	if len(fs) != 3 {
		t.Fatalf("want marker/text/wire collisions, got %+v", fs)
	}
}

func TestDesignatorObservedFlatSegmentsDoNotInventConnector(t *testing.T) {
	s, c, d := designatorGeometryFixture()
	w := []schGroupWire{{
		ID:     "wire",
		Points: []float64{30, 30, 40, 30, 80, 80, 90, 80},
		ObservedSegments: [][4]float64{
			{30, 30, 40, 30},
			{80, 80, 90, 80},
		},
	}}
	if fs := schDesignatorFindings(s, d, c, w); len(fs) != 0 {
		t.Fatalf("independent flat segments gained a phantom diagonal: %+v", fs)
	}
	w[0].ObservedSegments = append(w[0].ObservedSegments, [4]float64{40, 54, 80, 54})
	fs := schDesignatorFindings(s, d, c, w)
	if len(fs) != 1 || fs[0].Type != "designator-wire-overlap" {
		t.Fatalf("real observed segment crossing escaped: %+v", fs)
	}
}

func TestDesignatorDiagonalWire(t *testing.T) {
	b := layoutBBox{MinX: 0, MinY: 0, MaxX: 10, MaxY: 10}
	for _, tc := range []struct {
		name string
		p    [4]float64
		want bool
	}{
		{"diagonal crossing", [4]float64{-5, -5, 15, 15}, true},
		{"diagonal misses", [4]float64{-5, 15, 15, 35}, false},
		{"wire along bottom edge", [4]float64{-5, 0, 15, 0}, true},
		{"wire along left edge", [4]float64{0, -5, 0, 15}, true},
		{"wire endpoint on edge", [4]float64{-5, 5, 0, 5}, true},
		{"wire endpoint on corner", [4]float64{-5, -5, 0, 0}, true},
		{"diagonal tangent to corner", [4]float64{-5, 5, 5, -5}, true},
		{"parallel just outside", [4]float64{-5, -0.001, 15, -0.001}, false},
		{"endpoint outside label", [4]float64{-5, 15, 0, 15}, false},
		{"zero length inside", [4]float64{5, 5, 5, 5}, false},
		{"zero length on corner", [4]float64{0, 0, 0, 0}, false},
		{"vertical crossing", [4]float64{5, -5, 5, 15}, true},
		{"inside", [4]float64{2, 2, 8, 8}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := schSegmentCrossesBox(tc.p[0], tc.p[1], tc.p[2], tc.p[3], b); got != tc.want {
				t.Fatalf("%v got %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestDesignatorWireBoundaryFinding(t *testing.T) {
	s, c, d := designatorGeometryFixture()
	for _, tc := range []struct {
		name string
		line []float64
		want bool
	}{
		{"touches label corner", []float64{45, 40, 55, 50}, true},
		{"follows label edge", []float64{55, 50, 65, 50}, true},
		{"ends at part body away from label", []float64{30, 50, 40, 50}, false},
		{"zero length within label", []float64{60, 54, 60, 54}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := schDesignatorFindings(s, d, c, []schGroupWire{{ID: "wire", Points: tc.line}})
			found := false
			for _, f := range fs {
				if f.Type == "designator-wire-overlap" {
					found = true
				}
			}
			if found != tc.want {
				t.Fatalf("line %v finding=%v, want %v: %+v", tc.line, found, tc.want, fs)
			}
		})
	}
}

func TestDesignatorSurveyReadsPerParentAndFiltersBeforeBBox(t *testing.T) {
	_, c, _ := designatorGeometryFixture()
	js := buildSchVisualSurveyJS(c)
	if !strings.Contains(js, "sch_PrimitiveAttribute.getAll(parentId)") {
		t.Fatal("must read scoped inventory")
	}
	filter := strings.Index(js, "a.getState_Key() !== 'Designator'")
	measure := strings.LastIndex(js, "getPrimitivesBBox([id])});")
	if filter < 0 || measure < filter {
		t.Fatal("must exclude property text before measuring")
	}
}
