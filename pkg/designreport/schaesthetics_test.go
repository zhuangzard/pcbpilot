package designreport

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

// §6C is additional: present with --sch-snapshot pages, absent (and NOT
// counted as missing) without; the verdict never moves.
func TestSchAestheticsSectionAdditive(t *testing.T) {
	raw, err := os.ReadFile("../schaes/testdata/ams1117-lib-layout.json")
	if err != nil {
		t.Fatal(err)
	}
	snap, err := schaes.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	base := Build(&Inputs{GeneratedAt: fixedTime, Project: "p"})
	with := Build(&Inputs{GeneratedAt: fixedTime, Project: "p", SchPages: []SchPage{{Label: "POWER", Snapshot: snap}}})
	s := with.SchAesthetics
	if s == nil || len(s.Pages) != 1 || s.Profile != "balanced" || s.Weight != 0 || len(s.Pages[0].Metrics) != 18 || len(s.Priority) != 3 {
		t.Fatalf("section %+v", s)
	}
	if base.SchAesthetics != nil {
		t.Fatal("6C invented without snapshots")
	}
	if base.Verdict.Status != with.Verdict.Status || len(base.Missing) != len(with.Missing) {
		t.Fatalf("6C changed the verdict or the missing list: %v/%d vs %v/%d", base.Verdict.Status, len(base.Missing), with.Verdict.Status, len(with.Missing))
	}
	for _, m := range base.Missing {
		if strings.HasPrefix(m.Section, "6C") {
			t.Fatal("6C counted as missing")
		}
	}
	md, err := RenderMarkdown(with)
	if err != nil {
		t.Fatal(err)
	}
	i6b, i6c, i7 := bytes.Index(md, []byte("## 6B ")), bytes.Index(md, []byte("## 6C ")), bytes.Index(md, []byte("## 7 验证状态"))
	if !(i6b > 0 && i6b < i6c && i6c < i7) || !bytes.Contains(md, []byte("| POWER | lib-layout |")) {
		t.Fatalf("section order 6B %d 6C %d 7 %d", i6b, i6c, i7)
	}
	h, err := RenderHTML(with, Charts(with))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(h, []byte(`id="s6c"`)) {
		t.Fatal("html lacks 6C")
	}
}
