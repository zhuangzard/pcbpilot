package app

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// TestKicadSchOpTimings logs the wall time of each KiCad schematic operation
// on the rich fixture (docs/kicad/PARITY.md quotes it against the
// connector's ~4 s per connected pin and 85–111 s per layout-plan --zones
// apply). Every write includes the strict gate and a kicad-cli netlist of a
// project copy. Run: go test ./internal/app -run TestKicadSchOpTimings -v
func TestKicadSchOpTimings(t *testing.T) {
	if testing.Short() {
		t.Skip("timing log; run without -short")
	}
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip(err)
	}
	timed := func(name string, f func() int) {
		t0 := time.Now()
		if code := f(); code != 0 {
			t.Fatalf("%s: exit %d", name, code)
		}
		t.Logf("%-40s %6.0f ms", name, float64(time.Since(t0).Microseconds())/1000)
	}
	sheet := richFixture(t)
	dir := filepath.Dir(sheet)
	spec := filepath.Join(dir, "spec.json")
	_ = os.WriteFile(spec, []byte(`{"connections":[{"pin":"R3:1","kind":"power","net":"+3V3"},{"pin":"R3:2","kind":"gnd","net":"GND"}]}`), 0o644)
	run := func(args ...string) int {
		var out, errb bytes.Buffer
		code := Run(append(append([]string{"sch"}, args...), "--backend", "kicad", "--kicad-sch", sheet), &out, &errb)
		if code != 0 {
			t.Logf("%s %s", out.String(), errb.String())
		}
		return code
	}
	timed("netlist export (kicad-cli)", func() int {
		if _, err := kicad.ExportSchNetlist(sheet); err != nil {
			return 1
		}
		return 0
	})
	timed("autoconnect 2 pins (one verified write)", func() int { return run("autoconnect", "--spec", spec) })
	timed("group-move R1,R2 (rigid)", func() int { return run("group-move", "--refs", "R1,R2", "--dx", "25.4", "--dy", "12.7") })
	timed("group-move R5 (re-route)", func() int { return run("group-move", "--refs", "R5", "--dx", "25.4", "--dy", "-7.62") })
	in := `{"schemaVersion":1,"netPolicies":{"SIG":"direct","GND":"local_ground","OUT":"direct","+3V3":"local_power","VREF":"module_port"},
"zones":[{"id":"a","title":"DIVIDER","coreComponentId":"r1","componentIds":["r1","r2"]},{"id":"b","title":"OUTPUT","coreComponentId":"r4","componentIds":["r4","r5"]}],
"components":[` + zoneComp("r1", "R1", "SIG", "GND", false) + `,` + zoneComp("r2", "R2", "SIG", "GND", true) + `,` +
		zoneComp("r4", "R4", "+3V3", "OUT", false) + `,` + zoneComp("r5", "R5", "VREF", "OUT", true) + `]}`
	from := filepath.Join(dir, "zones.json")
	_ = os.WriteFile(from, []byte(in), 0o644)
	timed("layout-plan --zones apply (2 zones)", func() int { return run("layout-plan", "--zones", "--from", from, "--fit") })
	timed("titleblock", func() int { return run("titleblock", "--title", "T", "--rev", "A") })
	timed("destagger (dry run)", func() int { return run("destagger") })
	timed("kicad sch-check --fix-pwr-flag", func() int {
		var out, errb bytes.Buffer
		Run([]string{"kicad", "sch-check", "--sch", sheet, "--fix-pwr-flag"}, &out, &errb)
		return 0
	})
}
