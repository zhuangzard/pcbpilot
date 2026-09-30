package pcbauto

// Aesthetics style profiles. A profile changes ONLY soft objectives of the
// lowest constraint tier (ConstraintPriority rank 7): the aesthetics group
// weight, per-metric weights inside the group, alignment tolerances / target
// grid, whether detected symmetry is required, and the slack budget a later
// generator may spend to buy looks. It can never touch safety, electrical,
// manufacturing/DRC or completion — ParseAesStyle rejects any such key.
//
// The shape is the future `aesthetics` object of pcbpilot.project.json
// (v07/console adds that file with a strict schema); until then the same
// object is read from a standalone `--style-file`.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// AesSlack is what a generator may spend to buy alignment / symmetry
// (Phase B/C). Phase A only reports it.
type AesSlack struct {
	WirelengthPct float64 `json:"wirelengthPct"`
	AreaPct       float64 `json:"areaPct"`
	ExtraVias     int     `json:"extraVias"`
}

// AesProfile is one aesthetics style.
type AesProfile struct {
	Name string `json:"name"`
	// Weight is the aesthetics group weight in the joint score once it is
	// scored (Phase C). Phase A applies AestheticsWeight = 0 regardless.
	Weight float64 `json:"weight"`
	// MetricWeights weigh P1–P9 / R1–R9 inside their group (default 1).
	MetricWeights    map[string]float64 `json:"metricWeights,omitempty"`
	AlignTolMil      float64            `json:"alignTolMil"`      // P1/P2 "same line"
	NearMissTolMil   float64            `json:"nearMissTolMil"`   // P1 "almost aligned" band
	PlacementGridMil float64            `json:"placementGridMil"` // P9 target grid
	GridBlend        float64            `json:"gridBlend"`        // P9: share of the target grid in the score (rest: 5 mil audit)
	SymmetryRequired bool               `json:"symmetryRequired"` // P4: worst group governs
	Slack            AesSlack           `json:"slack"`
	// Auto is set when the profile was chosen by the complexity index.
	Auto *AesAuto `json:"auto,omitempty"`
}

// AesAuto records why `auto` chose a preset.
type AesAuto struct {
	Index   float64            `json:"index"` // 0 simple/slack … 1 complex/dense
	Chosen  string             `json:"chosen"`
	Reason  string             `json:"reason"`
	Factors map[string]float64 `json:"factors"`
}

// AesProfiles is THE preset table.
var AesProfiles = map[string]AesProfile{
	// Dense / complex boards: looks must not cost a single mil of wire or
	// area; loose tolerances so near-aligned is good enough.
	"functional": {Name: "functional", Weight: 0.05, AlignTolMil: 4, NearMissTolMil: 20, PlacementGridMil: 5, GridBlend: 0,
		MetricWeights: map[string]float64{"P2": 0.5, "P4": 0.5, "P9": 0.5, "R5": 0.5, "R6": 0.5, "R7": 0.5}},
	// Default. Reproduces the Phase A baseline (baseline.md) exactly.
	"balanced": {Name: "balanced", Weight: 0.10, AlignTolMil: 2, NearMissTolMil: 15, PlacementGridMil: 25, GridBlend: 0.3,
		Slack: AesSlack{WirelengthPct: 2}},
	// "German / obsessive" tidiness: 25 mil grid, tight alignment, symmetry
	// required when detected; may spend +5 % wire, +3 % area and 4 vias.
	"precision": {Name: "precision", Weight: 0.20, AlignTolMil: 1, NearMissTolMil: 10, PlacementGridMil: 25, GridBlend: 1, SymmetryRequired: true,
		MetricWeights: map[string]float64{"P1": 1.5, "P3": 1.5, "P4": 2, "P9": 1.5, "R1": 1.5, "R2": 1.5, "R3": 1.5},
		Slack:         AesSlack{WirelengthPct: 5, AreaPct: 3, ExtraVias: 4}},
}

// DefaultAesProfile is the profile used when none is selected.
const DefaultAesProfile = "balanced"

// AesProfileByName returns a preset copy ("" = default). "custom" needs a
// style file; "auto" needs a board (AutoAesProfile).
func AesProfileByName(name string) (AesProfile, error) {
	if name == "" {
		name = DefaultAesProfile
	}
	p, ok := AesProfiles[name]
	if !ok {
		names := make([]string, 0, len(AesProfiles))
		for k := range AesProfiles {
			names = append(names, k)
		}
		sort.Strings(names)
		return AesProfile{}, fmt.Errorf("unknown aesthetics profile %q (presets: %s, custom via a style file, auto)", name, strings.Join(names, ", "))
	}
	return p.clone(), nil
}

func (p AesProfile) clone() AesProfile {
	q := p
	if p.MetricWeights != nil {
		q.MetricWeights = map[string]float64{}
		for k, v := range p.MetricWeights {
			q.MetricWeights[k] = v
		}
	}
	return q
}

// metricWeight is the in-group weight of a metric (default 1).
func (p *AesProfile) metricWeight(id string) float64 {
	if p == nil || p.MetricWeights == nil {
		return 1
	}
	if w, ok := p.MetricWeights[id]; ok {
		return w
	}
	return 1
}

var aesMetricIDs = map[string]bool{"P1": true, "P2": true, "P3": true, "P4": true, "P5": true, "P6": true, "P7": true, "P8": true, "P9": true,
	"R1": true, "R2": true, "R3": true, "R4": true, "R5": true, "R6": true, "R7": true, "R8": true, "R9": true}

// Validate keeps every field inside the soft, aesthetics-only range.
func (p *AesProfile) Validate() error {
	var errs []string
	chk := func(ok bool, f string, a ...any) {
		if !ok {
			errs = append(errs, fmt.Sprintf(f, a...))
		}
	}
	chk(p.Weight >= 0 && p.Weight <= 0.3, "weight %.3g outside [0, 0.30]: aesthetics may never outweigh the hard tiers", p.Weight)
	for k, v := range p.MetricWeights {
		chk(aesMetricIDs[k], "metricWeights: %q is not an aesthetics metric (P1–P9, R1–R9)", k)
		chk(v >= 0 && v <= 3, "metricWeights[%s] %.3g outside [0, 3]", k, v)
	}
	chk(p.AlignTolMil > 0 && p.AlignTolMil <= 20, "alignTolMil %.3g outside (0, 20]", p.AlignTolMil)
	chk(p.NearMissTolMil >= p.AlignTolMil && p.NearMissTolMil <= 50, "nearMissTolMil %.3g outside [alignTolMil, 50]", p.NearMissTolMil)
	grid := false
	for _, g := range []float64{1, 2.5, 5, 10, 25, 50, 100} {
		grid = grid || p.PlacementGridMil == g
	}
	chk(grid, "placementGridMil %.3g not one of 1, 2.5, 5, 10, 25, 50, 100", p.PlacementGridMil)
	chk(p.GridBlend >= 0 && p.GridBlend <= 1, "gridBlend %.3g outside [0, 1]", p.GridBlend)
	chk(p.Slack.WirelengthPct >= 0 && p.Slack.WirelengthPct <= 10, "slack.wirelengthPct %.3g outside [0, 10]", p.Slack.WirelengthPct)
	chk(p.Slack.AreaPct >= 0 && p.Slack.AreaPct <= 10, "slack.areaPct %.3g outside [0, 10]", p.Slack.AreaPct)
	chk(p.Slack.ExtraVias >= 0 && p.Slack.ExtraVias <= 20, "slack.extraVias %d outside [0, 20]", p.Slack.ExtraVias)
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("aesthetics profile %q: %s", p.Name, strings.Join(errs, "; "))
	}
	return nil
}

// aesHardTokens name hard-constraint quantities (tiers 1–4). A style key
// containing one is rejected, whatever its value.
var aesHardTokens = []struct {
	tok  string
	tier int
}{
	{"creepage", 1}, {"isolation", 1}, {"insulation", 1}, {"slot", 1}, {"moat", 1}, {"edge", 1}, {"keepout", 1}, {"safety", 1},
	{"clearance", 3}, {"width", 2}, {"neck", 2}, {"current", 2}, {"ampacity", 2}, {"impedance", 2}, {"irdrop", 2}, {"thermal", 2},
	{"diff", 2}, {"length", 2}, {"skew", 2}, {"plane", 2}, {"stackup", 2}, {"layer", 2}, {"electrical", 2}, {"drill", 3},
	{"via", 2}, {"drc", 3}, {"rule", 3}, {"annular", 3}, {"hole", 3}, {"completion", 4}, {"unrouted", 4},
}

// aesStyleKeys are the only keys a style object may carry.
var aesStyleKeys = map[string]bool{"profile": true, "base": true, "weight": true, "metricWeights": true, "alignTolMil": true,
	"nearMissTolMil": true, "placementGridMil": true, "gridBlend": true, "symmetryRequired": true, "slack": true}
var aesSlackKeys = map[string]bool{"wirelengthPct": true, "areaPct": true, "extraVias": true}

// ParseAesStyle reads a style document: either a pcbpilot.project.json
// (its "aesthetics" object is used, other keys ignored) or the bare
// aesthetics object. Shape:
//
//	{"profile": "custom"|"functional"|"balanced"|"precision"|"auto",
//	 "base": "balanced", "weight": 0.12, "metricWeights": {"P4": 2},
//	 "alignTolMil": 1.5, "nearMissTolMil": 10, "placementGridMil": 25,
//	 "gridBlend": 0.5, "symmetryRequired": true,
//	 "slack": {"wirelengthPct": 3, "areaPct": 1, "extraVias": 2}}
//
// Any key naming a hard constraint (clearance, width, via, creepage, drc,
// completion …) is rejected with its tier; unknown keys are rejected too.
// The returned profile has Name "auto" (resolve with AutoAesProfile) when
// the document asks for it.
func ParseAesStyle(raw []byte) (AesProfile, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		return AesProfile{}, fmt.Errorf("style: %w", err)
	}
	obj := raw
	if a, ok := top["aesthetics"]; ok {
		obj = a
		top = nil // a fresh map: Unmarshal would merge into the outer one
		if err := json.Unmarshal(a, &top); err != nil {
			return AesProfile{}, fmt.Errorf("style: aesthetics: %w", err)
		}
	}
	if err := aesRejectHard(top, ""); err != nil {
		return AesProfile{}, err
	}
	var doc struct {
		Profile          string             `json:"profile"`
		Base             string             `json:"base"`
		Weight           *float64           `json:"weight"`
		MetricWeights    map[string]float64 `json:"metricWeights"`
		AlignTolMil      *float64           `json:"alignTolMil"`
		NearMissTolMil   *float64           `json:"nearMissTolMil"`
		PlacementGridMil *float64           `json:"placementGridMil"`
		GridBlend        *float64           `json:"gridBlend"`
		SymmetryRequired *bool              `json:"symmetryRequired"`
		Slack            *AesSlack          `json:"slack"`
	}
	dec := json.NewDecoder(bytes.NewReader(obj))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return AesProfile{}, fmt.Errorf("style: %w", err)
	}
	name := doc.Profile
	if name == "" {
		name = DefaultAesProfile
	}
	if name == "auto" {
		return AesProfile{Name: "auto"}, nil
	}
	base := name
	if name == "custom" {
		base = doc.Base
	}
	p, err := AesProfileByName(base)
	if err != nil {
		return AesProfile{}, err
	}
	custom := false
	set := func(dst *float64, v *float64) {
		if v != nil {
			*dst, custom = *v, true
		}
	}
	set(&p.Weight, doc.Weight)
	set(&p.AlignTolMil, doc.AlignTolMil)
	set(&p.NearMissTolMil, doc.NearMissTolMil)
	set(&p.PlacementGridMil, doc.PlacementGridMil)
	set(&p.GridBlend, doc.GridBlend)
	if doc.SymmetryRequired != nil {
		p.SymmetryRequired, custom = *doc.SymmetryRequired, true
	}
	if doc.Slack != nil {
		p.Slack, custom = *doc.Slack, true
	}
	if doc.MetricWeights != nil {
		if p.MetricWeights == nil {
			p.MetricWeights = map[string]float64{}
		}
		for k, v := range doc.MetricWeights {
			p.MetricWeights[k] = v
		}
		custom = true
	}
	if custom || name == "custom" {
		p.Name = "custom"
	}
	if err := p.Validate(); err != nil {
		return AesProfile{}, err
	}
	return p, nil
}

func aesRejectHard(m map[string]json.RawMessage, path string) error {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		full := k
		if path != "" {
			full = path + "." + k
		}
		allowed := aesStyleKeys[k] && path == "" || path == "slack" && aesSlackKeys[k] || path == "metricWeights"
		if !allowed {
			low := strings.ToLower(k)
			for _, h := range aesHardTokens {
				if strings.Contains(low, h.tok) {
					return fmt.Errorf("style key %q touches a hard constraint (tier %d %s): an aesthetics profile may only change soft objectives",
						full, h.tier, ConstraintPriority[h.tier-1].Name)
				}
			}
			return fmt.Errorf("style key %q is not an aesthetics setting", full)
		}
		if k == "slack" && path == "" {
			var sub map[string]json.RawMessage
			if err := json.Unmarshal(m[k], &sub); err != nil {
				return fmt.Errorf("style: slack: %w", err)
			}
			if err := aesRejectHard(sub, "slack"); err != nil {
				return err
			}
		}
	}
	return nil
}

// Complexity index thresholds of `auto` (initial values, chosen so the dense
// SoC reference boards come out functional and the small ESP32 / adapter
// boards balanced or precision — see baseline.md §7).
const (
	aesAutoFunctional = 0.45
	aesAutoPrecision  = 0.20
)

// AutoAesProfile picks a preset from the board's complexity and slack:
// part and net count, pin density per cm², layer count, high-speed and
// high-voltage presence, and placement congestion (RUDY, rudy.go).
func AutoAesProfile(b *Board, an *Analysis, layers int) AesProfile {
	if an == nil {
		an = Analyze(b, PowerSpec{}, nil)
	}
	pads := 0
	for _, p := range b.Parts {
		pads += len(p.Pads)
	}
	areaCm2 := b.Area() / (393.7007874 * 393.7007874)
	density := 0.0
	if areaCm2 > 0 {
		density = float64(pads) / areaCm2
	}
	hs, hv := 0.0, 0.0
	for _, np := range an.Nets {
		if ClassifyHS(np) != nil {
			hs = 1
		}
	}
	if an.MaxVoltage > 60 || an.Iso != nil {
		hv = 1
	}
	if layers <= 0 {
		layers = b.CopperLayers
	}
	cong := 0.0
	if len(b.Parts) > 0 {
		cm := Congestion(b, an)
		cong = cm.MaxUtil
	}
	f := map[string]float64{
		"parts":       math.Min(1, float64(len(b.Parts))/300),
		"nets":        math.Min(1, float64(len(an.Nets))/250),
		"pinDensity":  math.Min(1, density/12),
		"layers":      clamp(float64(layers-2)/6, 0, 1),
		"highSpeed":   hs,
		"highVoltage": hv,
		"congestion":  math.Min(1, cong/1.5),
	}
	w := map[string]float64{"parts": 0.2, "nets": 0.15, "pinDensity": 0.2, "layers": 0.15, "highSpeed": 0.1, "highVoltage": 0.05, "congestion": 0.15}
	idx := 0.0
	keys := make([]string, 0, len(f))
	for k := range f {
		keys = append(keys, k)
		idx += w[k] * f[k]
	}
	sort.Strings(keys)
	chosen := "balanced"
	switch {
	case idx >= aesAutoFunctional:
		chosen = "functional"
	case idx <= aesAutoPrecision:
		chosen = "precision"
	}
	p, _ := AesProfileByName(chosen)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %.2f", k, f[k]))
	}
	raw := map[string]float64{"parts": float64(len(b.Parts)), "nets": float64(len(an.Nets)), "pinsPerCm2": round2(density), "layers": float64(layers), "maxCongestion": round2(cong)}
	for k, v := range raw {
		f["raw."+k] = v
	}
	p.Auto = &AesAuto{Index: round3(idx), Chosen: chosen, Factors: f,
		Reason: fmt.Sprintf("complexity index %.2f (%s; %d parts, %.1f pins/cm², %d layers) → %s (functional ≥ %.2f, precision ≤ %.2f)",
			idx, strings.Join(parts, ", "), len(b.Parts), density, layers, chosen, aesAutoFunctional, aesAutoPrecision)}
	return p
}
