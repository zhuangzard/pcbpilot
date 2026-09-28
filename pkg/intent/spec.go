package intent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Spec is the product-level input (`--spec spec.json`): the things a
// schematic cannot say — the safety standard, environment, declared rail
// budgets that override the simulation, ripple budgets and high-speed
// interfaces. Every field is optional.
type Spec struct {
	Standard *Standard `json:"standard,omitempty"`
	// Layers is the planned copper layer count (default 4).
	Layers int `json:"layers,omitempty"`
	// Copper weights (default 1 oz outer / 0.5 oz inner, JLC standard).
	OuterOz float64 `json:"outerOz,omitempty"`
	InnerOz float64 `json:"innerOz,omitempty"`
	// TempRiseC is the allowed conductor temperature rise (default 10 °C).
	TempRiseC float64 `json:"tempRiseC,omitempty"`
	// Rails are declared voltage/current budgets; a declared current wins over
	// the simulated one (reported as "declared").
	Rails []SpecRail `json:"rails,omitempty"`
	// HSInterfaces declare impedance-controlled interfaces beyond the
	// auto-recognised USB pairs.
	HSInterfaces []SpecHS `json:"hsInterfaces,omitempty"`
	// Mains describes AC line nets (names are otherwise recognised as
	// L/N/AC_L/…); Vrms defaults to 230.
	Mains *SpecMains `json:"mains,omitempty"`
	// Domains override the kind of a domain (e.g. patient) by any of its nets.
	Domains []SpecDomain `json:"domains,omitempty"`
	// USBBudgetA is the current a USB source may deliver (default 0.5 A,
	// USB 2.0 default power; 1.5/3.0 for USB-C current advertisement).
	USBBudgetA float64 `json:"usbBudgetA,omitempty"`
	// Rules override the fabrication minimums (mil).
	Rules *SpecRules `json:"rules,omitempty"`
	// Edge declares how the board is separated from the panel and
	// overrides the board-edge safety distances (default: routed edge,
	// 20 mil outer / 30 mil inner).
	Edge *SpecEdge `json:"edge,omitempty"`
}

// SpecEdge is spec.json "edge".
type SpecEdge struct {
	// EdgeKind is routed | vcut | mixed (default routed).
	EdgeKind string `json:"edgeKind,omitempty"`
	// OuterMil / InnerMil / VcutMil raise or lower the defaults; never
	// below the fabricator's floor (0.2 mm routed, 0.4 mm V-cut).
	OuterMil float64 `json:"outerMil,omitempty"`
	InnerMil float64 `json:"innerMil,omitempty"`
	VcutMil  float64 `json:"vcutMil,omitempty"`
	// Insulation is the grade hazardous copper needs to the board edge and
	// metal mounting holes: reinforced (default — the edge is accessible)
	// or basic (the enclosure provides the second means of protection, or
	// the mounting hardware is protectively earthed).
	Insulation string `json:"insulation,omitempty"`
	// DomainMil raises a domain's edge distance (key: domain id or any net
	// of the domain, mil). A value below the insulation distance is ignored.
	DomainMil map[string]float64 `json:"domainMil,omitempty"`
}

// SpecRail is a declared rail.
type SpecRail struct {
	Net        string  `json:"net"`
	Voltage    float64 `json:"voltage,omitempty"`
	CurrentA   float64 `json:"currentA,omitempty"`
	RippleMvpp float64 `json:"rippleMvpp,omitempty"`
	// PeakV overrides the peak voltage (surges, inductive kick).
	PeakV float64 `json:"peakV,omitempty"`
}

// SpecHS is a declared high-speed interface.
type SpecHS struct {
	Name        string      `json:"name"`
	Nets        []string    `json:"nets,omitempty"`  // single-ended members
	Pairs       [][2]string `json:"pairs,omitempty"` // P/N pairs
	DiffOhm     float64     `json:"diffOhm,omitempty"`
	SingleOhm   float64     `json:"singleOhm,omitempty"`
	LengthGroup string      `json:"lengthGroup,omitempty"`
	// LengthTolMil is the length-group tolerance (max − min, mil; a pair
	// counts at its mean length); 0 = the interface default.
	LengthTolMil float64 `json:"lengthTolMil,omitempty"`
	// MaxSkewMil is the intra-pair skew limit; 0 = the interface default.
	MaxSkewMil float64 `json:"maxSkewMil,omitempty"`
	// MaxVias is the via budget per net; 0 = the interface default.
	MaxVias int `json:"maxVias,omitempty"`
}

// SpecMains declares the AC line.
type SpecMains struct {
	Vrms float64  `json:"vrms,omitempty"`
	Nets []string `json:"nets,omitempty"`
}

// SpecDomain overrides a domain's kind.
type SpecDomain struct {
	Kind string   `json:"kind"` // patient | floating | isolated-secondary | SELV | hazardous | mains
	Nets []string `json:"nets"`
	// WorkingVrms overrides the domain working voltage (e.g. a floating
	// secondary referenced to mains).
	WorkingVrms float64 `json:"workingVrms,omitempty"`
	// Transient declares the transient overvoltage regime of the domain for
	// clearance procedure 2 (IEC 60664-1 Table F.1): "mains" (connected to a
	// supply network: the overvoltage category applies), "secondary" (behind
	// an isolating transformer: one category lower) or "none" (battery /
	// isolated DC, peak working voltage only). Empty = inferred.
	Transient string `json:"transient,omitempty"`
	// RatedVrms is the nominal system voltage (line to neutral/earth, or
	// the DC bus) the transient is taken from; 0 = the domain's working
	// voltage.
	RatedVrms float64 `json:"ratedVrms,omitempty"`
	// IsolationVrms is an electric-strength (hi-pot) requirement of the
	// barrier to this domain, e.g. 1500 for IEEE 802.3 MDI (§14.3.1.1 /
	// §40.6.1.1): the pair becomes basic insulation dimensioned for that
	// withstand voltage even between two SELV domains.
	IsolationVrms float64 `json:"isolationVrms,omitempty"`
}

// SpecRules override fabrication minimums (mil).
type SpecRules struct {
	ClearanceMil float64 `json:"clearanceMil,omitempty"`
	TrackMil     float64 `json:"trackMil,omitempty"`
	ViaDrillMil  float64 `json:"viaDrillMil,omitempty"`
	ViaDiaMil    float64 `json:"viaDiaMil,omitempty"`
}

// ParseSpec decodes spec.json (unknown fields are rejected to catch typos).
func ParseSpec(b []byte) (*Spec, error) {
	var s Spec
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("spec: %w", err)
	}
	if st := s.Standard; st != nil {
		if err := validateStandard(st); err != nil {
			return nil, fmt.Errorf("spec.standard: %w", err)
		}
	}
	for i, d := range s.Domains {
		switch d.Kind {
		case "patient", "floating", "isolated-secondary", "SELV", "hazardous", "mains":
		default:
			return nil, fmt.Errorf("spec.domains[%d].kind %q: want patient|floating|isolated-secondary|SELV|hazardous|mains", i, d.Kind)
		}
		switch d.Transient {
		case "", "mains", "secondary", "none":
		default:
			return nil, fmt.Errorf("spec.domains[%d].transient %q: want mains|secondary|none", i, d.Transient)
		}
		if d.RatedVrms < 0 || d.WorkingVrms < 0 {
			return nil, fmt.Errorf("spec.domains[%d]: negative voltage", i)
		}
	}
	if e := s.Edge; e != nil {
		kind, err := pcbauto.NormEdgeKind(e.EdgeKind)
		if err != nil {
			return nil, fmt.Errorf("spec.edge: %w", err)
		}
		_, _, _, fab, _ := pcbauto.EdgeDefaults(kind)
		for name, v := range map[string]float64{"outerMil": e.OuterMil, "innerMil": e.InnerMil, "vcutMil": e.VcutMil} {
			if v != 0 && v < fab {
				return nil, fmt.Errorf("spec.edge.%s %.1f mil is below the fabricator's %s floor %.1f mil", name, v, kind, fab)
			}
		}
		switch strings.ToLower(e.Insulation) {
		case "", "basic", "supplementary", "double", "reinforced":
		default:
			return nil, fmt.Errorf("spec.edge.insulation %q: want basic|supplementary|double|reinforced", e.Insulation)
		}
		for k, v := range e.DomainMil {
			if v < 0 {
				return nil, fmt.Errorf("spec.edge.domainMil[%s]: negative distance", k)
			}
		}
	}
	return &s, nil
}

var (
	knownStandards = []string{"IPC-2221B", "IEC62368-1", "IEC60601-1", "IEC61010-1"}
	knownInsul     = []string{"functional", "basic", "supplementary", "double", "reinforced"}
)

func validateStandard(st *Standard) error {
	if st.Name != "" && !containsFold(knownStandards, st.Name) {
		return fmt.Errorf("name %q: want one of %s", st.Name, strings.Join(knownStandards, ", "))
	}
	if st.Insulation != "" && !containsFold(knownInsul, st.Insulation) {
		return fmt.Errorf("insulation %q: want one of %s", st.Insulation, strings.Join(knownInsul, ", "))
	}
	switch strings.ToUpper(st.MOP) {
	case "", "MOOP", "MOPP":
	default:
		return fmt.Errorf("mop %q: want MOOP, MOPP or empty", st.MOP)
	}
	if st.PollutionDegree < 0 || st.PollutionDegree > 4 {
		return fmt.Errorf("pollutionDegree %d: want 1..4", st.PollutionDegree)
	}
	return nil
}

func containsFold(list []string, v string) bool {
	for _, s := range list {
		if strings.EqualFold(s, v) {
			return true
		}
	}
	return false
}

// resolveStandard fills engineering defaults. hazardous tells whether the
// design has a hazardous/mains domain (then the default standard is IEC
// 62368-1 instead of plain IPC-2221B spacing).
func resolveStandard(in *Standard, hazardous bool) Standard {
	var st Standard
	if in != nil {
		st = *in
	}
	st.Defaulted = nil
	def := func(field string) { st.Defaulted = append(st.Defaulted, field) }
	for _, n := range knownStandards {
		if strings.EqualFold(n, st.Name) {
			st.Name = n
		}
	}
	if st.Name == "" {
		st.Name = "IPC-2221B"
		if hazardous {
			st.Name = "IEC62368-1"
		}
		def("name")
	}
	st.Insulation = strings.ToLower(st.Insulation)
	if st.Insulation == "" {
		st.Insulation = "functional"
		if hazardous {
			st.Insulation = "reinforced"
		}
		def("insulation")
	}
	st.MOP = strings.ToUpper(st.MOP)
	if st.Name == "IEC60601-1" && st.MOP == "" {
		st.MOP = "MOPP"
		def("mop")
	}
	if st.MOP != "" && st.MOPCount == 0 {
		st.MOPCount = 1
		if st.Insulation == "reinforced" || st.Insulation == "double" {
			st.MOPCount = 2
		}
		def("mopCount")
	}
	if st.PollutionDegree == 0 {
		st.PollutionDegree = 2
		def("pollutionDegree")
	}
	if st.MaterialGroup == "" {
		st.MaterialGroup = "IIIa"
		def("materialGroup")
	}
	if st.AltitudeM == 0 {
		st.AltitudeM = 2000
		def("altitudeM")
	}
	if st.OvervoltageCategory == "" {
		st.OvervoltageCategory = "II"
		def("overvoltageCategory")
	}
	return st
}
