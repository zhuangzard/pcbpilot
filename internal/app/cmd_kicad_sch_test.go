package app

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

const testSymLib = `(kicad_symbol_lib (version 20251024) (generator "test")
	(symbol "R"
		(exclude_from_sim no) (in_bom yes) (on_board yes)
		(property "Reference" "R" (at 2.032 0 90) (effects (font (size 1.27 1.27))))
		(property "Value" "R" (at 0 0 90) (effects (font (size 1.27 1.27))))
		(symbol "R_0_1" (rectangle (start -1.016 -2.54) (end 1.016 2.54) (stroke (width 0.254) (type default)) (fill (type none))))
		(symbol "R_1_1"
			(pin passive line (at 0 3.81 270) (length 1.27) (name "~" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
			(pin passive line (at 0 -3.81 90) (length 1.27) (name "~" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27)))))
		)
	)
)`

// TestSchKicadBackend drives sch place / wire / netflag / no-connect /
// modify with --backend kicad on a fresh sheet and reads the result back.
func TestSchKicadBackend(t *testing.T) {
	dir := t.TempDir()
	sheet := filepath.Join(dir, "t.kicad_sch")
	if err := os.WriteFile(sheet, []byte(kicad.NewSchematicText("A4", kicad.NewUUID(), true)), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(dir, "my.kicad_sym")
	if err := os.WriteFile(lib, []byte(testSymLib), 0o644); err != nil {
		t.Fatal(err)
	}
	k := []string{"--backend", "kicad", "--kicad-sch", sheet}
	run := func(args ...string) string {
		t.Helper()
		var out, errb bytes.Buffer
		if code := Run(append(append([]string{"sch"}, args...), k...), &out, &errb); code != 0 {
			t.Fatalf("%v: exit %d: %s %s", args, code, out.String(), errb.String())
		}
		return out.String()
	}
	run("place", "--symbol", "my:R", "--symbol-lib", lib, "--designator", "R1", "--value", "10k", "--lcsc", "C25804", "--x", "50.8", "--y", "50.8")
	run("place", "--symbol", "my:R", "--designator", "R2", "--value", "1k", "--x", "76.2", "--y", "50.8", "--rotation", "90")
	run("wire", "--through", "R1.2", "--through", "50.8,60.96", "--through", "71.12,60.96", "--through", "R2.2")
	run("netflag", "--kind", "gnd", "--net", "GND", "--at", "R1.1")
	run("netflag", "--kind", "net_port_bi", "--net", "SIG", "--at", "R2.1")
	run("place", "--symbol", "my:R", "--designator", "R3", "--value", "1k", "--x", "101.6", "--y", "50.8")
	run("no-connect", "--designator", "R3", "--pin", "1")
	run("modify", "--id", "R2", "--designator", "R9", "--patch", `{"customAttributes":{"MPN":"0603WAF1001T5E"}}`)
	src, _ := os.ReadFile(sheet)
	for _, s := range []string{`(reference "R9")`, `(property "MPN" "0603WAF1001T5E"`, `(property "LCSC" "C25804"`, `(global_label "SIG"`, `(no_connect`} {
		if !strings.Contains(string(src), s) {
			t.Errorf("sheet lacks %s", s)
		}
	}
	var errb bytes.Buffer
	if code := Run([]string{"sch", "place", "--backend", "kicad", "--symbol", "my:R"}, &bytes.Buffer{}, &errb); code == 0 {
		t.Error("--backend kicad without --kicad-sch accepted")
	}
	if _, err := kicad.KicadCLI(); err != nil {
		t.Skip(err)
	}
	conn := filepath.Join(dir, "conn.json")
	var out bytes.Buffer
	if code := Run([]string{"kicad", "netlist", "--sch", sheet, "--out", conn}, &out, &errb); code != 0 {
		t.Fatalf("kicad netlist: %s", errb.String())
	}
	b, _ := os.ReadFile(conn)
	for _, s := range []string{`"ref": "R9"`, `"supplierId": "C25804"`, `"name": "GND"`, `"name": "SIG"`, `"noConnected": true`} {
		if !bytes.Contains(b, []byte(s)) {
			t.Errorf("connectivity lacks %s", s)
		}
	}
}
