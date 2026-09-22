package pcbauto

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// NetRole is the electrical role that drives width, clearance and layer choice.
type NetRole string

const (
	RoleGround NetRole = "ground"
	RolePower  NetRole = "power" // a supply rail
	RoleSignal NetRole = "signal"
	RoleDiff   NetRole = "diff"  // member of a differential pair
	RoleClock  NetRole = "clock" // crystal / clock: short, guarded
	RoleAnalog NetRole = "analog"
	RoleRF     NetRole = "rf"
	RoleSwitch NetRole = "switch" // SMPS switch node: short and wide, noisy
)

// PowerRail is an optional user-declared rail budget. Declared values always
// win over inference, and are marked as such in the report.
type PowerRail struct {
	Net      string  `json:"net"`
	Voltage  float64 `json:"voltage"`
	CurrentA float64 `json:"currentA"`
	// Plane requests a dedicated plane/area (true) or tracks (false); nil = decide.
	Plane *bool `json:"plane,omitempty"`
}

// PowerSpec collects rail budgets and analysis parameters.
type PowerSpec struct {
	Rails []PowerRail `json:"rails,omitempty"`
	// TempRiseC is the allowed conductor temperature rise (default 10 °C).
	TempRiseC float64 `json:"tempRiseC,omitempty"`
	// Coated selects IPC-2221B B4 (conformal coated) instead of B2 external spacing.
	Coated bool `json:"coated,omitempty"`
	// DiffPairs lists explicit P/N pairs; USB_DP/DM-style pairs are auto-detected.
	DiffPairs [][2]string `json:"diffPairs,omitempty"`
	// SingleEndedOhm / DiffOhm targets (defaults 50 / 90).
	SingleEndedOhm float64 `json:"singleEndedOhm,omitempty"`
	DiffOhm        float64 `json:"diffOhm,omitempty"`
}

// NetPlan is the per-net electrical decision.
type NetPlan struct {
	Net               string   `json:"net"`
	Role              NetRole  `json:"role"`
	Voltage           float64  `json:"voltage"`
	CurrentA          float64  `json:"currentA"`
	Source            string   `json:"source"` // declared | name | heuristic
	PadCount          int      `json:"padCount"`
	WidthMil          float64  `json:"widthMil"`      // outer-layer track width
	InnerWidthMil     float64  `json:"innerWidthMil"` // inner-layer width for the same current
	ClearanceMil      float64  `json:"clearanceMil"`
	ViasPerTransition int      `json:"viasPerTransition"`
	PairWith          string   `json:"pairWith,omitempty"`
	PairGapMil        float64  `json:"pairGapMil,omitempty"`
	Priority          int      `json:"priority"` // routing order: lower first
	Plane             bool     `json:"plane"`    // delivered by a plane/area instead of tracks
	Why               []string `json:"why,omitempty"`
}

var (
	reVolt     = regexp.MustCompile(`(?i)(?:^|[^0-9])([0-9]+)V([0-9]+)(?:$|[^0-9])`)             // 3V3, 1V8
	reVoltDec  = regexp.MustCompile(`(?i)(?:^|[^0-9.])([0-9]+(?:\.[0-9]+)?)\s*V(?:$|[^0-9A-Z])`) // 3.3V, +5V, 12V
	reGround   = regexp.MustCompile(`(?i)^([A-Z0-9]+_)?(A|D|P|S|C|E)?GND[A-Z0-9_]*$|^VSS[A-Z0-9_]*$|^GROUND$|^EARTH$|^0V$`)
	rePower    = regexp.MustCompile(`(?i)^\+?(VCC|VDD|VBUS|VIN|VBAT|VSYS|VOUT|VCORE|VIO|VREF|AVDD|DVDD|PVDD|V[0-9]|[0-9]+V[0-9]*|\+)`)
	reClock    = regexp.MustCompile(`(?i)(XTAL|XIN|XOUT|OSC|XI$|XO$|CLK|MCLK|SCLK|BCLK)`)
	reRF       = regexp.MustCompile(`(?i)(ANT|RF_|_RF|LNA)`)
	reSwitch   = regexp.MustCompile(`(?i)^(SW|LX|PH)[0-9_]*$|_SW$|_LX$`)
	reAnalog   = regexp.MustCompile(`(?i)(ADC|AIN|VREF|MIC|AUDIO|SENSE)`)
	rePairBase = regexp.MustCompile(`(?i)^(.*?)(_?)(D|DP|DM|DN|P|N|\+|-)$`)
	reUSBData  = regexp.MustCompile(`(?i)(^|[_\-])(USB[0-9]*[_\-]?)?D[+-]$`)
)

// InferVoltage extracts a rail voltage from a net name (0 when unknown).
func InferVoltage(net string) float64 {
	n := upper(net)
	switch {
	case reGround.MatchString(n):
		return 0
	case strings.Contains(n, "VBUS") || strings.Contains(n, "USB_5V"):
		return 5
	case strings.Contains(n, "VBAT") || strings.Contains(n, "BAT+"):
		return 4.2
	}
	if m := reVolt.FindStringSubmatch(n); m != nil {
		v, _ := strconv.ParseFloat(m[1]+"."+m[2], 64)
		return v
	}
	if m := reVoltDec.FindStringSubmatch(n); m != nil {
		v, _ := strconv.ParseFloat(m[1], 64)
		if v > 0 && v <= 1000 {
			return v
		}
	}
	return 0
}

// classify returns the base role of a net from its name and pads.
func classify(net string, padCount int) NetRole {
	n := upper(net)
	switch {
	case reGround.MatchString(n):
		return RoleGround
	case reSwitch.MatchString(n):
		return RoleSwitch
	case rePower.MatchString(n) && (InferVoltage(n) > 0 || padCount >= 3):
		return RolePower
	case reRF.MatchString(n):
		return RoleRF
	case reClock.MatchString(n):
		return RoleClock
	case reAnalog.MatchString(n):
		return RoleAnalog
	}
	return RoleSignal
}

// defaultCurrent is the heuristic current budget when none is declared.
// It is deliberately conservative; the report marks it "heuristic".
func defaultCurrent(role NetRole, name string, pads int) float64 {
	n := upper(name)
	switch role {
	case RoleGround:
		return 0 // sized as the sum of rails below
	case RolePower:
		switch {
		case strings.Contains(n, "VBUS"), strings.Contains(n, "VIN"), strings.Contains(n, "USB"):
			return 1.5
		case strings.Contains(n, "VBAT"):
			return 2
		}
		v := InferVoltage(n)
		base := 0.5
		switch {
		case v >= 9:
			base = 1.0
		case v >= 4.5:
			base = 1.0
		case v >= 3:
			base = 0.6
		case v > 0:
			base = 0.4
		}
		// More loads on a rail → more current; cap the heuristic growth.
		return base * math.Min(2, 1+float64(pads)/40)
	case RoleSwitch:
		return 1.5
	}
	return 0.05
}

// IPC-2221 external/internal constants for I = k·ΔT^0.44·A^0.725 (A in mil²).
const (
	ipcKExternal = 0.048
	ipcKInternal = 0.024
	milPerOz     = 1.378
)

// TraceWidthForCurrent returns the IPC-2221 width (mil) that carries amps
// with tempRise °C rise on copper of oz weight. internal selects inner layers.
func TraceWidthForCurrent(amps, tempRise, oz float64, internal bool) float64 {
	if amps <= 0 {
		return 0
	}
	k := ipcKExternal
	if internal {
		k = ipcKInternal
	}
	area := math.Pow(amps/(k*math.Pow(tempRise, 0.44)), 1/0.725)
	return area / (oz * milPerOz)
}

// CurrentForWidth is the inverse of TraceWidthForCurrent.
func CurrentForWidth(width, tempRise, oz float64, internal bool) float64 {
	k := ipcKExternal
	if internal {
		k = ipcKInternal
	}
	return k * math.Pow(tempRise, 0.44) * math.Pow(width*oz*milPerOz, 0.725)
}

// ViaCurrent returns the current one via barrel carries (plating 0.7 mil ≈ 18 µm,
// the JLC standard), treating the barrel as an internal conductor.
func ViaCurrent(drill, tempRise float64) float64 {
	const plating = 0.7
	area := math.Pi * (drill + plating) * plating
	return ipcKInternal * math.Pow(tempRise, 0.44) * math.Pow(area, 0.725)
}

// ClearanceForVoltage returns the IPC-2221B minimum spacing (mil) for a peak
// voltage between conductors. external selects B2 (uncoated external) or B4
// (coated) when coated; internal layers use B1.
func ClearanceForVoltage(volts float64, external, coated bool) float64 {
	type row struct{ maxV, b1, b2, b4 float64 } // mm
	table := []row{
		{15, 0.05, 0.1, 0.05}, {30, 0.05, 0.1, 0.05}, {50, 0.1, 0.6, 0.13},
		{100, 0.1, 0.6, 0.13}, {150, 0.2, 0.6, 0.4}, {170, 0.2, 1.25, 0.4},
		{250, 0.2, 1.25, 0.4}, {300, 0.2, 1.25, 0.4}, {500, 0.25, 2.5, 0.8},
	}
	pick := func(r row) float64 {
		switch {
		case !external:
			return r.b1
		case coated:
			return r.b4
		}
		return r.b2
	}
	v := math.Abs(volts)
	for _, r := range table {
		if v <= r.maxV {
			return pick(r) / 0.0254
		}
	}
	perV := 0.005
	if !external {
		perV = 0.0025
	} else if coated {
		perV = 0.00305
	}
	return (pick(table[len(table)-1]) + (v-500)*perV) / 0.0254
}

// MicrostripZ0 is the Hammerstad-Jensen characteristic impedance of an outer
// trace of width w over a reference plane at height h (all mil), εr, copper t.
func MicrostripZ0(w, h, t, er float64) float64 {
	// Effective width with thickness correction.
	we := w + t/math.Pi*math.Log(4*math.E/math.Sqrt(math.Pow(t/h, 2)+math.Pow(t/(w*math.Pi+1.1*t*math.Pi), 2)))*(1+1/er)/2
	u := we / h
	a := 1 + math.Log((math.Pow(u, 4)+math.Pow(u/52, 2))/(math.Pow(u, 4)+0.432))/49 + math.Log(1+math.Pow(u/18.1, 3))/18.7
	b := 0.564 * math.Pow((er-0.9)/(er+3), 0.053)
	eeff := (er+1)/2 + (er-1)/2*math.Pow(1+10/u, -a*b)
	f := 6 + (2*math.Pi-6)*math.Exp(-math.Pow(30.666/u, 0.7528))
	return 60 / math.Sqrt(eeff) * math.Log(f/u+math.Sqrt(1+4/(u*u)))
}

// DiffZ approximates edge-coupled microstrip differential impedance.
func DiffZ(w, s, h, t, er float64) float64 {
	return 2 * MicrostripZ0(w, h, t, er) * (1 - 0.48*math.Exp(-0.96*s/h))
}

// SolveWidthForZ0 bisects the width giving target single-ended impedance.
func SolveWidthForZ0(target, h, t, er float64) float64 {
	lo, hi := 1.0, 200.0
	for i := 0; i < 60; i++ {
		m := (lo + hi) / 2
		if MicrostripZ0(m, h, t, er) > target {
			lo = m
		} else {
			hi = m
		}
	}
	return (lo + hi) / 2
}

// SolveDiff finds (width, gap) for a target differential impedance with the
// gap fixed at max(minGap, width) — the common "gap ≈ width" coupled pair.
func SolveDiff(target, h, t, er, minGap float64) (w, s float64) {
	lo, hi := 2.0, 100.0
	for i := 0; i < 60; i++ {
		m := (lo + hi) / 2
		g := math.Max(minGap, m)
		if DiffZ(m, g, h, t, er) > target {
			lo = m
		} else {
			hi = m
		}
	}
	w = (lo + hi) / 2
	return w, math.Max(minGap, w)
}

// pairPartner finds the P/N partner of a net in a name set.
func pairPartner(name string, names map[string]bool) string {
	n := upper(name)
	swaps := [][2]string{{"DP", "DM"}, {"DP", "DN"}, {"D+", "D-"}, {"_P", "_N"}, {"+", "-"}, {"P", "N"}}
	for _, sw := range swaps {
		for _, pair := range [][2]string{sw, {sw[1], sw[0]}} {
			if strings.HasSuffix(n, pair[0]) {
				cand := strings.TrimSuffix(n, pair[0]) + pair[1]
				for other := range names {
					if upper(other) == cand && other != name {
						return other
					}
				}
			}
		}
	}
	return ""
}

// looksDiffName requires an interface keyword AND a P/N style suffix: a bare
// "+/-" suffix (LED+/LED-, BAT+/BAT-) is polarity, not a differential pair.
func looksDiffName(name string) bool {
	n := upper(name)
	if !rePairBase.MatchString(n) {
		return false
	}
	for _, k := range []string{"USB", "MIPI", "DSI", "CSI", "LVDS", "ETH", "RGMII", "SGMII", "HDMI", "TMDS", "PCIE", "SATA", "CAN", "RS485", "485", "DP", "DM", "D+", "D-"} {
		if !strings.Contains(n, k) {
			continue
		}
		// A "+/-" suffix is a pair only as a USB-style data pin (D+, USB_D-);
		// LED+/BAT- end in "D+"/"T-" too but are polarity.
		if strings.HasSuffix(n, "+") || strings.HasSuffix(n, "-") {
			return reUSBData.MatchString(n)
		}
		return true
	}
	return false
}

// Analysis is the electrical report for a board.
type Analysis struct {
	Nets          []*NetPlan          `json:"nets"`
	ByNet         map[string]*NetPlan `json:"-"`
	TempRiseC     float64             `json:"tempRiseC"`
	MaxVoltage    float64             `json:"maxVoltage"`
	TotalCurrentA float64             `json:"totalCurrentA"`
	Notes         []string            `json:"notes,omitempty"`
}

// Analyze derives per-net electrical requirements. stack may be nil (then
// impedance-controlled widths use a 4-layer JLC7628 default reference height).
func Analyze(b *Board, spec PowerSpec, stack *Stackup) *Analysis {
	if spec.TempRiseC <= 0 {
		spec.TempRiseC = 10
	}
	if spec.SingleEndedOhm <= 0 {
		spec.SingleEndedOhm = 50
	}
	if spec.DiffOhm <= 0 {
		spec.DiffOhm = 90
	}
	r := b.Rules
	a := &Analysis{ByNet: map[string]*NetPlan{}, TempRiseC: spec.TempRiseC}
	declared := map[string]PowerRail{}
	for _, rail := range spec.Rails {
		declared[upper(rail.Net)] = rail
	}
	nets := b.Nets()
	names := map[string]bool{}
	for _, n := range nets {
		names[n.Name] = true
	}
	explicitPair := map[string]string{}
	for _, p := range spec.DiffPairs {
		explicitPair[p[0]], explicitPair[p[1]] = p[1], p[0]
	}

	h, er, t := 8.4, 4.05, 1.378 // JLC04161H-7628 L1→L2 prepreg ≈ 0.2104 mm
	if stack != nil && stack.RefHeightMil > 0 {
		h, er = stack.RefHeightMil, stack.Er
	}
	if stack != nil && stack.Layers == 2 {
		h = r.BoardThickMil
	}

	for _, n := range nets {
		np := &NetPlan{Net: n.Name, PadCount: len(n.Pads)}
		np.Role = classify(n.Name, len(n.Pads))
		np.Voltage = InferVoltage(n.Name)
		np.Source = "name"
		if rail, ok := declared[upper(n.Name)]; ok {
			np.Source = "declared"
			if rail.Voltage != 0 {
				np.Voltage = rail.Voltage
			}
			np.CurrentA = rail.CurrentA
			if np.Role != RoleGround {
				np.Role = RolePower
			}
			if rail.Plane != nil {
				np.Plane = *rail.Plane
			}
		} else {
			np.CurrentA = defaultCurrent(np.Role, n.Name, len(n.Pads))
			if np.Role == RolePower || np.Role == RoleSwitch {
				np.Source = "heuristic"
			}
		}
		if p := explicitPair[n.Name]; p != "" {
			np.PairWith, np.Role = p, RoleDiff
		} else if looksDiffName(n.Name) {
			if p := pairPartner(n.Name, names); p != "" {
				np.PairWith, np.Role = p, RoleDiff
			}
		}
		a.Nets = append(a.Nets, np)
		a.ByNet[n.Name] = np
	}

	// Ground carries the return of every rail.
	total := 0.0
	for _, np := range a.Nets {
		if np.Role == RolePower {
			total += np.CurrentA
			a.MaxVoltage = math.Max(a.MaxVoltage, np.Voltage)
		}
	}
	a.TotalCurrentA = total
	for _, np := range a.Nets {
		if np.Role == RoleGround {
			np.CurrentA = math.Max(total, 0.5)
			np.Source = "sum-of-rails"
		}
	}

	oz := r.CopperOz
	inOz := r.InnerCopperOz
	for _, np := range a.Nets {
		w := TraceWidthForCurrent(np.CurrentA, spec.TempRiseC, oz, false)
		wi := TraceWidthForCurrent(np.CurrentA, spec.TempRiseC, inOz, true)
		np.Why = append(np.Why, whyf("IPC-2221 %.2fA/ΔT%.0f°C → %.1fmil outer, %.1fmil inner", np.CurrentA, spec.TempRiseC, w, wi))
		minW := r.TrackWidth
		switch np.Role {
		case RolePower, RoleGround:
			// Power branches never go below 10 mil even at tiny currents:
			// resistance/IR drop and pad-entry robustness.
			minW = math.Max(minW, 10)
		case RoleSwitch:
			minW = math.Max(minW, 20)
		case RoleDiff:
			dw, gap := SolveDiff(spec.DiffOhm, h, t, er, r.Clearance)
			if dw > 25 {
				// No nearby reference plane (2-layer over 1.6 mm): the impedance
				// width is unbuildable; keep a coupled standard-width pair.
				np.Why = append(np.Why, whyf("%.0fΩ needs w=%.0fmil over h=%.0fmil — impedance not controllable without an adjacent plane; routed as tightly coupled pair", spec.DiffOhm, dw, h))
				dw, gap = r.TrackWidth, r.Clearance
			} else {
				np.Why = append(np.Why, whyf("%.0fΩ diff over h=%.1fmil εr=%.2f → w=%.1f s=%.1f mil", spec.DiffOhm, h, er, dw, gap))
			}
			np.PairGapMil = roundUpTo(gap, 0.5)
			minW = math.Max(r.MinTrack, dw)
		case RoleRF:
			zw := SolveWidthForZ0(spec.SingleEndedOhm, h, t, er)
			if zw > 40 {
				np.Why = append(np.Why, whyf("%.0fΩ microstrip needs %.0fmil on this stackup — use a 4-layer board or coplanar waveguide", spec.SingleEndedOhm, zw))
				zw = 20
			} else {
				np.Why = append(np.Why, whyf("%.0fΩ microstrip → %.1fmil", spec.SingleEndedOhm, zw))
			}
			minW = math.Max(r.MinTrack, zw)
		}
		// Round only current-driven widths up to the metric step; the board's
		// rule width is used exactly (rounding 10 mil up to 0.3 mm would stop
		// signals from escaping 0.5 mm-pitch parts).
		pick := func(ipc float64) float64 {
			if ipc > minW {
				return metricRound(ipc)
			}
			return minW
		}
		np.WidthMil = pick(w)
		np.InnerWidthMil = pick(wi)
		if np.Role == RoleDiff || np.Role == RoleRF {
			np.WidthMil = roundUpTo(math.Max(minW, w), 0.1)
		}
		np.ClearanceMil = math.Max(r.Clearance, ClearanceForVoltage(np.Voltage, true, spec.Coated))
		if np.Voltage > 30 {
			np.Why = append(np.Why, whyf("IPC-2221B %gV → clearance %.1fmil", np.Voltage, np.ClearanceMil))
		}
		if np.CurrentA > 0 {
			per := ViaCurrent(r.ViaDrill, spec.TempRiseC)
			np.ViasPerTransition = int(math.Max(1, math.Ceil(np.CurrentA/per)))
		} else {
			np.ViasPerTransition = 1
		}
		switch np.Role {
		case RoleClock, RoleRF:
			np.Priority = 0
		case RoleDiff:
			np.Priority = 1
		case RoleSwitch:
			np.Priority = 2
		case RoleAnalog:
			np.Priority = 3
		case RolePower:
			np.Priority = 4
		case RoleGround:
			np.Priority = 6
		default:
			np.Priority = 5
		}
	}
	return a
}

// metricRound rounds a width up to the 0.05 mm step the repo standardises on.
func metricRound(mil float64) float64 {
	mm := roundUpTo(mil*0.0254, 0.05)
	return math.Round(mm/0.0254*100) / 100
}

func whyf(format string, args ...any) string {
	return strings.TrimSpace(sprintf(format, args...))
}

// SortedByPriority returns plans in routing order.
func (a *Analysis) SortedByPriority() []*NetPlan {
	out := append([]*NetPlan(nil), a.Nets...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].PadCount < out[j].PadCount
	})
	return out
}

// Plan returns the plan for a net, or a default signal plan.
func (a *Analysis) Plan(net string, r Rules) *NetPlan {
	if p := a.ByNet[net]; p != nil {
		return p
	}
	return &NetPlan{Net: net, Role: RoleSignal, WidthMil: r.TrackWidth, InnerWidthMil: r.TrackWidth, ClearanceMil: r.Clearance, Priority: 5}
}
