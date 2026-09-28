package postsim

import (
	"math"
	"sort"
)

// laplacian accumulates a symmetric conductance matrix: link(i, j, g) adds a
// conductance between two unknowns, ground(i, g) a conductance to the fixed
// reference (0 V / ambient). Unknowns are 0…n-1.
type laplacian struct {
	n    int
	diag []float64
	off  [][]offEntry
}

type offEntry struct {
	j int
	g float64
}

func newLaplacian(n int) *laplacian {
	return &laplacian{n: n, diag: make([]float64, n), off: make([][]offEntry, n)}
}

func (l *laplacian) link(i, j int, g float64) {
	if i == j || g == 0 {
		return
	}
	l.diag[i] += g
	l.diag[j] += g
	l.off[i] = append(l.off[i], offEntry{j, -g})
	l.off[j] = append(l.off[j], offEntry{i, -g})
}

func (l *laplacian) ground(i int, g float64) { l.diag[i] += g }

// csr is the compressed matrix.
type csr struct {
	n    int
	diag []float64
	ptr  []int
	col  []int
	val  []float64
}

func (l *laplacian) compress() *csr {
	m := &csr{n: l.n, diag: append([]float64(nil), l.diag...), ptr: make([]int, l.n+1)}
	for i := 0; i < l.n; i++ {
		row := l.off[i]
		sort.Slice(row, func(a, b int) bool { return row[a].j < row[b].j })
		for k, e := range row {
			if k > 0 && row[k-1].j == e.j {
				m.val[len(m.val)-1] += e.g
				continue
			}
			m.col = append(m.col, e.j)
			m.val = append(m.val, e.g)
		}
		m.ptr[i+1] = len(m.col)
	}
	return m
}

func (m *csr) mul(x, y []float64) {
	for i := 0; i < m.n; i++ {
		s := m.diag[i] * x[i]
		for k := m.ptr[i]; k < m.ptr[i+1]; k++ {
			s += m.val[k] * x[m.col[k]]
		}
		y[i] = s
	}
}

// ic0 is the zero fill-in incomplete Cholesky factor of the matrix (lower
// triangle by rows). A conductance matrix with a grounded node is an
// M-matrix, for which IC(0) exists; a non-positive pivot falls back to
// Jacobi.
type ic0 struct {
	n   int
	d   []float64 // diagonal of L
	ptr []int
	col []int
	val []float64
}

func (m *csr) ic0() *ic0 {
	f := &ic0{n: m.n, d: make([]float64, m.n), ptr: make([]int, m.n+1)}
	for i := 0; i < m.n; i++ {
		for k := m.ptr[i]; k < m.ptr[i+1]; k++ {
			if m.col[k] < i {
				f.col = append(f.col, m.col[k])
				f.val = append(f.val, m.val[k])
			}
		}
		f.ptr[i+1] = len(f.col)
	}
	for i := 0; i < m.n; i++ {
		ri0, ri1 := f.ptr[i], f.ptr[i+1]
		for a := ri0; a < ri1; a++ {
			k := f.col[a]
			// Σ_j<k L[i][j]·L[k][j] over the common pattern
			s := 0.0
			p, q := ri0, f.ptr[k]
			for p < a && q < f.ptr[k+1] {
				switch {
				case f.col[p] == f.col[q]:
					s += f.val[p] * f.val[q]
					p++
					q++
				case f.col[p] < f.col[q]:
					p++
				default:
					q++
				}
			}
			f.val[a] = (f.val[a] - s) / f.d[k]
		}
		s := m.diag[i]
		for a := ri0; a < ri1; a++ {
			s -= f.val[a] * f.val[a]
		}
		if s <= 1e-12*m.diag[i] {
			return nil
		}
		f.d[i] = math.Sqrt(s)
	}
	return f
}

// apply solves L·Lᵀ·z = r.
func (f *ic0) apply(r, z []float64) {
	for i := 0; i < f.n; i++ {
		s := r[i]
		for a := f.ptr[i]; a < f.ptr[i+1]; a++ {
			s -= f.val[a] * z[f.col[a]]
		}
		z[i] = s / f.d[i]
	}
	for i := f.n - 1; i >= 0; i-- {
		z[i] /= f.d[i]
		zi := z[i]
		for a := f.ptr[i]; a < f.ptr[i+1]; a++ {
			z[f.col[a]] -= f.val[a] * zi
		}
	}
}

// solve is preconditioned conjugate gradient (IC(0), else Jacobi) on the SPD
// system M·x = b. It returns x, the iterations used and the final relative
// residual.
func (m *csr) solve(b []float64, tol float64, maxIter int) ([]float64, int, float64) {
	return m.solveWith(b, tol, maxIter, m.ic0())
}

// solveWith is solve with a given preconditioner (nil = Jacobi).
func (m *csr) solveWith(b []float64, tol float64, maxIter int, pre *ic0) ([]float64, int, float64) {
	n := m.n
	x := make([]float64, n)
	r := append([]float64(nil), b...)
	bn := norm(b)
	if bn == 0 {
		return x, 0, 0
	}
	inv := make([]float64, n)
	for i, d := range m.diag {
		if d > 0 {
			inv[i] = 1 / d
		}
	}
	z := make([]float64, n)
	precond := func() {
		if pre != nil {
			pre.apply(r, z)
			return
		}
		for i := range z {
			z[i] = inv[i] * r[i]
		}
	}
	precond()
	p := append([]float64(nil), z...)
	q := make([]float64, n)
	rz := dot(r, z)
	it := 0
	res := 1.0
	for it = 1; it <= maxIter; it++ {
		m.mul(p, q)
		pq := dot(p, q)
		if pq <= 0 {
			break
		}
		a := rz / pq
		for i := range x {
			x[i] += a * p[i]
			r[i] -= a * q[i]
		}
		res = norm(r) / bn
		if res < tol {
			break
		}
		precond()
		rz2 := dot(r, z)
		beta := rz2 / rz
		rz = rz2
		for i := range p {
			p[i] = z[i] + beta*p[i]
		}
	}
	return x, it, res
}

func dot(a, b []float64) float64 {
	s := 0.0
	for i := range a {
		s += a[i] * b[i]
	}
	return s
}

func norm(a []float64) float64 { return math.Sqrt(dot(a, a)) }

// unionFind merges ideal (zero-resistance) connections before the solve.
type unionFind []int

func (u *unionFind) add() int {
	*u = append(*u, len(*u))
	return len(*u) - 1
}

func (u unionFind) find(i int) int {
	for u[i] != i {
		u[i] = u[u[i]]
		i = u[i]
	}
	return i
}

func (u unionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		if ra < rb {
			u[rb] = ra
		} else {
			u[ra] = rb
		}
	}
}
