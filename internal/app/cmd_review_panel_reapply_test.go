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
	rec := reviewRecord{Stage: "design", InputSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(prompt))),
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
