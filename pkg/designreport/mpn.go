package designreport

import (
	"math"
	"regexp"
	"strconv"
	"strings"
)

// CapInfo is what a capacitor MPN (or value string) encodes.
type CapInfo struct {
	Package    string  `json:"package,omitempty"`
	Dielectric string  `json:"dielectric,omitempty"`
	Farad      float64 `json:"farad,omitempty"`
	RatedV     float64 `json:"ratedV,omitempty"`
	TolPct     float64 `json:"tolPct,omitempty"`
	Decoder    string  `json:"decoder"`
}

// ResInfo is what a resistor MPN (or value string) encodes.
type ResInfo struct {
	Package string  `json:"package,omitempty"`
	Ohm     float64 `json:"ohm,omitempty"`
	TolPct  float64 `json:"tolPct,omitempty"`
	Decoder string  `json:"decoder"`
}

// ResistorPackageW is the generic thick-film chip-resistor power rating at
// 70 °C ambient (derate linearly to 0 at 155 °C above that), by EIA size.
var ResistorPackageW = map[string]float64{
	"0201": 1.0 / 20, "0402": 1.0 / 16, "0603": 1.0 / 10, "0805": 1.0 / 8,
	"1206": 1.0 / 4, "1210": 1.0 / 2, "2010": 3.0 / 4, "2512": 1,
}

// Samsung CL series.
var (
	reSamsungCap   = regexp.MustCompile(`^CL(03|05|10|21|31|32|43|55)([A-Z])(\d{2}[0-9]|\dR\d|R\d{2})([A-Z])([A-Z])`)
	samsungSize    = map[string]string{"03": "0201", "05": "0402", "10": "0603", "21": "0805", "31": "1206", "32": "1210", "43": "1812", "55": "2220"}
	samsungDiel    = map[string]string{"A": "X5R", "B": "X7R", "C": "C0G", "F": "Y5V", "X": "X6S", "Y": "X7S", "Z": "X7T"}
	samsungVoltage = map[string]float64{"S": 2.5, "R": 4, "Q": 6.3, "P": 10, "O": 16, "A": 25, "L": 35, "B": 50, "C": 100, "D": 200, "E": 250, "G": 500, "H": 630, "I": 1000, "J": 2000, "K": 3000}
	// Murata GRM/GCM/GRT: size, thickness, dielectric (2), voltage (2), value, tolerance.
	reMurataCap   = regexp.MustCompile(`^G(?:RM|CM|RT|CJ|JM)(\d{2})(\w)(\w\w)(\w\w)(\d{2}[0-9]|\dR\d|R\d{2})([A-Z])`)
	murataSize    = map[string]string{"02": "01005", "03": "0201", "15": "0402", "18": "0603", "21": "0805", "31": "1206", "32": "1210", "43": "1812", "55": "2220"}
	murataDiel    = map[string]string{"5C": "C0G", "R6": "X5R", "R7": "X7R", "C6": "X5S", "C7": "X7S", "C8": "X6S", "D7": "X7T", "E7": "X7U", "F5": "Y5V"}
	murataVoltage = map[string]float64{"0E": 2.5, "0G": 4, "0J": 6.3, "1A": 10, "1C": 16, "1E": 25, "YA": 35, "1V": 35, "1H": 50, "2A": 100, "2D": 200, "2E": 250, "2W": 450, "2J": 630, "3A": 1000}
	// Yageo CC series: CC0402KRX7R9BB104.
	reYageoCap   = regexp.MustCompile(`^CC(0201|0402|0603|0805|1206|1210)([A-Z])R(NPO|X5R|X7R|Y5V|X7S)(\d)BB(\d{2}[0-9]|\dR\d)`)
	yageoVoltage = map[string]float64{"4": 4, "5": 6.3, "6": 10, "7": 16, "8": 25, "9": 50, "0": 100}
	capTol       = map[string]float64{"B": 0.1, "C": 0.25, "D": 0.5, "F": 1, "G": 2, "J": 5, "K": 10, "M": 20, "Z": 80}

	// UNI-ROYAL (UniOhm) 0402WGF1002TCE: size, W, power letter, tolerance, 4-digit code.
	reUniOhm = regexp.MustCompile(`^(0201|0402|0603|0805|1206|1210|2010|2512)W([A-Z])([BCDFGJK])([0-9R]{4})T[A-Z0-9]{2}$`)
	// Yageo RC0402FR-0710KL.
	reYageoRes = regexp.MustCompile(`^RC(0201|0402|0603|0805|1206|1210|2010|2512)([BCDFGJK])R-\d{2}([0-9RKM]+)L$`)
	resTol     = map[string]float64{"B": 0.1, "C": 0.25, "D": 0.5, "F": 1, "G": 2, "J": 5, "K": 10}

	rePackage   = regexp.MustCompile(`\b(0201|0402|0603|0805|1206|1210|2010|2512)\b`)
	reCapValue  = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(p|n|u|µ|μ|m)F\b`)
	reVoltValue = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*V\b`)
	reResValue  = regexp.MustCompile(`(?i)^(\d+(?:\.\d+)?)\s*([kKmM]?)\s*(?:Ω|ohm|R)?$|^(\d+)([kKmMR])(\d+)$`)
)

// pfCode decodes an EIA 3-character capacitance code in pF ("104" = 100 nF,
// "1R0" = 1.0 pF).
func pfCode(code string) (float64, bool) {
	if strings.Contains(code, "R") {
		v, err := strconv.ParseFloat(strings.Replace(code, "R", ".", 1), 64)
		return v, err == nil
	}
	if len(code) != 3 {
		return 0, false
	}
	sig, err1 := strconv.Atoi(code[:2])
	exp, err2 := strconv.Atoi(code[2:])
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return float64(sig) * math.Pow(10, float64(exp)), true
}

// ParseCapacitor decodes a capacitor MPN (Samsung CL, Murata GRM/GCM, Yageo
// CC) or, failing that, a value string such as "10uF 25V 0805".
func ParseCapacitor(mpn, value string) (CapInfo, bool) {
	m := strings.ToUpper(strings.TrimSpace(mpn))
	if s := reSamsungCap.FindStringSubmatch(m); s != nil {
		pf, ok := pfCode(s[3])
		if v, vok := samsungVoltage[s[5]]; ok && vok {
			return CapInfo{Package: samsungSize[s[1]], Dielectric: samsungDiel[s[2]], Farad: pf * 1e-12, RatedV: v, TolPct: capTol[s[4]], Decoder: "Samsung CL part number"}, true
		}
	}
	if s := reMurataCap.FindStringSubmatch(m); s != nil {
		pf, ok := pfCode(s[5])
		if v, vok := murataVoltage[s[4]]; ok && vok {
			return CapInfo{Package: murataSize[s[1]], Dielectric: murataDiel[s[3]], Farad: pf * 1e-12, RatedV: v, TolPct: capTol[s[6]], Decoder: "Murata GRM part number"}, true
		}
	}
	if s := reYageoCap.FindStringSubmatch(m); s != nil {
		pf, ok := pfCode(s[5])
		if v, vok := yageoVoltage[s[4]]; ok && vok {
			d := s[3]
			if d == "NPO" {
				d = "C0G"
			}
			return CapInfo{Package: s[1], Dielectric: d, Farad: pf * 1e-12, RatedV: v, TolPct: capTol[s[2]], Decoder: "Yageo CC part number"}, true
		}
	}
	// Value string: needs both capacitance and a voltage to be useful.
	info := CapInfo{Decoder: "value string"}
	for _, s := range []string{value, mpn} {
		if c := reCapValue.FindStringSubmatch(s); c != nil && info.Farad == 0 {
			f, _ := strconv.ParseFloat(c[1], 64)
			info.Farad = f * siPrefix(c[2])
		}
		if v := reVoltValue.FindStringSubmatch(s); v != nil && info.RatedV == 0 {
			info.RatedV, _ = strconv.ParseFloat(v[1], 64)
		}
		if p := rePackage.FindString(s); p != "" && info.Package == "" {
			info.Package = p
		}
	}
	return info, info.RatedV > 0 || info.Farad > 0
}

func siPrefix(p string) float64 {
	switch strings.ToLower(p) {
	case "p":
		return 1e-12
	case "n":
		return 1e-9
	case "u", "µ", "μ":
		return 1e-6
	case "m":
		return 1e-3
	case "k":
		return 1e3
	}
	return 1
}

// resCode decodes a 4-character resistance code: 3 significant digits and a
// multiplier ("1002" = 10 kΩ, "0472" = 4.7 kΩ) or an R decimal ("10R0").
func resCode(code string) (float64, bool) {
	if strings.Contains(code, "R") {
		v, err := strconv.ParseFloat(strings.Replace(code, "R", ".", 1), 64)
		return v, err == nil
	}
	if len(code) != 4 {
		return 0, false
	}
	sig, err1 := strconv.Atoi(code[:3])
	exp, err2 := strconv.Atoi(code[3:])
	if err1 != nil || err2 != nil {
		return 0, false
	}
	return float64(sig) * math.Pow(10, float64(exp)), true
}

// ParseResistor decodes a resistor MPN (UNI-ROYAL, Yageo RC) or a value
// string ("4.7k", "10kΩ 0402").
func ParseResistor(mpn, value string) (ResInfo, bool) {
	m := strings.ToUpper(strings.TrimSpace(mpn))
	if s := reUniOhm.FindStringSubmatch(m); s != nil {
		if ohm, ok := resCode(s[4]); ok {
			return ResInfo{Package: s[1], Ohm: ohm, TolPct: resTol[s[3]], Decoder: "UNI-ROYAL part number"}, true
		}
	}
	if s := reYageoRes.FindStringSubmatch(m); s != nil {
		if ohm, ok := parseOhm(s[3]); ok {
			return ResInfo{Package: s[1], Ohm: ohm, TolPct: resTol[s[2]], Decoder: "Yageo RC part number"}, true
		}
	}
	info := ResInfo{Decoder: "value string"}
	for _, s := range []string{value, mpn} {
		for _, tok := range strings.Fields(s) {
			if ohm, ok := parseOhm(tok); ok && info.Ohm == 0 {
				info.Ohm = ohm
			}
		}
		if p := rePackage.FindString(s); p != "" && info.Package == "" {
			info.Package = p
		}
	}
	return info, info.Ohm > 0 || info.Package != ""
}

// parseOhm reads "10k", "4K7", "100R", "45.3kΩ", "1M".
func parseOhm(s string) (float64, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(s, "Ω"), "ohm"))
	m := reResValue.FindStringSubmatch(s)
	if m == nil {
		return 0, false
	}
	if m[1] != "" {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			return 0, false
		}
		switch m[2] {
		case "k", "K":
			v *= 1e3
		case "M":
			v *= 1e6
		case "m":
			v *= 1e-3
		}
		return v, true
	}
	whole, frac := m[3], m[5]
	mult := 1.0
	switch strings.ToUpper(m[4]) {
	case "K":
		mult = 1e3
	case "M":
		mult = 1e6
	}
	v, err := strconv.ParseFloat(whole+"."+frac, 64)
	return v * mult, err == nil
}

// fmtSI formats a value with an SI prefix and unit ("22 µF", "4.7 kΩ").
func fmtSI(v float64, unit string) string {
	if v == 0 {
		return "0 " + unit
	}
	type p struct {
		f float64
		s string
	}
	for _, x := range []p{{1e6, "M"}, {1e3, "k"}, {1, ""}, {1e-3, "m"}, {1e-6, "µ"}, {1e-9, "n"}, {1e-12, "p"}} {
		if math.Abs(v) >= x.f*0.9999 {
			return strconv.FormatFloat(round(v/x.f, 3), 'g', 4, 64) + " " + x.s + unit
		}
	}
	return strconv.FormatFloat(v, 'g', 3, 64) + " " + unit
}

func round(v float64, digits int) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	p := math.Pow(10, float64(digits))
	return math.Round(v*p) / p
}

// DescRatings are ratings read from an LCSC-style attribute description
// ("Current Rating:17A Voltage Rating (Max):250V Power(Watts):62.5mW").
type DescRatings struct {
	CurrentA float64 `json:"currentA,omitempty"`
	VoltageV float64 `json:"voltageV,omitempty"`
	PowerW   float64 `json:"powerW,omitempty"`
}

var (
	reDescCurrent = regexp.MustCompile(`(?i)(?:current rating|rated current|current - (?:max|rated))[^:]*:\s*([\d.]+)\s*(mA|A)\b`)
	reDescVoltage = regexp.MustCompile(`(?i)(?:voltage rating|rated voltage|voltage rated|voltage - rated)[^:]*:\s*([\d.]+)\s*(kV|V)\b`)
	reDescPower   = regexp.MustCompile(`(?i)power\s*\(watts\)\s*:\s*(?:([\d.]+)\s*(mW|W)|(\d+)/(\d+)\s*W)`)
)

// ParseDescRatings extracts current / voltage / power ratings from a part
// description of LCSC attributes.
func ParseDescRatings(desc string) (DescRatings, bool) {
	var d DescRatings
	if m := reDescCurrent.FindStringSubmatch(desc); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		if strings.EqualFold(m[2], "mA") {
			v /= 1000
		}
		d.CurrentA = v
	}
	if m := reDescVoltage.FindStringSubmatch(desc); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		if strings.EqualFold(m[2], "kV") {
			v *= 1000
		}
		d.VoltageV = v
	}
	if m := reDescPower.FindStringSubmatch(desc); m != nil {
		if m[1] != "" {
			v, _ := strconv.ParseFloat(m[1], 64)
			if strings.EqualFold(m[2], "mW") {
				v /= 1000
			}
			d.PowerW = v
		} else {
			a, _ := strconv.ParseFloat(m[3], 64)
			b, _ := strconv.ParseFloat(m[4], 64)
			if b > 0 {
				d.PowerW = a / b
			}
		}
	}
	return d, d.CurrentA > 0 || d.VoltageV > 0 || d.PowerW > 0
}
