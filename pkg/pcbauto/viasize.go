package pcbauto

import (
	"math"
	"sort"
)

// Via current sizing — the one model every stage uses (Analyze, intent
// derive, the router's fan-out and layer transitions, pcb check via-current).
//
// A power path is only as strong as its narrowest cross-section: a 3 A track
// that changes layer through one 0.3 mm via is limited by that via's barrel,
// however wide the copper around it. The barrel is a copper tube of the
// plating thickness t around the drill d; its cross-section is
//
//	A = π·(d + t)·t                    (IPC-2221 barrel = annulus, mean diameter d+t)
//
// and it is rated like an IPC-2221 internal conductor (k = 0.024; the
// conservative curve — the barrel is buried in the laminate):
//
//	I = 0.024 · ΔT^0.44 · A^0.725      (A in mil², I in A)
//
// Its DC resistance over the barrel length L (the board thickness between the
// two layers — the full thickness at design time, the worst case) is
//
//	R = ρ·L / (π·(d + t)·t),  ρ_Cu = 1.72e-8 Ω·m at 20 °C.
//
// SizeVias picks, from a fab-legal ladder, the via size and parallel count
// that carry the current with a margin (default 20 %) per layer transition:
// the class size first, then more vias in parallel as long as they fit the
// space at the transition, then a larger drill.

const (
	// DefaultViaPlatingMil is the via barrel plating: JLC standard ≈ 18 µm
	// (0.7 mil; IPC-6012 class 2 minimum average is 20 µm, class 3 25 µm).
	DefaultViaPlatingMil = 0.7
	// DefaultViaMarginPct is the ampacity margin a transition must keep.
	DefaultViaMarginPct = 20.0
	// viaTransitionSpanMil is how far along a wide track a transition's via
	// array may extend (2.54 mm) when the track is narrower than that.
	viaTransitionSpanMil = 100.0
	// viaDefaultHoleGapMil is the drill-edge to drill-edge gap assumed for a
	// same-net via array when the board declares none (JLC 0.254 mm).
	viaDefaultHoleGapMil = 10.0
	// viaMarginCapPct caps the reported margin (a 10 mA net on a 0.7 A via).
	viaMarginCapPct = 999.0
	// viaMaxCount bounds a transition when the space is unconstrained.
	viaMaxCount = 64
)

// ViaSize is a via's drill and pad (outer) diameter, mil.
type ViaSize struct {
	DrillMil float64 `json:"drillMil"`
	DiaMil   float64 `json:"diaMil"`
}

// JLCViaLadder is the fab-legal via ladder: drill 0.2/0.25/0.3/0.4/0.5/0.6 mm,
// pad = drill + 2 × annular ring (0.1 mm below a 0.3 mm drill — JLC's
// 0.2/0.4 minimum —, 0.15 mm from 0.3 mm — JLC's standard 0.3/0.6).
func JLCViaLadder() []ViaSize {
	var out []ViaSize
	for _, d := range []float64{0.2, 0.25, 0.3, 0.4, 0.5, 0.6} {
		ring := 0.15
		if d < 0.3 {
			ring = 0.1
		}
		out = append(out, ViaSize{DrillMil: round2(d / 0.0254), DiaMil: round2((d + 2*ring) / 0.0254)})
	}
	return out
}

// ViaAmpacity is the current one via barrel of drill (mil) with plating
// (mil) carries at a temperature rise (°C): IPC-2221 internal conductor on
// the barrel annulus π(d+t)t.
func ViaAmpacity(drill, plating, tempRise float64) float64 {
	if drill <= 0 || plating <= 0 || tempRise <= 0 {
		return 0
	}
	area := math.Pi * (drill + plating) * plating
	return ipcKInternal * math.Pow(tempRise, 0.44) * math.Pow(area, 0.725)
}

// ViaResistance is the DC resistance (Ω, 20 °C) of one barrel of drill and
// plating (mil) over length (mil).
func ViaResistance(drill, plating, length float64) float64 {
	if drill <= 0 || plating <= 0 || length <= 0 {
		return 0
	}
	area := math.Pi * (drill + plating) * plating * milToM * milToM
	return rhoCu * length * milToM / area
}

// ViaSpace is the rectangle a transition's via array may occupy (mil): the
// cross dimension W (the track width, or a pad side) and the length L along
// it. Zero = unconstrained.
type ViaSpace struct {
	WMil float64 `json:"wMil,omitempty"`
	LMil float64 `json:"lMil,omitempty"`
}

// TrackViaSpace is the space at a layer change of a track of width w: the
// array spans the track width and up to viaTransitionSpanMil along it.
func TrackViaSpace(w float64) ViaSpace {
	return ViaSpace{WMil: w, LMil: math.Max(w, viaTransitionSpanMil)}
}

// PadViaSpace is the space around (or, for an exposed pad, inside) a pad of
// w × h: a ring of vias at one pitch outside the copper — the perimeter as
// one row — or the pad area itself when in-pad vias are allowed.
func PadViaSpace(w, h float64, inPad bool) ViaSpace {
	if inPad {
		return ViaSpace{WMil: math.Max(w, h), LMil: math.Min(w, h)}
	}
	return ViaSpace{WMil: 0.1, LMil: 2 * (w + h)}
}

// viaPitch is the centre spacing of a same-net array: pads a clearance apart
// and drills a hole gap apart.
func viaPitch(v ViaSize, clearance, holeGap float64) float64 {
	if holeGap <= 0 {
		holeGap = viaDefaultHoleGapMil
	}
	return math.Max(v.DiaMil+math.Max(clearance, 0), v.DrillMil+holeGap)
}

// Fits is how many vias of size v fit the space as a grid (≥ 1: a single
// via always fits where the track is).
func (s ViaSpace) Fits(v ViaSize, clearance, holeGap float64) int {
	if s.WMil <= 0 || s.LMil <= 0 {
		return viaMaxCount
	}
	p := viaPitch(v, clearance, holeGap)
	n := func(span float64) int {
		if span < v.DiaMil {
			return 1
		}
		return int(math.Floor((span-v.DiaMil)/p+1e-9)) + 1
	}
	return min(n(s.WMil)*n(s.LMil), viaMaxCount)
}

// ViaSizing is one sizing request.
type ViaSizing struct {
	CurrentA   float64 // thermal (DC / RMS) current through the transition
	TempRiseC  float64 // allowed barrel temperature rise (default 10 °C)
	PlatingMil float64 // barrel plating (default DefaultViaPlatingMil)
	MarginPct  float64 // required ampacity margin (default 20 %; < 0 = none)
	Class      ViaSize // preferred size (net class / board default)
	Ladder     []ViaSize
	Space      ViaSpace // where the array may go (zero = unconstrained)
	MaxCount   int      // hard cap on the count (0 = what fits)
	LengthMil  float64  // barrel length between the two layers (default 62.99)
	Clearance  float64  // array pitch inputs (mil)
	HoleGap    float64
}

// ViaPlan is the sized via of a net: size, count per layer transition, and
// the numbers behind it.
type ViaPlan struct {
	DrillMil  float64 `json:"drillMil"`
	DiaMil    float64 `json:"diaMil"`
	Count     int     `json:"countPerTransition"`
	PerViaA   float64 `json:"perViaA"`
	AmpacityA float64 `json:"ampacityA"` // Count × PerViaA
	CurrentA  float64 `json:"currentA"`
	MarginPct float64 `json:"marginPct"` // achieved: ampacity / current − 1
	// RequiredMarginPct is the margin the sizing had to keep.
	RequiredMarginPct float64 `json:"requiredMarginPct"`
	PlatingMil        float64 `json:"platingMil"`
	LengthMil         float64 `json:"lengthMil"`
	// ResistanceMOhm is one barrel; DropMV the transition's drop at CurrentA
	// (Count barrels in parallel).
	ResistanceMOhm float64 `json:"resistanceMOhm"`
	DropMV         float64 `json:"dropMV"`
	Fits           int     `json:"fits"` // vias of this size the space holds
	OK             bool    `json:"ok"`   // carries the current with the margin
	Why            string  `json:"why"`
}

// Size returns the plan's via size.
func (p *ViaPlan) Size() ViaSize { return ViaSize{p.DrillMil, p.DiaMil} }

func (q *ViaSizing) defaults() {
	if q.TempRiseC <= 0 {
		q.TempRiseC = 10
	}
	if q.PlatingMil <= 0 {
		q.PlatingMil = DefaultViaPlatingMil
	}
	if q.MarginPct == 0 {
		q.MarginPct = DefaultViaMarginPct
	} else if q.MarginPct < 0 {
		q.MarginPct = 0
	}
	if q.LengthMil <= 0 {
		q.LengthMil = DefaultRules().BoardThickMil
	}
	if q.Class.DrillMil <= 0 || q.Class.DiaMil <= q.Class.DrillMil {
		d := DefaultRules()
		q.Class = ViaSize{d.ViaDrill, d.ViaDia}
	}
	if q.Ladder == nil {
		q.Ladder = JLCViaLadder()
	}
}

// EvalVias rates a given via size and count for a current (no choice made).
func EvalVias(q ViaSizing, v ViaSize, count int) ViaPlan {
	q.defaults()
	if count < 1 {
		count = 1
	}
	per := ViaAmpacity(v.DrillMil, q.PlatingMil, q.TempRiseC)
	r := ViaResistance(v.DrillMil, q.PlatingMil, q.LengthMil)
	p := ViaPlan{DrillMil: v.DrillMil, DiaMil: v.DiaMil, Count: count, PerViaA: round3(per), AmpacityA: round3(per * float64(count)),
		CurrentA: round3(q.CurrentA), RequiredMarginPct: q.MarginPct, PlatingMil: q.PlatingMil, LengthMil: round2(q.LengthMil), ResistanceMOhm: round3(r * 1000),
		Fits: q.Space.Fits(v, q.Clearance, q.HoleGap)}
	if q.CurrentA > 0 {
		p.MarginPct = math.Min(round2((per*float64(count)/q.CurrentA-1)*100), viaMarginCapPct)
		p.DropMV = round3(q.CurrentA * r / float64(count) * 1000)
	} else {
		p.MarginPct = viaMarginCapPct
	}
	p.OK = q.CurrentA <= 0 || per*float64(count) >= q.CurrentA*(1+q.MarginPct/100)-1e-9
	return p
}

// SizeVias chooses the via size and count per layer transition for a
// current: the class size with as many vias in parallel as needed while they
// fit, else the smallest larger ladder size that fits. When nothing fits the
// plan with the most ampacity is returned with OK = false.
func SizeVias(q ViaSizing) ViaPlan {
	q.defaults()
	need := func(v ViaSize) int {
		if q.CurrentA <= 0 {
			return 1
		}
		per := ViaAmpacity(v.DrillMil, q.PlatingMil, q.TempRiseC)
		return max(1, int(math.Ceil(q.CurrentA*(1+q.MarginPct/100)/per-1e-9)))
	}
	limit := func(v ViaSize) int {
		f := q.Space.Fits(v, q.Clearance, q.HoleGap)
		if q.MaxCount > 0 {
			f = min(f, q.MaxCount)
		}
		return f
	}
	cands := []ViaSize{q.Class}
	lad := append([]ViaSize(nil), q.Ladder...)
	sort.Slice(lad, func(i, j int) bool { return lad[i].DrillMil < lad[j].DrillMil })
	for _, v := range lad {
		if v.DrillMil > q.Class.DrillMil+1e-6 {
			cands = append(cands, v)
		}
	}
	var best ViaPlan
	bestA := -1.0
	for i, v := range cands {
		n, lim := need(v), limit(v)
		if n <= lim {
			p := EvalVias(q, v, n)
			switch {
			case q.CurrentA <= 0:
				p.Why = "no current: one via"
			case i == 0 && n == 1:
				p.Why = sprintf("one %.1f/%.1f mil via carries %.2f A ≥ %.2f A × %.0f %% margin", v.DrillMil, v.DiaMil, p.PerViaA, q.CurrentA, 100+q.MarginPct)
			case i == 0:
				p.Why = sprintf("%d × %.1f/%.1f mil vias in parallel (%.2f A each, %d fit) carry %.2f A ≥ %.2f A × %.0f %%", n, v.DrillMil, v.DiaMil, p.PerViaA, lim, p.AmpacityA, q.CurrentA, 100+q.MarginPct)
			default:
				p.Why = sprintf("%d × %.1f/%.1f mil (class %.1f/%.1f needs %d, only %d fit) carry %.2f A ≥ %.2f A × %.0f %%", n, v.DrillMil, v.DiaMil, q.Class.DrillMil, q.Class.DiaMil, need(q.Class), limit(q.Class), p.AmpacityA, q.CurrentA, 100+q.MarginPct)
			}
			return p
		}
		if a := ViaAmpacity(v.DrillMil, q.PlatingMil, q.TempRiseC) * float64(lim); a > bestA {
			bestA = a
			best = EvalVias(q, v, lim)
		}
	}
	best.OK = false
	best.Why = sprintf("no ladder via carries %.2f A × %.0f %% in the space (%.0f × %.0f mil): best %d × %.1f/%.1f mil = %.2f A — widen the copper at the transition, use a pour, or avoid the layer change",
		q.CurrentA, 100+q.MarginPct, q.Space.WMil, q.Space.LMil, best.Count, best.DrillMil, best.DiaMil, best.AmpacityA)
	return best
}

// ViaMarginOr resolves a margin setting (0 = the default 20 %, < 0 = none).
func ViaMarginOr(m float64) float64 {
	switch {
	case m == 0:
		return DefaultViaMarginPct
	case m < 0:
		return 0
	}
	return m
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// thermalCurrent is the current a net's vias are sized for: the sizing
// (DC / RMS) current; a switch node's pulsed peak does not set the barrel's
// steady-state temperature and only counts when no RMS figure exists.
func (np *NetPlan) thermalCurrent() float64 {
	if np.CurrentA > 0 {
		return np.CurrentA
	}
	return np.PeakA
}

// sizeNetVias sets np.Via / ViasPerTransition from the net's current, the
// class via size and the track width at the transition.
func sizeNetVias(np *NetPlan, class ViaSize, r Rules, spec PowerSpec) {
	cur := np.thermalCurrent()
	p := SizeVias(ViaSizing{CurrentA: cur, TempRiseC: spec.TempRiseC, PlatingMil: spec.ViaPlatingMil, MarginPct: spec.ViaMarginPct,
		Class: class, Space: TrackViaSpace(np.WidthMil), LengthMil: r.BoardThickMil,
		Clearance: r.Clearance, HoleGap: r.HoleGap})
	np.Via = &p
	np.ViasPerTransition = p.Count
}
