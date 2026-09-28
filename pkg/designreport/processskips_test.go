package designreport

import (
	"strings"
	"testing"
)

func TestProcessSkipsReplaceMissing(t *testing.T) {
	in := &Inputs{Project: "demo", ProcessSkips: &ProcessSkips{Template: "quick-proto", Source: "pcbpilot.project.json",
		Steps:    []Missing{{Section: "P10.5 设计后仿真验证", Reason: "客户只要打样"}},
		Sections: []Missing{{Section: "6A 设计后仿真验证", Reason: "客户只要打样"}}}}
	r := Build(in)
	for _, m := range r.Missing {
		if strings.HasPrefix(m.Section, "6A") {
			t.Fatalf("skipped section still listed as missing: %+v", m)
		}
	}
	if len(r.Missing) == 0 {
		t.Fatal("other sections without inputs must still be missing")
	}
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(md), "按项目流程模板跳过") || !strings.Contains(string(md), "客户只要打样") {
		t.Fatal("markdown must list the skipped steps with reasons")
	}
	html, err := RenderHTML(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(html), "11.5 按项目流程模板跳过") {
		t.Fatal("html must list the skipped steps")
	}
}
