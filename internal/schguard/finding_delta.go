package schguard

import (
	"encoding/json"
	"sort"
)

// NewGeometryFindings subtracts an exact multiset of observed baseline defects.
// Include coordinates, body bounds and multiplicity: comparing only rule/IDs
// could hide a new bad segment on an already-invalid primitive. Text is not
// evidence (segment indexes can change), but geometry and identities are.
func NewGeometryFindings(before, after []Finding) []Finding {
	key := func(f Finding) string {
		f.Message = ""
		f.Pins = append([]string(nil), f.Pins...)
		sort.Strings(f.Pins)
		b, _ := json.Marshal(f)
		return string(b)
	}
	counts := map[string]int{}
	for _, f := range before {
		counts[key(f)]++
	}
	var out []Finding
	for _, f := range after {
		k := key(f)
		if counts[k] > 0 {
			counts[k]--
		} else {
			out = append(out, f)
		}
	}
	return out
}
