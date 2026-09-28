package analogsim

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

func parseValue(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "Ω", "")
	s = strings.ReplaceAll(s, "ohm", "")
	return powersim.ParseValue(s)
}

var (
	reDescR   = regexp.MustCompile(`(?i)Resistance:\s*([0-9.]+\s*[pnuµμmkKMG]?)\s*Ω`)
	reDescC   = regexp.MustCompile(`(?i)Capacitance:\s*([0-9.]+\s*[pnuµμmkKMG]?)F`)
	reDescL   = regexp.MustCompile(`(?i)Inductance:\s*([0-9.]+\s*[pnuµμmkKMG]?)H`)
	reDescTol = regexp.MustCompile(`(?i)Tolerance:\s*±?\s*([0-9.]+)\s*%`)
	reDescCL  = regexp.MustCompile(`(?i)(Load Capacitance|负载电容)[:：]?\s*([0-9.]+)\s*pF`)
	reDescFq  = regexp.MustCompile(`(?i)(Frequency|频率)[:：]?\s*([0-9.]+)\s*([kKM]?)Hz`)
	reValFq   = regexp.MustCompile(`(?i)^([0-9.]+)\s*([kKM])Hz`)
	reDiel    = regexp.MustCompile(`(?i)\b(C0G|NP0|X7R|X5R|X7S|X6S|Y5V)\b`)
)

// partValue is the numeric value of a two-pin passive (R, C, L).
func partValue(p *powersim.Part, kind string) (float64, string, bool) {
	for _, s := range []string{p.Value, p.DeviceName} {
		if s == "" || strings.HasPrefix(s, "=") {
			continue
		}
		if v, ok := parseValue(s); ok {
			return v, s, true
		}
	}
	re := map[string]*regexp.Regexp{"R": reDescR, "C": reDescC, "L": reDescL}[kind]
	if re != nil {
		if m := re.FindStringSubmatch(p.Description); m != nil {
			if v, ok := parseValue(m[1]); ok {
				return v, m[1] + map[string]string{"R": "Ω", "C": "F", "L": "H"}[kind], true
			}
		}
	}
	return 0, "", false
}

// tolerance returns the part tolerance (%) and where it came from.
func tolerance(p *powersim.Part, kind string, d *Defaults) (float64, string) {
	if m := reDescTol.FindStringSubmatch(p.Description); m != nil {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 0 {
			return v, "description"
		}
	}
	mpn := strings.ToUpper(p.MPN)
	switch kind {
	case "R":
		// UniOhm 0402WGF1002TCE: 7th char F=1 %, J=5 %, D=0.5 %, B=0.1 %.
		if len(mpn) >= 7 && strings.HasPrefix(mpn[4:6], "WG") {
			if t, ok := map[byte]float64{'F': 1, 'J': 5, 'D': 0.5, 'B': 0.1, 'G': 2}[mpn[6]]; ok {
				return t, "mpn"
			}
		}
		// Yageo RC0402FR-0710KL: F/J after the size.
		if strings.HasPrefix(mpn, "RC") && len(mpn) >= 7 {
			if t, ok := map[byte]float64{'F': 1, 'J': 5, 'D': 0.5, 'B': 0.1}[mpn[6]]; ok {
				return t, "mpn"
			}
		}
		if d != nil {
			return d.ResistorTolAssumedPct, "assumed"
		}
		return 5, "assumed"
	case "C":
		// Samsung CL05B104KO5NNNC: 8th char tolerance code; Murata GRM…K…
		if strings.HasPrefix(mpn, "CL") && len(mpn) >= 9 {
			if t, ok := map[byte]float64{'K': 10, 'M': 20, 'J': 5, 'F': 1, 'G': 2, 'C': 0, 'B': 0}[mpn[8]]; ok && t > 0 {
				return t, "mpn"
			}
		}
		if strings.HasPrefix(mpn, "GRM") {
			if i := strings.IndexAny(mpn[6:], "KMJ"); i >= 0 {
				if t, ok := map[byte]float64{'K': 10, 'M': 20, 'J': 5}[mpn[6+i]]; ok {
					return t, "mpn"
				}
			}
		}
		if m := reDiel.FindString(strings.ToUpper(p.Description + " " + p.Value)); m != "" {
			switch m {
			case "C0G", "NP0":
				return 5, "dielectric"
			case "Y5V":
				return 20, "dielectric"
			}
			return 10, "dielectric"
		}
		if d != nil {
			return d.CapacitorTolAssumedPct, "assumed"
		}
		return 20, "assumed"
	case "L":
		return 20, "assumed"
	}
	return 5, "assumed"
}

// crystalSpec returns the crystal frequency (Hz) and load capacitance (pF).
func crystalSpec(p *powersim.Part) (fHz, clPF float64) {
	if m := reDescCL.FindStringSubmatch(p.Description); m != nil {
		clPF, _ = strconv.ParseFloat(m[2], 64)
	}
	if m := reDescFq.FindStringSubmatch(p.Description); m != nil {
		v, _ := strconv.ParseFloat(m[2], 64)
		fHz = v * map[string]float64{"": 1, "k": 1e3, "K": 1e3, "M": 1e6}[m[3]]
	}
	for _, s := range []string{p.Value, p.MPN, p.DeviceName} {
		if fHz == 0 {
			if m := reValFq.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
				v, _ := strconv.ParseFloat(m[1], 64)
				fHz = v * map[string]float64{"k": 1e3, "K": 1e3, "M": 1e6}[m[2]]
			}
		}
		if clPF == 0 {
			if m := regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*pF`).FindStringSubmatch(s); m != nil {
				clPF, _ = strconv.ParseFloat(m[1], 64)
			}
		}
	}
	return fHz, clPF
}
