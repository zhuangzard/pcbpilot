// Package population is the one rule for whether a part is fitted, shared by
// the schematic netlist IR, sim power, kicad lcsc and kicad fab.
//
// KiCad's own DNP flag (the symbol's "Do not populate" attribute) decides.
// A symbol field named "DNP" is text, not the flag: when it is set while the
// flag is off, the part is fitted everywhere (simulated, on the BOM and the
// placement file) and every tool says so with the same warning, so that no
// tool quietly reads the note as "not fitted" while another fits the part.
package population

import (
	"fmt"
	"strings"
)

// FieldName is the custom symbol field that is easily taken for the flag.
const FieldName = "DNP"

// Field returns the value of a field named DNP (any case) in fields.
func Field(fields map[string]string) string {
	for k, v := range fields {
		if strings.EqualFold(strings.TrimSpace(k), FieldName) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// Conflict is the warning for ref when its "DNP" field disagrees with
// KiCad's flag (field set, flag off); "" when they agree.
func Conflict(ref, field string, flag bool) string {
	field = strings.TrimSpace(field)
	if flag || field == "" || field == "~" {
		return ""
	}
	return fmt.Sprintf(`%s: symbol field "DNP" = %q but KiCad's DNP flag is off — fitted (simulated, on the BOM and placed); KiCad's flag decides: set it in KiCad to leave the part out, or clear / rename the field`, ref, field)
}
