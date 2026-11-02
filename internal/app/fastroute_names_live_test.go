package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// End to end with KiCad and the real fastroute: nets whose names carry
// spaces, both quote types, = # * ? and a backslash are exported by KiCad,
// aliased for fastroute, routed as pairs, and the session is imported by
// KiCad onto exactly those nets. Needs PCBPILOT_KICAD_LIVE=1 and
// PCBPILOT_LIVE_FASTROUTE.
func TestFastrouteAliasesKiCadRoundTripLive(t *testing.T) {
	if os.Getenv("PCBPILOT_KICAD_LIVE") != "1" || os.Getenv("PCBPILOT_LIVE_FASTROUTE") == "" {
		t.Skip("set PCBPILOT_KICAD_LIVE=1 and PCBPILOT_LIVE_FASTROUTE (a fastroute executable)")
	}
	kt, err := kicad.Locate()
	if err != nil {
		t.Skip(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "kicad", "testdata", "tiny.kicad_pcb"))
	if err != nil {
		t.Fatal(err)
	}
	// KiCad file escaping: \" and \ inside a quoted string.
	file := map[string]string{"A": `/He said \"hi\" \ x (1/2)`, "B": `/it's = #1/B`, "C": `/A*? c`, "D": `/D d`}
	real := map[string]string{"A": `/He said "hi" \ x (1/2)`, "B": `/it's = #1/B`, "C": `/A*? c`, "D": `/D d`}
	body := string(src)
	for k, v := range file {
		body = strings.ReplaceAll(body, `(net "`+k+`")`, `(net "`+v+`")`)
	}
	dir := t.TempDir()
	pcb := filepath.Join(dir, "names.kicad_pcb")
	if err := os.WriteFile(pcb, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	dsn := filepath.Join(dir, "route.dsn")
	if _, err := kt.ExportDSN(pcb, dsn); err != nil {
		t.Fatal(err)
	}
	dsnText, _ := os.ReadFile(dsn)
	in := &designIntent{Nets: map[string]*intentNet{
		real["A"]: {DiffPair: real["B"]}, real["B"]: {DiffPair: real["A"]},
		real["C"]: {DiffPair: real["D"]}, real["D"]: {DiffPair: real["C"]},
	}}
	al := newFastrouteNetAliases(intentRouterNets(in), string(dsnText))
	if len(al.toAlias) != 4 {
		t.Fatalf("aliases %v", al.toAlias)
	}
	pairs, _ := intentPairsAndTune(in, al)
	pf := filepath.Join(dir, "route-pairs.txt")
	_ = os.WriteFile(pf, []byte(pairs), 0o644)
	ses, runs, err := runFastroute(fastrouteOpts{bin: os.Getenv("PCBPILOT_LIVE_FASTROUTE"), maxTime: 20 * time.Second, timeout: 3 * time.Minute, pairsFile: pf, aliases: al},
		dsn, filepath.Join(dir, "route"), os.Stderr)
	if err != nil {
		t.Fatalf("%v %+v", err, runs)
	}
	out := filepath.Join(dir, "routed.kicad_pcb")
	if _, err := kt.ImportSES(pcb, ses, out); err != nil {
		t.Fatal(err)
	}
	raw, err := kt.Snapshot(out)
	if err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Copper struct {
			Lines []struct {
				Net string `json:"net"`
			} `json:"lines"`
		} `json:"copper"`
	}
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatal(err)
	}
	tracks := map[string]int{}
	for _, l := range snap.Copper.Lines {
		tracks[l.Net]++
	}
	for k, n := range real {
		if tracks[n] == 0 {
			t.Fatalf("net %s %q has no imported track: %v", k, n, tracks)
		}
	}
	for n := range tracks {
		if strings.Contains(n, "pcbpilot_net_") {
			t.Fatalf("an alias reached the board: %q", n)
		}
	}
}
