package powersim

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Model kinds.
const (
	KindLoad      = "load"
	KindICSmall   = "ic-small"
	KindLDO       = "ldo"
	KindBuck      = "buck"
	KindDiode     = "diode"
	KindLED       = "led"
	KindSource    = "connector-source"
	KindConnector = "connector"
	KindESD       = "esd"
	KindBJT       = "bjt"
	KindSwitch    = "switch"
	KindResistor  = "resistor"
	KindCapacitor = "capacitor"
	KindInductor  = "inductor"
	KindFerrite   = "ferrite"
	KindFuse      = "fuse"
	KindOpen      = "open" // crystals, MOSFETs without a model, mechanical parts
	KindIgnore    = "ignore"
)

// Match selects the parts a model applies to.
type Match struct {
	MPN       []string `json:"mpn,omitempty"`
	LCSC      []string `json:"lcsc,omitempty"`
	NameRegex string   `json:"nameRegex,omitempty"`
	re        *regexp.Regexp
}

// Rail is one supply rail of a multi-rail load.
type Rail struct {
	Pins  []string `json:"pins"`
	TypA  float64  `json:"typA"`
	PeakA float64  `json:"peakA"`
}

// Regulate is the regulation constraint V(plus) - V(minus) = volts, pins by name.
type Regulate struct {
	Plus  string  `json:"plus"`
	Minus string  `json:"minus"`
	Volts float64 `json:"volts"`
}

// Model is one part power model (data, not code).
type Model struct {
	ID          string              `json:"id"`
	Kind        string              `json:"kind"`
	Match       Match               `json:"match"`
	Description string              `json:"description,omitempty"`
	Pins        map[string][]string `json:"pins,omitempty"` // role → pin names/numbers
	SupplyPins  []string            `json:"supplyPins,omitempty"`
	ReturnPins  []string            `json:"returnPins,omitempty"`
	Rails       []Rail              `json:"rails,omitempty"`
	TypA        float64             `json:"typA,omitempty"`
	PeakA       float64             `json:"peakA,omitempty"`
	// Regulators.
	Vref      float64   `json:"vref,omitempty"`
	Vout      float64   `json:"vout,omitempty"`
	Regulate  *Regulate `json:"regulate,omitempty"`
	Eta       float64   `json:"eta,omitempty"`
	FswHz     float64   `json:"fswHz,omitempty"`
	IqA       float64   `json:"iqA,omitempty"`
	DropoutV  float64   `json:"dropoutV,omitempty"`
	VinMinV   float64   `json:"vinMinV,omitempty"`
	EnThreshV float64   `json:"enThreshV,omitempty"`
	MaxA      float64   `json:"maxA,omitempty"`
	// Diodes / LEDs.
	VfV      float64      `json:"vfV,omitempty"`
	IfA      float64      `json:"ifA,omitempty"`
	N        float64      `json:"n,omitempty"`
	IsA      float64      `json:"isA,omitempty"`
	VfPoints [][2]float64 `json:"vfPoints,omitempty"` // [[I(A), V(V)], ...]
	Color    string       `json:"color,omitempty"`
	// BJT.
	Polarity string  `json:"polarity,omitempty"` // npn | pnp
	Beta     float64 `json:"beta,omitempty"`
	BetaR    float64 `json:"betaR,omitempty"`
	// Sources.
	VoltageV   float64 `json:"voltageV,omitempty"`
	SourceName string  `json:"sourceName,omitempty"`
	// Passives / switches / resistive parts.
	RsOhm     float64 `json:"rsOhm,omitempty"`
	DCROhm    float64 `json:"dcrOhm,omitempty"`
	RonOhm    float64 `json:"ronOhm,omitempty"`
	Momentary *bool   `json:"momentary,omitempty"`
	// Leakage for esd/tvs (typ / peak scenario).
	LeakTypA  float64 `json:"leakTypA,omitempty"`
	LeakPeakA float64 `json:"leakPeakA,omitempty"`
	// GPIO outputs that drive an LED network.
	GpioPins    string  `json:"gpioPins,omitempty"`
	GpioRoutOhm float64 `json:"gpioRoutOhm,omitempty"`
	GpioMaxA    float64 `json:"gpioMaxA,omitempty"`
	// Provenance.
	Source     string `json:"source,omitempty"`
	Confidence string `json:"confidence,omitempty"` // datasheet | approx | assumed
}

// Defaults are the generic-part parameters used when no model matches.
type Defaults struct {
	UnknownICLoadA   float64            `json:"unknownIcLoadAPerSupplyPin,omitempty"`
	LoadKneeV        float64            `json:"loadKneeV,omitempty"`
	GpioRoutOhm      float64            `json:"gpioRoutOhm,omitempty"`
	SwitchRonOhm     float64            `json:"switchRonOhm,omitempty"`
	InductorDCROhm   float64            `json:"inductorDcrOhm,omitempty"`
	FerriteOhm       float64            `json:"ferriteOhm,omitempty"`
	FuseOhm          float64            `json:"fuseOhm,omitempty"`
	ZeroOhm          float64            `json:"zeroOhm,omitempty"`
	SourceRsOhm      float64            `json:"sourceRsOhm,omitempty"`
	SourceVoltageV   float64            `json:"sourceVoltageV,omitempty"`
	DiodeVfV         float64            `json:"diodeVfV,omitempty"`
	DiodeIfA         float64            `json:"diodeIfA,omitempty"`
	SchottkyVfV      float64            `json:"schottkyVfV,omitempty"`
	SchottkyIfA      float64            `json:"schottkyIfA,omitempty"`
	LEDIfA           float64            `json:"ledIfA,omitempty"`
	LEDN             float64            `json:"ledN,omitempty"`
	LEDVf            map[string]float64 `json:"ledVf,omitempty"`
	BuckEta          float64            `json:"buckEta,omitempty"`
	BuckFswHz        float64            `json:"buckFswHz,omitempty"`
	BuckVref         float64            `json:"buckVref,omitempty"`
	LDODropoutV      float64            `json:"ldoDropoutV,omitempty"`
	EnThreshV        float64            `json:"enThreshV,omitempty"`
	ESDLeakA         float64            `json:"esdLeakA,omitempty"`
	BJTBeta          float64            `json:"bjtBeta,omitempty"`
	GpioMaxA         float64            `json:"gpioMaxA,omitempty"`
	Sources          map[string]string  `json:"sources,omitempty"` // provenance per default
	defaultsFromCode bool
}

// Library is an ordered set of power-model files; earlier files win.
type Library struct {
	SchemaVersion int      `json:"schemaVersion"`
	Defaults      Defaults `json:"defaults"`
	Models        []Model  `json:"models"`
	Origin        string   `json:"-"`
}

// builtinDefaults are used when the library omits a default.
func builtinDefaults() Defaults {
	return Defaults{
		UnknownICLoadA: 0.05, LoadKneeV: 0.8, GpioRoutOhm: 40, SwitchRonOhm: 0.05,
		InductorDCROhm: 0.05, FerriteOhm: 0.1, FuseOhm: 0.05, ZeroOhm: 0.001,
		SourceRsOhm: 0.05, SourceVoltageV: 5, DiodeVfV: 0.7, DiodeIfA: 0.01,
		SchottkyVfV: 0.45, SchottkyIfA: 1, LEDIfA: 0.005, LEDN: 2,
		LEDVf:   map[string]float64{"red": 1.9, "yellow": 2.0, "orange": 2.0, "green": 2.1, "green-ingan": 3.0, "blue": 3.0, "white": 3.0},
		BuckEta: 0.85, BuckFswHz: 1e6, BuckVref: 0.6, LDODropoutV: 1.1, EnThreshV: 1.0,
		ESDLeakA: 1e-6, BJTBeta: 100, GpioMaxA: 0.02,
	}
}

// LoadLibrary reads one power-models JSON file.
func LoadLibrary(path string) (*Library, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lib, err := ParseLibrary(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	lib.Origin = path
	return lib, nil
}

// ParseLibrary parses a power-models JSON document.
func ParseLibrary(b []byte) (*Library, error) {
	var lib Library
	if err := json.Unmarshal(b, &lib); err != nil {
		return nil, err
	}
	if lib.SchemaVersion != 0 && lib.SchemaVersion != 1 {
		return nil, fmt.Errorf("unsupported power-models schemaVersion %d", lib.SchemaVersion)
	}
	for i := range lib.Models {
		m := &lib.Models[i]
		if m.ID == "" || m.Kind == "" {
			return nil, fmt.Errorf("model #%d needs id and kind", i)
		}
		if m.Match.NameRegex != "" {
			re, err := regexp.Compile(m.Match.NameRegex)
			if err != nil {
				return nil, fmt.Errorf("model %s nameRegex: %w", m.ID, err)
			}
			m.Match.re = re
		}
	}
	return &lib, nil
}

// Libraries is an override chain: index 0 wins.
type Libraries []*Library

// defaults merges defaults: first library that sets a field wins, then builtin.
func (ls Libraries) defaults() Defaults {
	d := builtinDefaults()
	// Apply in reverse so earlier libraries override later ones.
	for i := len(ls) - 1; i >= 0; i-- {
		mergeDefaults(&d, ls[i].Defaults)
	}
	return d
}

func mergeDefaults(d *Defaults, o Defaults) {
	set := func(dst *float64, v float64) {
		if v != 0 {
			*dst = v
		}
	}
	set(&d.UnknownICLoadA, o.UnknownICLoadA)
	set(&d.LoadKneeV, o.LoadKneeV)
	set(&d.GpioRoutOhm, o.GpioRoutOhm)
	set(&d.SwitchRonOhm, o.SwitchRonOhm)
	set(&d.InductorDCROhm, o.InductorDCROhm)
	set(&d.FerriteOhm, o.FerriteOhm)
	set(&d.FuseOhm, o.FuseOhm)
	set(&d.ZeroOhm, o.ZeroOhm)
	set(&d.SourceRsOhm, o.SourceRsOhm)
	set(&d.SourceVoltageV, o.SourceVoltageV)
	set(&d.DiodeVfV, o.DiodeVfV)
	set(&d.DiodeIfA, o.DiodeIfA)
	set(&d.SchottkyVfV, o.SchottkyVfV)
	set(&d.SchottkyIfA, o.SchottkyIfA)
	set(&d.LEDIfA, o.LEDIfA)
	set(&d.LEDN, o.LEDN)
	set(&d.BuckEta, o.BuckEta)
	set(&d.BuckFswHz, o.BuckFswHz)
	set(&d.BuckVref, o.BuckVref)
	set(&d.LDODropoutV, o.LDODropoutV)
	set(&d.EnThreshV, o.EnThreshV)
	set(&d.ESDLeakA, o.ESDLeakA)
	set(&d.BJTBeta, o.BJTBeta)
	set(&d.GpioMaxA, o.GpioMaxA)
	for k, v := range o.LEDVf {
		d.LEDVf[k] = v
	}
	if len(o.Sources) > 0 {
		if d.Sources == nil {
			d.Sources = map[string]string{}
		}
		for k, v := range o.Sources {
			d.Sources[k] = v
		}
	}
}

// MatchResult is the model chosen for a part and why.
type MatchResult struct {
	Model *Model
	By    string // lcsc | mpn | nameRegex
	Lib   string
}

func eqFold(list []string, v string) bool {
	if v == "" {
		return false
	}
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

// Match finds the best model: earlier libraries win; within one library LCSC
// beats MPN beats name regex.
func (ls Libraries) Match(p *Part) *MatchResult {
	for _, lib := range ls {
		for i := range lib.Models {
			if m := &lib.Models[i]; eqFold(m.Match.LCSC, p.LCSC) {
				return &MatchResult{Model: m, By: "lcsc", Lib: lib.Origin}
			}
		}
		for i := range lib.Models {
			if m := &lib.Models[i]; eqFold(m.Match.MPN, p.MPN) {
				return &MatchResult{Model: m, By: "mpn", Lib: lib.Origin}
			}
		}
		for i := range lib.Models {
			m := &lib.Models[i]
			if m.Match.re == nil {
				continue
			}
			for _, cand := range []string{p.MPN, p.Value, p.DeviceName} {
				if cand != "" && m.Match.re.MatchString(cand) {
					return &MatchResult{Model: m, By: "nameRegex", Lib: lib.Origin}
				}
			}
		}
	}
	return nil
}
