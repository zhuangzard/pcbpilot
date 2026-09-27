package intent

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

var (
	// reMains mirrors pcbauto's unambiguous AC line names.
	reMains     = regexp.MustCompile(`(?i)^(AC_?L|AC_?N|L_?IN|N_?IN|LINE|NEUTRAL|MAINS|AC\d*|~|220V|230V|110V|120V|VAC|L|N|PE)$`)
	reCapDesc   = regexp.MustCompile(`(?i)Capacitance:\s*([0-9.]+\s*[pnuµμm]?)F`)
	reUSBUART   = regexp.MustCompile(`(?i)(CH34[0-3]|CH910|CP210|FT23|FT2232|PL2303|GD32VF103.*USB|CH9102|CH9340)`)
	reCharger   = regexp.MustCompile(`(?i)(TP40\d\d|TP5100|BQ24|BQ25|MCP738|LTC40\d\d|IP5\d{3}|CN30\d\d|ETA40\d\d|SGM4105|MAX1555|LTC4054|XT4052|HX6001)`)
	reMotor     = regexp.MustCompile(`(?i)(DRV8\d|A4988|A4950|TB6612|TB67|L298|L293|L9110|TMC2\d|MP6550|BTS79|AT8236|RZ7899|DRV10|IR2104|IR2110)`)
	reSensor    = regexp.MustCompile(`(?i)(BME\d|BMP\d|SHT\d|AHT\d|MPU\d|ICM\d|LIS\d|ADXL|INA2\d|INA1\d|MAX3010|HX711|DS18B|VL53|QMC|HMC\d|LSM\d|TMP1\d|LM75|SGP\d|SCD4|HDC\d|BH1750|OPT3001|AS5600)`)
	reRFModule  = regexp.MustCompile(`(?i)(ESP32|ESP8266|ESP-|WROOM|WROVER|NRF5|BLE|WIFI|LORA|SX12|RA-0|SIM\d|EC2\d|BC\d\d|HC-0|RFM\d|CC2\d|W800|BL60|RTL87)`)
	reAutoDLSrc = regexp.MustCompile(`(?i)(DTR|RTS)`)
	reAutoDLDst = regexp.MustCompile(`(?i)^(EN|CHIP_?EN|CHIP_PU|IO0|GPIO0|BOOT\d?|RST|NRST|RESET)$`)
	reUSBData   = regexp.MustCompile(`(?i)(^|[^A-Z])(D[+-]|DP\d?|DM\d?|DN\d?|UD[+-])$`)
	reUART      = regexp.MustCompile(`(?i)(TXD|RXD|^TX|^RX)`)
)

// draft is a block under construction.
type draft struct {
	base, function, sub, core string
	parts                     []string
	notes, why                []string
}

func (d *draft) has(ref string) bool { return has(d.parts, ref) }

// buildBlocks classifies what every circuit is for. It starts from pcbauto's
// core/auxiliary blocks (Understand) and carves recognised sub-functions out
// of them: the power input with its OR-ing diodes, ESD arrays, LED
// indicators, user keys, auto-download transistors.
func (c *ctx) buildBlocks() {
	var drafts []*draft
	owner := map[string]*draft{}
	move := func(ref string, to *draft) {
		if from := owner[ref]; from != nil {
			for i, r := range from.parts {
				if r == ref {
					from.parts = append(from.parts[:i], from.parts[i+1:]...)
					break
				}
			}
		}
		if !to.has(ref) {
			to.parts = append(to.parts, ref)
		}
		owner[ref] = to
	}
	byCore := map[string]*draft{}
	for _, bl := range c.circ.Blocks {
		d := &draft{core: bl.Core}
		if bl.Core != "" {
			d.why = append(d.why, fmt.Sprintf("pcbauto core %s (%s block, %d parts)", bl.Core, bl.Kind, len(bl.Parts)))
			byCore[bl.Core] = d
		}
		for _, m := range bl.Members {
			if m.Role != "unassigned" {
				d.why = append(d.why, m.Ref+": "+m.Role+" — "+m.Why)
			}
		}
		drafts = append(drafts, d)
		for _, ref := range bl.Parts {
			move(ref, d)
		}
	}

	// 1. Power input: input sources, OR-ing diodes, input protection.
	var sources []string
	for _, p := range c.d.Parts {
		if c.simKind[p.Ref] == powersim.KindSource {
			sources = append(sources, p.Ref)
		}
	}
	if len(sources) > 0 {
		pin := &draft{base: "POWER_IN", function: "power-input"}
		inNets := map[string]bool{}
		for _, s := range sources {
			for _, n := range c.partNets(s) {
				if c.isPowerNet(n) && !c.isGround(n) {
					inNets[n] = true
				}
			}
		}
		// Diodes carrying the input onto a shared rail (OR-ing / reverse polarity).
		var ors []string
		for changed := true; changed; {
			changed = false
			for _, p := range c.d.Parts {
				if c.simKind[p.Ref] != powersim.KindDiode || has(ors, p.Ref) {
					continue
				}
				ns := c.partNets(p.Ref)
				if len(ns) != 2 || c.isGround(ns[0]) || c.isGround(ns[1]) || !c.isPowerNet(ns[0]) || !c.isPowerNet(ns[1]) {
					continue // a flyback / signal diode, not a supply path
				}
				if inNets[ns[0]] || inNets[ns[1]] {
					ors = append(ors, p.Ref)
					inNets[ns[0]], inNets[ns[1]] = true, true
					changed = true
				}
			}
		}
		for _, p := range c.d.Parts {
			k := c.kind(p.Ref)
			if k != pcbauto.KindFuse && !(k == pcbauto.KindInductor && strings.HasPrefix(strings.ToUpper(p.Ref), "FB")) {
				continue
			}
			for _, n := range c.partNets(p.Ref) {
				if inNets[n] {
					ors = append(ors, p.Ref)
					for _, m := range c.partNets(p.Ref) {
						inNets[m] = true
					}
					break
				}
			}
		}
		core := ""
		for _, s := range sources {
			powerOnly := true
			for _, n := range c.partNets(s) {
				if !c.isGround(n) && !c.isPowerNet(n) {
					powerOnly = false
				}
			}
			if powerOnly {
				if core == "" {
					core = s
				}
				if d := byCore[s]; d != nil {
					for _, ref := range append([]string(nil), d.parts...) {
						move(ref, pin)
					}
				} else {
					move(s, pin)
				}
			}
		}
		for _, ref := range ors {
			move(ref, pin)
		}
		// Clamps (TVS) on an input net.
		for _, p := range c.d.Parts {
			if c.simKind[p.Ref] != powersim.KindESD || len(p.Pins) > 2 {
				continue
			}
			for _, n := range c.partNets(p.Ref) {
				if inNets[n] {
					move(p.Ref, pin)
				}
			}
		}
		if core == "" && len(ors) > 0 {
			core = ors[0]
		}
		pin.core = core
		if len(pin.parts) > 0 {
			if len(sources) > 1 && len(ors) > 0 {
				pin.sub = "or-ing"
			}
			pin.why = append(pin.why, "input sources (sim connector-source): "+strings.Join(sources, ", "))
			if len(ors) > 0 {
				pin.why = append(pin.why, "series/OR-ing parts on the input nets: "+strings.Join(sortRefs(ors), ", "))
			}
			drafts = append(drafts, pin)
		}
	}

	// 2. ESD / TVS arrays (≥3 pins) get their own block.
	for _, p := range c.d.Parts {
		if len(p.Pins) < 3 || !(c.simKind[p.Ref] == powersim.KindESD || c.kind(p.Ref) == pcbauto.KindDiode && reESDName.MatchString(firstNonEmpty(p.MPN, p.DeviceName, p.Value))) {
			continue
		}
		d := &draft{base: "ESD", function: "esd", core: p.Ref, why: []string{p.Ref + ": multi-line ESD/TVS array"}}
		move(p.Ref, d)
		drafts = append(drafts, d)
	}

	// 3. LED indicators with their series resistors.
	for _, p := range c.d.Parts {
		if c.kind(p.Ref) != pcbauto.KindLED && !(c.simKind[p.Ref] == powersim.KindLED && len(p.Pins) <= 3 && !c.kind(p.Ref).Bridges()) {
			continue // an optocoupler's input LED is an isolation part
		}
		d := &draft{base: "LED", function: "led", core: p.Ref, why: []string{p.Ref + ": LED"}}
		move(p.Ref, d)
		for _, n := range c.partNets(p.Ref) {
			if c.isGround(n) || c.isPowerNet(n) {
				continue
			}
			for _, r := range c.partsOn(n) {
				if r != p.Ref && c.kind(r) == pcbauto.KindResistor {
					move(r, d)
					d.why = append(d.why, r+": series resistor on "+n)
				}
			}
		}
		drafts = append(drafts, d)
	}

	// 4. User keys: switches with their pull resistors and RC caps.
	var keys *draft
	for _, p := range c.d.Parts {
		if c.kind(p.Ref) != pcbauto.KindSwitch {
			continue
		}
		if keys == nil {
			keys = &draft{base: "KEYS", function: "other", sub: "keys"}
			drafts = append(drafts, keys)
		}
		move(p.Ref, keys)
		for _, n := range c.partNets(p.Ref) {
			if c.isGround(n) || c.isPowerNet(n) {
				continue
			}
			for _, r := range c.partsOn(n) {
				k := c.kind(r)
				if r == p.Ref || (k != pcbauto.KindResistor && k != pcbauto.KindCapacitor) {
					continue
				}
				ns := c.partNets(r)
				if len(ns) == 2 && (c.isGround(ns[0]) || c.isGround(ns[1]) || c.isPowerNet(ns[0]) || c.isPowerNet(ns[1])) {
					move(r, keys)
					keys.why = append(keys.why, fmt.Sprintf("%s: %s on key net %s", r, map[bool]string{true: "pull resistor", false: "RC/debounce capacitor"}[k == pcbauto.KindResistor], n))
				}
			}
		}
	}
	if keys != nil {
		keys.core = keys.parts[0]
	}

	// 5. Auto-download (DTR/RTS → EN/IO0 transistor pair).
	var adl *draft
	for _, p := range c.d.Parts {
		if c.kind(p.Ref) != pcbauto.KindTransistor {
			continue
		}
		src, dst := false, false
		for _, n := range c.partNets(p.Ref) {
			if reAutoDLSrc.MatchString(n) {
				src = true
			}
			if reAutoDLDst.MatchString(n) {
				dst = true
			}
			for _, pr := range c.netPins[n] {
				if pr.Ref != p.Ref && reAutoDLSrc.MatchString(pr.Name) {
					src = true
				}
				if pr.Ref != p.Ref && reAutoDLDst.MatchString(pr.Name) {
					dst = true
				}
			}
		}
		if !src || !dst {
			continue
		}
		if adl == nil {
			adl = &draft{base: "AUTO_DOWNLOAD", function: "other", sub: "auto-download"}
			drafts = append(drafts, adl)
		}
		move(p.Ref, adl)
		adl.why = append(adl.why, p.Ref+": transistor between DTR/RTS and EN/IO0")
		// Base resistors: a resistor on the base net.
		base := c.pinNet(p.Ref, "B", "1")
		for _, r := range c.partsOn(base) {
			if c.kind(r) == pcbauto.KindResistor {
				move(r, adl)
				adl.why = append(adl.why, r+": base resistor of "+p.Ref)
			}
		}
	}
	if adl != nil {
		adl.core = adl.parts[0]
	}

	// 6. Leftovers (pcbauto MISC or orphans): a part sharing a non-ground
	// net (rails included) with a block's core joins that block (a bulk cap
	// at a supply connector); the rest is MISC.
	var misc *draft
	for _, p := range c.d.Parts {
		if d := owner[p.Ref]; d != nil && d.core != "" {
			continue
		} else if d != nil && d.base != "" {
			continue
		}
		joined := false
		for _, n := range c.partNets(p.Ref) {
			if c.isGround(n) || joined {
				continue
			}
			for _, q := range c.partsOn(n) {
				if d := owner[q]; d != nil && d.core == q && q != p.Ref {
					move(p.Ref, d)
					d.why = append(d.why, p.Ref+": shares "+n+" with "+q)
					joined = true
					break
				}
			}
		}
		if joined {
			continue
		}
		if misc == nil {
			misc = &draft{base: "MISC", function: "other", sub: "unassigned"}
			drafts = append(drafts, misc)
		}
		move(p.Ref, misc)
	}

	// Function of the remaining core blocks.
	for _, d := range drafts {
		if d.function != "" || d.core == "" {
			continue
		}
		d.function, d.sub, d.base = c.coreFunction(d.core)
	}

	// Materialise with unique ids.
	var live []*draft
	for _, d := range drafts {
		if len(d.parts) > 0 {
			if !d.has(d.core) {
				d.core = d.parts[0]
			}
			live = append(live, d)
		}
	}
	count := map[string]int{}
	for _, d := range live {
		count[d.base]++
	}
	c.blockOfPart = map[string]string{}
	c.blockByID = map[string]*Block{}
	for _, d := range live {
		id := d.base
		if count[d.base] > 1 {
			id = d.base + "_" + sanitizeID(d.core)
		}
		for c.blockByID[id] != nil {
			id += "_X"
		}
		parts := append([]string{d.core}, sortRefs(without(d.parts, d.core))...)
		bl := &Block{ID: id, Function: d.function, SubFunction: d.sub, Core: d.core, Parts: parts, Nets: []string{}, Notes: []string{}, Why: d.why}
		for _, ref := range parts {
			c.blockOfPart[ref] = id
		}
		c.blockByID[id] = bl
		c.out.Blocks = append(c.out.Blocks, bl)
	}
	sort.SliceStable(c.out.Blocks, func(i, j int) bool {
		ri, rj := functionRank(c.out.Blocks[i].Function), functionRank(c.out.Blocks[j].Function)
		if ri != rj {
			return ri < rj
		}
		return c.out.Blocks[i].ID < c.out.Blocks[j].ID
	})
	c.assignNetBlocks()
	for _, bl := range c.out.Blocks {
		pw := 0.0
		for _, ref := range bl.Parts {
			if pr := c.partW(ref); pr != nil && pr.PowerW > 0 {
				pw += pr.PowerW
			}
		}
		bl.PowerW = round(pw, 4)
		bl.Summary, bl.Notes = c.summarize(bl)
		if bl.Notes == nil {
			bl.Notes = []string{}
		}
	}
}

var reESDName = regexp.MustCompile(`(?i)(ESD|TVS|PESD|USBLC|SRV0|LESD|ULC\d|PRTR|RCLAMP|TPD\d|IP4220|NUP\d)`)

func without(xs []string, v string) []string {
	var out []string
	for _, x := range xs {
		if x != v {
			out = append(out, x)
		}
	}
	return out
}

func functionRank(f string) int {
	order := []string{"mains", "power-input", "charger", "buck", "boost", "ldo", "isolation", "mcu", "rf-module", "usb-uart", "connector", "esd", "sensor", "motor-driver", "led", "other"}
	for i, o := range order {
		if o == f {
			return i
		}
	}
	return len(order)
}

var reNonID = regexp.MustCompile(`[^A-Z0-9]+`)

func sanitizeID(s string) string {
	s = strings.Trim(reNonID.ReplaceAllString(strings.ToUpper(s), "_"), "_")
	if s == "" {
		return "X"
	}
	return s
}

// railLabel renders "+3V3" → "3V3", 12.0 → "12V".
func railLabel(net string, v float64) string {
	if id := sanitizeID(net); pcbauto.InferVoltage(net) > 0 {
		return id
	}
	return voltLabel(v)
}

func voltLabel(v float64) string {
	v = math.Round(v*10) / 10
	if v == math.Trunc(v) {
		return fmt.Sprintf("%.0fV", v)
	}
	s := fmt.Sprintf("%.1f", v)
	return strings.Replace(s, ".", "V", 1)
}

// coreFunction classifies a core part.
func (c *ctx) coreFunction(ref string) (function, sub, base string) {
	p := c.parts[ref]
	dev := firstNonEmpty(p.MPN, p.DeviceName, p.Value, c.modelID[ref])
	switch c.simKind[ref] {
	case powersim.KindBuck:
		out := c.regOutRail(ref)
		f := "buck"
		for _, cv := range c.circ.Converters {
			if cv.Core == ref && cv.Topology == "boost" {
				f = "boost"
			}
		}
		return f, "", strings.ToUpper(f) + "_" + railLabel(out, c.netVoltage(out))
	case powersim.KindLDO:
		out := c.regOutRail(ref)
		return "ldo", "", "LDO_" + railLabel(out, c.netVoltage(out))
	}
	for _, cv := range c.circ.Converters {
		if cv.Core == ref {
			return cv.Topology, "", strings.ToUpper(cv.Topology) + "_" + railLabel(cv.OutRail, c.netVoltage(cv.OutRail))
		}
	}
	switch {
	case reCharger.MatchString(dev):
		return "charger", "", "CHARGER"
	case reMotor.MatchString(dev):
		return "motor-driver", "", "MOTOR_DRV"
	case reSensor.MatchString(dev):
		return "sensor", "", "SENSOR"
	}
	k := c.kind(ref)
	if k.Bridges() {
		return "isolation", string(k), "ISO"
	}
	switch k {
	case pcbauto.KindConnector:
		for _, n := range c.partNets(ref) {
			if c.mains[n] {
				return "mains", "", "MAINS_IN"
			}
		}
		return "connector", "", "CONN_" + sanitizeID(ref)
	case pcbauto.KindAntenna:
		return "other", "antenna", "ANT"
	case pcbauto.KindModule:
		if reRFModule.MatchString(dev) {
			return "rf-module", "", "RF_MODULE"
		}
		return "mcu", "", "MCU"
	}
	if reUSBUART.MatchString(dev) || c.hasUSBAndUART(ref) {
		return "usb-uart", "", "USB_UART"
	}
	if reRFModule.MatchString(dev) && len(p.Pins) >= 16 {
		return "rf-module", "", "RF_MODULE"
	}
	for _, bl := range c.circ.Blocks {
		if bl.Core == ref && bl.Kind == "mcu" {
			return "mcu", "", "MCU"
		}
	}
	return "other", "", "IC_" + sanitizeID(ref)
}

func (c *ctx) hasUSBAndUART(ref string) bool {
	usb, uart := false, false
	for _, pin := range c.parts[ref].Pins {
		usb = usb || reUSBData.MatchString(pin.Name)
		uart = uart || reUART.MatchString(pin.Name)
	}
	return usb && uart
}

// regOutRail is the rail a regulator delivers (after its inductor for a buck).
func (c *ctx) regOutRail(ref string) string {
	for _, cv := range c.circ.Converters {
		if cv.Core == ref && cv.OutRail != "" {
			return cv.OutRail
		}
	}
	if c.worst != nil {
		for k, rp := range c.worst.Ripple {
			if rp.Regulator == ref && c.kind(k) == pcbauto.KindInductor {
				for _, n := range c.partNets(k) {
					if c.isPowerNet(n) {
						return n
					}
				}
			}
		}
	}
	if n := c.pinNet(ref, "VOUT", "OUT", "VO"); n != "" {
		return n
	}
	return ""
}

func (c *ctx) regInRail(ref string) string {
	for _, cv := range c.circ.Converters {
		if cv.Core == ref && cv.InRail != "" {
			return cv.InRail
		}
	}
	return c.pinNet(ref, "IN", "VIN", "VI", "PVIN", "VCC")
}

// netVoltage is the typical (else worst) simulated voltage of a net.
func (c *ctx) netVoltage(net string) float64 {
	if vs, typ, ok := c.scenarioVoltages(net); ok {
		return typ
	} else if len(vs) > 0 {
		return vs[0]
	}
	return pcbauto.InferVoltage(net)
}

// assignNetBlocks decides which block owns each net.
func (c *ctx) assignNetBlocks() {
	pinBlock := c.blockByID["POWER_IN"]
	netBlock := map[string]string{}
	for _, net := range c.d.Nets() {
		if c.isGround(net) {
			continue
		}
		// Power nets belong to the block that sources them (largest source pin).
		if c.isPowerNet(net) || c.an.Plan(net, c.rules).Role == pcbauto.RoleSwitch {
			if nr := c.netW(net); nr != nil {
				best, bestA := "", 0.0
				for _, p := range nr.Pins {
					if p.Dir == "source" && p.CurrentA > bestA {
						best, bestA = c.blockOfPart[p.Ref], p.CurrentA
					}
				}
				if best != "" {
					// A connector feeding the input OR-ing belongs to POWER_IN.
					if pinBlock != nil && best != pinBlock.ID {
						for _, ref := range c.partsOn(net) {
							if c.blockOfPart[ref] == pinBlock.ID && c.simKind[ref] != powersim.KindESD {
								best = pinBlock.ID
							}
						}
					}
					netBlock[net] = best
					continue
				}
			}
		}
		count := map[string]int{}
		for _, pr := range c.netPins[net] {
			count[c.blockOfPart[pr.Ref]]++
		}
		best, bestN := "", -1
		for _, id := range sortedKeys(count) {
			n := count[id]
			if bl := c.blockByID[id]; bl != nil && c.onNet(bl.Core, net) {
				// The block whose core is on the net owns it; an IC/module
				// core outranks a connector, which outranks a key or LED.
				switch c.kind(bl.Core) {
				case pcbauto.KindIC, pcbauto.KindModule:
					n += 100
				case pcbauto.KindConnector:
					n += 50
				default:
					n += 10
				}
			}
			if n > bestN {
				best, bestN = id, n
			}
		}
		netBlock[net] = best
	}
	for net, id := range netBlock {
		if bl := c.blockByID[id]; bl != nil {
			bl.Nets = append(bl.Nets, net)
		}
	}
	for _, bl := range c.out.Blocks {
		sort.Strings(bl.Nets)
	}
	c.netBlock = netBlock
}

func (c *ctx) onNet(ref, net string) bool {
	for _, pr := range c.netPins[net] {
		if pr.Ref == ref {
			return true
		}
	}
	return false
}

// summarize writes the block's one-line purpose from the simulation.
func (c *ctx) summarize(bl *Block) (string, []string) {
	var notes []string
	p := c.parts[bl.Core]
	dev := firstNonEmpty(p.MPN, p.DeviceName, p.Value, c.modelID[bl.Core])
	switch bl.Function {
	case "buck", "boost", "ldo":
		in, out := c.regInRail(bl.Core), c.regOutRail(bl.Core)
		pr := c.partW(bl.Core)
		typ := c.partScen("typical", bl.Core)
		var s strings.Builder
		vinLo, vinHi := c.envelope(in)
		fmt.Fprintf(&s, "%s %s → %s %s ", in, rangeV(vinLo, vinHi), out, fmtV(c.netVoltage(out)))
		switch bl.Function {
		case "buck":
			sync := "synchronous"
			for _, cv := range c.circ.Converters {
				if cv.Core == bl.Core && cv.Diode != "" {
					sync = "non-synchronous (catch diode " + cv.Diode + ")"
				}
			}
			s.WriteString(sync + " buck")
		case "boost":
			s.WriteString("boost")
		default:
			s.WriteString("LDO")
		}
		fmt.Fprintf(&s, " (%s)", dev)
		if pr != nil {
			fmt.Fprintf(&s, ", %s peak", fmtA(pr.OutputA))
			if typ != nil {
				fmt.Fprintf(&s, " / %s typical", fmtA(typ.OutputA))
			}
			if pr.Efficiency > 0 {
				fmt.Fprintf(&s, ", η %.0f%%", pr.Efficiency*100)
			}
			fmt.Fprintf(&s, ", loss %s", fmtW(pr.PowerW))
			notes = append(notes, pr.Notes...)
		}
		return s.String(), notes
	case "power-input":
		var srcs []string
		for _, ref := range c.sortedParts(func(r string) bool { return c.simKind[r] == powersim.KindSource }) {
			var nets []string
			for _, n := range c.partNets(ref) {
				if !c.isGround(n) && c.isPowerNet(n) {
					nets = append(nets, n)
				}
			}
			if len(nets) == 0 {
				continue
			}
			name := ""
			if m := c.model[ref]; m != nil && m.SourceName != "" {
				name = ", " + m.SourceName
			}
			srcs = append(srcs, fmt.Sprintf("%s (%s%s)", ref, strings.Join(nets, "/"), name))
		}
		var ors, clamps []string
		out := ""
		for _, ref := range bl.Parts {
			switch c.simKind[ref] {
			case powersim.KindDiode:
				ors = append(ors, ref)
				if nr := c.worst; nr != nil {
					for _, n := range c.partNets(ref) {
						for _, pp := range nr.Nets[n].Pins {
							if pp.Ref == ref && pp.Dir == "source" {
								out = n
							}
						}
					}
				}
			case powersim.KindESD:
				clamps = append(clamps, ref)
			}
		}
		var s strings.Builder
		s.WriteString(strings.Join(srcs, " + "))
		if len(ors) > 0 {
			verb := "through"
			if len(srcs) > 1 {
				verb = "OR-ed by"
			}
			fmt.Fprintf(&s, " %s %s (%s)", verb, strings.Join(ors, ", "), firstNonEmpty(c.parts[ors[0]].MPN, c.modelID[ors[0]]))
			if out != "" {
				fmt.Fprintf(&s, " onto %s", out)
			}
		}
		if out == "" && len(srcs) > 0 {
			out = strings.Split(strings.TrimSuffix(strings.SplitN(srcs[0], "(", 2)[1], ")"), ",")[0]
		}
		if nr := c.netW(out); nr != nil {
			fmt.Fprintf(&s, "; %s worst (%s)", fmtA(nr.CurrentA), nr.Scenario)
		}
		loss := 0.0
		for _, ref := range ors {
			if pr := c.partW(ref); pr != nil {
				loss = math.Max(loss, pr.PowerW)
			}
		}
		if loss > 0 {
			fmt.Fprintf(&s, ", diode loss ≤ %s each", fmtW(loss))
		}
		if len(clamps) > 0 {
			fmt.Fprintf(&s, "; TVS %s", strings.Join(clamps, ", "))
		}
		return s.String(), notes
	case "usb-uart":
		rail, ia := c.supplyOf(bl.Core)
		var uart []string
		for _, pin := range p.Pins {
			if reUART.MatchString(pin.Name) && pin.Net != "" {
				uart = append(uart, pin.Net)
			}
		}
		return fmt.Sprintf("%s USB↔UART bridge on %s (%s peak); UART %s", dev, rail, fmtA(ia), strings.Join(uniq(uart), "/")), notes
	case "mcu", "rf-module":
		rail, ia := c.supplyOf(bl.Core)
		typA := 0.0
		if t := c.scenRes("typical"); t != nil && t.Nets[rail] != nil {
			for _, pp := range t.Nets[rail].Pins {
				if pp.Ref == bl.Core && pp.Dir == "sink" {
					typA += pp.CurrentA
				}
			}
		}
		what := "MCU"
		if bl.Function == "rf-module" {
			what = "RF/MCU module"
		}
		s := fmt.Sprintf("%s %s on %s: %s typical, %s peak", dev, what, rail, fmtA(typA), fmtA(ia))
		if pr := c.partW(bl.Core); pr != nil && pr.PowerW > 0 {
			s += fmt.Sprintf(" (%s)", fmtW(pr.PowerW))
		}
		if c.conf[bl.Core] == "assumed" || strings.HasPrefix(c.modelID[bl.Core], "generic-") {
			notes = append(notes, "load current is ASSUMED (no power model) — add one to power-models.json")
		}
		return s, notes
	case "led":
		var rs []string
		drv := ""
		ia := 0.0
		for _, ref := range bl.Parts[1:] {
			rs = append(rs, c.refVal(ref))
			for _, n := range c.partNets(ref) {
				for _, pr := range c.netPins[n] {
					if !bl.hasPart(pr.Ref) && !c.isPowerNet(n) {
						drv = fmt.Sprintf("%s.%s (%s)", pr.Ref, pr.Name, n)
					}
				}
			}
		}
		if nr := c.netW(c.pinNet(bl.Core, "+", "A", "1")); nr != nil {
			for _, pp := range nr.Pins {
				if pp.Ref == bl.Core {
					ia = pp.CurrentA
				}
			}
		}
		s := fmt.Sprintf("%s (%s) indicator", bl.Core, dev)
		if drv != "" {
			s += " driven from " + drv
		}
		if len(rs) > 0 {
			s += " via " + strings.Join(rs, ", ")
		}
		s += ", " + fmtA(ia)
		return s, notes
	case "esd":
		var nets []string
		for _, n := range c.partNets(bl.Core) {
			if !c.isGround(n) {
				nets = append(nets, n)
			}
		}
		sort.Strings(nets)
		return fmt.Sprintf("%s (%s) ESD array on %s", bl.Core, dev, strings.Join(nets, ", ")), notes
	case "connector":
		var nets, pulls []string
		for _, n := range c.partNets(bl.Core) {
			if !c.isGround(n) {
				nets = append(nets, n)
			}
		}
		sort.Strings(nets)
		for _, ref := range bl.Parts[1:] {
			ns := c.partNets(ref)
			if c.kind(ref) == pcbauto.KindResistor && len(ns) == 2 && (strings.HasPrefix(strings.ToUpper(ns[0]), "CC") || strings.HasPrefix(strings.ToUpper(ns[1]), "CC")) {
				pulls = append(pulls, c.refVal(ref))
			}
		}
		s := fmt.Sprintf("%s (%s): %s", bl.Core, dev, strings.Join(nets, ", "))
		if len(pulls) > 0 {
			s += "; CC pull-downs " + strings.Join(pulls, ", ") + " (USB-C sink Rd → default USB power)"
		}
		return s, notes
	case "mains":
		var nets []string
		for _, n := range c.partNets(bl.Core) {
			if c.mains[n] {
				nets = append(nets, n)
			}
		}
		s := fmt.Sprintf("%s (%s) line-voltage terminal: %s at %s Vrms", bl.Core, dev, strings.Join(nets, ", "), trimFloat(c.mainsVrms(), 0))
		if len(bl.Parts) > 1 {
			s += "; with " + strings.Join(bl.Parts[1:], ", ")
		}
		return s, notes
	case "isolation":
		return fmt.Sprintf("%s (%s) isolation bridge", bl.Core, dev), notes
	}
	switch bl.SubFunction {
	case "keys":
		var ks []string
		for _, ref := range bl.Parts {
			if c.kind(ref) != pcbauto.KindSwitch {
				continue
			}
			for _, n := range c.partNets(ref) {
				if !c.isGround(n) {
					ks = append(ks, ref+" → "+n)
				}
			}
		}
		var extra []string
		for _, ref := range bl.Parts {
			switch c.kind(ref) {
			case pcbauto.KindResistor:
				extra = append(extra, c.refVal(ref)+" pull")
			case pcbauto.KindCapacitor:
				extra = append(extra, c.refVal(ref)+" RC")
			}
		}
		s := "momentary keys " + strings.Join(ks, ", ")
		if len(extra) > 0 {
			s += "; " + strings.Join(extra, ", ")
		}
		return s, notes
	case "auto-download":
		var qs []string
		for _, ref := range bl.Parts {
			if c.kind(ref) == pcbauto.KindTransistor {
				qs = append(qs, ref)
			}
		}
		return fmt.Sprintf("%s (%s) auto-download: DTR/RTS drive EN/IO0 for automatic flashing", strings.Join(qs, "/"), dev), notes
	case "unassigned":
		return "parts without a recognised function: " + strings.Join(bl.Parts, ", "), notes
	}
	return fmt.Sprintf("%s (%s)", bl.Core, dev), notes
}

func (b *Block) hasPart(ref string) bool { return has(b.Parts, ref) }

func (c *ctx) scenRes(name string) *powersim.Result {
	for _, r := range c.scens {
		if r.Scenario == name {
			return r
		}
	}
	return nil
}

// supplyOf returns the rail a part draws the most current from (worst).
func (c *ctx) supplyOf(ref string) (string, float64) {
	best, bestA := "", 0.0
	for _, n := range c.partNets(ref) {
		nr := c.netW(n)
		if nr == nil || c.isGround(n) {
			continue
		}
		sum := 0.0
		for _, pp := range nr.Pins {
			if pp.Ref == ref && pp.Dir == "sink" {
				sum += pp.CurrentA
			}
		}
		if sum > bestA {
			best, bestA = n, sum
		}
	}
	return best, bestA
}

func (c *ctx) envelope(net string) (lo, hi float64) {
	all, _, _ := c.scenarioVoltages(net)
	var vs []float64
	for _, v := range all {
		if math.Abs(v) > 0.05 {
			vs = append(vs, v) // a scenario where the rail is unpowered says nothing about its range
		}
	}
	if len(vs) == 0 {
		vs = all
	}
	if len(vs) == 0 {
		return 0, 0
	}
	lo, hi = vs[0], vs[0]
	for _, v := range vs {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	return
}

func rangeV(lo, hi float64) string {
	if math.Abs(hi-lo) < 1e-3 {
		return fmtV(hi)
	}
	return fmt.Sprintf("%s–%s V", trimFloat(lo, 2), trimFloat(hi, 2))
}

func (c *ctx) sortedParts(keep func(string) bool) []string {
	var out []string
	for _, p := range c.d.Parts {
		if keep(p.Ref) {
			out = append(out, p.Ref)
		}
	}
	return sortRefs(out)
}

// refVal renders "R9 1kΩ", or just "R9" when the value is unknown.
func (c *ctx) refVal(ref string) string {
	if p := c.parts[ref]; p != nil && strings.TrimSpace(p.Value) != "" {
		return ref + " " + strings.TrimSpace(p.Value)
	}
	return ref
}
