package kicad

import "testing"

// The sheet parse cache is keyed by the text: an edited sheet is parsed
// again, the same text shares one tree.
func TestParseSheetCachedFollowsTheText(t *testing.T) {
	a := []byte(`(kicad_sch (uuid "aaaa") (symbol (property "Reference" "R1")))`)
	b := []byte(`(kicad_sch (uuid "aaaa") (symbol (property "Reference" "R2")))`) // same length
	ra, err := parseSheetCached(a)
	if err != nil {
		t.Fatal(err)
	}
	rb, err := parseSheetCached(b)
	if err != nil {
		t.Fatal(err)
	}
	if ra == rb || propVal(rb.child("symbol"), "Reference") != "R2" {
		t.Fatalf("an edited sheet came back from the cache: %q", propVal(rb.child("symbol"), "Reference"))
	}
	if again, _ := parseSheetCached(append([]byte(nil), a...)); again != ra {
		t.Error("the same text was parsed twice")
	}
	if _, err := parseSheetCached([]byte(`(kicad_sch (uuid`)); err == nil {
		t.Error("broken text parsed")
	}
}
