package kicad

// schreview.go — the design-review audit of `kicad sch-check --review` and
// the sch-review gate of `kicad sch-build` / `sch-edit`: schematic design
// mistakes proven from pcbpilot's own evidence — the KiCad netlist (net
// identities, pkg/netname), the library pin electrical types it carries,
// kicad-cli ERC, the sheet files, and, when given, intent.json and the DC
// power simulation.
//
// Every check reports pass | fail | cannot-check | not-applicable with the
// evidence it needs and the evidence it used. A finding carries a severity
// (error | warning | info) and a certainty (proven | likely | heuristic):
// only a proven finding is an error; what a check cannot decide from its
// evidence is listed as unverified and makes that check cannot-check, never
// a pass. An exemption needs an explicit role (intent review notes, a
// component field, the part kind) and names its source; a low current
// alone never exempts anything.
//
// Virtual symbols (#PWR, #FLG) are neither sources nor loads. Connector
// pins are the off-board boundary: what the board cannot see past them is
// info, not an error. 0 Ω resistors, net ties and bridged solder jumpers
// join nets for tracing; an open/unknown jumper makes the path ambiguous.

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/pkg/netname"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// SchReviewSchema is the report schema of the review.
const SchReviewSchema = "pcbpilot.kicad.sch-review/1"

// Review check ids (each with its version in reviewCheckVersions).
const (
	RvI2CPullup      = "i2c-pullup"
	RvOpenDrain      = "open-drain-pullup"
	RvPullupVoltage  = "pullup-voltage"
	RvLEDLimiter     = "led-limiter"
	RvFloatingInput  = "floating-input"
	RvOutputConflict = "output-conflict"
	RvICPowerGround  = "ic-power-ground"
	RvDecoupling     = "decoupling"
	RvRegOutputCap   = "regulator-output-cap"
	RvRegInputCap    = "regulator-input-cap"
	RvBulkCap        = "bulk-cap"
	RvMergedNets     = "merged-nets"
	RvSinglePinNet   = "single-pin-net"
	RvHierMismatch   = "hier-mismatch"
)

var reviewCheckVersions = map[string]int{
	RvI2CPullup: 1, RvOpenDrain: 1, RvPullupVoltage: 1, RvLEDLimiter: 1, RvFloatingInput: 1, RvOutputConflict: 1,
	RvICPowerGround: 1, RvDecoupling: 1, RvRegOutputCap: 1, RvRegInputCap: 1, RvBulkCap: 1,
	RvMergedNets: 1, RvSinglePinNet: 1, RvHierMismatch: 1,
}

var reviewCheckGroup = map[string]string{
	RvI2CPullup: "connections", RvOpenDrain: "connections", RvPullupVoltage: "connections", RvLEDLimiter: "connections",
	RvFloatingInput: "connections", RvOutputConflict: "connections",
	RvICPowerGround: "power", RvDecoupling: "power", RvRegOutputCap: "power", RvRegInputCap: "power", RvBulkCap: "power",
	RvMergedNets: "net-integrity", RvSinglePinNet: "net-integrity", RvHierMismatch: "net-integrity",
}

// ReviewNotCovered lists what the review does not check (documented scope).
var ReviewNotCovered = []string{
	"crystal load capacitors", "boot/strap pin default states", "feedback-divider values",
	"capacitance/ESR adequacy beyond the 1 µF bulk floor", "power sequencing / back-powering", "transient protection",
	"physical decoupling distance (EG-06 / PCB)",
}

// BulkMinF is the total capacitance to ground a loaded rail needs before
// the bulk check is satisfied (a floor, not an adequacy calculation).
const BulkMinF = 1e-6

// ReviewFinding is one design-review finding.
type ReviewFinding struct {
	Rule string `json:"rule"`
	// Fingerprint is the stable identity a waiver matches: rule + sorted
	// refs, pins and net identities — never message text or coordinates.
	Fingerprint string   `json:"fingerprint"`
	Severity    string   `json:"severity"`  // error | warning | info
	Certainty   string   `json:"certainty"` // proven | likely | heuristic
	Message     string   `json:"message"`
	Refs        []string `json:"refs,omitempty"`
	Pins        []string `json:"pins,omitempty"` // REF.PIN
	Nets        []string `json:"nets,omitempty"` // display names
	Sheet       string   `json:"sheet,omitempty"`
	Fix         string   `json:"fix"`
	Evidence    []string `json:"evidence"`
	Waived      string   `json:"waived,omitempty"` // "reason (by)" of the waiver that accepted it
	netIDs      []string
}

// ReviewExemption is a candidate a declared role exempted.
type ReviewExemption struct {
	Subject string `json:"subject"`
	Reason  string `json:"reason"`
	Source  string `json:"source"` // intent review.<field> | component field | part kind | …
}

// ReviewCoverage counts what a check looked at.
type ReviewCoverage struct {
	Candidates int `json:"candidates"`
	Checked    int `json:"checked"`
	Findings   int `json:"findings"`
	Exempt     int `json:"exempt"`
	Unverified int `json:"unverified"`
}

// ReviewCheck is one check's verdict.
type ReviewCheck struct {
	ID         string            `json:"id"`
	Version    int               `json:"version"`
	Group      string            `json:"group"`
	Status     string            `json:"status"` // pass | fail | cannot-check | not-applicable
	Detail     string            `json:"detail"`
	Required   []string          `json:"evidenceRequired"`
	Used       []string          `json:"evidenceUsed"`
	Coverage   ReviewCoverage    `json:"coverage"`
	Findings   []ReviewFinding   `json:"findings,omitempty"`
	Exempt     []ReviewExemption `json:"exempt,omitempty"`
	Unverified []string          `json:"unverified,omitempty"`
}

// ReviewInputFile is one input and its hash.
type ReviewInputFile struct {
	Kind   string `json:"kind"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

// SchReview is the review report.
type SchReview struct {
	Schema string `json:"schema"`
	// Status is the gate verdict: fail (an unwaived error), cannot-check
	// (no error, some check undecided), pass.
	Status gate.Status `json:"status"`
	// Verdict tells a clean pass from one that needed waivers.
	Verdict    string            `json:"verdict"` // pass | pass-with-waivers | fail | cannot-check
	Errors     int               `json:"errors"`
	Warnings   int               `json:"warnings"`
	Infos      int               `json:"infos"`
	Waived     int               `json:"waived"`
	Undecided  []string          `json:"cannotCheck,omitempty"`
	Inputs     []ReviewInputFile `json:"inputs,omitempty"`
	RuntimeMs  float64           `json:"runtimeMs"`
	NotCovered []string          `json:"notCovered"`
	// Notes are input problems that did not stop the review (a declared
	// name that matches no net or part).
	Notes  []string      `json:"notes,omitempty"`
	Checks []ReviewCheck `json:"checks"`
}

// ReviewHints is the optional evidence beyond the netlist (intent.json,
// sim power, component fields are read from the netlist itself). Nets are
// keyed by identity (SchNet.Name); pins are "REF.PIN" with PIN a number.
type ReviewHints struct {
	Intent, Sim string // sources ("" = not given)
	// NetRole is the intent role of a net (power|ground|signal|…).
	NetRole map[string]string
	// NetVoltage is a net's nominal voltage and where it comes from.
	NetVoltage    map[string]float64
	VoltageSource map[string]string
	// Regulators: ref → why it is one (intent block function).
	Regulators map[string]string
	// Pin roles (internal pull-up / pull-down, tolerant of a higher
	// pull-up rail): "REF.PIN" or "REF.PINNAME" (upper-case name) → source.
	PullUp, PullDown, Tolerant map[string]string
	// Net-level roles by identity → source.
	PullUpNet, ReferenceRail, SinglePinNet map[string]string
	// Part-level roles by ref → source.
	TestPoint, Module, Capless map[string]string
	// Aliases are groups of net names intentionally carried as one net.
	Aliases [][]string
}

// ReviewInput is what Review checks.
type ReviewInput struct {
	Netlist *SchNetlist
	ERC     *ERCReport // nil: not run
	// ERCIgnored lists ERC rule types the project sets to "ignore".
	ERCIgnored map[string]bool
	// Hier are the sheet-pin / hierarchical-label mismatches of the sheet
	// files (SheetHierarchyMismatches); HierErr: the files could not be read.
	Hier       []HierMismatch
	HierSheets int // sheet instances compared
	HierErr    error
	HierRan    bool
	Hints      ReviewHints
}

// ── netlist view ────────────────────────────────────────────────────────────

type rvPin struct {
	Ref, Num, Name, Type string
	NC                   bool
	Net                  string // identity
}

type rvComp struct {
	c    *SchNetComp
	kind string
	pins []*rvPin
}

type rvView struct {
	in                *ReviewInput
	comps             map[string]*rvComp
	refs              []string
	nets              map[string][]*rvPin // identity → real pins
	netIDs            []string
	display           map[string]string
	parent            map[string]string   // union-find over identities (0 Ω, net ties, bridged jumpers)
	members           map[string][]string // union-find root → its identities (after the joins)
	jumpers           map[string][]string
	sheetOf           map[string]string // ref → sheet path
	hasIntent, hasSim bool
}

// part kinds
const (
	pkResistor  = "resistor"
	pkResArray  = "resistor-array"
	pkCap       = "capacitor"
	pkLED       = "led"
	pkConnector = "connector"
	pkTestPoint = "test-point"
	pkHole      = "mounting-hole"
	pkNetTie    = "net-tie"
	pkJumper    = "jumper"
	pkSwitch    = "switch"
	pkInductor  = "inductor"
	pkDiode     = "diode"
	pkIC        = "ic"
	pkOther     = "other"
)

var rvPrefixRe = regexp.MustCompile(`^[A-Za-z_]+`)

func rvPrefix(ref string) string { return strings.ToUpper(rvPrefixRe.FindString(ref)) }

// rvKind classifies a component from its reference prefix and library
// part (KiCad's stock naming); the classes only decide which checks look
// at a part, never a verdict by themselves.
func rvKind(c *SchNetComp, npins int) string {
	pre := rvPrefix(c.Ref)
	lib := strings.ToLower(c.Lib + ":" + c.Part)
	val := strings.ToLower(c.Value)
	has := func(s string) bool { return strings.Contains(lib, s) }
	switch {
	case has("nettie") || has("net_tie") || pre == "NT":
		return pkNetTie
	case has("testpoint") || pre == "TP":
		return pkTestPoint
	case has("mountinghole") || pre == "MH" || pre == "H" && npins <= 1:
		return pkHole
	case has("jumper") || pre == "JP" || pre == "SJ":
		return pkJumper
	case has("r_pack") || has("r_array") || has("r_network") || pre == "RN" || pre == "RA":
		return pkResArray
	case pre == "R" && npins == 2:
		return pkResistor
	case pre == "C" && npins == 2:
		return pkCap
	case (pre == "D" || pre == "LED") && (has("led") || strings.Contains(val, "led")):
		return pkLED
	case has("connector") || pre == "J" || pre == "P" || pre == "CN" || pre == "CON" || pre == "USB":
		return pkConnector
	case has(":sw_") || pre == "SW" || pre == "S" || pre == "K":
		return pkSwitch
	case pre == "L" || pre == "FB" || pre == "FL":
		return pkInductor
	case pre == "D":
		return pkDiode
	case pre == "U" || pre == "IC":
		return pkIC
	}
	for _, p := range c.Pins {
		if rvBaseType(p.Type) == "power_in" {
			return pkIC
		}
	}
	return pkOther
}

func rvBaseType(t string) string {
	t, _, _ = strings.Cut(t, "+")
	return t
}

func newRvView(in *ReviewInput) *rvView {
	v := &rvView{in: in, comps: map[string]*rvComp{}, nets: map[string][]*rvPin{}, parent: map[string]string{},
		jumpers: map[string][]string{}, sheetOf: map[string]string{}}
	nl := in.Netlist
	for i := range nl.Components {
		c := &nl.Components[i]
		if strings.HasPrefix(c.Ref, "#") {
			continue // virtual: power symbols, PWR_FLAG
		}
		v.comps[c.Ref] = &rvComp{c: c}
		v.refs = append(v.refs, c.Ref)
		v.sheetOf[c.Ref] = c.Sheet
	}
	sort.Strings(v.refs)
	libName := map[string]string{}
	for _, c := range v.comps {
		for _, p := range c.c.Pins {
			libName[c.c.Ref+"."+p.Number] = p.Name
		}
	}
	var ids []string
	for _, n := range nl.Nets {
		ids = append(ids, n.Name)
		seen := map[string]bool{}
		for _, d := range n.Nodes {
			c := v.comps[d.Ref]
			if c == nil || seen[d.Ref+"."+d.Pin] {
				continue
			}
			seen[d.Ref+"."+d.Pin] = true
			// the library pin name: kicad-cli 10 writes pinfunction as
			// NAME_NUMBER for some symbols
			name := libName[d.Ref+"."+d.Pin]
			if name == "" {
				name = d.Function
			}
			p := &rvPin{Ref: d.Ref, Num: d.Pin, Name: name, Type: rvBaseType(d.Type), NC: strings.Contains(d.Type, "no_connect"), Net: n.Name}
			c.pins = append(c.pins, p)
			v.nets[n.Name] = append(v.nets[n.Name], p)
		}
	}
	v.display = netname.Display(ids)
	for id := range v.nets {
		v.netIDs = append(v.netIDs, id)
	}
	sort.Strings(v.netIDs)
	for _, r := range v.refs {
		c := v.comps[r]
		c.kind = rvKind(c.c, len(c.pins))
		sort.Slice(c.pins, func(i, j int) bool { return rvPinLess(c.pins[i].Num, c.pins[j].Num) })
	}
	v.hasIntent, v.hasSim = in.Hints.Intent != "", in.Hints.Sim != ""
	// joins: 0 Ω resistors, net ties, bridged solder jumpers
	for _, r := range v.refs {
		c := v.comps[r]
		nets := c.netSet()
		if len(nets) < 2 {
			continue
		}
		join := false
		switch c.kind {
		case pkNetTie:
			join = true
		case pkResistor:
			val, ok := powersim.ParseValue(c.c.Value)
			join = ok && val == 0
		case pkJumper:
			part := strings.ToLower(c.c.Part)
			switch {
			case strings.Contains(part, "bridged") || strings.Contains(part, "closed"):
				join = true
			default:
				for _, n := range nets {
					v.jumpers[n] = append(v.jumpers[n], r)
				}
			}
		}
		if join {
			for _, n := range nets[1:] {
				v.union(nets[0], n)
			}
		}
	}
	v.members = map[string][]string{}
	for _, id := range v.netIDs {
		r := v.find(id)
		v.members[r] = append(v.members[r], id)
	}
	return v
}

func rvPinLess(a, b string) bool {
	var x, y int
	if _, err := fmt.Sscanf(a, "%d", &x); err == nil {
		if _, err := fmt.Sscanf(b, "%d", &y); err == nil && x != y {
			return x < y
		}
	}
	return a < b
}

func (c *rvComp) netSet() []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range c.pins {
		if !seen[p.Net] && !netname.IsUnconnected(p.Net) {
			seen[p.Net] = true
			out = append(out, p.Net)
		}
	}
	sort.Strings(out)
	return out
}

func (v *rvView) find(n string) string {
	for {
		p, ok := v.parent[n]
		if !ok || p == n {
			return n
		}
		n = p
	}
}

func (v *rvView) union(a, b string) {
	a, b = v.find(a), v.find(b)
	if a != b {
		if b < a {
			a, b = b, a
		}
		v.parent[b] = a
	}
}

// group is every net joined to n (0 Ω, net ties, bridged jumpers).
func (v *rvView) group(n string) []string {
	if g, ok := v.members[v.find(n)]; ok {
		return g
	}
	return []string{n}
}

func (v *rvView) groupPins(n string) []*rvPin {
	var out []*rvPin
	for _, id := range v.group(n) {
		out = append(out, v.nets[id]...)
	}
	return out
}

func (v *rvView) name(id string) string {
	if d := v.display[id]; d != "" {
		return d
	}
	return id
}

func (v *rvView) isGround(id string) bool {
	if r := v.in.Hints.NetRole[id]; r == "ground" {
		return true
	}
	return IsGroundNet(v.name(id))
}

// isSupply: a non-ground net that intent calls power, that carries a real
// part's power pin, or that is named like a rail.
func (v *rvView) isSupply(id string) bool {
	if netname.IsUnconnected(id) {
		return false
	}
	for _, g := range v.group(id) {
		if v.isGround(g) {
			return false
		}
	}
	for _, g := range v.group(id) {
		if v.in.Hints.NetRole[g] == "power" || isPowerName(v.name(g)) {
			return true
		}
		for _, p := range v.nets[g] {
			if p.Type == "power_out" || p.Type == "power_in" && v.comps[p.Ref].kind != pkConnector {
				return true
			}
		}
	}
	return false
}

func (v *rvView) groupIsGround(id string) bool {
	for _, g := range v.group(id) {
		if v.isGround(g) {
			return true
		}
	}
	return false
}

func (v *rvView) sheet(refs []string, nets []string) string {
	for _, r := range refs {
		if s := v.sheetOf[r]; s != "" {
			return s
		}
	}
	for _, n := range nets {
		if s := netname.SheetPath(n); s != "" {
			return s
		}
	}
	return "/"
}

// pinRole looks a pin up in a role map by number and by name.
func pinRole(m map[string]string, p *rvPin) (string, bool) {
	if m == nil {
		return "", false
	}
	if s, ok := m[p.Ref+"."+p.Num]; ok {
		return s, true
	}
	if p.Name != "" {
		if s, ok := m[p.Ref+"."+strings.ToUpper(p.Name)]; ok {
			return s, true
		}
	}
	return "", false
}

// fieldRole reads a review role from a component field: ReviewRole
// (module | capless | test-point | reference) or InternalPullup /
// InternalPulldown (comma-separated pin numbers or names).
func (c *rvComp) fieldRole(role string) bool {
	for k, val := range c.c.Fields {
		if strings.EqualFold(k, "ReviewRole") {
			for _, t := range strings.FieldsFunc(strings.ToLower(val), func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
				if t == role {
					return true
				}
			}
		}
	}
	return false
}

func (c *rvComp) fieldPins(field string) map[string]bool {
	out := map[string]bool{}
	for k, val := range c.c.Fields {
		if strings.EqualFold(strings.ReplaceAll(k, " ", ""), field) {
			for _, t := range strings.FieldsFunc(val, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
				out[strings.ToUpper(t)] = true
			}
		}
	}
	return out
}

// internalPull reports a declared internal pull-up ("up") / pull-down
// ("down") on pin p, and its source.
func (v *rvView) internalPull(p *rvPin, dir string) (string, bool) {
	m, field := v.in.Hints.PullUp, "InternalPullup"
	if dir == "down" {
		m, field = v.in.Hints.PullDown, "InternalPulldown"
	}
	if s, ok := pinRole(m, p); ok {
		return s, true
	}
	if fp := v.comps[p.Ref].fieldPins(field); fp[strings.ToUpper(p.Num)] || p.Name != "" && fp[strings.ToUpper(p.Name)] {
		return "component field " + field + " of " + p.Ref, true
	}
	return "", false
}

// ── result builder ──────────────────────────────────────────────────────────

type rvCheck struct {
	*ReviewCheck
	v *rvView
}

func (v *rvView) check(id string, required ...string) *rvCheck {
	c := &ReviewCheck{ID: id, Version: reviewCheckVersions[id], Group: reviewCheckGroup[id], Required: required}
	c.Used = append(c.Used, required...)
	return &rvCheck{ReviewCheck: c, v: v}
}

func (c *rvCheck) use(ev string) {
	for _, u := range c.Used {
		if u == ev {
			return
		}
	}
	c.Used = append(c.Used, ev)
}

func (c *rvCheck) add(f ReviewFinding) {
	f.Rule = c.ID
	sort.Strings(f.Refs)
	sort.Strings(f.Pins)
	f.Refs, f.Pins = rvUniq(f.Refs), rvUniq(f.Pins)
	ids := rvUniq(append([]string(nil), f.netIDs...))
	sort.Strings(ids)
	f.netIDs = ids
	f.Nets = nil
	for _, id := range ids {
		f.Nets = append(f.Nets, c.v.name(id))
	}
	if f.Sheet == "" {
		f.Sheet = c.v.sheet(f.Refs, ids)
	}
	if rvNetKeyed[c.ID] {
		f.Fingerprint = ReviewFingerprint(c.ID, nil, nil, ids)
	} else {
		f.Fingerprint = ReviewFingerprint(c.ID, f.Refs, f.Pins, ids)
	}
	if f.Certainty == "" {
		f.Certainty = "proven"
	}
	if f.Severity == "error" && f.Certainty != "proven" {
		f.Severity = "warning" // only a proven finding is an error
	}
	c.Findings = append(c.Findings, f)
}

func (c *rvCheck) exempt(subject, reason, source string) {
	c.Exempt = append(c.Exempt, ReviewExemption{Subject: subject, Reason: reason, Source: source})
}

func (c *rvCheck) unverified(s string) { c.Unverified = append(c.Unverified, s) }

func rvUniq(xs []string) []string {
	var out []string
	for i, x := range xs {
		if x != "" && (i == 0 || x != xs[i-1]) {
			out = append(out, x)
		}
	}
	return out
}

// rvNetKeyed rules are about a net, whatever parts sit on it: their
// fingerprint is the rule and the net identities only, so adding a device
// to a waived bus keeps the waiver.
var rvNetKeyed = map[string]bool{RvI2CPullup: true, RvOpenDrain: true, RvFloatingInput: true, RvBulkCap: true, RvSinglePinNet: true}

// ReviewFingerprint is the stable identity of a finding: rule, refs, pins
// and net identities, each sorted.
func ReviewFingerprint(rule string, refs, pins, nets []string) string {
	s := func(xs []string) string {
		ys := append([]string(nil), xs...)
		sort.Strings(ys)
		return strings.Join(ys, ",")
	}
	return rule + "|" + s(refs) + "|" + s(pins) + "|" + s(nets)
}

// ── review ──────────────────────────────────────────────────────────────────

// Review runs every check. A nil netlist is a tool error, not a verdict.
func Review(in ReviewInput) (*SchReview, error) {
	if in.Netlist == nil {
		return nil, fmt.Errorf("review: no KiCad netlist")
	}
	v := newRvView(&in)
	var checks []*rvCheck
	for _, f := range []func() *rvCheck{
		v.checkI2C, v.checkOpenDrain, v.checkPullupVoltage, v.checkLEDs, v.checkFloating, v.checkOutputs,
		v.checkICPower, v.checkDecoupling, v.checkRegOutput, v.checkRegInput, v.checkBulk,
		v.checkMerged, v.checkSinglePin, v.checkHier,
	} {
		checks = append(checks, f())
	}
	r := &SchReview{Schema: SchReviewSchema, NotCovered: ReviewNotCovered}
	for _, c := range checks {
		c.Coverage.Findings = len(c.Findings)
		c.Coverage.Exempt = len(c.Exempt)
		c.Coverage.Unverified = len(c.Unverified)
		sort.SliceStable(c.Findings, func(i, j int) bool { return c.Findings[i].Fingerprint < c.Findings[j].Fingerprint })
		r.Checks = append(r.Checks, *c.ReviewCheck)
	}
	r.Judge(nil)
	return r, nil
}

// Waiver accepts one finding by fingerprint: reason and by are recorded.
type Waiver struct{ Fingerprint, Reason, By string }

// Judge applies waivers (exact fingerprint match; errors and warnings
// only) and sets every status and count. A waiver never decides an
// unverified item: cannot-check stays cannot-check.
func (r *SchReview) Judge(waive func(fingerprint string) (string, bool)) {
	r.Errors, r.Warnings, r.Infos, r.Waived = 0, 0, 0, 0
	r.Undecided = nil
	var res []gate.Result
	for i := range r.Checks {
		c := &r.Checks[i]
		errs, warns := 0, 0
		for j := range c.Findings {
			f := &c.Findings[j]
			if waive != nil && f.Severity != "info" {
				if note, ok := waive(f.Fingerprint); ok {
					f.Waived = note
				}
			}
			switch {
			case f.Waived != "":
				r.Waived++
			case f.Severity == "error":
				errs++
				r.Errors++
			case f.Severity == "warning":
				warns++
				r.Warnings++
			default:
				r.Infos++
			}
		}
		switch {
		case errs > 0:
			c.Status = "fail"
		case len(c.Unverified) > 0:
			c.Status = "cannot-check"
			r.Undecided = append(r.Undecided, c.ID)
		case c.Coverage.Candidates == 0 && len(c.Findings) == 0:
			c.Status = "not-applicable"
		default:
			c.Status = "pass"
		}
		c.Detail = fmt.Sprintf("%d candidate(s): %d error(s), %d warning(s), %d exempt, %d unverified", c.Coverage.Candidates, errs, warns, len(c.Exempt), len(c.Unverified))
		st := gate.Pass
		switch c.Status {
		case "fail":
			st = gate.Fail
		case "cannot-check":
			st = gate.CannotCheck
		}
		res = append(res, gate.Result{Name: c.ID, Status: st})
	}
	r.Status = gate.Aggregate(res...)
	r.Verdict = string(r.Status)
	if r.Status == gate.Pass && r.Waived > 0 {
		r.Verdict = "pass-with-waivers"
	}
}

// Summary is one line for stderr / a gate detail.
func (r *SchReview) Summary() string {
	s := fmt.Sprintf("sch-review %s: %d error(s), %d warning(s), %d info, %d waived", r.Verdict, r.Errors, r.Warnings, r.Infos, r.Waived)
	if len(r.Undecided) > 0 {
		s += "; cannot-check: " + strings.Join(r.Undecided, ", ")
	}
	return s
}

// Items lists the unwaived errors and warnings as "fingerprint: message".
func (r *SchReview) Items() []string {
	var out []string
	for _, c := range r.Checks {
		for _, f := range c.Findings {
			if f.Waived == "" && f.Severity != "info" {
				out = append(out, fmt.Sprintf("%s %s: %s", f.Severity, f.Fingerprint, f.Message))
			}
		}
	}
	return out
}

// ── pull-up tracing ─────────────────────────────────────────────────────────

// rvPull is how a net is pulled to a rail.
type rvPull struct {
	Res, Rail string // resistor ref, rail identity
	How       string
}

// pulls lists the resistors from net n's group to a supply (up) or ground
// (down) group. Resistor arrays use KiCad's stock pairing (isolated pack:
// pin i ↔ pin N+1−i; bussed network: every pin ↔ pin 1). ambiguous: an
// open/unknown jumper sits on the path.
func (v *rvView) pulls(n string, dir string) (out []rvPull, ambiguous []string) {
	if v.isSupply(n) || v.groupIsGround(n) {
		return nil, nil // a resistor between rails is not a pull-up
	}
	grp := map[string]bool{}
	for _, g := range v.group(n) {
		grp[g] = true
		ambiguous = append(ambiguous, v.jumpers[g]...)
	}
	target := func(net string) bool {
		if grp[v.find(net)] || grp[net] || netname.IsUnconnected(net) {
			return false
		}
		if dir == "down" {
			return v.groupIsGround(net)
		}
		return v.isSupply(net)
	}
	for _, p := range v.groupPins(n) {
		c := v.comps[p.Ref]
		switch c.kind {
		case pkResistor:
			if val, ok := powersim.ParseValue(c.c.Value); ok && val == 0 {
				continue
			}
			for _, q := range c.pins {
				if q != p && target(q.Net) {
					out = append(out, rvPull{Res: p.Ref, Rail: q.Net, How: fmt.Sprintf("%s (%s) to %s", p.Ref, c.c.Value, v.name(q.Net))})
				}
			}
		case pkResArray:
			mate := rvArrayMate(c, p)
			for _, q := range c.pins {
				if mate[q.Num] && target(q.Net) {
					out = append(out, rvPull{Res: p.Ref, Rail: q.Net, How: fmt.Sprintf("%s pin %s–%s (resistor array, KiCad stock pairing) to %s", p.Ref, p.Num, q.Num, v.name(q.Net))})
				}
			}
		}
	}
	return out, ambiguous
}

func rvArrayMate(c *rvComp, p *rvPin) map[string]bool {
	n := len(c.c.Pins)
	if n == 0 {
		n = len(c.pins)
	}
	out := map[string]bool{}
	var i int
	if _, err := fmt.Sscanf(p.Num, "%d", &i); err != nil {
		return out
	}
	if strings.Contains(strings.ToLower(c.c.Part), "network") {
		if i == 1 {
			for _, q := range c.pins {
				if q.Num != "1" {
					out[q.Num] = true
				}
			}
		} else {
			out["1"] = true
		}
		return out
	}
	out[fmt.Sprint(n+1-i)] = true
	return out
}

func (v *rvView) offBoard(n string) []string {
	var out []string
	for _, p := range v.groupPins(n) {
		if v.comps[p.Ref].kind == pkConnector {
			out = append(out, p.Ref+"."+p.Num)
		}
	}
	sort.Strings(out)
	return rvUniq(out)
}

// ── connections ─────────────────────────────────────────────────────────────

var rvI2CRe = regexp.MustCompile(`(?i)(^|[^A-Z])(SDA|SCL)\d*($|[^A-Z])`)

// checkI2C: every I2C SDA/SCL net needs a resistor to a supply on the
// board, or reaches a connector (pull-up off-board: info). An internal
// pull-up is too weak for I2C rise times, so the check needs no intent;
// a declared one exempts the net and is named.
func (v *rvView) checkI2C() *rvCheck {
	c := v.check(RvI2CPullup, "netlist", "pin-names")
	done := map[string]bool{}
	for _, id := range v.netIDs {
		root := v.find(id)
		if done[root] || netname.IsUnconnected(id) {
			continue
		}
		// a pin whose primary function is SDA/SCL proves the bus; one that
		// only lists it among alternate functions ("RB4/SDA") suggests it
		var byPin, byAlt []string
		var refs []string
		for _, p := range v.groupPins(id) {
			if k := v.comps[p.Ref].kind; k == pkConnector || k == pkResistor || k == pkResArray || !rvI2CRe.MatchString(p.Name) {
				continue
			}
			multi := strings.Contains(p.Name, "/")
			if multi && !strings.HasPrefix(id, "Net-(") && !rvI2CRe.MatchString(v.name(id)) {
				continue // a multi-function pin on a net named for another use (CANRX, LED1)
			}
			refs = append(refs, p.Ref)
			if !multi || rvI2CRe.MatchString(strings.Split(p.Name, "/")[0]) && rvI2CRe.MatchString(v.name(id)) {
				byPin = append(byPin, p.Ref+"."+p.Num+" ("+p.Name+")")
			} else {
				byAlt = append(byAlt, p.Ref+"."+p.Num+" ("+p.Name+")")
			}
		}
		byName := rvI2CRe.MatchString(v.name(id))
		if len(byPin) == 0 && len(byAlt) == 0 && !byName {
			continue
		}
		done[root] = true
		c.Coverage.Candidates++
		grp := v.group(id)
		how, cert := "pin name(s) "+strings.Join(byPin, ", "), "proven"
		switch {
		case len(byPin) > 0:
		case len(byAlt) > 0:
			how, cert = "alternate pin function(s) "+strings.Join(byAlt, ", ")+" (the pin may not be used as I2C)", "heuristic"
		default:
			how, cert = "net name "+v.name(id)+" only (heuristic)", "heuristic"
		}
		pulls, amb := v.pulls(id, "up")
		if len(pulls) > 0 {
			c.Coverage.Checked++
			continue
		}
		if src, ok := v.in.Hints.PullUpNet[root]; ok {
			c.exempt(v.name(id), "pull-up declared for the net", src)
			continue
		}
		declared := false
		for _, p := range v.groupPins(id) {
			if src, ok := v.internalPull(p, "up"); ok {
				c.exempt(p.Ref+"."+p.Num, "internal pull-up declared (an internal pull-up is weak for I2C: check the rise time)", src)
				declared = true
				break
			}
		}
		if declared {
			continue
		}
		if len(amb) > 0 {
			c.unverified(fmt.Sprintf("%s: the pull-up path runs through jumper(s) %s whose state is unknown", v.name(id), strings.Join(rvUniq(amb), ", ")))
			continue
		}
		c.Coverage.Checked++
		ev := []string{"I2C recognised by " + how, "no resistor from " + v.name(id) + " (or a net joined to it by 0 Ω / net tie) to a supply"}
		if off := v.offBoard(id); len(off) > 0 {
			c.add(ReviewFinding{Severity: "info", Certainty: cert, Message: fmt.Sprintf("I2C line %s has no pull-up on this board; it leaves through %s", v.name(id), strings.Join(off, ", ")),
				Refs: refs, netIDs: grp, Fix: "confirm the pull-up is on the other side of the connector, or add 2.2–10 kΩ to the bus supply",
				Evidence: append(ev, "connector pins are the off-board boundary")})
			continue
		}
		c.add(ReviewFinding{Severity: "error", Certainty: cert, Message: fmt.Sprintf("I2C line %s has no pull-up resistor to a supply", v.name(id)),
			Refs: refs, netIDs: grp, Fix: fmt.Sprintf("add a pull-up (2.2–10 kΩ, sized for bus capacitance and speed) from %s to the bus supply", v.name(id)),
			Evidence: ev})
	}
	return c
}

var rvResetIntRe = regexp.MustCompile(`(?i)(RST|RESET|INT|IRQ|ALERT|PG|PGOOD|POK|FAULT|FLT|NMI)`)

// rvDrivers splits a group's pins into push-pull drivers (output,
// power_out, tri_state, bidirectional — a bidirectional pin may be a
// push-pull output) and open-drain/collector (oc) / open-emitter pins.
func rvDrivers(pins []*rvPin) (pp, oc, oe []*rvPin) {
	for _, p := range pins {
		switch p.Type {
		case "output", "power_out", "tri_state", "bidirectional":
			pp = append(pp, p)
		case "open_collector":
			oc = append(oc, p)
		case "open_emitter":
			oe = append(oe, p)
		}
	}
	return
}

// checkOpenDrain: a net driven only by open-collector/open-drain pins
// needs a pull-up (open-emitter: a pull-down). A push-pull capable pin on
// the net drives it (not flagged, named in the evidence); a declared
// internal pull-up of any pin exempts it; without intent the internal
// pull-ups are unknown and the net is unverified.
func (v *rvView) checkOpenDrain() *rvCheck {
	c := v.check(RvOpenDrain, "netlist", "pin-types", "intent (internal pull-ups)")
	c.Used = c.Used[:2]
	done := map[string]bool{}
	for _, id := range v.netIDs {
		root := v.find(id)
		if done[root] {
			continue
		}
		pins := v.groupPins(id)
		pp, oc, oe := rvDrivers(pins)
		for _, mode := range []struct {
			dir  string
			pins []*rvPin
			what string
		}{{"up", oc, "open-collector/open-drain"}, {"down", oe, "open-emitter"}} {
			if len(mode.pins) == 0 {
				continue
			}
			done[root] = true
			c.Coverage.Candidates++
			name := v.name(id)
			var drv []string
			for _, p := range mode.pins {
				drv = append(drv, p.Ref+"."+p.Num+" ("+p.Name+")")
			}
			if len(pp) > 0 {
				var pps []string
				for _, p := range pp {
					pps = append(pps, p.Ref+"."+p.Num+" "+p.Type)
				}
				c.exempt(name, "driven by a push-pull capable pin: "+strings.Join(pps, ", "), "pin types (netlist)")
				continue
			}
			pulls, amb := v.pulls(id, mode.dir)
			if len(pulls) > 0 {
				c.Coverage.Checked++
				continue
			}
			if loads := v.kindPins(id, pkLED, pkDiode); len(loads) > 0 && mode.dir == "up" {
				c.exempt(name, "the open-drain pin sinks a load ("+strings.Join(loads, ", ")+") that returns to a supply", "part kind")
				continue
			}
			src, ok := v.in.Hints.PullUpNet[root]
			if !ok {
				for _, p := range pins {
					if s, has := v.internalPull(p, mode.dir); has {
						src, ok = p.Ref+"."+p.Num+": "+s, true
						break
					}
				}
			}
			if ok {
				c.exempt(name, "internal pull-"+mode.dir+" declared", src)
				if strings.HasPrefix(src, "intent") || strings.Contains(src, "intent") {
					c.use("intent")
				} else {
					c.use("component fields")
				}
				continue
			}
			if len(amb) > 0 {
				c.unverified(fmt.Sprintf("%s: the pull-%s path runs through jumper(s) %s whose state is unknown", name, mode.dir, strings.Join(rvUniq(amb), ", ")))
				continue
			}
			if off := v.offBoard(id); len(off) > 0 {
				c.Coverage.Checked++
				c.add(ReviewFinding{Severity: "info", Message: fmt.Sprintf("%s line %s (%s) has no pull-%s on this board; it leaves through %s", mode.what, name, strings.Join(drv, ", "), mode.dir, strings.Join(off, ", ")),
					Refs: rvRefs(mode.pins), Pins: rvPinKeys(mode.pins), netIDs: v.group(id), Fix: "confirm the pull-" + mode.dir + " is off-board, or add one",
					Evidence: []string{"pin types: " + strings.Join(drv, ", "), "connector pins are the off-board boundary"}})
				continue
			}
			if !v.hasIntent {
				c.unverified(fmt.Sprintf("%s: %s %s with no pull-%s resistor — whether a receiver has an internal pull-%s is unknown without intent (review.internalPullups) or an InternalPullup field", name, mode.what, strings.Join(drv, ", "), mode.dir, mode.dir))
				continue
			}
			c.use("intent")
			c.Coverage.Checked++
			sev := "error"
			kind := "line"
			if rvResetIntRe.MatchString(name) {
				kind = "reset/interrupt/status line"
			}
			for _, p := range mode.pins {
				if rvResetIntRe.MatchString(p.Name) {
					kind = "reset/interrupt/status line"
				}
			}
			c.add(ReviewFinding{Severity: sev, Message: fmt.Sprintf("%s %s %s is driven only by %s pin(s) %s and has no pull-%s", mode.what, kind, name, mode.what, strings.Join(drv, ", "), mode.dir),
				Refs: rvRefs(mode.pins), Pins: rvPinKeys(mode.pins), netIDs: v.group(id),
				Fix:      fmt.Sprintf("add a pull-%s resistor (typ. 4.7–10 kΩ) from %s to the %s, or declare the receiver's internal pull-%s in intent review.internalPull%ss", mode.dir, name, map[string]string{"up": "receiver's supply", "down": "ground"}[mode.dir], mode.dir, mode.dir),
				Evidence: []string{"pin types: " + strings.Join(drv, ", "), "no push-pull/bidirectional pin on the net", "no resistor to a " + map[string]string{"up": "supply", "down": "ground"}[mode.dir], "intent " + v.in.Hints.Intent + " declares no internal pull-" + mode.dir + " on the net"}})
		}
	}
	return c
}

// kindPins lists the pins of parts of the given kinds on n's group.
func (v *rvView) kindPins(n string, kinds ...string) []string {
	var out []string
	for _, p := range v.groupPins(n) {
		for _, k := range kinds {
			if v.comps[p.Ref].kind == k {
				out = append(out, p.Ref+"."+p.Num)
			}
		}
	}
	return out
}

func rvRefs(ps []*rvPin) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Ref)
	}
	return out
}

func rvPinKeys(ps []*rvPin) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.Ref+"."+p.Num)
	}
	return out
}

// supplyV is the highest known voltage of the nets on a part's power_in
// pins ("" source: none known).
func (v *rvView) supplyV(ref string) (float64, string, bool) {
	best, src, ok := 0.0, "", false
	for _, p := range v.comps[ref].pins {
		if p.Type != "power_in" || v.groupIsGround(p.Net) {
			continue
		}
		if x, has := v.voltage(p.Net); has && (!ok || x > best) {
			best, src, ok = x, v.name(p.Net), true
		}
	}
	return best, src, ok
}

func (v *rvView) voltage(n string) (float64, bool) {
	for _, g := range v.group(n) {
		if x, ok := v.in.Hints.NetVoltage[g]; ok {
			return x, true
		}
	}
	return 0, false
}

// checkPullupVoltage: a pull-up's rail must not exceed the supply domain
// of the parts it pulls (by more than 0.3 V) unless intent declares the pin
// tolerant. Needs rail voltages (intent or sim): without them cannot-check.
func (v *rvView) checkPullupVoltage() *rvCheck {
	c := v.check(RvPullupVoltage, "netlist", "pin-types", "rail voltages (intent or sim)")
	c.Used = c.Used[:2]
	done := map[string]bool{}
	for _, id := range v.netIDs {
		root := v.find(id)
		if done[root] {
			continue
		}
		pulls, _ := v.pulls(id, "up")
		if len(pulls) == 0 {
			continue
		}
		done[root] = true
		var devs []*rvPin
		for _, p := range v.groupPins(id) {
			if v.comps[p.Ref].kind == pkIC && p.Type != "passive" && p.Type != "power_in" && p.Type != "power_out" {
				devs = append(devs, p)
			}
		}
		if len(devs) == 0 {
			continue // a pull-up to a transistor / LED resistor: no supply domain to compare
		}
		for _, pu := range pulls {
			c.Coverage.Candidates++
			rv, ok := v.voltage(pu.Rail)
			if !ok {
				c.unverified(fmt.Sprintf("%s pulled up by %s: the voltage of %s is unknown (give --intent or --sim)", v.name(id), pu.How, v.name(pu.Rail)))
				continue
			}
			c.use(v.in.Hints.VoltageSource[v.find(pu.Rail)])
			for _, p := range devs {
				sv, sn, ok := v.supplyV(p.Ref)
				if !ok {
					c.unverified(fmt.Sprintf("%s.%s on %s: the supply voltage of %s is unknown", p.Ref, p.Num, v.name(id), p.Ref))
					continue
				}
				c.Coverage.Checked++
				if rv <= sv+0.3 {
					continue
				}
				if src, ok := pinRole(v.in.Hints.Tolerant, p); ok {
					c.exempt(p.Ref+"."+p.Num, fmt.Sprintf("tolerant of %.2f V declared", rv), src)
					continue
				}
				c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("%s is pulled up to %s (%.2f V) but %s.%s (%s) is powered from %s (%.2f V)", v.name(id), v.name(pu.Rail), rv, p.Ref, p.Num, p.Name, sn, sv),
					Refs: []string{p.Ref, pu.Res}, Pins: []string{p.Ref + "." + p.Num}, netIDs: []string{id, pu.Rail},
					Fix:      fmt.Sprintf("pull %s up to %s's own supply (or use a level shifter); if the pin is rated for %.2f V declare it in intent review.tolerantPins", v.name(id), p.Ref, rv),
					Evidence: []string{"pull-up " + pu.How, fmt.Sprintf("%s = %.2f V, %s = %.2f V (%s)", v.name(pu.Rail), rv, sn, sv, v.in.Hints.VoltageSource[v.find(pu.Rail)])}})
			}
		}
	}
	return c
}

var rvLEDDriverPin = regexp.MustCompile(`(?i)^(LED\w*|ISINK\w*|SINK\w*|K\d*|LEDK\w*|CC\d*|ILED\w*|OUT_?LED\w*|D\d+K)$`)

// checkLEDs traces each LED terminal to a rail, ground or a driving pin;
// the path needs a series limiter: a resistor (not 0 Ω), a current-sink
// pin (named LED/ISINK/K…), or a series LED whose own path has one. A
// transistor, switch or unknown jumper on the path is ambiguous
// (unverified), a connector is the off-board boundary (info).
func (v *rvView) checkLEDs() *rvCheck {
	c := v.check(RvLEDLimiter, "netlist", "part kinds")
	for _, ref := range v.refs {
		led := v.comps[ref]
		if led.kind != pkLED {
			continue
		}
		c.Coverage.Candidates++
		limited, why, amb, off := v.ledPath(ref, map[string]bool{})
		switch {
		case limited:
			c.Coverage.Checked++
		case amb != "":
			c.unverified(fmt.Sprintf("%s: %s", ref, amb))
		case off != "":
			c.Coverage.Checked++
			c.add(ReviewFinding{Severity: "info", Message: fmt.Sprintf("LED %s has no series limiter on this board; its path leaves through %s", ref, off),
				Refs: []string{ref}, netIDs: led.netSet(), Fix: "confirm the current is limited off-board, or add a series resistor",
				Evidence: []string{why, "connector pins are the off-board boundary"}})
		default:
			c.Coverage.Checked++
			c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("LED %s has no series resistor or current limiter (%s)", ref, why),
				Refs: []string{ref}, netIDs: led.netSet(), Fix: fmt.Sprintf("add a series resistor to %s: R = (Vsupply − Vf) / If", ref),
				Evidence: []string{why}})
		}
	}
	return c
}

// ledPath reports whether LED ref's current is limited: each terminal
// net's group is examined; a series LED is followed.
func (v *rvView) ledPath(ref string, seen map[string]bool) (limited bool, why, amb, off string) {
	seen[ref] = true
	led := v.comps[ref]
	var ends []string
	for _, n := range led.netSet() {
		g := v.group(n)
		rail := v.isSupply(n) || v.groupIsGround(n)
		var limiter, ambig, offb []string
		var seriesLED []string
		for _, gn := range g {
			for _, j := range v.jumpers[gn] {
				ambig = append(ambig, "jumper "+j)
			}
		}
		for _, p := range v.groupPins(n) {
			if p.Ref == ref {
				continue
			}
			comp := v.comps[p.Ref]
			switch comp.kind {
			case pkResistor, pkResArray:
				if val, ok := powersim.ParseValue(comp.c.Value); ok && val == 0 {
					continue
				}
				limiter = append(limiter, p.Ref)
			case pkLED:
				if !seen[p.Ref] {
					seriesLED = append(seriesLED, p.Ref)
				}
			case pkConnector:
				if !rail { // a connector on a rail is the rail's source, not where the LED current goes
					offb = append(offb, p.Ref+"."+p.Num)
				}
			case pkSwitch, pkJumper, pkOther, pkInductor, pkDiode:
				if comp.kind == pkOther && strings.HasPrefix(rvPrefix(p.Ref), "Q") || comp.kind != pkOther {
					ambig = append(ambig, p.Ref)
				}
			case pkIC:
				if rvLEDDriverPin.MatchString(p.Name) {
					limiter = append(limiter, p.Ref+"."+p.Num+" ("+p.Name+", current-sink/driver pin)")
				}
			}
		}
		if len(limiter) > 0 {
			return true, "limited by " + strings.Join(limiter, ", "), "", ""
		}
		for _, s := range seriesLED {
			if ok, w, a, o := v.ledPath(s, seen); ok {
				return true, "in series with " + s + ", " + w, "", ""
			} else if a != "" {
				ambig = append(ambig, a)
			} else if o != "" {
				offb = append(offb, o)
			}
		}
		if len(ambig) > 0 && amb == "" {
			amb = "the current path through " + v.name(n) + " runs through " + strings.Join(rvUniq(ambig), ", ") + " (a transistor/switch/jumper may or may not limit it)"
		}
		if len(offb) > 0 && off == "" {
			off = strings.Join(offb, ", ")
		}
		ends = append(ends, v.name(n))
	}
	return false, "terminals on " + strings.Join(ends, " and ") + " with no resistor (0 Ω does not count) or current-sink pin", amb, off
}

// checkFloating: a net whose pins are all inputs has no driver. A
// declared internal pull-up/down exempts it; without intent the internal
// pulls are unknown and the net is unverified. Single-pin nets are the
// single-pin check's.
func (v *rvView) checkFloating() *rvCheck {
	c := v.check(RvFloatingInput, "netlist", "pin-types", "intent (internal pulls)")
	c.Used = c.Used[:2]
	done := map[string]bool{}
	for _, id := range v.netIDs {
		root := v.find(id)
		if done[root] || netname.IsUnconnected(id) {
			continue
		}
		done[root] = true
		pins := v.groupPins(id)
		if len(pins) < 2 {
			continue
		}
		all := true
		for _, p := range pins {
			all = all && p.Type == "input" && !p.NC
		}
		if !all {
			continue
		}
		c.Coverage.Candidates++
		name := v.name(id)
		src, ok := "", false
		for _, p := range pins {
			if s, has := v.internalPull(p, "up"); has {
				src, ok = p.Ref+"."+p.Num+": "+s, true
			} else if s, has := v.internalPull(p, "down"); has {
				src, ok = p.Ref+"."+p.Num+": "+s, true
			}
		}
		if ok {
			c.exempt(name, "internal pull declared", src)
			continue
		}
		if !v.hasIntent {
			c.unverified(fmt.Sprintf("%s: only input pins (%s) — whether one has an internal pull is unknown without intent (review.internalPullups/internalPulldowns)", name, strings.Join(rvPinKeys(pins), ", ")))
			continue
		}
		c.use("intent")
		c.Coverage.Checked++
		c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("net %s connects only input pins (%s): nothing drives it", name, strings.Join(rvPinKeys(pins), ", ")),
			Refs: rvRefs(pins), Pins: rvPinKeys(pins), netIDs: v.group(id),
			Fix:      fmt.Sprintf("drive %s from an output, tie it to a rail / ground through a resistor, or declare the internal pull in intent review", name),
			Evidence: []string{"pin types: all input", "intent " + v.in.Hints.Intent + " declares no internal pull on these pins"}})
	}
	return c
}

// checkOutputs: two or more parts with push-pull outputs (output,
// power_out) on one net contend. Pins of one part count once; virtual
// symbols are not parts.
func (v *rvView) checkOutputs() *rvCheck {
	c := v.check(RvOutputConflict, "netlist", "pin-types")
	done := map[string]bool{}
	for _, id := range v.netIDs {
		root := v.find(id)
		if done[root] {
			continue
		}
		done[root] = true
		byRef := map[string][]*rvPin{}
		var refs []string
		for _, p := range v.groupPins(id) {
			if p.Type == "output" || p.Type == "power_out" {
				if _, seen := byRef[p.Ref]; !seen {
					refs = append(refs, p.Ref)
				}
				byRef[p.Ref] = append(byRef[p.Ref], p)
			}
		}
		if len(refs) == 0 {
			continue
		}
		c.Coverage.Candidates++
		c.Coverage.Checked++
		if len(refs) < 2 {
			continue
		}
		sort.Strings(refs)
		var ps []*rvPin
		var desc []string
		for _, r := range refs {
			ps = append(ps, byRef[r]...)
			for _, p := range byRef[r] {
				desc = append(desc, p.Ref+"."+p.Num+" "+p.Type)
			}
		}
		c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("net %s has %d driving outputs: %s", v.name(id), len(refs), strings.Join(desc, ", ")),
			Refs: refs, Pins: rvPinKeys(ps), netIDs: v.group(id),
			Fix:      "keep one driver per net: split the net, or make the extra drivers open-drain / tri-state, or OR them through diodes",
			Evidence: []string{"pin types: " + strings.Join(desc, ", ")}})
	}
	return c
}

// ── power ───────────────────────────────────────────────────────────────────

// icParts are the parts with typed power inputs (the loads).
func (v *rvView) icParts() []string {
	var out []string
	for _, r := range v.refs {
		c := v.comps[r]
		if c.kind == pkConnector || c.kind == pkTestPoint || c.kind == pkHole {
			continue
		}
		for _, p := range c.pins {
			if p.Type == "power_in" {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

// checkICPower: a part with typed power inputs needs one on a supply and a
// pin on ground.
func (v *rvView) checkICPower() *rvCheck {
	c := v.check(RvICPowerGround, "netlist", "pin-types")
	for _, r := range v.icParts() {
		comp := v.comps[r]
		c.Coverage.Candidates++
		c.Coverage.Checked++
		supply, ground, typed := false, false, false
		rails := map[string]bool{}
		for _, p := range comp.pins {
			if p.Type == "power_in" && !v.groupIsGround(p.Net) && !netname.IsUnconnected(p.Net) {
				rails[v.find(p.Net)] = true
			}
			if p.Type == "power_in" && !v.groupIsGround(p.Net) {
				typed = true
			}
			if v.groupIsGround(p.Net) {
				ground = true
			} else if p.Type == "power_in" && !netname.IsUnconnected(p.Net) && len(v.groupPins(p.Net)) > 1 {
				supply = true
			} else if p.Type == "power_in" && !netname.IsUnconnected(p.Net) && v.isSupply(p.Net) && isPowerName(v.name(p.Net)) {
				supply = true // a rail from a power symbol only
			}
		}
		if !supply && typed {
			c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("%s has no power input connected to a supply", r), Refs: []string{r},
				Fix: "connect " + r + "'s power pins to its rail", Evidence: []string{"pin types: no power_in pin on a non-ground net with another connection"}})
		}
		if !ground && len(rails) >= 2 {
			// dual supply (V+ / V−): the return is the other rail
			c.exempt(r, fmt.Sprintf("no ground pin, supplied between %d rails (dual supply)", len(rails)), "pin types (netlist)")
			continue
		}
		if !ground {
			if comp.fieldRole("no-ground") {
				c.exempt(r, "no ground pin by design", "component field ReviewRole")
				continue
			}
			c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("%s has no pin on a ground net", r), Refs: []string{r},
				Fix: "connect " + r + "'s ground / exposed pad to GND (or set ReviewRole=no-ground on a part that has none)", Evidence: []string{"no pin of " + r + " on a net named like ground or with intent role ground"}})
		}
	}
	return c
}

// capsTo lists the capacitors from net n's group to ground, and to other
// nets.
func (v *rvView) capsTo(n string) (toGnd []string, other []string) {
	grp := map[string]bool{}
	for _, g := range v.group(n) {
		grp[g] = true
	}
	seen := map[string]bool{}
	for _, p := range v.groupPins(n) {
		c := v.comps[p.Ref]
		if c.kind != pkCap || seen[p.Ref] {
			continue
		}
		seen[p.Ref] = true
		for _, q := range c.pins {
			if q == p || grp[q.Net] {
				continue
			}
			if v.groupIsGround(q.Net) {
				toGnd = append(toGnd, p.Ref)
			} else {
				other = append(other, p.Ref+"→"+v.name(q.Net))
			}
		}
	}
	sort.Strings(toGnd)
	sort.Strings(other)
	return toGnd, other
}

// capsToRail lists the capacitors from net n's group to another supply
// rail.
func (v *rvView) capsToRail(n string) []string {
	grp := map[string]bool{}
	for _, g := range v.group(n) {
		grp[g] = true
	}
	var out []string
	for _, p := range v.groupPins(n) {
		c := v.comps[p.Ref]
		if c.kind != pkCap {
			continue
		}
		for _, q := range c.pins {
			if q != p && !grp[q.Net] && v.isSupply(q.Net) {
				out = append(out, p.Ref+"→"+v.name(q.Net))
			}
		}
	}
	sort.Strings(out)
	return rvUniq(out)
}

// railExempt: an explicit role (intent reference rail, module with
// integrated decoupling) — never a low current.
func (v *rvView) railExempt(net, ref string) (string, string, bool) {
	for _, g := range v.group(net) {
		if s, ok := v.in.Hints.ReferenceRail[g]; ok {
			return "reference/sense rail that needs no capacitor", s, true
		}
	}
	if ref != "" {
		if s, ok := v.in.Hints.Module[ref]; ok {
			return "module with integrated decoupling", s, true
		}
		if v.comps[ref].fieldRole("module") {
			return "module with integrated decoupling", "component field ReviewRole of " + ref, true
		}
	}
	return "", "", false
}

// checkDecoupling: every power input of a part needs a capacitor from its
// rail to ground; the exact set is reported per pin. One set covering
// several parts is a warning (shared coverage) — distance is EG-06's.
func (v *rvView) checkDecoupling() *rvCheck {
	c := v.check(RvDecoupling, "netlist", "pin-types", "part kinds")
	loads := map[string][]string{} // rail group root → parts
	for _, r := range v.icParts() {
		seen := map[string]bool{}
		for _, p := range v.comps[r].pins {
			if p.Type != "power_in" || v.groupIsGround(p.Net) || netname.IsUnconnected(p.Net) || seen[v.find(p.Net)] {
				continue
			}
			seen[v.find(p.Net)] = true
			loads[v.find(p.Net)] = append(loads[v.find(p.Net)], r)
		}
	}
	for _, r := range v.icParts() {
		seen := map[string]bool{}
		for _, p := range v.comps[r].pins {
			if p.Type != "power_in" || v.groupIsGround(p.Net) || netname.IsUnconnected(p.Net) {
				continue
			}
			root := v.find(p.Net)
			if seen[root] {
				continue
			}
			seen[root] = true
			c.Coverage.Candidates++
			subj := r + " " + v.name(p.Net)
			if why, src, ok := v.railExempt(p.Net, r); ok {
				c.exempt(subj, why, src)
				if strings.Contains(src, "intent") {
					c.use("intent")
				}
				continue
			}
			c.Coverage.Checked++
			caps, other := v.capsTo(p.Net)
			var pins []string
			for _, q := range v.comps[r].pins {
				if q.Type == "power_in" && v.find(q.Net) == root {
					pins = append(pins, r+"."+q.Num)
				}
			}
			if len(caps) == 0 {
				ev := []string{fmt.Sprintf("power_in pin(s) %s on %s", strings.Join(pins, ", "), v.name(p.Net)), "no capacitor from " + v.name(p.Net) + " to ground"}
				if len(other) > 0 {
					ev = append(ev, "capacitors to other nets: "+strings.Join(other, ", "))
				}
				c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("%s supply %s (%s) has no decoupling capacitor to ground", r, v.name(p.Net), strings.Join(pins, ", ")),
					Refs: []string{r}, Pins: pins, netIDs: []string{p.Net},
					Fix:      fmt.Sprintf("add a 100 nF ceramic from %s to GND at %s", v.name(p.Net), r),
					Evidence: ev})
				continue
			}
			if n := len(loads[root]); n > len(caps) {
				c.add(ReviewFinding{Severity: "warning", Certainty: "likely", Message: fmt.Sprintf("%s supply %s shares its decoupling: %d capacitor(s) (%s) for %d parts (%s)", r, v.name(p.Net), len(caps), strings.Join(caps, ", "), n, strings.Join(loads[root], ", ")),
					Refs: []string{r}, Pins: pins, netIDs: []string{p.Net},
					Fix:      fmt.Sprintf("give %s its own 100 nF on %s (shared coverage is not local decoupling)", r, v.name(p.Net)),
					Evidence: []string{"capacitor set on " + v.name(p.Net) + ": " + strings.Join(caps, ", ")}})
			}
		}
	}
	return c
}

var rvOutPinRe = regexp.MustCompile(`(?i)^(V?OUT\d*|VO\d*|OUTPUT)$`)
var rvInPinRe = regexp.MustCompile(`(?i)^(V?IN\d*|VI|INPUT|PVIN\d*|VCC|VDD)$`)

// regulators are the parts identified as voltage regulators: an intent
// block (ldo/buck/boost) or a real part with a typed power output.
func (v *rvView) regulators() map[string]string {
	out := map[string]string{}
	for r, why := range v.in.Hints.Regulators {
		if v.comps[r] != nil {
			out[r] = why
		}
	}
	for _, r := range v.refs {
		c := v.comps[r]
		if c.kind == pkConnector || c.kind == pkTestPoint {
			continue
		}
		for _, p := range c.pins {
			if p.Type == "power_out" {
				if _, ok := out[r]; !ok {
					out[r] = "pin " + p.Num + " (" + p.Name + ") is power_out"
				}
				break
			}
		}
	}
	return out
}

// regPins are a regulator's output (power_out or named VOUT/OUT) and
// input (power_in or named VIN/IN) pins on non-ground nets; byType tells
// the outputs came from pin types.
func (v *rvView) regPins(r string) (outs, ins []*rvPin, byType bool) {
	c := v.comps[r]
	for _, p := range c.pins {
		if p.Type == "power_out" && !v.groupIsGround(p.Net) {
			outs = append(outs, p)
			byType = true
		}
	}
	if len(outs) == 0 {
		for _, p := range c.pins {
			if rvOutPinRe.MatchString(p.Name) && !v.groupIsGround(p.Net) {
				outs = append(outs, p)
			}
		}
	}
	outNet := map[string]bool{}
	for _, p := range outs {
		outNet[v.find(p.Net)] = true
	}
	for _, p := range c.pins {
		if outNet[v.find(p.Net)] || v.groupIsGround(p.Net) || netname.IsUnconnected(p.Net) {
			continue
		}
		if p.Type == "power_in" || rvInPinRe.MatchString(p.Name) {
			ins = append(ins, p)
		}
	}
	return outs, ins, byType
}

func (v *rvView) capless(r string) (string, bool) {
	if s, ok := v.in.Hints.Capless[r]; ok {
		return s, true
	}
	if v.comps[r].fieldRole("capless") {
		return "component field ReviewRole of " + r, true
	}
	return "", false
}

// checkRegOutput: a regulator's output needs a capacitor to ground.
func (v *rvView) checkRegOutput() *rvCheck { return v.checkReg(RvRegOutputCap, true) }

// checkRegInput: a regulator's input needs a capacitor to ground.
func (v *rvView) checkRegInput() *rvCheck { return v.checkReg(RvRegInputCap, false) }

func (v *rvView) checkReg(id string, output bool) *rvCheck {
	c := v.check(id, "netlist", "pin-types or intent blocks")
	regs := v.regulators()
	var refs []string
	for r := range regs {
		refs = append(refs, r)
	}
	sort.Strings(refs)
	side := map[bool]string{true: "output", false: "input"}[output]
	for _, r := range refs {
		outs, ins, byType := v.regPins(r)
		if strings.HasPrefix(regs[r], "intent") {
			c.use("intent")
		}
		pins := ins
		if output {
			pins = outs
		}
		c.Coverage.Candidates++
		if len(pins) == 0 {
			c.unverified(fmt.Sprintf("%s (%s): no %s pin identified (power_out / VOUT-named for outputs, power_in / VIN-named for inputs)", r, regs[r], side))
			continue
		}
		if s, ok := v.capless(r); ok && output {
			c.exempt(r, "capless-stable regulator declared", s)
			continue
		}
		seen := map[string]bool{}
		for _, p := range pins {
			root := v.find(p.Net)
			if seen[root] {
				continue
			}
			seen[root] = true
			if why, src, ok := v.railExempt(p.Net, ""); ok {
				c.exempt(r+" "+v.name(p.Net), why, src)
				continue
			}
			c.Coverage.Checked++
			caps, other := v.capsTo(p.Net)
			if len(caps) > 0 {
				continue
			}
			if rc := v.capsToRail(p.Net); len(rc) > 0 {
				c.exempt(r+" "+v.name(p.Net), "capacitor(s) to another rail: "+strings.Join(rc, ", ")+" (AC return through that rail, e.g. a charge pump)", "netlist")
				continue
			}
			_ = other
			cert := "proven"
			how := "pin type " + p.Type
			if output && !byType || !output && p.Type != "power_in" {
				cert, how = "likely", "pin name "+p.Name
			}
			c.add(ReviewFinding{Severity: "error", Certainty: cert, Message: fmt.Sprintf("regulator %s %s %s (%s.%s) has no capacitor to ground", r, side, v.name(p.Net), r, p.Num),
				Refs: []string{r}, Pins: []string{r + "." + p.Num}, netIDs: []string{p.Net},
				Fix:      fmt.Sprintf("add the %s capacitor the %s datasheet requires (typ. 1–10 µF ceramic) from %s to GND", side, r, v.name(p.Net)),
				Evidence: []string{"regulator: " + regs[r], side + " pin by " + how, "no capacitor from " + v.name(p.Net) + " to ground"}})
		}
	}
	return c
}

// checkBulk: a rail feeding parts' power inputs needs at least BulkMinF of
// capacitance to ground in total (a floor, not an adequacy check). A cap
// without a readable value leaves the rail unverified.
func (v *rvView) checkBulk() *rvCheck {
	c := v.check(RvBulkCap, "netlist", "pin-types", "capacitor values")
	done := map[string]bool{}
	for _, r := range v.icParts() {
		for _, p := range v.comps[r].pins {
			if p.Type != "power_in" || v.groupIsGround(p.Net) || netname.IsUnconnected(p.Net) || done[v.find(p.Net)] {
				continue
			}
			done[v.find(p.Net)] = true
			c.Coverage.Candidates++
			name := v.name(p.Net)
			if why, src, ok := v.railExempt(p.Net, ""); ok {
				c.exempt(name, why, src)
				continue
			}
			caps, _ := v.capsTo(p.Net)
			if len(caps) == 0 {
				c.Coverage.Checked++
				continue // the decoupling check reports it
			}
			total := 0.0
			var unknown []string
			for _, cr := range caps {
				if x, ok := powersim.ParseValue(v.comps[cr].c.Value); ok {
					total += x
				} else {
					unknown = append(unknown, cr+" ("+v.comps[cr].c.Value+")")
				}
			}
			if total+1e-12 >= BulkMinF {
				c.Coverage.Checked++
				continue
			}
			if len(unknown) > 0 {
				c.unverified(fmt.Sprintf("%s: capacitor value(s) not readable: %s", name, strings.Join(unknown, ", ")))
				continue
			}
			c.Coverage.Checked++
			c.add(ReviewFinding{Severity: "warning", Certainty: "heuristic", Message: fmt.Sprintf("rail %s has %s of capacitance to ground (%s), below the %s bulk floor", name, rvFarad(total), strings.Join(caps, ", "), rvFarad(BulkMinF)),
				Refs: caps, netIDs: []string{p.Net},
				Fix:      fmt.Sprintf("add bulk capacitance (≥ %s, typ. 10 µF) on %s near where it enters the loads", rvFarad(BulkMinF), name),
				Evidence: []string{"capacitor values from the netlist", "loads on the rail: power_in pins"}})
		}
	}
	return c
}

func rvFarad(f float64) string {
	switch {
	case f >= 1e-6:
		return trimF(f*1e6) + " µF"
	case f >= 1e-9:
		return trimF(f*1e9) + " nF"
	}
	return trimF(f*1e12) + " pF"
}

func trimF(x float64) string {
	s := fmt.Sprintf("%.3f", math.Round(x*1000)/1000)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}

// ── net integrity ───────────────────────────────────────────────────────────

var rvQuoted = regexp.MustCompile(`'([^']*)'`)

// checkMerged: two names on one net (kicad-cli ERC multiple_net_names —
// KiCad's own label/power-symbol driver resolution, per sheet instance).
// Intent review.aliases exempt intentional ones. No ERC, or the rule set
// to ignore in the project: cannot-check.
func (v *rvView) checkMerged() *rvCheck {
	c := v.check(RvMergedNets, "kicad-cli ERC (multiple_net_names)")
	if v.in.ERC == nil {
		c.Coverage.Candidates++
		c.unverified("no kicad-cli ERC report")
		return c
	}
	if v.in.ERCIgnored["multiple_net_names"] {
		c.Coverage.Candidates++
		c.unverified("the project sets ERC rule multiple_net_names to ignore: KiCad does not report merged names (set it to warning)")
		return c
	}
	alias := func(names []string) (string, bool) {
		for _, g := range v.in.Hints.Aliases {
			set := map[string]bool{}
			for _, n := range g {
				set[n] = true
			}
			all := true
			for _, n := range names {
				all = all && (set[n] || set[netname.Unescape(n)])
			}
			if all {
				return "intent review.aliases " + strings.Join(g, "="), true
			}
		}
		return "", false
	}
	for _, e := range v.in.ERC.Violations {
		if e.Type != "multiple_net_names" {
			continue
		}
		c.Coverage.Candidates++
		c.Coverage.Checked++
		var names, items []string
		for _, m := range rvQuoted.FindAllStringSubmatch(e.Description, -1) {
			names = append(names, m[1])
		}
		for _, it := range e.Items {
			items = append(items, it.Description)
		}
		sort.Strings(names)
		names = rvUniq(names)
		if src, ok := alias(names); ok {
			c.exempt(strings.Join(names, " = ")+" on "+e.Sheet, "intentional alias", src)
			continue
		}
		f := ReviewFinding{Severity: "error", Message: fmt.Sprintf("names %s are on one net on sheet %s: %s", strings.Join(names, ", "), e.Sheet, e.Description),
			Sheet: e.Sheet, Fix: "rename or separate the labels / power symbols so each net has one name (or declare an intentional alias in intent review.aliases)",
			Evidence: append([]string{"kicad-cli ERC multiple_net_names (" + e.Severity + ")"}, items...)}
		f.Rule = RvMergedNets
		f.Nets = names
		f.Fingerprint = ReviewFingerprint(RvMergedNets, nil, nil, append([]string{e.Sheet}, names...))
		f.Certainty = "proven"
		c.Findings = append(c.Findings, f)
	}
	return c
}

// checkSinglePin: a named net that reaches only one pin is a typo or a
// missing connection. Test points, mounting holes, one-pin parts, no-
// connect pins and nets intent declares are exempt; a connector pin is the
// off-board boundary (info); an auto-named net (a dangling wire) is a
// warning.
func (v *rvView) checkSinglePin() *rvCheck {
	c := v.check(RvSinglePinNet, "netlist")
	for _, id := range v.netIDs {
		if netname.IsUnconnected(id) {
			continue
		}
		pins := v.groupPins(id)
		if len(pins) != 1 || len(v.group(id)) > 1 {
			continue
		}
		p := pins[0]
		if p.NC {
			continue
		}
		c.Coverage.Candidates++
		comp := v.comps[p.Ref]
		name := v.name(id)
		switch {
		case comp.kind == pkTestPoint || comp.kind == pkHole:
			c.exempt(name, "only pin is "+p.Ref+" ("+comp.kind+")", "part kind")
			continue
		case len(comp.c.Pins) == 1 || len(comp.c.Pins) == 0 && len(comp.pins) == 1:
			c.exempt(name, p.Ref+" has a single pin", "part pin count")
			continue
		}
		if s, ok := v.in.Hints.TestPoint[p.Ref]; ok {
			c.exempt(name, p.Ref+" is a declared test point", s)
			continue
		}
		if comp.fieldRole("test-point") {
			c.exempt(name, p.Ref+" is a declared test point", "component field ReviewRole")
			continue
		}
		if s, ok := v.in.Hints.SinglePinNet[id]; ok {
			c.exempt(name, "intentional single-pin net", s)
			continue
		}
		c.Coverage.Checked++
		f := ReviewFinding{Refs: []string{p.Ref}, Pins: []string{p.Ref + "." + p.Num}, netIDs: []string{id},
			Evidence: []string{"KiCad netlist: net " + name + " has one node, " + p.Ref + "." + p.Num + " (" + p.Type + ")"}}
		switch {
		case comp.kind == pkConnector:
			f.Severity, f.Message = "info", fmt.Sprintf("net %s reaches only connector pin %s.%s (off-board)", name, p.Ref, p.Num)
			f.Fix = "fine for a spare connector pin; otherwise connect the net"
		case strings.HasPrefix(id, "Net-("):
			f.Severity, f.Message = "warning", fmt.Sprintf("a wire on %s.%s goes nowhere (net %s has one pin)", p.Ref, p.Num, name)
			f.Fix = "connect the wire or delete it (a pin left open should carry a no-connect flag)"
		default:
			f.Severity, f.Message = "error", fmt.Sprintf("net %s reaches only %s.%s (%s): no other pin carries that name", name, p.Ref, p.Num, p.Name)
			f.Fix = "check the label spelling against its partner, connect the net, or mark the pin no-connect / declare it in intent review.singlePinNets"
		}
		c.add(f)
	}
	return c
}

// checkHier: every sheet pin needs a hierarchical label of its name in
// the sheet's file and every hierarchical label a sheet pin, per sheet
// instance.
func (v *rvView) checkHier() *rvCheck {
	c := v.check(RvHierMismatch, "sheet files")
	if v.in.HierErr != nil {
		c.Coverage.Candidates++
		c.unverified("sheet files not readable: " + v.in.HierErr.Error())
		return c
	}
	if !v.in.HierRan {
		c.Coverage.Candidates++
		c.unverified("sheet files not given")
		return c
	}
	c.Coverage.Candidates, c.Coverage.Checked = v.in.HierSheets, v.in.HierSheets
	for _, m := range v.in.Hier {
		f := ReviewFinding{Rule: RvHierMismatch, Severity: "error", Certainty: "proven", Sheet: m.Path, Nets: []string{m.Name},
			Evidence: []string{"sheet " + m.Path + " (file " + m.File + "), parent " + m.Parent}}
		if m.Kind == "pin-without-label" {
			f.Message = fmt.Sprintf("sheet pin %q of %s has no hierarchical label in %s", m.Name, m.Path, m.File)
			f.Fix = "add hierarchical label " + m.Name + " in " + m.File + " or delete the sheet pin"
		} else {
			f.Message = fmt.Sprintf("hierarchical label %q in %s has no sheet pin on %s", m.Name, m.File, m.Path)
			f.Fix = "add sheet pin " + m.Name + " to the sheet symbol of " + m.Path + " (Sync Sheet Pins) or delete the label"
		}
		f.Fingerprint = ReviewFingerprint(RvHierMismatch, nil, nil, []string{m.Path, m.Kind, m.Name})
		c.Findings = append(c.Findings, f)
	}
	return c
}
