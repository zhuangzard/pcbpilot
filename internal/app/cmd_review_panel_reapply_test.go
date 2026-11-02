package app

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --reapply-waivers recomputes the gate from the recorded verdicts: a waiver
// covering every item passes it, a partial set does not, and changed evidence
// is refused (the verdicts would no longer belong to the input).
func TestReapplyReviewWaivers(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "REQ.md")
	ev := filepath.Join(dir, "ev.json")
	if err := os.WriteFile(req, []byte("R1 the box shall be blue\nR2 the box shall be small\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ev, []byte(`{"color":"red"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := reviewPrompt("design", map[string]string{"REQ.md": "R1 the box shall be blue\nR2 the box shall be small\n"},
		map[string]string{"ev.json": `{"color":"red"}`}, 120000)
	files, err := reviewFilesSHA([]string{req}, []string{ev})
	if err != nil {
		t.Fatal(err)
	}
	rec := reviewRecord{Stage: "design", InputSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(prompt))), FilesSHA256: files,
		Reviewers: []reviewVerdict{{Reviewer: "codex", Verdict: "fail",
			Requirements: []reviewRequirement{{ID: "R1", Status: "not_met", Evidence: "red"}, {ID: "R2", Status: "met"}},
			Blocking:     []string{"enclosure not designed"}}}}
	out := filepath.Join(dir, "review")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	blob, _ := json.Marshal(rec)
	if err := os.WriteFile(filepath.Join(out, "review.json"), blob, 0o644); err != nil {
		t.Fatal(err)
	}
	w1 := []gateWaiver{{Gate: "design-review", Match: "R1 not met", Reason: "colour change approved", By: "t"}}
	got, err := reapplyReviewWaivers("design", []string{req}, []string{ev}, out, 120000, w1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Gate.Pass {
		t.Fatalf("a partial waiver set must not pass: %+v", got.Gate)
	}
	w2 := append(w1, gateWaiver{Gate: "design-review", Match: "enclosure not designed", Reason: "next stage", By: "t"})
	if got, err = reapplyReviewWaivers("design", []string{req}, []string{ev}, out, 120000, w2); err != nil || !got.Gate.Pass {
		t.Fatalf("all items waived must pass: %v %+v", err, got)
	}
	if err := os.WriteFile(ev, []byte(`{"color":"blue"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := reapplyReviewWaivers("design", []string{req}, []string{ev}, out, 120000, w2); err == nil || !strings.Contains(err.Error(), "differ") {
		t.Fatalf("changed evidence must be refused, got %v", err)
	}
}

// --max-bytes cuts the prompt; a change past the cut leaves the prompt hash
// as it was, but not the hash of the complete file: reapplying waivers to
// the old verdicts is refused, and so is a review.json without per-file
// hashes.
func TestReapplyReviewWaiversChangeBeyondTruncation(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "REQ.md")
	ev := filepath.Join(dir, "ev.json")
	head := "R1 the box shall be blue\n" + strings.Repeat("filler line\n", 400)
	if err := os.WriteFile(req, []byte(head+"R9 the box shall weigh 1 kg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ev, []byte(`{"color":"blue"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	const limit = 2000 // far shorter than REQ.md
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	prompt := func() string {
		return reviewPrompt("design", map[string]string{"REQ.md": read(req)}, map[string]string{"ev.json": read(ev)}, limit)
	}
	files, err := reviewFilesSHA([]string{req}, []string{ev})
	if err != nil {
		t.Fatal(err)
	}
	rec := reviewRecord{Stage: "design", InputSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(prompt()))), FilesSHA256: files,
		Reviewers: []reviewVerdict{{Reviewer: "codex", Verdict: "fail", Requirements: []reviewRequirement{{ID: "R1", Status: "not_met"}}}}}
	out := filepath.Join(dir, "review")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(r reviewRecord) {
		blob, _ := json.Marshal(r)
		if err := os.WriteFile(filepath.Join(out, "review.json"), blob, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(rec)
	w := []gateWaiver{{Gate: "design-review", Match: "R1 not met", Reason: "approved", By: "t"}}
	if _, err := reapplyReviewWaivers("design", []string{req}, []string{ev}, out, limit, w); err != nil {
		t.Fatalf("unchanged input refused: %v", err)
	}
	before := prompt()
	if err := os.WriteFile(req, []byte(head+"R9 the box shall weigh 9 kg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if prompt() != before {
		t.Fatal("test setup: the change must lie past the --max-bytes cut")
	}
	write(rec)
	if _, err := reapplyReviewWaivers("design", []string{req}, []string{ev}, out, limit, w); err == nil || !strings.Contains(err.Error(), "REQ.md differs") {
		t.Fatalf("a change past the cut must be refused, got %v", err)
	}
	old := rec
	old.FilesSHA256 = nil
	write(old)
	if _, err := reapplyReviewWaivers("design", []string{req}, []string{ev}, out, limit, w); err == nil || !strings.Contains(err.Error(), "no per-file hashes") {
		t.Fatalf("a review without per-file hashes must be refused, got %v", err)
	}
}

// --reapply-waivers checks the inputs it read once; a file that changes
// after that check and before review.json is written is refused, and the
// record on disk keeps its old gate.
func TestReapplyReviewWaiversInputChangedBeforeWrite(t *testing.T) {
	dir := t.TempDir()
	req := filepath.Join(dir, "REQ.md")
	ev := filepath.Join(dir, "ev.json")
	_ = os.WriteFile(req, []byte("R1 the box shall be blue\n"), 0o644)
	_ = os.WriteFile(ev, []byte(`{"color":"red"}`), 0o644)
	reqs, evs, files, err := readReviewInputs([]string{req}, []string{ev})
	if err != nil {
		t.Fatal(err)
	}
	rec := reviewRecord{Stage: "design", InputSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(reviewPrompt("design", reqs, evs, 120000)))), FilesSHA256: files,
		Reviewers: []reviewVerdict{{Reviewer: "codex", Verdict: "fail", Requirements: []reviewRequirement{{ID: "R1", Status: "not_met"}}}}}
	out := filepath.Join(dir, "review")
	_ = os.MkdirAll(out, 0o755)
	blob, _ := json.Marshal(rec)
	_ = os.WriteFile(filepath.Join(out, "review.json"), blob, 0o644)
	old := reviewBeforeWrite
	t.Cleanup(func() { reviewBeforeWrite = old })
	reviewBeforeWrite = func() { _ = os.WriteFile(ev, []byte(`{"color":"green"}`), 0o644) }
	w := []gateWaiver{{Gate: "design-review", Match: "R1 not met", Reason: "approved", By: "t"}}
	if _, err := reapplyReviewWaivers("design", []string{req}, []string{ev}, out, 120000, w); err == nil || !strings.Contains(err.Error(), "changed while the gate was recomputed") {
		t.Fatalf("a change before the write must be refused, got %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(out, "review.json"))
	if string(after) != string(blob) {
		t.Fatal("review.json rewritten despite the refusal")
	}
}
