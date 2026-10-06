// Package projectconfig is the per-project process template: which design-flow
// steps, simulations and report sections a project runs or skips, plus the
// constraint sets (standards, requirement files, mechanics, fab profile,
// preferred/banned parts) the agent must honour.
//
// The file lives at <work dir>/pcbpilot.project.json. The Skill reads it first
// (design-flow.md); `pcbpilot project-config` and the console write it. It is a
// planning document only: it never authorises an EDA write and never relaxes a
// safety check (edge clearance, DRC, save → reload → readback stay mandatory
// for every step that does run).
package projectconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// FileName is the canonical config file name inside a project work dir.
const FileName = "pcbpilot.project.json"

// SchemaVersion is the current schema. Readers accept any version <= this and
// refuse newer files rather than silently dropping fields they don't know.
const SchemaVersion = 1

// Config is pcbpilot.project.json.
type Config struct {
	SchemaVersion int    `json:"schemaVersion"`
	Name          string `json:"name,omitempty"`
	// Template is the preset the config was initialised from (informational;
	// the explicit step/sim/section maps below are authoritative).
	Template string `json:"template,omitempty"`
	// EDA binds the work dir to the EasyEDA project the daemon routes to
	// (name or uuid) — the console uses it to find workflow state.
	EDA   EDABinding        `json:"eda,omitempty"`
	Steps map[string]Toggle `json:"steps"`
	Sims  map[string]Toggle `json:"sims"`
	// Report maps report section ids ("3A", "6A", …) to a toggle.
	Report      map[string]Toggle `json:"reportSections"`
	Constraints Constraints       `json:"constraints"`
	// Manual configures the board user manual that pcb auto route / pcb gate
	// regenerate after every run (board-manual gate).
	Manual *Manual `json:"manual,omitempty"`
	// Resources is the resource-library directory relative to the work dir.
	Resources string `json:"resourcesDir,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
	UpdatedBy string `json:"updatedBy,omitempty"`
}

// Manual is the board-manual configuration. Paths are relative to the work dir.
type Manual struct {
	// Notes is the human text (default pcbpilot.manual-notes.json).
	Notes string `json:"notes,omitempty"`
	// Out is an extra copy of the current manual (e.g. 05_Output/<board>_使用说明.html).
	Out string `json:"out,omitempty"`
	// PinMap is the FPGA/CPLD pin assignment file (Quartus .tcl/.qsf or .xdc).
	PinMap string `json:"pinMap,omitempty"`
	// Name is the <Board> part of the file name (default: the config name).
	Name string `json:"name,omitempty"`
	Lang string `json:"lang,omitempty"` // zh (default) | en
}

// ManualNotesFile is the default notes file of the board manual.
const ManualNotesFile = "pcbpilot.manual-notes.json"

// EDABinding names the EasyEDA project this work dir belongs to.
type EDABinding struct {
	Project string `json:"project,omitempty"`
}

// Toggle is one on/off switch with the reason a human gave for turning it off.
type Toggle struct {
	Enabled bool   `json:"enabled"`
	Reason  string `json:"reason,omitempty"`
}

// Constraints are the project-wide inputs every step must respect.
type Constraints struct {
	Standards    []string   `json:"standards,omitempty"`
	Requirements []string   `json:"requirements,omitempty"`
	Mech         string     `json:"mech,omitempty"`
	Fab          FabProfile `json:"fab,omitempty"`
	Parts        PartsRules `json:"parts,omitempty"`
	Notes        string     `json:"notes,omitempty"`
}

// FabProfile is the fabrication target.
type FabProfile struct {
	Profile string `json:"profile,omitempty"` // e.g. jlcpcb
	Layers  int    `json:"layers,omitempty"`
	Notes   string `json:"notes,omitempty"`
}

// PartsRules lists preferred and banned parts (LCSC C-numbers or MPNs).
type PartsRules struct {
	Preferred []string `json:"preferred,omitempty"`
	Banned    []string `json:"banned,omitempty"`
}

// Step is one node of the design flow (design-flow.md).
type Step struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Domain string `json:"domain"` // schematic | pcb | report
	// Sim, when set, is the simulation toggle this step runs.
	Sim string `json:"sim,omitempty"`
}

// Steps is the canonical ordered design flow (S0–S6.5, P0–P11).
var Steps = []Step{
	{"S0", "需求与来源", "schematic", ""},
	{"S1", "原始快照与纸张", "schematic", ""},
	{"S2", "目标连接数据", "schematic", ""},
	{"S3", "参数化几何", "schematic", ""},
	{"S4", "计划与 Apply", "schematic", ""},
	{"S5", "事实核对", "schematic", ""},
	{"S5.5", "模拟仿真验证", "schematic", "analog"},
	{"S6", "修复与保存", "schematic", ""},
	{"S6.5", "设计意图推导", "schematic", "power"},
	{"P0", "选择工程", "pcb", ""},
	{"P1", "导入与规则", "pcb", ""},
	{"P2", "板框策略", "pcb", ""},
	{"P3", "固定与机械", "pcb", ""},
	{"P4", "使用空间", "pcb", ""},
	{"P5", "关键布局", "pcb", ""},
	{"P6", "规则、预布检查与 Layout 确认", "pcb", ""},
	{"P7", "关键网布线", "pcb", ""},
	{"P8", "普通信号与铜", "pcb", ""},
	{"P9", "丝印与工艺", "pcb", ""},
	{"P10", "终检", "pcb", ""},
	{"P10.5", "设计后仿真验证", "pcb", "postLayout"},
	{"P11", "设计报告", "report", ""},
}

// SimInfo describes one simulation toggle.
type SimInfo struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Cmd   string `json:"cmd"`
}

// Sims is the simulation catalog.
var Sims = []SimInfo{
	{"power", "原理图电源仿真", "pcbpilot sim power / intent derive --sim-out"},
	{"analog", "模拟电路 SPICE 仿真", "pcbpilot sim analog"},
	{"monteCarlo", "模拟容差 Monte-Carlo", "pcbpilot sim analog（Monte-Carlo 部分）"},
	{"postLayout", "设计后铜皮仿真（IR/过孔/热）", "pcbpilot sim post-layout"},
	{"elmer", "Elmer FEM 交叉校验", "pcbpilot sim post-layout --elmer-dir"},
}

// Section is one design-report chapter (templates/design-report/README.md).
type Section struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Required bool   `json:"required,omitempty"`
}

// Sections is the report chapter catalog. Required chapters cannot be skipped:
// the cover verdict, summary, verification status and appendix are what make a
// report auditable.
var Sections = []Section{
	{"0", "封面", true},
	{"1", "执行摘要", true},
	{"2", "需求与设计意图", false},
	{"3", "电源仿真", false},
	{"3A", "模拟电路仿真", false},
	{"4", "器件可行性", false},
	{"5", "工程计算", false},
	{"6", "布局与布线", false},
	{"6A", "设计后仿真验证", false},
	{"7", "验证状态", true},
	{"8", "测试点计划", false},
	{"9", "制造与装配", false},
	{"10", "调试上电", false},
	{"11", "附录", true},
}

// Template is a named preset.
type Template struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	OffSteps    []string `json:"offSteps,omitempty"`
	OffSims     []string `json:"offSims,omitempty"`
	OffSections []string `json:"offSections,omitempty"`
}

// Templates are the built-in presets. Every one keeps the checks that guard a
// step that does run (S5 after S4, P10 after routing).
var Templates = []Template{
	{ID: "full", Title: "完整流程", Description: "S0–S6.5 + P0–P11 全部执行，全部仿真（Elmer 与 Monte-Carlo 除外）与报告章节。",
		OffSims: []string{"elmer", "monteCarlo"}},
	{ID: "quick-proto", Title: "快速打样", Description: "跳过模拟 SPICE、设计后铜皮仿真与 Elmer；报告不含 §3A/§6A。适合低风险数字小板。",
		OffSteps: []string{"S5.5", "P10.5"}, OffSims: []string{"analog", "monteCarlo", "postLayout", "elmer"}, OffSections: []string{"3A", "6A"}},
	{ID: "schematic-only", Title: "仅原理图", Description: "只做原理图 S0–S6.5 与预布局报告；PCB P0–P10.5 全部跳过。",
		OffSteps: []string{"P0", "P1", "P2", "P3", "P4", "P5", "P6", "P7", "P8", "P9", "P10", "P10.5"},
		OffSims:  []string{"postLayout", "elmer", "monteCarlo"}, OffSections: []string{"6", "6A", "9"}},
	{ID: "layout-from-existing", Title: "已有原理图 → PCB", Description: "原理图已给定：跳过 S2–S4 重建，仍做 S1 快照、S5 核对与全部 PCB 步骤。",
		OffSteps: []string{"S2", "S3", "S4"}, OffSims: []string{"elmer", "monteCarlo"}},
	{ID: "analog-heavy", Title: "模拟/电源重点", Description: "完整流程并打开 Monte-Carlo 与 Elmer 交叉校验。"},
}

// FindTemplate returns the preset by id.
func FindTemplate(id string) (Template, bool) {
	for _, t := range Templates {
		if t.ID == id {
			return t, true
		}
	}
	return Template{}, false
}

// New builds a config from a template.
func New(name, template string) (*Config, error) {
	if template == "" {
		template = "full"
	}
	t, ok := FindTemplate(template)
	if !ok {
		return nil, fmt.Errorf("unknown template %q (have: %s)", template, strings.Join(TemplateIDs(), ", "))
	}
	c := &Config{SchemaVersion: SchemaVersion, Name: name, Template: t.ID,
		Steps: map[string]Toggle{}, Sims: map[string]Toggle{}, Report: map[string]Toggle{}, Resources: "resources"}
	off := func(list []string, id string) bool {
		for _, v := range list {
			if v == id {
				return true
			}
		}
		return false
	}
	reason := "模板 " + t.ID
	for _, s := range Steps {
		c.Steps[s.ID] = toggle(!off(t.OffSteps, s.ID), reason)
	}
	for _, s := range Sims {
		c.Sims[s.ID] = toggle(!off(t.OffSims, s.ID), reason)
	}
	for _, s := range Sections {
		c.Report[s.ID] = toggle(!off(t.OffSections, s.ID), reason)
	}
	return c, nil
}

func toggle(on bool, reason string) Toggle {
	if on {
		return Toggle{Enabled: true}
	}
	return Toggle{Enabled: false, Reason: reason}
}

// TemplateIDs lists preset ids.
func TemplateIDs() []string {
	out := make([]string, 0, len(Templates))
	for _, t := range Templates {
		out = append(out, t.ID)
	}
	return out
}

// Path returns the config path for a work dir.
func Path(dir string) string { return filepath.Join(dir, FileName) }

// ErrNotFound is returned by Load when the work dir has no config.
var ErrNotFound = errors.New("no " + FileName)

// Load reads and decodes the config of a work dir. Unknown fields are refused
// so a typo ("enable": false) cannot silently leave a step on.
func Load(dir string) (*Config, error) {
	b, err := os.ReadFile(Path(dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return Decode(b)
}

// Decode parses config JSON strictly.
func Decode(b []byte) (*Config, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", FileName, err)
	}
	if c.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("%s schemaVersion %d is newer than this pcbpilot (%d) — upgrade pcbpilot", FileName, c.SchemaVersion, SchemaVersion)
	}
	if c.SchemaVersion <= 0 {
		return nil, fmt.Errorf("%s: schemaVersion is required", FileName)
	}
	c.fill()
	return &c, nil
}

// fill adds catalog entries missing from an older file (default on) so readers
// always see the full catalog.
func (c *Config) fill() {
	if c.Steps == nil {
		c.Steps = map[string]Toggle{}
	}
	if c.Sims == nil {
		c.Sims = map[string]Toggle{}
	}
	if c.Report == nil {
		c.Report = map[string]Toggle{}
	}
	for _, s := range Steps {
		if _, ok := c.Steps[s.ID]; !ok {
			c.Steps[s.ID] = Toggle{Enabled: true}
		}
	}
	for _, s := range Sims {
		if _, ok := c.Sims[s.ID]; !ok {
			c.Sims[s.ID] = Toggle{Enabled: s.ID != "elmer" && s.ID != "monteCarlo"}
		}
	}
	for _, s := range Sections {
		if _, ok := c.Report[s.ID]; !ok {
			c.Report[s.ID] = Toggle{Enabled: true}
		}
	}
	if c.Resources == "" {
		c.Resources = "resources"
	}
}

// Issue is one validation finding.
type Issue struct {
	Severity string `json:"severity"` // error | warn
	Field    string `json:"field"`
	Message  string `json:"message"`
}

// Validate checks ids, required chapters and the guard rules. It returns all
// issues; the config is usable when none has severity "error".
func (c *Config) Validate() []Issue {
	var out []Issue
	add := func(sev, field, msg string) { out = append(out, Issue{sev, field, msg}) }
	stepSet := map[string]Step{}
	for _, s := range Steps {
		stepSet[s.ID] = s
	}
	for id := range c.Steps {
		if _, ok := stepSet[id]; !ok {
			add("error", "steps."+id, "unknown step id (see `pcbpilot project-config show --catalog`)")
		}
	}
	simSet := map[string]bool{}
	for _, s := range Sims {
		simSet[s.ID] = true
	}
	for id := range c.Sims {
		if !simSet[id] {
			add("error", "sims."+id, "unknown simulation id")
		}
	}
	secSet := map[string]Section{}
	for _, s := range Sections {
		secSet[s.ID] = s
	}
	for id, t := range c.Report {
		s, ok := secSet[id]
		if !ok {
			add("error", "reportSections."+id, "unknown report section id")
			continue
		}
		if s.Required && !t.Enabled {
			add("error", "reportSections."+id, fmt.Sprintf("§%s %s is required and cannot be skipped", id, s.Title))
		}
	}
	on := func(id string) bool { t, ok := c.Steps[id]; return !ok || t.Enabled }
	simOn := func(id string) bool { t, ok := c.Sims[id]; return !ok || t.Enabled }
	secOn := func(id string) bool { t, ok := c.Report[id]; return !ok || t.Enabled }
	// Guard rules: a step that writes must be followed by the step that checks it.
	if on("S4") && !on("S5") {
		add("error", "steps.S5", "S5 事实核对 cannot be skipped while S4 Apply is enabled")
	}
	if (on("P7") || on("P8")) && !on("P10") {
		add("error", "steps.P10", "P10 终检 cannot be skipped while routing (P7/P8) is enabled")
	}
	if on("P7") && !on("P6") {
		add("error", "steps.P6", "P6 Layout 确认 cannot be skipped before P7 routing")
	}
	// Step ↔ simulation consistency.
	for _, s := range Steps {
		if s.Sim != "" && on(s.ID) && !simOn(s.Sim) {
			add("error", "sims."+s.Sim, fmt.Sprintf("step %s %s is enabled but simulation %q is off", s.ID, s.Title, s.Sim))
		}
		if s.Sim != "" && !on(s.ID) && simOn(s.Sim) && s.Sim != "power" {
			add("warn", "steps."+s.ID, fmt.Sprintf("simulation %q is on but its step %s is skipped — it will not run", s.Sim, s.ID))
		}
	}
	if simOn("elmer") && !simOn("postLayout") {
		add("error", "sims.elmer", "Elmer cross-check needs postLayout simulation")
	}
	if simOn("monteCarlo") && !simOn("analog") {
		add("error", "sims.monteCarlo", "Monte-Carlo needs analog simulation")
	}
	if secOn("3A") && !simOn("analog") {
		add("warn", "reportSections.3A", "§3A is on but analog simulation is off — the report will print 未运行")
	}
	if secOn("6A") && !simOn("postLayout") {
		add("warn", "reportSections.6A", "§6A is on but post-layout simulation is off — it will be listed as missing")
	}
	for id, t := range c.Steps {
		if !t.Enabled && strings.TrimSpace(t.Reason) == "" {
			add("warn", "steps."+id, "skipped without a reason — reports must say why a step was skipped")
		}
	}
	if c.Constraints.Fab.Layers < 0 || c.Constraints.Fab.Layers > 64 {
		add("error", "constraints.fab.layers", "layer count out of range")
	}
	seen := map[string]bool{}
	for _, p := range c.Constraints.Parts.Preferred {
		seen[strings.ToUpper(p)] = true
	}
	for _, p := range c.Constraints.Parts.Banned {
		if seen[strings.ToUpper(p)] {
			add("error", "constraints.parts", fmt.Sprintf("%s is both preferred and banned", p))
		}
	}
	for _, r := range append(append([]string{}, c.Constraints.Requirements...), c.Constraints.Mech) {
		if r == "" {
			continue
		}
		if filepath.IsAbs(r) || strings.HasPrefix(filepath.Clean(r), "..") {
			add("warn", "constraints", fmt.Sprintf("%s should be a path relative to the work dir", r))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == "error"
		}
		return out[i].Field < out[j].Field
	})
	return out
}

// HasErrors reports whether any issue is an error.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}

// Save validates and writes the config atomically (tmp + rename).
func Save(dir string, c *Config, by string) error {
	c.fill()
	if c.SchemaVersion == 0 {
		c.SchemaVersion = SchemaVersion
	}
	if issues := c.Validate(); HasErrors(issues) {
		var msgs []string
		for _, i := range issues {
			if i.Severity == "error" {
				msgs = append(msgs, i.Field+": "+i.Message)
			}
		}
		return fmt.Errorf("invalid %s: %s", FileName, strings.Join(msgs, "; "))
	}
	c.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	c.UpdatedBy = by
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	tmp, err := os.CreateTemp(dir, ".pcbpilot.project.*.tmp")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), Path(dir))
}

// StepEnabled reports whether a step runs (unknown ids default to on).
func (c *Config) StepEnabled(id string) bool {
	if c == nil {
		return true
	}
	t, ok := c.Steps[id]
	if !ok {
		return true
	}
	if !t.Enabled {
		return false
	}
	for _, s := range Steps {
		if s.ID == id && s.Sim != "" {
			if st, ok := c.Sims[s.Sim]; ok && !st.Enabled {
				return false
			}
		}
	}
	return true
}

// Skip is one step/section skipped by the config, with its reason.
type Skip struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Reason string `json:"reason"`
}

// SkippedSteps lists disabled steps in flow order.
func (c *Config) SkippedSteps() []Skip {
	var out []Skip
	for _, s := range Steps {
		if !c.StepEnabled(s.ID) {
			r := c.Steps[s.ID].Reason
			if r == "" && s.Sim != "" && !c.Sims[s.Sim].Enabled {
				r = c.Sims[s.Sim].Reason
			}
			out = append(out, Skip{ID: s.ID, Title: s.Title, Reason: orDefault(r, "项目配置跳过")})
		}
	}
	return out
}

// SkippedSections lists report sections the config turns off.
func (c *Config) SkippedSections() []Skip {
	var out []Skip
	for _, s := range Sections {
		if t, ok := c.Report[s.ID]; ok && !t.Enabled && !s.Required {
			out = append(out, Skip{ID: s.ID, Title: s.Title, Reason: orDefault(t.Reason, "项目配置跳过")})
		}
	}
	return out
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

// LookupStep returns the catalog entry for a step id.
func LookupStep(id string) (Step, bool) {
	for _, s := range Steps {
		if strings.EqualFold(s.ID, id) {
			return s, true
		}
	}
	return Step{}, false
}
