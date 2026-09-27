package powersim

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// 4K7, 1R5, 2u2, 0R — multiplier as decimal point.
	reInfix = regexp.MustCompile(`^([0-9]+)([pnuµμmkKMGR])([0-9]+)\s*(Ω|ohm|ohms|F|H)?$`)
	// 10k, 4.7kΩ, 2.2uH, 100nF, 0.1 uF, 10 Ω, 5.1K
	rePlain = regexp.MustCompile(`^([0-9]*\.?[0-9]+(?:[eE][-+]?[0-9]+)?)\s*([pnuµμmkKMG]?)\s*(Ω|ohm|ohms|R|F|H|A|V|Hz)?$`)
)

func multiplier(m string) float64 {
	switch m {
	case "p":
		return 1e-12
	case "n":
		return 1e-9
	case "u", "µ", "μ":
		return 1e-6
	case "m":
		return 1e-3
	case "k", "K":
		return 1e3
	case "M":
		return 1e6
	case "G":
		return 1e9
	}
	return 1
}

// ParseValue parses an SI-prefixed component value such as "10kΩ", "4K7",
// "2.2uH", "100nF", "0R", "1R5" or "22µF". It returns ok=false when the text is
// not a plain value. The unit (Ω/F/H) is ignored; callers know the kind.
func ParseValue(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	if f := strings.Fields(s); len(f) > 1 {
		if v, ok := ParseValue(strings.Join(f, "")); ok {
			return v, true
		}
		return ParseValue(f[0])
	}
	// Tolerance / rating suffixes after a separator: "10k 1%" → "10k".
	for _, sep := range []string{"±", "/", ",", ";", "@"} {
		if i := strings.Index(s, sep); i > 0 {
			s = s[:i]
		}
	}
	if m := reInfix.FindStringSubmatch(s); m != nil {
		v, err := strconv.ParseFloat(m[1]+"."+m[3], 64)
		if err != nil {
			return 0, false
		}
		if m[2] == "R" {
			return v, true
		}
		return v * multiplier(m[2]), true
	}
	if m := rePlain.FindStringSubmatch(s); m != nil {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, false
		}
		return v * multiplier(m[2]), true
	}
	// "0R" / "10R"
	if strings.HasSuffix(s, "R") {
		if v, err := strconv.ParseFloat(strings.TrimSuffix(s, "R"), 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

var reDescValue = map[string]*regexp.Regexp{
	"resistance":  regexp.MustCompile(`(?i)Resistance:\s*([0-9.]+\s*[pnuµμmkKMG]?)\s*Ω`),
	"capacitance": regexp.MustCompile(`(?i)Capacitance:\s*([0-9.]+\s*[pnuµμmkKMG]?)F`),
	"inductance":  regexp.MustCompile(`(?i)Inductance:\s*([0-9.]+\s*[pnuµμmkKMG]?)H`),
	"dcr":         regexp.MustCompile(`(?i)DC Resistance(?:\s*\(DCR\))?:\s*([0-9.]+\s*[pnuµμmkKMG]?)Ω`),
}

// valueFromDescription extracts "Resistance:45.3kΩ"-style LCSC attributes.
func valueFromDescription(desc, key string) (float64, bool) {
	re := reDescValue[key]
	if re == nil || desc == "" {
		return 0, false
	}
	m := re.FindStringSubmatch(desc)
	if m == nil {
		return 0, false
	}
	return ParseValue(m[1])
}
