package pcbauto

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const fixtureDir = "../../internal/app/testdata/boards"

func loadFixture(t testing.TB, name string) *Board {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Skipf("fixture %s: %v", name, err)
	}
	b, err := FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// runPipeline analyses, decides the stackup (optionally forced to the board's
// real layer count) and routes the board with its existing placement.
func runPipeline(t testing.TB, b *Board, force int, timeout time.Duration) (*Stackup, *Analysis, *RouteResult, *DRCReport) {
	t.Helper()
	pre := Analyze(b, PowerSpec{}, nil)
	st := DecideStackup(b, pre, StackOptions{Force: force})
	an := Analyze(b, PowerSpec{}, st)
	res, err := Route(context.Background(), b, st, an, RouteOptions{Timeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	drc := CheckDRC(b, an, st, res.Tracks, res.Vias)
	return st, an, res, drc
}

func TestFixtureMIPIAdapterRoutes(t *testing.T) {
	b := loadFixture(t, "lckfb-mipi-3in1-adapter.json")
	st, _, res, drc := runPipeline(t, b, b.CopperLayers, 2*time.Minute)
	t.Logf("stackup %d layers: %v", st.Layers, st.Reasons)
	t.Logf("stats %+v", res.Stats)
	t.Logf("drc %v disconnected=%d", drc.ByKind, len(drc.Disconnected))
	for i, u := range res.Unrouted {
		if i < 10 {
			t.Logf("unrouted %+v", u)
		}
	}
	for i, v := range drc.Violations {
		if i < 10 {
			t.Logf("violation %+v", v)
		}
	}
	for i, n := range res.Notes {
		if i < 5 {
			t.Logf("note %s", n)
		}
	}
	if res.Stats.Completion < 80 {
		t.Errorf("completion %.1f%% < 80%%", res.Stats.Completion)
	}
}

// TestFixtureBench routes every real fixture board with its human placement
// and logs completion / vias / DRC. Long-running: skipped with -short.
func TestFixtureBench(t *testing.T) {
	if testing.Short() {
		t.Skip("bench")
	}
	for _, name := range []string{
		"lckfb-mipi-3in1-adapter.json", "bbclaw-ai-voice-terminal.json",
		"lckfb-szpi-esp32s3.json", "lckfb-rk3568-4layer.json", "lckfb-k230-canmv.json",
	} {
		if only := os.Getenv("PCBAUTO_BOARD"); only != "" && only != name {
			continue
		}
		t.Run(name, func(t *testing.T) {
			b := loadFixture(t, name)
			out, err := Run(t.Context(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, Route: RouteOptions{Timeout: 4 * time.Minute}})
			if err != nil {
				t.Fatal(err)
			}
			st, res, drc := out.Stackup, out.Route, out.DRC
			t.Logf("attempts %+v", out.Attempts)
			s := res.Stats
			t.Logf("%s: layers=%d pads=%d nets=%d conn=%d routed=%d (%.1f%%) vias=%d fanout=%d len=%.1fin iters=%d t=%.1fs drc=%v disc=%d pre=%d repaired=%d",
				name, st.Layers, st.Metrics.PadCount, s.Nets, s.Connections, s.Routed, s.Completion, s.Vias, s.FanoutVias, s.WireLengthIn, s.Iterations, float64(s.Millis)/1000, drc.ByKind, len(drc.Disconnected), s.PreRepairViolations, s.Repaired)
			reasons := map[string]int{}
			for _, u := range res.Unrouted {
				reasons[u.Reason]++
			}
			t.Logf("unrouted reasons %v trace %v grid %.2f", reasons, s.ConflictTrace, s.GridMil)
		})
	}
}
