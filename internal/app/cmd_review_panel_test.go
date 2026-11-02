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
	if _, ok := lastVerdictJSON(`echo: {"verdict":"pass|fail","requirements":[],"blocking":[],"advisory":[]} then nothing`); ok {
		t.Fatal("the prompt's template was read as a verdict")
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
	// Raw replies are reviewer_<name>.md — never <name>.md, so a reviewer named
	// claude cannot collide with a CLAUDE.md on a case-insensitive filesystem.
	if _, err := os.Stat(filepath.Join(dir, "out", "reviewer_a.md")); err != nil {
		t.Fatal(err)
	}
	if got := reviewerRawFile("claude"); strings.EqualFold(got, "CLAUDE.md") {
		t.Fatalf("raw reply file %q collides with CLAUDE.md", got)
	}
	if _, err := runReviewPanel("design", nil, nil, []string{"a"}, dir, time.Minute, 0, nil, io.Discard); err == nil {
		t.Fatal("ran without requirements")
	}
}

// field report 2026-10-15: a 537 KB prompt passed as an argument failed every
// reviewer on Windows ("The filename or extension is too long"). The prompt
// must travel by stdin or file; the command line must stay far below the
// 8 191-char limit that applies through a .cmd shim (codex.cmd).
func TestReviewerInvocationKeepsLargePromptOffCommandLine(t *testing.T) {
	prompt := strings.Repeat("requirement R-1: the board shall … ", 1500) // ~54 KB > 32 KB
	if len(prompt) <= 32*1024 {
		t.Fatalf("test prompt is only %d bytes", len(prompt))
	}
	for _, name := range []string{"codex", "kimi", "claude"} {
		call, err := reviewerInvocation(name, filepath.Join("C:", "tmp", "pcbpilot-"+name), prompt)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		cmdLine := len(call.Bin)
		for _, a := range call.Args {
			cmdLine += len(a) + 1
			if strings.Contains(a, "requirement R-1") {
				t.Fatalf("%s: prompt text leaked into argv", name)
			}
		}
		if cmdLine > 2048 {
			t.Fatalf("%s: command line is %d chars", name, cmdLine)
		}
		if call.Stdin != prompt && call.PromptFile == "" {
			t.Fatalf("%s: prompt is delivered neither by stdin nor by file", name)
		}
	}
	// kimi exits with "Cannot combine --prompt with --auto." (checked 2026-10-15).
	if k, _ := reviewerInvocation("kimi", "d", prompt); strings.Contains(strings.Join(k.Args, " "), "--auto") {
		t.Fatalf("kimi -p must not be combined with --auto: %v", k.Args)
	}
	if _, err := reviewerInvocation("nope", "", prompt); err == nil {
		t.Fatal("unknown reviewer accepted")
	}
}

// field report 2026-10-19: a review CLI read confidential files from the project
// working directory. Every reviewer is confined to its private prompt-only
// directory: codex is pinned there with -C and a read-only sandbox, claude
// gets no tools, kimi only that directory via --add-dir.
func TestReviewerInvocationIsConfinedToPrivateDir(t *testing.T) {
	const dir = "PRIVATE-DIR"
	codex, _ := reviewerInvocation("codex", dir, "p")
	if a := strings.Join(codex.Args, " "); !strings.Contains(a, "-C "+dir) || !strings.Contains(a, "-s read-only") {
		t.Errorf("codex not pinned to the private dir read-only: %v", codex.Args)
	}
	claude, _ := reviewerInvocation("claude", dir, "p")
	tools := -1
	for i, a := range claude.Args {
		if a == "--tools" && i+1 < len(claude.Args) && claude.Args[i+1] == "" {
			tools = i
		}
	}
	if tools < 0 {
		t.Errorf("claude reviewer must run with --tools \"\": %v", claude.Args)
	}
	kimi, _ := reviewerInvocation("kimi", dir, "p")
	for i, a := range kimi.Args {
		if a == "--add-dir" && (i+1 >= len(kimi.Args) || kimi.Args[i+1] != dir) {
			t.Errorf("kimi --add-dir must be the private dir: %v", kimi.Args)
		}
	}
}

// An input that changes while the reviewers run — here past the prompt's
// --max-bytes cut, so the prompt is the same — is not recorded as
// reviewed: the run fails and review.json is not written.
func TestRunReviewPanelInputChangedDuringReview(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "REQ.md")
	ev := filepath.Join(dir, "intent.json")
	head := "| R-01 | fuse |\n" + strings.Repeat("filler\n", 300)
	_ = os.WriteFile(req, []byte(head+"| R-09 | 1 kg |\n"), 0o644)
	_ = os.WriteFile(ev, []byte("intent-evidence"), 0o644)
	old := reviewerRunners
	defer func() { reviewerRunners = old }()
	reviewerRunners = map[string]reviewerRun{
		"a": func(ctx context.Context, p string) (string, error) {
			// the tail changes while this reviewer works
			_ = os.WriteFile(req, []byte(head+"| R-09 | 9 kg |\n"), 0o644)
			return `{"verdict":"pass","requirements":[{"id":"R-01","status":"met","evidence":"F1"}],"blocking":[],"advisory":[]}`, nil
		},
	}
	out := filepath.Join(dir, "out")
	_, err := runReviewPanel("design", []string{req}, []string{ev}, []string{"a"}, out, time.Minute, 500, nil, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "changed during the review") || !strings.Contains(err.Error(), "REQ.md differs") {
		t.Fatalf("want a refusal naming REQ.md, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "review.json")); !os.IsNotExist(err) {
		t.Fatalf("review.json written for changed inputs: %v", err)
	}
	// Unchanged inputs: recorded, with the hashes of the bytes reviewed.
	_ = os.WriteFile(req, []byte(head+"| R-09 | 1 kg |\n"), 0o644)
	reviewerRunners = map[string]reviewerRun{"a": func(ctx context.Context, p string) (string, error) {
		return `{"verdict":"pass","requirements":[{"id":"R-01","status":"met","evidence":"F1"}],"blocking":[],"advisory":[]}`, nil
	}}
	rec, err := runReviewPanel("design", []string{req}, []string{ev}, []string{"a"}, out, time.Minute, 500, nil, io.Discard)
	if err != nil || len(rec.FilesSHA256) != 2 {
		t.Fatalf("%v %+v", err, rec)
	}
}
