package app

import (
	"bytes"
	"strings"
	"testing"
)

func designatorExportFixture() map[string]any {
	return map[string]any{"documentId": "page-1", "count": float64(1), "designators": []any{map[string]any{
		"id": "attr-1", "parentId": "part-1", "key": "Designator", "value": "R1", "visible": true,
		"bbox":   map[string]any{"minX": float64(10), "minY": float64(20), "maxX": float64(16), "maxY": float64(28)},
		"source": schDesignatorBBoxSource,
	}}}
}

func TestDesignatorGeometryExportKeepsOfficialMeasurement(t *testing.T) {
	got, err := parseSchDesignatorGeometryExport(designatorExportFixture(), "page-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 1 || got.Designators[0].ParentID != "part-1" || got.Designators[0].Value != "R1" || got.Designators[0].BBox.MaxY != 28 || got.Designators[0].Source != schDesignatorBBoxSource {
		t.Fatalf("incomplete source measurement: %+v", got)
	}
}

func TestDesignatorGeometryExportFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong page", func(v map[string]any) { v["documentId"] = "page-2" }},
		{"missing inventory", func(v map[string]any) { delete(v, "designators") }},
		{"count mismatch", func(v map[string]any) { v["count"] = float64(2) }},
		{"hidden", func(v map[string]any) { v["designators"].([]any)[0].(map[string]any)["visible"] = false }},
		{"partial bbox", func(v map[string]any) {
			v["designators"].([]any)[0].(map[string]any)["bbox"] = map[string]any{"maxX": float64(16), "maxY": float64(28)}
		}},
		{"zero bbox", func(v map[string]any) {
			v["designators"].([]any)[0].(map[string]any)["bbox"] = map[string]any{"minX": float64(10), "minY": float64(20), "maxX": float64(10), "maxY": float64(28)}
		}},
		{"missing source", func(v map[string]any) { delete(v["designators"].([]any)[0].(map[string]any), "source") }},
		{"duplicate parent", func(v map[string]any) {
			rows := v["designators"].([]any)
			clone := map[string]any{"id": "attr-2", "parentId": "part-1", "key": "Designator", "value": "R2", "visible": true, "bbox": rows[0].(map[string]any)["bbox"], "source": schDesignatorBBoxSource}
			v["designators"] = append(rows, clone)
			v["count"] = float64(2)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := designatorExportFixture()
			tc.mutate(v)
			if got, err := parseSchDesignatorGeometryExport(v, "page-1"); err == nil || got != nil {
				t.Fatalf("invalid export accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestDesignatorGeometryCLIHelpAndRegistration(t *testing.T) {
	var out bytes.Buffer
	root := newSchCmd(nil, &out, &out)
	root.SetOut(&out)
	root.SetErr(&out)
	var commandFound bool
	for _, child := range root.Commands() {
		if child.Name() == "designator-geometry" {
			commandFound = true
			break
		}
	}
	if !commandFound {
		t.Fatal("sch designator-geometry is not registered")
	}
	root.SetArgs([]string{"designator-geometry", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, phrase := range []string{"--out", "--project", "--doc", "selected schematic page", "textBboxes"} {
		if !strings.Contains(out.String(), phrase) {
			t.Fatalf("help missing %q: %s", phrase, out.String())
		}
	}
}
