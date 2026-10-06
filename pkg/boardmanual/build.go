package boardmanual

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// Generator names the producer on the cover.
const Generator = "pcbpilot report manual"

// Inputs are the parsed documents of one manual. Only Board is required.
type Inputs struct {
	Board       *Board
	Intent      *intent.Intent
	Sim         *powersim.Output
	Notes       *Notes
	PinMap      []PinAssign
	Sources     []Source
	Lang        string // zh (default) | en
	GeneratedAt time.Time
	Tool        string // pcbpilot version
}

// Source is the provenance of one input file.
type Source struct {
	Kind   string
	Path   string
	SHA256 string
	Bytes  int
}

// Manual is the computed content the template renders.
type Manual struct {
	Lang, Title, Subtitle, Revision, Overview string
	Sequence                                  []string
	Generated, Generator, CapturedAt          string
	NotesSources                              []string
	Sources                                   []Source
	WidthMM, HeightMM                         float64
	Layers, PartCount                         int
	Holes                                     []string
	HasNotes, HasSim, HasIntent               bool

	BoardSVG, ProbeSVG, LEDSVG string // inline SVG (escaped when built)

	Connectors []*Conn
	Power      PowerSection
	IO         []IORow
	LEDs       []LEDRow
	Firmwares  []string
	Cautions   []Caution
	ConnCaut   []Caution
	Jumpers    []JumperNote
	Equipment  []Equipment
	RailProbes []ProbeRow
	SigProbes  []ProbeRow
	MeasNotes  []string
	Bringup    []BringupStep
	Software   *Software
	Firmware   *FirmwareNotes
	PinMap     []PinMapRow
	PinMapRef  string
	Trouble    []Trouble
	Checks     []Check
	TODO       []string
}

// Conn is one connector, jumper or header.
type Conn struct {
	Index                        int
	Ref, Name, Device, Kind      string
	Role, Subtitle, Color        string
	Purpose, Mating, Part        string
	ConnectsTo, SystemRole       string
	Usage                        []string
	Highlight                    string
	PadCount, ExpectedPins       int
	XMM, YMM                     float64
	Pins                         []PinRow
	SVG                          string
	Cautions                     []string
	HasPower, HasSignal, HasDocs bool
	part                         *Part
}

// PinRow is one row of a connector pin table.
type PinRow struct {
	Pin, Net, Role, Voltage, MaxCurrent, Direction, Note, DocNet string
	Mismatch                                                     bool
	maxA                                                         float64
}

// PowerSection is chapter "电源要求".
type PowerSection struct {
	Input                       PowerInput
	InputRef, InputPins         string
	Voltage                     string
	Typ, Peak, Worst, Recommend string
	Margin                      float64
	PeakA                       float64
	Scenarios                   []string
	Rails                       []RailRow
	Notes                       []string
}

// RailRow is one on-board rail with per-scenario values.
type RailRow struct {
	Net    string
	Values []string // "V / A" per scenario
}

// IORow is one external signal row.
type IORow struct {
	Connector, Pin, Net, Signal, Direction, Level, MaxCurrent, Notes string
	FromNotes                                                        bool
}

// LEDRow is one LED.
type LEDRow struct {
	Ref, Name, Color, Net, Hardware string
	Modes                           []string // per Firmwares
	Mismatch                        bool
}

// Caution is one usage caution.
type Caution struct {
	Text, Source string
}

// ProbeRow is one numbered measurement point.
type ProbeRow struct {
	N                                       int
	Name, Where, Expected, Tolerance, Range string
	Current, Instrument, Note               string
	Found                                   bool
	x, y                                    float64
}

// PinMapRow is one programmable-device pin.
type PinMapRow struct {
	Signal, Pin, Net string
	Match            bool
}

// Check is one document-vs-board mismatch.
type Check struct {
	Where, Doc, Board, Note string
}

// PinAssign is one signal → device pin assignment of a pin-map file.
type PinAssign struct {
	Signal, Pin string
}

var (
	qsfRe = regexp.MustCompile(`set_location_assignment\s+PIN_([A-Za-z0-9]+)\s+-to\s+"?([^"\s]+)"?`)
	xdcRe = regexp.MustCompile(`set_property\s+(?:-dict\s+\{[^}]*PACKAGE_PIN\s+([A-Za-z0-9]+)[^}]*\}|PACKAGE_PIN\s+([A-Za-z0-9]+))\s+\[get_ports\s+\{?([^}\]\s]+)\}?\]`)
)

// ParsePinMap reads a Quartus .tcl/.qsf (set_location_assignment PIN_n -to sig)
// or Vivado .xdc (PACKAGE_PIN) pin assignment file.
func ParsePinMap(raw []byte) ([]PinAssign, error) {
	var out []PinAssign
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") {
			continue
		}
		if m := qsfRe.FindStringSubmatch(line); m != nil {
			out = append(out, PinAssign{Signal: m[2], Pin: m[1]})
		} else if m := xdcRe.FindStringSubmatch(line); m != nil {
			pin := m[1]
			if pin == "" {
				pin = m[2]
			}
			out = append(out, PinAssign{Signal: m[3], Pin: pin})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no pin assignments (set_location_assignment PIN_… / PACKAGE_PIN) found")
	}
	return out, nil
}

type ctx struct {
	in    Inputs
	lang  string
	notes *Notes
	m     *Manual
	ob    BBox
}

func (c *ctx) t(key string) string { return label(c.lang, key) }

// Build computes the manual. It never invents values: what the inputs do
// not give is "—" or TODO.
func Build(in Inputs) *Manual {
	lang := in.Lang
	if lang != "en" {
		lang = "zh"
	}
	n := in.Notes
	if n == nil {
		n = &Notes{}
	}
	c := &ctx{in: in, lang: lang, notes: n, m: &Manual{Lang: lang}}
	m := c.m
	b := in.Board
	c.ob = b.OutlineBox()
	m.HasNotes, m.HasSim, m.HasIntent = in.Notes != nil, in.Sim != nil, in.Intent != nil
	m.Title = firstNonEmpty(n.Title, "PCB")
	m.Subtitle, m.Revision, m.Overview, m.Sequence = n.Subtitle, n.Revision, n.Overview, n.Sequence
	m.Generated = in.GeneratedAt.UTC().Format("2006-01-02")
	m.Generator = Generator
	if in.Tool != "" {
		m.Generator += " " + in.Tool
	}
	m.CapturedAt = b.CapturedAt
	m.NotesSources = n.Sources
	m.Sources = in.Sources
	m.WidthMM, m.HeightMM = round1(c.ob.W()*MilToMM), round1(c.ob.H()*MilToMM)
	m.Layers, m.PartCount = b.CopperLayers, len(b.Components)
	for _, h := range b.MountHoles() {
		src := ""
		if h.From == "fill" {
			src = c.t("holeFromFill")
		}
		m.Holes = append(m.Holes, fmt.Sprintf("Ø%.1f mm @ (%.1f, %.1f)%s", h.Dia*MilToMM, (h.X-c.ob.MinX)*MilToMM, (h.Y-c.ob.MinY)*MilToMM, src))
	}
	m.Firmwares = n.Firmwares
	m.Jumpers = n.Jumpers
	m.Equipment = n.Equipment
	m.Bringup = n.Bringup
	m.Software = n.Software
	m.Firmware = n.Firmware
	m.Trouble = n.Troubleshooting
	m.TODO = n.TODO
	if n.Measurements != nil {
		m.MeasNotes = n.Measurements.Notes
	}

	c.buildConnectors()
	c.buildPower()
	c.buildIO()
	c.buildLEDs()
	c.buildProbes()
	c.buildPinMap()
	c.buildCautions()
	c.buildSVGs()
	return m
}

// ---------------------------------------------------------------- connectors

var connDeviceRe = regexp.MustCompile(`(?i)(conn|header|hdr|\bxh\b|b\dB-XH|micro-?fit|terminal|kf2edg|pz254|dc-0\d\d|usb|rj45|jst|molex|2\.54|1\.27|3\.81|5\.08|牛角|排针|排母|端子|插座)`)

// IsConnector reports whether a part is a connector, jumper or header:
// designator J* / CN* / P<n> / JP* / CON*, or a footprint name that suggests one.
func IsConnector(p *Part) bool {
	if len(p.Pads) < 2 {
		return false
	}
	switch refPrefix(p.Designator) {
	case "J", "CN", "P", "JP", "CON", "X", "XS", "XP":
		if refPrefix(p.Designator) == "X" && !connDeviceRe.MatchString(p.Device) {
			return false // X is also crystals / oscillators
		}
		return true
	}
	return connDeviceRe.MatchString(p.Device)
}

// ConnKind is jumper | header | connector.
func ConnKind(p *Part, jumper bool) string {
	switch {
	case jumper || refPrefix(p.Designator) == "JP" || regexp.MustCompile(`(?i)jumper|跳线`).MatchString(p.Device):
		return "jumper"
	case regexp.MustCompile(`(?i)header|hdr|2\.54|牛角|排针|pz254`).MatchString(p.Device):
		return "header"
	}
	return "connector"
}

var (
	jtagRe = regexp.MustCompile(`(?i)^(TCK|TMS|TDI|TDO|SWDIO|SWCLK)$`)
	canRe  = regexp.MustCompile(`(?i)^CAN_?[HL]$`)
	uartRe = regexp.MustCompile(`(?i)(TXD|RXD|UART|RS232|RS485|_TX$|_RX$)`)
	vinRe  = regexp.MustCompile(`(?i)^(VIN|\+?\d+V(_IN)?$|VBUS|DC_?IN|PWR_?IN)`)
	usbRe  = regexp.MustCompile(`(?i)^USB_?D[PMN+-]|^D[PM]$`)
)

// ConnRole is the role tag a connector gets from its pad nets when the
// notes give none: JTAG, CAN, USB, UART, POWER (supply input + ground
// only), JUMPER (2-pin header / jumper), else CONN.
func ConnRole(p *Part, kind string) string {
	var jtag, can, uart, usb, vin, gnd, other int
	for _, pd := range p.Pads {
		n := pd.Net
		switch {
		case jtagRe.MatchString(n):
			jtag++
		case canRe.MatchString(n):
			can++
		case usbRe.MatchString(n):
			usb++
		case uartRe.MatchString(n):
			uart++
		case groundRe.MatchString(n):
			gnd++
		case vinRe.MatchString(n):
			vin++
		default:
			other++
		}
	}
	switch {
	case jtag >= 3:
		return "JTAG"
	case can >= 2:
		return "CAN"
	case usb >= 2:
		return "USB"
	case uart >= 2:
		return "UART"
	case vin > 0 && gnd > 0 && other == 0 && jtag+can+uart+usb == 0:
		return "POWER"
	case kind == "jumper" || len(p.Pads) == 2:
		return "JUMPER"
	}
	return "CONN"
}

// RoleColor is the board-picture colour of a connector role.
func RoleColor(role string) string {
	role, _, _ = strings.Cut(role, "/")
	switch strings.TrimSpace(role) {
	case "POWER":
		return "#d62728"
	case "JTAG", "SWD":
		return "#9467bd"
	case "CAN":
		return "#1f77b4"
	case "GAS":
		return "#2ca02c"
	case "JUMPER":
		return "#ff7f0e"
	case "RS232", "UART", "RS485":
		return "#17becf"
	case "ANALOG", "TANK", "SENSOR":
		return "#8c564b"
	case "EXP", "GPIO":
		return "#e377c2"
	case "USB":
		return "#393b79"
	}
	return "#7f7f7f"
}

var (
	groundRe = regexp.MustCompile(`(?i)^(A|D|P|S|C)?GND|^VSS|^GND|GND$|^0V$|^EARTH|^PE$`)
	powerRe  = regexp.MustCompile(`(?i)^\+|^-\d|^V(CC|DD|IN|BUS|BAT|SYS|MOT)|^\d+V\d*|^P\d+V\d*`)
)

// NetRole is power | ground | signal | nc: the intent role when known,
// otherwise from the net name.
func NetRole(net string, it *intent.Intent) string {
	if strings.TrimSpace(net) == "" {
		return "nc"
	}
	if it != nil {
		if np, ok := it.Nets[net]; ok && np != nil {
			switch np.Role {
			case "power", "ground":
				return np.Role
			case "":
			default:
				return "signal"
			}
		}
	}
	switch {
	case groundRe.MatchString(net):
		return "ground"
	case powerRe.MatchString(net):
		return "power"
	}
	return "signal"
}

func (c *ctx) jumperRefs() map[string]bool {
	m := map[string]bool{}
	for _, j := range c.notes.Jumpers {
		m[j.Ref] = true
	}
	return m
}

func (c *ctx) buildConnectors() {
	b := c.in.Board
	jumpers := c.jumperRefs()
	var refs []string
	for i := range b.Components {
		p := &b.Components[i]
		if IsConnector(p) || jumpers[p.Designator] {
			refs = append(refs, p.Designator)
		}
	}
	sortNat(refs)
	onBoard := map[string]bool{}
	for i, ref := range refs {
		p := b.Part(ref)
		onBoard[ref] = true
		cn := c.notes.Connectors[ref]
		cc := &Conn{Index: i + 1, Ref: ref, Device: p.Device, Kind: ConnKind(p, jumpers[ref]),
			Name: cn.Name, Purpose: cn.Purpose, Mating: cn.Mating, Part: cn.Part,
			PadCount: len(p.Pads), ExpectedPins: cn.ExpectedPins, Cautions: cn.Cautions,
			HasDocs: len(cn.DocPins) > 0, part: p, Subtitle: cn.Subtitle,
			ConnectsTo: cn.ConnectsTo, SystemRole: cn.SystemRole, Usage: cn.Usage, Highlight: cn.Highlight}
		cc.XMM, cc.YMM = round1((p.X-c.ob.MinX)*MilToMM), round1((p.Y-c.ob.MinY)*MilToMM)
		if cc.Name == "" {
			if c.in.Notes != nil {
				c.check(ref, "—", cc.Device, c.t("chkNoNotes"))
			}
		}
		cc.Pins = c.pinTable(p, cn)
		for _, r := range cc.Pins {
			switch r.Role {
			case "power":
				cc.HasPower = true
			case "signal":
				cc.HasSignal = true
			}
		}
		cc.Role = strings.ToUpper(cn.Role)
		if cc.Role == "" {
			cc.Role = ConnRole(p, cc.Kind)
		}
		cc.Color = RoleColor(cc.Role)
		if cn.ExpectedPins > 0 && cn.ExpectedPins != len(p.Pads) {
			c.check(ref, fmt.Sprintf("%d", cn.ExpectedPins), fmt.Sprintf("%d", len(p.Pads)), fmt.Sprintf(c.t("chkPinCount"), cn.ExpectedPins, len(p.Pads)))
		}
		var docOnly []string
		for pin := range cn.DocPins {
			if p.Pad(pin) == nil {
				docOnly = append(docOnly, pin)
			}
		}
		sortNat(docOnly)
		for _, pin := range docOnly {
			d := cn.DocPins[pin]
			c.check(ref+"."+pin, strings.TrimSpace(d.Name+" "+d.Net), "—", c.t("chkPinMissing"))
		}
		c.m.Connectors = append(c.m.Connectors, cc)
	}
	var missing []string
	for ref := range c.notes.Connectors {
		if !onBoard[ref] {
			missing = append(missing, ref)
		}
	}
	sortNat(missing)
	for _, ref := range missing {
		c.check(ref, c.notes.Connectors[ref].Name, "—", c.t("chkMissingConn"))
	}
}

func (c *ctx) check(where, doc, board, note string) {
	c.m.Checks = append(c.m.Checks, Check{Where: where, Doc: doc, Board: board, Note: note})
}

// docNetEqual compares a document net with a board net ("NC" = no net).
func docNetEqual(doc, board string) bool {
	d := strings.TrimSpace(doc)
	if strings.EqualFold(d, "NC") || d == "—" || d == "-" {
		d = ""
	}
	return d == strings.TrimSpace(board)
}

// pinTable builds the pin rows of a connector from the board pads, the
// sim currents, the intent roles and the notes.
func (c *ctx) pinTable(p *Part, cn ConnNote) []PinRow {
	pads := append([]Pad(nil), p.Pads...)
	sort.SliceStable(pads, func(i, j int) bool { return natLess(pads[i].PadNumber, pads[j].PadNumber) })
	var rows []PinRow
	for _, pd := range pads {
		r := PinRow{Pin: pd.PadNumber, Net: pd.Net, Role: NetRole(pd.Net, c.in.Intent)}
		maxA, dir := c.pinCurrent(p.Designator, pd.PadNumber)
		r.maxA = maxA
		if maxA > 0 {
			r.MaxCurrent = fmtA(maxA)
		}
		switch r.Role {
		case "power":
			if v, ok := c.netVoltage(pd.Net); ok {
				r.Voltage = fmtV(v)
			}
			switch dir {
			case "source":
				r.Direction = c.t("dirIn")
			case "sink":
				r.Direction = c.t("dirOut")
			}
		case "ground":
			r.Voltage = "0 V"
		}
		if io := c.ioFor(p.Designator, pd.PadNumber); io != nil {
			if io.Direction != "" {
				r.Direction = io.Direction
			}
			if io.Level != "" && r.Role != "ground" {
				r.Voltage = io.Level
			}
			if io.MaxCurrent != "" {
				r.MaxCurrent = io.MaxCurrent
			}
		}
		r.Note = cn.PinNotes[pd.PadNumber]
		if d, ok := cn.DocPins[pd.PadNumber]; ok {
			r.DocNet = strings.TrimSpace(d.Name + " " + d.Net)
			if d.Net != "" && !docNetEqual(d.Net, pd.Net) {
				r.Mismatch = true
				r.DocNet = strings.TrimSpace(d.Name + " " + d.Net)
				board := pd.Net
				if board == "" {
					board = "NC"
				}
				c.check(p.Designator+"."+pd.PadNumber, r.DocNet, board, c.t("net"))
			}
		}
		rows = append(rows, r)
	}
	return rows
}

func (c *ctx) ioFor(ref, pin string) *IONote {
	for i := range c.notes.IO {
		io := &c.notes.IO[i]
		if io.Connector == ref && io.Pin == pin {
			return io
		}
	}
	return nil
}

// ---------------------------------------------------------------- sim access

func (c *ctx) result(name string) *powersim.Result {
	if c.in.Sim == nil {
		return nil
	}
	for i := range c.in.Sim.Results {
		if c.in.Sim.Results[i].Scenario == name {
			return &c.in.Sim.Results[i]
		}
	}
	return nil
}

// baseResult is "typical" or the first converged scenario.
func (c *ctx) baseResult() *powersim.Result {
	if r := c.result("typical"); r != nil {
		return r
	}
	if c.in.Sim != nil && len(c.in.Sim.Results) > 0 {
		return &c.in.Sim.Results[0]
	}
	return nil
}

// pinCurrent is the max current through a pad over all scenarios and the
// direction where it occurred.
func (c *ctx) pinCurrent(ref, pin string) (float64, string) {
	if c.in.Sim == nil {
		return 0, ""
	}
	best, dir := 0.0, ""
	for _, r := range c.in.Sim.Results {
		for _, nr := range r.Nets {
			if nr == nil {
				continue
			}
			for _, pr := range nr.Pins {
				if pr.Ref == ref && pr.Pin == pin && pr.CurrentA > best {
					best, dir = pr.CurrentA, pr.Dir
				}
			}
		}
	}
	return best, dir
}

func (c *ctx) pinCurrentIn(r *powersim.Result, ref, pin string) (float64, string) {
	for _, nr := range r.Nets {
		if nr == nil {
			continue
		}
		for _, pr := range nr.Pins {
			if pr.Ref == ref && pr.Pin == pin {
				return pr.CurrentA, pr.Dir
			}
		}
	}
	return 0, ""
}

func (c *ctx) netVoltage(net string) (float64, bool) {
	if r := c.baseResult(); r != nil {
		if nr, ok := r.Nets[net]; ok && nr != nil {
			return nr.Voltage, true
		}
	}
	if c.in.Intent != nil {
		if np, ok := c.in.Intent.Nets[net]; ok && np != nil && np.Voltage.Nom != 0 {
			return np.Voltage.Nom, true
		}
	}
	return 0, false
}

// ---------------------------------------------------------------- power

func (c *ctx) buildPower() {
	ps := &c.m.Power
	pn := c.notes.Power
	ps.Input, ps.Notes = pn.Input, pn.Notes
	ps.Margin = pn.Margin
	if ps.Margin <= 0 {
		ps.Margin = 1.5
	}
	if c.in.Sim != nil {
		ps.Scenarios = append(ps.Scenarios, c.in.Sim.Scenarios...)
		if len(ps.Scenarios) == 0 {
			for _, r := range c.in.Sim.Results {
				ps.Scenarios = append(ps.Scenarios, r.Scenario)
			}
		}
	}
	// Input connector: notes, else the connector whose power pin sources
	// the most current into the board.
	ref := pn.Input.Connector
	if ref == "" {
		best := 0.0
		for _, cc := range c.m.Connectors {
			for _, r := range cc.Pins {
				if r.Role != "power" {
					continue
				}
				if a, d := c.pinCurrent(cc.Ref, r.Pin); d == "source" && a > best {
					best, ref = a, cc.Ref
				}
			}
		}
	}
	ps.InputRef = ref
	p := c.in.Board.Part(ref)
	cur := map[string]float64{}
	var pins []string
	if p != nil && c.in.Sim != nil {
		for _, r := range c.in.Sim.Results {
			sum := 0.0
			for _, pd := range p.Pads {
				if NetRole(pd.Net, c.in.Intent) != "power" {
					continue
				}
				if a, d := c.pinCurrentIn(&r, ref, pd.PadNumber); d == "source" {
					sum += a
					if r.Scenario == c.baseResult().Scenario {
						pins = append(pins, pd.PadNumber+" "+pd.Net)
					}
				}
			}
			cur[r.Scenario] = sum
		}
		if len(pins) > 0 {
			if v, ok := c.netVoltage(strings.SplitN(pins[0], " ", 2)[1]); ok && pn.Input.VoltageV == 0 {
				ps.Voltage = fmtV(v) + " (sim)"
			}
		}
	}
	sortNat(pins)
	ps.InputPins = strings.Join(pins, ", ")
	if pn.Input.VoltageV != 0 {
		ps.Voltage = fmtV(pn.Input.VoltageV)
	}
	set := func(dst *string, scen string, override *float64) {
		switch {
		case override != nil:
			*dst = fmtA(*override)
		case cur[scen] > 0:
			*dst = fmtA(cur[scen])
		}
	}
	set(&ps.Typ, "typical", pn.Input.CurrentATyp)
	set(&ps.Peak, "peak", nil)
	set(&ps.Worst, "worst", pn.Input.CurrentAMax)
	ps.PeakA = math.Max(cur["peak"], cur["worst"])
	if pn.Input.CurrentAMax != nil {
		ps.PeakA = math.Max(ps.PeakA, *pn.Input.CurrentAMax)
	}
	if ps.PeakA > 0 {
		ps.Recommend = fmt.Sprintf("≥ %s（%s = %s × %.2g）", fmtA(ps.PeakA*ps.Margin), c.t("recFormula"), fmtA(ps.PeakA), ps.Margin)
	}
	// Rails: every simulated power net above 0.5 V, highest voltage first.
	base := c.baseResult()
	if base == nil {
		return
	}
	var nets []string
	for n, nr := range base.Nets {
		if nr != nil && nr.Role == "power" && nr.Voltage > 0.5 {
			nets = append(nets, n)
		}
	}
	sort.SliceStable(nets, func(i, j int) bool {
		vi, vj := base.Nets[nets[i]].Voltage, base.Nets[nets[j]].Voltage
		if math.Abs(vi-vj) > 1e-6 {
			return vi > vj
		}
		return natLess(nets[i], nets[j])
	})
	for _, n := range nets {
		row := RailRow{Net: n}
		for _, s := range ps.Scenarios {
			r := c.result(s)
			if r == nil || r.Nets[n] == nil {
				row.Values = append(row.Values, "—")
				continue
			}
			row.Values = append(row.Values, fmtV(r.Nets[n].Voltage)+" / "+fmtA(r.Nets[n].CurrentA))
		}
		ps.Rails = append(ps.Rails, row)
	}
}

// ---------------------------------------------------------------- IO

func (c *ctx) buildIO() {
	used := map[int]bool{}
	for _, cc := range c.m.Connectors {
		for _, r := range cc.Pins {
			var io *IONote
			idx := -1
			for i := range c.notes.IO {
				if c.notes.IO[i].Connector == cc.Ref && c.notes.IO[i].Pin == r.Pin {
					io, idx = &c.notes.IO[i], i
					break
				}
			}
			if r.Role != "signal" && io == nil {
				continue
			}
			row := IORow{Connector: cc.Ref, Pin: r.Pin, Net: r.Net, Signal: r.Net, Direction: "—", Level: c.t("todoMark"), MaxCurrent: "—"}
			if r.MaxCurrent != "" {
				row.MaxCurrent = r.MaxCurrent
			}
			if io != nil {
				used[idx] = true
				row.FromNotes = true
				row.Signal = firstNonEmpty(io.Signal, r.Net)
				row.Direction = firstNonEmpty(io.Direction, row.Direction)
				row.Level = firstNonEmpty(io.Level, row.Level)
				row.MaxCurrent = firstNonEmpty(io.MaxCurrent, row.MaxCurrent)
				row.Notes = io.Notes
			}
			c.m.IO = append(c.m.IO, row)
		}
	}
	for i, io := range c.notes.IO {
		if used[i] {
			continue
		}
		if io.Pin != "" && io.Connector != "" {
			c.check(io.Connector+"."+io.Pin, io.Signal, "—", c.t("chkIOPin"))
		}
		c.m.IO = append(c.m.IO, IORow{Connector: io.Connector, Pin: io.Pin, Signal: io.Signal, Direction: io.Direction,
			Level: io.Level, MaxCurrent: io.MaxCurrent, Notes: io.Notes, FromNotes: true})
	}
}

// ---------------------------------------------------------------- LEDs

var ledNetRe = regexp.MustCompile(`(?i)LED`)

// ledParts is the notes' LED list, else every D*/LED* part with a pad on a
// net whose name contains "LED".
func (c *ctx) buildLEDs() {
	b := c.in.Board
	if len(c.notes.LEDs) == 0 {
		var refs []string
		for i := range b.Components {
			p := &b.Components[i]
			pf := refPrefix(p.Designator)
			if pf != "D" && pf != "LED" {
				continue
			}
			for n := range p.Nets() {
				if ledNetRe.MatchString(n) {
					refs = append(refs, p.Designator)
					break
				}
			}
		}
		sortNat(refs)
		for _, r := range refs {
			c.notes.LEDs = append(c.notes.LEDs, LEDNote{Ref: r})
		}
	}
	for _, ln := range c.notes.LEDs {
		row := LEDRow{Ref: ln.Ref, Name: ln.Name, Color: ln.Color, Hardware: ln.Hardware}
		p := b.Part(ln.Ref)
		if p == nil {
			row.Mismatch = true
			c.check(ln.Ref, ln.Name, "—", c.t("chkLEDMissing"))
		} else {
			row.Net = ledAnodeNet(p, c.in.Intent)
			if ln.Net != "" && !p.Nets()[ln.Net] {
				row.Mismatch = true
				c.check(ln.Ref, ln.Net, row.Net, c.t("chkLEDNet"))
			}
		}
		for _, fw := range c.m.Firmwares {
			row.Modes = append(row.Modes, firstNonEmpty(ln.Modes[fw], "—"))
		}
		c.m.LEDs = append(c.m.LEDs, row)
	}
}

// ledAnodeNet is the LED's non-ground net (the one naming LED first).
func ledAnodeNet(p *Part, it *intent.Intent) string {
	var nets []string
	for n := range p.Nets() {
		if NetRole(n, it) != "ground" {
			nets = append(nets, n)
		}
	}
	sortNat(nets)
	for _, n := range nets {
		if ledNetRe.MatchString(n) {
			return n
		}
	}
	if len(nets) > 0 {
		return nets[0]
	}
	return ""
}

// ---------------------------------------------------------------- probes

// resolveProbe finds "REF.PAD" / "REF" on the board; for "REF" the pad on
// net (when given) is preferred.
func (c *ctx) resolveProbe(spec, net string) (*Part, *Pad) {
	ref, pad, _ := strings.Cut(strings.TrimSpace(spec), ".")
	p := c.in.Board.Part(ref)
	if p == nil {
		return nil, nil
	}
	if pad != "" {
		return p, p.Pad(pad)
	}
	for i := range p.Pads {
		if net != "" && p.Pads[i].Net == net {
			return p, &p.Pads[i]
		}
	}
	if len(p.Pads) > 0 {
		pads := append([]Pad(nil), p.Pads...)
		sort.SliceStable(pads, func(i, j int) bool { return natLess(pads[i].PadNumber, pads[j].PadNumber) })
		return p, p.Pad(pads[0].PadNumber)
	}
	return p, nil
}

// autoProbe picks a probe pad on a net: a connector pin, else a two-pad
// capacitor whose other pad is ground, else any pad.
func (c *ctx) autoProbe(net string) (*Part, *Pad, string) {
	type cand struct {
		p   *Part
		pad *Pad
	}
	var conns, caps, any []cand
	for i := range c.in.Board.Components {
		p := &c.in.Board.Components[i]
		for j := range p.Pads {
			pd := &p.Pads[j]
			if pd.Net != net {
				continue
			}
			switch {
			case IsConnector(p):
				conns = append(conns, cand{p, pd})
			case refPrefix(p.Designator) == "C" && len(p.Pads) == 2:
				other := p.Pads[1-j]
				if NetRole(other.Net, c.in.Intent) == "ground" {
					caps = append(caps, cand{p, pd})
				}
			default:
				any = append(any, cand{p, pd})
			}
		}
	}
	for _, list := range [][]cand{conns, caps, any} {
		if len(list) == 0 {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].p.Designator != list[j].p.Designator {
				return natLess(list[i].p.Designator, list[j].p.Designator)
			}
			return natLess(list[i].pad.PadNumber, list[j].pad.PadNumber)
		})
		note := ""
		if refPrefix(list[0].p.Designator) == "C" && len(list[0].p.Pads) == 2 {
			note = c.t("otherSideGND")
		}
		return list[0].p, list[0].pad, note
	}
	return nil, nil, ""
}

func (c *ctx) buildProbes() {
	n := 0
	base := c.baseResult()
	for _, rail := range c.m.Power.Rails {
		rn := c.notes.Power.Rails[rail.Net]
		row := ProbeRow{Name: rail.Net, Tolerance: firstNonEmpty(rn.Tolerance, c.t("todoMark")), Note: rn.Note, Instrument: "DMM"}
		var p *Part
		var pd *Pad
		autoNote := ""
		if rn.Probe != "" {
			p, pd = c.resolveProbe(rn.Probe, rail.Net)
			if pd == nil {
				c.check(rn.Probe, rail.Net, "—", c.t("chkProbe"))
			}
		} else {
			p, pd, autoNote = c.autoProbe(rail.Net)
		}
		if pd != nil {
			row.Found = true
			row.Where = p.Designator + "." + pd.PadNumber
			if pd.Net != rail.Net {
				row.Where += "（" + firstNonEmpty(pd.Net, "NC") + "）"
				c.check(row.Where, rail.Net, firstNonEmpty(pd.Net, "NC"), c.t("chkProbe"))
			}
			if autoNote != "" {
				row.Note = strings.TrimSpace(autoNote + "；" + row.Note)
				row.Note = strings.TrimSuffix(row.Note, "；")
			}
			row.x, row.y = pd.X, pd.Y
		} else {
			row.Where = c.t("railNoProbe")
		}
		if base != nil && base.Nets[rail.Net] != nil {
			row.Expected = fmtV(base.Nets[rail.Net].Voltage)
			lo, hi := math.Inf(1), math.Inf(-1)
			var curs []string
			for _, s := range c.m.Power.Scenarios {
				r := c.result(s)
				if r == nil || r.Nets[rail.Net] == nil {
					continue
				}
				v := r.Nets[rail.Net].Voltage
				lo, hi = math.Min(lo, v), math.Max(hi, v)
				curs = append(curs, s+" "+fmtA(r.Nets[rail.Net].CurrentA))
			}
			if hi >= lo {
				row.Range = fmtV(lo) + " – " + fmtV(hi)
			}
			row.Current = strings.Join(curs, " / ")
		}
		if rn.Expected != "" {
			row.Expected = rn.Expected
		}
		n++
		row.N = n
		c.m.RailProbes = append(c.m.RailProbes, row)
	}
	if c.notes.Measurements == nil {
		return
	}
	for _, s := range c.notes.Measurements.Signals {
		row := ProbeRow{Name: s.Signal, Expected: firstNonEmpty(s.Expected, c.t("todoMark")), Instrument: s.Instrument, Note: s.Note}
		var p *Part
		var pd *Pad
		if s.Probe != "" {
			p, pd = c.resolveProbe(s.Probe, s.Net)
		} else if s.Net != "" {
			p, pd, _ = c.autoProbe(s.Net)
		}
		if pd != nil {
			row.Found = true
			row.Where = p.Designator + "." + pd.PadNumber + "（" + firstNonEmpty(pd.Net, "NC") + "）"
			if s.Net != "" && pd.Net != s.Net {
				c.check(p.Designator+"."+pd.PadNumber, s.Net, firstNonEmpty(pd.Net, "NC"), c.t("chkProbe"))
			}
			row.x, row.y = pd.X, pd.Y
		} else {
			row.Where = firstNonEmpty(s.Probe, s.Net, "—")
			if s.Probe != "" {
				c.check(s.Probe, s.Signal, "—", c.t("chkProbe"))
			}
		}
		n++
		row.N = n
		c.m.SigProbes = append(c.m.SigProbes, row)
	}
}

// ---------------------------------------------------------------- pin map

func normSig(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (c *ctx) buildPinMap() {
	if len(c.in.PinMap) == 0 {
		return
	}
	ref := ""
	if c.notes.Firmware != nil {
		ref = c.notes.Firmware.FPGARef
	}
	var p *Part
	if ref != "" {
		p = c.in.Board.Part(ref)
	} else {
		// the part with the most pads that has every assigned pin number
		for i := range c.in.Board.Components {
			q := &c.in.Board.Components[i]
			better := p == nil || len(q.Pads) > len(p.Pads) ||
				len(q.Pads) == len(p.Pads) && refPrefix(q.Designator) == "U" && refPrefix(p.Designator) != "U"
			if IsConnector(q) || !better {
				continue
			}
			ok := true
			for _, a := range c.in.PinMap {
				if q.Pad(a.Pin) == nil {
					ok = false
					break
				}
			}
			if ok {
				p = q
			}
		}
	}
	if p == nil {
		c.check(firstNonEmpty(ref, "—"), "pin map", "—", c.t("chkFPGA"))
		return
	}
	c.m.PinMapRef = p.Designator
	for _, a := range c.in.PinMap {
		row := PinMapRow{Signal: a.Signal, Pin: a.Pin}
		if pd := p.Pad(a.Pin); pd != nil {
			row.Net = pd.Net
			s, n := normSig(a.Signal), normSig(pd.Net)
			row.Match = s != "" && n != "" && (strings.Contains(n, s) || strings.Contains(s, n))
		}
		if !row.Match {
			c.check(p.Designator+"."+a.Pin, a.Signal, firstNonEmpty(row.Net, "NC"), c.t("chkPinMap"))
		}
		c.m.PinMap = append(c.m.PinMap, row)
	}
}

// ---------------------------------------------------------------- cautions

func (c *ctx) buildCautions() {
	for _, s := range c.notes.Cautions {
		c.m.Cautions = append(c.m.Cautions, Caution{Text: s, Source: "notes"})
	}
	auto := func(s string) { c.m.Cautions = append(c.m.Cautions, Caution{Text: s, Source: "auto"}) }
	for _, cc := range c.m.Connectors {
		for _, s := range cc.Cautions {
			c.m.ConnCaut = append(c.m.ConnCaut, Caution{Text: cc.Ref + "：" + s, Source: "notes"})
		}
	}
	// Max current per power pin, grouped by net within a connector.
	for _, cc := range c.m.Connectors {
		type grp struct {
			pins []string
			maxA float64
		}
		g := map[string]*grp{}
		var order []string
		for _, r := range cc.Pins {
			if r.Role != "power" || r.maxA <= 0 {
				continue
			}
			if g[r.Net] == nil {
				g[r.Net] = &grp{}
				order = append(order, r.Net)
			}
			g[r.Net].pins = append(g[r.Net].pins, r.Pin)
			g[r.Net].maxA = math.Max(g[r.Net].maxA, r.maxA)
		}
		if len(order) == 0 {
			continue
		}
		var parts []string
		for _, net := range order {
			parts = append(parts, fmt.Sprintf("%s.%s（%s）%s/%s", cc.Ref, strings.Join(g[net].pins, "/"), net, fmtA(g[net].maxA), c.t("pinsWord")))
		}
		auto(fmt.Sprintf(c.t("cautMaxPin"), cc.Ref, strings.Join(parts, "；")))
	}
	if it := c.in.Intent; it != nil {
		for _, d := range it.Domains {
			if d == nil || d.Kind == "" || strings.EqualFold(d.Kind, "SELV") {
				continue
			}
			nets := append([]string(nil), d.Nets...)
			sortNat(nets)
			if len(nets) > 8 {
				nets = append(nets[:8], "…")
			}
			auto(fmt.Sprintf(c.t("cautHazard"), d.ID, d.Kind, trimNum(d.WorkingVpeak), strings.Join(nets, ", ")))
		}
	}
	for _, cc := range c.m.Connectors {
		if cc.HasPower && cc.HasSignal {
			auto(fmt.Sprintf(c.t("cautMixed"), cc.Ref+" "+cc.Name))
		}
	}
	if c.m.Power.PeakA > 2 {
		auto(fmt.Sprintf(c.t("cautInputHigh"), trimNum(c.m.Power.PeakA)))
	}
	if n := len(c.m.Checks); n > 0 {
		auto(fmt.Sprintf(c.t("seeChecks"), n))
	}
}

// ---------------------------------------------------------------- formatting

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

func trimNum(v float64) string {
	s := fmt.Sprintf("%.3f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "-0" {
		s = "0"
	}
	return s
}

func fmtV(v float64) string {
	if math.Abs(v) >= 10 {
		return fmt.Sprintf("%.2f V", v)
	}
	return fmt.Sprintf("%.3g V", v)
}

func fmtA(a float64) string {
	switch {
	case a >= 1:
		return fmt.Sprintf("%.2f A", a)
	case a >= 0.001:
		return trimNum(a*1000) + " mA"
	case a > 0:
		return trimNum(a*1e6) + " µA"
	}
	return "0 A"
}
