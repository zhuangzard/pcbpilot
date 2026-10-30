package population

import (
	"strings"
	"testing"
)

func TestConflict(t *testing.T) {
	if Conflict("R1", "", false) != "" || Conflict("R1", "backup", true) != "" || Conflict("R1", "~", false) != "" {
		t.Fatal("agreeing flag and field reported")
	}
	if w := Conflict("R1", "backup footprint", false); !strings.Contains(w, `R1: symbol field "DNP" = "backup footprint" but KiCad's DNP flag is off`) {
		t.Fatalf("%q", w)
	}
	if Field(map[string]string{"dnp": " x "}) != "x" || Field(map[string]string{"Value": "1k"}) != "" {
		t.Fatal("field lookup")
	}
}
