package analogsim

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Spec is the analog target file (--spec). Every field is optional; blocks
// without an entry get targets inferred from the design (nominal values and
// datasheet limits).
//
//	{"blocks":[{"core":"U1:B","targets":{"fcHz":1000,"q":0.707},"tolPct":{"fcHz":5}},
//	           {"output":"ADC_IN","inputRange":[0,24]}],
//	 "adc":{"sampleRateHz":100000},"phaseMarginMinDeg":50,"mcRuns":200,"seed":7,
//	 "strayPF":4,"crystals":{"Y1":{"clPF":12}},"optimise":true}
type Spec struct {
	Blocks            []SpecBlock            `json:"blocks,omitempty"`
	ADC               *SpecADC               `json:"adc,omitempty"`
	PhaseMarginMinDeg float64                `json:"phaseMarginMinDeg,omitempty"`
	MCRuns            int                    `json:"mcRuns,omitempty"`
	Seed              int                    `json:"seed,omitempty"`
	StrayPF           float64                `json:"strayPF,omitempty"`
	Crystals          map[string]SpecCrystal `json:"crystals,omitempty"`
	Optimise          *bool                  `json:"optimise,omitempty"`
	Rails             map[string]float64     `json:"rails,omitempty"`
}

// SpecBlock selects a block (by id, core, output net or any part ref) and sets targets.
type SpecBlock struct {
	Block      string             `json:"block,omitempty"`
	Core       string             `json:"core,omitempty"`
	Output     string             `json:"output,omitempty"`
	Ref        string             `json:"ref,omitempty"`
	Targets    map[string]float64 `json:"targets,omitempty"`
	Min        map[string]float64 `json:"min,omitempty"`
	Max        map[string]float64 `json:"max,omitempty"`
	TolPct     map[string]float64 `json:"tolPct,omitempty"`
	InputRange []float64          `json:"inputRange,omitempty"`
	Optimise   *bool              `json:"optimise,omitempty"`
	// Fixed lists refs the optimiser must not change.
	Fixed []string `json:"fixed,omitempty"`
}

// SpecADC overrides the ADC sampling of every ADC input.
type SpecADC struct {
	SampleRateHz float64 `json:"sampleRateHz,omitempty"`
	TsampleS     float64 `json:"tsampleS,omitempty"`
	Bits         int     `json:"bits,omitempty"`
	CshF         float64 `json:"cshF,omitempty"`
	RadcOhm      float64 `json:"radcOhm,omitempty"`
}

// SpecCrystal declares a crystal's load capacitance / frequency.
type SpecCrystal struct {
	CLPF float64 `json:"clPF,omitempty"`
	FHz  float64 `json:"fHz,omitempty"`
}

// ParseSpec decodes an analog spec (optionally nested under "analog").
func ParseSpec(b []byte) (*Spec, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, err
	}
	if inner, ok := raw["analog"]; ok {
		b = inner
	}
	var s Spec
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	for i, sb := range s.Blocks {
		if sb.Block == "" && sb.Core == "" && sb.Output == "" && sb.Ref == "" {
			return nil, fmt.Errorf("blocks[%d]: give block, core, output or ref", i)
		}
	}
	return &s, nil
}

func normCore(s string) string {
	return strings.ToUpper(strings.NewReplacer(":", "", ".", "", "-", "", " ", "").Replace(s))
}

// forBlock returns the spec entry of a block.
func (s *Spec) forBlock(b *Block) *SpecBlock {
	if s == nil {
		return nil
	}
	for i := range s.Blocks {
		sb := &s.Blocks[i]
		switch {
		case sb.Block != "" && strings.EqualFold(sb.Block, b.ID):
			return sb
		case sb.Core != "" && normCore(sb.Core) == normCore(b.Core):
			return sb
		case sb.Output != "" && sb.Output == b.Output:
			return sb
		case sb.Ref != "":
			for _, p := range b.Parts {
				if p == sb.Ref {
					return sb
				}
			}
		}
	}
	return nil
}
