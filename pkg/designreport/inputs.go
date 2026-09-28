package designreport

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// Inputs are the parsed documents one report is built from. Every field is
// optional; the report states which were present.
type Inputs struct {
	Project     string
	Customer    string
	Tools       ToolInfo
	GeneratedAt time.Time

	Intent      *intent.Intent
	Sim         *powersim.Output
	Plan        *pcbauto.Report
	Feedback    *pcbauto.Feedback
	Board       *Board
	ReloadBoard *Board
	DRC         *DRCResult
	Check       *CheckResult
	RulesCheck  *RulesCheck
	NetDiff     *NetDiff
	Models      *ModelLib
	Values      map[string]powersim.PartValues
	Images      []Image
	// Refs is the provenance of every input kind (present or not).
	Refs []InputRef
}

// Board is the subset of a `pcb dump` document the report reads (units mil).
type Board struct {
	Components []BoardPart `json:"components"`
	Outline    struct {
		BBox struct {
			MinX float64 `json:"minX"`
			MinY float64 `json:"minY"`
			MaxX float64 `json:"maxX"`
			MaxY float64 `json:"maxY"`
		} `json:"bbox"`
		Points [][]float64 `json:"points"`
	} `json:"outline"`
	CopperLayers int        `json:"copperLayers"`
	Rules        BoardRules `json:"rules"`
	Copper       struct {
		Lines []struct {
			Net       string  `json:"net"`
			Layer     int     `json:"layer"`
			LineWidth float64 `json:"lineWidth"`
		} `json:"lines"`
		Vias json.RawMessage `json:"vias"`
	} `json:"copper"`
	FootprintHoles []json.RawMessage `json:"footprintHoles"`
	CapturedAt     string            `json:"capturedAt"`
	Project        string            `json:"project"`
	SemanticSHA256 string            `json:"semanticSha256"`
	ContentSHA256  string            `json:"contentSha256"`

	// raw is the dump document (the copper-to-edge measurement re-reads it).
	raw []byte
}

// BoardRules are the live design rules of the dump.
type BoardRules struct {
	ClearanceMil     float64 `json:"clearanceMil"`
	TrackTrackMil    float64 `json:"clearanceTrackTrackMil"`
	TrackWidthMil    float64 `json:"trackWidthMil"`
	PowerWidthMil    float64 `json:"powerWidthMil"`
	TrackWidthMinMil float64 `json:"trackWidthMinMil"`
	ViaDrillMil      float64 `json:"viaDrillMil"`
	ViaDiameterMil   float64 `json:"viaDiameterMil"`
	CopperToEdgeMil  float64 `json:"copperToEdgeMil"`
	HoleToHoleMil    float64 `json:"holeToHoleMil"`
	SlotClearanceMil float64 `json:"slotClearanceMil"`
	Source           string  `json:"source"`
}

// BoardPart is one placed component.
type BoardPart struct {
	Designator string     `json:"designator"`
	Device     string     `json:"device"`
	Layer      int        `json:"layer"`
	X          float64    `json:"x"`
	Y          float64    `json:"y"`
	Rotation   float64    `json:"rotation"`
	Pads       []BoardPad `json:"pads"`
}

// BoardPad is one pad.
type BoardPad struct {
	PadNumber string  `json:"padNumber"`
	Net       string  `json:"net"`
	Layer     int     `json:"layer"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Width     float64 `json:"width"`
	Height    float64 `json:"height"`
}

// ViaCount is the number of vias in the dump (list or count).
func (b *Board) ViaCount() int {
	if b == nil || len(b.Copper.Vias) == 0 {
		return 0
	}
	var list []json.RawMessage
	if json.Unmarshal(b.Copper.Vias, &list) == nil {
		return len(list)
	}
	var n int
	if json.Unmarshal(b.Copper.Vias, &n) == nil {
		return n
	}
	return 0
}

// Part returns the component with a designator.
func (b *Board) Part(ref string) *BoardPart {
	if b == nil {
		return nil
	}
	for i := range b.Components {
		if b.Components[i].Designator == ref {
			return &b.Components[i]
		}
	}
	return nil
}

// ParseBoard reads a `pcb dump` JSON (bare or inside {"result":…}).
func ParseBoard(raw []byte) (*Board, error) {
	var env struct {
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &env) == nil && len(env.Result) > 0 && env.Result[0] == '{' {
		var probe map[string]json.RawMessage
		if json.Unmarshal(env.Result, &probe) == nil {
			if _, ok := probe["components"]; ok {
				raw = env.Result
			}
		}
	}
	var b Board
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, err
	}
	if len(b.Components) == 0 {
		return nil, fmt.Errorf("no components[] — not a pcb dump")
	}
	b.raw = raw
	return &b, nil
}

// DRCResult is a native `pcb drc` outcome.
type DRCResult struct {
	Passed     bool
	Violations int
	Kinds      []string
	CreatedAt  string
	Project    string
	Document   string
}

// ParseDRC reads `pcb drc` JSON: the daemon envelope {ok,result:{passed,violations}}
// or a bare {passed,violations}.
func ParseDRC(raw []byte) (*DRCResult, error) {
	var env struct {
		CreatedAt string          `json:"createdAt"`
		Result    json.RawMessage `json:"result"`
		Context   struct {
			ProjectName  string `json:"projectName"`
			DocumentUUID string `json:"documentUuid"`
		} `json:"context"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	body := raw
	if len(env.Result) > 0 {
		body = env.Result
	}
	var r struct {
		Passed     *bool             `json:"passed"`
		Violations []json.RawMessage `json:"violations"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Passed == nil {
		return nil, fmt.Errorf("no passed field — not a pcb drc result")
	}
	out := &DRCResult{Passed: *r.Passed, CreatedAt: env.CreatedAt, Project: env.Context.ProjectName, Document: env.Context.DocumentUUID}
	kinds := map[string]bool{}
	var walk func(json.RawMessage)
	walk = func(m json.RawMessage) {
		var node struct {
			Count     int               `json:"count"`
			List      []json.RawMessage `json:"list"`
			ErrorType string            `json:"errorType"`
			ObjType   string            `json:"errorObjType"`
		}
		if json.Unmarshal(m, &node) != nil {
			return
		}
		if node.ErrorType != "" || node.ObjType != "" {
			out.Violations++
			kinds[strings.TrimSpace(node.ObjType+" "+node.ErrorType)] = true
			return
		}
		for _, c := range node.List {
			walk(c)
		}
	}
	for _, v := range r.Violations {
		walk(v)
	}
	for k := range kinds {
		out.Kinds = append(out.Kinds, k)
	}
	sort.Strings(out.Kinds)
	return out, nil
}

// CheckResult is a normalised `pcb check` report (text or --json).
type CheckResult struct {
	Errors, Warns, Infos int
	Counts               map[string]int // summary counters (text) / per type (json)
	Items                []CheckItem
	Limits               []string
}

// CheckItem is one pcb check finding.
type CheckItem struct {
	Level   string
	Type    string
	Message string
	Nets    string
}

var (
	reCheckSummary = regexp.MustCompile(`ERROR=(\d+)\s+WARN=(\d+)`)
	reCheckCounter = regexp.MustCompile(`([A-Za-z]+)=(\d+)`)
	reCheckItem    = regexp.MustCompile(`^\s+(ERROR|WARN|INFO)\s+(\S+)\s+(.*?)\s*(\[[^\]]*\])?\s*$`)
)

// ParseCheck reads `pcb check` text output or its --json report.
func ParseCheck(raw []byte) (*CheckResult, error) {
	out := &CheckResult{Counts: map[string]int{}}
	if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '{' {
		var j struct {
			Findings []struct {
				Type    string   `json:"type"`
				Level   string   `json:"level"`
				Net     string   `json:"net"`
				Nets    []string `json:"nets"`
				Message string   `json:"message"`
			} `json:"findings"`
			Limitations []string `json:"limitations"`
		}
		if err := json.Unmarshal(t, &j); err != nil {
			return nil, err
		}
		for _, f := range j.Findings {
			nets := f.Net
			if len(f.Nets) > 0 {
				nets = strings.Join(f.Nets, " ")
			}
			out.add(CheckItem{Level: strings.ToUpper(f.Level), Type: f.Type, Message: f.Message, Nets: nets})
		}
		out.Limits = j.Limitations
		return out, nil
	}
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	sawSummary := false
	for sc.Scan() {
		line := sc.Text()
		if m := reCheckSummary.FindStringSubmatch(line); m != nil && strings.Contains(line, "|") {
			sawSummary = true
			_, rest, _ := strings.Cut(line, "|")
			for _, c := range reCheckCounter.FindAllStringSubmatch(rest, -1) {
				n, _ := strconv.Atoi(c[2])
				out.Counts[c[1]] = n
			}
			continue
		}
		if strings.HasPrefix(strings.TrimSpace(line), "LIMIT ") {
			out.Limits = append(out.Limits, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "LIMIT")))
			continue
		}
		if m := reCheckItem.FindStringSubmatch(line); m != nil {
			out.add(CheckItem{Level: m[1], Type: m[2], Message: m[3], Nets: strings.Trim(m[4], "[]")})
		}
	}
	if !sawSummary && len(out.Items) == 0 {
		return nil, fmt.Errorf("no pcb check summary line or findings")
	}
	return out, nil
}

func (c *CheckResult) add(it CheckItem) {
	c.Items = append(c.Items, it)
	switch it.Level {
	case "ERROR":
		c.Errors++
	case "WARN":
		c.Warns++
	default:
		c.Infos++
	}
}

// RulesCheck is `pcb rules check` (intent → EasyEDA rule sync).
type RulesCheck struct {
	Status   string `json:"status"`
	Verified bool   `json:"verified"`
	Intent   string `json:"intentSha256"`
	Plan     struct {
		Classes []struct {
			Name   string `json:"name"`
			Action string `json:"action"`
		} `json:"classes"`
		Conflicts     []json.RawMessage `json:"conflicts"`
		PendingWrites int               `json:"pendingWrites"`
		Advisories    []struct {
			Item   string `json:"item"`
			Detail string `json:"detail"`
		} `json:"advisories"`
	} `json:"plan"`
}

// ParseRulesCheck reads `pcb rules check` JSON.
func ParseRulesCheck(raw []byte) (*RulesCheck, error) {
	var r RulesCheck
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	if r.Status == "" {
		return nil, fmt.Errorf("no status — not a rules check result")
	}
	return &r, nil
}

// NetDiff is a pad-net reconciliation result (schematic netlist vs PCB pads).
type NetDiff struct {
	Passed  bool
	Diffs   int
	Summary string
}

// ParseNetDiff reads a pad-net diff JSON: {passed|ok|inSync, diffs|mismatches|
// missing|extra:[…]} (bare or inside {"result":…}).
func ParseNetDiff(raw []byte) (*NetDiff, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	if r, ok := m["result"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(r, &inner) == nil {
			m = inner
		}
	}
	out := &NetDiff{}
	known := false
	for _, k := range []string{"passed", "ok", "inSync", "match"} {
		var b bool
		if v, ok := m[k]; ok && json.Unmarshal(v, &b) == nil {
			out.Passed, known = b, true
			break
		}
	}
	var parts []string
	for _, k := range []string{"diffs", "mismatches", "missing", "extra", "changed"} {
		var list []json.RawMessage
		if v, ok := m[k]; ok && json.Unmarshal(v, &list) == nil {
			out.Diffs += len(list)
			parts = append(parts, fmt.Sprintf("%s %d", k, len(list)))
		}
	}
	if !known {
		if len(parts) == 0 {
			return nil, fmt.Errorf("no passed/diffs fields — not a pad-net diff")
		}
		out.Passed = out.Diffs == 0
	}
	out.Summary = strings.Join(parts, ", ")
	return out, nil
}

// ModelLib is the power-model library with the report's rating fields.
type ModelLib struct {
	Origin string
	Models map[string]*LibModel
}

// LibModel is the part of a power model the report uses.
type LibModel struct {
	ID         string       `json:"id"`
	Kind       string       `json:"kind"`
	MaxA       float64      `json:"maxA"`
	VinMinV    float64      `json:"vinMinV"`
	DropoutV   float64      `json:"dropoutV"`
	Vref       float64      `json:"vref"`
	FswHz      float64      `json:"fswHz"`
	Eta        float64      `json:"eta"`
	VfPoints   [][2]float64 `json:"vfPoints"`
	VfV        float64      `json:"vfV"`
	GpioMaxA   float64      `json:"gpioMaxA"`
	GpioPins   string       `json:"gpioPins"`
	Source     string       `json:"source"`
	Confidence string       `json:"confidence"`
	Ratings    *Ratings     `json:"ratings"`
}

// Ratings are the optional absolute/recommended limits of power-models.json
// "ratings" (additive; `sim power` ignores them). Every value must be cited in
// Source.
type Ratings struct {
	VinMaxV         float64 `json:"vinMaxV,omitempty"`
	VccMinV         float64 `json:"vccMinV,omitempty"`
	VccMaxV         float64 `json:"vccMaxV,omitempty"`
	VrrmV           float64 `json:"vrrmV,omitempty"`
	VrwmV           float64 `json:"vrwmV,omitempty"`
	IsatA           float64 `json:"isatA,omitempty"`
	IratedA         float64 `json:"iratedA,omitempty"`
	ContactA        float64 `json:"contactA,omitempty"`
	ThetaJACW       float64 `json:"thetaJaCW,omitempty"`
	TjMaxC          float64 `json:"tjMaxC,omitempty"`
	SourceVMinV     float64 `json:"sourceVMinV,omitempty"`
	SourceVMaxV     float64 `json:"sourceVMaxV,omitempty"`
	SourceBudgetA   float64 `json:"sourceBudgetA,omitempty"`
	VrefAccuracyPct float64 `json:"vrefAccuracyPct,omitempty"`
	Source          string  `json:"source,omitempty"`
}

// ParseModels reads a power-models.json.
func ParseModels(raw []byte, origin string) (*ModelLib, error) {
	var doc struct {
		Models []*LibModel `json:"models"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	lib := &ModelLib{Origin: origin, Models: map[string]*LibModel{}}
	for _, m := range doc.Models {
		if m.ID != "" {
			lib.Models[m.ID] = m
		}
	}
	return lib, nil
}

// Model returns a library model by id (nil when absent).
func (l *ModelLib) Model(id string) *LibModel {
	if l == nil {
		return nil
	}
	return l.Models[id]
}
