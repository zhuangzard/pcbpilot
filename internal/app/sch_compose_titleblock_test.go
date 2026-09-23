package app

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestComposeTitleBlockSourceCompilesBeforeStrictGate(t *testing.T) {
	fields := map[string]string{
		"Name": "Power and USB", "Drawed": "Design team", "Description": "Input and regulator",
	}
	src := composeFixture(1)
	src.TitleBlock = fields
	plan, err := planSchComposition(src)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.TitleBlock, fields) {
		t.Fatalf("page title block was lost from composition source: %+v", plan.TitleBlock)
	}
	for _, matching := range []bool{false, true} {
		p, before := composeApplyFixture(t, matching)
		p.TitleBlock = plan.TitleBlock
		pb, err := schCompositionPlaybook(p, composeApplyBytes(t, before), !matching)
		if err != nil {
			t.Fatal(err)
		}
		writeIndex, write := composeStep(t, pb, "apply-page-titleblock")
		gateIndex, _ := composeStep(t, pb, "strict-schematic-gate")
		saveIndex, _ := composeStep(t, pb, "save-composition")
		if write.Run != "sch titleblock" || writeIndex >= gateIndex || gateIndex >= saveIndex {
			t.Fatalf("title block must use the guarded CLI before gate/save: %+v", write)
		}
		var patch map[string]map[string]string
		if err := json.Unmarshal([]byte(write.Flags["data"].(string)), &patch); err != nil {
			t.Fatal(err)
		}
		for key, value := range fields {
			if patch[key]["value"] != value {
				t.Fatalf("titleBlock.%s source value changed: %+v", key, patch)
			}
		}
		if len(patch) != len(fields) {
			t.Fatalf("unexpected title block fields: %+v", patch)
		}
	}
}

func TestComposeTitleBlockRejectsUnsafeOrEmptySource(t *testing.T) {
	for name, fields := range map[string]map[string]string{
		"empty object":  {},
		"empty value":   {"Name": "   "},
		"derived field": {"@Project Name": "project"},
		"structure":     {"Border": "1"},
		"paper size":    {"Width": "1170"},
		"nonexact key":  {" Name": "sheet"},
	} {
		t.Run(name, func(t *testing.T) {
			src := composeFixture(1)
			src.TitleBlock = fields
			if _, err := planSchComposition(src); err == nil || !strings.Contains(err.Error(), "titleBlock") {
				t.Fatalf("unsafe titleBlock accepted: %v", err)
			}
		})
	}
}

func TestComposeLegacySourceDoesNotRewriteTitleBlock(t *testing.T) {
	p, before := composeApplyFixture(t, true)
	pb, err := schCompositionPlaybook(p, composeApplyBytes(t, before), false)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range pb.Steps {
		if step.ID == "apply-page-titleblock" || step.Run == "sch titleblock" {
			t.Fatal("legacy composition unexpectedly edits title block")
		}
	}
}

func TestComposeSelectedPageKeepsTitleBlockFromCompositionSource(t *testing.T) {
	src, page := composePreplacedFixture(t)
	src.TitleBlock = map[string]string{"Name": "Selected page"}
	plan, err := planSchCompositionWithPage(src, &page)
	if err != nil {
		t.Fatal(err)
	}
	if plan.TitleBlock["Name"] != "Selected page" {
		t.Fatalf("selected page lost per-page source metadata: %+v", plan.TitleBlock)
	}
	plan.TitleBlock["Name"] = "changed plan"
	if src.TitleBlock["Name"] != "Selected page" {
		t.Fatal("planning reused the mutable source titleBlock map")
	}
}
