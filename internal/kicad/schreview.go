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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	// Missing names the evidence whose absence downgraded the finding to
	// likely / heuristic (it is never reported as verified).
	Missing []string `json:"missingEvidence,omitempty"`
	netIDs  []string
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
	// Verdict tells a clean pass from one with unwaived warnings (likely /
	// heuristic findings, incomplete coverage) and one that needed waivers.
	Verdict    string            `json:"verdict"` // pass | pass-with-warnings | pass-with-waivers | fail | cannot-check
	Errors     int               `json:"errors"`
	Warnings   int               `json:"warnings"`
	Infos      int               `json:"infos"`
	Waived     int               `json:"waived"`
	Undecided  []string          `json:"cannotCheck,omitempty"`
	Inputs     []ReviewInputFile `json:"inputs,omitempty"`
	RuntimeMs  float64           `json:"runtimeMs"`
	NotCovered []string          `json:"notCovered"`
	// Summary is the grouped reading of the findings: every error and
	// cannot-check item in full, warnings / info grouped by check, part
	// and missing evidence (the checks keep every finding).
	Summary *ReviewSummary `json:"summary,omitempty"`
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
	// PullNone: pins declared to have no internal pull; PullsComplete
	// (its source, "" = not declared): every internal pull the design
	// relies on is listed, so an unlisted pin has none.
	PullNone      map[string]string
	PullsComplete string
	// PushPull: bidirectional / tri-state pins declared push-pull outputs.
	PushPull map[string]string
	// PinDomain: pin → supply net identity of its I/O domain; PinMaxV: a
	// pin's declared maximum voltage.
	PinDomain map[string]string
	PinMaxV   map[string]float64
	// ChargePump: parts declared charge pumps (output cap to another rail).
	ChargePump map[string]string
	// InputPin: pins declared inputs (a passive/unspecified pin is
	// otherwise no receiver); CurrentDriver: pins declared LED current
	// drivers; CommonPins: groups of REF.PIN that are one electrical pin.
	InputPin, CurrentDriver map[string]string
	CommonPins              [][]string
	// Connector: parts declared connectors; GroundedPin: power pins that
	// sit on ground by design.
	Connector, GroundedPin map[string]string
}

// ReviewInput is what Review checks.
type ReviewInput struct {
	Netlist *SchNetlist
	ERC     *ERCReport // nil: not run
	// ERCIgnored lists ERC rule types the project sets to "ignore".
	ERCIgnored map[string]bool
	// Hier are the sheet-pin / hierarchical-label mismatches of the sheet
	// files (SheetHierarchyMismatches); HierErr: the files could not be read.
	// Stacked: lib id ("Lib:Part") → groups of pin numbers the symbol
	// stacks at one position of one unit (one electrical pin; LibStacked).
	Stacked    map[string][][]string
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
	notes             []string // input problems found while reviewing
	ledEnd            string   // the endpoint the last LED trace reached (rail id | pin | conn)
	ledEndPin         *rvPin   // the driver pin when ledEnd is "pin"
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

// rvLEDWord: "LED" as a word of a part name or value (LED, LED_Small,
// LED_RGB, "Red LED") — not inside another word ("D_TVS_Filled").
var rvLEDWord = regexp.MustCompile(`(?i)(^|[^a-z])LEDS?([^a-z]|$)`)

func rvPrefix(ref string) string { return strings.ToUpper(rvPrefixRe.FindString(ref)) }

// rvKind classifies a component from its reference prefix and library
// part (KiCad's stock naming); the classes only decide which checks look
// at a part, never a verdict by themselves.
func rvKind(c *SchNetComp, npins int) string {
	pre := rvPrefix(c.Ref)
	lib := strings.ToLower(c.Lib + ":" + c.Part)
	val := strings.ToLower(c.Value)
	fp := strings.ToLower(c.Footprint)
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
	case (pre == "D" || pre == "LED") && (rvLEDWord.MatchString(c.Part) || rvLEDWord.MatchString(val)):
		return pkLED
	case strings.HasPrefix(strings.ToLower(c.Lib), "connector") || strings.HasPrefix(fp, "connector_"):
		// KiCad's connector libraries (Connector, Connector_Generic …;
		// footprints Connector_*); an explicit role is applied in newRvView
		return pkConnector
	case (pre == "J" || pre == "P") && !rvHasPowerIn(c):
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

func rvHasPowerIn(c *SchNetComp) bool {
	for _, p := range c.Pins {
		if rvBaseType(p.Type) == "power_in" {
			return true
		}
	}
	return false
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
		if _, ok := in.Hints.Connector[r]; ok || c.fieldRole("connector") {
			c.kind = pkConnector // explicit role (intent review.connectors / ReviewRole)
		}
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

// resolvePin turns "REF.PIN" or "REF.PINNAME" into "REF.NUMBER": a pin
// number of REF as is, else the one pin of REF with that name; an
// ambiguous or unknown name is a note and no match.
func (v *rvView) resolvePin(key, what string) (string, bool) {
	ref, pin, ok := strings.Cut(key, ".")
	c := v.comps[ref]
	if !ok || c == nil {
		v.notes = append(v.notes, fmt.Sprintf("%s: %q is not REF.PIN of a part", what, key))
		return "", false
	}
	var byName []string
	for _, p := range c.pins {
		if p.Num == pin {
			return ref + "." + p.Num, true
		}
		if strings.EqualFold(p.Name, pin) {
			byName = append(byName, p.Num)
		}
	}
	for _, p := range c.c.Pins { // library pins, on a net or not
		if p.Number == pin {
			return ref + "." + p.Number, true
		}
		if strings.EqualFold(p.Name, pin) {
			byName = append(byName, p.Number)
		}
	}
	byName = rvUniq(sortedCopy(byName))
	switch len(byName) {
	case 1:
		return ref + "." + byName[0], true
	case 0:
		v.notes = append(v.notes, fmt.Sprintf("%s: %s has no pin %q", what, ref, pin))
	default:
		v.notes = append(v.notes, fmt.Sprintf("%s: pin name %q of %s is ambiguous (pins %s); use the pin number", what, pin, ref, strings.Join(byName, ", ")))
	}
	return "", false
}

func sortedCopy(xs []string) []string {
	ys := append([]string(nil), xs...)
	sort.Strings(ys)
	return ys
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

// pullAbsent reports explicit evidence that pin p has no internal pull:
// intent review.internalPullsComplete (every relied-on internal pull is
// listed) or review.noInternalPulls naming the pin. An intent.json alone,
// or one without review{}, is no such evidence.
func (v *rvView) pullAbsent(p *rvPin) (string, bool) {
	if v.in.Hints.PullsComplete != "" {
		return v.in.Hints.PullsComplete, true
	}
	if s, ok := pinRole(v.in.Hints.PullNone, p); ok {
		return s, true
	}
	return "", false
}

// groupHint looks a net-level hint up on every net joined to n.
func (v *rvView) groupHint(m map[string]string, n string) (string, bool) {
	for _, g := range v.group(n) {
		if s, ok := m[g]; ok {
			return s, true
		}
	}
	return "", false
}

// isJoin: a part that only joins nets (0 Ω, net tie, bridged jumper).
func (v *rvView) isJoin(ref string) bool {
	c := v.comps[ref]
	switch c.kind {
	case pkNetTie:
		return true
	case pkResistor:
		val, ok := powersim.ParseValue(c.c.Value)
		return ok && val == 0
	case pkJumper:
		part := strings.ToLower(c.c.Part)
		return strings.Contains(part, "bridged") || strings.Contains(part, "closed")
	}
	return false
}

var (
	// A, A1, ANODE, +, and the per-die forms of multi-colour LEDs (RA,
	// GA, BA, A_R …); cathodes likewise
	rvAnodeRe   = regexp.MustCompile(`(?i)^(A\d?|ANODE\w*|\+|[RGBYW]_?A|A_?[RGBYW])$`)
	rvCathodeRe = regexp.MustCompile(`(?i)^(K\d?|C|CATHODE\w*|-|[RGBYW]_?K|K_?[RGBYW])$`)
	rvClampRe   = regexp.MustCompile(`(?i)TVS|ESD|ZENER|D_Z|TRANSIL|SUPPRESS`)
)

// diodeRole is a diode/LED pin's terminal: "A", "K", or "" (unknown),
// from its name, else its number (libraries number LED dies GA/GK/RA/RK).
func (v *rvView) diodeRole(p *rvPin) string {
	for _, t := range []string{p.Name, p.Num} {
		switch {
		case rvAnodeRe.MatchString(t):
			return "A"
		case rvCathodeRe.MatchString(t):
			return "K"
		}
	}
	return ""
}

// dcMates are the pins through which a DC current can pass pin p of a
// resistor, inductor, diode or LED: dir "in" — current flowing from the
// mate into p's net (a diode only when p is its cathode), "out" — from p's
// net out through the mate (p the anode). Resistors and inductors conduct
// both ways; a TVS / zener / ESD clamp is not a DC path; a diode pin whose
// terminal role cannot be told sets unknown.
func (v *rvView) dcMates(p *rvPin, dir string) (out []*rvPin, unknown bool) {
	c := v.comps[p.Ref]
	switch c.kind {
	case pkResistor, pkInductor:
		for _, q := range c.pins {
			if q != p {
				out = append(out, q)
			}
		}
	case pkResArray:
		mate := rvArrayMate(c, p)
		for _, q := range c.pins {
			if mate[q.Num] {
				out = append(out, q)
			}
		}
	case pkDiode, pkLED:
		if rvClampRe.MatchString(c.c.Part + " " + c.c.Value) {
			return nil, false
		}
		role := v.diodeRole(p)
		if role == "" {
			return nil, true
		}
		if dir == "in" && role != "K" || dir == "out" && role != "A" {
			return nil, false
		}
		want := map[string]string{"K": "A", "A": "K"}[role]
		for _, q := range c.pins {
			if q != p {
				if r := v.diodeRole(q); r == "" {
					return nil, true
				} else if r == want {
					out = append(out, q)
				}
			}
		}
	}
	return out, false
}

// reachesSupply follows a forward-biased DC path from the pins of kinds
// on n's group back to a supply rail (current flowing from the supply into
// n: through resistors, inductors and diodes/LEDs anode→cathode) and
// describes it ("" = none within 4 parts). unknown: a diode terminal on
// the way could not be told.
func (v *rvView) reachesSupply(n string, kinds ...string) (path string, unknown bool) {
	want := map[string]bool{}
	for _, k := range kinds {
		want[k] = true
	}
	type step struct {
		net, path string
		depth     int
	}
	seen := map[string]bool{v.find(n): true}
	var q []step
	for _, p := range v.groupPins(n) {
		if !want[v.comps[p.Ref].kind] {
			continue
		}
		ms, u := v.dcMates(p, "in")
		unknown = unknown || u
		for _, m := range ms {
			q = append(q, step{m.Net, p.Ref, 1})
		}
	}
	for len(q) > 0 {
		s := q[0]
		q = q[1:]
		if seen[v.find(s.net)] || netname.IsUnconnected(s.net) {
			continue
		}
		seen[v.find(s.net)] = true
		if v.isSupply(s.net) {
			return s.path + " → " + v.name(s.net), false
		}
		if v.groupIsGround(s.net) || s.depth >= 4 {
			continue
		}
		for _, p := range v.groupPins(s.net) {
			ms, u := v.dcMates(p, "in")
			unknown = unknown || u
			for _, m := range ms {
				q = append(q, step{m.Net, s.path + " → " + p.Ref, s.depth + 1})
			}
		}
	}
	return "", unknown
}

// rvDrive is what drives a net group.
type rvDrive struct {
	State string // driven | offboard | ambiguous (topology) | unknown (pin direction) | none
	How   string
}

// driveOf traces a net group to what defines its DC level: a push-pull
// output (output, power_out, or a bidirectional/tri-state pin intent
// declares push-pull), an open-drain pin (its bias is the open-drain
// check's), a rail through resistors / inductors, or a diode / LED in the
// direction it conducts. A capacitor, a clamp or a reverse-biased diode
// does not drive. A connector is the off-board boundary. A switch, an
// open jumper or a diode of unknown polarity is unresolved topology
// (ambiguous); a pin of unknown direction (GPIO, passive or unspecified IC
// pin, transistor) leaves the drive unverified (unknown).
func (v *rvView) driveOf(n string) rvDrive {
	type st struct{ net, dir string } // dir: "" any, "in" sourced only, "out" sunk only
	seen := map[string]bool{}
	q := []st{{n, ""}}
	var off, amb, unk []string
	for depth := 0; len(q) > 0 && depth < 6; depth++ {
		var next []st
		for _, s := range q {
			key := v.find(s.net) + "|" + s.dir
			if seen[key] || netname.IsUnconnected(s.net) {
				continue
			}
			seen[key] = true
			if v.isSupply(s.net) && s.dir != "out" {
				return rvDrive{"driven", "sourced from rail " + v.name(s.net)}
			}
			if v.groupIsGround(s.net) && s.dir != "in" {
				return rvDrive{"driven", "sunk to " + v.name(s.net)}
			}
			if v.isSupply(s.net) || v.groupIsGround(s.net) {
				continue // a rail behind a diode that cannot conduct that way
			}
			for _, g := range v.group(s.net) {
				if j := v.jumpers[g]; len(j) > 0 {
					amb = append(amb, "jumper "+strings.Join(j, ","))
				}
			}
			for _, p := range v.groupPins(s.net) {
				c := v.comps[p.Ref]
				if v.isJoin(p.Ref) {
					continue
				}
				switch p.Type {
				case "output", "power_out", "open_collector", "open_emitter":
					return rvDrive{"driven", p.Ref + "." + p.Num + " " + p.Type}
				case "bidirectional", "tri_state":
					if src, ok := pinRole(v.in.Hints.PushPull, p); ok {
						return rvDrive{"driven", p.Ref + "." + p.Num + " push-pull (" + src + ")"}
					}
					unk = append(unk, p.Ref+"."+p.Num+" ("+p.Type+")")
					continue
				}
				switch c.kind {
				case pkConnector:
					off = append(off, p.Ref+"."+p.Num)
				case pkResistor, pkResArray, pkInductor:
					ms, _ := v.dcMates(p, "in")
					for _, m := range ms {
						next = append(next, st{m.Net, s.dir})
					}
				case pkDiode, pkLED:
					for _, d := range []string{"in", "out"} {
						if s.dir != "" && s.dir != d {
							continue
						}
						ms, u := v.dcMates(p, d)
						if u {
							amb = append(amb, p.Ref+" (diode polarity unknown)")
						}
						for _, m := range ms {
							next = append(next, st{m.Net, d})
						}
					}
				case pkCap, pkTestPoint, pkHole:
				case pkSwitch, pkJumper:
					amb = append(amb, p.Ref+" ("+c.kind+")")
				default:
					if p.Type != "input" && p.Type != "power_in" && !pinIsInput(v, p) {
						unk = append(unk, p.Ref+"."+p.Num+" ("+p.Type+")")
					}
				}
			}
		}
		q = next
	}
	switch {
	case len(off) > 0:
		return rvDrive{"offboard", strings.Join(rvUniq(off), ", ")}
	case len(amb) > 0:
		sort.Strings(amb)
		return rvDrive{"ambiguous", strings.Join(rvUniq(amb), ", ")}
	case len(unk) > 0:
		sort.Strings(unk)
		return rvDrive{"unknown", strings.Join(rvUniq(unk), ", ")}
	}
	return rvDrive{"none", ""}
}

// pinIsInput: a pin intent review.inputPins declares an input.
func pinIsInput(v *rvView, p *rvPin) bool {
	_, ok := pinRole(v.in.Hints.InputPin, p)
	return ok
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
// to a waived bus keeps the waiver. floating-input is not one: its waiver
// names the receivers it accepted.
var rvNetKeyed = map[string]bool{RvI2CPullup: true, RvOpenDrain: true, RvBulkCap: true, RvSinglePinNet: true}

// ReviewFingerprint is the stable identity of a finding: "sr2:<rule>:" and
// 24 hex digits of the SHA-256 of the canonical JSON [rule, refs, pins,
// nets, extra] (each list sorted) — names are never joined unescaped, so
// ["A,B","C"] and ["A","B,C"] differ. sr2 is the encoding version.
func ReviewFingerprint(rule string, refs, pins, nets []string, extra ...string) string {
	sorted := func(xs []string) []string {
		ys := append([]string{}, xs...)
		sort.Strings(ys)
		return ys
	}
	b, _ := json.Marshal([]any{rule, sorted(refs), sorted(pins), sorted(nets), sorted(extra)})
	h := sha256.Sum256(b)
	return "sr2:" + rule + ":" + hex.EncodeToString(h[:12])
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
	r := &SchReview{Schema: SchReviewSchema, NotCovered: ReviewNotCovered, Notes: rvUniq(sortedCopy(v.notes))}
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
	defer r.summarize()
	switch {
	case r.Status != gate.Pass:
	case r.Warnings > 0:
		r.Verdict = "pass-with-warnings" // coverage incomplete: never "verified"
	case r.Waived > 0:
		r.Verdict = "pass-with-waivers"
	}
}

// ReviewSummary groups the findings for a human reader.
type ReviewSummary struct {
	Errors      []string      `json:"errors"`      // every unwaived error: "rule fingerprint: message"
	CannotCheck []string      `json:"cannotCheck"` // every unverified item: "check: item"
	Groups      []ReviewGroup `json:"groups"`      // unwaived warnings and info
	Waived      []string      `json:"waived,omitempty"`
	Findings    int           `json:"findings"` // all findings in checks[]
}

// ReviewGroup is a set of warnings / info of one check, severity and
// missing evidence; Parts breaks it down by part ("U12×8", most first)
// and Examples gives a few representative pins and fingerprints.
type ReviewGroup struct {
	Check     string   `json:"check"`
	Severity  string   `json:"severity"`
	Certainty string   `json:"certainty"`
	Missing   string   `json:"missing,omitempty"`
	Count     int      `json:"count"`
	Parts     []string `json:"parts"`
	Examples  []string `json:"examples"`
	parts     map[string]int
}

var rvPinTok = regexp.MustCompile(`\b[A-Za-z_#]+\d+\.[A-Za-z0-9_+~{}-]+( \([^)]*\))?`)

// normMissing is the missing-evidence text with the pins taken out, so
// the same gap on different pins groups together.
func normMissing(m []string) string {
	if len(m) == 0 {
		return ""
	}
	t := rvPinTok.ReplaceAllString(m[0], "<pin>")
	for strings.Contains(t, "<pin>, <pin>") {
		t = strings.ReplaceAll(t, "<pin>, <pin>", "<pin>")
	}
	return t
}

func (r *SchReview) summarize() {
	sm := &ReviewSummary{Errors: []string{}, CannotCheck: []string{}, Groups: []ReviewGroup{}}
	idx := map[string]int{}
	for _, c := range r.Checks {
		for _, u := range c.Unverified {
			sm.CannotCheck = append(sm.CannotCheck, c.ID+": "+u)
		}
		for _, f := range c.Findings {
			sm.Findings++
			switch {
			case f.Waived != "":
				sm.Waived = append(sm.Waived, f.Fingerprint+" "+f.Waived)
				continue
			case f.Severity == "error":
				sm.Errors = append(sm.Errors, f.Fingerprint+": "+f.Message)
				continue
			}
			part := "-"
			if len(f.Pins) > 0 {
				part, _, _ = strings.Cut(f.Pins[0], ".")
			} else if len(f.Refs) > 0 {
				part = f.Refs[0]
			}
			g := ReviewGroup{Check: c.ID, Severity: f.Severity, Certainty: f.Certainty, Missing: normMissing(f.Missing), parts: map[string]int{}}
			k := g.Check + "|" + g.Severity + "|" + g.Certainty + "|" + g.Missing
			i, ok := idx[k]
			if !ok {
				i = len(sm.Groups)
				idx[k] = i
				sm.Groups = append(sm.Groups, g)
			}
			sm.Groups[i].Count++
			sm.Groups[i].parts[part]++
			if len(sm.Groups[i].Examples) < 3 {
				ex := strings.Join(f.Pins, ",")
				if ex == "" {
					ex = strings.Join(f.Nets, ",")
				}
				sm.Groups[i].Examples = append(sm.Groups[i].Examples, ex+" "+f.Fingerprint)
			}
		}
	}
	for i := range sm.Groups {
		g := &sm.Groups[i]
		var ps []string
		for p := range g.parts {
			ps = append(ps, p)
		}
		sort.Slice(ps, func(a, b int) bool {
			if g.parts[ps[a]] != g.parts[ps[b]] {
				return g.parts[ps[a]] > g.parts[ps[b]]
			}
			return rvPinLess(ps[a], ps[b])
		})
		for j, p := range ps {
			if j == 8 {
				g.Parts = append(g.Parts, fmt.Sprintf("… %d more", len(ps)-8))
				break
			}
			g.Parts = append(g.Parts, fmt.Sprintf("%s×%d", p, g.parts[p]))
		}
	}
	sev := map[string]int{"warning": 0, "info": 1}
	sort.SliceStable(sm.Groups, func(i, j int) bool {
		a, b := sm.Groups[i], sm.Groups[j]
		if sev[a.Severity] != sev[b.Severity] {
			return sev[a.Severity] < sev[b.Severity]
		}
		if a.Check != b.Check {
			return a.Check < b.Check
		}
		return a.Count > b.Count
	})
	r.Summary = sm
}

// Lines is the grouped human reading: errors and cannot-check items in
// full, then one line per group.
func (r *SchReview) Lines() []string {
	if r.Summary == nil {
		r.summarize()
	}
	var out []string
	for _, e := range r.Summary.Errors {
		out = append(out, "error "+e)
	}
	for _, u := range r.Summary.CannotCheck {
		out = append(out, "cannot-check "+u)
	}
	for _, g := range r.Summary.Groups {
		l := fmt.Sprintf("%s %s (%s) ×%d [%s]", g.Severity, g.Check, g.Certainty, g.Count, strings.Join(g.Parts, " "))
		if g.Missing != "" {
			l += " — missing: " + g.Missing
		}
		if len(g.Examples) > 0 {
			l += " — e.g. " + strings.Join(g.Examples, "; ")
		}
		out = append(out, l)
	}
	return out
}

// SummaryLine is one line for stderr / a gate detail.
func (r *SchReview) SummaryLine() string {
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

// logicDrivers are the output / tri-state pins that drive net n's group
// directly or through one series resistor, other than I2C (SDA/SCL) pins:
// a bus bit-banged by ordinary logic.
func (v *rvView) logicDrivers(n string) []string {
	var out []string
	look := func(pins []*rvPin, via string) {
		for _, p := range pins {
			if (p.Type == "output" || p.Type == "tri_state") && !rvI2CRe.MatchString(p.Name) && v.comps[p.Ref].kind != pkConnector {
				out = append(out, p.Ref+"."+p.Num+" ("+p.Type+via+")")
			}
		}
	}
	look(v.groupPins(n), "")
	for _, p := range v.groupPins(n) {
		if c := v.comps[p.Ref]; c.kind == pkResistor && !v.isJoin(p.Ref) {
			if f := v.farPin(p); f != nil && !v.isSupply(f.Net) && !v.groupIsGround(f.Net) {
				look(v.groupPins(f.Net), " through "+p.Ref)
			}
		}
	}
	sort.Strings(out)
	return rvUniq(out)
}

// railDefined: the net is a rail defined by a power symbol (a global
// power-named / ground net), a power_out source or an intent rail.
func (v *rvView) railDefined(n string) bool {
	if v.groupIsGround(n) {
		return true
	}
	for _, g := range v.group(n) {
		if r := v.in.Hints.NetRole[g]; r == "power" || r == "ground" {
			return true
		}
		if nm := v.name(g); !strings.HasPrefix(g, "/") && !strings.HasPrefix(g, "Net-(") && (isPowerName(nm) || strings.HasPrefix(nm, "-")) {
			return true
		}
		for _, p := range v.nets[g] {
			if p.Type == "power_out" {
				return true
			}
		}
	}
	return false
}

// onBoardDevices are the ICs / other active parts with a non-passive pin
// on n's group (connectors and passives excluded).
func (v *rvView) onBoardDevices(n string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range v.groupPins(n) {
		k := v.comps[p.Ref].kind
		if (k == pkIC || k == pkOther) && p.Type != "passive" && !seen[p.Ref] {
			seen[p.Ref] = true
			out = append(out, p.Ref)
		}
	}
	sort.Strings(out)
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
		if src, ok := v.groupHint(v.in.Hints.PullUpNet, id); ok {
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
			if on := v.onBoardDevices(id); len(on) < 2 {
				// one on-board device: the bus is completed off-board, where
				// the pull-up may legitimately be
				c.add(ReviewFinding{Severity: "info", Certainty: cert, Message: fmt.Sprintf("I2C line %s has no pull-up on this board; it leaves through %s", v.name(id), strings.Join(off, ", ")),
					Refs: refs, netIDs: grp, Fix: "confirm the pull-up is on the other side of the connector, or add 2.2–10 kΩ to the bus supply",
					Evidence: append(ev, "connector pins are the off-board boundary", "one on-board device on the line")})
				continue
			}
			// two or more on-board devices talk over it without the
			// connector: the on-board bus needs its own pull-up
			ev = append(ev, fmt.Sprintf("%d on-board devices (%s) share the line; the connector (%s) cannot be relied on to pull it up", len(v.onBoardDevices(id)), strings.Join(v.onBoardDevices(id), ", "), strings.Join(off, ", ")))
		}
		if drv := v.logicDrivers(id); len(drv) > 0 {
			// driven by non-I2C push-pull / tri-state logic (a bit-banged
			// bus): a missing pull-up is not proven
			c.add(ReviewFinding{Severity: "warning", Certainty: "likely", Message: fmt.Sprintf("I2C line %s has no pull-up; it is driven by %s, so the need is not proven", v.name(id), strings.Join(drv, ", ")),
				Refs: refs, netIDs: grp, Fix: "if any device releases the line (open-drain SDA / clock stretching), add a pull-up to the bus supply",
				Evidence: append(ev, "non-I2C driver(s): "+strings.Join(drv, ", ")), Missing: []string{"whether the bus is open-drain on every device (intent review.pulledUpNets / a pull-up)"}})
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
// needs a pull-up (open-emitter: a pull-down). Only a proven push-pull
// output (pin type output / power_out, or a bidirectional/tri-state pin
// intent declares push-pull) exempts it; an MCU GPIO's direction is
// unknown. An LED/diode sinking into the pin exempts it only when its path
// back to a supply is traced. A declared internal pull exempts it; a
// missing pull-up is an error only with explicit evidence that the pins
// have no internal pull (intent review.internalPullsComplete or
// noInternalPulls) — otherwise unverified.
func (v *rvView) checkOpenDrain() *rvCheck {
	c := v.check(RvOpenDrain, "netlist", "pin-types", "internal-pull evidence (intent review.internalPullsComplete / noInternalPulls)")
	c.Used = c.Used[:2]
	done := map[string]bool{}
	for _, id := range v.netIDs {
		root := v.find(id)
		if done[root] {
			continue
		}
		pins := v.groupPins(id)
		_, oc, oe := rvDrivers(pins)
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
			var pp, unknownDir []string
			for _, p := range pins {
				switch p.Type {
				case "output", "power_out":
					pp = append(pp, p.Ref+"."+p.Num+" "+p.Type)
				case "bidirectional", "tri_state":
					if s, ok := pinRole(v.in.Hints.PushPull, p); ok {
						pp = append(pp, p.Ref+"."+p.Num+" push-pull ("+s+")")
					} else {
						unknownDir = append(unknownDir, p.Ref+"."+p.Num)
					}
				}
			}
			if len(pp) > 0 {
				c.exempt(name, "driven by a push-pull output: "+strings.Join(pp, ", "), "pin types (netlist) / intent review.pushPullOutputs")
				continue
			}
			pulls, amb := v.pulls(id, mode.dir)
			if len(pulls) > 0 {
				c.Coverage.Checked++
				continue
			}
			if mode.dir == "up" {
				path, unk := v.reachesSupply(id, pkLED, pkDiode)
				if path != "" {
					c.exempt(name, "the open-drain pin sinks a load returning to a supply: "+path, "netlist (traced)")
					continue
				}
				if unk {
					c.unverified(fmt.Sprintf("%s: a diode/LED on the line has a terminal whose anode/cathode role cannot be told", name))
					continue
				}
			}
			src, ok := v.groupHint(v.in.Hints.PullUpNet, id)
			if !ok {
				for _, p := range pins {
					if s, has := v.internalPull(p, mode.dir); has {
						src, ok = p.Ref+"."+p.Num+": "+s, true
						break
					}
				}
			}
			if ok {
				c.exempt(name, "pull-"+mode.dir+" declared", src)
				if strings.Contains(src, "intent") {
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
			if off := v.offBoard(id); len(off) > 0 && len(v.onBoardDevices(id)) < 2 {
				// the line's other end is off-board: its pull may be there
				c.Coverage.Checked++
				c.add(ReviewFinding{Severity: "info", Message: fmt.Sprintf("%s line %s (%s) has no pull-%s on this board; it leaves through %s", mode.what, name, strings.Join(drv, ", "), mode.dir, strings.Join(off, ", ")),
					Refs: rvRefs(mode.pins), Pins: rvPinKeys(mode.pins), netIDs: v.group(id), Fix: "confirm the pull-" + mode.dir + " is off-board, or add one",
					Evidence: []string{"pin types: " + strings.Join(drv, ", "), "connector pins are the off-board boundary", "one on-board device on the line"}})
				continue
			}
			var unknown, absent []string
			for _, p := range pins {
				if p.Type == "passive" || p.Type == "power_in" || v.comps[p.Ref].kind != pkIC && v.comps[p.Ref].kind != pkOther {
					continue
				}
				if s, ok := v.pullAbsent(p); ok {
					absent = append(absent, s)
				} else {
					unknown = append(unknown, p.Ref+"."+p.Num)
				}
			}
			if len(unknown) > 0 {
				c.Coverage.Checked++
				missing := []string{"internal-pull evidence for " + strings.Join(unknown, ", ") + " (intent review.internalPullups / noInternalPulls / internalPullsComplete)"}
				if len(unknownDir) > 0 {
					missing = append(missing, "direction of "+strings.Join(unknownDir, ", ")+" (intent review.pushPullOutputs if it drives the line)")
				}
				c.add(ReviewFinding{Severity: "warning", Certainty: "likely", Message: fmt.Sprintf("%s line %s (%s) has no pull-%s resistor; internal pulls unknown", mode.what, name, strings.Join(drv, ", "), mode.dir),
					Refs: rvRefs(mode.pins), Pins: rvPinKeys(mode.pins), netIDs: v.group(id),
					Fix:      fmt.Sprintf("add a pull-%s resistor, or declare the receiver's internal pull-%s (intent review.internalPull%ss)", mode.dir, mode.dir, mode.dir),
					Evidence: []string{"pin types: " + strings.Join(drv, ", "), "no proven push-pull output on the net", "no pull-" + mode.dir + " resistor"},
					Missing:  missing})
				continue
			}
			c.use("intent")
			c.Coverage.Checked++
			kind := "line"
			if rvResetIntRe.MatchString(name) {
				kind = "reset/interrupt/status line"
			}
			for _, p := range mode.pins {
				if rvResetIntRe.MatchString(p.Name) {
					kind = "reset/interrupt/status line"
				}
			}
			c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("%s %s %s is driven only by %s pin(s) %s and has no pull-%s", mode.what, kind, name, mode.what, strings.Join(drv, ", "), mode.dir),
				Refs: rvRefs(mode.pins), Pins: rvPinKeys(mode.pins), netIDs: v.group(id),
				Fix:      fmt.Sprintf("add a pull-%s resistor (typ. 4.7–10 kΩ) from %s to the %s, or declare the receiver's internal pull-%s in intent review.internalPull%ss", mode.dir, name, map[string]string{"up": "receiver's supply", "down": "ground"}[mode.dir], mode.dir, mode.dir),
				Evidence: []string{"pin types: " + strings.Join(drv, ", "), "no proven push-pull output on the net", "no resistor to a " + map[string]string{"up": "supply", "down": "ground"}[mode.dir], "no internal pull: " + strings.Join(rvUniq(absent), ", ")}})
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

// pinDomain is the supply rail a signal pin belongs to: intent
// review.pinDomains, else the part's only non-ground power rail; "" when
// the part has several rails and none is declared.
func (v *rvView) pinDomain(p *rvPin) (string, string) {
	if s, ok := pinRole(v.in.Hints.PinDomain, p); ok {
		return s, "intent review.pinDomains"
	}
	rails := map[string]string{}
	for _, q := range v.comps[p.Ref].pins {
		if q.Type == "power_in" && !v.groupIsGround(q.Net) && !netname.IsUnconnected(q.Net) {
			rails[v.find(q.Net)] = q.Net
		}
	}
	if len(rails) == 1 {
		for _, n := range rails {
			return n, "the only supply rail of " + p.Ref
		}
	}
	return "", ""
}

func (v *rvView) voltage(n string) (float64, bool) {
	for _, g := range v.group(n) {
		if x, ok := v.in.Hints.NetVoltage[g]; ok {
			return x, true
		}
	}
	return 0, false
}

// checkPullupVoltage: a pull-up's rail against what the pulled pin
// tolerates. A pull-up to the pin's own supply group (the part's only
// rail, or intent review.pinDomains) is structurally matched: pass, no
// numbers needed. Above a declared maximum (review.pinMaxV) is a proven
// error; above the domain voltage + 0.3 V without a known maximum, or a
// different / unknown domain without voltages, is a likely warning
// ("overvoltage not evaluated"); review.tolerantPins exempts. A pull-up
// with no logic receiver is not-applicable.
func (v *rvView) checkPullupVoltage() *rvCheck {
	c := v.check(RvPullupVoltage, "netlist", "pin-types")
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
			// a declared receiver counts whatever the part kind; the type
			// inference is for ICs only
			if pinIsInput(v, p) || v.comps[p.Ref].kind == pkIC && p.Type != "passive" && p.Type != "power_in" && p.Type != "power_out" {
				devs = append(devs, p)
			}
		}
		for _, pu := range pulls {
			rv, rok := v.voltage(pu.Rail)
			for _, p := range devs {
				c.Coverage.Candidates++
				key := p.Ref + "." + p.Num
				if src, ok := pinRole(v.in.Hints.Tolerant, p); ok {
					c.exempt(key, "tolerant of the pull-up rail declared", src)
					continue
				}
				base := ReviewFinding{Refs: []string{p.Ref, pu.Res}, Pins: []string{key}, netIDs: []string{id, pu.Rail}}
				if max, ok := v.pinMaxV(p); ok && rok {
					c.Coverage.Checked++
					c.use(v.in.Hints.VoltageSource[pu.Rail])
					if rv > max {
						f := base
						f.Severity, f.Message = "error", fmt.Sprintf("%s is pulled up to %s (%.2f V), above the %.2f V maximum of %s (%s)", v.name(id), v.name(pu.Rail), rv, max, key, p.Name)
						f.Fix = fmt.Sprintf("pull %s up to a rail within %s's rating, or add a level shifter", v.name(id), key)
						f.Evidence = []string{"pull-up " + pu.How, fmt.Sprintf("%s = %.2f V (%s)", v.name(pu.Rail), rv, v.in.Hints.VoltageSource[pu.Rail]), fmt.Sprintf("%s maximum %.2f V (intent review.pinMaxV)", key, max)}
						c.add(f)
					}
					continue
				}
				// a declared maximum is judged first; without one a pull-up to
				// the pin's own supply is structurally matched
				dom, how := v.pinDomain(p)
				if dom != "" && v.find(dom) == v.find(pu.Rail) {
					c.Coverage.Checked++
					continue
				}
				dv, dok := 0.0, false
				if dom != "" {
					dv, dok = v.voltage(dom)
				}
				if rok && dok {
					c.Coverage.Checked++
					if rv <= dv+0.3 {
						continue
					}
					f := base
					f.Severity, f.Certainty = "warning", "likely"
					f.Message = fmt.Sprintf("%s is pulled up to %s (%.2f V) but %s (%s) is in the %s domain (%.2f V)", v.name(id), v.name(pu.Rail), rv, key, p.Name, v.name(dom), dv)
					f.Fix = fmt.Sprintf("pull %s up to %s (or use a level shifter); if the pin is rated for %.2f V declare intent review.tolerantPins or review.pinMaxV", v.name(id), v.name(dom), rv)
					f.Evidence = []string{"pull-up " + pu.How, fmt.Sprintf("%s = %.2f V, domain %s = %.2f V (%s)", v.name(pu.Rail), rv, v.name(dom), dv, how)}
					f.Missing = []string{"absolute maximum of " + key + " (intent review.pinMaxV)"}
					c.add(f)
					continue
				}
				c.Coverage.Checked++
				var missing []string
				if dom == "" {
					missing = append(missing, "supply domain of "+key+" ("+p.Ref+" has several rails; intent review.pinDomains)")
				} else if !dok {
					missing = append(missing, "voltage of "+v.name(dom)+" (--intent / --sim)")
				}
				if !rok {
					missing = append(missing, "voltage of "+v.name(pu.Rail)+" (--intent / --sim)")
				}
				domName := "an unknown domain"
				if dom != "" {
					domName = "domain " + v.name(dom)
				}
				f := base
				f.Severity, f.Certainty = "warning", "likely"
				f.Message = fmt.Sprintf("%s is pulled up to %s but %s (%s) is in %s: overvoltage not evaluated", v.name(id), v.name(pu.Rail), key, p.Name, domName)
				f.Fix = "give rail voltages (--intent / --sim) and, for a multi-rail part, intent review.pinDomains; or pull up to the pin's own supply"
				f.Evidence = []string{"pull-up " + pu.How}
				f.Missing = missing
				c.add(f)
			}
		}
	}
	return c
}

// pinMaxV is a declared maximum voltage of pin p (intent review.pinMaxV).
func (v *rvView) pinMaxV(p *rvPin) (float64, bool) {
	m := v.in.Hints.PinMaxV
	if m == nil {
		return 0, false
	}
	if x, ok := m[p.Ref+"."+p.Num]; ok {
		return x, true
	}
	x, ok := m[p.Ref+"."+strings.ToUpper(p.Name)]
	return x, ok && p.Name != ""
}

var rvLEDDriverPin = regexp.MustCompile(`(?i)^(LED\w*|ISINK\w*|SINK\w*|K\d*|LEDK\w*|CC\d*|ILED\w*|OUT_?LED\w*|D\d+K)$`)

// checkLEDs traces each LED die's own current path — anode side and
// cathode side, polarity resolved throughout — and reports every die on
// its own (one die's cannot-check never hides another die's error). A side
// ends at a rail, at a driving pin, or at a common node shared only by LED
// terminals (common anode / cathode) that itself reaches a rail or one
// driver: the sibling LEDs are independent branches there. A die is
// limited when one side has its own exclusive series resistor (not 0 Ω)
// whose far side completes the path, or a declared current driver
// (review.currentDrivers). A resistor that also carries a sibling die's
// current — on the package's common terminal, or with a sibling die
// terminal of the same polarity on its node — or one shared by several
// LEDs before the branches is no individual limiter; antiparallel dies
// (opposite polarity on the node) conduct one at a time and keep it. The
// endpoints must forward-bias the die (anode end above cathode end, by
// rail voltage or rail polarity); a reversed die is an error, endpoints
// whose levels are unknown are cannot-check. A pin only named like a
// current driver (LED1, K, CC) is a likely warning. A branch inside a
// resistor path, a transistor, switch, open jumper or unknown polarity is
// unverified (cannot-check); a path that ends nowhere is an error; a
// connector is the off-board boundary (info). The aggregate current of a
// driver feeding several LEDs is not checked.
func (v *rvView) checkLEDs() *rvCheck {
	c := v.check(RvLEDLimiter, "netlist", "part kinds")
	for _, ref := range v.refs {
		led := v.comps[ref]
		if led.kind != pkLED {
			continue
		}
		dies, why := v.ledDies(ref)
		if len(dies) == 0 {
			c.Coverage.Candidates++
			c.unverified(fmt.Sprintf("%s: %s", ref, why))
			continue
		}
		users := map[*rvPin]int{} // how many dies use each terminal pin
		for _, d := range dies {
			users[d.A]++
			users[d.K]++
		}
		for _, d := range dies {
			c.Coverage.Candidates++
			v.ledEnd, v.ledEndPin = "", nil
			sa, wa := v.ledSide(ref, d.A.Net, "in", map[string]bool{ref: true})
			endA, pinA := v.ledEnd, v.ledEndPin
			v.ledEnd, v.ledEndPin = "", nil
			sk, wk := v.ledSide(ref, d.K.Net, "out", map[string]bool{ref: true})
			endK, pinK := v.ledEnd, v.ledEndPin
			if sa == "limit" {
				if s := v.dieShares(ref, d.A, users); s != "" {
					sa, wa = "shared", wa+" (also carries "+s+": no individual limiter)"
				}
			}
			if sk == "limit" {
				if s := v.dieShares(ref, d.K, users); s != "" {
					sk, wk = "shared", wk+" (also carries "+s+": no individual limiter)"
				}
			}
			st := ledDieState(sa, sk)
			label, name := "", "LED "+ref
			if len(dies) > 1 {
				label, name = "die "+d.Name+": ", "LED "+ref+" die "+d.Name
			}
			dwhy := fmt.Sprintf("%sanode %s: %s; cathode %s: %s", label, v.name(d.A.Net), wa, v.name(d.K.Net), wk)
			biasWhy, biasExtra := "", false
			if st != "ambiguous" && st != "open" {
				switch b, w := v.ledBias(endA, endK, pinA, pinK); b {
				case "reversed":
					st, biasWhy = "reversed", w
				case "likely":
					if st == "ok" {
						st = "bias?"
					} else {
						biasExtra = true // reported as its own warning beside the limiter result
					}
					biasWhy = w
				}
			}
			if biasWhy != "" {
				dwhy += "; bias: " + biasWhy
			}
			base := ReviewFinding{Refs: []string{ref}, Pins: []string{ref + "." + d.A.Num, ref + "." + d.K.Num}, netIDs: []string{d.A.Net, d.K.Net}, Evidence: []string{dwhy}}
			if biasExtra {
				f := base
				f.Rule = RvLEDLimiter
				f.Severity, f.Certainty = "warning", "likely"
				f.Message = fmt.Sprintf("%s: forward bias not established (%s)", name, biasWhy)
				f.Fix = "check the LED is forward-biased in operation; give rail voltages / driver domains (intent) to decide it"
				f.Missing = []string{"endpoint levels (rail voltages, intent review.pinDomains)"}
				c.add(f)
				// distinct identity from the die's limiter finding
				c.Findings[len(c.Findings)-1].Fingerprint = ReviewFingerprint(RvLEDLimiter, f.Refs, f.Pins, []string{d.A.Net, d.K.Net}, "bias")
			}
			switch st {
			case "ambiguous":
				c.unverified(fmt.Sprintf("%s: %s", name, dwhy))
			case "ok":
				c.Coverage.Checked++
			case "open":
				c.Coverage.Checked++
				f := base
				f.Severity, f.Message = "error", fmt.Sprintf("%s has no complete current path (%s)", name, dwhy)
				f.Fix = "complete the LED's path to its supply / driver, or remove the part"
				c.add(f)
			case "reversed":
				c.Coverage.Checked++
				f := base
				f.Severity, f.Message = "error", fmt.Sprintf("%s is reverse-biased: its anode end is not above its cathode end (%s)", name, dwhy)
				f.Fix = "turn the LED around (anode toward the supply / driver, cathode toward ground / sink)"
				c.add(f)
			case "bias?":
				c.Coverage.Checked++
				f := base
				f.Severity, f.Certainty = "warning", "likely"
				f.Message = fmt.Sprintf("%s: forward bias not established (%s)", name, biasWhy)
				f.Fix = "check the LED is forward-biased in operation; give rail voltages / driver domains (intent) to decide it"
				f.Missing = []string{"endpoint levels (rail voltages, intent review.pinDomains)"}
				c.add(f)
			case "drive":
				c.Coverage.Checked++
				f := base
				f.Severity, f.Certainty = "warning", "likely"
				f.Message = fmt.Sprintf("%s has a series resistor but its driver is not verified (%s)", name, dwhy)
				f.Fix = "check the pin drives the LED; declare it in intent review.pushPullOutputs"
				f.Missing = []string{"direction of the driving pin (intent review.pushPullOutputs)"}
				c.add(f)
			case "named":
				c.Coverage.Checked++
				f := base
				f.Severity, f.Certainty = "warning", "likely"
				f.Message = fmt.Sprintf("%s is driven by a pin named like a current driver, not verified (%s)", name, dwhy)
				f.Fix = "check the driver regulates the LED current, then declare the pin in intent review.currentDrivers; otherwise add a series resistor"
				f.Missing = []string{"current-driver role of the pin (intent review.currentDrivers)"}
				c.add(f)
			case "offboard":
				c.Coverage.Checked++
				f := base
				f.Severity, f.Message = "info", fmt.Sprintf("%s has no series limiter on this board; its path leaves through a connector (%s)", name, dwhy)
				f.Fix = "confirm the current is limited off-board, or add a series resistor"
				f.Evidence = append(f.Evidence, "connector pins are the off-board boundary")
				c.add(f)
			default:
				c.Coverage.Checked++
				f := base
				f.Severity, f.Message = "error", fmt.Sprintf("%s has no individual series resistor or current limiter in its own path (%s)", name, dwhy)
				f.Fix = fmt.Sprintf("add a series resistor to %s: R = (Vsupply − Vf) / If (a resistor shared by several LEDs or dies does not limit each)", ref)
				c.add(f)
			}
		}
	}
	return c
}

// dieShares names what else a die's limiter on terminal pin p carries:
// the package's common terminal (p used by several dies), or a sibling
// die terminal of the same polarity on p's node. "" = exclusive.
func (v *rvView) dieShares(ref string, p *rvPin, users map[*rvPin]int) string {
	if users[p] > 1 {
		return fmt.Sprintf("the common terminal %s.%s of %d dies", ref, p.Num, users[p])
	}
	role := v.diodeRole(p)
	for _, q := range v.groupPins(p.Net) {
		if q.Ref == ref && q != p && v.diodeRole(q) == role {
			return "sibling die terminal " + ref + "." + q.Num
		}
	}
	return ""
}

var rvNameVolt = regexp.MustCompile(`(?i)(?:^|[^0-9A-Z])[+-]?(\d+)(?:[.,](\d+))?V(\d*)(?:$|[^0-9A-Z])`)

// railLevel is a rail's level for the bias check. known: ground (0), a
// declared / simulated voltage, or a voltage the rail name spells (+12V,
// 5V0, 3V3, +3.3V, -5V); otherwise only the sign the name gives ("+…",
// VCC/VDD… positive, "-…" negative: known false, sign ±1); sign 0: nothing
// is known.
func (v *rvView) railLevel(n string) (x float64, known bool, sign int) {
	if v.groupIsGround(n) {
		return 0, true, 0
	}
	if x, ok := v.voltage(n); ok {
		return x, true, rvSign(x)
	}
	for _, g := range v.group(n) { // a 0 Ω-joined rail names the group too
		nm := v.name(g)
		if m := rvNameVolt.FindStringSubmatch(nm); m != nil {
			s := m[1]
			if m[2] != "" {
				s += "." + m[2]
			} else if m[3] != "" {
				s += "." + m[3]
			}
			var f float64
			if _, err := fmt.Sscanf(s, "%g", &f); err == nil {
				if strings.HasPrefix(nm, "-") || strings.Contains(nm, "-"+m[1]) {
					f = -f
				}
				return f, true, rvSign(f)
			}
		}
	}
	for _, g := range v.group(n) {
		nm := v.name(g)
		switch {
		case strings.HasPrefix(nm, "-"):
			return 0, false, -1
		case isPowerName(nm):
			return 0, false, 1
		}
	}
	return 0, false, 0
}

func rvSign(x float64) int {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}

// ledBias checks a die's endpoints forward-bias it. Ends are a rail
// identity, "pin" (a driver: endPin), "conn" or "". Result: ok | reversed
// (proven: error) | likely (direction not established: warning) with why.
//   - two rails: numeric levels (ground, voltages, name-spelled) must put
//     the anode end above the cathode end; signs alone order only rails of
//     different sign; otherwise likely.
//   - pin and ground: the pin must be on the anode side (source).
//   - pin and a positive rail: the pin on the cathode side sinks (ok); on
//     the anode side it is reversed unless the driver's domain exceeds the
//     rail — domain known and not higher: reversed; unknown: likely.
//   - pin and pin: the bias depends on the drivers (likely).
func (v *rvView) ledBias(a, k string, pa, pk *rvPin) (string, string) {
	rail := func(e string) bool { return e != "" && e != "pin" && e != "conn" }
	switch {
	case a == "conn" || k == "conn" || a == "" || k == "":
		return "ok", ""
	case rail(a) && rail(k):
		va, ka, sa := v.railLevel(a)
		vk, kk, sk := v.railLevel(k)
		switch {
		case ka && kk:
			if va <= vk {
				return "reversed", fmt.Sprintf("anode end %s (%.3g V) is not above cathode end %s (%.3g V)", v.name(a), va, v.name(k), vk)
			}
			return "ok", ""
		case (ka || sa != 0) && (kk || sk != 0) && rvSignOf(va, ka, sa) != rvSignOf(vk, kk, sk):
			if rvSignOf(va, ka, sa) < rvSignOf(vk, kk, sk) {
				return "reversed", fmt.Sprintf("anode end %s is below cathode end %s", v.name(a), v.name(k))
			}
			return "ok", ""
		}
		return "likely", fmt.Sprintf("the levels of %s and %s cannot be ordered (give rail voltages)", v.name(a), v.name(k))
	case a == "pin" && k == "pin":
		return "likely", "both ends are driver pins: the bias depends on the drivers"
	case a == "pin": // cathode on a rail
		vk, kk, sk := v.railLevel(k)
		if kk && vk == 0 || !kk && sk < 0 {
			return "ok", "" // the pin sources into ground / a negative rail
		}
		if pa != nil {
			if dom, _ := v.pinDomain(pa); dom != "" {
				if dv, ok, _ := v.railLevel(dom); ok && kk {
					if dv > vk {
						return "ok", ""
					}
					return "reversed", fmt.Sprintf("driver %s.%s (domain %s, %.3g V) cannot exceed cathode rail %s (%.3g V)", pa.Ref, pa.Num, v.name(dom), dv, v.name(k), vk)
				}
			}
		}
		return "likely", fmt.Sprintf("anode driven by a pin, cathode on %s: reversed unless the driver domain exceeds the rail", v.name(k))
	case k == "pin": // anode on a rail
		va, ka, sa := v.railLevel(a)
		if ka && va == 0 || !ka && sa < 0 {
			return "reversed", fmt.Sprintf("anode on %s, cathode on a driver pin: nothing drives the cathode below it", v.name(a))
		}
		return "ok", "" // the pin sinks
	}
	return "ok", ""
}

func rvSignOf(x float64, known bool, sign int) int {
	if known {
		return rvSign(x)
	}
	return sign
}

// ledDieState combines the two sides of one die ("shared": resolved, not
// an individual limiter).
func ledDieState(a, k string) string {
	for _, s := range []string{"ambiguous", "open"} {
		if a == s || k == s {
			return s
		}
	}
	switch {
	case a == "limit" || k == "limit":
		return "ok"
	case a == "drive" || k == "drive":
		return "drive"
	case a == "named" || k == "named":
		return "named"
	case a == "offboard" || k == "offboard":
		return "offboard"
	}
	return "none"
}

// rvDie is one LED die: its anode and cathode pins.
type rvDie struct {
	Name string
	A, K *rvPin
}

var rvDieRe = regexp.MustCompile(`(?i)^(?:([RGBYW])_?([AK])|([AK])_?([RGBYW]))$`)

// ledDies splits an LED into dies: a two-terminal LED is one; a
// multi-colour LED pairs anode and cathode per colour (RA/RK, A_G/K_G …),
// a colourless common anode / cathode serving every colour. No dies (and
// why): the terminals' polarity cannot be told.
func (v *rvView) ledDies(ref string) ([]rvDie, string) {
	pins := v.comps[ref].pins
	var as, ks []*rvPin
	byColour := map[string][2]*rvPin{}
	var colours []string
	for _, p := range pins {
		if netname.IsUnconnected(p.Net) {
			continue
		}
		role := v.diodeRole(p)
		col := ""
		for _, t := range []string{p.Name, p.Num} {
			if m := rvDieRe.FindStringSubmatch(t); m != nil {
				col = strings.ToUpper(m[1] + m[4])
				break
			}
		}
		switch {
		case role == "":
			return nil, fmt.Sprintf("anode/cathode of %s.%s (%s) cannot be told", ref, p.Num, p.Name)
		case col != "":
			e, seen := byColour[col]
			if !seen {
				colours = append(colours, col)
			}
			if role == "A" {
				e[0] = p
			} else {
				e[1] = p
			}
			byColour[col] = e
		case role == "A":
			as = append(as, p)
		default:
			ks = append(ks, p)
		}
	}
	if len(colours) == 0 {
		if len(as) == 1 && len(ks) == 1 {
			return []rvDie{{"", as[0], ks[0]}}, ""
		}
		return nil, fmt.Sprintf("%s: %d anode and %d cathode terminals without die names", ref, len(as), len(ks))
	}
	sort.Strings(colours)
	var out []rvDie
	for _, col := range colours {
		e := byColour[col]
		if e[0] == nil && len(as) == 1 {
			e[0] = as[0]
		}
		if e[1] == nil && len(ks) == 1 {
			e[1] = ks[0]
		}
		if e[0] == nil || e[1] == nil {
			return nil, fmt.Sprintf("%s: die %s has no matching anode/cathode", ref, col)
		}
		out = append(out, rvDie{col, e[0], e[1]})
	}
	return out, ""
}

func sbContainsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// ledSide follows the current of an LED die from its terminal net n in
// direction dir ("in" on the anode side, "out" on the cathode side):
// rail | limit | shared (a resistor shared by several LEDs: resolved, no
// individual limit) | named | pin (an IC pin drives it directly) | drive
// (complete path, driver direction unknown) | offboard | ambiguous | open,
// and why.
func (v *rvView) ledSide(ref, n, dir string, seen map[string]bool) (string, string) {
	if v.isSupply(n) || v.groupIsGround(n) {
		v.ledEnd = n
		return "rail", "on rail " + v.name(n)
	}
	p, st, why := v.ledNext(ref, n)
	if p == nil {
		if st == "ambiguous" && !strings.HasPrefix(why, "through jumper") {
			return v.ledCommon(ref, n, dir)
		}
		return st, why
	}
	return v.ledElement(ref, p, dir, seen)
}

// ledElement continues an LED side at element pin p (the one thing on
// the node besides the LED).
func (v *rvView) ledElement(ref string, p *rvPin, dir string, seen map[string]bool) (string, string) {
	comp := v.comps[p.Ref]
	switch comp.kind {
	case pkResistor, pkResArray:
		far := v.farPin(p)
		if far == nil {
			return "open", "series " + p.Ref + " has no far terminal"
		}
		fs, fwhy := v.pathOn(p.Ref, far.Net, dir, 0)
		lim := "series " + p.Ref + " (" + comp.c.Value + "), then " + fwhy
		switch fs {
		case "ok":
			return "limit", lim
		case "unknown":
			return "drive", lim
		}
		return fs, lim
	case pkLED:
		if seen[p.Ref] {
			return "ambiguous", "LED loop through " + p.Ref
		}
		seen[p.Ref] = true
		ms, u := v.dcMates(p, dir)
		switch {
		case u:
			return "ambiguous", "series LED " + p.Ref + ": polarity cannot be told"
		case len(ms) == 0:
			return "open", "series LED " + p.Ref + " is reverse to the current"
		case len(ms) > 1:
			return "ambiguous", "series LED " + p.Ref + " has several far terminals"
		}
		st, why := v.ledSide(p.Ref, ms[0].Net, dir, seen)
		return st, "in series with " + p.Ref + ", " + why
	case pkConnector:
		v.ledEnd = "conn"
		return "offboard", "leaves through " + p.Ref + "." + p.Num
	case pkIC:
		v.ledEnd, v.ledEndPin = "pin", p
		if src, ok := pinRole(v.in.Hints.CurrentDriver, p); ok {
			return "limit", p.Ref + "." + p.Num + " (" + p.Name + ", current driver: " + src + ")"
		}
		if rvLEDDriverPin.MatchString(p.Name) {
			return "named", p.Ref + "." + p.Num + " (" + p.Name + ", named like a current driver)"
		}
		switch p.Type {
		case "output", "power_out", "bidirectional", "tri_state", "open_collector", "open_emitter":
			return "pin", "driven directly by " + p.Ref + "." + p.Num + " (" + p.Name + ")"
		}
		return "pin", "driven directly by " + p.Ref + "." + p.Num + " (" + p.Name + ", " + p.Type + ")"
	}
	return "ambiguous", "through " + p.Ref + " (" + comp.kind + ")"
}

// ledCommon resolves a node that branches: when everything on it besides
// the LED is other LEDs' terminals plus exactly one element — a rail is
// handled before — that element ends the side: a driver pin (pin), a
// connector, or a resistor shared by the LEDs (shared: no individual
// limit) whose far side completes. Anything else stays ambiguous.
func (v *rvView) ledCommon(ref, n, dir string) (string, string) {
	var leds, rest []*rvPin
	for _, p := range v.groupPins(n) {
		if p.Ref == ref || v.isJoin(p.Ref) {
			continue
		}
		if v.comps[p.Ref].kind == pkLED {
			if v.diodeRole(p) == "" {
				return "ambiguous", "common node " + v.name(n) + ": polarity of " + p.Ref + "." + p.Num + " cannot be told"
			}
			leds = append(leds, p)
		} else {
			rest = append(rest, p)
		}
	}
	branch := "node " + v.name(n) + " branches to " + strings.Join(rvPinKeys(append(append([]*rvPin(nil), rest...), leds...)), ", ")
	if len(leds) == 0 || len(rest) != 1 {
		return "ambiguous", branch
	}
	// only a sibling that conducts in the same direction from this node
	// (same terminal role) shares the element at the same time; an
	// antiparallel sibling conducts when this die does not
	want := map[string]string{"in": "A", "out": "K"}[dir]
	same := 0
	for _, q := range leds {
		if v.diodeRole(q) == want {
			same++
		}
	}
	p := rest[0]
	common := fmt.Sprintf("common node %s (%d sibling LED terminal(s), %d conducting with this die) ", v.name(n), len(leds), same)
	if same == 0 {
		st, why := v.ledElement(ref, p, dir, map[string]bool{ref: true})
		return st, common + why
	}
	switch v.comps[p.Ref].kind {
	case pkResistor, pkResArray:
		st, why := v.ledElement(ref, p, dir, map[string]bool{ref: true})
		if st == "limit" {
			return "shared", common + "through a shared resistor: " + why + " (not an individual limiter)"
		}
		return st, common + why
	case pkIC, pkConnector:
		st, why := v.ledElement(ref, p, dir, map[string]bool{ref: true})
		if st == "limit" || st == "named" {
			return "shared", common + why + " (a driver shared by several LEDs: aggregate current not checked)"
		}
		return st, common + why
	}
	return "ambiguous", branch
}

// ledNext is the one element net n leads on to (besides ref and joins),
// or the state that ends the trace there (open / ambiguous).
func (v *rvView) ledNext(ref, n string) (*rvPin, string, string) {
	for _, g := range v.group(n) {
		if j := v.jumpers[g]; len(j) > 0 {
			return nil, "ambiguous", "through jumper " + strings.Join(j, ",") + " of unknown state"
		}
	}
	var others []*rvPin
	for _, p := range v.groupPins(n) {
		if p.Ref == ref || v.isJoin(p.Ref) {
			continue
		}
		others = append(others, p)
	}
	switch len(others) {
	case 0:
		return nil, "open", "node " + v.name(n) + " goes nowhere"
	case 1:
		return others[0], "", ""
	}
	return nil, "ambiguous", "node " + v.name(n) + " branches to " + strings.Join(rvPinKeys(others), ", ")
}

// farPin is the other terminal of a two-terminal part (resistor array:
// the paired pin), nil when there is not exactly one.
func (v *rvView) farPin(p *rvPin) *rvPin {
	c := v.comps[p.Ref]
	var out []*rvPin
	if c.kind == pkResArray {
		mate := rvArrayMate(c, p)
		for _, q := range c.pins {
			if mate[q.Num] {
				out = append(out, q)
			}
		}
	} else {
		for _, q := range c.pins {
			if q != p && q.Net != p.Net {
				out = append(out, q)
			}
		}
	}
	if len(out) != 1 || netname.IsUnconnected(out[0].Net) {
		return nil
	}
	return out[0]
}

// pathOn checks the path beyond a series limiter completes, following the
// LED current in direction dir (dcMates: a reverse diode/LED or a clamp
// does not conduct): a rail, a driving pin or a connector (ok), a further
// series element (followed), nothing or a non-conducting part (open), a
// branch / unknown element / unknown polarity (ambiguous), an IC pin of
// unknown direction (unknown).
func (v *rvView) pathOn(from, n, dir string, depth int) (string, string) {
	if v.isSupply(n) || v.groupIsGround(n) {
		v.ledEnd = n
		return "ok", "rail " + v.name(n)
	}
	if depth > 6 {
		return "ambiguous", "path too long"
	}
	p, st, why := v.ledNext(from, n)
	if p == nil {
		return st, why
	}
	comp := v.comps[p.Ref]
	switch comp.kind {
	case pkResistor, pkResArray, pkInductor, pkLED, pkDiode:
		ms, u := v.dcMates(p, dir)
		switch {
		case u:
			return "ambiguous", p.Ref + ": polarity cannot be told"
		case len(ms) == 0:
			return "open", p.Ref + " does not conduct the LED current (reverse diode or clamp)"
		case len(ms) > 1:
			return "ambiguous", p.Ref + " has several far terminals"
		}
		s, w := v.pathOn(p.Ref, ms[0].Net, dir, depth+1)
		return s, p.Ref + " → " + w
	case pkConnector:
		v.ledEnd = "conn"
		return "ok", "connector " + p.Ref + "." + p.Num
	case pkIC:
		v.ledEnd, v.ledEndPin = "pin", p
		switch p.Type {
		case "output", "power_out", "bidirectional", "tri_state", "open_collector", "open_emitter":
			return "ok", "driver pin " + p.Ref + "." + p.Num + " (" + p.Name + ")"
		}
		// a passive / unspecified IC pin (an FPGA I/O typed passive): the
		// path is complete but whether the pin drives it is not known
		return "unknown", "IC pin " + p.Ref + "." + p.Num + " (" + p.Name + ", " + p.Type + ") of unknown direction"
	}
	return "ambiguous", "through " + p.Ref + "." + p.Num + " (" + comp.kind + ", " + p.Type + ")"
}

// checkFloating: receivers (pins typed input, or declared in intent
// review.inputPins — a passive/unspecified pin is no receiver) that
// nothing drives (driveOf). Driven → pass; a connector → info; unresolved
// topology (switch, jumper, unknown diode polarity) → unverified; only
// pins of unknown direction → likely warning (drive not verified). No
// driver: a declared internal pull exempts; explicit evidence of no
// internal pull (review.internalPullsComplete / noInternalPulls) makes it
// a proven error, otherwise a likely warning. Single-pin nets are the
// single-pin check's.
func (v *rvView) checkFloating() *rvCheck {
	c := v.check(RvFloatingInput, "netlist", "pin-types")
	done := map[string]bool{}
	for _, id := range v.netIDs {
		root := v.find(id)
		if done[root] || netname.IsUnconnected(id) {
			continue
		}
		done[root] = true
		var pins, inputs []*rvPin
		for _, p := range v.groupPins(id) {
			if v.isJoin(p.Ref) {
				continue
			}
			pins = append(pins, p)
			if (p.Type == "input" || pinIsInput(v, p)) && !p.NC {
				inputs = append(inputs, p)
			}
		}
		if len(inputs) == 0 || len(pins) < 2 || v.isSupply(id) || v.groupIsGround(id) {
			continue
		}
		c.Coverage.Candidates++
		name := v.name(id)
		base := ReviewFinding{Refs: rvRefs(inputs), Pins: rvPinKeys(inputs), netIDs: v.group(id)}
		d := v.driveOf(id)
		switch d.State {
		case "driven":
			c.Coverage.Checked++
			continue
		case "offboard":
			c.Coverage.Checked++
			f := base
			f.Severity, f.Message = "info", fmt.Sprintf("input net %s has no driver on this board; it leaves through %s", name, d.How)
			f.Fix, f.Evidence = "confirm it is driven off-board, or bias it with a resistor", []string{"connector pins are the off-board boundary"}
			c.add(f)
			continue
		case "ambiguous":
			c.unverified(fmt.Sprintf("%s: inputs %s; the path runs through %s", name, strings.Join(rvPinKeys(inputs), ", "), d.How))
			continue
		case "unknown":
			c.Coverage.Checked++
			f := base
			f.Severity, f.Certainty = "warning", "likely"
			f.Message = fmt.Sprintf("net %s: drive of inputs %s not verified — only pins of unknown direction (%s)", name, strings.Join(rvPinKeys(inputs), ", "), d.How)
			f.Fix = "make sure one of them drives the net; declare it in intent review.pushPullOutputs"
			f.Evidence = []string{"no output / power_out pin or rail traced"}
			f.Missing = []string{"direction of " + d.How + " (intent review.pushPullOutputs)"}
			c.add(f)
			continue
		}
		src, ok := "", false
		for _, p := range inputs {
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
		var unknown, absent []string
		for _, p := range inputs {
			if s, ok := v.pullAbsent(p); ok {
				absent = append(absent, s)
			} else {
				unknown = append(unknown, p.Ref+"."+p.Num)
			}
		}
		c.Coverage.Checked++
		f := base
		f.Fix = fmt.Sprintf("drive %s from an output, tie it to a rail / ground through a resistor, or declare the internal pull in intent review", name)
		f.Evidence = []string{"no driver pin or rail through resistors / inductors / forward-biased diodes (capacitors do not drive DC)"}
		if len(unknown) > 0 {
			f.Severity, f.Certainty = "warning", "likely"
			f.Message = fmt.Sprintf("net %s: nothing drives inputs %s (internal pulls unknown)", name, strings.Join(rvPinKeys(inputs), ", "))
			f.Missing = []string{"internal-pull evidence for " + strings.Join(unknown, ", ") + " (intent review.internalPullups / internalPulldowns / noInternalPulls / internalPullsComplete)"}
			c.add(f)
			continue
		}
		c.use("intent")
		f.Severity = "error"
		f.Message = fmt.Sprintf("net %s: nothing drives inputs %s", name, strings.Join(rvPinKeys(inputs), ", "))
		f.Evidence = append(f.Evidence, "no internal pull: "+strings.Join(rvUniq(absent), ", "))
		c.add(f)
	}
	return c
}

// checkOutputs: two or more push-pull outputs (output, power_out) on one
// net contend. Only one electrical pin collapses: the same pin number of a
// part (drawn on several units), or pins the symbol stacks at one position
// (ReviewInput.Stacked) or intent review.commonPins declares common. Two
// pins with the same name are not enough (both outputs of a dual op-amp
// are "OUT"). Virtual symbols are not parts.
func (v *rvView) checkOutputs() *rvCheck {
	c := v.check(RvOutputConflict, "netlist", "pin-types")
	common := map[string]string{} // REF.PIN → group key
	for _, r := range v.refs {
		comp := v.comps[r]
		for _, g := range v.in.Stacked[comp.c.Lib+":"+comp.c.Part] {
			for _, n := range g {
				common[r+"."+n] = r + "#" + strings.Join(g, "+")
			}
		}
	}
	for _, g := range v.in.Hints.CommonPins {
		var nums []string
		for _, k := range g {
			if n, ok := v.resolvePin(k, "intent review.commonPins"); ok {
				nums = append(nums, n)
			}
		}
		for _, n := range nums {
			common[n] = "intent#" + strings.Join(nums, "+")
		}
	}
	done := map[string]bool{}
	for _, id := range v.netIDs {
		root := v.find(id)
		if done[root] {
			continue
		}
		done[root] = true
		byKey := map[string][]*rvPin{}
		var keys []string
		for _, p := range v.groupPins(id) {
			if p.Type != "output" && p.Type != "power_out" {
				// a GPIO intent declares push-pull drives like an output
				if _, pp := pinRole(v.in.Hints.PushPull, p); !pp || p.Type != "bidirectional" && p.Type != "tri_state" {
					continue
				}
			}
			k := p.Ref + "#" + p.Num
			if g, ok := common[p.Ref+"."+p.Num]; ok {
				k = g
			}
			if _, seen := byKey[k]; !seen {
				keys = append(keys, k)
			}
			byKey[k] = append(byKey[k], p)
		}
		if len(keys) == 0 {
			continue
		}
		c.Coverage.Candidates++
		c.Coverage.Checked++
		if len(keys) < 2 {
			continue
		}
		sort.Strings(keys)
		var ps []*rvPin
		var desc []string
		for _, k := range keys {
			ps = append(ps, byKey[k]...)
			for _, p := range byKey[k] {
				desc = append(desc, p.Ref+"."+p.Num+" ("+p.Name+") "+p.Type)
			}
		}
		c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("net %s has %d driving outputs: %s", v.name(id), len(keys), strings.Join(desc, ", ")),
			Refs: rvRefs(ps), Pins: rvPinKeys(ps), netIDs: v.group(id),
			Fix:      "keep one driver per net: split the net, or make the extra drivers open-drain / tri-state, or OR them through diodes; pins the symbol does not stack but that are one electrical pin go in intent review.commonPins",
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

// supplyEvidence tells why net n is a supply a power pin may rely on: a
// power_out pin of a real part (a regulator output), a global power-named
// rail (a power symbol), an intent power rail or voltage, or a connector
// (an off-board supply) — on n or behind series inductors / ferrites.
// "" = none: never accepted on net population alone.
func (v *rvView) supplyEvidence(n string) string {
	seen := map[string]bool{}
	q := []string{n}
	for depth := 0; len(q) > 0 && depth < 4; depth++ {
		var next []string
		for _, net := range q {
			r := v.find(net)
			if seen[r] || netname.IsUnconnected(net) || v.groupIsGround(net) {
				continue
			}
			seen[r] = true
			for _, g := range v.group(net) {
				if v.in.Hints.NetRole[g] == "power" {
					return "intent power rail " + v.name(g)
				}
				if _, ok := v.in.Hints.NetVoltage[g]; ok {
					return "rail " + v.name(g) + " with a known voltage"
				}
				if nm := v.name(g); !strings.HasPrefix(g, "/") && !strings.HasPrefix(g, "Net-(") && (isPowerName(nm) || strings.HasPrefix(nm, "-")) {
					return "power rail " + v.name(g) + " (power symbol)"
				}
			}
			for _, p := range v.groupPins(net) {
				switch k := v.comps[p.Ref].kind; {
				case p.Type == "power_out" && k != pkConnector:
					return "power output " + p.Ref + "." + p.Num + " (" + p.Name + ")"
				case k == pkConnector:
					return "connector " + p.Ref + "." + p.Num + " (off-board supply)"
				case k == pkInductor:
					if f := v.farPin(p); f != nil {
						next = append(next, f.Net)
					}
				}
			}
		}
		q = next
	}
	return ""
}

var rvGndPinRe = regexp.MustCompile(`(?i)^(\w*GND\w*|VSS\w*|EP|EPAD|PAD|THERMAL\w*|0V)$`)

// rvNegSupRe: a negative-supply terminal (V−, VEE, −VS): on ground in a
// single-supply design, on a negative rail in a split one.
var rvNegSupRe = regexp.MustCompile(`(?i)^(V-|VEE\w*|-VS\w*|VS-|V_?NEG\w*)$`)

// checkICPower checks every power pin of a part on its own. Each
// designated ground pin (GND/VSS/EP-named, whatever its type) must be on
// ground, a no-connect flag included. Each other power input must sit on
// a supply with positive evidence (supplyEvidence: a regulator / power_out
// source, a power-symbol rail, an intent rail, a connector) — never on net
// population alone: unconnected, no-connect or alone on its net is an
// error, no identified source a likely warning, and a non-ground power pin
// on ground an error unless intent review.groundedPowerPins declares it (a
// V−/VEE pin on ground is a single-supply negative terminal). A part with
// supply terminals only (an op-amp across two rails) has no ground pin to
// check. A connector with power_in pins gets its power inputs checked.
func (v *rvView) checkICPower() *rvCheck {
	c := v.check(RvICPowerGround, "netlist", "pin-types")
	parts := append([]string(nil), v.icParts()...)
	for _, r := range v.refs {
		if v.comps[r].kind == pkConnector && rvHasPowerIn(v.comps[r].c) {
			parts = append(parts, r)
		}
	}
	for _, r := range parts {
		comp := v.comps[r]
		conn := comp.kind == pkConnector
		c.Coverage.Candidates++
		c.Coverage.Checked++
		ground := false
		rails := map[string]string{}
		var gndPins, badGnd, badSup, onGnd, noSrc, negOnGnd []string
		for _, p := range comp.pins {
			key := p.Ref + "." + p.Num
			if rvGndPinRe.MatchString(p.Name) && p.Type != "output" && p.Type != "power_out" {
				if conn {
					continue // a connector's ground pins are its own business
				}
				gndPins = append(gndPins, key)
				if v.groupIsGround(p.Net) {
					ground = true
				} else { // a no-connect flag does not excuse a ground pin
					badGnd = append(badGnd, key)
				}
				continue
			}
			if p.Type != "power_in" {
				continue
			}
			label := key + " (" + p.Name + ")"
			switch {
			case p.NC || netname.IsUnconnected(p.Net):
				badSup = append(badSup, label)
			case v.groupIsGround(p.Net):
				if rvNegSupRe.MatchString(p.Name) {
					negOnGnd = append(negOnGnd, label) // decided below
					continue
				}
				if _, ok := pinRole(v.in.Hints.GroundedPin, p); ok {
					c.exempt(key, "power pin on ground by design", "intent review.groundedPowerPins")
					continue
				}
				onGnd = append(onGnd, label)
			default:
				ev := v.supplyEvidence(p.Net)
				if ev == "" {
					if len(v.groupPins(p.Net)) < 2 {
						badSup = append(badSup, label)
					} else {
						noSrc = append(noSrc, label+" on "+v.name(p.Net))
					}
					continue
				}
				rails[v.find(p.Net)] = p.Net
			}
		}
		// V-/VEE on ground is a single-supply negative terminal only when
		// another power pin of the part reaches a positive rail
		if len(negOnGnd) > 0 {
			pos := false
			for _, n := range rails {
				x, known, sign := v.railLevel(n)
				pos = pos || known && x > 0 || !known && sign > 0
			}
			if pos {
				ground = true
			} else {
				onGnd = append(onGnd, negOnGnd...)
			}
		}
		pinsOf := func(xs []string) []string {
			var out []string
			for _, x := range xs {
				k, _, _ := strings.Cut(x, " ")
				out = append(out, k)
			}
			return out
		}
		if len(badSup) > 0 {
			c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("%s power input(s) %s not connected to a supply", r, strings.Join(badSup, ", ")), Refs: []string{r}, Pins: pinsOf(badSup),
				Fix:      "connect every power pin of " + r + " to its rail (a no-connect flag on a power input is a fault unless the datasheet says so — then waive it)",
				Evidence: []string{"power_in pins unconnected, no-connect or alone on their net: " + strings.Join(badSup, ", ")}})
		}
		if len(onGnd) > 0 {
			c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("%s power input(s) %s sit on ground", r, strings.Join(onGnd, ", ")), Refs: []string{r}, Pins: pinsOf(onGnd),
				Fix:      "connect the pin to its supply rail, or declare it in intent review.groundedPowerPins when the datasheet ties it to ground",
				Evidence: []string{"non-ground power_in pins on a ground net: " + strings.Join(onGnd, ", ")}})
		}
		for _, x := range noSrc {
			k, _, _ := strings.Cut(x, " ")
			c.add(ReviewFinding{Severity: "warning", Certainty: "likely", Message: fmt.Sprintf("%s power input %s: no supply source identified", r, x), Refs: []string{r}, Pins: []string{k},
				Fix:      "feed the net from a regulator / power symbol / connector, or declare the rail in intent",
				Evidence: []string{"no power_out source, power-symbol rail, intent rail or connector on the net (net population alone is no supply)"},
				Missing:  []string{"supply source of the net (intent rails / a power symbol)"}})
		}
		if conn {
			continue
		}
		switch {
		case len(badGnd) > 0:
			c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("%s ground pin(s) %s are not on a ground net", r, strings.Join(badGnd, ", ")), Refs: []string{r}, Pins: badGnd,
				Fix: "connect " + r + "'s ground / exposed pad pins to GND", Evidence: []string{"designated ground pins (GND/VSS/EP-named): " + strings.Join(gndPins, ", "), "not on ground: " + strings.Join(badGnd, ", ")}})
		case ground:
		case len(negOnGnd) > 0: // V−/VEE on ground already reported above
		case comp.fieldRole("no-ground"):
			c.exempt(r, "no ground pin by design", "component field ReviewRole")
		case len(rails) >= 2:
			c.exempt(r, fmt.Sprintf("supply terminals only, between %s (no ground pin by design)", strings.Join(rvRailNames(v, rails), " and ")), "pin types (netlist)")
		default:
			c.add(ReviewFinding{Severity: "warning", Certainty: "likely", Message: fmt.Sprintf("%s has no recognised ground pin", r), Refs: []string{r},
				Fix:      "connect its ground pin, or name it GND / type it power_in (ReviewRole=no-ground on a part that has none)",
				Evidence: []string{"no GND/VSS/EP-named pin on ground, one supply rail"}, Missing: []string{"ground pin of " + r}})
		}
	}
	return c
}

func rvRailNames(v *rvView, m map[string]string) []string {
	var out []string
	for _, n := range m {
		out = append(out, v.name(n))
	}
	sort.Strings(out)
	return out
}

func rvFirst(m map[string]string) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	if len(ks) == 0 {
		return ""
	}
	return m[ks[0]]
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

// chargePump reports a part positively identified as a charge pump
// (intent review.chargePumps, or flying-capacitor pins C1+/C1−/CAP+…):
// its output capacitor may return to another rail.
func (v *rvView) chargePump(r string) (string, bool) {
	if s, ok := v.in.Hints.ChargePump[r]; ok {
		return s, true
	}
	n := 0
	for _, p := range v.comps[r].pins {
		if rvFlyPinRe.MatchString(p.Name) {
			n++
		}
	}
	if n >= 2 {
		return "flying-capacitor pins of " + r, true
	}
	return "", false
}

var rvFlyPinRe = regexp.MustCompile(`(?i)^(C\d?[+-]|CAP\d?[+-]|CP[+-]|CF[+-]|C\d?[PN]|FLY\w*)$`)

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
	loads := map[string][]string{} // rail group root → power pins (REF.PIN)
	for _, r := range v.icParts() {
		for _, p := range v.comps[r].pins {
			if p.Type == "power_in" && !v.groupIsGround(p.Net) && !netname.IsUnconnected(p.Net) {
				loads[v.find(p.Net)] = append(loads[v.find(p.Net)], r+"."+p.Num)
			}
		}
	}
	for _, r := range v.icParts() {
		for _, p := range v.comps[r].pins {
			if p.Type != "power_in" || v.groupIsGround(p.Net) || netname.IsUnconnected(p.Net) {
				continue
			}
			root := v.find(p.Net)
			key := r + "." + p.Num
			c.Coverage.Candidates++
			subj := key + " " + v.name(p.Net)
			if why, src, ok := v.railExempt(p.Net, r); ok {
				c.exempt(subj, why, src)
				if strings.Contains(src, "intent") {
					c.use("intent")
				}
				continue
			}
			c.Coverage.Checked++
			caps, other := v.capsTo(p.Net)
			if len(caps) == 0 {
				ev := []string{fmt.Sprintf("power_in pin %s (%s) on %s", key, p.Name, v.name(p.Net)), "no capacitor from " + v.name(p.Net) + " to ground"}
				if len(other) > 0 {
					ev = append(ev, "capacitors to other nets: "+strings.Join(other, ", "))
				}
				c.add(ReviewFinding{Severity: "error", Message: fmt.Sprintf("%s supply pin %s (%s) on %s has no decoupling capacitor to ground", r, key, p.Name, v.name(p.Net)),
					Refs: []string{r}, Pins: []string{key}, netIDs: []string{p.Net},
					Fix:      fmt.Sprintf("add a 100 nF ceramic from %s to GND at %s", v.name(p.Net), key),
					Evidence: ev})
				continue
			}
			// the capacitor set of this pin also serves other power pins —
			// of other parts, or of this package (VDD1/VDD2 on one cap)
			var others []string
			for _, o := range loads[root] {
				if o != key {
					others = append(others, o)
				}
			}
			if len(others) > 0 {
				shown := others
				if len(shown) > 6 {
					shown = append(append([]string(nil), shown[:6]...), fmt.Sprintf("… %d more", len(others)-6))
				}
				c.add(ReviewFinding{Severity: "warning", Certainty: "likely", Message: fmt.Sprintf("%s supply pin %s on %s shares its decoupling: capacitor(s) %s also serve %s (%d power pins on the rail)", r, key, v.name(p.Net), strings.Join(caps, ", "), strings.Join(shown, ", "), len(loads[root])),
					Refs: []string{r}, Pins: []string{key}, netIDs: []string{p.Net},
					Fix:      fmt.Sprintf("give %s its own 100 nF on %s (shared coverage is not local decoupling)", key, v.name(p.Net)),
					Evidence: []string{"capacitor set on " + v.name(p.Net) + ": " + strings.Join(caps, ", ")}})
			}
		}
	}
	return c
}

var rvOutPinRe = regexp.MustCompile(`(?i)^(V?OUT\d*|VO\d*|OUTPUT)$`)
var rvInPinRe = regexp.MustCompile(`(?i)^(V?IN\d*|VI|INPUT|PVIN\d*|VCC|VDD)$`)

// regulators are the parts that may be voltage regulators and the
// evidence: an intent block (ldo/buck/boost: proven) or only a pin typed
// power_out (a load switch, a power mux, a charge pump look the same: at
// most likely).
func (v *rvView) regulators() map[string]rvReg {
	out := map[string]rvReg{}
	for r, why := range v.in.Hints.Regulators {
		if v.comps[r] != nil {
			out[r] = rvReg{why, true}
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
					out[r] = rvReg{"pin " + p.Num + " (" + p.Name + ") is power_out (regulator role not proven)", false}
				}
				break
			}
		}
	}
	return out
}

// regFunc is the intent block function of a regulator (buck, boost, ldo;
// "" unknown).
func (v *rvView) regFunc(r string) string {
	w := v.in.Hints.Regulators[r]
	if i := strings.LastIndex(w, " "); i >= 0 && strings.HasPrefix(w, "intent block") {
		return w[i+1:]
	}
	return ""
}

// rvReg is why a part is taken for a regulator; Proven: intent says so.
type rvReg struct {
	Why    string
	Proven bool
}

var rvSwPinRe = regexp.MustCompile(`(?i)^(SW\d*|LX\d*|PH\d*|SWN?\d*)$`)

// regPins are a regulator's output pins (power_out or named VOUT/OUT, and
// switch-node pins SW/LX/PH) and input pins (power_in or named VIN/IN) on
// non-ground nets; byType tells the outputs came from pin types.
func (v *rvView) regPins(r string) (outs, ins []*rvPin, byType bool) {
	c := v.comps[r]
	for _, p := range c.pins {
		if (p.Type == "power_out" || rvSwPinRe.MatchString(p.Name)) && !v.groupIsGround(p.Net) {
			outs = append(outs, p)
			byType = byType || p.Type == "power_out"
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

// outputNode is where a regulator output pin's capacitor belongs: the pin's
// own net, or for a switch-node pin (SW/LX/PH) the output the topology
// leads to — a buck: SW → the one inductor → output; a boost: SW → the
// rectifier diode (anode on SW) → output. The function comes from the
// intent block (fn); without it, or when the trace does not resolve, or it
// lands on the regulator's own input, the output is unresolved (ok false)
// and the input capacitor is never taken for the output one.
func (v *rvView) outputNode(r string, p *rvPin, fn string, ins []*rvPin) (string, string, bool) {
	if !rvSwPinRe.MatchString(p.Name) {
		return p.Net, "", true
	}
	input := map[string]bool{}
	for _, q := range ins {
		input[v.find(q.Net)] = true
	}
	var far *rvPin
	var via string
	switch fn {
	case "buck":
		var ind []*rvPin
		for _, q := range v.groupPins(p.Net) {
			if v.comps[q.Ref].kind == pkInductor {
				ind = append(ind, q)
			}
		}
		if len(ind) != 1 {
			return "", "buck switch node " + v.name(p.Net) + ": not exactly one inductor", false
		}
		far, via = v.farPin(ind[0]), ind[0].Ref
	case "boost":
		var d []*rvPin
		for _, q := range v.groupPins(p.Net) {
			if k := v.comps[q.Ref].kind; (k == pkDiode || k == pkLED) && v.diodeRole(q) == "A" && !rvClampRe.MatchString(v.comps[q.Ref].c.Part) {
				d = append(d, q)
			}
		}
		if len(d) != 1 {
			return "", "boost switch node " + v.name(p.Net) + ": no single rectifier diode (anode on SW; a synchronous FET is not traced)", false
		}
		ms, _ := v.dcMates(d[0], "out")
		if len(ms) == 1 {
			far = ms[0]
		}
		via = d[0].Ref
	default:
		return "", "switch pin " + r + "." + p.Num + " of a regulator whose topology is not declared (intent buck/boost block)", false
	}
	if far == nil || netname.IsUnconnected(far.Net) {
		return "", fn + " switch node " + v.name(p.Net) + " → " + via + ": no far terminal", false
	}
	if input[v.find(far.Net)] {
		return "", fn + " switch node " + v.name(p.Net) + " → " + via + " → " + v.name(far.Net) + " is the regulator's input, not its output", false
	}
	return far.Net, "switch node " + v.name(p.Net) + " → " + via + " → " + v.name(far.Net), true
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

// checkReg: a regulator's output (input) needs a capacitor to ground. The
// output of a switch-node pin is traced through its inductor — a capacitor
// is never asked for on SW itself. A proven error needs regulator
// evidence (intent ldo/buck/boost block); a part known only by a power_out
// pin (load switch, power mux …) gets at most a likely warning.
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
		reg := regs[r]
		outs, ins, byType := v.regPins(r)
		if reg.Proven {
			c.use("intent")
		}
		pins := ins
		if output {
			pins = outs
		}
		c.Coverage.Candidates++
		if len(pins) == 0 {
			c.add(ReviewFinding{Severity: "warning", Certainty: "likely", Message: fmt.Sprintf("regulator %s (%s): no %s pin identified, %s capacitor not evaluated", r, reg.Why, side, side), Refs: []string{r},
				Fix: "type the pin power_out / power_in in the symbol, or name it VOUT / VIN", Evidence: []string{"regulator: " + reg.Why},
				Missing: []string{side + " pin of " + r}})
			continue
		}
		if s, ok := v.capless(r); ok && output {
			c.exempt(r, "capless-stable regulator declared", s)
			continue
		}
		seen := map[string]bool{}
		for _, p := range pins {
			node, via := p.Net, ""
			if output {
				var ok bool
				if node, via, ok = v.outputNode(r, p, v.regFunc(r), ins); !ok {
					c.unverified(fmt.Sprintf("%s: output of switch pin %s.%s (%s) not resolved: %s", r, r, p.Num, p.Name, via))
					continue
				}
			}
			root := v.find(node)
			if seen[root] {
				continue
			}
			seen[root] = true
			if why, src, ok := v.railExempt(node, ""); ok {
				c.exempt(r+" "+v.name(node), why, src)
				continue
			}
			c.Coverage.Checked++
			if caps, _ := v.capsTo(node); len(caps) > 0 {
				continue
			}
			if rc := v.capsToRail(node); len(rc) > 0 {
				if src, ok := v.chargePump(r); ok {
					c.exempt(r+" "+v.name(node), "charge pump: capacitor(s) "+strings.Join(rc, ", ")+" return through that rail", src)
					continue
				}
			}
			ev := []string{"regulator: " + reg.Why}
			how := "pin type " + p.Type
			if output && !byType || !output && p.Type != "power_in" {
				how = "pin name " + p.Name
			}
			ev = append(ev, side+" pin by "+how)
			if via != "" {
				ev = append(ev, "output traced: "+via)
			}
			ev = append(ev, "no capacitor from "+v.name(node)+" to ground")
			f := ReviewFinding{Severity: "error", Message: fmt.Sprintf("regulator %s %s %s (%s.%s) has no capacitor to ground", r, side, v.name(node), r, p.Num),
				Refs: []string{r}, Pins: []string{r + "." + p.Num}, netIDs: []string{node},
				Fix:      fmt.Sprintf("add the %s capacitor the %s datasheet requires (typ. 1–10 µF ceramic) from %s to GND", side, r, v.name(node)),
				Evidence: ev}
			if !reg.Proven {
				f.Severity, f.Certainty = "warning", "likely"
				f.Missing = []string{"regulator role of " + r + " (an intent ldo/buck/boost block)"}
			}
			c.add(f)
		}
	}
	return c
}

// checkRegOutput: a regulator's output needs a capacitor to ground.
func (v *rvView) checkRegOutput() *rvCheck { return v.checkReg(RvRegOutputCap, true) }

// checkRegInput: a regulator's input needs a capacitor to ground.
func (v *rvView) checkRegInput() *rvCheck { return v.checkReg(RvRegInputCap, false) }

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
				c.add(ReviewFinding{Severity: "warning", Certainty: "heuristic", Message: fmt.Sprintf("rail %s feeds loads and has no capacitance to ground (bulk floor %s)", name, rvFarad(BulkMinF)),
					netIDs: []string{p.Net}, Fix: fmt.Sprintf("add bulk capacitance (≥ %s, typ. 10 µF) on %s", rvFarad(BulkMinF), name),
					Evidence: []string{"no capacitor from " + name + " to ground", "loads on the rail: power_in pins"}})
				continue
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
				c.Coverage.Checked++
				c.add(ReviewFinding{Severity: "warning", Certainty: "likely", Message: fmt.Sprintf("rail %s: bulk capacitance not evaluated (%s known; value(s) not readable: %s)", name, rvFarad(total), strings.Join(unknown, ", ")),
					Refs: caps, netIDs: []string{p.Net}, Fix: "give the capacitors plain values (10u, 100n)", Evidence: []string{"capacitor values from the netlist"},
					Missing: []string{"capacitance of " + strings.Join(unknown, ", ")}})
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
// The names come from the violation's items (one quoted name each); a
// violation whose items do not give two names is unverified, never matched
// to an alias. Intent review.aliases exempt intentional ones. No ERC, or
// the rule set to ignore in the project: cannot-check.
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
		if len(names) < 2 {
			return "", false
		}
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
		var names, items, ids []string
		parsed := true
		for _, it := range e.Items {
			items = append(items, it.Description)
			m := rvQuoted.FindStringSubmatch(it.Description)
			if m == nil {
				parsed = false
				continue
			}
			names = append(names, m[1])
			id := it.UUID
			if id == "" {
				id = it.Description
			}
			ids = append(ids, id)
		}
		sort.Strings(names)
		names = rvUniq(names)
		if !parsed || len(names) < 2 {
			c.unverified(fmt.Sprintf("sheet %s: ERC multiple_net_names %q — its items do not name two labels (%s)", e.Sheet, e.Description, strings.Join(items, "; ")))
			continue
		}
		c.Coverage.Checked++
		if src, ok := alias(names); ok {
			c.exempt(strings.Join(names, " = ")+" on "+e.Sheet, "intentional alias", src)
			continue
		}
		f := ReviewFinding{Severity: "error", Message: fmt.Sprintf("names %s are on one net on sheet %s: %s", strings.Join(names, ", "), e.Sheet, e.Description),
			Sheet: e.Sheet, Fix: "rename or separate the labels / power symbols so each net has one name (or declare an intentional alias in intent review.aliases)",
			Evidence: append([]string{"kicad-cli ERC multiple_net_names (" + e.Severity + ")"}, items...)}
		f.Rule = RvMergedNets
		f.Nets = names
		f.Fingerprint = ReviewFingerprint(RvMergedNets, nil, nil, names, append([]string{"sheet=" + e.UUIDPath + e.Sheet}, ids...)...)
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
		if p.NC || v.railDefined(id) {
			continue // a rail (power symbol, power_out, intent) is the power checks' job
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
		f.Fingerprint = ReviewFingerprint(RvHierMismatch, nil, nil, []string{m.Name}, "path="+m.Path, "kind="+m.Kind)
		c.Findings = append(c.Findings, f)
	}
	return c
}
