package powersim

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// binding is the model resolved for one part.
type binding struct {
	part       *Part
	kind       string
	model      Model
	matched    *MatchResult
	confidence string
	why        string
	sourceName string // connector-source scenario key
}

var (
	reGroundNet = regexp.MustCompile(`(?i)^([A-Z0-9]+_)?(A|D|P|S|C|E)?GND[A-Z0-9_]*$|^VSS[A-Z0-9_]*$|^GROUND$|^EARTH$|^0V$`)
	rePowerName = regexp.MustCompile(`(?i)^\+?(VCC|VDD|VBUS|VIN|VBAT|VSYS|VOUT|VCORE|VIO|AVDD|DVDD|PVDD|V[0-9]|[0-9]+V[0-9]*)|(^|_)[0-9]+V[0-9]*($|_)|VBUS|VIN`)
	reInputNet  = regexp.MustCompile(`(?i)VBUS|VIN|_IN$|^IN_|TERM|DC_?IN|VBAT|BAT\+|VEXT|EXT_?PWR`)
	reRefPrefix = regexp.MustCompile(`^[A-Za-z]+`)
	reVoutMPN   = regexp.MustCompile(`[-_ ]([0-9]\.[0-9]{1,2})(?:[^0-9]|$)|[-_ ]([0-9])V([0-9])`)
	reSchottky  = regexp.MustCompile(`(?i)schottky|^SS[0-9]{2}|^SK[0-9]{2}|^B5[0-9]{3}|^1N58[0-9]{2}|^BAT[0-9]`)
)

func (e *Engine) isGround(net string) bool {
	if net == "" {
		return false
	}
	if e.d.NetRole[net] == "ground" {
		return true
	}
	return reGroundNet.MatchString(net)
}

// powerLike reports whether a net looks like a supply rail.
func (e *Engine) powerLike(net string) bool {
	if net == "" || e.isGround(net) {
		return false
	}
	return e.d.NetRole[net] == "power" || pcbauto.InferVoltage(net) > 0 || rePowerName.MatchString(net)
}

// pinsNamed returns the part's pins whose name or number is in names.
func (p *Part) pinsNamed(names []string) []Pin {
	var out []Pin
	for _, pin := range p.Pins {
		for _, n := range names {
			n = strings.TrimSpace(n)
			if strings.EqualFold(pin.Name, n) || pin.Number == n || "#"+pin.Number == n {
				out = append(out, pin)
				break
			}
		}
	}
	return out
}

func (e *Engine) groundPins(p *Part) []Pin {
	var out []Pin
	for _, pin := range p.Pins {
		if e.isGround(pin.Net) {
			out = append(out, pin)
		}
	}
	return out
}

func roleNames(m Model, role string, fallback ...string) []string {
	if v, ok := m.Pins[role]; ok && len(v) > 0 {
		return v
	}
	return fallback
}

var (
	defIn      = []string{"IN", "VIN", "PVIN", "VI", "INPUT", "VIN1"}
	defLX      = []string{"LX", "SW", "PH", "SW1"}
	defLDOOut  = []string{"VOUT", "OUT", "VO", "OUTPUT"}
	defGnd     = []string{"GND", "PGND", "AGND", "VSS", "EP", "EPAD", "PAD", "GND/ADJ"}
	defFB      = []string{"FB", "VFB", "VOS"}
	defEN      = []string{"EN", "CE", "SHDN", "ON/OFF", "EN/UVLO", "RUN"}
	defAnode   = []string{"A", "+", "ANODE", "AN", "A1"}
	defCathode = []string{"K", "-", "C", "CATHODE", "KA", "K1"}
	// Bridge rectifiers (MB10S, GBU, DB10x, KBP): AC inputs and DC outputs.
	defBridgeAC    = []string{"~", "~1", "~2", "AC", "AC1", "AC2", "AC~", "IN1", "IN2"}
	defBridgePlus  = []string{"+", "DC+", "V+", "PLUS", "OUT+"}
	defBridgeMinus = []string{"-", "DC-", "V-", "MINUS", "OUT-"}
)

func defaultModel(kind string) Model { return Model{ID: "generic-" + kind, Kind: kind} }

// bindAll resolves a model for every part.
func (e *Engine) bindAll() {
	e.byRef = map[string]*binding{}
	for _, p := range e.d.Parts {
		b := &binding{part: p}
		if mr := e.libs.Match(p); mr != nil {
			b.matched = mr
			b.model = *mr.Model
			b.kind = mr.Model.Kind
			b.confidence = mr.Model.Confidence
			if b.confidence == "" {
				b.confidence = "approx"
			}
			key := p.LCSC
			if mr.By == "mpn" || (mr.By == "nameRegex" && p.MPN != "") {
				key = p.MPN
			}
			if mr.By == "nameRegex" && p.MPN == "" {
				key = p.Value
			}
			b.why = fmt.Sprintf("%s %s → model %s", mr.By, key, mr.Model.ID)
		} else {
			e.bindGeneric(b)
		}
		if b.kind == KindICSmall {
			b.kind = KindLoad
		}
		if b.kind == "tvs" {
			b.kind = KindESD
		}
		if b.kind == "schottky" {
			b.kind = KindDiode
		}
		e.binds = append(e.binds, b)
		e.byRef[p.Ref] = b
	}
	// Source names (scenario keys), unique.
	used := map[string]int{}
	for _, b := range e.binds {
		if b.kind != KindSource {
			continue
		}
		name := b.model.SourceName
		if name == "" {
			name = strings.ToLower(b.part.Ref)
			if len(b.part.pinsNamed([]string{"VBUS"})) > 0 {
				name = "usb"
			}
		}
		used[name]++
		if used[name] > 1 {
			name = name + "-" + strings.ToLower(b.part.Ref)
		}
		b.sourceName = name
		e.sources = append(e.sources, b)
	}
}

func (e *Engine) bindGeneric(b *binding) {
	p := b.part
	prefix := strings.ToUpper(reRefPrefix.FindString(p.Ref))
	desc := strings.ToLower(strings.Join([]string{p.Description, p.MPN, p.Value, p.DeviceName}, " "))
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(desc, w) {
				return true
			}
		}
		return false
	}
	b.confidence = "assumed"
	set := func(kind, why string) {
		b.kind = kind
		b.model = defaultModel(kind)
		b.why = "generic: " + why
	}
	switch {
	case prefix == "LED" || (prefix == "D" && has("led", "light emitting")):
		set(KindLED, "LED by ref/description")
	case prefix == "R" && len(p.Pins) == 2:
		set(KindResistor, "resistor by ref prefix R")
		b.confidence = "value"
	case prefix == "C" && len(p.Pins) <= 2:
		set(KindCapacitor, "capacitor (open at DC)")
		b.confidence = "value"
	case prefix == "FB" || (prefix == "L" && has("ferrite", "bead")):
		set(KindFerrite, "ferrite bead")
	case prefix == "L":
		set(KindInductor, "inductor by ref prefix L")
	case prefix == "F" || prefix == "PTC":
		set(KindFuse, "fuse")
	case (prefix == "RT" || prefix == "NTC" || prefix == "TH") && len(p.Pins) == 2:
		// Thermistors (inrush NTC, protection PTC) are resistors at DC. The
		// default made "RT1 on VIN_HV" an unknown-IC load of 50 mA.
		if _, ok := partValue(p, "resistance"); ok {
			set(KindResistor, "thermistor by ref prefix (cold resistance)")
			b.confidence = "value"
		} else {
			set(KindFuse, "thermistor with no value (small series resistance)")
		}
	case prefix == "RV" || prefix == "MOV" || prefix == "VDR" || prefix == "ZNR" || has("varistor", "mov "):
		// A varistor below its clamping voltage leaks µA: open at DC. Left to
		// the default it became an "unknown IC" load of 50 mA per pin.
		set(KindOpen, "varistor (leakage neglected, open at DC)")
	case (prefix == "BR" || prefix == "DB") && len(p.Pins) >= 4:
		set(KindBridge, "bridge rectifier by ref prefix")
	case prefix == "T" || prefix == "TR":
		// Energy crosses a transformer only while it switches: no DC path.
		set(KindOpen, "transformer (no DC transfer — model its windings as a multi-output source)")
		e.warnf("%s: transformer %s has no power model — its windings supply nothing in the DC simulation (add a connector-source model with outputs[])", p.Ref, firstNonEmpty(p.MPN, p.Value, p.DeviceName))
	case (prefix == "D" || prefix == "TVS" || prefix == "ZD") && has("tvs", "esd", "transient", "smaj", "smbj"):
		set(KindESD, "TVS/ESD by description")
	case prefix == "D" || prefix == "ZD":
		set(KindDiode, "diode by ref prefix D")
		if reSchottky.MatchString(p.MPN) || has("schottky") {
			b.model.VfV, b.model.IfA = e.def.SchottkyVfV, e.def.SchottkyIfA
			b.why += " (schottky)"
		}
	case prefix == "Q" && has("mosfet", "n-ch", "p-ch", "nmos", "pmos"):
		set(KindOpen, "MOSFET not modelled (open)")
		e.warnf("%s: MOSFET %s is not modelled — treated as open", p.Ref, p.MPN)
	case prefix == "Q":
		set(KindBJT, "BJT by ref prefix Q")
		if has("pnp") {
			b.model.Polarity = "pnp"
		}
	case prefix == "SW" || prefix == "S" || prefix == "K" || prefix == "KEY" || prefix == "BTN":
		set(KindSwitch, "switch (momentary assumed)")
	case prefix == "J" || prefix == "CN" || prefix == "P" || prefix == "USB" || prefix == "CON" || prefix == "X" && has("conn"):
		set(KindConnector, "connector")
		var power []Pin
		for _, pin := range p.Pins {
			if strings.EqualFold(pin.Name, "VBUS") || (e.powerLike(pin.Net) && reInputNet.MatchString(pin.Net)) {
				power = append(power, pin)
			}
		}
		if len(power) > 0 && len(e.groundPins(p)) > 0 {
			b.kind = KindSource
			b.why = "generic: connector with input-rail pin " + power[0].Net + " → assumed supply input"
			var names []string
			for _, pin := range power {
				names = append(names, "#"+pin.Number)
			}
			b.model.Pins = map[string][]string{"power": names}
			if has("usb", "type-c", "typec") || strings.EqualFold(power[0].Name, "VBUS") {
				b.model.SourceName = "usb"
			}
		}
	case prefix == "Y" || prefix == "X" || prefix == "XTAL" || prefix == "OSC" && len(p.Pins) <= 2:
		set(KindOpen, "crystal (open at DC)")
	case prefix == "H" || prefix == "MH" || prefix == "TP" || prefix == "MK" || prefix == "FID" || len(p.Pins) == 0:
		set(KindIgnore, "mechanical/test point")
	case has("ldo", "low dropout", "linear regulator", "linear voltage regulator"):
		set(KindLDO, "LDO by description")
		if m := reVoutMPN.FindStringSubmatch(p.MPN); m != nil {
			if m[1] != "" {
				b.model.Vout, _ = strconv.ParseFloat(m[1], 64)
			} else {
				b.model.Vout, _ = strconv.ParseFloat(m[2]+"."+m[3], 64)
			}
		}
		if b.model.Vout == 0 {
			e.warnf("%s: LDO %s output voltage unknown (add a power model) — treated as a load", p.Ref, p.MPN)
			set(KindLoad, "unknown LDO → assumed load")
		}
	case has("step-down", "buck", "dc-dc step"):
		set(KindBuck, "buck by description")
	default:
		set(KindLoad, "unknown IC → assumed load")
	}
}

func (e *Engine) warnf(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, w := range e.warnings {
		if w == msg {
			return
		}
	}
	e.warnings = append(e.warnings, msg)
}

func (e *Engine) assumef(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	for _, w := range e.assumptions {
		if w == msg {
			return
		}
	}
	e.assumptions = append(e.assumptions, msg)
}

// partValue returns the numeric value of a passive (Ω/F/H) from Value, then
// the LCSC Description attribute.
func partValue(p *Part, descKey string) (float64, bool) {
	if v, ok := ParseValue(p.Value); ok {
		return v, true
	}
	if v, ok := valueFromDescription(p.Description, descKey); ok {
		return v, true
	}
	if v, ok := ParseValue(p.DeviceName); ok {
		return v, true
	}
	return 0, false
}

// diodeParams returns Shockley Is and n for a diode/LED model.
func diodeParams(m Model, vf, ifA, nDefault float64) (float64, float64) {
	if m.IsA > 0 {
		n := m.N
		if n == 0 {
			n = nDefault
		}
		return m.IsA, n
	}
	if len(m.VfPoints) >= 2 {
		pts := append([][2]float64(nil), m.VfPoints...)
		sort.Slice(pts, func(i, j int) bool { return pts[i][0] < pts[j][0] })
		a, b := pts[0], pts[len(pts)-1]
		n := (b[1] - a[1]) / (thermalVoltage * math.Log(b[0]/a[0]))
		if n > 0.5 && n < 10 {
			return a[0] / (math.Exp(a[1]/(n*thermalVoltage)) - 1), n
		}
	}
	if len(m.VfPoints) == 1 {
		ifA, vf = m.VfPoints[0][0], m.VfPoints[0][1]
	}
	n := m.N
	if n == 0 {
		n = nDefault
	}
	return ifA / (math.Exp(vf/(n*thermalVoltage)) - 1), n
}

var ledColorWords = []struct{ word, color string }{
	{"white", "white"}, {"blue", "blue"}, {"green", "green"}, {"yellow", "yellow"},
	{"amber", "yellow"}, {"orange", "orange"}, {"red", "red"},
	{"白", "white"}, {"蓝", "blue"}, {"绿", "green"}, {"黄", "yellow"}, {"橙", "orange"}, {"红", "red"},
}

var reLEDSuffix = regexp.MustCompile(`(?i)[-_0-9]([RYGBW])([-_/].*)?$`)

// ledColor infers the LED colour and how it was found.
func ledColor(m Model, p *Part) (string, string) {
	if m.Color != "" {
		return strings.ToLower(m.Color), "model"
	}
	text := strings.ToLower(p.Description + " " + p.Value)
	for _, w := range ledColorWords {
		if strings.Contains(text, w.word) {
			return w.color, "description"
		}
	}
	if mm := reLEDSuffix.FindStringSubmatch(p.MPN); mm != nil {
		switch strings.ToUpper(mm[1]) {
		case "R":
			return "red", "MPN suffix"
		case "Y":
			return "yellow", "MPN suffix"
		case "G":
			return "green", "MPN suffix"
		case "B":
			return "blue", "MPN suffix"
		case "W":
			return "white", "MPN suffix"
		}
	}
	return "", ""
}
