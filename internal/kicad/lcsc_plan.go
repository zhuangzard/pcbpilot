package kicad

// lcsc_plan.go — reading an `--alternatives` plan back for `--set @plan.json`.
// The plan carries the LCSC number each designator had when it was made
// ("expect"); applying it to a design that has moved on is refused, so a later
// manual change is never silently overwritten.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Plan is an `--alternatives` plan read back: what to write, what the design
// must still carry, and the data it was computed from.
type Plan struct {
	Assign      map[string]string
	Expect      map[string]string
	Boards      int
	Fingerprint string
}

// PlanCodes is the set of LCSC numbers a plan touches (replaced and replacement).
func PlanCodes(assign, expect map[string]string) []string {
	set := map[string]bool{}
	for _, m := range []map[string]string{assign, expect} {
		for _, v := range m {
			set[strings.ToUpper(strings.TrimSpace(v))] = true
		}
	}
	out := make([]string, 0, len(set))
	for c := range set {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// PlanFingerprint is a canonical digest of everything a substitution depends
// on: the order quantity (boards) and, for every part carrying one of the
// plan's LCSC numbers, its designator, value, footprint, design MPN, LCSC
// number and whether it is assembled. A changed value, footprint or MPN, or a
// placement added to (or removed from) a number the plan reasons about, changes
// it even when every C-number is still the expected one.
func PlanFingerprint(parts []Part, mpn map[string]string, boards int, codes []string) string {
	want := map[string]bool{}
	for _, c := range codes {
		want[strings.ToUpper(strings.TrimSpace(c))] = true
	}
	rows := []string{fmt.Sprintf("boards=%d", boards)}
	for _, p := range parts {
		if !want[strings.ToUpper(strings.TrimSpace(p.LCSC))] {
			continue
		}
		rows = append(rows, fmt.Sprintf("%q|%q|%q|%q|%q|%t", p.Ref, p.Value, p.Footprint, mpn[p.Ref], strings.ToUpper(strings.TrimSpace(p.LCSC)), p.Assembled()))
	}
	sort.Strings(rows)
	sum := sha256.Sum256([]byte(strings.Join(rows, "\n")))
	return hex.EncodeToString(sum[:])
}

// LoadPlan reads a plan file.
func LoadPlan(path string) (*Plan, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	var doc struct {
		Kind        string            `json:"kind"`
		Boards      int               `json:"boards"`
		Fingerprint string            `json:"fingerprint"`
		Assignments map[string]string `json:"assignments"`
		Expect      map[string]string `json:"expect"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if doc.Kind != "" && doc.Kind != AltReportKind {
		return nil, fmt.Errorf("%s: kind %q is not %q", path, doc.Kind, AltReportKind)
	}
	if len(doc.Assignments) == 0 {
		return nil, fmt.Errorf("%s: the plan has no assignments (nothing to apply)", path)
	}
	assign := map[string]string{}
	for ref, v := range doc.Assignments {
		v = strings.ToUpper(strings.TrimSpace(v))
		if strings.TrimSpace(ref) == "" || !ValidLCSC(v) {
			return nil, fmt.Errorf("%s: assignment %q=%q is not REF → C + digits", path, ref, v)
		}
		assign[strings.TrimSpace(ref)] = v
	}
	if doc.Expect == nil {
		return nil, fmt.Errorf("%s: the plan has no \"expect\" section, so it cannot be checked against the design (regenerate it with --alternatives)", path)
	}
	for ref := range assign {
		if _, ok := doc.Expect[ref]; !ok {
			return nil, fmt.Errorf("%s: %s has no expected current LCSC number", path, ref)
		}
	}
	if doc.Fingerprint == "" || doc.Boards < 1 {
		return nil, fmt.Errorf("%s: the plan carries no design fingerprint / board count, so it cannot be checked against the design (regenerate it with --alternatives)", path)
	}
	return &Plan{Assign: assign, Expect: doc.Expect, Boards: doc.Boards, Fingerprint: doc.Fingerprint}, nil
}

// VerifyPlan returns one message per way the design has moved on from the
// plan: a designator whose LCSC number differs (or is gone), or a changed
// fingerprint of the substitution-relevant data.
func VerifyPlan(parts []Part, mpn map[string]string, pl *Plan) []string {
	bad := VerifyExpect(parts, pl.Expect)
	if got := PlanFingerprint(parts, mpn, pl.Boards, PlanCodes(pl.Assign, pl.Expect)); got != pl.Fingerprint {
		bad = append(bad, "value, footprint, MPN or placements of the affected LCSC numbers changed since the plan was made (fingerprint mismatch)")
	}
	return bad
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
