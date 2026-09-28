package intent

import (
	"fmt"
	"math"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// Via sizing in the intent: every net carries the via its current needs
// (pcbauto.SizeVias — size from the JLC ladder, count per layer transition,
// ampacity, margin, barrel drop), a net class takes the largest via of its
// members (so `pcb rules apply` writes the sized via, not the board
// default), and a via declared in spec.rails is rated as declared.

// viaQ is the sizing request for a current through a track of width w.
func (c *ctx) viaQ(current, w float64) pcbauto.ViaSizing {
	q := pcbauto.ViaSizing{CurrentA: current, TempRiseC: c.tempRise, PlatingMil: pcbauto.DefaultViaPlatingMil,
		Class: pcbauto.ViaSize{DrillMil: c.rules.ViaDrill, DiaMil: c.rules.ViaDia}, Space: pcbauto.TrackViaSpace(w),
		LengthMil: c.rules.BoardThickMil, Clearance: c.rules.Clearance, HoleGap: c.rules.HoleGap}
	if r := c.spec.Rules; r != nil {
		if r.ViaPlatingMil > 0 {
			q.PlatingMil = r.ViaPlatingMil
		}
		q.MarginPct = r.ViaMarginPct
	}
	return q
}

func netVia(p pcbauto.ViaPlan, src string) *NetVia {
	return &NetVia{DrillMil: p.DrillMil, DiaMil: p.DiaMil, CountPerTransition: p.Count, PerViaA: p.PerViaA, AmpacityA: p.AmpacityA,
		CurrentA: p.CurrentA, MarginPct: p.MarginPct, PlatingMil: p.PlatingMil, LengthMil: p.LengthMil, ResistanceMOhm: p.ResistanceMOhm,
		DropMV: p.DropMV, Source: src, Why: p.Why}
}

// viaCurrent is the thermal current a net's vias carry (see
// pcbauto.NetPlan: the sizing current; a switch-node peak only without it).
func viaCurrent(np *NetPlan) float64 {
	if np.CurrentA > 0 {
		return np.CurrentA
	}
	return np.PeakA
}

// specVia is the via declared for a net in spec.rails.
func (c *ctx) specVia(net string) *SpecVia {
	for _, r := range c.spec.Rails {
		if strings.EqualFold(r.Net, net) && r.Via != nil {
			return r.Via
		}
	}
	return nil
}

// sizeVia sizes (or, declared, rates) a net's via with class as the
// preferred size.
func (c *ctx) sizeVia(net string, np *NetPlan, class pcbauto.ViaSize) {
	q := c.viaQ(viaCurrent(np), np.WidthMil.Outer)
	q.Class = class
	if d := c.specVia(net); d != nil {
		size := class
		if d.DrillMil > 0 && d.DiaMil > d.DrillMil {
			size = pcbauto.ViaSize{DrillMil: d.DrillMil, DiaMil: d.DiaMil}
		}
		n := d.Count
		if n <= 0 {
			qq := q
			qq.Class, qq.Ladder = size, []pcbauto.ViaSize{}
			n = pcbauto.SizeVias(qq).Count
		}
		p := pcbauto.EvalVias(q, size, n)
		p.Why = fmt.Sprintf("declared (spec.rails) %d × %.1f/%.1f mil: %.2f A for %.2f A, margin %.0f %%", n, size.DrillMil, size.DiaMil, p.AmpacityA, p.CurrentA, p.MarginPct)
		np.Via = netVia(p, "declared")
	} else {
		np.Via = netVia(pcbauto.SizeVias(q), "sized")
	}
	np.ViasPerTransition = np.Via.CountPerTransition
}

// classVias gives each class the largest member via and re-sizes the
// members for it (fewer vias of the larger size), then writes the why lines.
func (c *ctx) classVias(classes map[string]*NetClass) {
	board := pcbauto.ViaSize{DrillMil: c.rules.ViaDrill, DiaMil: c.rules.ViaDia}
	for _, nc := range classes {
		size := board
		for _, net := range nc.Nets {
			if v := c.out.Nets[net].Via; v != nil && v.Source != "declared" && v.DrillMil > size.DrillMil {
				size = pcbauto.ViaSize{DrillMil: v.DrillMil, DiaMil: v.DiaMil}
			}
		}
		nc.ViaDrillMil, nc.ViaDiaMil = size.DrillMil, size.DiaMil
		if size != board {
			var why []string
			for _, net := range nc.Nets {
				np := c.out.Nets[net]
				if np.Via != nil && np.Via.Source != "declared" {
					if np.Via.DrillMil < size.DrillMil {
						c.sizeVia(net, np, size)
						np.Via.Source = "class"
					}
					if np.Via.DrillMil == size.DrillMil {
						why = append(why, fmt.Sprintf("%s %d×", net, np.Via.CountPerTransition))
					}
				}
			}
			nc.Why = append(nc.Why, fmt.Sprintf("via %.1f/%.1f mil (board %.1f/%.1f): the members' current needs it where the class via does not fit the transition (%s)", size.DrillMil, size.DiaMil, board.DrillMil, board.DiaMil, strings.Join(why, ", ")))
		}
	}
	for _, net := range sortedKeys(c.out.Nets) {
		np := c.out.Nets[net]
		if v := np.Via; v != nil && (v.CountPerTransition > 1 || v.DrillMil != board.DrillMil || v.Source == "declared") {
			np.Why = append(np.Why, fmt.Sprintf("vias: %s; %d × %.1f/%.1f mil = %.2f A (%.2f A each, IPC-2221 barrel π(d+t)t, t=%.2f mil, ΔT %.0f °C), margin %.0f %%, %.3g mΩ each, %.2f mV per transition",
				v.Why, v.CountPerTransition, v.DrillMil, v.DiaMil, v.AmpacityA, v.PerViaA, v.PlatingMil, c.tempRise, v.MarginPct, v.ResistanceMOhm, v.DropMV))
		}
	}
}

// findVias reports vias that cannot carry their net's current: ERROR when
// the ampacity is below the current, WARN when the margin is short.
func (c *ctx) findVias() {
	margin := pcbauto.ViaMarginOr(c.viaQ(0, 0).MarginPct)
	for _, net := range sortedKeys(c.out.Nets) {
		np := c.out.Nets[net]
		v := np.Via
		if v == nil || v.CurrentA <= 0 {
			continue
		}
		what := fmt.Sprintf("%s: %d × %.1f/%.1f mil via per layer change carries %.2f A for %.2f A", net, v.CountPerTransition, v.DrillMil, v.DiaMil, v.AmpacityA, v.CurrentA)
		switch {
		case v.AmpacityA < v.CurrentA:
			c.add("error", "via-undersized", what+" — the via limits the path however wide the copper is", nil, []string{net},
				fmt.Sprintf("use %s, or keep the net on one layer / deliver it by a pour", c.viaFix(net, np)))
		case v.MarginPct < margin-1e-6:
			c.add("warn", "via-margin", fmt.Sprintf("%s (margin %.0f %% < %.0f %%)", what, v.MarginPct, margin), nil, []string{net},
				fmt.Sprintf("use %s", c.viaFix(net, np)))
		}
	}
}

// viaFix names the sized alternative for a net.
func (c *ctx) viaFix(net string, np *NetPlan) string {
	p := pcbauto.SizeVias(c.viaQ(viaCurrent(np), np.WidthMil.Outer))
	if !p.OK {
		return "a wider transition area (" + p.Why + ")"
	}
	return fmt.Sprintf("%d × %.1f/%.1f mil (%.2f A, margin %.0f %%)", p.Count, p.DrillMil, p.DiaMil, p.AmpacityA, math.Round(p.MarginPct))
}
