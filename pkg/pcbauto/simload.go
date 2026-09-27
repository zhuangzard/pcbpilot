package pcbauto

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// Simulated per-pin currents (`pcbpilot sim power` output, schemaVersion 1).
//
// The file carries one result per scenario; each result lists, per net, the
// current through every pad and its direction (source = the part supplies
// the net, sink = it draws from it, pass = a series element). Per net
// Σsource = Σsink within one scenario; "pass" means no DC current (< 1 nA).
// pcbauto uses the "worst" result when present, otherwise the per-pin
// maximum over all scenarios. A merged worst result is itself a per-pin
// maximum (each pin carries the scenario it came from), so it does not
// satisfy KCL per net: two OR-ing supplies both appear at full current.

// SimFile is the raw simulation output.
type SimFile struct {
	SchemaVersion int         `json:"schemaVersion"`
	Generator     string      `json:"generator"`
	Scenarios     []string    `json:"scenarios"`
	Results       []SimResult `json:"results"`
	Note          string      `json:"note,omitempty"`
}

// SimResult is one scenario.
type SimResult struct {
	Scenario    string               `json:"scenario"`
	Nets        map[string]SimNet    `json:"nets"`
	Parts       map[string]SimPart   `json:"parts,omitempty"`
	Ripple      map[string]SimRipple `json:"ripple,omitempty"`
	Warnings    []string             `json:"warnings,omitempty"`
	Assumptions []string             `json:"assumptions,omitempty"`
}

// SimNet is the simulated state of one net.
type SimNet struct {
	Voltage    float64  `json:"voltage"`
	CurrentA   float64  `json:"currentA"`
	Role       string   `json:"role"` // power | ground | signal | switch
	Pins       []SimPin `json:"pins"`
	VoltageMin float64  `json:"voltageMin,omitempty"`
	VoltageMax float64  `json:"voltageMax,omitempty"`
	Floating   bool     `json:"floating,omitempty"`
	KCLErrorA  float64  `json:"kclErrorA,omitempty"`
	Scenario   string   `json:"scenario,omitempty"`
}

// SimPin is the current through one pad (magnitude) and its direction.
type SimPin struct {
	Ref      string  `json:"ref"`
	Pin      string  `json:"pin"`
	Name     string  `json:"name,omitempty"`
	CurrentA float64 `json:"currentA"`
	Dir      string  `json:"dir"`                // source | sink | pass (no DC current)
	Kind     string  `json:"kind,omitempty"`     // e.g. load, buck, diode, connector-source
	Scenario string  `json:"scenario,omitempty"` // origin of a merged worst-case value
}

// SimPart is a part's simulated operating point.
type SimPart struct {
	Model  string   `json:"model"`
	PowerW float64  `json:"powerW"`
	Notes  []string `json:"notes,omitempty"`
}

// SimRipple is the AC content of a switch net or capacitor.
type SimRipple struct {
	IPeakA    float64 `json:"iPeakA,omitempty"`
	IRmsA     float64 `json:"iRmsA,omitempty"`
	IAvgA     float64 `json:"iAvgA,omitempty"`
	DeltaIA   float64 `json:"deltaIA,omitempty"`
	Duty      float64 `json:"duty,omitempty"`
	Regulator string  `json:"regulator,omitempty"`
	Scenario  string  `json:"scenario,omitempty"`
}

// SimPower is the resolved current set pcbauto sizes copper from.
type SimPower struct {
	Scenario    string             `json:"scenario"` // "worst" or "envelope(a,b,…)"
	Generator   string             `json:"generator,omitempty"`
	Nets        map[string]*SimNet `json:"nets"`
	Ripple      map[string]SimRipple
	Warnings    []string `json:"warnings,omitempty"`
	Assumptions []string `json:"assumptions,omitempty"`
}

// ParseSim decodes and resolves a `sim power` file.
func ParseSim(raw []byte) (*SimPower, error) {
	var f SimFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("sim power: %w", err)
	}
	if f.SchemaVersion != 1 {
		return nil, fmt.Errorf("sim power: schemaVersion %d unsupported (want 1)", f.SchemaVersion)
	}
	if len(f.Results) == 0 {
		return nil, fmt.Errorf("sim power: no results")
	}
	return f.Resolve(), nil
}

// Resolve picks the "worst" result, or the per-pin maximum over results.
func (f *SimFile) Resolve() *SimPower {
	sp := &SimPower{Generator: f.Generator, Nets: map[string]*SimNet{}, Ripple: map[string]SimRipple{}}
	for _, r := range f.Results {
		if strings.EqualFold(r.Scenario, "worst") {
			sp.Scenario = r.Scenario
			for name, n := range r.Nets {
				c := n
				c.Pins = append([]SimPin(nil), n.Pins...)
				sp.Nets[name] = &c
			}
			for k, v := range r.Ripple {
				sp.Ripple[k] = v
			}
			sp.Warnings = append(sp.Warnings, r.Warnings...)
			sp.Assumptions = append(sp.Assumptions, r.Assumptions...)
			return sp
		}
	}
	var names []string
	for _, r := range f.Results {
		names = append(names, r.Scenario)
		for name, n := range r.Nets {
			cur := sp.Nets[name]
			if cur == nil {
				cur = &SimNet{Role: n.Role}
				sp.Nets[name] = cur
			}
			cur.Voltage = math.Max(cur.Voltage, n.Voltage)
			cur.CurrentA = math.Max(cur.CurrentA, n.CurrentA)
			if cur.Role == "" {
				cur.Role = n.Role
			}
			for _, p := range n.Pins {
				if p.Scenario == "" {
					p.Scenario = r.Scenario
				}
				found := false
				for k := range cur.Pins {
					q := &cur.Pins[k]
					if q.Ref == p.Ref && q.Pin == p.Pin {
						found = true
						if p.CurrentA > q.CurrentA {
							*q = p
						}
					}
				}
				if !found {
					cur.Pins = append(cur.Pins, p)
				}
			}
		}
		for k, v := range r.Ripple {
			o := sp.Ripple[k]
			v.IPeakA, v.IRmsA = math.Max(o.IPeakA, v.IPeakA), math.Max(o.IRmsA, v.IRmsA)
			sp.Ripple[k] = v
		}
		sp.Warnings = append(sp.Warnings, r.Warnings...)
		sp.Assumptions = append(sp.Assumptions, r.Assumptions...)
	}
	sp.Scenario = "envelope(" + strings.Join(names, ",") + ")"
	return sp
}

// PadCurrent is the simulated current through one pad of a net.
type PadCurrent struct {
	Pad      string  `json:"pad"` // REF.PIN
	Name     string  `json:"name,omitempty"`
	CurrentA float64 `json:"currentA"` // DC current (IR drop, segment currents)
	Dir      string  `json:"dir"`
	Kind     string  `json:"kind,omitempty"`
	Scenario string  `json:"scenario,omitempty"`
	// SizeA is the current the pad's own copper (stub, vias) is sized for:
	// the DC current, or its part's ripple RMS when larger (a converter's
	// input/output capacitor carries AC with no DC).
	SizeA float64 `json:"sizeA,omitempty"`
	pad   *Pad
}

// sizing is the current pad copper is sized for.
func (pc PadCurrent) sizing() float64 { return math.Max(pc.CurrentA, pc.SizeA) }

// padCurrent returns the simulated current through pd (ok=false when the
// net carries no per-pad data or the pad is not listed).
func (np *NetPlan) padCurrent(pd *Pad) (PadCurrent, bool) {
	for _, pc := range np.PadCurrents {
		if pc.pad == pd {
			return pc, true
		}
	}
	return PadCurrent{}, false
}

// hasPadCurrents reports whether the net's copper can be sized per pad.
func (np *NetPlan) hasPadCurrents() bool { return len(np.PadCurrents) > 0 }

// applySim folds simulated currents into the plan of net n. declared tells
// whether a power.json rail already fixed the current (it keeps priority).
func applySim(a *Analysis, np *NetPlan, n *Net, sn *SimNet, sim *SimPower, declared bool) {
	scenario := sim.Scenario
	if sn.Scenario != "" && sn.Scenario != scenario {
		scenario += "/" + sn.Scenario
	}
	np.SimScenario = scenario
	rip, hasRip := sim.Ripple[n.Name]
	if !hasRip {
		// Ripple keyed by the inductor on the switch node (ripple["L1"]).
		for _, pd := range n.Pads {
			if r, ok := sim.Ripple[pd.Part]; ok && r.IPeakA > 0 {
				rip, hasRip = r, true
				break
			}
		}
	}
	if sn.Floating {
		// A high-impedance signal net floats in a DC solve; a floating
		// supply is a finding.
		if r := sn.Role; r == "power" || r == "ground" || r == "switch" {
			np.Warnings = append(np.Warnings, "simulation marks the supply net floating")
		} else {
			np.Why = append(np.Why, "simulation: no DC path (floating)")
		}
	}
	simA := sn.CurrentA
	if simA <= 0 {
		// Net total from the pins: Σsource (= Σsink by contract).
		src, snk := 0.0, 0.0
		for _, p := range sn.Pins {
			switch p.Dir {
			case "source":
				src += p.CurrentA
			case "sink":
				snk += p.CurrentA
			}
		}
		simA = math.Max(src, snk)
	}
	np.SimCurrentA = simA
	role := np.Role
	if sn.Role == "switch" && role != RoleDiff {
		role = RoleSwitch
	} else if sn.Role == "power" && (role == RoleSignal || role == RoleAnalog) {
		role = RolePower
	} else if sn.Role == "ground" && role == RoleSignal {
		role = RoleGround
	}
	sizeA := simA
	if role == RoleSwitch && hasRip {
		if rip.IRmsA > 0 {
			sizeA = rip.IRmsA
		}
		np.PeakA = rip.IPeakA
	}
	if declared {
		if np.CurrentA+1e-9 < sizeA {
			w := whyf("declared %.2fA is below the simulated %.2fA (%s): declared value kept (explicit intent) — check the power budget", np.CurrentA, sizeA, scenario)
			np.Why = append(np.Why, w)
			np.Warnings = append(np.Warnings, w)
			a.Notes = append(a.Notes, np.Net+": "+w)
		} else {
			np.Why = append(np.Why, whyf("declared %.2fA ≥ simulated %.2fA (%s)", np.CurrentA, sizeA, scenario))
		}
	} else {
		np.Role = role
		np.CurrentA = sizeA
		np.Source = "simulated"
		if sn.Voltage > 0 {
			np.Voltage = sn.Voltage
		}
		if role == RoleSwitch && hasRip {
			np.Why = append(np.Why, whyf("simulated %s: switch node sized by ripple RMS %.2fA, vias by peak %.2fA", scenario, sizeA, rip.IPeakA))
		} else {
			np.Why = append(np.Why, whyf("simulated %s: %.3fA", scenario, sizeA))
		}
	}
	// Per-pad currents: (ref, pin) → the net's pads. A pin with several pads
	// (an exposed pad split into a paste grid) shares its current equally.
	byKey := map[string][]*Pad{}
	for _, pd := range n.Pads {
		byKey[pd.Part+"."+pd.Number] = append(byKey[pd.Part+"."+pd.Number], pd)
	}
	var missing []string
	for _, p := range sn.Pins {
		pads := byKey[p.Ref+"."+p.Pin]
		if len(pads) == 0 {
			missing = append(missing, p.Ref+"."+p.Pin)
			continue
		}
		share := p.CurrentA / float64(len(pads))
		size := 0.0
		if r, ok := sim.Ripple[p.Ref]; ok && r.IPeakA == 0 && r.IRmsA > share {
			// Capacitor ripple (an inductor's ripple is already its DC path).
			size = r.IRmsA / float64(len(pads))
		}
		for _, pd := range pads {
			np.PadCurrents = append(np.PadCurrents, PadCurrent{Pad: pd.Key(), Name: p.Name, CurrentA: share, Dir: p.Dir,
				Kind: p.Kind, Scenario: p.Scenario, SizeA: size, pad: pd})
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		w := whyf("sim pins not on this net in the board: %s", strings.Join(missing, ", "))
		np.Warnings = append(np.Warnings, w)
		a.Notes = append(a.Notes, np.Net+": "+w)
	}
}

// simBoost is the IR-drop feedback into the next routing pass: a larger
// net width and extra fan-out vias per pad for nets that missed the budget
// with every segment already at its routed width.
type simBoost struct {
	WidthMil float64
	ExtraVia int
}
