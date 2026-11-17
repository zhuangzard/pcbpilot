package pcbauto

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strconv"
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
	reOpto = regexp.MustCompile(`(?i)(PC8\d\d|EL3\d\d|EL8\d\d|TLP\d|LTV|6N13|HCPL|ACPL|MOC30|OPTO|FOD\d|PS28|K10\d\d)`)
	// Digital isolators, isolated transceivers, isolated gate drivers
	// (UCC215xx, UCC53xx, Si823x/827x, 1EDI/1EDC, STGAP, NCD570x, ADuM4xxx)
	// and isolated amplifiers / modulators (AMC1xxx/3xxx, ISO22x).
	reIsolator = regexp.MustCompile(`(?i)(ADUM|ISO7\d|ISO1\d|ISO35|SI86|SI84|CA-IS|π1|PI1\d\d|NSI|TPT7|ADM2\d\d\dE|MAX1449|ISOW|ISO5\d|UCC215[0-9]\d|UCC53\d\d|UCC57\d\d|SI82[37]\d|1ED[IC]\d|2EDS\d|STGAP\d|NCD57\d|AMC1\d{3}|AMC3\d{3}|ISO22\d)`)
	// Isolated DC/DC modules and isolated bias converters (Murata MGJ/MEJ/NXE/
	// NMV, Recom R..P/REM, Traco TMA/TMR, TI UCC12xxx/UCC14xxx).
	reIsoPower = regexp.MustCompile(`(?i)(^B\d{4}S|^IB\d{4}|URB\d|VRB\d|A\d{4}S|HLK-|HI-LINK|AC-?DC|DC-?DC.*ISO|ISO.*DC-?DC|B0505|B0503|F0505|R1SE|MEE1|^MGJ\d|^MEJ\d|^NXE\d|^NMV\d|^REM\d|^R\d{2}P\d|^TMA\d|^TMR\d|UCC1[24]\d{3})`)
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
	case "C", "CE", "EC", "CB", "CX", "CY":
		// CX / CY: safety (X / Y) capacitors. Left unclassified, a Y capacitor
		// across a barrier counted as a wire and joined the two grounds.
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
	case "T", "TR", "TX":
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
	Kind       string   `json:"kind,omitempty"` // intent domain kind (SELV, mains, patient, …)
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
	SlotWidthMil float64  `json:"slotWidthMil,omitempty"`
	Ref          string   `json:"standardRef,omitempty"`
	Source       string   `json:"source,omitempty"` // inferred | intent | computed
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
	// Bridge marks parts spanning two insulated intent domains that are not
	// an isolator kind (a Y capacitor, a sense resistor chain).
	Bridge map[string]bool `json:"bridgeParts,omitempty"`
	// UnknownWindings are 4-pad inductors on two or more grounds whose
	// windings are not declared: their grounds were not joined.
	UnknownWindings []string `json:"unknownWindings,omitempty"`
	// GroundTies are the parts that joined two ground nets (or a ground and
	// the line) into one domain, in board order: the trail a caller follows
	// when one domain swallowed the board.
	GroundTies []GroundTie `json:"-"`
	// GroundDomain maps every ground net to the domain it ended up in.
	GroundDomain map[string]string `json:"-"`
	// DeclaredNet maps the nets of a declared domain that has no ground
	// net of its own (a primary whose return is a bus, not a "GND") to the
	// domain they are: a ground's domain when the declared nets hang on
	// one, else a domain of their own named after their busiest net.
	DeclaredNet map[string]string `json:"-"`
	// orphanNet maps a declared net that sits on isolation parts only to
	// its domain: those parts bridge that domain too.
	orphanNet map[string]string
	Notes     []string `json:"notes,omitempty"`
}

// GroundTie is one part that joined ground net A to ground net B (Line: B is
// the AC line, A the ground tied to it). Sure marks a tie that is clearly
// meant: a resistor of known low value, a net-tie, a declared winding.
type GroundTie struct {
	Ref  string
	A, B string
	Line bool
	Sure bool
}

// BleederOhms is the resistance from which a resistor between two grounds
// (or from the line to a ground) is a bleeder / divider / protective
// impedance across a barrier, not a tie that makes them one circuit.
const BleederOhms = 100e3

var (
	reOhmPlain = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([mRrkKMG]?)\s*(?:[Ω\x{2126}]|[oO][hH][mM][sS]?|R)?$`)
	reOhmInfix = regexp.MustCompile(`^(\d+)([mRrkKMG])(\d+)\s*[Ω\x{2126}]?$`)
	// reTieRef / reTieDev: the two-pad parts of no recognised kind that are a
	// deliberate connection (net-tie, solder jumper / bridge, 0 Ω link).
	reTieRef = regexp.MustCompile(`^(NT|SJ|SB|JP|W|LK)\d`)
	reTieDev = regexp.MustCompile(`(?i)(NET[-_ ]?TIE|JUMPER|SOLDER.?BRIDGE|^0 ?(R|[Ω\x{2126}]|OHM)$)`)
)

// ResistorOhms reads a plain resistance ("0R", "4K7", "330kΩ", "1M", "100");
// ok is false for anything else (a part number, an empty value).
func ResistorOhms(s string) (ohms float64, ok bool) {
	s = strings.TrimSpace(s)
	if f := strings.Fields(s); len(f) > 1 { // "330 kΩ", "10k 1%"
		if v, ok := ResistorOhms(f[0] + f[1]); ok {
			return v, true
		}
		s = f[0]
	}
	mult := func(u string) float64 {
		switch u {
		case "m":
			return 1e-3
		case "k", "K":
			return 1e3
		case "M":
			return 1e6
		case "G":
			return 1e9
		}
		return 1
	}
	if m := reOhmInfix.FindStringSubmatch(s); m != nil {
		v, err := strconv.ParseFloat(m[1]+"."+m[3], 64)
		return v * mult(m[2]), err == nil
	}
	if m := reOhmPlain.FindStringSubmatch(s); m != nil {
		v, err := strconv.ParseFloat(m[1], 64)
		return v * mult(m[2]), err == nil
	}
	return 0, false
}

// partOhms is the resistance of a resistor part from its Value, else its
// Device when that is a value with a unit (a bare number there is a
// footprint or an order code, not ohms).
func partOhms(p *Part) (float64, bool) {
	if v, ok := ResistorOhms(p.Value); ok {
		return v, true
	}
	if strings.Trim(strings.TrimSpace(p.Device), "0123456789.") == "" {
		return 0, false
	}
	return ResistorOhms(p.Device)
}

// tiesGrounds reports whether a two-pad part between two grounds makes them
// one circuit, and whether that is certain. A resistor does unless it is
// known to be ≥ 100 kΩ (a bleeder or divider across a barrier); one of
// unknown value still does — a ground-to-ground resistor is nearly always a
// 0 Ω star point, and a board often carries only its part number — but is
// not "sure". An inductor / ferrite does. A part of no recognised kind does
// only when it is named as a link (net-tie, solder jumper, 0 Ω): a spark
// gap, a varistor or an unrecognised safety capacitor is not a wire.
func (c *Circuit) tiesGrounds(p *Part) (tie, sure bool) {
	switch c.Kinds[p.Ref] {
	case KindResistor:
		v, ok := partOhms(p)
		return !ok || v < BleederOhms, ok && v <= 10
	case KindInductor:
		return true, false
	case KindOther:
		if reTieRef.MatchString(upper(p.Ref)) || reTieDev.MatchString(strings.TrimSpace(p.Device)) || reTieDev.MatchString(strings.TrimSpace(p.Value)) {
			return true, true
		}
	}
	return false, false
}

var (
	// A safety (X / Y class) capacitor by its designator or its device /
	// value text: it stands across a barrier and says nothing about which
	// side a net belongs to.
	reSafetyCapRef = regexp.MustCompile(`^C[XY]\d`)
	reSafetyCapDev = regexp.MustCompile(`(?i)(^|[^A-Z0-9])[XY][12]([^A-Z0-9]|$)|safety`)
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
	intent := an != nil && an.Iso != nil && len(an.Iso.Domains) > 0
	if intent {
		c.intentDomains(b, an)
	}
	c.buildBlocks(b, an)
	c.buildConverters(b, an)
	c.buildChains(b, an)
	c.buildLinks(b, an)
	if !intent {
		c.buildBarriers(b, an)
	}
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
	// Declared domains win: two grounds declared in different domains are
	// never joined (grp follows the union roots, so not through a third
	// ground either), and a ground declared outside the line is never tied
	// to it.
	grp, grpLine := map[string]int{}, map[int]bool{}
	// onLine: the net is the line by its name — unless it is declared in a
	// domain that is not (a sensor's "N", a motor phase "L").
	offLine := map[string]bool{}
	onLine := func(n string) bool { return !offLine[n] && reMains.MatchString(upper(n)) }
	if an != nil {
		for i, dd := range an.declared {
			grpLine[i+1] = dd.Line
			for _, n := range dd.Nets {
				offLine[n] = offLine[n] || !dd.Line
				if grp[n] == 0 && an.Plan(n, b.Rules).Role == RoleGround {
					grp[n] = i + 1
				}
			}
		}
	}
	// lineGrounds: the line net and the grounds a conducting part sits on.
	// Isolating parts (transformer, opto, isolator, relay), capacitors (X/Y
	// caps) and resistors not known to be below 100 kΩ (the bleeder beside a
	// Y capacitor, a protective impedance; one whose value cannot be read
	// is not taken for a tie to the line, as in lineTied) conduct nothing
	// that ties.
	lineGrounds := func(p *Part) (line string, gs []string) {
		k := c.Kinds[p.Ref]
		if k.Bridges() || k == KindCapacitor {
			return "", nil
		}
		if v, ok := partOhms(p); k == KindResistor && (!ok || v >= BleederOhms) {
			return "", nil
		}
		for _, pd := range p.Pads {
			if onLine(pd.Net) {
				line = pd.Net
			}
			if an.Plan(pd.Net, b.Rules).Role == RoleGround && !slices.Contains(gs, pd.Net) {
				gs = append(gs, pd.Net)
			}
		}
		return line, gs
	}
	if len(grp) > 0 {
		// An undeclared ground on the line counts as declared on it, so a
		// ground declared outside the line cannot absorb it first.
		grpLine[-1] = true
		for _, p := range b.Parts {
			if line, gs := lineGrounds(p); line != "" {
				for _, g := range gs {
					if grp[g] == 0 {
						grp[g] = -1
					}
				}
			}
		}
	}
	union := func(ref, a, bb string) bool {
		ra, rb := gfind(a), gfind(bb)
		if ga, gb := grp[ra], grp[rb]; ga != 0 && gb != 0 && ga != gb && !(grpLine[ga] && grpLine[gb] && (ga == -1 || gb == -1)) {
			c.Notes = append(c.Notes, sprintf("%s: grounds %s and %s are declared in different domains — not joined", ref, a, bb))
			return false
		} else if ra != rb {
			gparent[ra] = rb
			if gb == 0 || (gb == -1 && ga > 0) {
				grp[rb] = ga // a declaration outlives "on the line, undeclared"
			}
		}
		return true
	}
	for _, p := range b.Parts {
		k := c.Kinds[p.Ref]
		if (k == KindResistor || k == KindInductor || k == KindOther) && len(p.Pads) == 2 {
			a, bb := p.Pads[0].Net, p.Pads[1].Net
			if a != bb && an.Plan(a, b.Rules).Role == RoleGround && an.Plan(bb, b.Rules).Role == RoleGround {
				if tie, sure := c.tiesGrounds(p); tie && union(p.Ref, a, bb) {
					c.GroundTies = append(c.GroundTies, GroundTie{Ref: p.Ref, A: a, B: bb, Sure: sure})
				}
			}
		}
		// A 4-pin common-mode choke carries the return line through one
		// winding: the connector-side ground (GND_J) is then the same domain
		// as the board ground (left apart, the inlet nets of a field board
		// formed their own SELV domain with no insulation pair to the patient
		// side). Only DECLARED windings join grounds: a choke wired 1–2 / 3–4
		// or a dual inductor must not merge two separate grounds and so
		// suppress their insulation pair. Undeclared, the grounds stay apart
		// and the part is listed in UnknownWindings.
		if k == KindInductor && len(p.Pads) == 4 {
			byNum := map[string]string{}
			var grounds []string
			for _, pd := range p.Pads {
				byNum[pd.Number] = pd.Net
				if pd.Net != "" && an.Plan(pd.Net, b.Rules).Role == RoleGround && !slices.Contains(grounds, pd.Net) {
					grounds = append(grounds, pd.Net)
				}
			}
			ws := an.windings(p.Ref)
			if ws == nil {
				if len(grounds) >= 2 {
					sort.Strings(grounds)
					c.UnknownWindings = append(c.UnknownWindings, p.Ref)
					c.Notes = append(c.Notes, fmt.Sprintf("%s: 4-pad inductor on grounds %s, windings not declared — its grounds are kept as separate domains (declare its windings, e.g. [[\"1\",\"4\"],[\"2\",\"3\"]], in the intent spec \"windings\" if it carries the return line)", p.Ref, strings.Join(grounds, ", ")))
				}
				continue
			}
			for _, w := range ws {
				a, bb := byNum[w[0]], byNum[w[1]]
				if a != "" && bb != "" && a != bb && an.Plan(a, b.Rules).Role == RoleGround && an.Plan(bb, b.Rules).Role == RoleGround && union(p.Ref, a, bb) {
					c.GroundTies = append(c.GroundTies, GroundTie{Ref: p.Ref, A: a, B: bb, Sure: true})
				}
			}
		}
	}
	// Mains nets with no ground of their own form a "MAINS" domain.
	hasMains := false
	for _, np := range an.Nets {
		if onLine(np.Net) && np.PadCount >= 1 {
			hasMains = true
		}
	}
	// A ground galvanically tied to the line — the DC side of a bridge
	// rectifier, a capacitive dropper's return — is the same hazardous
	// circuit as the line: one domain, no insulation between them. Only
	// isolating parts (transformer, opto, isolator, relay), capacitors
	// (X/Y caps) and resistors known to be ≥ 100 kΩ (the bleeder beside a Y
	// capacitor, a protective impedance) do not tie. Without this the
	// primary of an off-line converter was a separate domain "insulated"
	// from the line by its own bridge rectifier.
	mainsRoot := "MAINS"
	if hasMains {
		var tied []string
		for _, p := range b.Parts {
			line, gs := lineGrounds(p)
			if line == "" {
				continue
			}
			for _, g := range gs {
				if d := grp[gfind(g)]; d != 0 && !grpLine[d] {
					c.Notes = append(c.Notes, sprintf("%s: ground %s is declared outside the line — not tied to %s", p.Ref, g, line))
					continue
				}
				tied = append(tied, g)
				c.GroundTies = append(c.GroundTies, GroundTie{Ref: p.Ref, A: g, B: line, Line: true})
			}
		}
		sort.Strings(tied)
		for _, g := range tied {
			if mainsRoot == "MAINS" {
				mainsRoot = gfind(g)
				continue
			}
			union("line", g, mainsRoot)
			mainsRoot = gfind(mainsRoot)
		}
		if mainsRoot != "MAINS" {
			c.Notes = append(c.Notes, sprintf("ground %s is galvanically tied to the line (rectifier / non-isolating part): one mains domain", mainsRoot))
		}
	}
	domOfGround := map[string]string{}
	for _, g := range grounds {
		domOfGround[g] = gfind(g)
	}
	c.GroundDomain = domOfGround
	// A declared domain without a ground net is a reference domain all the
	// same: its nets seed it as a ground would. (Without this its parts had
	// no reference, and the first capacitor from one of its nets to another
	// circuit carried that circuit's domain over all of it.) When most
	// of the parts on its nets sit on one ground, it is that ground's
	// domain — a rail declared to give its domain a kind — and nothing
	// changes. The parts that count are those on three nets or more that
	// are no capacitor (a controller, a transistor: a part with a reference
	// pin: an IC, a transistor, a module), never an isolation part, which
	// belongs to no side. The Y
	// capacitor or the bleeder across the barrier looks just like a rail's
	// decoupling capacitor, so the other parts count only where no such
	// part does and the declaration is not the line's — and then never a
	// safety capacitor by its name, nor a resistor that would not tie two
	// grounds.
	declOf := map[string]string{}
	partOf := map[string]*Part{}
	for _, p := range b.Parts {
		partOf[p.Ref] = p
	}
	// refPin: the part has a reference pin; passive: it may stand for one
	// in the fallback.
	// Both lists are closed: a part of a kind not named here (a resistor
	// network, a spark gap, a part nothing is known about) sides no net.
	safety := func(ref string) bool {
		p := partOf[ref]
		return reSafetyCapRef.MatchString(upper(ref)) || reSafetyCapDev.MatchString(p.Device) || reSafetyCapDev.MatchString(p.Value)
	}
	refPin := func(ref string) bool {
		switch c.Kinds[ref] {
		case KindIC, KindTransistor, KindModule:
			return !safety(ref) && len(partNets[ref]) >= 3
		}
		return false
	}
	passive := func(ref string) bool {
		// One element, two pads: a network or an array, bussed or isolated,
		// is no one part's word, and its value is no one path's.
		if safety(ref) || len(partNets[ref]) != 2 || len(partOf[ref].Pads) != 2 {
			return false
		}
		switch c.Kinds[ref] {
		case KindCapacitor:
			return true
		case KindResistor:
			p := partOf[ref]
			tie, _ := c.tiesGrounds(p)
			v, ok := partOhms(p)
			return tie && ok && v < BleederOhms
		}
		return false
	}
	if an != nil {
		for _, dd := range an.declared {
			grounded := false
			for _, n := range dd.Nets {
				_, ok := domOfGround[n]
				grounded = grounded || ok
			}
			if grounded {
				continue
			}
			on := map[string]bool{}
			busiest := ""
			for _, n := range dd.Nets {
				for _, ref := range netParts[n] {
					on[ref] = true
				}
				// A net an earlier declaration holds does not name this
				// one: the two would become one domain.
				if _, taken := declOf[n]; taken {
					continue
				}
				if len(netParts[n]) > len(netParts[busiest]) || len(netParts[n]) == len(netParts[busiest]) && len(netParts[n]) > 0 && n < busiest {
					busiest = n
				}
			}
			if busiest == "" {
				continue
			}
			vote := func(counts func(string) bool, groundedOnly bool) (map[string]int, int) {
				byGround, sided := map[string]int{}, 0
				for ref := range on {
					if c.Kinds[ref].Bridges() || !counts(ref) {
						continue
					}
					seen := map[string]bool{}
					for _, n := range partNets[ref] {
						if d, ok := domOfGround[n]; ok && !seen[d] {
							seen[d] = true
							byGround[d]++
						}
					}
					if len(seen) > 0 || !groundedOnly {
						sided++
					}
				}
				return byGround, sided
			}
			byGround, sided := vote(refPin, false)
			if sided == 0 && !dd.Line {
				// No part with a reference pin: the others that sit on a
				// ground decide among themselves.
				byGround, sided = vote(passive, true)
			}
			// One ground with a strict majority names the domain. A part
			// on two grounds votes for both, so two may reach a majority:
			// then none does and the declaration is its own domain.
			id, winners := busiest, map[string]bool{}
			for _, g := range grounds {
				if d := domOfGround[g]; 2*byGround[d] > sided {
					winners[d] = true
				}
			}
			if len(winners) == 1 {
				for d := range winners {
					id = d
				}
			}
			for _, n := range dd.Nets {
				if _, taken := declOf[n]; !taken && len(netParts[n]) > 0 {
					declOf[n] = id
				}
			}
			if id == busiest {
				c.Notes = append(c.Notes, sprintf("declared domain without a ground net: its %d nets form reference domain %s", len(dd.Nets), id))
			}
		}
	}
	c.DeclaredNet = declOf
	assign := map[string]string{}
	for _, p := range b.Parts {
		// An isolation part never belongs to one side, even when only one
		// side's reference is a ground-named net: an isolated gate driver
		// (VSSA = the switch node's Kelvin source) or a bias module (0 V =
		// that source) used to join the controller's GND domain and drag
		// the whole gate drive into SELV.
		if c.Kinds[p.Ref].Bridges() {
			continue
		}
		var ds []string
		for _, n := range partNets[p.Ref] {
			if d, ok := domOfGround[n]; ok && !containsStr(ds, d) {
				ds = append(ds, d)
			}
			if d, ok := declOf[n]; ok && !containsStr(ds, d) {
				ds = append(ds, d)
			}
			if hasMains && onLine(n) && !containsStr(ds, mainsRoot) {
				ds = append(ds, mainsRoot)
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
			if d, ok := declOf[n]; ok && d != assign[ref] {
				continue // a declared net carries only its own domain
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
				if d, ok := assign[other]; ok && !c.Kinds[other].Bridges() && (declOf[n] == "" || declOf[n] == d) {
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
	// A declared net that only isolation parts sit on (an isolator's output
	// into an isolated converter) has no part to bring it into a domain: it
	// is in the one it was declared in.
	held := map[string]bool{}
	for _, dom := range byID {
		for _, ref := range dom.Parts {
			for _, n := range partNets[ref] {
				held[n] = true
			}
		}
	}
	orphans := map[string][]string{}
	if an != nil {
		for _, dd := range an.declared {
			id := ""
			for _, n := range dd.Nets {
				if d, ok := domOfGround[n]; ok && id == "" {
					id = d
				}
			}
			for _, n := range dd.Nets {
				d := id
				if d == "" {
					d = declOf[n]
				}
				if d == "" || held[n] || len(netParts[n]) == 0 {
					continue
				}
				if byID[d] == nil {
					// Nothing but isolation parts on the whole declaration:
					// the domain exists all the same, without a part.
					byID[d] = &Domain{ID: d, Ground: d}
				}
				held[n] = true
				orphans[d] = append(orphans[d], n)
				if c.orphanNet == nil {
					c.orphanNet = map[string]string{}
				}
				c.orphanNet[n] = d
			}
		}
	}
	for _, dom := range byID {
		nets := map[string]bool{}
		for _, ref := range dom.Parts {
			for _, n := range partNets[ref] {
				nets[n] = true
			}
		}
		for _, n := range orphans[dom.ID] {
			nets[n] = true
		}
		for n := range nets {
			dom.Nets = append(dom.Nets, n)
			v := an.Plan(n, b.Rules).Voltage
			if onLine(n) {
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
			if d := c.orphanNet[pd.Net]; d != "" {
				touched[d] = true
			}
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
