package pcbauto

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

var (
	reCapUnit = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*([pnuµμm])F?\b|(\d+)([pnuµμ])(\d+)`)
	// Manufacturer part numbers with an EIA three-digit value code (pF):
	// Samsung CL05B104KO5NNNC, Murata GRM155R71C104KA88D, Yageo CC0402KRX7R9BB104.
	reCapSamsung = regexp.MustCompile(`^CL\d{2}[A-Z](\d{3})`)
	reCapMurata  = regexp.MustCompile(`^GR[MJ]\d{3}[A-Z0-9]{2}\d[A-Z](\d{3})`)
	reCapYageo   = regexp.MustCompile(`^CC\d{4}[A-Z]{2}[A-Z0-9]{3}\d[A-Z]{2}(\d{3})$`)
)

// CapFarads reads a capacitance from a device string ("100nF", "4u7",
// "CL05B104KO5NNNC"); 0 when it cannot tell.
func CapFarads(dev string) float64 {
	d := strings.ToUpper(strings.TrimSpace(dev))
	for _, re := range []*regexp.Regexp{reCapSamsung, reCapMurata, reCapYageo} {
		if m := re.FindStringSubmatch(d); m != nil {
			return eia3(m[1])
		}
	}
	if m := reCapUnit.FindStringSubmatch(dev); m != nil {
		var v float64
		var unit string
		if m[1] != "" {
			v, _ = strconv.ParseFloat(m[1], 64)
			unit = m[2]
		} else { // 4u7 style
			v, _ = strconv.ParseFloat(m[3]+"."+m[5], 64)
			unit = m[4]
		}
		switch strings.ToLower(unit) {
		case "p":
			return v * 1e-12
		case "n":
			return v * 1e-9
		case "u", "µ", "μ":
			return v * 1e-6
		case "m":
			return v * 1e-3
		}
	}
	return 0
}

func eia3(code string) float64 {
	sig, _ := strconv.Atoi(code[:2])
	exp, _ := strconv.Atoi(code[2:])
	return float64(sig) * math.Pow(10, float64(exp)) * 1e-12
}

// decapClass grades a decoupling cap by the frequency band it serves:
// the smaller the cap, the higher its useful band and the more the loop
// inductance (distance to the pin) matters.
//
//	hf    ≤ 220 nF  — must sit at the pin (≤ ~1 mm), via-in-pad or short stub
//	mid   ≤ 2.2 µF  — second ring, within a few mm
//	bulk  > 2.2 µF  — low-frequency reservoir, anywhere on the rail near the IC
func decapClass(dev string) (class string, weight, slack float64) {
	f := CapFarads(dev)
	switch {
	case f == 0 || f <= 220e-9:
		return "hf", 7, 25
	case f <= 2.2e-6:
		return "mid", 5, 60
	default:
		return "bulk", 2.5, 180
	}
}
