package app

import "github.com/zhuangzard/pcbpilot/internal/schguard"

// Geometric meeting and physical contact are intentionally separate. The
// official editor preserves a bare proper X as separate physical wire islands.
func plSegmentsContact(a, b, c, d [2]float64) bool {
	return schguard.SegmentsContact(schguard.Point{X: a[0], Y: a[1]}, schguard.Point{X: b[0], Y: b[1]}, schguard.Point{X: c[0], Y: c[1]}, schguard.Point{X: d[0], Y: d[1]})
}

func plNormalizeWirePoints(points [][2]float64) [][2]float64 {
	p := make([]schguard.Point, len(points))
	for i, q := range points {
		p[i] = schguard.Point{X: q[0], Y: q[1]}
	}
	p = schguard.NormalizePolylinePoints(p)
	out := make([][2]float64, len(p))
	for i, q := range p {
		out[i] = [2]float64{q.X, q.Y}
	}
	return out
}
