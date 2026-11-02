package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// boardDRCBusy is tiny.kicad_pcb plus a comb of 80 tracks of two nets,
// 0.1 mm apart under a 0.2 mm clearance: every track violates against both
// neighbours (many errors per item, KiCad picks which ones it reports).
func boardDRCBusy(t *testing.T, extra string) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "tiny.kicad_pcb"))
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < 80; i++ {
		net := map[bool]string{true: "GND", false: "VCC"}[i%2 == 0]
		y := 40 + float64(i)*0.35
		fmt.Fprintf(&b, "\t(segment\n\t\t(start 40 %.2f)\n\t\t(end 70 %.2f)\n\t\t(width 0.25)\n\t\t(layer \"F.Cu\")\n\t\t(net %q)\n\t\t(uuid \"00000000-0000-4000-8000-%012d\")\n\t)\n", y, y, net, i)
	}
	b.WriteString(extra)
	body := strings.TrimRight(strings.ReplaceAll(string(src), "\r\n", "\n"), "\n")
	body = strings.TrimSuffix(body, ")") + b.String() + ")\n"
	p := filepath.Join(t.TempDir(), "busy.kicad_pcb")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// The same board twice passes kicad board's before/after DRC gate every
// time, however many errors per item it has; a track that really adds a
// clearance error still fails it.
func TestBoardDRCCheckStableLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 to run against the installed KiCad")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Skip(err)
	}
	in := boardDRCBusy(t, "")
	for run := 0; run < 3; run++ {
		res, d := boardDRCCheck(kt, "outline", in, boardDRCBusy(t, ""), nil, t.TempDir())
		if res.Status != gate.Pass || d == nil || d.Before < 100 {
			t.Fatalf("run %d: same board: %+v (%+v)", run, res, d)
		}
	}
	// A third-net track next to the comb: a new clearance error with its uuid.
	cross := "\t(segment\n\t\t(start 40 68.00)\n\t\t(end 70 68.00)\n\t\t(width 0.25)\n\t\t(layer \"F.Cu\")\n\t\t(net \"A\")\n\t\t(uuid \"00000000-0000-4000-8000-999999999999\")\n\t)\n"
	res, d := boardDRCCheck(kt, "outline", in, boardDRCBusy(t, cross), nil, t.TempDir())
	if res.Status != gate.Fail || d == nil || len(d.New) == 0 {
		t.Fatalf("new crossing track not detected: %+v", res)
	}
	found := false
	for _, n := range d.New {
		for _, o := range n.Objs {
			found = found || o == "00000000-0000-4000-8000-999999999999"
		}
	}
	if !found {
		t.Fatalf("new errors do not name the new track: %+v", d.New)
	}
}
