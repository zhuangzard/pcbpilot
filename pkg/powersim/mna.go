// Package powersim computes a DC operating point of a schematic's power tree
// (Modified Nodal Analysis + Newton-Raphson) and reports the current through
// every component pin, so PCB trace widths can be computed instead of guessed
// from net names.
//
// It is a DC/average model, not a transient SPICE: capacitors are open,
// inductors are their DCR, switching regulators are averaged (power-balance)
// controlled sources, and ripple is estimated with closed-form formulas.
package powersim

import (
	"errors"
	"fmt"
	"math"
)

// Ground is the reference node index.
const Ground = -1

const (
	thermalVoltage = 0.025852 // kT/q at 300 K
	gminNode       = 1e-12    // conductance from every node to ground (keeps floating nets solvable)
	gminJunction   = 1e-12
)

// PinRef names one schematic pin (ref + pin number).
type PinRef struct {
	Ref string
	Pin string
}

// Terminal is one element connection. Current reported for the terminal is
// split equally across Pins (the same-rail pins of one part). Pins may be empty
// for internal nodes.
type Terminal struct {
	Node int
	Pins []PinRef
}

// element is one MNA device.
type element interface {
	terminals() []Terminal
	branches() int
	setBranch(base int)
	// stamp adds the element's (linearised at x) contribution.
	stamp(s *system, x []float64)
	// currents returns the current flowing INTO the element at each terminal.
	currents(x []float64) []float64
}

// limiter is implemented by junction elements: limited reports whether the
// last stamp linearised at a voltage other than the iterate (pnjlim clipped),
// in which case Newton has not converged yet.
type limiter interface{ limited() bool }

// system is a dense MNA matrix A·x = b.
type system struct {
	n int
	a [][]float64
	b []float64
}

func newSystem(n int) *system {
	s := &system{n: n, a: make([][]float64, n), b: make([]float64, n)}
	for i := range s.a {
		s.a[i] = make([]float64, n)
	}
	return s
}

func (s *system) add(i, j int, v float64) {
	if i < 0 || j < 0 {
		return
	}
	s.a[i][j] += v
}

func (s *system) rhs(i int, v float64) {
	if i < 0 {
		return
	}
	s.b[i] += v
}

// conductance stamps g between nodes p and q.
func (s *system) conductance(p, q int, g float64) {
	s.add(p, p, g)
	s.add(q, q, g)
	s.add(p, q, -g)
	s.add(q, p, -g)
}

// currentInto stamps a constant current i flowing from node p through the
// element to node q (i.e. drawn from p, delivered to q).
func (s *system) currentInto(p, q int, i float64) {
	s.rhs(p, -i)
	s.rhs(q, i)
}

// nonlinear stamps a general nonlinear element given its terminal currents
// (into element) I at node voltages v0 and the Jacobian J[i][j] = dI_i/dV_j.
func (s *system) nonlinear(nodes []int, v0, cur []float64, jac [][]float64) {
	for i, ni := range nodes {
		if ni < 0 {
			continue
		}
		eq := cur[i]
		for j, nj := range nodes {
			s.add(ni, nj, jac[i][j])
			eq -= jac[i][j] * v0[j]
		}
		s.rhs(ni, -eq)
	}
}

func volt(x []float64, n int) float64 {
	if n < 0 {
		return 0
	}
	return x[n]
}

// solveDense solves A·x=b with partial-pivot Gaussian elimination.
func solveDense(a [][]float64, b []float64) ([]float64, error) {
	n := len(b)
	m := make([][]float64, n)
	for i := range a {
		m[i] = append([]float64(nil), a[i]...)
	}
	x := append([]float64(nil), b...)
	for k := 0; k < n; k++ {
		p, best := k, math.Abs(m[k][k])
		for i := k + 1; i < n; i++ {
			if v := math.Abs(m[i][k]); v > best {
				p, best = i, v
			}
		}
		if best < 1e-300 {
			return nil, errSingular
		}
		m[k], m[p] = m[p], m[k]
		x[k], x[p] = x[p], x[k]
		for i := k + 1; i < n; i++ {
			f := m[i][k] / m[k][k]
			if f == 0 {
				continue
			}
			row, piv := m[i], m[k]
			for j := k; j < n; j++ {
				row[j] -= f * piv[j]
			}
			x[i] -= f * x[k]
		}
	}
	for i := n - 1; i >= 0; i-- {
		sum := x[i]
		for j := i + 1; j < n; j++ {
			sum -= m[i][j] * x[j]
		}
		x[i] = sum / m[i][i]
	}
	return x, nil
}

var errSingular = errors.New("singular MNA matrix (floating source loop or two regulators driving one net)")

// Circuit is a set of nodes and elements.
type Circuit struct {
	names []string
	index map[string]int
	elems []element
	nb    int
}

// NewCircuit returns an empty circuit.
func NewCircuit() *Circuit { return &Circuit{index: map[string]int{}} }

// Node returns (creating) the node for a net name. Names in ground map to Ground
// by the caller; this always creates a real node.
func (c *Circuit) Node(name string) int {
	if i, ok := c.index[name]; ok {
		return i
	}
	c.index[name] = len(c.names)
	c.names = append(c.names, name)
	return len(c.names) - 1
}

// Internal creates a fresh internal node.
func (c *Circuit) Internal(prefix string) int {
	return c.Node(fmt.Sprintf("%s#%d", prefix, len(c.names)))
}

// NodeName returns the name of node i ("0" for ground).
func (c *Circuit) NodeName(i int) string {
	if i < 0 {
		return "0"
	}
	return c.names[i]
}

func (c *Circuit) add(e element) {
	e.setBranch(len(c.names) + c.nb) // provisional; fixed in finalize
	c.nb += e.branches()
	c.elems = append(c.elems, e)
}

func (c *Circuit) finalize() int {
	base := len(c.names)
	for _, e := range c.elems {
		e.setBranch(base)
		base += e.branches()
	}
	return base
}

// ---- linear elements ------------------------------------------------------

type resistor struct {
	t [2]Terminal
	r float64
}

func (e *resistor) terminals() []Terminal { return e.t[:] }
func (e *resistor) branches() int         { return 0 }
func (e *resistor) setBranch(int)         {}
func (e *resistor) stamp(s *system, _ []float64) {
	s.conductance(e.t[0].Node, e.t[1].Node, 1/e.r)
}
func (e *resistor) currents(x []float64) []float64 {
	i := (volt(x, e.t[0].Node) - volt(x, e.t[1].Node)) / e.r
	return []float64{i, -i}
}

// vsource: V(p) - V(n) = E + Rs·Ib, Ib = current into the p terminal.
type vsource struct {
	t      [2]Terminal
	e, rs  float64
	br     int
	active bool
}

func (e *vsource) terminals() []Terminal { return e.t[:] }
func (e *vsource) branches() int         { return 1 }
func (e *vsource) setBranch(b int)       { e.br = b }
func (e *vsource) stamp(s *system, _ []float64) {
	p, n := e.t[0].Node, e.t[1].Node
	s.add(p, e.br, 1)
	s.add(n, e.br, -1)
	s.add(e.br, p, 1)
	s.add(e.br, n, -1)
	s.add(e.br, e.br, -e.rs)
	s.rhs(e.br, e.e)
}
func (e *vsource) currents(x []float64) []float64 {
	return []float64{x[e.br], -x[e.br]}
}

// isource draws i from terminal 0 and delivers it to terminal 1.
type isource struct {
	t [2]Terminal
	i float64
}

func (e *isource) terminals() []Terminal { return e.t[:] }
func (e *isource) branches() int         { return 0 }
func (e *isource) setBranch(int)         {}
func (e *isource) stamp(s *system, _ []float64) {
	s.currentInto(e.t[0].Node, e.t[1].Node, e.i)
}
func (e *isource) currents([]float64) []float64 { return []float64{e.i, -e.i} }

// ---- nonlinear elements ---------------------------------------------------

func safeExp(v float64) (float64, float64) {
	const lim = 80
	if v > lim {
		ev := math.Exp(lim)
		return ev * (1 + v - lim), ev
	}
	ev := math.Exp(v)
	return ev, ev
}

// pnjlim is SPICE's junction-voltage limiter.
func pnjlim(vnew, vold, vt, vcrit float64) float64 {
	if vnew > vcrit && math.Abs(vnew-vold) > 2*vt {
		if vold > 0 {
			arg := 1 + (vnew-vold)/vt
			if arg > 0 {
				return vold + vt*math.Log(arg)
			}
			return vcrit
		}
		return vt * math.Log(vnew/vt)
	}
	return vnew
}

// diode is a Shockley junction anode→cathode.
type diode struct {
	t     [2]Terminal // anode, cathode
	is, n float64
	vold  float64
	lim   bool
}

func (e *diode) limited() bool { return e.lim }

func (e *diode) terminals() []Terminal { return e.t[:] }
func (e *diode) branches() int         { return 0 }
func (e *diode) setBranch(int)         {}
func (e *diode) nvt() float64          { return e.n * thermalVoltage }
func (e *diode) iv(v float64) (float64, float64) {
	nvt := e.nvt()
	ex, dex := safeExp(v / nvt)
	return e.is*(ex-1) + gminJunction*v, e.is*dex/nvt + gminJunction
}
func (e *diode) stamp(s *system, x []float64) {
	nvt := e.nvt()
	vd := volt(x, e.t[0].Node) - volt(x, e.t[1].Node)
	vcrit := nvt * math.Log(nvt/(math.Sqrt2*e.is))
	vl := pnjlim(vd, e.vold, nvt, vcrit)
	e.lim = math.Abs(vl-vd) > 1e-9
	vd = vl
	e.vold = vd
	i, g := e.iv(vd)
	s.conductance(e.t[0].Node, e.t[1].Node, g)
	s.currentInto(e.t[0].Node, e.t[1].Node, i-g*vd)
}
func (e *diode) currents(x []float64) []float64 {
	i, _ := e.iv(volt(x, e.t[0].Node) - volt(x, e.t[1].Node))
	return []float64{i, -i}
}

// bjt is an Ebers-Moll transport model. Terminals: C, B, E.
type bjt struct {
	t          [3]Terminal
	pnp        bool
	is, bf, br float64
	vbeOld     float64
	vbcOld     float64
	lim        bool
}

func (e *bjt) limited() bool { return e.lim }

func (e *bjt) terminals() []Terminal { return e.t[:] }
func (e *bjt) branches() int         { return 0 }
func (e *bjt) setBranch(int)         {}
func (e *bjt) pol() float64 {
	if e.pnp {
		return -1
	}
	return 1
}

// eval returns currents into C, B, E and their Jacobian w.r.t. V(C), V(B), V(E).
func (e *bjt) eval(vbe, vbc float64) ([]float64, [][]float64) {
	p := e.pol()
	vt := thermalVoltage
	ef, gfe := safeExp(vbe / vt)
	er, gre := safeExp(vbc / vt)
	iF := e.is * (ef - 1)
	iR := e.is * (er - 1)
	gf := e.is * gfe / vt
	gr := e.is * gre / vt
	ic := iF - iR - iR/e.br + gminJunction*(vbe-vbc)
	ib := iF/e.bf + iR/e.br + gminJunction*(vbe+vbc)
	dicBE, dicBC := gf+gminJunction, -gr-gr/e.br-gminJunction
	dibBE, dibBC := gf/e.bf+gminJunction, gr/e.br+gminJunction
	// Vbe = p(Vb-Ve), Vbc = p(Vb-Vc); I = p·I'; dI/dV = p·p·dI'/dV' = dI'/dV'.
	dIc := []float64{-dicBC, dicBE + dicBC, -dicBE}
	dIb := []float64{-dibBC, dibBE + dibBC, -dibBE}
	dIe := []float64{-(dIc[0] + dIb[0]), -(dIc[1] + dIb[1]), -(dIc[2] + dIb[2])}
	cur := []float64{p * ic, p * ib, -p * (ic + ib)}
	return cur, [][]float64{dIc, dIb, dIe}
}
func (e *bjt) junctions(x []float64) (float64, float64) {
	p := e.pol()
	vc, vb, ve := volt(x, e.t[0].Node), volt(x, e.t[1].Node), volt(x, e.t[2].Node)
	return p * (vb - ve), p * (vb - vc)
}
func (e *bjt) stamp(s *system, x []float64) {
	vt := thermalVoltage
	vcrit := vt * math.Log(vt/(math.Sqrt2*e.is))
	vbe0, vbc0 := e.junctions(x)
	vbe := pnjlim(vbe0, e.vbeOld, vt, vcrit)
	vbc := pnjlim(vbc0, e.vbcOld, vt, vcrit)
	e.lim = math.Abs(vbe-vbe0) > 1e-9 || math.Abs(vbc-vbc0) > 1e-9
	e.vbeOld, e.vbcOld = vbe, vbc
	cur, jac := e.eval(vbe, vbc)
	// Terminal voltages consistent with the limited junctions (take E as base).
	p := e.pol()
	ve := volt(x, e.t[2].Node)
	vb := ve + p*vbe
	vc := vb - p*vbc
	nodes := []int{e.t[0].Node, e.t[1].Node, e.t[2].Node}
	s.nonlinear(nodes, []float64{vc, vb, ve}, cur, jac)
}
func (e *bjt) currents(x []float64) []float64 {
	vbe, vbc := e.junctions(x)
	cur, _ := e.eval(vbe, vbc)
	return cur
}

// load is a constant-current sink between supply (0) and return (1) that
// degrades to a resistor below the knee voltage, so an unpowered rail draws
// nothing instead of forcing a negative voltage.
type load struct {
	t    [2]Terminal
	inom float64
	knee float64
}

func (e *load) terminals() []Terminal { return e.t[:] }
func (e *load) branches() int         { return 0 }
func (e *load) setBranch(int)         {}
func (e *load) iv(v float64) (float64, float64) {
	if e.inom == 0 {
		return 0, 0
	}
	if v >= e.knee {
		return e.inom, 0
	}
	g := e.inom / e.knee
	return g * v, g
}
func (e *load) stamp(s *system, x []float64) {
	v := volt(x, e.t[0].Node) - volt(x, e.t[1].Node)
	i, g := e.iv(v)
	s.conductance(e.t[0].Node, e.t[1].Node, g)
	s.currentInto(e.t[0].Node, e.t[1].Node, i-g*v)
}
func (e *load) currents(x []float64) []float64 {
	i, _ := e.iv(volt(x, e.t[0].Node) - volt(x, e.t[1].Node))
	return []float64{i, -i}
}

// Regulator modes.
const (
	modeOn      = "regulating"
	modeDropout = "dropout"
	modeOff     = "off"
)

// regulator is an averaged LDO/buck: an output branch that enforces
// V(senseP) - V(senseN) = vreg, and an input draw Iin = kIn·Iout + iq.
// Terminals: in, out, gnd, senseP, senseN, en (en optional: Node may be Ground
// with no pins and enUsed=false).
type regulator struct {
	t        [6]Terminal
	vreg     float64
	vdrop    float64 // dropout: V(out) = V(in) - vdrop
	iq       float64
	kIn      float64 // input current per output current (LDO: 1; buck: Vout/(η·Vin))
	mode     string
	br       int
	enUsed   bool
	enThresh float64
	vinMin   float64
	buck     bool
	eta      float64
}

func (e *regulator) terminals() []Terminal { return e.t[:] }
func (e *regulator) branches() int         { return 1 }
func (e *regulator) setBranch(b int)       { e.br = b }
func (e *regulator) stamp(s *system, _ []float64) {
	in, out, gnd := e.t[0].Node, e.t[1].Node, e.t[2].Node
	sp, sn := e.t[3].Node, e.t[4].Node
	// Output branch Ib (into element at out) returns through gnd.
	s.add(out, e.br, 1)
	s.add(gnd, e.br, -1)
	iq := e.iq
	switch e.mode {
	case modeOn:
		s.add(e.br, sp, 1)
		s.add(e.br, sn, -1)
		s.rhs(e.br, e.vreg)
	case modeDropout:
		s.add(e.br, out, 1)
		s.add(e.br, in, -1)
		s.rhs(e.br, -e.vdrop)
	default: // off
		s.add(e.br, e.br, 1)
		iq = 0
	}
	// Input draw Iin = kIn·(-Ib) + iq, from in to gnd.
	s.add(in, e.br, -e.kIn)
	s.add(gnd, e.br, e.kIn)
	s.currentInto(in, gnd, iq)
}
func (e *regulator) outCurrent(x []float64) float64 { return -x[e.br] }
func (e *regulator) inCurrent(x []float64) float64 {
	iq := e.iq
	if e.mode == modeOff {
		iq = 0
	}
	return e.kIn*e.outCurrent(x) + iq
}
func (e *regulator) currents(x []float64) []float64 {
	ib := x[e.br]
	iin := e.inCurrent(x)
	return []float64{iin, ib, -ib - iin, 0, 0, 0}
}

// ---- solve ----------------------------------------------------------------

// Solution is a converged operating point.
type Solution struct {
	X          []float64
	Iterations int
	Converged  bool
}

// solveNR runs Newton-Raphson from x0.
func (c *Circuit) solveNR(x0 []float64) (*Solution, error) {
	n := c.finalize()
	x := make([]float64, n)
	copy(x, x0)
	const maxIter = 400
	for it := 1; it <= maxIter; it++ {
		s := newSystem(n)
		for i := range c.names {
			s.add(i, i, gminNode)
		}
		limited := false
		for _, e := range c.elems {
			e.stamp(s, x)
			if l, ok := e.(limiter); ok && l.limited() {
				limited = true
			}
		}
		xn, err := solveDense(s.a, s.b)
		if err != nil {
			return nil, err
		}
		conv := !limited
		for i := range xn {
			d := xn[i] - x[i]
			if math.IsNaN(xn[i]) || math.IsInf(xn[i], 0) {
				return nil, fmt.Errorf("numeric overflow at iteration %d", it)
			}
			if math.Abs(d) > 1e-9+1e-6*math.Abs(xn[i]) {
				conv = false
			}
		}
		if it > 150 {
			// Damped update to break limit cycles at piecewise knees.
			for i := range xn {
				xn[i] = x[i] + 0.5*(xn[i]-x[i])
			}
		}
		x = xn
		if conv && it > 1 {
			return &Solution{X: x, Iterations: it, Converged: true}, nil
		}
	}
	return &Solution{X: x, Iterations: maxIter, Converged: false}, nil
}

// regulators returns the regulator elements.
func (c *Circuit) regulators() []*regulator {
	var out []*regulator
	for _, e := range c.elems {
		if r, ok := e.(*regulator); ok {
			out = append(out, r)
		}
	}
	return out
}

// Solve finds the operating point, iterating the regulator modes and the buck
// power-balance coefficient around Newton-Raphson.
func (c *Circuit) Solve() (*Solution, int, bool, error) {
	n := c.finalize()
	x := make([]float64, n)
	regs := c.regulators()
	for _, r := range regs {
		if r.mode == "" {
			r.mode = modeOn
		}
		if r.kIn == 0 {
			r.kIn = 1
		}
	}
	var sol *Solution
	total := 0
	const maxOuter = 60
	for outer := 1; outer <= maxOuter; outer++ {
		var err error
		sol, err = c.solveNR(x)
		if err != nil {
			return nil, total, false, err
		}
		total += sol.Iterations
		x = sol.X
		changed := false
		for _, r := range regs {
			in, out, gnd := volt(x, r.t[0].Node), volt(x, r.t[1].Node), volt(x, r.t[2].Node)
			vin, vout := in-gnd, out-gnd
			mode := r.mode
			enOK := !r.enUsed || volt(x, r.t[5].Node)-gnd >= r.enThresh
			switch {
			case !enOK:
				mode = modeOff
			case r.mode == modeOff:
				if vin >= r.vinMin {
					mode = modeOn
				}
			case vin < r.vinMin:
				mode = modeOff
			case r.mode == modeOn && vout > vin-r.vdrop+1e-9:
				mode = modeDropout
			case r.mode == modeDropout:
				sp, sn := volt(x, r.t[3].Node), volt(x, r.t[4].Node)
				if sp-sn > r.vreg+1e-9 {
					mode = modeOn
				}
			}
			if mode != r.mode {
				r.mode = mode
				changed = true
			}
			if r.buck && r.mode != modeOff && vin > 0 {
				k := math.Max(vout, 0) / (r.eta * vin)
				if math.Abs(k-r.kIn) > 1e-10*math.Max(1, k) {
					r.kIn = k
					changed = true
				}
			}
		}
		if !changed {
			return sol, total, sol.Converged, nil
		}
	}
	return sol, total, false, nil
}
