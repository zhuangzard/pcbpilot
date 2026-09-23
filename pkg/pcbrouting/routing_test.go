package pcbrouting_test

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"testing"

	"github.com/zhuangzard/pcbpilot/pkg/pcbrouting"
)

func request() pcbrouting.Request {
	return pcbrouting.Request{From: pcbrouting.Point{0, 0}, To: pcbrouting.Point{100, 0}, Step: 2, MaxDetour: 30, MaxStates: 100000}
}

// Analytic segment-to-circle clearance: even a thin obstacle between two free
// endpoints must block the complete segment. Radius includes trace+clearance.
func circle(center pcbrouting.Point, radius float64) pcbrouting.SegmentClear {
	return func(a, b pcbrouting.Point) bool {
		dx, dy := b[0]-a[0], b[1]-a[1]
		u := 0.0
		if dx*dx+dy*dy > 0 {
			u = math.Max(0, math.Min(1, ((center[0]-a[0])*dx+(center[1]-a[1])*dy)/(dx*dx+dy*dy)))
		}
		return math.Hypot(center[0]-a[0]-u*dx, center[1]-a[1]-u*dy) >= radius
	}
}

func ExampleSolve() {
	r := pcbrouting.Request{From: pcbrouting.Point{0, 0}, To: pcbrouting.Point{100, 0}, Step: 5, MaxDetour: 20, MaxStates: 10000}
	result, err := pcbrouting.Solve(context.Background(), r, func(a, b pcbrouting.Point) bool { return true })
	fmt.Println(result.Status, result.Length, err)
	// Output: found 100 <nil>
}

func TestDetourAndIndependentReplay(t *testing.T) {
	r := request()
	clear := circle(pcbrouting.Point{50, 0}, 12)
	result, err := pcbrouting.Solve(context.Background(), r, clear)
	if err != nil || result.Status != pcbrouting.Found || result.Length <= 100 || result.States <= 1 {
		t.Fatalf("%+v %v", result, err)
	}
	if err := pcbrouting.Check(context.Background(), r, result.Points, clear); err != nil {
		t.Fatal(err)
	}
	// Translate both problem and obstacle to verify this is parameterized.
	r.From = pcbrouting.Point{300, -77}
	r.To = pcbrouting.Point{400, -77}
	translated, err := pcbrouting.Solve(context.Background(), r, circle(pcbrouting.Point{350, -77}, 12))
	if err != nil || translated.Status != pcbrouting.Found || math.Abs(result.Length-translated.Length) > 1e-6 {
		t.Fatalf("translation changed solution: %+v %v", translated, err)
	}
	for i, p := range result.Points {
		if distance := math.Hypot(translated.Points[i][0]-p[0]-300, translated.Points[i][1]-p[1]+77); distance > 1e-6 {
			t.Fatal("route depends on absolute coordinates")
		}
	}
}

func TestDeterministicAndDoesNotMutateInputs(t *testing.T) {
	r := request()
	before := r
	clear := circle(pcbrouting.Point{50, 0}, 12)
	one, _ := pcbrouting.Solve(context.Background(), r, clear)
	for i := 0; i < 5; i++ {
		two, err := pcbrouting.Solve(context.Background(), r, clear)
		if err != nil || !reflect.DeepEqual(one, two) {
			t.Fatalf("nondeterministic result: %+v %v", two, err)
		}
	}
	if r != before {
		t.Fatal("request mutated")
	}
	path := []pcbrouting.Point{{0, 0}, {20, 0}, {20, 20}}
	saved := append([]pcbrouting.Point(nil), path...)
	_, _ = pcbrouting.Bevel45(path, func(a, b pcbrouting.Point) bool { return true })
	if !reflect.DeepEqual(path, saved) {
		t.Fatal("bevel mutated caller path")
	}
}

func TestIncompleteIsNotGlobalInfeasibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*pcbrouting.Request)
		clear  pcbrouting.SegmentClear
		reason string
	}{
		{"blocked endpoint", func(r *pcbrouting.Request) {}, circle(pcbrouting.Point{0, 0}, 1), "endpoint-blocked"},
		{"bounded no path", func(r *pcbrouting.Request) { r.MaxDetour = 0 }, circle(pcbrouting.Point{50, 0}, 12), "no-path-within-bounds"},
		{"state budget", func(r *pcbrouting.Request) { r.MaxStates = 1 }, circle(pcbrouting.Point{50, 0}, 12), "state-budget"},
		{"grid budget", func(r *pcbrouting.Request) { r.Step = .001 }, circle(pcbrouting.Point{50, 0}, 12), "grid-limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := request()
			tc.mutate(&r)
			got, err := pcbrouting.Solve(context.Background(), r, tc.clear)
			if err != nil || got.Status != pcbrouting.Incomplete || got.Reason != tc.reason || len(got.Points) != 0 || got.States > r.MaxStates {
				t.Fatalf("%+v %v", got, err)
			}
		})
	}
}

func TestInputValidationAndCancellation(t *testing.T) {
	for _, mutate := range []func(*pcbrouting.Request){
		func(r *pcbrouting.Request) { r.Step = 0 }, func(r *pcbrouting.Request) { r.Step = math.NaN() },
		func(r *pcbrouting.Request) { r.MaxDetour = -1 }, func(r *pcbrouting.Request) { r.MaxStates = 0 },
		func(r *pcbrouting.Request) { r.MaxStates = pcbrouting.MaxSearchStates + 1 }, func(r *pcbrouting.Request) { r.To[0] = math.Inf(1) },
		func(r *pcbrouting.Request) { r.From[0] = math.MaxFloat64; r.To[0] = -math.MaxFloat64 },
	} {
		r := request()
		mutate(&r)
		_, err := pcbrouting.Solve(context.Background(), r, func(a, b pcbrouting.Point) bool { t.Fatal("invalid input reached geometry"); return true })
		if err == nil {
			t.Fatalf("invalid request accepted %+v", r)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := pcbrouting.Solve(ctx, request(), func(a, b pcbrouting.Point) bool { return true })
	if !errors.Is(err, context.Canceled) || result.Reason != "canceled" {
		t.Fatalf("%+v %v", result, err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	calls := 0
	clear := circle(pcbrouting.Point{50, 0}, 12)
	result, err = pcbrouting.Solve(ctx, request(), func(a, b pcbrouting.Point) bool {
		calls++
		if calls == 20 {
			cancel()
		}
		return clear(a, b)
	})
	if !errors.Is(err, context.Canceled) || result.Status == pcbrouting.Found {
		t.Fatalf("mid-search cancellation lost %+v %v", result, err)
	}
}

func TestCheckRejectsTamperedPaths(t *testing.T) {
	r := request()
	r.To = pcbrouting.Point{20, 0}
	r.MaxDetour = 5
	for _, path := range [][]pcbrouting.Point{
		nil, {{1, 0}, {20, 0}}, {{0, 0}, {21, 0}}, {{0, 0}, {9, 2}, {20, 0}},
		{{0, 0}, {10, 0}, {10, 5}, {15, 5}, {20, 0}}, // 90-degree turn
		{{0, 0}, {10, 10}, {20, 0}},                  // outside detour and 90-degree reversal
		{{0, 0}, {0, 0}, {20, 0}}, {{0, 0}, {math.NaN(), 0}, {20, 0}},
	} {
		if err := pcbrouting.Check(context.Background(), r, path, func(a, b pcbrouting.Point) bool { return true }); err == nil {
			t.Fatalf("invalid path accepted %v", path)
		}
	}
	// Endpoints are legal; copper between them is not.
	if err := pcbrouting.Check(context.Background(), r, []pcbrouting.Point{{0, 0}, {20, 0}}, circle(pcbrouting.Point{10, 0}, .01)); err == nil {
		t.Fatal("segment interior was not checked")
	}
	// Rounded detour boundary: (-5,5) is sqrt(2)*5 outside, not within 5.
	if err := pcbrouting.Check(context.Background(), r, []pcbrouting.Point{{0, 0}, {-5, 5}, {20, 0}}, func(a, b pcbrouting.Point) bool { return true }); err == nil {
		t.Fatal("diagonal detour expansion accepted")
	}
}

func TestOffGridAndSameSite(t *testing.T) {
	r := request()
	r.To = pcbrouting.Point{100.5, .5}
	clear := circle(pcbrouting.Point{50, 0}, 12)
	got, err := pcbrouting.Solve(context.Background(), r, clear)
	if err != nil || got.Status != pcbrouting.Found || got.Points[len(got.Points)-1] != r.To {
		t.Fatalf("%+v %v", got, err)
	}
	r.To = r.From
	got, err = pcbrouting.Solve(context.Background(), r, clear)
	if err != nil || got.Status != pcbrouting.Found || len(got.Points) != 1 || got.Length != 0 {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestOptimize45ReducesBendsAndRechecksVisibility(t *testing.T) {
	r := pcbrouting.Request{From: pcbrouting.Point{0, 0}, To: pcbrouting.Point{40, 0}, Step: 1, MaxDetour: 20, MaxStates: 1000}
	path := []pcbrouting.Point{{0, 0}, {5, 0}, {10, 5}, {15, 5}, {20, 10}, {25, 10}, {30, 5}, {35, 5}, {40, 0}}
	optimized, err := pcbrouting.Optimize45(context.Background(), r, path, func(a, b pcbrouting.Point) bool { return true })
	if err != nil {
		t.Fatal(err)
	}
	_, before := pcbrouting.Metrics(path)
	_, after := pcbrouting.Metrics(optimized)
	if after >= before || after != 0 || len(optimized) != 2 {
		t.Fatalf("failed to reduce visibility path: before=%d after=%d %v", before, after, optimized)
	}
	blocked := circle(pcbrouting.Point{20, 0}, 2)
	kept, err := pcbrouting.Optimize45(context.Background(), r, path, blocked)
	if err != nil {
		t.Fatal(err)
	}
	if err := pcbrouting.Check(context.Background(), r, kept, blocked); err != nil {
		t.Fatalf("optimizer emitted blocked shortcut: %v %v", kept, err)
	}
}

func BenchmarkSolveDetour(b *testing.B) {
	r := request()
	clear := circle(pcbrouting.Point{50, 0}, 12)
	for b.Loop() {
		result, err := pcbrouting.Solve(context.Background(), r, clear)
		if err != nil || result.Status != pcbrouting.Found {
			b.Fatal(result, err)
		}
	}
}
