package analogsim

import (
	"math"
	"sort"
)

// dcSolve solves the resistive network of the given passives (resistors by
// value, inductors as 1 mΩ, capacitors open) with the fixed node voltages.
// Floating nodes get 0 V. vals overrides passive values by ref.
func (c *circuit) dcSolve(parts []string, vals map[string]float64, fixed map[string]float64) map[string]float64 {
	type br struct {
		a, b string
		g    float64
	}
	var brs []br
	nodes := map[string]bool{}
	for _, ref := range parts {
		p := c.passive[ref]
		if p == nil || p.Kind == "C" {
			continue
		}
		v := p.Value
		if x, ok := vals[ref]; ok {
			v = x
		}
		if p.Kind == "L" {
			v = 1e-3
		}
		if v <= 0 {
			v = 1e-3
		}
		brs = append(brs, br{p.Nets[0], p.Nets[1], 1 / v})
		nodes[p.Nets[0]], nodes[p.Nets[1]] = true, true
	}
	var free []string
	for n := range nodes {
		if _, ok := fixed[n]; !ok {
			free = append(free, n)
		}
	}
	sort.Strings(free)
	idx := map[string]int{}
	for i, n := range free {
		idx[n] = i
	}
	N := len(free)
	A := make([][]float64, N)
	for i := range A {
		A[i] = make([]float64, N+1)
		A[i][i] = 1e-12 // leakage keeps floating islands solvable
	}
	for _, b := range brs {
		ia, oka := idx[b.a]
		ib, okb := idx[b.b]
		if oka {
			A[ia][ia] += b.g
		}
		if okb {
			A[ib][ib] += b.g
		}
		switch {
		case oka && okb:
			A[ia][ib] -= b.g
			A[ib][ia] -= b.g
		case oka:
			A[ia][N] += b.g * fixed[b.b]
		case okb:
			A[ib][N] += b.g * fixed[b.a]
		}
	}
	x := gauss(A)
	out := map[string]float64{}
	for n, v := range fixed {
		out[n] = v
	}
	for i, n := range free {
		out[n] = x[i]
	}
	return out
}

// theveninR is the DC resistance seen at node n with every fixed node grounded.
func (c *circuit) theveninR(parts []string, vals map[string]float64, n string, fixedNets []string) float64 {
	fixed := map[string]float64{}
	for _, f := range fixedNets {
		fixed[f] = 0
	}
	// Inject 1 A by fixing n at 1 V and measuring the current out of n.
	fixed[n] = 1
	v := c.dcSolve(parts, vals, fixed)
	i := 0.0
	for _, ref := range parts {
		p := c.passive[ref]
		if p == nil || p.Kind == "C" {
			continue
		}
		val := p.Value
		if x, ok := vals[ref]; ok {
			val = x
		}
		if p.Kind == "L" {
			val = 1e-3
		}
		if p.Nets[0] == n {
			i += (1 - v[p.Nets[1]]) / val
		} else if p.Nets[1] == n {
			i += (1 - v[p.Nets[0]]) / val
		}
	}
	if i <= 0 {
		return math.Inf(1)
	}
	return 1 / i
}

// capTo sums the capacitors between net n and any rail among parts.
func (c *circuit) capTo(parts []string, vals map[string]float64, n string) float64 {
	sum := 0.0
	for _, ref := range parts {
		p := c.passive[ref]
		if p == nil || p.Kind != "C" {
			continue
		}
		v := p.Value
		if x, ok := vals[ref]; ok {
			v = x
		}
		if (p.Nets[0] == n && c.isRail(p.Nets[1])) || (p.Nets[1] == n && c.isRail(p.Nets[0])) {
			sum += v
		}
	}
	return sum
}

func gauss(A [][]float64) []float64 {
	n := len(A)
	for col := 0; col < n; col++ {
		piv := col
		for r := col + 1; r < n; r++ {
			if math.Abs(A[r][col]) > math.Abs(A[piv][col]) {
				piv = r
			}
		}
		A[col], A[piv] = A[piv], A[col]
		if math.Abs(A[col][col]) < 1e-300 {
			continue
		}
		for r := 0; r < n; r++ {
			if r == col {
				continue
			}
			f := A[r][col] / A[col][col]
			if f == 0 {
				continue
			}
			for k := col; k <= n; k++ {
				A[r][k] -= f * A[col][k]
			}
		}
	}
	x := make([]float64, n)
	for i := 0; i < n; i++ {
		if A[i][i] != 0 {
			x[i] = A[i][n] / A[i][i]
		}
	}
	return x
}
