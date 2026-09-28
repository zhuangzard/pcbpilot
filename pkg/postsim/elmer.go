package postsim

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Open-source cross-check of the thermal model with Elmer FEM
// (ElmerSolver, HeatSolver), the way `sim power --spice-check` cross-checks
// the circuit with ngspice.
//
// The deck is the SAME model on a vertex-centred 3-D hexahedral mesh: one
// mesh node per (copper layer, board cell centre) — exactly the unknowns of
// the finite-volume solve — and one 8-node hexahedron per 2×2 block of board
// cells between two consecutive copper layers. Element properties are the
// finite-volume ones smeared over the element:
//
//	k_xy = (k_FR4·d + Σ f·k_Cu·t·coverage) / h_e   (f = 1 outer, ½ inner copper)
//	k_z  = (k_FR4·A/d + k_Cu·A_barrel/span) · h_e / A
//	q    = the node heat (parts + Joule) shared equally by the node's elements
//
// Elements are grouped into bodies by quantised (k_xy, k_z, q) — 5 % bins,
// volume-weighted means per body, so the total power is conserved exactly.
// Top/bottom faces carry the convection coefficient (Robin BC), edges are
// adiabatic, as in the finite-volume model. SaveScalars writes the
// temperature at probe coordinates (the board maximum and the hottest cell
// under each dissipating part) to probes.dat.

// ElmerCheck is the cross-check result recorded in post.json.
type ElmerCheck struct {
	Status   string       `json:"status"` // agree | disagree | skipped | exported | error
	Note     string       `json:"note,omitempty"`
	Deck     string       `json:"deck,omitempty"`
	Nodes    int          `json:"nodes"`
	Elements int          `json:"elements"`
	Boundary int          `json:"boundaryFaces"`
	Bodies   int          `json:"bodies"`
	TolC     float64      `json:"tolC"`
	MaxDiffC float64      `json:"maxDiffC,omitempty"`
	Probes   []ElmerProbe `json:"probes"`
}

// ElmerProbe is one comparison point.
type ElmerProbe struct {
	Name     string     `json:"name"`
	Layer    string     `json:"layer"`
	X        float64    `json:"x"` // mil
	Y        float64    `json:"y"`
	ModelC   float64    `json:"modelC"`
	ElmerC   float64    `json:"elmerC,omitempty"`
	HasElmer bool       `json:"hasElmer"`
	XYZ      [3]float64 `json:"xyzM"`
}

type elmerDeck struct {
	nodes    [][3]float64
	elems    [][9]int // body, n1..n8
	bounds   [][6]int // bc, parent, n1..n4
	bodies   []elmerBody
	probes   []ElmerProbe
	hTop     float64
	hBot     float64
	ambientC float64
}

type elmerBody struct {
	kxy, kz, q float64
}

// ExportElmer writes the Elmer deck of the hottest scenario into dir (mesh/
// + case.sif + probes.json) and records it in res.Elmer.
func (res *Result) ExportElmer(dir string) error {
	d, err := res.elmerDeck()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "mesh"), 0o755); err != nil {
		return err
	}
	if err := d.write(dir); err != nil {
		return err
	}
	tol := math.Max(1, 0.05*(res.Thermal.MaxBoardC-res.Settings.AmbientC))
	res.Elmer = &ElmerCheck{Status: "exported", Deck: dir, Nodes: len(d.nodes), Elements: len(d.elems), Boundary: len(d.bounds),
		Bodies: len(d.bodies), TolC: round(tol, 2), Probes: d.probes}
	return nil
}

// RunElmer runs ElmerSolver on an exported deck when it is on PATH and
// compares the probes; otherwise the check is marked skipped.
func (res *Result) RunElmer(timeout time.Duration) {
	e := res.Elmer
	if e == nil {
		return
	}
	bin, err := exec.LookPath("ElmerSolver")
	if err != nil {
		e.Status = "skipped"
		e.Note = "ElmerSolver not installed: the deck is written for a later run (cd " + e.Deck + " && ElmerSolver case.sif), then `pcbpilot sim post-layout … --elmer-result " + filepath.Join(e.Deck, "probes.dat") + "`"
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "case.sif")
	cmd.Dir = e.Deck
	out, err := cmd.CombinedOutput()
	_ = os.WriteFile(filepath.Join(e.Deck, "elmer.log"), out, 0o644)
	if err != nil {
		e.Status = "error"
		e.Note = fmt.Sprintf("ElmerSolver failed: %v (see elmer.log)", err)
		return
	}
	raw, err := os.ReadFile(filepath.Join(e.Deck, "probes.dat"))
	if err != nil {
		e.Status = "error"
		e.Note = "ElmerSolver ran but wrote no probes.dat"
		return
	}
	res.CompareElmer(raw)
}

// CompareElmer reads a SaveScalars probes.dat (the last row; its last N
// numbers are the probe temperatures in probe order) and compares.
func (res *Result) CompareElmer(raw []byte) {
	e := res.Elmer
	if e == nil {
		return
	}
	var last []float64
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		var row []float64
		for _, f := range strings.Fields(sc.Text()) {
			if v, err := strconv.ParseFloat(f, 64); err == nil {
				row = append(row, v)
			}
		}
		if len(row) > 0 {
			last = row
		}
	}
	if len(last) < len(e.Probes) {
		e.Status = "error"
		e.Note = fmt.Sprintf("probes.dat has %d values, expected ≥ %d", len(last), len(e.Probes))
		return
	}
	vals := last[len(last)-len(e.Probes):]
	e.MaxDiffC = 0
	for i := range e.Probes {
		e.Probes[i].ElmerC, e.Probes[i].HasElmer = round(vals[i], 3), true
		e.MaxDiffC = math.Max(e.MaxDiffC, math.Abs(vals[i]-e.Probes[i].ModelC))
	}
	e.MaxDiffC = round(e.MaxDiffC, 3)
	if e.MaxDiffC <= e.TolC {
		e.Status = "agree"
		e.Note = fmt.Sprintf("Elmer FEM and the finite-volume model agree within %.2f °C (tolerance %.2f °C)", e.MaxDiffC, e.TolC)
	} else {
		e.Status = "disagree"
		e.Note = fmt.Sprintf("Elmer FEM differs by up to %.2f °C (> tolerance %.2f °C): check the mesh/cell size and the model before trusting either", e.MaxDiffC, e.TolC)
		res.Findings = append(res.Findings, Finding{Severity: "warn", Kind: "elmer-check", Message: e.Note})
		if res.Verdict.Status == "pass" {
			res.Verdict.Status = "warn"
		}
		res.Verdict.Reasons = append(res.Verdict.Reasons, e.Note)
	}
}

func (res *Result) elmerDeck() (*elmerDeck, error) {
	if res.grid == nil || res.thermal == nil || res.hotQ == nil {
		return nil, fmt.Errorf("elmer: run the simulation first")
	}
	g, st, tm := res.grid, res.Stackup, res.thermal
	nl, nc := len(st.Layers), g.cells()
	if nl < 2 {
		return nil, fmt.Errorf("elmer: needs ≥ 2 copper layers")
	}
	d := &elmerDeck{hTop: res.Settings.HTop, hBot: res.Settings.HBottom, ambientC: res.Settings.AmbientC}
	hM := g.Cell * MilMm * 1e-3
	A := hM * hM
	node := make([]int, nl*nc) // 1-based mesh node id, 0 = none
	for k := 0; k < nl; k++ {
		z := -st.Layers[k].ZMm * 1e-3
		for c := 0; c < nc; c++ {
			if !g.Inside[c] {
				continue
			}
			p := g.cellCentre(c)
			d.nodes = append(d.nodes, [3]float64{(p.X - g.X0) * MilMm * 1e-3, (p.Y - g.Y0) * MilMm * 1e-3, z})
			node[k*nc+c] = len(d.nodes)
		}
	}
	// Barrel area per cell.
	barrel := make([]float64, nc)
	t := res.Settings.PlatingMil * MilMm * 1e-3
	for _, v := range res.board.Vias {
		if c := g.cellAt(v.C); c >= 0 {
			dd := v.Drill * MilMm * 1e-3
			barrel[c] += math.Pi * (dd + t) * t
		}
	}
	for _, p := range res.board.Parts {
		for _, pd := range p.Pads {
			if pd.Layer == LayerMulti && pd.Drill > 0 {
				if c := g.cellAt(pd.C); c >= 0 {
					dd := pd.Drill * MilMm * 1e-3
					barrel[c] += math.Pi * (dd + t) * t
				}
			}
		}
	}
	// Elements: per slab, per 2×2 block of inside cells.
	type el struct {
		n        [8]int
		kxy, kz  float64
		vol      float64
		heat     float64
		nodeKeys [8]int
	}
	var els []el
	adj := map[int]int{} // node key → number of elements
	for k := 0; k+1 < nl; k++ {
		span := st.spanM(k, k+1)
		dz := st.DielMm[k] * 1e-3
		fk, fk1 := 0.5, 0.5
		if k == 0 {
			fk = 1
		}
		if k+1 == nl-1 {
			fk1 = 1
		}
		for iy := 0; iy+1 < g.NY; iy++ {
			for ix := 0; ix+1 < g.NX; ix++ {
				cs := [4]int{iy*g.NX + ix, iy*g.NX + ix + 1, (iy+1)*g.NX + ix + 1, (iy+1)*g.NX + ix}
				ok := true
				for _, c := range cs {
					if !g.Inside[c] {
						ok = false
					}
				}
				if !ok {
					continue
				}
				var e el
				kxy, kz := 0.0, 0.0
				for i, c := range cs {
					// lower face = deeper layer k+1 first (positive orientation, z up)
					e.nodeKeys[i] = (k+1)*nc + c
					e.nodeKeys[i+4] = k*nc + c
					gxy := res.Settings.KFR4XY*dz + fk*KCu*st.cuM(k)*tm.gcov[k][c] + fk1*KCu*st.cuM(k+1)*tm.gcov[k+1][c]
					kxy += gxy / span / 4
					gz := res.Settings.KFR4Z*A/dz + KCu*barrel[c]/span
					kz += gz * span / A / 4
				}
				for i := range e.n {
					e.n[i] = node[e.nodeKeys[i]]
					adj[e.nodeKeys[i]]++
				}
				e.kxy, e.kz, e.vol = kxy, kz, A*span
				els = append(els, e)
			}
		}
	}
	// Node heat → elements.
	for i := range els {
		for _, key := range els[i].nodeKeys {
			if u := tm.idx[key]; u >= 0 && res.hotQ[u] != 0 {
				els[i].heat += res.hotQ[u] / float64(adj[key])
			}
		}
	}
	// Negligible sources (< 0.1 % of the densest element, e.g. the Joule
	// heat of lightly loaded copper) are folded into the real sources in
	// proportion, so the total power is unchanged.
	qmax, total, kept := 0.0, 0.0, 0.0
	for _, e := range els {
		qmax = math.Max(qmax, e.heat/e.vol)
		total += e.heat
	}
	for i := range els {
		if els[i].heat/els[i].vol < 1e-3*qmax {
			els[i].heat = 0
		}
		kept += els[i].heat
	}
	if kept > 0 && total > kept {
		for i := range els {
			els[i].heat *= total / kept
		}
	}
	// Bodies by quantised properties (15 % bins for conductivity, 30 % for
	// heat density; volume-weighted means inside a body).
	binK := func(v float64) int64 {
		if v <= 0 {
			return math.MinInt32
		}
		return int64(math.Round(math.Log(v) / math.Log(1.15)))
	}
	binQ := func(v float64) int64 {
		if v <= 0 {
			return math.MinInt32
		}
		return int64(math.Round(math.Log(v) / math.Log(1.3)))
	}
	type bkey struct{ a, b, c int64 }
	type bacc struct{ vol, kxyV, kzV, heat float64 }
	bodyOf := map[bkey]int{}
	var acc []bacc
	elemBody := make([]int, len(els))
	for i, e := range els {
		key := bkey{binK(e.kxy), binK(e.kz), binQ(e.heat / e.vol)}
		b, ok := bodyOf[key]
		if !ok {
			b = len(acc)
			bodyOf[key] = b
			acc = append(acc, bacc{})
		}
		acc[b].vol += e.vol
		acc[b].kxyV += e.kxy * e.vol
		acc[b].kzV += e.kz * e.vol
		acc[b].heat += e.heat
		elemBody[i] = b
	}
	for _, a := range acc {
		d.bodies = append(d.bodies, elmerBody{kxy: a.kxyV / a.vol, kz: a.kzV / a.vol, q: a.heat / a.vol})
	}
	for i, e := range els {
		var row [9]int
		row[0] = elemBody[i] + 1
		copy(row[1:], e.n[:])
		d.elems = append(d.elems, row)
	}
	// Boundary faces: top (layer 0) and bottom (layer nl-1) quads.
	for i, e := range els {
		n := e.nodeKeys
		if n[4]/nc == 0 { // upper face on the top layer
			d.bounds = append(d.bounds, [6]int{1, i + 1, node[n[4]], node[n[5]], node[n[6]], node[n[7]]})
		}
		if n[0]/nc == nl-1 {
			d.bounds = append(d.bounds, [6]int{2, i + 1, node[n[3]], node[n[2]], node[n[1]], node[n[0]]})
		}
	}
	// Probes: the board maximum and the hottest cell under each dissipating part.
	addProbe := func(name string, k, c int) {
		u := tm.idx[k*nc+c]
		if u < 0 || node[k*nc+c] == 0 {
			return
		}
		p := g.cellCentre(c)
		d.probes = append(d.probes, ElmerProbe{Name: name, Layer: st.Layers[k].Name, X: round(p.X, 1), Y: round(p.Y, 1),
			ModelC: round(res.Settings.AmbientC+res.hotTheta[u], 3), XYZ: d.nodes[node[k*nc+c]-1]})
	}
	bu, bv := -1, math.Inf(-1)
	for u, th := range res.hotTheta {
		if th > bv {
			bu, bv = u, th
		}
	}
	if bu >= 0 {
		kc := tm.cells[bu]
		addProbe("board max", kc/nc, kc%nc)
	}
	var refs []string
	for ref := range res.partCells {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	for _, ref := range refs {
		pw := 0.0
		for _, pt := range res.Thermal.Parts {
			if pt.Ref == ref {
				pw = pt.PowerW
			}
		}
		if pw <= 0.01 {
			continue
		}
		best, bk := math.Inf(-1), -1
		for _, kc := range res.partCells[ref] {
			if u := tm.idx[kc]; u >= 0 && res.hotTheta[u] > best {
				best, bk = res.hotTheta[u], kc
			}
		}
		if bk >= 0 {
			addProbe(ref, bk/nc, bk%nc)
		}
	}
	return d, nil
}

func (d *elmerDeck) write(dir string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%d %d %d\n2\n808 %d\n404 %d\n", len(d.nodes), len(d.elems), len(d.bounds), len(d.elems), len(d.bounds))
	if err := os.WriteFile(filepath.Join(dir, "mesh", "mesh.header"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	b.Reset()
	for i, n := range d.nodes {
		fmt.Fprintf(&b, "%d -1 %.9g %.9g %.9g\n", i+1, n[0], n[1], n[2])
	}
	if err := os.WriteFile(filepath.Join(dir, "mesh", "mesh.nodes"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	b.Reset()
	for i, e := range d.elems {
		fmt.Fprintf(&b, "%d %d 808 %d %d %d %d %d %d %d %d\n", i+1, e[0], e[1], e[2], e[3], e[4], e[5], e[6], e[7], e[8])
	}
	if err := os.WriteFile(filepath.Join(dir, "mesh", "mesh.elements"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	b.Reset()
	for i, f := range d.bounds {
		fmt.Fprintf(&b, "%d %d %d 0 404 %d %d %d %d\n", i+1, f[0], f[1], f[2], f[3], f[4], f[5])
	}
	if err := os.WriteFile(filepath.Join(dir, "mesh", "mesh.boundary"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	b.Reset()
	w := func(f string, a ...any) { fmt.Fprintf(&b, f, a...) }
	w("! pcbpilot sim post-layout — Elmer FEM cross-check of the board thermal model\n")
	w("! units: m, W, °C (steady linear heat equation; Density = 1 so Heat Source is W/m³)\n")
	w("Header\n  CHECK KEYWORDS Warn\n  Mesh DB \".\" \"mesh\"\n  Results Directory \"results\"\nEnd\n\n")
	w("Simulation\n  Max Output Level = 3\n  Coordinate System = Cartesian 3D\n  Simulation Type = Steady state\n  Steady State Max Iterations = 1\n  Output Intervals = 1\nEnd\n\n")
	w("Constants\n  Stefan Boltzmann = 5.670e-08\nEnd\n\n")
	for i, bd := range d.bodies {
		w("Body %d\n  Target Bodies(1) = %d\n  Equation = 1\n  Material = %d\n", i+1, i+1, i+1)
		if bd.q > 0 {
			w("  Body Force = %d\n", i+1)
		}
		w("End\n\n")
	}
	for i, bd := range d.bodies {
		w("Material %d\n  Density = 1.0\n  Heat Conductivity(3,3) = Real %.9g 0 0  0 %.9g 0  0 0 %.9g\nEnd\n\n", i+1, bd.kxy, bd.kxy, bd.kz)
		if bd.q > 0 {
			w("Body Force %d\n  Heat Source = %.9g\nEnd\n\n", i+1, bd.q)
		}
	}
	w("Equation 1\n  Active Solvers(1) = 1\nEnd\n\n")
	w("Solver 1\n  Equation = Heat Equation\n  Procedure = \"HeatSolve\" \"HeatSolver\"\n  Variable = Temperature\n")
	w("  Linear System Solver = Iterative\n  Linear System Iterative Method = BiCGStab\n  Linear System Max Iterations = 10000\n")
	w("  Linear System Convergence Tolerance = 1.0e-10\n  Linear System Preconditioning = ILU0\n  Nonlinear System Max Iterations = 1\n  Steady State Convergence Tolerance = 1.0e-8\nEnd\n\n")
	w("Solver 2\n  Exec Solver = After Simulation\n  Equation = SaveScalars\n  Procedure = \"SaveData\" \"SaveScalars\"\n  Filename = \"probes.dat\"\n  Variable 1 = Temperature\n")
	w("  Save Coordinates(%d,3) = ", len(d.probes))
	for i, p := range d.probes {
		if i > 0 {
			w("    ")
		}
		w("%.9g %.9g %.9g", p.XYZ[0], p.XYZ[1], p.XYZ[2])
		if i < len(d.probes)-1 {
			w(" \\\n")
		}
	}
	w("\nEnd\n\n")
	w("Boundary Condition 1\n  Name = \"top\"\n  Target Boundaries(1) = 1\n  Heat Transfer Coefficient = %.9g\n  External Temperature = %.9g\nEnd\n\n", d.hTop, d.ambientC)
	w("Boundary Condition 2\n  Name = \"bottom\"\n  Target Boundaries(1) = 2\n  Heat Transfer Coefficient = %.9g\n  External Temperature = %.9g\nEnd\n", d.hBot, d.ambientC)
	if err := os.WriteFile(filepath.Join(dir, "case.sif"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	pj, _ := json.MarshalIndent(map[string]any{"probes": d.probes, "order": "probes.dat: the last N numbers of the last row are the probe temperatures in this order"}, "", "  ")
	return os.WriteFile(filepath.Join(dir, "probes.json"), pj, 0o644)
}
