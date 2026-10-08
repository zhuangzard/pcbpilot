package app

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLastVerdictJSON(t *testing.T) {
	out := "• thinking {not json}\n• Here: ```json\n{\"verdict\":\"fail\",\"requirements\":[{\"id\":\"R-02\",\"status\":\"not_met\",\"evidence\":\"drop 0.4 V {x}\"}],\"blocking\":[\"a\"],\"advisory\":[]}\n```\nTo resume: kimi -r s"
	v, ok := lastVerdictJSON(out)
	if !ok || v.Verdict != "fail" || len(v.Requirements) != 1 || v.Requirements[0].Evidence != "drop 0.4 V {x}" {
		t.Fatalf("%v %+v", ok, v)
	}
	if _, ok := lastVerdictJSON("no json here"); ok {
		t.Fatal("parsed nothing")
	}
}

// The gate passes only when every reviewer answered with no not_met / blocking.
func TestReviewGate(t *testing.T) {
	ok := reviewVerdict{Reviewer: "codex", Verdict: "pass", Requirements: []reviewRequirement{{ID: "R-01", Status: "met"}}}
	k := ok
	k.Reviewer = "kimi"
	c := ok
	c.Reviewer = "claude"
	if g := reviewGate([]reviewVerdict{ok, k, c}, []string{"codex", "kimi", "claude"}); !g.Pass {
		t.Fatalf("%+v", g)
	}
	bad := k
	bad.Requirements = []reviewRequirement{{ID: "R-02", Status: "not_met", Evidence: "0.4 V"}}
	g := reviewGate([]reviewVerdict{ok, bad, c}, []string{"codex", "kimi", "claude"})
	if g.Pass || !strings.Contains(strings.Join(g.Items, "\n"), "kimi: R-02 not met") {
		t.Fatalf("%+v", g)
	}
	if g := reviewGate([]reviewVerdict{ok, k}, []string{"codex", "kimi", "claude"}); g.Pass {
		t.Fatal("missing reviewer passed")
	}
	if g := reviewGate([]reviewVerdict{ok, k, {Reviewer: "claude", Error: "OAuth expired"}}, []string{"codex", "kimi", "claude"}); g.Pass {
		t.Fatal("errored reviewer passed")
	}
}

// End to end with fake reviewers: one passes, one fails a requirement.
func TestRunReviewPanelFake(t *testing.T) {
	old := reviewerRunners
	defer func() { reviewerRunners = old }()
	reviewerRunners = map[string]reviewerRun{
		"a": func(ctx context.Context, p string) (string, error) {
			if !strings.Contains(p, "R-01") || !strings.Contains(p, "intent-evidence") {
				return "prompt lacks inputs", nil
			}
			return `{"verdict":"pass","requirements":[{"id":"R-01","status":"met","evidence":"F1"}],"blocking":[],"advisory":[]}`, nil
		},
		"b": func(ctx context.Context, p string) (string, error) {
			return `{"verdict":"fail","requirements":[{"id":"R-01","status":"not_met","evidence":"no fuse"}],"blocking":[],"advisory":[]}`, nil
		},
	}
	dir := t.TempDir()
	req := filepath.Join(dir, "REQ.md")
	ev := filepath.Join(dir, "intent.json")
	_ = os.WriteFile(req, []byte("| R-01 | fuse |"), 0o644)
	_ = os.WriteFile(ev, []byte("intent-evidence"), 0o644)
	rec, err := runReviewPanel("design", []string{req}, []string{ev}, []string{"a", "b"}, filepath.Join(dir, "out"), time.Minute, 0, nil, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Gate.Pass || len(rec.Reviewers) != 2 || rec.Reviewers[0].Verdict != "pass" {
		t.Fatalf("%+v", rec)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "review.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := runReviewPanel("design", nil, nil, []string{"a"}, dir, time.Minute, 0, nil, io.Discard); err == nil {
		t.Fatal("ran without requirements")
	}
}
