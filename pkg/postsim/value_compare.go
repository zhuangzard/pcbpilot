package postsim

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// sameIdentity compares what simBoardIdentity picked: footprint values on a
// KiCad board (sameValue), manufacturer ids otherwise (sameDevice).
func sameIdentity(kicad bool, sim, board string) bool {
	if kicad {
		return sameValue(sim, board)
	}
	return sameDevice(sim, board)
}

var (
	// 4k7, 4K7, 2u2, 4R7, 1M5: the prefix is the decimal point.
	reValInfix = regexp.MustCompile(`^([0-9]+)([pnuµμmkKMGR])([0-9]+)(Ω|ohms?|F|H)?$`)
	// 100n, 100nF, 4.7k, 4.7kΩ, 10R, 1M, 1m, 25MHz, 3.3V.
	reValPlain = regexp.MustCompile(`^([0-9]*\.?[0-9]+)([pnuµμmkKMG]?)(Ω|ohms?|R|F|H|Hz|V|A)?$`)
)

// valuePrefix is case-sensitive: m is milli, M mega (K is kilo by the
// common 4K7 convention; no SI prefix uses K).
var valuePrefix = map[string]float64{"": 1, "p": 1e-12, "n": 1e-9, "u": 1e-6, "µ": 1e-6, "μ": 1e-6,
	"m": 1e-3, "k": 1e3, "K": 1e3, "M": 1e6, "G": 1e9}

// parseElectrical reads a component value as a number and a unit ("" when
// the text gives none; R, ohm and Ω are all "Ω"). ok=false: not a value.
func parseElectrical(s string) (v float64, unit string, ok bool) {
	s = strings.ReplaceAll(strings.TrimSpace(s), " ", "")
	norm := func(u string) string {
		if u == "R" || strings.HasPrefix(u, "ohm") {
			return "Ω"
		}
		return u
	}
	if m := reValInfix.FindStringSubmatch(s); m != nil {
		f, err := strconv.ParseFloat(m[1]+"."+m[3], 64)
		if err != nil {
			return 0, "", false
		}
		if m[2] == "R" {
			return f, "Ω", true
		}
		return f * valuePrefix[m[2]], norm(m[4]), true
	}
	if m := reValPlain.FindStringSubmatch(s); m != nil {
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, "", false
		}
		return f * valuePrefix[m[2]], norm(m[3]), true
	}
	return 0, "", false
}

// sameValue compares two footprint values electrically: 4k7 = 4.7k =
// 4700Ω, 100nF = 100n, but 1M (megohm) ≠ 1m (milliohm). Two values with
// different units differ; one without a unit takes the other's. Text that
// is not a plain value ("10k NTC", "DNP") must match exactly.
func sameValue(a, b string) bool {
	va, ua, oka := parseElectrical(a)
	vb, ub, okb := parseElectrical(b)
	if !oka || !okb {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	if ua != "" && ub != "" && ua != ub {
		return false
	}
	return math.Abs(va-vb) <= 1e-9*math.Max(math.Abs(va), math.Abs(vb))
}
