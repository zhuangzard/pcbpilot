package intent

import (
	"fmt"
	"math"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// floatInfo is a net that rides on a switching node: the gate drive of a
// high-side switch (Kelvin source, gate, the isolated driver output and its
// bias rail) sits a few volts from the switch node and moves with it.
type floatInfo struct {
	anchor       string  // the switch node (the transistor's source net)
	relLo, relHi float64 // voltage relative to the anchor
}

var (
	pinSource = []string{"S", "SOURCE", "E", "EMITTER"}
	pinKelvin = []string{"KS", "SS", "KELVIN", "K-S", "SK"}
	pinGate   = []string{"G", "GATE", "B", "BASE"}
)

// floatingIslands finds the nets referenced to a swinging transistor source
// (the switch node) and re-expresses their envelope on top of it. In the DC
// simulation such a floating gate drive sits near 0 V; on the board its
// copper swings with the switch node — up to 535 V against the DC bus
// return, not the "7.5 V" the solver reported (400 V inverter stress board:
// the high- and low-side Kelvin nets were planned 6 mil apart).
func (c *ctx) floatingIslands() {
	c.floats = map[string]floatInfo{}
	for _, p := range c.d.Parts {
		if c.kind(p.Ref) != pcbauto.KindTransistor {
			continue
		}
		src := c.pinNet(p.Ref, pinSource...)
		if src == "" || c.isGround(src) {
			continue
		}
		av := c.volts[src]
		if av.Peak <= av.Max+1e-9 && av.Min >= 0 {
			continue // not a swinging node
		}
		ref := src
		seeds := []string{}
		if ks := c.pinNet(p.Ref, pinKelvin...); ks != "" && ks != src {
			ref = ks
			seeds = append(seeds, ks)
		}
		if g := c.pinNet(p.Ref, pinGate...); g != "" && g != src {
			seeds = append(seeds, g)
		}
		if len(seeds) == 0 {
			continue
		}
		// Grow through two-pin passives (gate resistor, bootstrap / bias
		// decoupling): never through transistors, isolation parts, ICs,
		// connectors, grounds or the anchor itself.
		island := map[string]bool{}
		queue := append([]string(nil), seeds...)
		for _, s := range seeds {
			island[s] = true
		}
		for len(queue) > 0 {
			n := queue[0]
			queue = queue[1:]
			for _, ref2 := range c.partsOn(n) {
				k := c.kind(ref2)
				if k != pcbauto.KindResistor && k != pcbauto.KindCapacitor && k != pcbauto.KindDiode && k != pcbauto.KindInductor {
					continue
				}
				for _, m := range c.partNets(ref2) {
					if m == "" || m == src || island[m] || c.isGround(m) || c.mains[m] {
						continue
					}
					island[m] = true
					queue = append(queue, m)
				}
			}
		}
		// Isolated driver outputs / bias modules on the island reach their
		// other island pins too (OUTA and VDDA of the same channel): add the
		// nets that share a bridge part with the island reference.
		refV := c.volts[ref].Nom
		for n := range island {
			v := c.volts[n]
			rel := v.Nom - refV
			lo, hi := rel, rel
			if !v.isZero() {
				lo, hi = math.Min(rel, v.Min-refV), math.Max(rel, v.Max-refV)
			}
			c.floats[n] = floatInfo{anchor: src, relLo: lo, relHi: hi}
			abs := Voltage{Nom: round(av.Nom+rel, 4), Min: round(math.Min(av.Min, 0)+lo, 4), Max: round(av.Max+hi, 4)}
			abs.Peak = round(math.Max(math.Abs(abs.Min), math.Max(av.Peak+hi, abs.Max)), 4)
			c.volts[n] = abs
		}
	}
}

func (v Voltage) isZero() bool { return v.Min == 0 && v.Max == 0 && v.Nom == 0 && v.Peak == 0 }

// floatWhy explains a floating net's envelope.
func (c *ctx) floatWhy(net string) string {
	fi := c.floats[net]
	return fmt.Sprintf("rides on switch node %s (%s…%s V relative, gate-drive island): absolute %s…%s V, peak %s V",
		fi.anchor, trimFloat(fi.relLo, 2), trimFloat(fi.relHi, 2), trimFloat(c.volts[net].Min, 1), trimFloat(c.volts[net].Max, 1), trimFloat(c.volts[net].Peak, 1))
}

var _ = strings.ToUpper

// floatAnchor reports whether net is the switch node of an island.
func (c *ctx) floatAnchor(net string) bool {
	for _, fi := range c.floats {
		if fi.anchor == net {
			return true
		}
	}
	return false
}

// floatAnchorOf is the anchor of the first floating net among nets.
func (c *ctx) floatAnchorOf(nets []string) string {
	for _, n := range nets {
		if fi, ok := c.floats[n]; ok {
			return fi.anchor
		}
	}
	return ""
}

// relV is a net's voltage relative to its island's switch node (0 for the
// node itself), the largest magnitude of its relative span.
func (c *ctx) relV(net string) float64 {
	fi, ok := c.floats[net]
	if !ok {
		return 0
	}
	if math.Abs(fi.relLo) > math.Abs(fi.relHi) {
		return fi.relLo
	}
	return fi.relHi
}
