package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --engineer-grade without --spec is a usage error, not a silent pass.
func TestKicadSchCheckEGNeedsSpec(t *testing.T) {
	if _, err := kicadSchCheckEG("root.kicad_sch", nil, ""); err == nil || !strings.Contains(err.Error(), "--spec") {
		t.Fatalf("want a --spec error, got %v", err)
	}
}

// The engineer-grade gate runs on sheets drawn outside sch-build: a supply
// symbol rotated 180° (EG-05) is reported for the external sheet.
func TestKicadSchCheckEGExternalSheet(t *testing.T) {
	dir := t.TempDir()
	spec := `{"schema":"pcbpilot.kicad.sch-build/1","name":"t",
"title":{"title":"T","rev":"1","company":"C","date":"2026-10-13"},
"parts":[{"ref":"C1","value":"100n","pins":[{"number":"1","name":"1"},{"number":"2","name":"2"}]}],
"nets":[{"name":"+3V3","pins":["C1:1"],"kind":"power"},{"name":"GND","pins":["C1:2"],"kind":"ground"}]}`
	specPath := filepath.Join(dir, "spec.json")
	if err := os.WriteFile(specPath, []byte(spec), 0o644); err != nil {
		t.Fatal(err)
	}
	sheet := `(kicad_sch (version 20231120) (generator "t") (uuid "00000000-0000-0000-0000-000000000001") (paper "A4")
 (lib_symbols
  (symbol "p:C" (exclude_from_sim no) (in_bom yes) (on_board yes)
   (property "Reference" "C" (at 0 0 0) (effects (font (size 1.27 1.27))))
   (symbol "C_0_1" (rectangle (start -1.27 -0.635) (end 1.27 0.635) (stroke (width 0) (type default)) (fill (type none))))
   (symbol "C_1_1" (pin passive line (at 0 3.81 270) (length 2.54) (name "1" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27)))))
                   (pin passive line (at 0 -3.81 90) (length 2.54) (name "2" (effects (font (size 1.27 1.27)))) (number "2" (effects (font (size 1.27 1.27)))))))
  (symbol "p:+3V3" (power global) (exclude_from_sim no) (in_bom yes) (on_board yes)
   (property "Reference" "#PWR" (at 0 0 0) (effects (font (size 1.27 1.27)) hide))
   (property "Value" "+3V3" (at 0 3.175 0) (effects (font (size 1.27 1.27))))
   (symbol "+3V3_0_1" (polyline (pts (xy -1.905 1.27) (xy 1.905 1.27)) (stroke (width 0.254) (type default)) (fill (type none))))
   (symbol "+3V3_1_1" (pin power_in line (at 0 0 90) (length 0) hide (name "+3V3" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))))
 (symbol (lib_id "p:C") (at 50.8 50.8 0) (unit 1) (in_bom yes) (on_board yes) (uuid "00000000-0000-0000-0000-000000000002")
  (property "Reference" "C1" (at 53.34 50.8 0) (effects (font (size 1.27 1.27))))
  (pin "1" (uuid "00000000-0000-0000-0000-000000000003")) (pin "2" (uuid "00000000-0000-0000-0000-000000000004")))
 (symbol (lib_id "p:+3V3") (at 50.8 46.99 180) (unit 1) (in_bom yes) (on_board yes) (uuid "00000000-0000-0000-0000-000000000005")
  (property "Reference" "#PWR01" (at 50.8 46.99 0) (effects (font (size 1.27 1.27)) hide))
  (property "Value" "+3V3" (at 50.8 43.18 0) (effects (font (size 1.27 1.27))))
  (pin "1" (uuid "00000000-0000-0000-0000-000000000006")))
 (sheet_instances (path "/" (page "1"))))
`
	root := filepath.Join(dir, "t.kicad_sch")
	if err := os.WriteFile(root, []byte(sheet), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := kicadSchCheckEG(root, []string{root}, specPath)
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != "fail" || !strings.Contains(g.Detail, "EG-05") {
		t.Fatalf("want an EG-05 failure for the inverted supply symbol, got %s: %s", g.Status, g.Detail)
	}
}
