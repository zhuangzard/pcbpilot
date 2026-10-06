package postsim

import "github.com/zhuangzard/pcbpilot/pkg/pcbauto"

// Output document of `pcbpilot sim post-layout` (schemaVersion 1). Board
// coordinates are mil (EasyEDA, y-up), temperatures °C, currents A, current
// densities A/mm², drops mV.

// Result is the whole post-layout verification.
type Result struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Generator     string                  `json:"generator"`
	Inputs        Inputs                  `json:"inputs"`
	Settings      Settings                `json:"settings"`
	Limits        Limits                  `json:"limits"`
	Stackup       *Stackup                `json:"stackup"`
	Grid          GridInfo                `json:"grid"`
	Scenarios     []string                `json:"scenarios"`
	Mode          string                  `json:"mode"` // scenarios | merged-envelope
	Nets          []*NetResult            `json:"nets"`
	Vias          []ViaResult             `json:"vias"`
	Thermal       *ThermalResult          `json:"thermal,omitempty"`
	Compare       []CompareRow            `json:"compare,omitempty"`
	Findings      []Finding               `json:"findings"`
	Assumptions   []string                `json:"assumptions"`
	Model         []string                `json:"model"`
	Feedback      []*pcbauto.FeedbackItem `json:"feedback"`
	Verdict       Verdict                 `json:"verdict"`
	Maps          []MapFile               `json:"maps,omitempty"`
	MapsDir       string                  `json:"mapsDir,omitempty"` // where WriteMaps put them (as given)
	Elmer         *ElmerCheck             `json:"elmer,omitempty"`

	// Rasters for the heat maps (not serialised).
	TempMap   [][]float64 `json:"-"` // per stack layer, per cell (°C, NaN off-board)
	JMap      [][]float64 `json:"-"` // per stack layer, per cell (A/mm², 0 = none)
	grid      *Grid
	board     *Board
	thermal   *thermalModel
	hotQ      []float64        // heat per thermal unknown, hottest scenario
	hotTheta  []float64        // rise per thermal unknown, hottest scenario
	partCells map[string][]int // part → k*nc+c cells under its pads
}

// Inputs is the provenance of one run.
type Inputs struct {
	Board         string `json:"board,omitempty"`
	BoardSHA256   string `json:"boardSha256,omitempty"`
	BoardSemantic string `json:"boardSemanticSha256,omitempty"`
	Sim           string `json:"sim,omitempty"`
	SimSHA256     string `json:"simSha256,omitempty"`
	Intent        string `json:"intent,omitempty"`
	Models        string `json:"models,omitempty"`
	Source        string `json:"source,omitempty"` // live readback | pcb auto result
}

// Settings echo the physical parameters.
type Settings struct {
	AmbientC    float64           `json:"ambientC"`
	HTop        float64           `json:"hTopWm2K"`
	HBottom     float64           `json:"hBottomWm2K"`
	Emissivity  float64           `json:"emissivity,omitempty"`
	KFR4XY      float64           `json:"kFr4InPlane"`
	KFR4Z       float64           `json:"kFr4ThroughPlane"`
	PlatingMil  float64           `json:"viaPlatingMil"`
	ViaDeltaTC  float64           `json:"viaAmpacityDeltaTC"`
	IRBudget    string            `json:"irBudget"`
	BoardLimitC float64           `json:"boardWarnC"`
	TjWarnPct   float64           `json:"tjWarnPct"`
	Planes      map[string]string `json:"planes,omitempty"`
}

// GridInfo describes the raster.
type GridInfo struct {
	CellMm float64 `json:"cellMm"`
	NX     int     `json:"nx"`
	NY     int     `json:"ny"`
	Sub    int     `json:"subSamples"`
	Cells  int     `json:"boardCellsPerLayer"`
}

// PadResult is one simulated pad.
type PadResult struct {
	Pad      string  `json:"pad"`
	Dir      string  `json:"dir"`
	CurrentA float64 `json:"currentA"`
	DropMV   float64 `json:"dropMV"` // |V(reference) − V(pad)|; -1 = no copper path
	VoltageV float64 `json:"voltageV,omitempty"`
	Scenario string  `json:"scenario,omitempty"`
	OK       bool    `json:"ok"`
}

// Hotspot is a high current-density or high-temperature location.
type Hotspot struct {
	Layer    string  `json:"layer"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Value    float64 `json:"value"` // A/mm² or °C
	Kind     string  `json:"kind"`  // track | sheet | via | cell
	WidthMil float64 `json:"widthMil,omitempty"`
	CurrentA float64 `json:"currentA,omitempty"`
	What     string  `json:"what,omitempty"`
}

// Segment is one track piece with its current and loss.
type Segment struct {
	Layer     string  `json:"layer"`
	A         Point   `json:"a"`
	B         Point   `json:"b"`
	WidthMil  float64 `json:"widthMil"`
	LengthMil float64 `json:"lengthMil"`
	CurrentA  float64 `json:"currentA"`
	JAmm2     float64 `json:"jAmm2"`
	PowerMW   float64 `json:"powerMW"`
	DropMV    float64 `json:"dropMV"`
	CapacityA float64 `json:"capacityA"` // IPC-2221 external curve at the ampacity ΔT
}

// ScenarioDrop is one scenario's result for a net.
type ScenarioDrop struct {
	Scenario  string  `json:"scenario"`
	Reference string  `json:"reference"`
	CurrentA  float64 `json:"currentA"`
	WorstMV   float64 `json:"worstMV"`
	WorstPad  string  `json:"worstPad,omitempty"`
	LossMW    float64 `json:"lossMW"`
	Iter      int     `json:"cgIterations"`
}

// NetResult is the DC result of one net (worst over scenarios).
type NetResult struct {
	Net       string      `json:"net"`
	Role      string      `json:"role"`
	NominalV  float64     `json:"nominalV"`
	CurrentA  float64     `json:"currentA"`
	Scenario  string      `json:"scenario"`
	Reference string      `json:"reference"`
	BudgetMV  float64     `json:"budgetMV,omitempty"`
	WorstMV   float64     `json:"worstMV"`
	WorstPad  string      `json:"worstPad,omitempty"`
	Status    string      `json:"status"` // ok | over-budget | open | info | no-copper | no-reference
	Pads      []PadResult `json:"pads"`
	MaxJAmm2  float64     `json:"maxJAmm2"`
	Hotspots  []Hotspot   `json:"hotspots,omitempty"`
	Segments  []Segment   `json:"topSegments,omitempty"`
	// AllSegments is every track piece with its worst current (not
	// serialised): the per-segment width gate judges each track by it.
	AllSegments   []Segment      `json:"-"`
	LossMW        float64        `json:"lossMW"`
	ViaCount      int            `json:"viaCount"`
	MaxViaA       float64        `json:"maxViaA"`
	SheetCells    int            `json:"sheetCells"`
	MaxTraceRiseC float64        `json:"maxTraceRiseC"` // copper self-heating over the board (Joule-only solve)
	TrackPieces   int            `json:"trackPieces"`
	PerScenario   []ScenarioDrop `json:"perScenario"`
	Notes         []string       `json:"notes,omitempty"`
}

// ViaResult is the worst current of one via.
type ViaResult struct {
	ID        string  `json:"id"`
	Net       string  `json:"net"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	DrillMil  float64 `json:"drillMil"`
	CurrentA  float64 `json:"currentA"`
	AmpacityA float64 `json:"ampacityA"`
	UsePct    float64 `json:"usePct"`
	Scenario  string  `json:"scenario"`
}

// PartThermal is one part's board temperature and junction estimate.
type PartThermal struct {
	Ref        string  `json:"ref"`
	Device     string  `json:"device,omitempty"`
	Side       string  `json:"side"`
	PowerW     float64 `json:"powerW"`
	Scenario   string  `json:"scenario"`
	BoardMaxC  float64 `json:"boardMaxC"`
	BoardMeanC float64 `json:"boardMeanC"`
	ThetaCW    float64 `json:"thetaCW,omitempty"`
	ThetaKind  string  `json:"thetaKind,omitempty"` // θJB | θJC
	TjC        float64 `json:"tjC,omitempty"`
	TjMaxC     float64 `json:"tjMaxC,omitempty"`
	TjPct      float64 `json:"tjPctOfMax,omitempty"`
	Status     string  `json:"status"` // ok | warn | fail | needs-datasheet
	Note       string  `json:"note,omitempty"`
}

// LayerTemp is one layer's temperature summary.
type LayerTemp struct {
	Layer string  `json:"layer"`
	MaxC  float64 `json:"maxC"`
	MeanC float64 `json:"meanC"`
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
}

// ScenarioHeat is one scenario's thermal solve.
type ScenarioHeat struct {
	Scenario string  `json:"scenario"`
	PartsW   float64 `json:"partsW"`
	JouleW   float64 `json:"jouleW"`
	MaxC     float64 `json:"maxC"`
	LossW    float64 `json:"lossW"`
	ErrPct   float64 `json:"balanceErrPct"`
	Iter     int     `json:"cgIterations"`
}

// ThermalResult is the steady-state thermal verification.
type ThermalResult struct {
	Scenario      string         `json:"scenario"` // hottest scenario (maps)
	MaxBoardC     float64        `json:"maxBoardC"`
	MaxAt         Hotspot        `json:"maxAt"`
	TotalW        float64        `json:"totalW"`
	PartsW        float64        `json:"partsW"`
	JouleW        float64        `json:"jouleW"`
	LossW         float64        `json:"lossW"`
	BalanceErrPct float64        `json:"balanceErrPct"`
	Layers        []LayerTemp    `json:"layers"`
	Parts         []PartThermal  `json:"parts"`
	PerScenario   []ScenarioHeat `json:"perScenario"`
	// Joule-only solve: the rise the copper losses alone cause.
	JouleScenario  string  `json:"jouleScenario,omitempty"`
	MaxCopperRiseC float64 `json:"maxCopperRiseC"`
}

// CompareRow sets the post-layout drop next to pcb auto's IR-drop estimate.
type CompareRow struct {
	Net      string  `json:"net"`
	AutoMV   float64 `json:"autoMV"`
	AutoPad  string  `json:"autoPad,omitempty"`
	PostMV   float64 `json:"postMV"`
	PostPad  string  `json:"postPad,omitempty"`
	DeltaMV  float64 `json:"deltaMV"`
	Scenario string  `json:"postScenario,omitempty"`
}

// Finding is one warning / failure.
type Finding struct {
	Severity string   `json:"severity"` // fail | warn | info
	Kind     string   `json:"kind"`
	Message  string   `json:"message"`
	Refs     []string `json:"refs,omitempty"`
	Nets     []string `json:"nets,omitempty"`
}

// Verdict is the overall status.
type Verdict struct {
	Status  string   `json:"status"` // pass | warn | fail
	Reasons []string `json:"reasons,omitempty"`
}

// MapFile is one written heat map.
type MapFile struct {
	Kind  string  `json:"kind"` // temperature | current-density
	Layer string  `json:"layer"`
	File  string  `json:"file"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Unit  string  `json:"unit"`
	Log   bool    `json:"logScale,omitempty"`
}

// Limits echo the feedback thresholds.
type Limits struct {
	TempRiseC float64 `json:"tempRiseC"`
	Margin    float64 `json:"margin"`
	JMaxAmm2  float64 `json:"jMaxAmm2,omitempty"`
}
