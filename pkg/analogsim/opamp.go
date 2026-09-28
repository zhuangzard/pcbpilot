package analogsim

import (
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// opampInst is one op-amp / comparator channel.
type opampInst struct {
	Ref, Ch       string
	Inp, Inn, Out string
	Vp, Vn        string
	Pins          map[string]string // role → pin number
	Model         OpampModel
	Vendor        *VendorModel
	Comparator    bool
	OpenDrain     bool
	FromLibrary   bool
	PinoutFromLib bool
}

func (o *opampInst) id() string {
	if o.Ch == "" {
		return o.Ref
	}
	return o.Ref + ":" + o.Ch
}

var (
	reChan = regexp.MustCompile(`^([A-D]|[1-4])?$`)
)

// normPin upper-cases and strips separators.
func normPin(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "_", "", "/", "", "(", "", ")", "").Replace(s)
	return s
}

func chanKey(s string) string {
	switch s {
	case "1":
		return "A"
	case "2":
		return "B"
	case "3":
		return "C"
	case "4":
		return "D"
	}
	return s
}

// opampPinRole maps an op-amp symbol pin name to a role (inp, inn, out, vp,
// vn) and a channel letter ("" = single / shared).
func opampPinRole(name string) (role, ch string) {
	s := normPin(name)
	switch s {
	case "V+", "VCC", "VDD", "VS+", "+VS", "VS", "VCC+", "+V", "V+VCC", "VDDA", "AVDD":
		return "vp", ""
	case "V-", "VEE", "VSS", "GND", "VS-", "-VS", "VCC-", "-V", "AGND", "V-GND", "VSSA":
		return "vn", ""
	}
	// Outputs: OUT, OUTA, 1OUT, OUT1, VOUT, VOUTA, OUTPUT.
	for _, tok := range []string{"OUTPUT", "VOUT", "OUT"} {
		if i := strings.Index(s, tok); i >= 0 {
			rest := s[:i] + s[i+len(tok):]
			if reChan.MatchString(rest) && !strings.HasPrefix(s, "D") {
				return "out", chanKey(rest)
			}
			break
		}
	}
	// Inputs: IN+, +IN, INA+, 1IN-, -INA, IN1-, VIN+, INP, INN, IN-A.
	t := strings.Replace(s, "VIN", "IN", 1)
	if i := strings.Index(t, "IN"); i >= 0 {
		rest := t[:i] + t[i+2:]
		sign := ""
		switch {
		case strings.Contains(rest, "+"):
			sign, rest = "+", strings.Replace(rest, "+", "", 1)
		case strings.Contains(rest, "-"):
			sign, rest = "-", strings.Replace(rest, "-", "", 1)
		case strings.HasPrefix(rest, "P") && len(rest) <= 2 && i == 0:
			sign, rest = "+", rest[1:]
		case strings.HasPrefix(rest, "N") && len(rest) <= 2 && i == 0:
			sign, rest = "-", rest[1:]
		case strings.HasPrefix(rest, "M") && len(rest) <= 2 && i == 0:
			sign, rest = "-", rest[1:]
		}
		if sign != "" && reChan.MatchString(rest) {
			if sign == "+" {
				return "inp", chanKey(rest)
			}
			return "inn", chanKey(rest)
		}
	}
	return "", ""
}

var (
	pinoutDual8 = map[string]string{"1": "out:A", "2": "inn:A", "3": "inp:A", "4": "vn", "5": "inp:B", "6": "inn:B", "7": "out:B", "8": "vp"}
	pinoutQuad  = map[string]string{"1": "out:A", "2": "inn:A", "3": "inp:A", "4": "vp", "5": "inp:B", "6": "inn:B", "7": "out:B",
		"8": "out:C", "9": "inn:C", "10": "inp:C", "11": "vn", "12": "inp:D", "13": "inn:D", "14": "out:D"}
	pinoutSingle8 = map[string]string{"2": "inn", "3": "inp", "4": "vn", "6": "out", "7": "vp"}
	reComparator  = regexp.MustCompile(`(?i)comparator|比较器`)
	reOpampDesc   = regexp.MustCompile(`(?i)op[- ]?amp|operational amplifier|运算放大器|运放`)
)

// findOpamps returns every op-amp / comparator channel in the design.
func (c *circuit) findOpamps() ([]*opampInst, []string) {
	var out []*opampInst
	var notes []string
	for _, p := range c.d.Parts {
		if len(p.Pins) < 5 || passiveKind(p) != "" {
			continue
		}
		model := c.lib.opamp(p)
		comp := c.lib.comparator(p)
		isComp := comp != nil
		if model == nil && comp == nil {
			if c.lib.amplifier(p) != nil || c.lib.adc(p) != nil || c.lib.reference(p) != nil {
				continue
			}
		}
		roles := map[string]map[string]string{} // ch → role → pin
		shared := map[string]string{}
		for _, pin := range p.Pins {
			r, ch := opampPinRole(pin.Name)
			if r == "" {
				continue
			}
			if r == "vp" || r == "vn" {
				if _, ok := shared[r]; !ok {
					shared[r] = pin.Number
				}
				continue
			}
			if roles[ch] == nil {
				roles[ch] = map[string]string{}
			}
			roles[ch][r] = pin.Number
		}
		fromPinout := false
		lm := model
		if lm == nil {
			lm = comp
		}
		complete := func() bool {
			n := 0
			for _, m := range roles {
				if m["inp"] != "" && m["inn"] != "" && m["out"] != "" {
					n++
				}
			}
			return n > 0 && shared["vp"] != "" && shared["vn"] != ""
		}
		if !complete() && lm != nil {
			// Numeric pin names: use the library pinout, else the standard one.
			po := lm.Pinout
			if po == nil {
				switch {
				case lm.Channels == 2 && len(p.Pins) == 8:
					po = pinoutDual8
				case lm.Channels == 4 && len(p.Pins) == 14:
					po = pinoutQuad
				case lm.Channels == 1 && len(p.Pins) == 8:
					po = pinoutSingle8
				}
			}
			if po != nil {
				roles, shared = map[string]map[string]string{}, map[string]string{}
				for num, rc := range po {
					r, ch, _ := strings.Cut(rc, ":")
					if r == "vp" || r == "vn" {
						shared[r] = num
						continue
					}
					if roles[ch] == nil {
						roles[ch] = map[string]string{}
					}
					roles[ch][r] = num
				}
				fromPinout = true
			}
		}
		if !complete() {
			continue
		}
		if lm == nil {
			// Unmatched part with op-amp-shaped pins: accept U/IC/AR refs or an
			// op-amp / comparator description.
			ref := strings.ToUpper(p.Ref)
			desc := p.Description + " " + p.DeviceName + " " + p.MPN
			if !(strings.HasPrefix(ref, "U") || strings.HasPrefix(ref, "IC") || strings.HasPrefix(ref, "AR")) && !reOpampDesc.MatchString(desc) && !reComparator.MatchString(desc) {
				continue
			}
			isComp = reComparator.MatchString(desc)
		}
		net := map[string]string{}
		for _, pin := range p.Pins {
			net[pin.Number] = pin.Net
		}
		chs := make([]string, 0, len(roles))
		for ch := range roles {
			chs = append(chs, ch)
		}
		sort.Strings(chs)
		multi := len(chs) > 1
		for _, ch := range chs {
			m := roles[ch]
			if m["inp"] == "" || m["inn"] == "" || m["out"] == "" {
				continue
			}
			o := &opampInst{Ref: p.Ref, Inp: net[m["inp"]], Inn: net[m["inn"]], Out: net[m["out"]], Vp: net[shared["vp"]], Vn: net[shared["vn"]],
				Pins: map[string]string{"inp": m["inp"], "inn": m["inn"], "out": m["out"], "vp": shared["vp"], "vn": shared["vn"]}, Comparator: isComp, PinoutFromLib: fromPinout}
			if multi {
				o.Ch = ch
			}
			switch {
			case model != nil:
				o.Model, o.FromLibrary = *model, true
			case comp != nil:
				o.Model, o.FromLibrary = *comp, true
				o.OpenDrain = comp.Output == "open-drain"
			case isComp:
				o.Model = c.lib.Defaults.Comparator
				o.OpenDrain = c.lib.Defaults.Comparator.Output == "open-drain"
			default:
				o.Model = c.lib.Defaults.Opamp
			}
			fillOpampDefaults(&o.Model, &c.lib.Defaults.Opamp)
			o.Vendor = c.lib.vendor(p)
			if o.Out == "" || o.Inp == "" || o.Inn == "" {
				notes = append(notes, o.id()+": channel has an unconnected input/output pin — not analysed")
				continue
			}
			out = append(out, o)
		}
	}
	return out, notes
}

func fillOpampDefaults(m, d *OpampModel) {
	def := func(p *float64, v float64) {
		if *p == 0 {
			*p = v
		}
	}
	def(&m.GBWHz, d.GBWHz)
	def(&m.SlewVPerUs, d.SlewVPerUs)
	def(&m.AolDB, d.AolDB)
	def(&m.ZoutOhm, d.ZoutOhm)
	def(&m.IoutMaxA, d.IoutMaxA)
	if m.HeadroomHighV == 0 && m.HeadroomLowV == 0 {
		m.HeadroomHighV, m.HeadroomLowV = d.HeadroomHighV, d.HeadroomLowV
	}
	if m.P2Hz == 0 {
		f := m.P2Factor
		if f == 0 {
			f = d.P2Factor
		}
		if f == 0 {
			f = 3
		}
		m.P2Hz = f * m.GBWHz
	}
	if m.Confidence == "" {
		m.Confidence = "assumed"
	}
}

// partOf returns the design part.
func (c *circuit) partOf(ref string) *powersim.Part { return c.parts[ref] }
