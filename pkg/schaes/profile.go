package schaes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Profile is one schematic aesthetics style. The NAMES are shared with the
// PCB style table (pcbauto.AesProfiles: functional / balanced / precision,
// plus auto and custom) so one project-level style word means the same
// intent on both sides; the numbers are schematic-specific (canvas units of
// 0.01 in). A profile only changes soft, lowest-tier objectives: it can never
// touch connectivity, NC, ownership or any check of sch layout-lint /
// sch check / layout-score.
type Profile struct {
	Name string `json:"name"`
	// Weight is the planned weight of schematic aesthetics in a future joint
	// schematic score (Phase C). Phase A reports it and applies 0.
	Weight        float64            `json:"weight"`
	GroupWeights  map[string]float64 `json:"groupWeights"`
	MetricWeights map[string]float64 `json:"metricWeights,omitempty"`
	GridUnits     float64            `json:"gridUnits"`       // W8 audit grid (connection grid)
	TargetGrid    float64            `json:"targetGridUnits"` // W8 preferred coarser grid
	GridBlend     float64            `json:"gridBlend"`       // share of TargetGrid in W8
	AlignTol      float64            `json:"alignTolUnits"`   // L3 "same line"
	NearMissTol   float64            `json:"nearMissTolUnits"`
	LongWire      float64            `json:"longWireUnits"`       // N1: wire tree longer → label candidate
	LongCrossings int                `json:"longWireCrossings"`   // N1: wire tree with more crossings → label candidate
	ShortLabel    float64            `json:"shortLabelSpanUnits"` // N2: signal net spanning less → a wire reads better
	// Generate holds the Phase B generation thresholds (the beautify pass of
	// sch lib-layout / layout-plan --aesthetics). Soft only: every move is
	// still re-checked against the full geometry/connectivity gates.
	Generate GenerateProfile `json:"generate"`
	Auto     *AutoChoice     `json:"auto,omitempty"`
}

// GenerateProfile is the Phase B beautify budget/threshold set of one style.
// Units are canvas units (0.01 in) unless stated.
type GenerateProfile struct {
	// JunctionClearance: a T-junction should sit at least this far from any
	// other node/bend of its island (W5 judge; 2 × 5-unit connection grid).
	JunctionClearance float64 `json:"junctionClearanceUnits"`
	// BendCost / CrossCost: maze weights (in units of wire length) for one
	// bend / one strict crossing of a foreign wire when the beautify pass
	// re-routes a direct-net trunk.
	BendCost  float64 `json:"bendCostUnits"`
	CrossCost float64 `json:"crossCostUnits"`
	// LabelSplit: allow long-wire → local label conversion (N1) for nets
	// whose policy permits labels; direct nets are never converted.
	LabelSplit bool `json:"labelSplit"`
	// BusPitch: lane pitch for virtual bus labels (N3).
	BusPitch float64 `json:"busPitchUnits"`
	// AlignMove: largest translation of one peripheral when snapping it onto
	// a shared row/column (L3); 0 disables part moves.
	AlignMove float64 `json:"alignMoveUnits"`
	// MaxEvaluations bounds candidate evaluations of the whole pass.
	MaxEvaluations int `json:"maxEvaluations"`
	// NativeBusMinMembers: a complete virtual bus lane with at least this
	// many members is also drawn as a native bus primitive (drawing only;
	// members keep wire + label taps). 0 = no native buses by default.
	NativeBusMinMembers int `json:"nativeBusMinMembers"`
}

// AutoChoice records why `auto` picked a preset.
type AutoChoice struct {
	Index   float64            `json:"index"`
	Chosen  string             `json:"chosen"`
	Reason  string             `json:"reason"`
	Factors map[string]float64 `json:"factors"`
}

var defaultGroupWeights = map[string]float64{GroupWiring: 0.5, GroupLayout: 0.3, GroupLabels: 0.2}

// Profiles is THE schematic preset table.
var Profiles = map[string]Profile{
	"functional": {Name: "functional", Weight: 0.05, GridUnits: 5, TargetGrid: 5, GridBlend: 0, AlignTol: 5, NearMissTol: 20,
		LongWire: 800, LongCrossings: 3, ShortLabel: 80,
		Generate:      GenerateProfile{JunctionClearance: 10, BendCost: 10, CrossCost: 40, LabelSplit: true, BusPitch: 10, AlignMove: 0, MaxEvaluations: 3000, NativeBusMinMembers: 0},
		MetricWeights: map[string]float64{"L3": 0.5, "L4": 0.5, "L7": 0.5, "W8": 0.5, "N3": 0.5}},
	"balanced": {Name: "balanced", Weight: 0.10, GridUnits: 5, TargetGrid: 10, GridBlend: 0.3, AlignTol: 2, NearMissTol: 15,
		LongWire: 600, LongCrossings: 2, ShortLabel: 120,
		Generate: GenerateProfile{JunctionClearance: 10, BendCost: 20, CrossCost: 80, LabelSplit: true, BusPitch: 10, AlignMove: 20, MaxEvaluations: 6000, NativeBusMinMembers: 3}},
	"precision": {Name: "precision", Weight: 0.20, GridUnits: 5, TargetGrid: 10, GridBlend: 0.6, AlignTol: 0.5, NearMissTol: 10,
		LongWire: 400, LongCrossings: 1, ShortLabel: 150,
		Generate:      GenerateProfile{JunctionClearance: 10, BendCost: 30, CrossCost: 150, LabelSplit: true, BusPitch: 10, AlignMove: 40, MaxEvaluations: 12000, NativeBusMinMembers: 2},
		MetricWeights: map[string]float64{"W1": 1.5, "W2": 1.5, "W3": 1.5, "L2": 1.5, "L3": 1.5, "N3": 1.5}},
}

// DefaultProfile is used when none is selected.
const DefaultProfile = "balanced"

// ProfileByName returns a preset copy ("" = default).
func ProfileByName(name string) (Profile, error) {
	if name == "" {
		name = DefaultProfile
	}
	p, ok := Profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown schematic aesthetics profile %q (presets: %s, auto, custom via --style-file)", name, strings.Join(sortedKeys(Profiles), ", "))
	}
	return p.clone(), nil
}

func (p Profile) clone() Profile {
	q := p
	q.MetricWeights = map[string]float64{}
	for k, v := range p.MetricWeights {
		q.MetricWeights[k] = v
	}
	q.GroupWeights = map[string]float64{}
	src := p.GroupWeights
	if src == nil {
		src = defaultGroupWeights
	}
	for k, v := range src {
		q.GroupWeights[k] = v
	}
	return q
}

func (p *Profile) metricWeight(id string) float64 {
	if w, ok := p.MetricWeights[id]; ok {
		return w
	}
	return 1
}

// Validate keeps every field inside the soft, aesthetics-only range.
func (p *Profile) Validate() error {
	if p.Weight < 0 || p.Weight > 0.3 {
		return fmt.Errorf("weight %.2f outside 0…0.3 (aesthetics stays the lowest tier)", p.Weight)
	}
	for k, v := range p.MetricWeights {
		if _, ok := metricNames[k]; !ok {
			return fmt.Errorf("metricWeights: unknown metric %q", k)
		}
		if v < 0 || v > 3 {
			return fmt.Errorf("metricWeights.%s %.2f outside 0…3", k, v)
		}
	}
	for k, v := range p.GroupWeights {
		if _, ok := defaultGroupWeights[k]; !ok {
			return fmt.Errorf("groupWeights: unknown group %q", k)
		}
		if v < 0 || v > 1 {
			return fmt.Errorf("groupWeights.%s outside 0…1", k)
		}
	}
	if p.GridUnits <= 0 || p.TargetGrid <= 0 || math.Mod(p.TargetGrid, p.GridUnits) != 0 {
		return fmt.Errorf("targetGridUnits must be a positive multiple of gridUnits")
	}
	if p.GridBlend < 0 || p.GridBlend > 1 {
		return fmt.Errorf("gridBlend outside 0…1")
	}
	if p.AlignTol < 0 || p.NearMissTol <= p.AlignTol {
		return fmt.Errorf("need 0 ≤ alignTolUnits < nearMissTolUnits")
	}
	if p.LongWire <= 0 || p.LongCrossings < 0 || p.ShortLabel < 0 {
		return fmt.Errorf("longWireUnits must be > 0, longWireCrossings/shortLabelSpanUnits ≥ 0")
	}
	g := p.Generate
	if g.JunctionClearance < 0 || g.JunctionClearance > 50 || math.Mod(g.JunctionClearance, 5) != 0 {
		return fmt.Errorf("generate.junctionClearanceUnits must be a multiple of 5 in 0…50")
	}
	if g.BendCost < 0 || g.BendCost > 200 || g.CrossCost < 0 || g.CrossCost > 1000 {
		return fmt.Errorf("generate.bendCostUnits must be 0…200 and crossCostUnits 0…1000")
	}
	if g.BusPitch < 5 || g.BusPitch > 50 || math.Mod(g.BusPitch, 5) != 0 {
		return fmt.Errorf("generate.busPitchUnits must be a multiple of 5 in 5…50")
	}
	if g.AlignMove < 0 || g.AlignMove > 100 || math.Mod(g.AlignMove, 5) != 0 {
		return fmt.Errorf("generate.alignMoveUnits must be a multiple of 5 in 0…100")
	}
	if g.NativeBusMinMembers != 0 && (g.NativeBusMinMembers < 2 || g.NativeBusMinMembers > 64) {
		return fmt.Errorf("generate.nativeBusMinMembers must be 0 (off) or 2…64")
	}
	if g.MaxEvaluations < 0 || g.MaxEvaluations > 200000 {
		return fmt.Errorf("generate.maxEvaluations outside 0…200000")
	}
	return nil
}

// hardKeys are settings that belong to connectivity / correctness tiers. A
// style file naming any of them is refused, never silently ignored.
var hardKeys = map[string]string{
	"connectivity": "connectivity correctness", "nets": "connectivity correctness", "nc": "NC preservation",
	"noConnect": "NC preservation", "ownership": "core/peripheral ownership", "zones": "core/peripheral ownership",
	"layoutLint": "sch layout-lint gate", "check": "sch check", "layoutScore": "sch layout-score", "drc": "DRC",
	"overlap": "sch layout-lint gate", "minScore": "gates",
}

var styleKeys = map[string]bool{"name": true, "profile": true, "base": true, "weight": true, "groupWeights": true,
	"metricWeights": true, "gridUnits": true, "targetGridUnits": true, "gridBlend": true, "alignTolUnits": true,
	"nearMissTolUnits": true, "longWireUnits": true, "longWireCrossings": true, "shortLabelSpanUnits": true, "generate": true}

// ParseStyle reads a style object: either the bare object or one wrapped as
// {"schematic":{…}} (the schematic half of a project "aesthetics" object).
// "base"/"profile"/"name" picks the preset the overrides apply to.
func ParseStyle(raw []byte) (Profile, error) {
	var m map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&m); err != nil {
		return Profile{}, fmt.Errorf("style: %w", err)
	}
	outerBase := ""
	if sub, ok := m["schematic"]; ok {
		for _, k := range []string{"profile", "name"} {
			if r, ok := m[k]; ok {
				_ = json.Unmarshal(r, &outerBase)
			}
		}
		m = nil
		if err := json.Unmarshal(sub, &m); err != nil {
			return Profile{}, fmt.Errorf("style.schematic: %w", err)
		}
	}
	keys := sortedKeys(m)
	for _, k := range keys {
		if tier, hard := hardKeys[k]; hard {
			return Profile{}, fmt.Errorf("style key %q belongs to %s (a hard tier above aesthetics); a style cannot change it", k, tier)
		}
		if !styleKeys[k] {
			return Profile{}, fmt.Errorf("style key %q is not a schematic aesthetics setting (allowed: %s)", k, strings.Join(sortedKeys(styleKeys), ", "))
		}
	}
	base := outerBase
	for _, k := range []string{"base", "profile", "name"} {
		if r, ok := m[k]; ok {
			_ = json.Unmarshal(r, &base)
		}
	}
	if base == "custom" || base == "auto" {
		base = ""
	}
	p, err := ProfileByName(base)
	if err != nil {
		return Profile{}, err
	}
	delete(m, "name")
	delete(m, "profile")
	delete(m, "base")
	over, _ := json.Marshal(m)
	if err := json.Unmarshal(over, &p); err != nil {
		return Profile{}, fmt.Errorf("style: %w", err)
	}
	p.Name = "custom"
	if err := p.Validate(); err != nil {
		return Profile{}, err
	}
	return p, nil
}

// AutoProfile picks a preset from the page's complexity: dense pages get
// functional (looks must not cost readability of a crowded sheet), small
// sparse pages precision, everything else balanced.
func AutoProfile(s *Snapshot) Profile {
	n := float64(len(s.Parts))
	pins := 0.0
	var content Box
	area := 0.0
	for _, p := range s.Parts {
		pins += float64(len(p.Pins))
		if p.HasBox {
			content = content.Union(p.Box)
			area += p.Box.Area()
		}
	}
	density := 0.0
	if content.Area() > 0 {
		density = area / content.Area()
	}
	f := map[string]float64{
		"parts":   round3(math.Min(1, n/60)),
		"pins":    round3(math.Min(1, pins/400)),
		"density": round3(math.Min(1, density/0.35)),
	}
	idx := round3(0.4*f["parts"] + 0.3*f["pins"] + 0.3*f["density"])
	chosen := "balanced"
	switch {
	case idx >= 0.66:
		chosen = "functional"
	case idx <= 0.33:
		chosen = "precision"
	}
	p, _ := ProfileByName(chosen)
	keys := sortedKeys(f)
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %.2f", k, f[k]))
	}
	p.Auto = &AutoChoice{Index: idx, Chosen: chosen, Factors: f,
		Reason: fmt.Sprintf("complexity %.2f (%s; ≥0.66 functional, ≤0.33 precision) → %s", idx, strings.Join(parts, ", "), chosen)}
	return p
}
