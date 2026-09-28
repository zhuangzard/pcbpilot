package designreport

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// §6B is additional: present with a routed board, absent (and NOT counted as
// missing) without one; every other section keeps its place.
func TestAestheticsSectionAdditive(t *testing.T) {
	raw, err := os.ReadFile("../pcbauto/testdata/esp32-v05-fixed.routed.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseBoard(raw)
	if err != nil {
		t.Fatal(err)
	}
	r := Build(&Inputs{GeneratedAt: fixedTime, Project: "esp32", Board: b})
	a := r.Aesthetics
	if a == nil || a.Source != "board" || a.Profile != "balanced" || len(a.Metrics) != 18 || len(a.Priority) != 7 {
		t.Fatalf("aesthetics section %+v", a)
	}
	for _, m := range r.Missing {
		if strings.HasPrefix(m.Section, "6B") {
			t.Errorf("6B recorded as missing: %+v", m)
		}
	}
	md, err := RenderMarkdown(r)
	if err != nil {
		t.Fatal(err)
	}
	i6a, i6b, i7 := bytes.Index(md, []byte("## 6A ")), bytes.Index(md, []byte("## 6B ")), bytes.Index(md, []byte("## 7 验证状态"))
	if !(i6a > 0 && i6a < i6b && i6b < i7) {
		t.Fatalf("section order 6A %d, 6B %d, 7 %d", i6a, i6b, i7)
	}
	if !bytes.Contains(md, []byte("只报告")) || !bytes.Contains(md, []byte("7 / 7")) {
		t.Error("6B does not state report-only / lowest tier")
	}
	h, err := RenderHTML(r, Charts(r))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(h, []byte(`id="s6b"`)) || !bytes.Contains(h, []byte(`id="s7"`)) {
		t.Error("html lacks 6B or 7")
	}
	// Without a board the section is absent and adds no Missing entry.
	e1 := Build(&Inputs{GeneratedAt: fixedTime, Project: "empty"})
	if e1.Aesthetics != nil {
		t.Error("6B invented without a board")
	}
	for _, m := range e1.Missing {
		if strings.HasPrefix(m.Section, "6B") {
			t.Error("empty build counts 6B as missing (would change existing verdicts)")
		}
	}
}
