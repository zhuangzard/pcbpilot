package app

// kicad_rules_timing.go — per-call wall time of kicad rules' KiCad calls
// (pcbnew loads through the bridge, kicad-cli DRC), summed per operation
// and printed on stderr at the end of a run: the cost of a rules run on a
// large board is almost all in these calls.

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// timedRulesBackend wraps a rulesBackend and records each call's time.
type timedRulesBackend struct {
	rulesBackend
	calls map[string]int
	secs  map[string]float64
	start time.Time
}

func newTimedRulesBackend(b rulesBackend) *timedRulesBackend {
	return &timedRulesBackend{rulesBackend: b, calls: map[string]int{}, secs: map[string]float64{}, start: time.Now()}
}

func (t *timedRulesBackend) note(op string, t0 time.Time) {
	t.calls[op]++
	t.secs[op] += time.Since(t0).Seconds()
}

func (t *timedRulesBackend) Snapshot(pcb string) ([]byte, error) {
	defer t.note("snapshot", time.Now())
	return t.rulesBackend.Snapshot(pcb)
}

func (t *timedRulesBackend) RulesRead(pcb string) (*kicad.RulesReadback, error) {
	defer t.note("rules-read", time.Now())
	return t.rulesBackend.RulesRead(pcb)
}

func (t *timedRulesBackend) NetclassesReq(pcb string, req kicad.NetclassRequest, out string) (*kicad.NetclassFull, error) {
	defer t.note("netclasses", time.Now())
	return t.rulesBackend.NetclassesReq(pcb, req, out)
}

func (t *timedRulesBackend) RulesProbe(pcb string, tracks []kicad.ProbeTrack, out string) ([]string, error) {
	defer t.note("rules-probe", time.Now())
	return t.rulesBackend.RulesProbe(pcb, tracks, out)
}

func (t *timedRulesBackend) DRC(pcb, out string) (*kicad.DRCReport, error) {
	defer t.note("drc", time.Now())
	return t.rulesBackend.DRC(pcb, out)
}

// summary is one stderr line: total and per operation (calls × seconds).
func (t *timedRulesBackend) summary() string {
	ops := make([]string, 0, len(t.calls))
	for op := range t.calls {
		ops = append(ops, op)
	}
	sort.Strings(ops)
	parts := make([]string, 0, len(ops))
	for _, op := range ops {
		parts = append(parts, fmt.Sprintf("%s %d× %.1fs", op, t.calls[op], t.secs[op]))
	}
	return fmt.Sprintf("total %.1fs; %s", time.Since(t.start).Seconds(), strings.Join(parts, ", "))
}

func (t *timedRulesBackend) print(w io.Writer) {
	fmt.Fprintf(w, "kicad rules: timing: %s\n", t.summary())
}

// kicadProbeTools is the real backend: kicad rules runs DRC only on its
// probe boards (bridge rules-probe removes the footprints), so kicad-cli
// runs with a private, empty KiCad configuration — loading the global
// footprint library table is ~100 s per run on a stock install and the
// probe board never reads it.
type kicadProbeTools struct{ *kicad.Tools }

func (k kicadProbeTools) DRC(pcb, out string) (*kicad.DRCReport, error) {
	return k.Tools.DRCNoLibraries(pcb, out)
}
