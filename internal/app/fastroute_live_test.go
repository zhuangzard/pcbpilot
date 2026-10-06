package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

// TestFastrouteLive is the compatibility check run before a new fastroute
// release is adopted (scripts/fastroute-upgrade-check.sh): it needs
// PCBPILOT_FASTROUTE_LIVE (the binary; TestMain clears FASTROUTE_BIN) and is
// skipped otherwise. It routes the EasyEDA export
// fixture through dsn-fix + the intent requirements, with every argument
// pcbpilot passes, and checks the session and report pcbpilot reads.
func TestFastrouteLive(t *testing.T) {
	bin := os.Getenv("PCBPILOT_FASTROUTE_LIVE")
	if bin == "" {
		t.Skip("PCBPILOT_FASTROUTE_LIVE not set")
	}
	raw, err := os.ReadFile("../pcb/specctra/testdata/easyeda-export.dsn")
	if err != nil {
		t.Fatal(err)
	}
	reqs := map[string]specctra.NetRequirement{"GND": {OuterMil: 21.65, InnerMil: 43.31, MinMil: 10, ClearanceMil: 6}}
	text, _, rq, err := prepareDSN(string(raw), specctra.FixOptions{EdgeOuterMil: 20, EdgeInnerMil: 30}, reqs)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	dsn := filepath.Join(dir, "b.dsn")
	pairs := filepath.Join(dir, "pairs.txt")
	tune := filepath.Join(dir, "tune.txt")
	if err := os.WriteFile(dsn, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(pairs, []byte(""), 0o644)
	_ = os.WriteFile(tune, []byte(""), 0o644)
	o := fastrouteOpts{bin: bin, threads: 1, minTraceUm: rq.MinTraceMil * 25.4, noNeckdown: []string{"GND"}, pairsFile: pairs, tuneFile: tune,
		rounds: 1, timeout: 5 * time.Minute, maxTime: time.Minute}
	var log strings.Builder
	ses, runs, err := runFastroute(o, dsn, filepath.Join(dir, "b"), &log)
	if err != nil {
		t.Fatalf("fastroute: %v\n%s", err, log.String())
	}
	last := lastOK(runs)
	if last == nil || last.Unrouted != 0 {
		t.Fatalf("runs = %+v\n%s", runs, log.String())
	}
	data, err := os.ReadFile(ses)
	if err != nil {
		t.Fatal(err)
	}
	w, err := specctra.ParseSES(string(data))
	if err != nil {
		t.Fatalf("session no longer parses: %v", err)
	}
	if len(w.Segments) == 0 {
		t.Fatal("session carries no wiring")
	}
	for _, s := range w.Segments {
		if s.Net == "GND" && s.Layer == "TopLayer" && s.WidthMil+0.01 < 10 {
			t.Fatalf("GND necked below its 10 mil minimum: %+v", s)
		}
	}
}
