package pcbauto

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// PartKind is the functional class of a part.
type PartKind string

const (
	KindIC          PartKind = "ic"
	KindConnector   PartKind = "connector"
	KindResistor    PartKind = "resistor"
	KindCapacitor   PartKind = "capacitor"
	KindInductor    PartKind = "inductor"
	KindDiode       PartKind = "diode"
	KindLED         PartKind = "led"
	KindTransistor  PartKind = "transistor"
	KindCrystal     PartKind = "crystal"
	KindFuse        PartKind = "fuse"
	KindRelay       PartKind = "relay"
	KindTransformer PartKind = "transformer"
	KindOpto        PartKind = "optocoupler"
	KindIsolator    PartKind = "isolator"  // digital isolator / isolated transceiver
	KindIsoPower    PartKind = "iso-power" // isolated DC/DC module
	KindSwitch      PartKind = "switch"
	KindTestPoint   PartKind = "testpoint"
	KindMechanical  PartKind = "mechanical" // mounting hole, fiducial
	KindModule      PartKind = "module"     // RF / MCU module
	KindAntenna     PartKind = "antenna"    // chip / PCB / connector-fed antenna: the RF port
	KindOther       PartKind = "other"
)

// Bridges reports whether a part kind can legitimately span two isolated
// voltage domains (its body is the isolation barrier).
func (k PartKind) Bridges() bool {
	switch k {
	case KindOpto, KindIsolator, KindIsoPower, KindTransformer, KindRelay:
		return true
	}
	return false
}

var (
	reOpto     = regexp.MustCompile(`(?i)(PC8\d\d|EL3\d\d|EL8\d\d|TLP\d|LTV|6N13|HCPL|ACPL|MOC30|OPTO|FOD\d|PS28|K10\d\d)`)
	reIsolator = regexp.MustCompile(`(?i)(ADUM|ISO7\d|ISO1\d|ISO35|SI86|SI84|CA-IS|π1|PI1\d\d|NSI|TPT7|ADM2\d\d\dE|MAX1449|ISOW|ISO5\d)`)
	reIsoPower = regexp.MustCompile(`(?i)(^B\d{4}S|^IB\d{4}|URB\d|VRB\d|A\d{4}S|HLK-|HI-LINK|AC-?DC|DC-?DC.*ISO|ISO.*DC-?DC|B0505|B0503|F0505|R1SE|MEE1)`)
	reModule   = regexp.MustCompile(`(?i)(ESP32-|ESP-|WROOM|WROVER|NRF52.*MOD|HC-05|SIM800|EC20|RA-0|BL60|MODULE)`)
	reConnRef  = regexp.MustCompile(`^(J|P|CN|CON|USB|FPC|X?JP|H\d|TB|DC)\d*`)
)

// ClassifyPart infers the functional kind from designator, device and pads.
// reRFConn matches coaxial RF connectors (U.FL/IPEX/MHF/SMA/MMCX).
var reRFConn = regexp.MustCompile(`(?i)(IPEX|U\.?FL|MHF|SMA[-_ ]?[KJ]|^SMA|MMCX|BWIPX|W\.?FL)`)

// reAntennaDev matches antenna part numbers (chip antennas, PCB antennas).
var reAntennaDev = regexp.MustCompile(`(?i)(ANTENNA|^CA-C0\d|2450AT|W3008|AMCA|ANT\d{4})`)

// reESDArray matches ESD/TVS array families (not fuses or PTCs).
var reESDArray = regexp.MustCompile(`(?i)(ESD|TVS|PESD|USBLC|SRV0|LESD|ULC\d|PRTR|RCLAMP|TPD\d|SMF\d|SMAJ|SMBJ|P6KE|ESDA|IP4220|NUP\d)`)

func ClassifyPart(p *Part) PartKind {
	ref := upper(p.Ref)
	dev := upper(p.Device)
	prefix := strings.TrimRightFunc(ref, func(r rune) bool { return r >= '0' && r <= '9' || r == '_' })
	switch {
	case reOpto.MatchString(dev):
		return KindOpto
	case reIsolator.MatchString(dev):
		return KindIsolator
	case reIsoPower.MatchString(dev):
		return KindIsoPower
	case reModule.MatchString(dev):
		return KindModule
	}
	if prefix == "ANT" || prefix == "AE" || reAntennaDev.MatchString(dev) {
		return KindAntenna
	}
	// ESD/TVS arrays are protection whatever the designer called them:
	// "U3 USBLC6-2SC6" is not a core IC and must not end a signal chain.
	if (prefix == "U" || prefix == "IC" || prefix == "D") && reESDArray.MatchString(dev) {
		return KindDiode
	}
	switch prefix {
	case "R", "RN", "RA", "VR":
		return KindResistor
	case "C", "CE", "EC", "CB":
		return KindCapacitor
	case "L", "FB":
		return KindInductor
	case "D", "TVS", "ZD", "ESD":
		return KindDiode
	case "LED":
		return KindLED
	case "Q", "M":
		return KindTransistor
	case "Y", "X", "XTAL", "OSC":
		return KindCrystal
	case "F", "FU", "PTC":
		return KindFuse
	case "K", "RLY":
		return KindRelay
	case "T", "TR":
		return KindTransformer
	case "SW", "S", "KEY", "BTN":
		return KindSwitch
	case "TP":
		return KindTestPoint
	case "MH", "H", "FID", "MARK":
		if len(p.Pads) <= 1 {
			return KindMechanical
		}
	case "U", "IC", "AR":
		if len(p.Pads) >= 3 {
			return KindIC
		}
	}
	if reConnRef.MatchString(ref) || reRFConn.MatchString(dev) || strings.Contains(dev, "USB") && len(p.Pads) >= 4 || strings.Contains(dev, "HEADER") || strings.Contains(dev, "CONN") {
		return KindConnector
	}
	if strings.HasPrefix(dev, "LED") || strings.Contains(dev, "-LED") {
		return KindLED
	}
	if len(p.Pads) >= 6 {
		return KindIC
	}
	return KindOther
}

// Block is a functional unit: a core part and the parts that serve it.
type Block struct {
	ID      string   `json:"id"`
	Kind    string   `json:"kind"` // mcu | power | interface | analog | rf | isolation | logic | misc
	Core    string   `json:"core"`
	Parts   []string `json:"parts"`
	Members []Member `json:"members,omitempty"` // auxiliaries with their role and served pin
	Domain  string   `json:"domain"`
	Why     []string `json:"why,omitempty"`
}

// Member is an auxiliary part of a block: what it does for the core and
// which core pad it serves (the placer tethers it there).
type Member struct {
	Ref  string `json:"ref"`
	Role string `json:"role"`          // decap | protection | clock | clock-load | power-stage | pull | signal | chain
	Pin  string `json:"pin,omitempty"` // REF.PAD it serves
	Why  string `json:"why"`
}

// Link is the signal traffic between two blocks.
type Link struct {
	From  string   `json:"from"`
	To    string   `json:"to"`
	Nets  []string `json:"nets"`
	Kinds []string `json:"kinds"` // i2c, spi, uart, usb, diff, clock, power, signal
}

// Domain is a voltage/reference domain (parts sharing a ground reference).
type Domain struct {
	ID         string   `json:"id"`
	Ground     string   `json:"ground"`
	MaxVoltage float64  `json:"maxVoltage"`
	Hazardous  bool     `json:"hazardous"` // above SELV (60 V DC / 30 V AC) or mains
	Mains      bool     `json:"mains"`
	Parts      []string `json:"parts"`
	Nets       []string `json:"nets"`
}

// Barrier is the insulation required between two domains.
type Barrier struct {
	A            string   `json:"a"`
	B            string   `json:"b"`
	Bridges      []string `json:"bridges"` // parts spanning the barrier
	WorkingV     float64  `json:"workingVoltage"`
	Insulation   string   `json:"insulation"` // functional | basic | reinforced
	CreepageMil  float64  `json:"creepageMil"`
	ClearanceMil float64  `json:"clearanceMil"`
	SlotUnder    []string `json:"slotUnder,omitempty"` // bridges needing a milled slot
	Why          []string `json:"why"`
}

// Circuit is the understood schematic.
type Circuit struct {
	Kinds    map[string]PartKind `json:"kinds"`
	Blocks   []*Block            `json:"blocks"`
	Links    []*Link             `json:"links"`
	Domains  []*Domain           `json:"domains"`
	Barriers []*Barrier          `json:"barriers"`
	BlockOf  map[string]string   `json:"blockOf"`
	DomainOf map[string]string   `json:"domainOf"`
	// Converters are the recognised switching regulators with their hot
	// loops, bootstrap and feedback parts.
	Converters []*Converter `json:"converters,omitempty"`
	// Chains are connector-to-IC signal paths whose part order matters.
	Chains []*SignalChain `json:"chains,omitempty"`
	Notes  []string       `json:"notes,omitempty"`
}

var (
	// Mains is recognised only by unambiguous names. An "HV_" prefix is NOT
	// mains: boards name the feed of an on-board HV converter HV_N5 or the
	// ±24 V pulser rail +HV (real case: pic0rick, A0524S). DC high voltage is
	// judged by its value (parsed from the name or declared in power.json).
	reMains = regexp.MustCompile(`(?i)^(AC_?L|AC_?N|L_?IN|N_?IN|LINE|NEUTRAL|MAINS|AC\d*|~|220V|230V|110V|120V|VAC|L|N|PE)$`)
)

// isGlobalNet marks nets that connect everything and carry no "belonging".
func isGlobalNet(an *Analysis, net string, r Rules) bool {
	p := an.Plan(net, r)
	return p.Role == RoleGround || p.Role == RolePower
}

// Understand builds the circuit model: part kinds, functional blocks,
// inter-block links, voltage domains and isolation barriers.
func Understand(b *Board, an *Analysis) *Circuit {
	c := &Circuit{Kinds: map[string]PartKind{}, BlockOf: map[string]string{}, DomainOf: map[string]string{}}
	for _, p := range b.Parts {
		c.Kinds[p.Ref] = ClassifyPart(p)
	}
	c.buildDomains(b, an)
	c.buildBlocks(b, an)
	c.buildConverters(b, an)
	c.buildChains(b, an)
	c.buildLinks(b, an)
	c.buildBarriers(b, an)
	return c
}

// ---- domains ----------------------------------------------------------------

func (c *Circuit) buildDomains(b *Board, an *Analysis) {
	// Every ground-role net seeds a domain; parts touching exactly one ground
	// belong to it; bridge parts touch two. Ungrounded parts inherit through
	// signal nets (never through bridge parts or global rails).
	partNets := map[string][]string{}
	netParts := map[string][]string{}
	for _, p := range b.Parts {
		seen := map[string]bool{}
		for _, pd := range p.Pads {
			if pd.Net != "" && !seen[pd.Net] {
				seen[pd.Net] = true
				partNets[p.Ref] = append(partNets[p.Ref], pd.Net)
				netParts[pd.Net] = append(netParts[pd.Net], p.Ref)
			}
		}
	}
	var grounds []string
	for _, np := range an.Nets {
		if np.Role == RoleGround {
			grounds = append(grounds, np.Net)
		}
	}
	sort.Strings(grounds)
	// Ground nets joined by a 0-ohm/net-tie/ferrite (2-pad resistor/inductor
	// between two grounds) are one domain (AGND–GND star point).
	gparent := map[string]string{}
	var gfind func(string) string
	gfind = func(g string) string {
		if gparent[g] == "" || gparent[g] == g {
			gparent[g] = g
			return g
		}
		gparent[g] = gfind(gparent[g])
		return gparent[g]
	}
	for _, p := range b.Parts {
		k := c.Kinds[p.Ref]
		if (k == KindResistor || k == KindInductor || k == KindOther) && len(p.Pads) == 2 {
			a, bb := p.Pads[0].Net, p.Pads[1].Net
			if a != bb && an.Plan(a, b.Rules).Role == RoleGround && an.Plan(bb, b.Rules).Role == RoleGround {
				gparent[gfind(a)] = gfind(bb)
			}
		}
	}
	domOfGround := map[string]string{}
	for _, g := range grounds {
		domOfGround[g] = gfind(g)
	}
	// Mains nets with no ground of their own form a "MAINS" domain.
	hasMains := false
	for _, np := range an.Nets {
		if reMains.MatchString(upper(np.Net)) && np.PadCount >= 1 {
			hasMains = true
		}
	}
	assign := map[string]string{}
	for _, p := range b.Parts {
		var ds []string
		for _, n := range partNets[p.Ref] {
			if d, ok := domOfGround[n]; ok && !containsStr(ds, d) {
				ds = append(ds, d)
			}
			if hasMains && reMains.MatchString(upper(n)) && !containsStr(ds, "MAINS") {
				ds = append(ds, "MAINS")
			}
		}
		if len(ds) == 1 {
			assign[p.Ref] = ds[0]
		}
	}
	// Propagate through signal nets (BFS), not through bridges or rails.
	queue := make([]string, 0, len(assign))
	for ref := range assign {
		queue = append(queue, ref)
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if c.Kinds[ref].Bridges() {
			continue
		}
		for _, n := range partNets[ref] {
			if isGlobalNet(an, n, b.Rules) {
				continue
			}
			for _, other := range netParts[n] {
				if _, done := assign[other]; done || c.Kinds[other].Bridges() {
					continue
				}
				assign[other] = assign[ref]
				queue = append(queue, other)
			}
		}
	}
	// Rails: a part on an unassigned rail joins the domain of other parts on it.
	for _, p := range b.Parts {
		if _, ok := assign[p.Ref]; ok || c.Kinds[p.Ref].Bridges() {
			continue
		}
		for _, n := range partNets[p.Ref] {
			for _, other := range netParts[n] {
				if d, ok := assign[other]; ok && !c.Kinds[other].Bridges() {
					assign[p.Ref] = d
					break
				}
			}
			if _, ok := assign[p.Ref]; ok {
				break
			}
		}
	}
	byID := map[string]*Domain{}
	for _, p := range b.Parts {
		d := assign[p.Ref]
		if d == "" {
			if c.Kinds[p.Ref].Bridges() {
				continue
			}
			d = "UNREFERENCED"
		}
		dom := byID[d]
		if dom == nil {
			dom = &Domain{ID: d, Ground: d}
			if d == "MAINS" || d == "UNREFERENCED" {
				dom.Ground = ""
			}
			byID[d] = dom
		}
		dom.Parts = append(dom.Parts, p.Ref)
		c.DomainOf[p.Ref] = d
	}
	for _, dom := range byID {
		nets := map[string]bool{}
		for _, ref := range dom.Parts {
			for _, n := range partNets[ref] {
				nets[n] = true
			}
		}
		for n := range nets {
			dom.Nets = append(dom.Nets, n)
			v := an.Plan(n, b.Rules).Voltage
			if reMains.MatchString(upper(n)) {
				dom.Mains = true
				v = math.Max(v, 325) // 230 Vrms peak
			}
			dom.MaxVoltage = math.Max(dom.MaxVoltage, v)
		}
		sort.Strings(dom.Nets)
		dom.Hazardous = dom.Mains || dom.MaxVoltage > 60
		c.Domains = append(c.Domains, dom)
	}
	sort.Slice(c.Domains, func(i, j int) bool { return c.Domains[i].ID < c.Domains[j].ID })
	if len(c.Domains) > 1 {
		c.Notes = append(c.Notes, sprintf("%d voltage/reference domains found — placement keeps each in its own zone", len(c.Domains)))
	}
}

func containsStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ---- blocks -----------------------------------------------------------------

func (c *Circuit) isDecap(b *Board, an *Analysis, p *Part) bool {
	if c.Kinds[p.Ref] != KindCapacitor || len(p.Pads) != 2 {
		return false
	}
	r0 := an.Plan(p.Pads[0].Net, b.Rules).Role
	r1 := an.Plan(p.Pads[1].Net, b.Rules).Role
	return (r0 == RoleGround && r1 == RolePower) || (r1 == RoleGround && r0 == RolePower)
}

func (c *Circuit) decapRail(b *Board, an *Analysis, p *Part) string {
	for _, pd := range p.Pads {
		if an.Plan(pd.Net, b.Rules).Role == RolePower {
			return pd.Net
		}
	}
	return ""
}

// coreKind names what a core part does from its kind and connectivity.
func (c *Circuit) coreKind(b *Board, an *Analysis, p *Part) string {
	switch c.Kinds[p.Ref] {
	case KindConnector:
		return "interface"
	case KindAntenna:
		return "rf"
	case KindOpto, KindIsolator, KindIsoPower, KindTransformer, KindRelay:
		return "isolation"
	case KindModule:
		return "mcu"
	}
	rails := map[string]bool{}
	signals := 0
	hasRF, hasClock := false, false
	analog := 0
	for _, pd := range p.Pads {
		np := an.Plan(pd.Net, b.Rules)
		switch np.Role {
		case RolePower:
			rails[pd.Net] = true
		case RoleRF:
			hasRF = true
		case RoleClock:
			hasClock = true
		case RoleAnalog:
			analog++
		case RoleSwitch:
			return "power"
		}
		if np.Role != RolePower && np.Role != RoleGround {
			signals++
		}
	}
	switch {
	case hasRF:
		return "rf"
	case analog >= 2 && !hasClock && analog*2 >= signals:
		return "analog" // op-amp / ADC front end: keep away from switchers
	case len(rails) >= 2 && signals <= 4:
		return "power" // regulator: input rail → output rail
	case hasClock || len(p.Pads) >= 24:
		return "mcu"
	}
	return "logic"
}

// ---- links ------------------------------------------------------------------

var (
	reI2C  = regexp.MustCompile(`(?i)(SDA|SCL|I2C|IIC)`)
	reSPI  = regexp.MustCompile(`(?i)(MOSI|MISO|SCK|SCLK|CS\b|NSS|SPI|SDI|SDO)`)
	reUART = regexp.MustCompile(`(?i)(TXD|RXD|TX\b|RX\b|UART|_TX|_RX)`)
	reUSB  = regexp.MustCompile(`(?i)(USB|D\+|D-|DP|DM)`)
	reI2S  = regexp.MustCompile(`(?i)(I2S|BCLK|LRCK|WS|MCLK|SDOUT|SDIN)`)
)

func netBus(name string, role NetRole) string {
	n := upper(name)
	switch {
	case role == RoleDiff:
		if reUSB.MatchString(n) {
			return "usb"
		}
		return "diff"
	case role == RoleClock:
		return "clock"
	case role == RoleRF:
		return "rf"
	case reI2S.MatchString(n):
		return "i2s"
	case reI2C.MatchString(n):
		return "i2c"
	case reSPI.MatchString(n):
		return "spi"
	case reUART.MatchString(n):
		return "uart"
	case reUSB.MatchString(n):
		return "usb"
	case role == RolePower:
		return "power"
	}
	return "signal"
}

func (c *Circuit) buildLinks(b *Board, an *Analysis) {
	type key struct{ a, b string }
	links := map[key]*Link{}
	for _, n := range b.Nets() {
		np := an.Plan(n.Name, b.Rules)
		if np.Role == RoleGround {
			continue
		}
		blocks := map[string]bool{}
		for _, pd := range n.Pads {
			if bl := c.BlockOf[pd.Part]; bl != "" {
				blocks[bl] = true
			}
		}
		var ids []string
		for id := range blocks {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for i := 0; i < len(ids); i++ {
			for j := i + 1; j < len(ids); j++ {
				k := key{ids[i], ids[j]}
				l := links[k]
				if l == nil {
					l = &Link{From: ids[i], To: ids[j]}
					links[k] = l
				}
				l.Nets = append(l.Nets, n.Name)
				bus := netBus(n.Name, np.Role)
				if !containsStr(l.Kinds, bus) {
					l.Kinds = append(l.Kinds, bus)
				}
			}
		}
	}
	for _, l := range links {
		sort.Strings(l.Kinds)
		c.Links = append(c.Links, l)
	}
	sort.Slice(c.Links, func(i, j int) bool {
		if len(c.Links[i].Nets) != len(c.Links[j].Nets) {
			return len(c.Links[i].Nets) > len(c.Links[j].Nets)
		}
		return c.Links[i].From+c.Links[i].To < c.Links[j].From+c.Links[j].To
	})
}

// ---- barriers ----------------------------------------------------------------

// InsulationDistances returns (creepage, clearance) in mm for a working
// voltage (Vrms or DC) and insulation grade, for pollution degree 2 and
// material group III printed boards at ≤ 2000 m. They are engineering
// defaults derived from the IEC 60664-1 / 62368-1 tables; the report says so
// and a product standard may require more.
func InsulationDistances(workingV float64, grade string) (creepMM, clearMM float64) {
	type row struct{ v, creep, clear float64 }
	// Basic insulation, PD2, MG III (creepage) / OVC II mains clearance.
	table := []row{
		{30, 0.2, 0.2}, {50, 1.2, 0.2}, {100, 1.4, 0.2}, {125, 1.5, 0.5}, {150, 1.6, 1.5},
		{200, 2.0, 1.5}, {250, 2.5, 2.0}, {300, 3.2, 2.0}, {400, 4.0, 3.0}, {600, 6.3, 5.5}, {1000, 10, 8},
	}
	v := math.Abs(workingV)
	pick := table[len(table)-1]
	for i, r := range table {
		if v <= r.v {
			pick = r
			if i > 0 {
				// Linear interpolation for creepage (IEC permits it), step for clearance.
				p := table[i-1]
				t := (v - p.v) / (r.v - p.v)
				pick.creep = p.creep + t*(r.creep-p.creep)
			}
			break
		}
	}
	creepMM, clearMM = pick.creep, pick.clear
	switch grade {
	case "reinforced":
		creepMM, clearMM = 2*creepMM, 2*clearMM
	case "functional":
		creepMM, clearMM = math.Max(0.13, creepMM/2), math.Max(0.13, clearMM/2)
	}
	return
}

func (c *Circuit) buildBarriers(b *Board, an *Analysis) {
	if len(c.Domains) < 2 {
		return
	}
	dom := map[string]*Domain{}
	for _, d := range c.Domains {
		dom[d.ID] = d
	}
	type key struct{ a, b string }
	bars := map[key]*Barrier{}
	for _, p := range b.Parts {
		if !c.Kinds[p.Ref].Bridges() {
			continue
		}
		// Domains this bridge touches, by the ground/mains nets on its pads
		// and by the domains of its signal neighbours.
		touched := map[string]bool{}
		for _, pd := range p.Pads {
			for _, q := range b.Parts {
				if q == p {
					continue
				}
				for _, qd := range q.Pads {
					if pd.Net != "" && qd.Net == pd.Net {
						if d := c.DomainOf[q.Ref]; d != "" && d != "UNREFERENCED" {
							touched[d] = true
						}
					}
				}
			}
		}
		var ds []string
		for d := range touched {
			ds = append(ds, d)
		}
		sort.Strings(ds)
		for i := 0; i < len(ds); i++ {
			for j := i + 1; j < len(ds); j++ {
				k := key{ds[i], ds[j]}
				br := bars[k]
				if br == nil {
					br = &Barrier{A: ds[i], B: ds[j]}
					bars[k] = br
				}
				br.Bridges = append(br.Bridges, p.Ref)
			}
		}
	}
	for _, br := range bars {
		a, bb := dom[br.A], dom[br.B]
		br.WorkingV = math.Max(a.MaxVoltage, bb.MaxVoltage)
		mainsV := br.WorkingV
		if a.Mains || bb.Mains {
			mainsV = math.Max(mainsV/math.Sqrt2, 250) // peak → rms, 250 V class
		}
		switch {
		case (a.Hazardous || bb.Hazardous) && !(a.Hazardous && bb.Hazardous):
			br.Insulation = "reinforced"
			br.Why = append(br.Why, "hazardous voltage to a touchable SELV domain needs reinforced (or double) insulation")
		case a.Hazardous && bb.Hazardous:
			br.Insulation = "basic"
		default:
			br.Insulation = "functional"
			br.Why = append(br.Why, "both sides SELV: functional isolation (noise / ground-loop), not safety")
		}
		cr, cl := InsulationDistances(mainsV, br.Insulation)
		br.CreepageMil, br.ClearanceMil = math.Round(cr/0.0254), math.Round(cl/0.0254)
		br.Why = append(br.Why, sprintf("%s insulation at %.0f V (PD2, MG III): creepage %.2f mm, clearance %.2f mm — engineering default, confirm against the product standard", br.Insulation, mainsV, cr, cl))
		// A bridge whose own pad rows are closer than the creepage needs a
		// milled slot under its body (creepage is measured around the slot).
		for _, ref := range br.Bridges {
			p := b.Part(ref)
			if p == nil {
				continue
			}
			if gap := bridgePadGap(p, c, b); gap > 0 && gap < br.CreepageMil {
				br.SlotUnder = append(br.SlotUnder, ref)
				br.Why = append(br.Why, sprintf("%s pad rows are %.0f mil apart < %.0f mil creepage: mill a ≥1 mm slot under the body", ref, gap, br.CreepageMil))
			}
		}
		c.Barriers = append(c.Barriers, br)
	}
	sort.Slice(c.Barriers, func(i, j int) bool { return c.Barriers[i].A+c.Barriers[i].B < c.Barriers[j].A+c.Barriers[j].B })
}

// bridgePadGap estimates the copper gap between the two sides of a bridge
// part: the split of its pads into the two halves along its longest axis.
func bridgePadGap(p *Part, c *Circuit, b *Board) float64 {
	if len(p.Pads) < 2 {
		return 0
	}
	body := p.Body()
	horiz := body.W() >= body.H()
	cen := body.Center()
	var lo, hi []*Pad
	for _, pd := range p.Pads {
		v := pd.Box.C.Y - cen.Y
		if horiz {
			v = pd.Box.C.X - cen.X
		}
		if v < 0 {
			lo = append(lo, pd)
		} else {
			hi = append(hi, pd)
		}
	}
	best := math.Inf(1)
	for _, a := range lo {
		for _, bb := range hi {
			d := bb.Box.Dist(a.Box.C) - math.Min(a.Box.W, a.Box.H)/2
			best = math.Min(best, d)
		}
	}
	if math.IsInf(best, 1) {
		return 0
	}
	return best
}
