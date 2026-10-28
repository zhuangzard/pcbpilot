package kicad

// lcsc_plan.go — reading an `--alternatives` plan back for `--set @plan.json`.
// The plan carries the LCSC number each designator had when it was made
// ("expect"); applying it to a design that has moved on is refused, so a later
// manual change is never silently overwritten.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// LoadPlan reads a plan file: its assignments and the numbers it expects.
func LoadPlan(path string) (assign, expect map[string]string, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	var doc struct {
		Kind        string            `json:"kind"`
		Assignments map[string]string `json:"assignments"`
		Expect      map[string]string `json:"expect"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind != "" && doc.Kind != AltReportKind {
		return nil, nil, fmt.Errorf("%s: kind %q is not %q", path, doc.Kind, AltReportKind)
	}
	if len(doc.Assignments) == 0 {
		return nil, nil, fmt.Errorf("%s: the plan has no assignments (nothing to apply)", path)
	}
	assign = map[string]string{}
	for ref, v := range doc.Assignments {
		v = strings.ToUpper(strings.TrimSpace(v))
		if strings.TrimSpace(ref) == "" || !ValidLCSC(v) {
			return nil, nil, fmt.Errorf("%s: assignment %q=%q is not REF → C + digits", path, ref, v)
		}
		assign[strings.TrimSpace(ref)] = v
	}
	if doc.Expect == nil {
		return nil, nil, fmt.Errorf("%s: the plan has no \"expect\" section, so it cannot be checked against the design (regenerate it with --alternatives)", path)
	}
	for ref := range assign {
		if _, ok := doc.Expect[ref]; !ok {
			return nil, nil, fmt.Errorf("%s: %s has no expected current LCSC number", path, ref)
		}
	}
	return assign, doc.Expect, nil
}

// VerifyExpect returns one message per designator whose current LCSC number
// differs from what the plan expected (or that is not on the board).
func VerifyExpect(parts []Part, expect map[string]string) []string {
	cur := map[string]string{}
	for _, p := range parts {
		cur[p.Ref] = p.LCSC
	}
	var bad []string
	for ref, want := range expect {
		got, ok := cur[ref]
		switch {
		case !ok:
			bad = append(bad, fmt.Sprintf("%s is not on the board any more", ref))
		case got != want:
			bad = append(bad, fmt.Sprintf("%s now has %q, the plan expected %q", ref, got, want))
		}
	}
	sort.Strings(bad)
	return bad
}
