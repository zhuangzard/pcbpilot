package postsim

import "math"

// Steady-state 2.5-D finite-volume thermal model.
//
// Unknowns: the temperature rise θ = T − T_ambient of every (copper layer,
// board cell). Conductances (square cells of side h, so an in-plane link is
// k·t·h/h = k·t):
//
//   - in-plane on layer k: k_Cu·t_Cu·c̄ + k_FR4,xy·t_share, where c̄ is the
//     harmonic mean of the two cells' copper coverage and t_share half of each
//     adjacent dielectric (the dielectric's own lateral spreading);
//   - vertical between layers k and k+1: k_FR4,z·h²/d plus every via /
//     plated hole in the cell as a copper barrel k_Cu·π(D+t)t/h_span;
//   - top / bottom face to ambient: (h_conv + h_rad)·h², h_rad linearised
//     4εσT³ and refined from the solved surface temperature (ε > 0 only).
//
// Sources: part dissipation on its pads (by pad area, on the part's side,
// through-hole pads on every layer) plus the Joule heat of the DC solve.

// ThermalOptions are the physical parameters.
type ThermalOptions struct {
	AmbientC      float64
	HTop, HBottom float64 // W/(m²·K)
	Emissivity    float64 // 0 = no radiation
	KFR4XY, KFR4Z float64 // W/(m·K)
	PlatingMm     float64
}

type thermalModel struct {
	g     *Grid
	st    *Stackup
	nl    int
	idx   []int // k*NC + c → unknown (-1 off-board)
	cells []int // unknown → k*NC + c
	lap   *laplacian
	conv  []float64 // per unknown: convective conductance (W/K) before radiation
	gcov  [][]float64
	base  *csr // conduction only
	full  *csr // conduction + convection (no radiation)
	pre   *ic0 // IC(0) of full
}

func buildThermal(b *Board, g *Grid, st *Stackup, o ThermalOptions) *thermalModel {
	nl, nc := len(st.Layers), g.cells()
	m := &thermalModel{g: g, st: st, nl: nl, idx: make([]int, nl*nc)}
	for i := range m.idx {
		m.idx[i] = -1
	}
	for k := 0; k < nl; k++ {
		for c := 0; c < nc; c++ {
			if g.Inside[c] {
				m.idx[k*nc+c] = len(m.cells)
				m.cells = append(m.cells, k*nc+c)
			}
		}
	}
	m.lap = newLaplacian(len(m.cells))
	m.conv = make([]float64, len(m.cells))
	hM := g.Cell * MilMm * 1e-3
	A := hM * hM
	m.gcov = make([][]float64, nl)
	for k := 0; k < nl; k++ {
		cov := g.coverage(k, 0, false)
		m.gcov[k] = cov
		share := 0.0
		if k > 0 {
			share += st.DielMm[k-1] / 2
		}
		if k < nl-1 {
			share += st.DielMm[k] / 2
		}
		gd := o.KFR4XY * share * 1e-3
		gc := KCu * st.cuM(k)
		for c := 0; c < nc; c++ {
			i := m.idx[k*nc+c]
			if i < 0 {
				continue
			}
			ix, iy := c%g.NX, c/g.NX
			for _, c2 := range []int{c + 1, c + g.NX} {
				if c2 == c+1 && ix+1 >= g.NX || c2 == c+g.NX && iy+1 >= g.NY {
					continue
				}
				j := m.idx[k*nc+c2]
				if j < 0 {
					continue
				}
				h := 0.0
				if cov[c] > 0 && cov[c2] > 0 {
					h = 2 / (1/cov[c] + 1/cov[c2])
				}
				m.lap.link(i, j, gc*h+gd)
			}
		}
	}
	// Vertical: dielectric + barrels.
	barrel := make([]float64, nc) // Σ π(D+t)t per cell (m²)
	t := o.PlatingMm * 1e-3
	for _, v := range b.Vias {
		if c := g.cellAt(v.C); c >= 0 {
			d := v.Drill * MilMm * 1e-3
			barrel[c] += math.Pi * (d + t) * t
		}
	}
	for _, p := range b.Parts {
		for _, pd := range p.Pads {
			if pd.Layer == LayerMulti && pd.Drill > 0 {
				if c := g.cellAt(pd.C); c >= 0 {
					d := pd.Drill * MilMm * 1e-3
					barrel[c] += math.Pi * (d + t) * t
				}
			}
		}
	}
	for k := 0; k+1 < nl; k++ {
		dz := st.DielMm[k] * 1e-3
		span := st.spanM(k, k+1)
		for c := 0; c < nc; c++ {
			i, j := m.idx[k*nc+c], m.idx[(k+1)*nc+c]
			if i < 0 || j < 0 {
				continue
			}
			m.lap.link(i, j, o.KFR4Z*A/dz+KCu*barrel[c]/span)
		}
	}
	for c := 0; c < nc; c++ {
		if i := m.idx[c]; i >= 0 {
			m.conv[i] += o.HTop * A
		}
		if nl > 1 {
			if i := m.idx[(nl-1)*nc+c]; i >= 0 {
				m.conv[i] += o.HBottom * A
			}
		} else if i := m.idx[c]; i >= 0 {
			m.conv[i] += o.HBottom * A
		}
	}
	m.base = m.lap.compress()
	m.full = &csr{n: m.base.n, diag: append([]float64(nil), m.base.diag...), ptr: m.base.ptr, col: m.base.col, val: m.base.val}
	for i := range m.full.diag {
		m.full.diag[i] += m.conv[i]
	}
	m.pre = m.full.ic0()
	return m
}

// solve returns θ per unknown for heat q (W per unknown), with the radiation
// refinement when ε > 0, plus the convective+radiative loss per unknown.
func (m *thermalModel) solve(q []float64, o ThermalOptions) (theta, loss []float64, iters int, res float64) {
	n := len(m.cells)
	hrad := make([]float64, n)
	ta := o.AmbientC + 273.15
	A := math.Pow(m.g.Cell*MilMm*1e-3, 2)
	nl, nc := m.nl, m.g.cells()
	surface := make([]bool, n)
	for c := 0; c < nc; c++ {
		if i := m.idx[c]; i >= 0 {
			surface[i] = true
		}
		if i := m.idx[(nl-1)*nc+c]; i >= 0 {
			surface[i] = true
		}
	}
	faces := func(i int) float64 {
		if nl == 1 {
			return 2
		}
		return 1
	}
	passes := 1
	if o.Emissivity > 0 {
		for i := range hrad {
			if surface[i] {
				hrad[i] = 4 * o.Emissivity * sigmaSB * ta * ta * ta * A * faces(i)
			}
		}
		passes = 4
	}
	for pass := 0; pass < passes; pass++ {
		if o.Emissivity <= 0 {
			theta, iters, res = m.full.solveWith(q, 1e-10, 40*n+5000, m.pre)
		} else {
			mat := &csr{n: m.base.n, diag: append([]float64(nil), m.base.diag...), ptr: m.base.ptr, col: m.base.col, val: m.base.val}
			for i := range mat.diag {
				mat.diag[i] += m.conv[i] + hrad[i]
			}
			theta, iters, res = mat.solve(q, 1e-10, 40*n+5000)
		}
		loss = make([]float64, n)
		for i := range loss {
			loss[i] = (m.conv[i] + hrad[i]) * theta[i]
		}
		if o.Emissivity > 0 && pass < passes-1 {
			for i := range hrad {
				if surface[i] {
					T := ta + theta[i]
					hrad[i] = o.Emissivity * sigmaSB * (T*T + ta*ta) * (T + ta) * A * faces(i)
				}
			}
		}
	}
	return theta, loss, iters, res
}
