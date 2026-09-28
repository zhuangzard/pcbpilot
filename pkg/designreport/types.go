// Package designreport builds the customer-facing, versioned DESIGN REPORT of
// a pcbpilot run: power simulation with charts, per-component feasibility
// (stress vs rating), engineering calculations, layout/routing evidence,
// verification status, a test-point plan, manufacturing/assembly notes and a
// bring-up procedure.
//
// Every number in the report is computed from an input document (intent.json,
// sim.json, pcb auto plan.json, a board dump, DRC / check outputs, the power
// model library, part MPNs) or from a documented guideline table in this
// package. A section whose inputs are missing is reported as "not available"
// with the reason — nothing is invented. report.json (schemaVersion 1) is the
// machine-readable form: fields may be added, never renamed.
//
// The package does no file-system I/O except template loading from its
// embedded copy; the CLI (`pcbpilot report design`) reads the inputs, writes
// the version directory and maintains index.json / CHANGELOG.md.
package designreport

// SchemaVersion of report.json and index.json.
const SchemaVersion = 1

// Generator is written into every document.
const Generator = "pcbpilot report design"

// Verdict values.
const (
	VerdictPass     = "PASS"
	VerdictWarnings = "PASS with warnings"
	VerdictFail     = "FAIL"
)

// Check / row status values.
const (
	StatusPass = "PASS"
	StatusWarn = "WARN"
	StatusFail = "FAIL"
	StatusNA   = "N/A"
)

// Feasibility row status values.
const (
	FeasOK       = "ok"       // margin ≥ guideline
	FeasMarginal = "marginal" // 0 ≤ margin < guideline
	FeasOver     = "over"     // stress exceeds the rating
	FeasUnknown  = "unknown"  // rating needs the datasheet
)

// Report is report.json.
type Report struct {
	SchemaVersion int    `json:"schemaVersion"`
	Generator     string `json:"generator"`
	// GeneratedAt is the only time-dependent field (header only).
	GeneratedAt  string     `json:"generatedAt"`
	Project      string     `json:"project"`
	Customer     string     `json:"customer,omitempty"`
	Version      int        `json:"version"`
	VersionLabel string     `json:"versionLabel"`
	Tools        ToolInfo   `json:"tools"`
	Inputs       []InputRef `json:"inputs"`
	// InputsDigest is sha256 over the present inputs' digests (changelog key).
	InputsDigest string   `json:"inputsDigest"`
	Verdict      Verdict  `json:"verdict"`
	Changes      *Changes `json:"changes,omitempty"`

	Summary       SummarySection       `json:"summary"`
	Requirements  *RequirementsSection `json:"requirements,omitempty"`
	Power         *PowerSection        `json:"power,omitempty"`
	Analog        *AnalogSection       `json:"analog,omitempty"` // §3A, see analog.go
	Feasibility   *FeasibilitySection  `json:"feasibility,omitempty"`
	Calcs         *CalcSection         `json:"calculations,omitempty"`
	Layout        *LayoutSection       `json:"layout,omitempty"`
	Post          *PostSection         `json:"postLayout,omitempty"`
	Verification  []Check              `json:"verification"`
	TestPlan      *TestPlanSection     `json:"testPlan,omitempty"`
	Manufacturing *MfgSection          `json:"manufacturing,omitempty"`
	BringUp       *BringUpSection      `json:"bringUp,omitempty"`
	Appendix      AppendixSection      `json:"appendix"`
	// Data maps an input kind to its file inside the report package.
	Data map[string]string `json:"data,omitempty"`
	// Package is the zip of this version (reports/<name>/<Package>).
	Package string `json:"package,omitempty"`
	// Missing lists the sections (or parts of them) left out and why.
	Missing []Missing `json:"missing,omitempty"`
	// ProcessSkips lists the steps and sections the project's process
	// template (pcbpilot.project.json) skipped on purpose, with reasons.
	ProcessSkips *ProcessSkips `json:"processSkips,omitempty"`
}

// ProcessSkips is the project process template's deliberate omissions.
type ProcessSkips struct {
	Template string    `json:"template,omitempty"`
	Source   string    `json:"source,omitempty"`
	Steps    []Missing `json:"steps,omitempty"`
	Sections []Missing `json:"sections,omitempty"`
}

// ToolInfo names the tool chain the evidence came from.
type ToolInfo struct {
	Pcbpilot  string `json:"pcbpilot"`
	Host      string `json:"host,omitempty"`
	Connector string `json:"connector,omitempty"`
}

// InputRef is one input's provenance.
type InputRef struct {
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	Path    string `json:"path,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
	Bytes   int    `json:"bytes,omitempty"`
	Present bool   `json:"present"`
	Note    string `json:"note,omitempty"`
}

// Verdict is the overall judgement.
type Verdict struct {
	Status   string   `json:"status"`
	Reasons  []string `json:"reasons,omitempty"`  // FAIL causes
	Warnings []string `json:"warnings,omitempty"` // PASS-with-warnings causes
}

// Missing records a section that could not be computed.
type Missing struct {
	Section string `json:"section"`
	Reason  string `json:"reason"`
}

// KV is a labelled value.
type KV struct {
	Label string `json:"label"`
	Value string `json:"value"`
	Note  string `json:"note,omitempty"`
}

// Risk is one top risk of the executive summary.
type Risk struct {
	Severity   string `json:"severity"` // error | warn | info
	Source     string `json:"source"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion,omitempty"`
}

// SummarySection is §1.
type SummarySection struct {
	KeyNumbers []KV   `json:"keyNumbers"`
	TopRisks   []Risk `json:"topRisks"`
}

// RequirementsSection is §2.
type RequirementsSection struct {
	Standard []KV        `json:"standard"`
	Copper   []KV        `json:"copper,omitempty"`
	Blocks   []BlockRow  `json:"blocks"`
	Domains  []DomainRow `json:"domains"`
	Findings []Risk      `json:"findings"`
	Pairs    int         `json:"pairs"`
}

// BlockRow is one functional block.
type BlockRow struct {
	ID       string  `json:"id"`
	Function string  `json:"function"`
	Core     string  `json:"core"`
	Parts    string  `json:"parts"`
	Summary  string  `json:"summary"`
	PowerW   float64 `json:"powerW,omitempty"`
}

// DomainRow is one voltage domain.
type DomainRow struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	Reference    string  `json:"reference"`
	WorkingVrms  float64 `json:"workingVrms"`
	WorkingVpeak float64 `json:"workingVpeak"`
	Nets         int     `json:"nets"`
	Parts        int     `json:"parts"`
}

// PowerSection is §3.
type PowerSection struct {
	Converged   bool            `json:"converged"`
	Scenarios   []string        `json:"scenarios"`
	Rails       []RailRow       `json:"rails"`
	ScenarioPwr []ScenarioPower `json:"scenarioPower"`
	PartPower   []PartPowerRow  `json:"partPower"`
	RailPower   []RailPowerRow  `json:"railPower"`
	Tree        PowerTree       `json:"tree"`
	Regulators  []RegulatorRow  `json:"regulators"`
	Ripple      []RippleRow     `json:"ripple"`
	Models      []ModelRow      `json:"models"`
	Confidence  map[string]int  `json:"confidence"`
	Assumptions []string        `json:"assumptions"`
	Warnings    []string        `json:"warnings"`
}

// RailRow is one power rail over the scenarios (Currents align with Scenarios).
type RailRow struct {
	Net      string    `json:"net"`
	Role     string    `json:"role"`
	VNom     float64   `json:"vNom"`
	VMin     float64   `json:"vMin"`
	VMax     float64   `json:"vMax"`
	Voltages []float64 `json:"voltages"`
	Currents []float64 `json:"currentsA"`
	IMaxA    float64   `json:"iMaxA"`
	Worst    string    `json:"worstScenario"`
}

// ScenarioPower is the power balance of one scenario.
type ScenarioPower struct {
	Scenario  string  `json:"scenario"`
	SuppliedW float64 `json:"suppliedW"`
	LoadW     float64 `json:"loadW"`
	LossW     float64 `json:"lossW"`
}

// PartPowerRow is one part's worst dissipation.
type PartPowerRow struct {
	Ref      string  `json:"ref"`
	Device   string  `json:"device,omitempty"`
	Kind     string  `json:"kind"`
	PowerW   float64 `json:"powerW"`
	Scenario string  `json:"scenario"`
}

// RailPowerRow is V × I of one rail at its worst current.
type RailPowerRow struct {
	Net    string  `json:"net"`
	V      float64 `json:"v"`
	IA     float64 `json:"iA"`
	PowerW float64 `json:"powerW"`
}

// PowerTree is the source → OR → regulator → load diagram.
type PowerTree struct {
	Nodes []TreeNode `json:"nodes"`
	Edges []TreeEdge `json:"edges"`
}

// TreeNode is one part in the power tree.
type TreeNode struct {
	Ref    string `json:"ref"`
	Kind   string `json:"kind"` // source | or-diode | regulator | load
	Col    int    `json:"col"`
	Label  string `json:"label"`
	Detail string `json:"detail"`
}

// TreeEdge is a rail between two parts.
type TreeEdge struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Net   string `json:"net"`
	Label string `json:"label"`
}

// RegulatorRow is one regulator operating point in one scenario.
type RegulatorRow struct {
	Ref      string   `json:"ref"`
	Device   string   `json:"device,omitempty"`
	Kind     string   `json:"kind"`
	Scenario string   `json:"scenario"`
	Mode     string   `json:"mode"`
	VinV     float64  `json:"vinV"`
	VoutV    float64  `json:"voutV"`
	IoutA    float64  `json:"ioutA"`
	IinA     float64  `json:"iinA"`
	Duty     float64  `json:"duty,omitempty"`
	Eff      float64  `json:"efficiency"`
	LossW    float64  `json:"lossW"`
	Notes    []string `json:"notes,omitempty"`
}

// RippleRow is one switching-current estimate.
type RippleRow struct {
	Key       string  `json:"key"`
	Kind      string  `json:"kind"` // switch-node | inductor | capacitor
	Regulator string  `json:"regulator"`
	IPeakA    float64 `json:"iPeakA,omitempty"`
	IRmsA     float64 `json:"iRmsA"`
	IAvgA     float64 `json:"iAvgA,omitempty"`
	DeltaIA   float64 `json:"deltaIA,omitempty"`
	Duty      float64 `json:"duty,omitempty"`
	Scenario  string  `json:"scenario"`
}

// ModelRow is the power model bound to one part.
type ModelRow struct {
	Ref        string `json:"ref"`
	Device     string `json:"device,omitempty"`
	Kind       string `json:"kind"`
	ModelID    string `json:"modelId"`
	Match      string `json:"match"`
	Confidence string `json:"confidence"`
	Source     string `json:"source,omitempty"`
	Assumed    bool   `json:"assumed"`
}

// FeasibilitySection is §4.
type FeasibilitySection struct {
	Rows       []FeasRow      `json:"rows"`
	Counts     map[string]int `json:"counts"`
	Guidelines []KV           `json:"guidelines"`
}

// FeasRow is one stress-vs-rating check.
type FeasRow struct {
	Ref          string   `json:"ref"`
	Device       string   `json:"device,omitempty"`
	Kind         string   `json:"kind"`
	Check        string   `json:"check"`
	Stress       float64  `json:"stress"`
	Unit         string   `json:"unit"`
	StressNote   string   `json:"stressNote,omitempty"`
	Rating       float64  `json:"rating,omitempty"`
	RatingSource string   `json:"ratingSource,omitempty"`
	MarginPct    *float64 `json:"marginPct,omitempty"`
	GuidePct     float64  `json:"guidelinePct"`
	Status       string   `json:"status"`
	Note         string   `json:"note,omitempty"`
}

// CalcSection is §5.
type CalcSection struct {
	Basis      []KV        `json:"basis"`
	Formulas   []KV        `json:"formulas"`
	Widths     []WidthCalc `json:"widths"`
	Vias       []ViaCalc   `json:"vias"`
	Clearance  []ClearCalc `json:"clearance"`
	Impedance  []ImpCalc   `json:"impedance"`
	Insulation []InsCalc   `json:"insulation"`
	Notes      []string    `json:"notes,omitempty"`
}

// WidthCalc is the IPC-2221/2152 width check of one net.
type WidthCalc struct {
	Net          string  `json:"net"`
	Class        string  `json:"class"`
	CurrentA     float64 `json:"currentA"`
	Source       string  `json:"source"`
	OuterNeedMil float64 `json:"outerNeedMil"`
	InnerNeedMil float64 `json:"innerNeedMil"`
	OuterPlanMil float64 `json:"outerPlanMil"`
	InnerPlanMil float64 `json:"innerPlanMil"`
	OuterCapA    float64 `json:"outerCapacityA"`
	Status       string  `json:"status"`
}

// ViaCalc is the via count of one net.
type ViaCalc struct {
	Net      string  `json:"net"`
	CurrentA float64 `json:"currentA"`
	DrillMil float64 `json:"drillMil"`
	PerViaA  float64 `json:"perViaA"`
	Need     int     `json:"need"`
	Plan     int     `json:"plan"`
	Status   string  `json:"status"`
}

// ClearCalc is the voltage clearance of one net.
type ClearCalc struct {
	Net     string  `json:"net"`
	VPeak   float64 `json:"vPeak"`
	IPCMil  float64 `json:"ipcMil"`
	FabMil  float64 `json:"fabMil"`
	PlanMil float64 `json:"planMil"`
	Governs string  `json:"governs"`
	Status  string  `json:"status"`
}

// ImpCalc is an impedance recomputation.
type ImpCalc struct {
	Name   string   `json:"name"`
	Nets   string   `json:"nets"`
	Kind   string   `json:"kind"` // single | diff
	WMil   float64  `json:"wMil"`
	GapMil float64  `json:"gapMil,omitempty"`
	HMil   float64  `json:"hMil"`
	TMil   float64  `json:"tMil"`
	Er     float64  `json:"er"`
	Z0     float64  `json:"z0"`
	Zdiff  float64  `json:"zdiff,omitempty"`
	Target float64  `json:"target"`
	DevPct float64  `json:"devPct"`
	Status string   `json:"status"`
	Routed *float64 `json:"routedWMil,omitempty"`
}

// InsCalc is the insulation requirement of one domain pair.
type InsCalc struct {
	A            string  `json:"a"`
	B            string  `json:"b"`
	WorkingVrms  float64 `json:"workingVrms"`
	WorkingVpeak float64 `json:"workingVpeak"`
	Insulation   string  `json:"insulation"`
	ClearanceMm  float64 `json:"clearanceMm"`
	CreepageMm   float64 `json:"creepageMm"`
	Slot         bool    `json:"slotRequired"`
	SlotWidthMm  float64 `json:"slotWidthMm,omitempty"`
	StandardRef  string  `json:"standardRef"`
	TestVrms     float64 `json:"testVoltageVrms,omitempty"`
	TestRef      string  `json:"testVoltageRef,omitempty"`
}

// LayoutSection is §6.
type LayoutSection struct {
	Images      []Image     `json:"images"`
	StackupName string      `json:"stackupName,omitempty"`
	Stackup     []StackRow  `json:"stackup"`
	StackNotes  []string    `json:"stackupReasons,omitempty"`
	Routing     []KV        `json:"routing"`
	Joint       []KV        `json:"joint,omitempty"`
	IRBudget    string      `json:"irBudget,omitempty"`
	IRNets      []IRNetRow  `json:"irNets"`
	IRPads      []IRPadRow  `json:"irPads"`
	Segments    []SegRow    `json:"segments"`
	SegmentsAll int         `json:"segmentsTotal"`
	WorstPaths  []PathRow   `json:"worstPaths"`
	IRModel     []string    `json:"irModel,omitempty"`
	SINets      []SINetRow  `json:"siNets"`
	SIPairs     []SIPairRow `json:"siPairs"`
	SIFindings  []string    `json:"siFindings"`
	Isolation   []string    `json:"isolation"`
	Feedback    []string    `json:"feedback"`
	Difficulty  []KV        `json:"difficulty,omitempty"`
}

// Image is one embedded picture.
type Image struct {
	Kind    string `json:"kind"` // sch | layout | preview
	Label   string `json:"label"`
	Path    string `json:"path"`
	SHA256  string `json:"sha256"`
	Asset   string `json:"asset"` // file name under the report's assets/
	Mime    string `json:"mime"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Bytes   int    `json:"bytes"`
	Resized bool   `json:"resized,omitempty"`
	Data    []byte `json:"-"`
}

// StackRow is one copper layer.
type StackRow struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Nets string `json:"nets,omitempty"`
	Dir  string `json:"dir,omitempty"`
}

// IRNetRow is the DC-drop result of one net.
type IRNetRow struct {
	Net       string  `json:"net"`
	Role      string  `json:"role"`
	VoltageV  float64 `json:"voltageV"`
	CurrentA  float64 `json:"currentA"`
	Reference string  `json:"reference"`
	BudgetMV  float64 `json:"budgetMV"`
	WorstMV   float64 `json:"worstMV"`
	WorstPad  string  `json:"worstPad"`
	RatioPct  float64 `json:"ratioPct"`
	Status    string  `json:"status"`
}

// IRPadRow is the drop at one load pad.
type IRPadRow struct {
	Net      string  `json:"net"`
	Pad      string  `json:"pad"`
	CurrentA float64 `json:"currentA"`
	DropMV   float64 `json:"dropMV"`
	BudgetMV float64 `json:"budgetMV"`
	RatioPct float64 `json:"ratioPct"`
}

// SegRow is one tapered power segment.
type SegRow struct {
	Net       string  `json:"net"`
	Layer     int     `json:"layer"`
	Kind      string  `json:"kind"`
	LengthMil float64 `json:"lengthMil"`
	CurrentA  float64 `json:"currentA"`
	RoutedMil float64 `json:"routedMil"`
	WidthMil  float64 `json:"widthMil"`
	NeedMil   float64 `json:"needMil"`
	DropMV    float64 `json:"dropMV"`
}

// PathRow is the worst drop path of one net.
type PathRow struct {
	Net     string     `json:"net"`
	Pad     string     `json:"pad"`
	TotalMV float64    `json:"totalMV"`
	Steps   []PathStep `json:"steps"`
}

// PathStep is one element of a worst path.
type PathStep struct {
	Kind   string  `json:"kind"`
	What   string  `json:"what"`
	DropMV float64 `json:"dropMV"`
}

// SINetRow is one measured high-speed net.
type SINetRow struct {
	Net       string  `json:"net"`
	Class     string  `json:"class"`
	LengthMil float64 `json:"lengthMil"`
	Vias      int     `json:"vias"`
	Layers    string  `json:"layers"`
}

// SIPairRow is one measured differential pair.
type SIPairRow struct {
	P        string  `json:"p"`
	N        string  `json:"n"`
	SkewMil  float64 `json:"skewMil"`
	LimitMil float64 `json:"limitMil"`
	Status   string  `json:"status"`
}

// Check is one verification item of §7.
type Check struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Detail   string `json:"detail"`
	Evidence string `json:"evidence,omitempty"`
	SHA256   string `json:"sha256,omitempty"`
	// Data is the evidence file inside the report package (data/…).
	Data string `json:"data,omitempty"`
}

// TestPlanSection is §8.
type TestPlanSection struct {
	Items []TestItem         `json:"items"`
	AddTP []TPRecommendation `json:"addTestPoints"`
	Notes []string           `json:"notes,omitempty"`
}

// TestItem is one bring-up / production measurement.
type TestItem struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Net      string `json:"net,omitempty"`
	What     string `json:"what"`
	Where    string `json:"where"`
	Expected string `json:"expected"`
	Limit    string `json:"limit,omitempty"`
	Method   string `json:"method"`
	Basis    string `json:"basis"`
}

// TPRecommendation proposes a test point where no accessible pad exists.
type TPRecommendation struct {
	Net    string  `json:"net"`
	Reason string  `json:"reason"`
	Near   string  `json:"near"`
	XMm    float64 `json:"xMm"`
	YMm    float64 `json:"yMm"`
}

// MfgSection is §9.
type MfgSection struct {
	Fab      []KV     `json:"fab"`
	DFM      []DFMRow `json:"dfm"`
	Assembly []AsmRow `json:"assembly"`
	HV       []string `json:"hv"`
	Handling []string `json:"handling"`
}

// DFMRow groups pcb check findings by type.
type DFMRow struct {
	Level   string `json:"level"`
	Type    string `json:"type"`
	Count   int    `json:"count"`
	Example string `json:"example"`
}

// AsmRow is an assembly attention item.
type AsmRow struct {
	Ref     string `json:"ref"`
	Device  string `json:"device,omitempty"`
	Concern string `json:"concern"`
	Note    string `json:"note"`
}

// BringUpSection is §10.
type BringUpSection struct {
	Steps      []Step      `json:"steps"`
	Shorts     []ShortRow  `json:"shortChecks"`
	Signatures []Signature `json:"signatures"`
}

// Step is one bring-up step.
type Step struct {
	N      int    `json:"n"`
	Title  string `json:"title"`
	Action string `json:"action"`
	Expect string `json:"expect"`
	IfFail string `json:"ifFail"`
}

// ShortRow is the unpowered rail-to-GND resistance check of one rail.
type ShortRow struct {
	Net       string `json:"net"`
	KnownPath string `json:"knownPath"`
	Expected  string `json:"expected"`
	FailBelow string `json:"failBelow"`
}

// Signature maps a failure symptom to its likely cause.
type Signature struct {
	Symptom string `json:"symptom"`
	Cause   string `json:"cause"`
	Check   string `json:"check"`
}

// AppendixSection is §11.
type AppendixSection struct {
	Nets     []NetRow  `json:"nets"`
	Parts    []PartRow `json:"parts"`
	Glossary []KV      `json:"glossary"`
}

// NetRow is one net of the full net table.
type NetRow struct {
	Net       string  `json:"net"`
	Role      string  `json:"role"`
	Class     string  `json:"class,omitempty"`
	Block     string  `json:"block,omitempty"`
	VNom      float64 `json:"vNom"`
	VMax      float64 `json:"vMax"`
	CurrentA  float64 `json:"currentA"`
	Source    string  `json:"source,omitempty"`
	OuterMil  float64 `json:"outerMil,omitempty"`
	InnerMil  float64 `json:"innerMil,omitempty"`
	ClearMil  float64 `json:"clearanceMil,omitempty"`
	Impedance float64 `json:"impedanceOhm,omitempty"`
	Pads      int     `json:"pads,omitempty"`
}

// PartRow is one part with its model and parsed ratings.
type PartRow struct {
	Ref        string `json:"ref"`
	Device     string `json:"device,omitempty"`
	Kind       string `json:"kind"`
	ModelID    string `json:"modelId,omitempty"`
	Confidence string `json:"confidence,omitempty"`
	Parsed     string `json:"parsed,omitempty"`
	Block      string `json:"block,omitempty"`
}

// Changes compares this version with the previous one.
type Changes struct {
	Previous         string      `json:"previous"`
	Identical        bool        `json:"inputsIdentical"`
	InputsChanged    []string    `json:"inputsChanged,omitempty"`
	Rows             []ChangeRow `json:"rows"`
	FindingsAdded    []string    `json:"findingsAdded,omitempty"`
	FindingsResolved []string    `json:"findingsResolved,omitempty"`
}

// ChangeRow is one metric delta.
type ChangeRow struct {
	Metric string `json:"metric"`
	Before string `json:"before"`
	After  string `json:"after"`
	Delta  string `json:"delta"`
}
