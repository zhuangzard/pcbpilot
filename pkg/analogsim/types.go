// Package analogsim finds the ANALOG circuits of a schematic (op-amp
// amplifiers and active filters, passive RC/LC filters, dividers and
// references feeding ADC pins, comparators with hysteresis, transistor
// switches, crystal load capacitors, regulator feedback dividers, current
// sense and instrumentation amplifiers, reset RC networks), extracts each as
// a SPICE subcircuit with its rails and loads, runs ngspice (-b batch, wrdata)
// for .op / .dc / .ac / .tran / loop gain / Monte-Carlo tolerance, compares
// the results with the targets (spec or inferred from the design), and
// optimises the component values (analytic first guess → E-series / stocked
// part snapping → ngspice verification, improvements only) into a schematic
// value-change PLAN. It never edits the schematic.
//
// analog.json (schemaVersion 1) is a fixed contract: fields may be added,
// never renamed. The package has no editor or daemon dependency; ngspice is
// an external program (skipped with a clear note when missing).
package analogsim

// SchemaVersion of analog.json and the value-change plan.
const SchemaVersion = 1

// Generator is written into every document.
const Generator = "pcbpilot sim analog"

// MissingNgspiceNote is the note every skipped simulation carries.
const MissingNgspiceNote = "ngspice missing → run pcbpilot sim tools install (analytic checks only)"

// Block classes.
const (
	ClassFollower     = "opamp-follower"
	ClassNonInverting = "opamp-noninverting"
	ClassInverting    = "opamp-inverting"
	ClassDifference   = "opamp-difference"
	ClassIntegrator   = "opamp-integrator"
	ClassSKLowPass    = "sallen-key-lowpass"
	ClassSKHighPass   = "sallen-key-highpass"
	ClassMFBLowPass   = "mfb-lowpass"
	ClassMFBHighPass  = "mfb-highpass"
	ClassComparator   = "comparator"
	ClassInstrument   = "instrumentation-amp"
	ClassCurrentSense = "current-sense"
	ClassRCLowPass    = "rc-lowpass"
	ClassLCFilter     = "lc-filter"
	ClassADCInput     = "adc-input"
	ClassReference    = "voltage-reference"
	ClassRegulatorFB  = "regulator-feedback"
	ClassCrystal      = "crystal-load"
	ClassTransistorSw = "transistor-switch"
	ClassResetRC      = "reset-rc"
	ClassOpampOther   = "opamp-unclassified"
	ClassLevelShifter = "level-shifter"
)

// Status values.
const (
	StatusPass = "PASS"
	StatusWarn = "WARN"
	StatusFail = "FAIL"
	StatusInfo = "INFO"
)

// Output is analog.json.
type Output struct {
	SchemaVersion int          `json:"schemaVersion"`
	Generator     string       `json:"generator"`
	Inputs        *Inputs      `json:"inputs,omitempty"`
	Ngspice       NgspiceInfo  `json:"ngspice"`
	Summary       Summary      `json:"summary"`
	Blocks        []*Block     `json:"blocks"`
	Findings      []Finding    `json:"findings"`
	Plan          *Plan        `json:"plan,omitempty"`
	Assumptions   []string     `json:"assumptions"`
	Warnings      []string     `json:"warnings"`
	Definitions   *Definitions `json:"definitions,omitempty"`
	Artifacts     []Artifact   `json:"artifacts,omitempty"`
	// WorkDir is where the artifacts were written (--work-dir).
	WorkDir string        `json:"workDir,omitempty"`
	Options *RunOptionsJS `json:"options,omitempty"`
}

// Inputs records provenance.
type Inputs struct {
	Schematic []string `json:"schematic,omitempty"`
	PowerSim  string   `json:"powerSim,omitempty"`
	Spec      string   `json:"spec,omitempty"`
	Models    []string `json:"models,omitempty"`
}

// RunOptionsJS echoes the knobs that change numbers.
type RunOptionsJS struct {
	MCRuns   int  `json:"mcRuns"`
	Seed     int  `json:"seed"`
	Optimise bool `json:"optimise"`
}

// NgspiceInfo says whether/which ngspice ran.
type NgspiceInfo struct {
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Runs      int    `json:"runs"`
	Note      string `json:"note,omitempty"`
}

// Summary counts.
type Summary struct {
	Blocks    int            `json:"blocks"`
	ByClass   map[string]int `json:"byClass"`
	Simulated int            `json:"simulated"`
	Targets   int            `json:"targets"`
	Met       int            `json:"met"`
	Failing   int            `json:"failing"`
	Changes   int            `json:"changes"`
	Status    string         `json:"status"` // PASS | WARN | FAIL | INFO (no analog blocks)
}

// Definitions documents conventions inside the document.
type Definitions struct {
	Metrics  string `json:"metrics"`
	Targets  string `json:"targets"`
	MC       string `json:"monteCarlo"`
	Plan     string `json:"plan"`
	Analytic string `json:"analytic"`
}

// Artifact is a netlist / raw output written for a block.
type Artifact struct {
	Block string `json:"block"`
	Kind  string `json:"kind"` // netlist | log | data
	Path  string `json:"path"` // relative to the work dir
}

// Block is one recognised analog circuit.
type Block struct {
	ID       string   `json:"id"`
	Class    string   `json:"class"`
	Title    string   `json:"title"`
	Core     string   `json:"core,omitempty"`    // ref or ref:channel
	Channel  string   `json:"channel,omitempty"` // op-amp channel A/B/…
	Parts    []string `json:"parts"`
	Nets     []string `json:"nets"`
	Input    string   `json:"input,omitempty"`  // stimulus net
	Input2   string   `json:"input2,omitempty"` // second input (difference amps)
	Output   string   `json:"output,omitempty"` // observed net
	Rails    []Rail   `json:"rails,omitempty"`
	Topology string   `json:"topology,omitempty"` // human description of the recognised structure
	// Components are the values the analysis depends on (role = Rf, Rg, R1…).
	Components []Component        `json:"components"`
	Model      *ModelRef          `json:"model,omitempty"`
	Analytic   map[string]float64 `json:"analytic,omitempty"`
	Metrics    []Metric           `json:"metrics"`
	Tolerance  *MCResult          `json:"tolerance,omitempty"`
	Curves     []Curve            `json:"curves,omitempty"`
	Optimise   *Optimisation      `json:"optimisation,omitempty"`
	Findings   []Finding          `json:"findings"`
	Simulated  bool               `json:"simulated"`
	Skipped    string             `json:"skipped,omitempty"`
	Status     string             `json:"status"`
	Notes      []string           `json:"notes,omitempty"`
	Netlist    string             `json:"netlist,omitempty"` // path of the main netlist
	// internal
	c *blockCtx
}

// Rail is a DC supply the block sits on.
type Rail struct {
	Net     string  `json:"net"`
	Voltage float64 `json:"voltage"`
	Source  string  `json:"source"` // power-sim | name | spec | assumed | ground
}

// Component is one value the block depends on.
type Component struct {
	Ref       string    `json:"ref"`
	Role      string    `json:"role"`
	Kind      string    `json:"kind"` // R | C | L
	Value     float64   `json:"value"`
	Text      string    `json:"text"`
	TolPct    float64   `json:"tolPct"`
	TolSource string    `json:"tolSource"` // description | mpn | default | assumed
	Package   string    `json:"package,omitempty"`
	Nets      [2]string `json:"nets"`
}

// ModelRef names the active-part model used.
type ModelRef struct {
	Ref        string             `json:"ref"`
	ID         string             `json:"id"`
	Kind       string             `json:"kind"`
	Confidence string             `json:"confidence"` // datasheet | approx | assumed | vendor
	Source     string             `json:"source,omitempty"`
	Params     map[string]float64 `json:"params,omitempty"`
	Vendor     string             `json:"vendor,omitempty"` // vendor .lib file when used
}

// Metric is one measured / computed quantity.
type Metric struct {
	Name     string   `json:"name"`
	Label    string   `json:"label"`
	Unit     string   `json:"unit,omitempty"`
	Value    float64  `json:"value"`
	Analytic *float64 `json:"analytic,omitempty"`
	Method   string   `json:"method"` // ngspice-ac | ngspice-tran | ngspice-op | ngspice-dc | ngspice-loop | analytic
	Target   *Target  `json:"target,omitempty"`
	Status   string   `json:"status,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// Target is a required value window for a metric.
type Target struct {
	Metric string   `json:"metric"`
	Value  *float64 `json:"value,omitempty"`
	Min    *float64 `json:"min,omitempty"`
	Max    *float64 `json:"max,omitempty"`
	TolPct float64  `json:"tolPct,omitempty"`
	Source string   `json:"source"` // spec | inferred | datasheet
	Why    string   `json:"why,omitempty"`
}

// MCResult is the tolerance (Monte-Carlo) spread.
type MCResult struct {
	Runs    int                `json:"runs"`
	Seed    int                `json:"seed"`
	Method  string             `json:"method"` // ngspice-ac-mc | ngspice-op-mc | analytic-mc
	Dist    string             `json:"distribution"`
	Stats   map[string]MCStats `json:"stats"`
	YieldPc *float64           `json:"yieldPct,omitempty"` // share of runs meeting every targeted metric
	Varied  []string           `json:"varied"`
}

// MCStats summarises one metric over the runs.
type MCStats struct {
	Nominal float64  `json:"nominal"`
	Min     float64  `json:"min"`
	Max     float64  `json:"max"`
	Mean    float64  `json:"mean"`
	Std     float64  `json:"std"`
	P1      float64  `json:"p1"`
	P99     float64  `json:"p99"`
	InSpec  *float64 `json:"inSpecPct,omitempty"`
}

// Curve is a plotted result (Bode or step).
type Curve struct {
	Name   string    `json:"name"` // bode | step | loop | transfer | ramp
	Title  string    `json:"title"`
	XLabel string    `json:"xLabel"`
	XUnit  string    `json:"xUnit"`
	LogX   bool      `json:"logX"`
	X      []float64 `json:"x"`
	Series []Series  `json:"series"`
	Marks  []Mark    `json:"marks,omitempty"`
}

// Series is one y-trace of a curve.
type Series struct {
	Name  string    `json:"name"`
	Unit  string    `json:"unit"`
	Axis  string    `json:"axis,omitempty"` // "" (left) | right
	Y     []float64 `json:"y"`
	Style string    `json:"style,omitempty"` // before | after | ""
}

// Mark is a labelled point / threshold on a curve.
type Mark struct {
	Label string  `json:"label"`
	X     float64 `json:"x,omitempty"`
	Y     float64 `json:"y,omitempty"`
	Kind  string  `json:"kind"` // x | y | point
}

// Finding is a designer hint / problem.
type Finding struct {
	Severity   string   `json:"severity"` // error | warn | info
	Kind       string   `json:"kind"`
	Block      string   `json:"block,omitempty"`
	Message    string   `json:"message"`
	Refs       []string `json:"refs"`
	Nets       []string `json:"nets"`
	Suggestion string   `json:"suggestion,omitempty"`
}

// Optimisation is the design-modification loop result of one block.
type Optimisation struct {
	Status     string             `json:"status"` // improved | not-needed | no-gain | unsupported | failed
	Reason     string             `json:"reason,omitempty"`
	Iterations int                `json:"iterations"`
	Before     map[string]float64 `json:"before"`
	After      map[string]float64 `json:"after,omitempty"`
	CostBefore float64            `json:"costBefore"`
	CostAfter  float64            `json:"costAfter,omitempty"`
	Verified   string             `json:"verified"` // ngspice | analytic
	Changes    []Change           `json:"changes,omitempty"`
	// ToleranceAfter is the Monte-Carlo spread with the proposed values.
	ToleranceAfter *MCResult `json:"toleranceAfter,omitempty"`
}

// Change is one proposed value change.
type Change struct {
	Ref       string  `json:"ref"`
	Block     string  `json:"block"`
	Role      string  `json:"role"`
	Kind      string  `json:"kind"`
	From      string  `json:"from"`
	To        string  `json:"to"`
	FromValue float64 `json:"fromValue"`
	ToValue   float64 `json:"toValue"`
	Series    string  `json:"series"` // E24 | E96 | E12 | E6 | stock
	Package   string  `json:"package,omitempty"`
	// Part is a stocked standard part carrying the new value (standard-parts.json).
	Part   *StockPart `json:"part,omitempty"`
	Action string     `json:"action"` // replace-lcsc | set-value
	// NeedsPartSelection: no stocked part — pick one (lib by-lcsc / parts-select.py)
	// before compiling; a Value-only edit leaves the old LCSC/MPN on the BOM.
	NeedsPartSelection bool               `json:"needsPartSelection,omitempty"`
	SearchHint         string             `json:"searchHint,omitempty"`
	Reason             string             `json:"reason"`
	Before             map[string]float64 `json:"before"`
	After              map[string]float64 `json:"after"`
}

// StockPart is a standard-parts.json entry.
type StockPart struct {
	Key        string `json:"key"`
	LCSC       string `json:"lcsc"`
	MPN        string `json:"mpn,omitempty"`
	DeviceUUID string `json:"deviceUuid,omitempty"`
	Footprint  string `json:"footprint,omitempty"`
	Basic      bool   `json:"basic,omitempty"`
	Value      string `json:"value"`
}

// Plan is the schematic value-change plan (--apply-plan).
type Plan struct {
	SchemaVersion int    `json:"schemaVersion"`
	Kind          string `json:"kind"` // pcbpilot.schematic-value-plan
	Generator     string `json:"generator"`
	// RequiresUserConfirmation is always true: value changes are schematic
	// edits; show the before/after and wait for the user's explicit yes.
	RequiresUserConfirmation bool     `json:"requiresUserConfirmation"`
	Changes                  []Change `json:"changes"`
	Apply                    []string `json:"apply"`
	AfterApply               []string `json:"afterApply"`
	Notes                    []string `json:"notes,omitempty"`
}
