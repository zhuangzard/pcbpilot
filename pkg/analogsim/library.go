package analogsim

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// GenericLib is the embedded copy of the Skill's pcbpilot-generic.lib
// (.agents/skills/pcbpilot/references/spice-models/ — the canonical source;
// TestGenericLibMatchesSkill keeps both identical).
//
//go:embed models/pcbpilot-generic.lib
var GenericLib string

// Match selects parts (same rules as power-models.json).
type Match struct {
	MPN       []string `json:"mpn,omitempty"`
	LCSC      []string `json:"lcsc,omitempty"`
	NameRegex string   `json:"nameRegex,omitempty"`
	re        *regexp.Regexp
}

// OpampModel parameterises PCBPILOT_OPAMP (and comparators).
type OpampModel struct {
	ID               string            `json:"id"`
	Match            Match             `json:"match"`
	Channels         int               `json:"channels,omitempty"`
	GBWHz            float64           `json:"gbwHz,omitempty"`
	SlewVPerUs       float64           `json:"slewVPerUs,omitempty"`
	AolDB            float64           `json:"aolDB,omitempty"`
	VosV             float64           `json:"vosV,omitempty"`
	P2Hz             float64           `json:"p2Hz,omitempty"`
	P2Factor         float64           `json:"p2Factor,omitempty"`
	RRIn             bool              `json:"rrIn,omitempty"`
	RROut            bool              `json:"rrOut,omitempty"`
	HeadroomHighV    float64           `json:"headroomHighV,omitempty"`
	HeadroomLowV     float64           `json:"headroomLowV,omitempty"`
	VcmHeadroomHighV float64           `json:"vcmHeadroomHighV,omitempty"`
	VcmHeadroomLowV  float64           `json:"vcmHeadroomLowV,omitempty"`
	ZoutOhm          float64           `json:"zoutOhm,omitempty"`
	IoutMaxA         float64           `json:"ioutMaxA,omitempty"`
	SupplyMinV       float64           `json:"supplyMinV,omitempty"`
	SupplyMaxV       float64           `json:"supplyMaxV,omitempty"`
	Output           string            `json:"output,omitempty"` // comparators: open-drain | push-pull
	TpdS             float64           `json:"tpdS,omitempty"`
	Pinout           map[string]string `json:"pinout,omitempty"`
	Source           string            `json:"source,omitempty"`
	Confidence       string            `json:"confidence,omitempty"`
}

// AmpModel is a fixed-gain amplifier IC (current sense, instrumentation).
type AmpModel struct {
	ID            string  `json:"id"`
	Kind          string  `json:"kind"` // current-sense | instrumentation
	Match         Match   `json:"match"`
	Gain          float64 `json:"gain,omitempty"`
	GainOffset    float64 `json:"gainOffset,omitempty"`
	GainK         float64 `json:"gainK,omitempty"`
	BWHz          float64 `json:"bwHz,omitempty"`
	GBWHz         float64 `json:"gbwHz,omitempty"`
	HeadroomHighV float64 `json:"headroomHighV,omitempty"`
	HeadroomLowV  float64 `json:"headroomLowV,omitempty"`
	SupplyMinV    float64 `json:"supplyMinV,omitempty"`
	SupplyMaxV    float64 `json:"supplyMaxV,omitempty"`
	Source        string  `json:"source,omitempty"`
	Confidence    string  `json:"confidence,omitempty"`
}

// RefModel is a voltage reference.
type RefModel struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"` // shunt | series
	Match      Match   `json:"match"`
	VzV        float64 `json:"vzV"`
	TolPct     float64 `json:"tolPct,omitempty"`
	IkMinA     float64 `json:"ikMinA,omitempty"`
	IkMaxA     float64 `json:"ikMaxA,omitempty"`
	ZOhm       float64 `json:"zOhm,omitempty"`
	DropoutV   float64 `json:"dropoutV,omitempty"`
	IoutMaxA   float64 `json:"ioutMaxA,omitempty"`
	Source     string  `json:"source,omitempty"`
	Confidence string  `json:"confidence,omitempty"`
}

// ADCModel is a SAR ADC input (sample-and-hold) model.
type ADCModel struct {
	ID           string  `json:"id"`
	Match        Match   `json:"match"`
	PinRegex     string  `json:"pinRegex,omitempty"`
	VrefPinRegex string  `json:"vrefPinRegex,omitempty"`
	Bits         int     `json:"bits,omitempty"`
	CshF         float64 `json:"cshF,omitempty"`
	RadcOhm      float64 `json:"radcOhm,omitempty"`
	CpinF        float64 `json:"cpinF,omitempty"`
	TsampleS     float64 `json:"tsampleS,omitempty"`
	VrefInputA   float64 `json:"vrefInputA,omitempty"`
	Source       string  `json:"source,omitempty"`
	Confidence   string  `json:"confidence,omitempty"`
	pinRe        *regexp.Regexp
	vrefRe       *regexp.Regexp
}

// ResetModel is an MCU reset/enable pin timing requirement.
type ResetModel struct {
	ID          string  `json:"id"`
	Match       Match   `json:"match"`
	PinRegex    string  `json:"pinRegex"`
	VihFraction float64 `json:"vihFraction"`
	MinDelayS   float64 `json:"minDelayS"`
	MinLowS     float64 `json:"minLowS,omitempty"`
	Recommended string  `json:"recommended,omitempty"`
	Source      string  `json:"source,omitempty"`
	Confidence  string  `json:"confidence,omitempty"`
	pinRe       *regexp.Regexp
}

// FETModel is a small MOSFET.
type FETModel struct {
	ID         string  `json:"id"`
	Match      Match   `json:"match"`
	Polarity   string  `json:"polarity"`
	VthV       float64 `json:"vthV"`
	VthMaxV    float64 `json:"vthMaxV"`
	RdsonOhm   float64 `json:"rdsonOhm"`
	RdsonVgsV  float64 `json:"rdsonVgsV"`
	Source     string  `json:"source,omitempty"`
	Confidence string  `json:"confidence,omitempty"`
}

// BJTModel adds switching numbers to the power-models BJT.
type BJTModel struct {
	ID         string  `json:"id"`
	Match      Match   `json:"match"`
	Polarity   string  `json:"polarity"`
	HfeMin     float64 `json:"hfeMin"`
	VbeOnV     float64 `json:"vbeOnV"`
	VceSatV    float64 `json:"vceSatV"`
	IcMaxA     float64 `json:"icMaxA"`
	Source     string  `json:"source,omitempty"`
	Confidence string  `json:"confidence,omitempty"`
}

// VendorModel maps a user-supplied vendor .subckt onto the pin roles.
type VendorModel struct {
	Match  Match  `json:"match"`
	Kind   string `json:"kind"` // opamp (default)
	File   string `json:"file"` // relative to the library folder
	Subckt string `json:"subckt"`
	// PinOrder lists the ROLE of each .subckt port in order: inp, inn, vp,
	// vn, out (extra ports: "nc" → a floating node).
	PinOrder []string `json:"pinOrder"`
	Source   string   `json:"source,omitempty"`
}

// Defaults are used when a part has no entry.
type Defaults struct {
	Opamp                  OpampModel         `json:"opamp"`
	Comparator             OpampModel         `json:"comparator"`
	ResistorTolPct         float64            `json:"resistorTolPct"`
	CapacitorTolPct        float64            `json:"capacitorTolPct"`
	ResistorTolAssumedPct  float64            `json:"resistorTolAssumedPct"`
	CapacitorTolAssumedPct float64            `json:"capacitorTolAssumedPct"`
	StrayPF                float64            `json:"strayPF"`
	PhaseMarginMinDeg      float64            `json:"phaseMarginMinDeg"`
	MCRuns                 int                `json:"mcRuns"`
	Seed                   int                `json:"seed"`
	TargetTolPct           map[string]float64 `json:"targetTolPct"`
	ADC                    ADCModel           `json:"adc"`
	Crystal                struct {
		C0PF       float64 `json:"c0PF"`
		CmFF       float64 `json:"cmFF"`
		ESROhm     float64 `json:"esrOhm"`
		Source     string  `json:"source"`
		Confidence string  `json:"confidence"`
	} `json:"crystal"`
	RampS float64 `json:"rampS"`
}

// Library is analog-models.json.
type Library struct {
	SchemaVersion int           `json:"schemaVersion"`
	Defaults      Defaults      `json:"defaults"`
	Opamps        []OpampModel  `json:"opamps"`
	Comparators   []OpampModel  `json:"comparators"`
	Amplifiers    []AmpModel    `json:"amplifiers"`
	References    []RefModel    `json:"references"`
	ADCs          []ADCModel    `json:"adcs"`
	Resets        []ResetModel  `json:"resets"`
	Mosfets       []FETModel    `json:"mosfets"`
	BJTs          []BJTModel    `json:"bjts"`
	Vendor        []VendorModel `json:"vendorModels"`
	// Dir is the folder the library was loaded from (vendor files resolve here).
	Dir    string `json:"-"`
	Origin string `json:"-"`
}

// LoadLibrary reads analog-models.json.
func LoadLibrary(path string) (*Library, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lib, err := ParseLibrary(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	lib.Dir, lib.Origin = filepath.Dir(path), path
	return lib, nil
}

// ParseLibrary decodes analog-models.json and compiles its regexes.
func ParseLibrary(b []byte) (*Library, error) {
	var lib Library
	if err := json.Unmarshal(b, &lib); err != nil {
		return nil, err
	}
	if lib.SchemaVersion != 1 {
		return nil, fmt.Errorf("analog models: schemaVersion %d unsupported (want 1)", lib.SchemaVersion)
	}
	comp := func(m *Match, id string) error {
		if m.NameRegex == "" {
			return nil
		}
		re, err := regexp.Compile(m.NameRegex)
		if err != nil {
			return fmt.Errorf("%s: nameRegex: %w", id, err)
		}
		m.re = re
		return nil
	}
	for i := range lib.Opamps {
		if err := comp(&lib.Opamps[i].Match, lib.Opamps[i].ID); err != nil {
			return nil, err
		}
	}
	for i := range lib.Comparators {
		if err := comp(&lib.Comparators[i].Match, lib.Comparators[i].ID); err != nil {
			return nil, err
		}
	}
	for i := range lib.Amplifiers {
		if err := comp(&lib.Amplifiers[i].Match, lib.Amplifiers[i].ID); err != nil {
			return nil, err
		}
	}
	for i := range lib.References {
		if err := comp(&lib.References[i].Match, lib.References[i].ID); err != nil {
			return nil, err
		}
	}
	for i := range lib.ADCs {
		a := &lib.ADCs[i]
		if err := comp(&a.Match, a.ID); err != nil {
			return nil, err
		}
		var err error
		if a.PinRegex != "" {
			if a.pinRe, err = regexp.Compile(a.PinRegex); err != nil {
				return nil, fmt.Errorf("%s: pinRegex: %w", a.ID, err)
			}
		}
		if a.VrefPinRegex != "" {
			if a.vrefRe, err = regexp.Compile(a.VrefPinRegex); err != nil {
				return nil, fmt.Errorf("%s: vrefPinRegex: %w", a.ID, err)
			}
		}
	}
	for i := range lib.Resets {
		r := &lib.Resets[i]
		if err := comp(&r.Match, r.ID); err != nil {
			return nil, err
		}
		var err error
		if r.pinRe, err = regexp.Compile(r.PinRegex); err != nil {
			return nil, fmt.Errorf("%s: pinRegex: %w", r.ID, err)
		}
	}
	for i := range lib.Mosfets {
		if err := comp(&lib.Mosfets[i].Match, lib.Mosfets[i].ID); err != nil {
			return nil, err
		}
	}
	for i := range lib.BJTs {
		if err := comp(&lib.BJTs[i].Match, lib.BJTs[i].ID); err != nil {
			return nil, err
		}
	}
	for i := range lib.Vendor {
		if err := comp(&lib.Vendor[i].Match, lib.Vendor[i].Subckt); err != nil {
			return nil, err
		}
	}
	d := &lib.Defaults
	if d.Opamp.GBWHz == 0 {
		d.Opamp = OpampModel{ID: "generic-opamp", GBWHz: 1e6, SlewVPerUs: 0.5, AolDB: 100, VosV: 0.002, P2Factor: 3, HeadroomHighV: 0.1, HeadroomLowV: 0.1, VcmHeadroomHighV: 1, ZoutOhm: 50, IoutMaxA: 0.02, Confidence: "assumed", Source: "pcbpilot generic op-amp"}
	}
	if d.Opamp.ID == "" {
		d.Opamp.ID = "generic-opamp"
	}
	if d.Comparator.ID == "" {
		d.Comparator.ID = "generic-comparator"
	}
	def := func(p *float64, v float64) {
		if *p == 0 {
			*p = v
		}
	}
	def(&d.ResistorTolPct, 1)
	def(&d.CapacitorTolPct, 10)
	def(&d.ResistorTolAssumedPct, 5)
	def(&d.CapacitorTolAssumedPct, 20)
	def(&d.StrayPF, 3)
	def(&d.PhaseMarginMinDeg, 45)
	def(&d.RampS, 1e-3)
	if d.MCRuns == 0 {
		d.MCRuns = 100
	}
	if d.Seed == 0 {
		d.Seed = 1
	}
	if d.TargetTolPct == nil {
		d.TargetTolPct = map[string]float64{}
	}
	for k, v := range map[string]float64{"gain": 2, "fcHz": 10, "q": 10, "voutV": 3, "thresholdV": 5, "clPF": 10} {
		if d.TargetTolPct[k] == 0 {
			d.TargetTolPct[k] = v
		}
	}
	if d.ADC.Bits == 0 {
		d.ADC = ADCModel{ID: "generic-sar", Bits: 12, CshF: 10e-12, RadcOhm: 1000, TsampleS: 1e-6, Confidence: "assumed", Source: "pcbpilot generic 12-bit SAR input"}
	}
	if d.ADC.ID == "" {
		d.ADC.ID = "generic-sar"
	}
	def(&d.Crystal.C0PF, 3)
	def(&d.Crystal.CmFF, 10)
	def(&d.Crystal.ESROhm, 60)
	if d.Crystal.Confidence == "" {
		d.Crystal.Confidence = "assumed"
	}
	return &lib, nil
}

// EmptyLibrary is the built-in fallback (generic defaults only).
func EmptyLibrary() *Library {
	lib, _ := ParseLibrary([]byte(`{"schemaVersion":1}`))
	return lib
}

func eqFold(list []string, v string) bool {
	v = strings.TrimSpace(v)
	if v == "" {
		return false
	}
	for _, s := range list {
		if strings.EqualFold(strings.TrimSpace(s), v) {
			return true
		}
	}
	return false
}

// matchScore ranks a match: 3 = LCSC, 2 = MPN, 1 = regex, 0 = none.
func (m *Match) score(p *powersim.Part) int {
	if eqFold(m.LCSC, p.LCSC) {
		return 3
	}
	if eqFold(m.MPN, p.MPN) || eqFold(m.MPN, p.Value) || eqFold(m.MPN, p.DeviceName) {
		return 2
	}
	if m.re != nil {
		for _, c := range []string{p.MPN, p.Value, p.DeviceName} {
			if c != "" && m.re.MatchString(c) {
				return 1
			}
		}
	}
	return 0
}

func (l *Library) opamp(p *powersim.Part) *OpampModel {
	best, bs := (*OpampModel)(nil), 0
	for i := range l.Opamps {
		if s := l.Opamps[i].Match.score(p); s > bs {
			best, bs = &l.Opamps[i], s
		}
	}
	return best
}

func (l *Library) comparator(p *powersim.Part) *OpampModel {
	best, bs := (*OpampModel)(nil), 0
	for i := range l.Comparators {
		if s := l.Comparators[i].Match.score(p); s > bs {
			best, bs = &l.Comparators[i], s
		}
	}
	return best
}

func (l *Library) amplifier(p *powersim.Part) *AmpModel {
	best, bs := (*AmpModel)(nil), 0
	for i := range l.Amplifiers {
		if s := l.Amplifiers[i].Match.score(p); s > bs {
			best, bs = &l.Amplifiers[i], s
		}
	}
	return best
}

func (l *Library) reference(p *powersim.Part) *RefModel {
	best, bs := (*RefModel)(nil), 0
	for i := range l.References {
		if s := l.References[i].Match.score(p); s > bs {
			best, bs = &l.References[i], s
		}
	}
	return best
}

func (l *Library) adc(p *powersim.Part) *ADCModel {
	best, bs := (*ADCModel)(nil), 0
	for i := range l.ADCs {
		if s := l.ADCs[i].Match.score(p); s > bs {
			best, bs = &l.ADCs[i], s
		}
	}
	return best
}

func (l *Library) reset(p *powersim.Part) *ResetModel {
	best, bs := (*ResetModel)(nil), 0
	for i := range l.Resets {
		if s := l.Resets[i].Match.score(p); s > bs {
			best, bs = &l.Resets[i], s
		}
	}
	return best
}

func (l *Library) mosfet(p *powersim.Part) *FETModel {
	best, bs := (*FETModel)(nil), 0
	for i := range l.Mosfets {
		if s := l.Mosfets[i].Match.score(p); s > bs {
			best, bs = &l.Mosfets[i], s
		}
	}
	return best
}

func (l *Library) bjt(p *powersim.Part) *BJTModel {
	best, bs := (*BJTModel)(nil), 0
	for i := range l.BJTs {
		if s := l.BJTs[i].Match.score(p); s > bs {
			best, bs = &l.BJTs[i], s
		}
	}
	return best
}

func (l *Library) vendor(p *powersim.Part) *VendorModel {
	best, bs := (*VendorModel)(nil), 0
	for i := range l.Vendor {
		if s := l.Vendor[i].Match.score(p); s > bs {
			best, bs = &l.Vendor[i], s
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Standard (stocked) parts: standard-parts.json.

// StockLib is the parsed standard-parts.json (passives only are used).
type StockLib struct {
	Parts []StockPart
	vals  []stockVal
}

type stockVal struct {
	kind  string
	value float64
	pkg   string
	part  StockPart
}

// ParseStock decodes standard-parts.json.
func ParseStock(b []byte) (*StockLib, error) {
	var raw struct {
		Parts map[string]struct {
			Value      string `json:"value"`
			MPN        string `json:"mpn"`
			LCSC       string `json:"lcsc"`
			DeviceUUID string `json:"deviceUuid"`
			Footprint  string `json:"footprint"`
			Basic      bool   `json:"basic"`
		} `json:"parts"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	s := &StockLib{}
	for key, p := range raw.Parts {
		kind := ""
		switch {
		case strings.HasPrefix(key, "res."):
			kind = "R"
		case strings.HasPrefix(key, "cap."):
			kind = "C"
		case strings.HasPrefix(key, "ind."):
			kind = "L"
		default:
			continue
		}
		v, ok := parseValue(p.Value)
		if !ok {
			continue
		}
		sp := StockPart{Key: key, LCSC: p.LCSC, MPN: p.MPN, DeviceUUID: p.DeviceUUID, Footprint: p.Footprint, Basic: p.Basic, Value: p.Value}
		pkg := packageOf(p.Footprint + " " + key + " " + p.MPN)
		s.Parts = append(s.Parts, sp)
		s.vals = append(s.vals, stockVal{kind: kind, value: v, pkg: pkg, part: sp})
	}
	return s, nil
}

// LoadStock reads standard-parts.json.
func LoadStock(path string) (*StockLib, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseStock(b)
}

// Values lists the stocked values of a kind within [lo, hi].
func (s *StockLib) Values(kind string, lo, hi float64) []float64 {
	if s == nil {
		return nil
	}
	var out []float64
	for _, sv := range s.vals {
		if sv.kind == kind && sv.value >= lo && sv.value <= hi {
			out = append(out, sv.value)
		}
	}
	return out
}

// Find returns a stocked part of kind with value v (±0.1 %), preferring the
// same package and JLC basic parts.
func (s *StockLib) Find(kind string, v float64, pkg string) *StockPart {
	if s == nil || v <= 0 {
		return nil
	}
	var best *StockPart
	bestScore := -1
	for i := range s.vals {
		sv := &s.vals[i]
		if sv.kind != kind || math.Abs(sv.value-v)/v > 0.001 {
			continue
		}
		score := 0
		if pkg != "" && sv.pkg == pkg {
			score += 2
		} else if pkg != "" && sv.pkg != "" && sv.pkg != pkg {
			continue // wrong package: not a drop-in
		}
		if sv.part.Basic {
			score++
		}
		if score > bestScore {
			p := sv.part
			best, bestScore = &p, score
		}
	}
	return best
}

// Has reports whether a value is stocked.
func (s *StockLib) Has(kind string, v float64, pkg string) bool { return s.Find(kind, v, pkg) != nil }

var rePkg = regexp.MustCompile(`(?i)(0201|0402|0603|0805|1206|1210|2512)`)

// packageOf infers an SMD chip size from a footprint / MPN / key.
func packageOf(s string) string {
	u := strings.ToUpper(s)
	switch {
	case strings.Contains(u, "CL05") || strings.Contains(u, "GRM15"):
		return "0402"
	case strings.Contains(u, "CL10") || strings.Contains(u, "GRM18"):
		return "0603"
	case strings.Contains(u, "CL21") || strings.Contains(u, "GRM21"):
		return "0805"
	case strings.Contains(u, "CL31") || strings.Contains(u, "GRM31"):
		return "1206"
	}
	if m := rePkg.FindString(u); m != "" {
		return m
	}
	return ""
}
