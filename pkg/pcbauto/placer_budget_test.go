package pcbauto

import (
	"strings"
	"testing"
	"time"
)

// A deadline already past: the placement still returns every part posed,
// skips the aesthetics stage and names the first phase it cut short.
func TestPlaceDeadlineFlagsBestSoFar(t *testing.T) {
	b := loadFixture(t, "reference-4stage-compact.json")
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	var phases []string
	r, err := Place(b, an, c, nil, PlaceOptions{Seed: 1, Moves: 200, Deadline: time.Now().Add(-time.Second),
		Progress: func(_ int64, ph string) { phases = append(phases, ph) }})
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.TimeBudgetHit != "legalise" {
		t.Fatalf("timeBudgetHit %q, want legalise (phases %v)", r.Metrics.TimeBudgetHit, phases)
	}
	if r.Aesthetics != nil {
		t.Fatal("aesthetics ran past the deadline")
	}
	if len(r.Placements) != len(b.Parts) {
		t.Fatalf("%d placements for %d parts", len(r.Placements), len(b.Parts))
	}
	note := strings.Join(r.Notes, "\n")
	if !strings.Contains(note, "time budget reached during legalise") || !strings.Contains(note, "aesthetics stage skipped") {
		t.Fatalf("notes:\n%s", note)
	}
	want := []string{"setup", "construct", "legalise", "anneal", "legalise-2", "polish", "rescue", "tidy", "aesthetics", "metrics", "done"}
	if strings.Join(phases, ",") != strings.Join(want, ",") {
		t.Fatalf("phases %v, want %v", phases, want)
	}
}

// No deadline, the default: nothing is flagged.
func TestPlaceNoDeadlineNoFlag(t *testing.T) {
	b := loadFixture(t, "degraded-4stage-compact.json")
	an := Analyze(b, PowerSpec{}, nil)
	r, err := Place(b, an, Understand(b, an), nil, PlaceOptions{Seed: 1, Moves: 200})
	if err != nil {
		t.Fatal(err)
	}
	if r.Metrics.TimeBudgetHit != "" {
		t.Fatalf("timeBudgetHit %q without a deadline", r.Metrics.TimeBudgetHit)
	}
}

// Legalisation that outlasts Timeout used to leave the anneal with a
// deadline in the past, which disabled its clock: it ran every move.
func TestAnnealDeadlineAfterSlowLegalise(t *testing.T) {
	pl := &placer{opt: PlaceOptions{Timeout: 90 * time.Second}}
	now := time.Now()
	if d := pl.annealDeadline(now); d.Sub(now) < 89*time.Second || d.Sub(now) > 91*time.Second {
		t.Fatalf("fresh start: anneal deadline %s after start, want Timeout", d.Sub(now))
	}
	if d := pl.annealDeadline(now.Add(-10 * time.Minute)); d.Sub(now) < 29*time.Second || d.Sub(now) > 31*time.Second {
		t.Fatalf("slow legalise: anneal deadline %s from now, want Timeout/3", d.Sub(now))
	}
	pl.opt.Deadline = now.Add(20 * time.Second)
	if d := pl.annealDeadline(now); d.Sub(now) > 16*time.Second || pl.budgetHit != "anneal" {
		t.Fatalf("budget: anneal deadline %s from now (hit %q), want 3/4 of the remaining 20 s", d.Sub(now), pl.budgetHit)
	}
}
