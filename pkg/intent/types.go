// Package intent turns a finished schematic (connectivity + part values) and
// its DC power simulation into an explained electrical DESIGN INTENT: what
// each circuit block is for, the voltage/current/width/clearance/impedance plan
// of every net, the voltage domains and the insulation required between them,
// the net classes, and designer findings.
//
// intent.json (schemaVersion 1) is a FIXED contract consumed by the EasyEDA
// rule push, pcb auto, the safety checker and the feedback loop: fields may be
// added, never renamed or removed. It has no editor, daemon or filesystem
// dependency; the CLI (`pcbpilot intent derive`) does the I/O.
package intent

// SchemaVersion of intent.json.
const SchemaVersion = 1

// Generator is written into every document.
const Generator = "pcbpilot intent derive"

// Intent is the intent.json document.
type Intent struct {
	SchemaVersion int                 `json:"schemaVersion"`
	Generator     string              `json:"generator"`
	Sources       Sources             `json:"sources"`
	Standard      Standard            `json:"standard"`
	Blocks        []*Block            `json:"blocks"`
	Domains       []*Domain           `json:"domains"`
	Nets          map[string]*NetPlan `json:"nets"`
	Pairs         []*Pair             `json:"pairs"`
	NetClasses    []*NetClass         `json:"netClasses"`
	Findings      []*Finding          `json:"findings"`
	// Additive fields (not in the v1 core contract; safe to ignore).
	Copper      *Copper      `json:"copper,omitempty"`
	Simulation  *SimInfo     `json:"simulation,omitempty"`
	Definitions *Definitions `json:"definitions,omitempty"`
}

// Sources records provenance.
type Sources struct {
	Schematic []string `json:"schematic"`
	Sim       string   `json:"sim"`
	Spec      string   `json:"spec"`
	Models    []string `json:"models,omitempty"`
}

// Standard is the product-level safety frame the distances are judged in.
type Standard struct {
	Name                string  `json:"name"`       // IPC-2221B | IEC62368-1 | IEC60601-1 | IEC61010-1
	Insulation          string  `json:"insulation"` // functional | basic | supplementary | double | reinforced
	MOP                 string  `json:"mop"`        // MOOP | MOPP | "" (IEC 60601-1 only)
	MOPCount            int     `json:"mopCount"`
	PollutionDegree     int     `json:"pollutionDegree"`
	MaterialGroup       string  `json:"materialGroup"`
	AltitudeM           float64 `json:"altitudeM"`
	OvervoltageCategory string  `json:"overvoltageCategory"`
	Coated              bool    `json:"coated"`
	// Defaulted lists the fields filled by engineering defaults (not the spec).
	Defaulted []string `json:"defaulted,omitempty"`
}

// Block is one functional circuit with a human summary.
type Block struct {
	ID       string   `json:"id"`
	Function string   `json:"function"` // power-input|buck|boost|ldo|charger|usb-uart|mcu|rf-module|led|esd|connector|isolation|mains|sensor|motor-driver|other
	Core     string   `json:"core"`
	Parts    []string `json:"parts"`
	Nets     []string `json:"nets"`
	Summary  string   `json:"summary"`
	Notes    []string `json:"notes"`
	// Additive.
	SubFunction string   `json:"subFunction,omitempty"` // e.g. auto-download, keys, or-ing
	Domain      string   `json:"domain,omitempty"`
	PowerW      float64  `json:"powerW,omitempty"` // worst-case dissipation of the block's parts
	Why         []string `json:"why,omitempty"`
}

// Domain is a voltage/reference domain.
type Domain struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"` // SELV|hazardous|mains|patient|floating|isolated-secondary
	Nets         []string `json:"nets"`
	Reference    string   `json:"reference"`
	WorkingVrms  float64  `json:"workingVrms"`
	WorkingVpeak float64  `json:"workingVpeak"`
	// Additive.
	Parts []string `json:"parts,omitempty"`
	Why   []string `json:"why,omitempty"`
}

// Voltage is a net's voltage envelope over the simulated scenarios.
type Voltage struct {
	Nom  float64 `json:"nom"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Peak float64 `json:"peak"`
}

// PinCurrent is the DC current through one pin (sim worst case).
type PinCurrent struct {
	Ref      string  `json:"ref"`
	Pin      string  `json:"pin"`
	CurrentA float64 `json:"currentA"`
	Dir      string  `json:"dir"` // source | sink | pass
	// Additive.
	Name     string `json:"name,omitempty"`
	Scenario string `json:"scenario,omitempty"`
}

// Width is the track-width plan of a net (mil).
type Width struct {
	Outer float64 `json:"outer"`
	Inner float64 `json:"inner"`
	Min   float64 `json:"min"`
}

// NetPlan is the per-net electrical plan.
type NetPlan struct {
	Role              string       `json:"role"` // power|ground|signal|switch|hs|diff|rf|analog|clock
	Domain            string       `json:"domain"`
	Block             string       `json:"block"`
	Voltage           Voltage      `json:"voltage"`
	CurrentA          float64      `json:"currentA"`
	CurrentSource     string       `json:"currentSource"` // simulated|declared|heuristic
	Pins              []PinCurrent `json:"pins"`
	WidthMil          Width        `json:"widthMil"`
	ViasPerTransition int          `json:"viasPerTransition"`
	ClearanceMil      float64      `json:"clearanceMil"`
	ImpedanceOhm      float64      `json:"impedanceOhm"`
	DiffPair          string       `json:"diffPair"`
	LengthGroup       string       `json:"lengthGroup"`
	NetClass          string       `json:"netClass"`
	Why               []string     `json:"why"`
	// Additive.
	PeakA      float64 `json:"peakA,omitempty"`      // switch-node / pulsed peak current
	DCCurrentA float64 `json:"dcCurrentA,omitempty"` // simulated DC net current (when sizing uses ripple RMS)
	PairGapMil float64 `json:"pairGapMil,omitempty"` // diff-pair edge gap
	Floating   bool    `json:"floating,omitempty"`   // no DC path in the simulation
	Scenario   string  `json:"scenario,omitempty"`   // scenario of the sizing current
	RippleMvpp float64 `json:"rippleMvpp,omitempty"` // declared ripple budget (spec rails)
	Interface  string  `json:"interface,omitempty"`  // recognised HS interface (USB, …)
	PadCount   int     `json:"padCount,omitempty"`   // pins on the net
	// FloatsOn names the switch node a floating gate-drive net rides on;
	// RelVoltage is its voltage relative to that node (voltage{} is then the
	// absolute envelope, switch node swing included).
	FloatsOn   string   `json:"floatsOn,omitempty"`
	RelVoltage *Voltage `json:"relVoltage,omitempty"`
	Priority   int      `json:"priority,omitempty"` // routing order hint (lower first)
	Warnings   []string `json:"warnings,omitempty"`
	// High-speed limits (diff / hs nets): intra-pair skew, the tolerance of
	// the length group, and the via budget per net — the engine's HS class
	// defaults unless spec.hsInterfaces declares them.
	MaxSkewMil   float64 `json:"maxSkewMil,omitempty"`
	LengthTolMil float64 `json:"lengthTolMil,omitempty"`
	MaxVias      int     `json:"maxVias,omitempty"`
	// Via is the sized via of a layer transition (additive): size, count
	// (= viasPerTransition), ampacity, margin and barrel drop.
	Via *NetVia `json:"via,omitempty"`
}

// NetVia is a net's via per layer transition (pcbauto.SizeVias): the via
// size and parallel count that carry the net's current with the margin, and
// the numbers behind it. Barrel = IPC-2221 internal conductor on π(d+t)t.
type NetVia struct {
	DrillMil           float64 `json:"drillMil"`
	DiaMil             float64 `json:"diaMil"`
	CountPerTransition int     `json:"countPerTransition"`
	PerViaA            float64 `json:"perViaA"`
	AmpacityA          float64 `json:"ampacityA"`
	CurrentA           float64 `json:"currentA"`
	MarginPct          float64 `json:"marginPct"`
	PlatingMil         float64 `json:"platingMil"`
	LengthMil          float64 `json:"lengthMil"`
	ResistanceMOhm     float64 `json:"resistanceMOhm"`   // one barrel
	DropMV             float64 `json:"dropMV"`           // per transition at currentA
	Source             string  `json:"source,omitempty"` // sized | class | declared
	Why                string  `json:"why"`
}

// Pair is the insulation requirement between two domains (or nets).
type Pair struct {
	A            string   `json:"a"` // "domain:<id>" or "net:<name>"
	B            string   `json:"b"`
	WorkingVrms  float64  `json:"workingVrms"`
	WorkingVpeak float64  `json:"workingVpeak"`
	Insulation   string   `json:"insulation"`
	ClearanceMm  float64  `json:"clearanceMm"`
	CreepageMm   float64  `json:"creepageMm"`
	SlotRequired bool     `json:"slotRequired"`
	SlotWidthMm  float64  `json:"slotWidthMm"`
	StandardRef  string   `json:"standardRef"`
	Why          []string `json:"why"`
	// Additive.
	Bridges  []string `json:"bridges,omitempty"` // parts spanning the barrier
	MOP      string   `json:"mop,omitempty"`
	MOPCount int      `json:"mopCount,omitempty"`
	// Transient is the transient regime clearance procedure 2 uses:
	// mains | secondary | none ("" = let the standard's rule infer it).
	Transient string `json:"transient,omitempty"`
	// MainsVrms is the nominal system voltage the transient is taken from
	// (IEC 60664-1 Table F.1 row).
	MainsVrms float64 `json:"mainsVrms,omitempty"`
	// RequiredWithstandV is a declared electric-strength requirement (V
	// peak), e.g. IEEE 802.3 MDI isolation 1500 Vrms → 2121 V: the
	// clearance is dimensioned for it (IEC 60664-1 procedure 2).
	RequiredWithstandV float64 `json:"requiredWithstandV,omitempty"`
}

// NetClass is an EasyEDA-pushable rule class.
type NetClass struct {
	Name         string   `json:"name"`
	Nets         []string `json:"nets"`
	TrackMil     float64  `json:"trackMil"`
	ClearanceMil float64  `json:"clearanceMil"`
	ViaDrillMil  float64  `json:"viaDrillMil"`
	ViaDiaMil    float64  `json:"viaDiaMil"`
	// Additive.
	InnerTrackMil float64  `json:"innerTrackMil,omitempty"`
	MinTrackMil   float64  `json:"minTrackMil,omitempty"`
	DiffGapMil    float64  `json:"diffGapMil,omitempty"`
	ImpedanceOhm  float64  `json:"impedanceOhm,omitempty"`
	Why           []string `json:"why,omitempty"`
}

// Finding is a designer hint.
type Finding struct {
	Severity   string   `json:"severity"` // error|warn|info
	Kind       string   `json:"kind"`
	Message    string   `json:"message"`
	Refs       []string `json:"refs"`
	Nets       []string `json:"nets"`
	Suggestion string   `json:"suggestion"`
}

// Copper records the stackup assumptions behind the widths.
type Copper struct {
	Layers       int     `json:"layers"`
	OuterOz      float64 `json:"outerOz"`
	InnerOz      float64 `json:"innerOz"`
	TempRiseC    float64 `json:"tempRiseC"`
	RefHeightMil float64 `json:"refHeightMil"`
	Er           float64 `json:"er"`
	Stackup      string  `json:"stackup"`
	MinTrackMil  float64 `json:"minTrackMil"`
	ClearanceMil float64 `json:"clearanceMil"`
	ViaDrillMil  float64 `json:"viaDrillMil"`
	ViaDiaMil    float64 `json:"viaDiaMil"`
	// Additive: the via barrel plating and the ampacity margin the via
	// sizing used.
	ViaPlatingMil float64 `json:"viaPlatingMil,omitempty"`
	ViaMarginPct  float64 `json:"viaMarginPct,omitempty"`
}

// SimInfo summarises the simulation the numbers came from.
type SimInfo struct {
	Generator   string   `json:"generator"`
	Scenarios   []string `json:"scenarios"`
	Converged   bool     `json:"converged"`
	Warnings    []string `json:"warnings,omitempty"`
	Assumptions []string `json:"assumptions,omitempty"`
}

// Definitions documents conventions inside the document.
type Definitions struct {
	Voltage   string `json:"voltage"`
	CurrentA  string `json:"currentA"`
	WidthMil  string `json:"widthMil"`
	Clearance string `json:"clearanceMil"`
	Pairs     string `json:"pairs"`
}
