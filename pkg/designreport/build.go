package designreport

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// ctx is the shared state of one Build.
type ctx struct {
	in    *Inputs
	rep   *Report
	elec  map[string]*elecNet // electrical view (sim worst, else intent)
	src   string              // "sim" | "intent" | ""
	parts map[string]*partInfo
	refs  []string
	// scenario results (sim) without the folded "worst".
	scen  []*powersim.Result
	worst *powersim.Result
	tree  *treeInfo
}

type elecNet struct {
	Name     string
	Role     string
	VNom     float64
	VMin     float64
	VMax     float64
	VPeak    float64
	IA       float64
	Floating bool
	Pins     []elecPin
}

type elecPin struct {
	Ref, Pin, Name, Dir string
	IA                  float64
}

type partInfo struct {
	Ref        string
	Device     string
	Value      string
	Kind       string
	ModelID    string
	Match      string
	Confidence string
	Source     string
	Lib        *LibModel
	Cap        *CapInfo
	Res        *ResInfo
	Desc       *DescRatings
	Block      string
	Pins       []partPin // pin → net (sorted by pin)
}

type partPin struct {
	Pin, Name, Net string
}

// Build computes the report. Missing inputs leave their sections nil and
// recorded in Report.Missing; Build never fails on a missing input.
func Build(in *Inputs) *Report {
	if in == nil {
		in = &Inputs{}
	}
	c := &ctx{in: in, parts: map[string]*partInfo{}}
	gen := in.GeneratedAt
	if gen.IsZero() {
		gen = time.Now()
	}
	c.rep = &Report{SchemaVersion: SchemaVersion, Generator: Generator, GeneratedAt: gen.UTC().Format(time.RFC3339),
		Project: in.Project, Customer: in.Customer, Tools: in.Tools, Inputs: append([]InputRef(nil), in.Refs...)}
	if c.rep.Tools.Pcbpilot == "" {
		c.rep.Tools.Pcbpilot = "dev"
	}
	c.rep.InputsDigest = inputsDigest(in.Refs)
	c.prepare()
	c.buildRequirements()
	c.buildPower()
	c.buildFeasibility()
	c.buildCalcs()
	c.buildLayout()
	c.buildVerification()
	c.buildTestPlan()
	c.buildManufacturing()
	c.buildBringUp()
	c.buildAppendix()
	c.buildSummary()
	return c.rep
}

func inputsDigest(refs []InputRef) string {
	var lines []string
	for _, r := range refs {
		if r.Present {
			lines = append(lines, r.Kind+":"+r.Label+":"+r.SHA256)
		}
	}
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:])
}

func (c *ctx) missing(section, reason string) {
	c.rep.Missing = append(c.rep.Missing, Missing{Section: section, Reason: reason})
}

// prepare builds the electrical view and the part table.
func (c *ctx) prepare() {
	in := c.in
	if s := in.Sim; s != nil {
		for _, r := range s.Results {
			r := r
			if r.Scenario == "worst" {
				c.worst = &r
			} else {
				c.scen = append(c.scen, &r)
			}
		}
		if c.worst == nil && len(c.scen) > 0 {
			c.worst = c.scen[0]
		}
	}
	c.elec = map[string]*elecNet{}
	switch {
	case c.worst != nil:
		c.src = "sim"
		typ := c.scenario("typical")
		for name, nr := range c.worst.Nets {
			en := &elecNet{Name: name, Role: nr.Role, VNom: nr.Voltage, VMin: nr.Voltage, VMax: nr.Voltage, IA: nr.CurrentA, Floating: nr.Floating}
			if typ != nil {
				if t := typ.Nets[name]; t != nil {
					en.VNom = t.Voltage
				}
			}
			if nr.VoltageMin != nil {
				en.VMin = *nr.VoltageMin
			}
			if nr.VoltageMax != nil {
				en.VMax = *nr.VoltageMax
			}
			en.VPeak = en.VMax
			for _, p := range nr.Pins {
				en.Pins = append(en.Pins, elecPin{Ref: p.Ref, Pin: p.Pin, Name: p.Name, Dir: p.Dir, IA: p.CurrentA})
			}
			c.elec[name] = en
		}
		if it := in.Intent; it != nil {
			for name, np := range it.Nets {
				if en := c.elec[name]; en != nil && np.Voltage.Peak > en.VPeak {
					en.VPeak = np.Voltage.Peak
				}
			}
		}
	case in.Intent != nil:
		c.src = "intent"
		for name, np := range in.Intent.Nets {
			en := &elecNet{Name: name, Role: np.Role, VNom: np.Voltage.Nom, VMin: np.Voltage.Min, VMax: np.Voltage.Max, VPeak: math.Max(np.Voltage.Peak, np.Voltage.Max), IA: np.CurrentA, Floating: np.Floating}
			for _, p := range np.Pins {
				en.Pins = append(en.Pins, elecPin{Ref: p.Ref, Pin: p.Pin, Name: p.Name, Dir: p.Dir, IA: p.CurrentA})
			}
			c.elec[name] = en
		}
	}
	// Parts: every ref seen anywhere.
	add := func(ref string) *partInfo {
		if ref == "" {
			return nil
		}
		p := c.parts[ref]
		if p == nil {
			p = &partInfo{Ref: ref}
			c.parts[ref] = p
		}
		return p
	}
	for _, en := range c.elec {
		for _, pin := range en.Pins {
			p := add(pin.Ref)
			p.Pins = append(p.Pins, partPin{Pin: pin.Pin, Name: pin.Name, Net: en.Name})
		}
	}
	if b := in.Board; b != nil {
		for _, bp := range b.Components {
			p := add(bp.Designator)
			if p.Device == "" {
				p.Device = bp.Device
			}
			if len(p.Pins) == 0 {
				for _, pad := range bp.Pads {
					if pad.Net != "" {
						p.Pins = append(p.Pins, partPin{Pin: pad.PadNumber, Name: pad.PadNumber, Net: pad.Net})
					}
				}
			}
		}
	}
	for ref, v := range in.Values {
		p := add(ref)
		if v.MPN != "" {
			p.Device = v.MPN
		}
		p.Value = v.Value
		if d, ok := ParseDescRatings(v.Description); ok {
			p.Desc = &d
		}
	}
	if s := in.Sim; s != nil {
		for _, m := range s.Models {
			p := add(m.Ref)
			p.Kind, p.ModelID, p.Match, p.Confidence, p.Source = m.Kind, m.ModelID, m.Match, m.Confidence, m.Source
			if lm := in.Models.Model(m.ModelID); lm != nil {
				p.Lib = lm
				if p.Source == "" {
					p.Source = lm.Source
				}
			}
		}
	}
	if it := in.Intent; it != nil {
		for _, b := range it.Blocks {
			for _, r := range b.Parts {
				if p := c.parts[r]; p != nil && p.Block == "" {
					p.Block = b.ID
				}
			}
		}
	}
	for ref, p := range c.parts {
		c.refs = append(c.refs, ref)
		if p.Kind == "" {
			p.Kind = kindFromRef(ref)
		}
		sort.Slice(p.Pins, func(i, j int) bool { return natLess(p.Pins[i].Pin, p.Pins[j].Pin) })
		switch p.Kind {
		case powersim.KindCapacitor:
			if ci, ok := ParseCapacitor(p.Device, p.Value); ok {
				p.Cap = &ci
			}
		case powersim.KindResistor:
			if ri, ok := ParseResistor(p.Device, p.Value); ok {
				p.Res = &ri
			}
		}
	}
	sort.Slice(c.refs, func(i, j int) bool { return natLess(c.refs[i], c.refs[j]) })
	c.tree = c.buildTree()
}

var reRefPrefix = regexp.MustCompile(`^([A-Za-z_]+)`)

func kindFromRef(ref string) string {
	p := strings.ToUpper(reRefPrefix.FindString(ref))
	switch p {
	case "R", "RN":
		return powersim.KindResistor
	case "C":
		return powersim.KindCapacitor
	case "L":
		return powersim.KindInductor
	case "FB":
		return powersim.KindFerrite
	case "D", "TVS", "ZD":
		return powersim.KindDiode
	case "LED":
		return powersim.KindLED
	case "Q":
		return powersim.KindBJT
	case "SW", "S", "K", "KEY":
		return powersim.KindSwitch
	case "J", "P", "CN", "USB", "H":
		return powersim.KindConnector
	case "F":
		return powersim.KindFuse
	case "Y", "X":
		return "crystal"
	case "TP":
		return "testpoint"
	case "U", "IC":
		return "ic"
	}
	return "other"
}

// natLess orders "C2" < "C10" and "2" < "10".
func natLess(a, b string) bool {
	pa, na := splitNum(a)
	pb, nb := splitNum(b)
	if pa != pb {
		return pa < pb
	}
	if na != nb {
		return na < nb
	}
	return a < b
}

var reTailNum = regexp.MustCompile(`^(.*?)(\d+)$`)

func splitNum(s string) (string, int) {
	m := reTailNum.FindStringSubmatch(s)
	if m == nil {
		return s, -1
	}
	n, _ := strconv.Atoi(m[2])
	return m[1], n
}

func (c *ctx) scenario(name string) *powersim.Result {
	for _, r := range c.scen {
		if r.Scenario == name {
			return r
		}
	}
	return nil
}

// isGround reports a ground net (sim role or name).
func (c *ctx) isGround(net string) bool {
	if en := c.elec[net]; en != nil && en.Role == "ground" {
		return true
	}
	u := strings.ToUpper(net)
	return u == "GND" || strings.HasPrefix(u, "GND") || strings.HasSuffix(u, "GND") || u == "VSS" || u == "AGND" || u == "PGND"
}

// partCurrent is the current a part carries: per net, the sum of its pins'
// currents on that net (paralleled connector/IC pins share one current);
// the largest net sum (worst case over scenarios, per-pin maxima).
func (c *ctx) partCurrent(ref string) float64 {
	m := 0.0
	for _, en := range c.elec {
		s := 0.0
		for _, p := range en.Pins {
			if p.Ref == ref {
				s += p.IA
			}
		}
		m = math.Max(m, s)
	}
	return m
}

// partCurrentIn is partCurrent in one scenario.
func partCurrentIn(r *powersim.Result, ref string) float64 {
	m := 0.0
	for _, nr := range r.Nets {
		s := 0.0
		for _, p := range nr.Pins {
			if p.Ref == ref {
				s += p.CurrentA
			}
		}
		m = math.Max(m, s)
	}
	return m
}

// nets returns the distinct non-empty nets of a part.
func (p *partInfo) nets() []string {
	seen := map[string]bool{}
	var out []string
	for _, pin := range p.Pins {
		if pin.Net != "" && !seen[pin.Net] {
			seen[pin.Net] = true
			out = append(out, pin.Net)
		}
	}
	sort.Strings(out)
	return out
}

// pinNet returns the net of the first pin whose name or number matches.
func (p *partInfo) pinNet(names ...string) string {
	for _, n := range names {
		for _, pin := range p.Pins {
			if strings.EqualFold(pin.Name, n) || strings.EqualFold(pin.Pin, n) {
				return pin.Net
			}
		}
	}
	return ""
}

// vAcross is the largest voltage across a two-net part (peak envelope).
func (c *ctx) vAcross(a, b string) float64 {
	ea, eb := c.elec[a], c.elec[b]
	va := func(e *elecNet) (float64, float64) {
		if e == nil {
			return 0, 0
		}
		return e.VMin, math.Max(e.VMax, e.VPeak)
	}
	amin, amax := va(ea)
	bmin, bmax := va(eb)
	return math.Max(math.Abs(amax-bmin), math.Abs(bmax-amin))
}

func (c *ctx) device(ref string) string {
	if p := c.parts[ref]; p != nil {
		return p.Device
	}
	return ""
}

// --- formatting helpers -------------------------------------------------

func f1(v float64) string { return strconv.FormatFloat(round(v, 1), 'f', -1, 64) }
func f2(v float64) string { return strconv.FormatFloat(round(v, 2), 'f', -1, 64) }
func f3(v float64) string { return strconv.FormatFloat(round(v, 3), 'f', -1, 64) }
func fV(v float64) string { return f3(v) + " V" }
func fA(v float64) string {
	if math.Abs(v) < 0.1 && v != 0 {
		return f2(v*1000) + " mA"
	}
	return f3(v) + " A"
}
func fW(v float64) string {
	if math.Abs(v) < 0.1 && v != 0 {
		return f2(v*1000) + " mW"
	}
	return f3(v) + " W"
}
func fPct(v float64) string             { return f1(v) + " %" }
func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
