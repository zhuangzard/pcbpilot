package app

// kicad_route_stitch_path.go — a stitch whose straight track has no way out: a
// bounded grid search on the pad's own layer finds the nearest via site and a
// path to it. Every final segment is checked by the same exact test a straight
// stitch passes, and the stitch proof on the filled board still decides.

import (
	"container/heap"
	"math"
)

const (
	stitchPathStepMil = 5.0
	// stitchPathNodes caps one search: a 400 mil reach is about 20000 grid
	// points, so this only stops a search a far larger --stitch-path-mil set.
	stitchPathNodes = 200000
	// stitchPathMaxMil is the largest --stitch-path-mil accepted.
	stitchPathMaxMil = 2000.0
)

type stitchNode struct {
	x, y int
	cost float64
}

type stitchQueue []stitchNode

func (q stitchQueue) Len() int           { return len(q) }
func (q stitchQueue) Less(i, j int) bool { return q[i].cost < q[j].cost }
func (q stitchQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *stitchQueue) Push(x any)        { *q = append(*q, x.(stitchNode)) }
func (q *stitchQueue) Pop() any {
	o := *q
	n := o[len(o)-1]
	*q = o[:len(o)-1]
	return n
}

// stitchPathSearch returns the corners of the shortest grid path from from to a
// point goal accepts, no longer than maxLen, nil if there is none. segOK judges
// one straight piece of track.
func stitchPathSearch(from [2]float64, maxLen float64, segOK func(a, b [2]float64) bool, goal func([2]float64) bool) [][2]float64 {
	type key struct{ x, y int }
	at := func(k key) [2]float64 {
		return [2]float64{math.Round((from[0]+float64(k.x)*stitchPathStepMil)*100) / 100, math.Round((from[1]+float64(k.y)*stitchPathStepMil)*100) / 100}
	}
	best := map[key]float64{{}: 0}
	came := map[key]key{}
	q := &stitchQueue{{}}
	for expanded := 0; q.Len() > 0 && expanded < stitchPathNodes; expanded++ {
		n := heap.Pop(q).(stitchNode)
		k := key{n.x, n.y}
		if n.cost > best[k] {
			continue
		}
		if n.cost > 0 && goal(at(k)) {
			var pts [][2]float64
			for c := k; ; c = came[c] {
				pts = append([][2]float64{at(c)}, pts...)
				if c == (key{}) {
					break
				}
			}
			pts[0] = from
			return stitchPathShorten(pts, segOK)
		}
		for dx := -1; dx <= 1; dx++ {
			for dy := -1; dy <= 1; dy++ {
				if dx == 0 && dy == 0 {
					continue
				}
				m := key{k.x + dx, k.y + dy}
				c := n.cost + math.Hypot(float64(dx), float64(dy))*stitchPathStepMil
				if c > maxLen {
					continue
				}
				if b, seen := best[m]; seen && b <= c {
					continue
				}
				if !segOK(at(k), at(m)) {
					continue
				}
				best[m], came[m] = c, k
				heap.Push(q, stitchNode{m.x, m.y, c})
			}
		}
	}
	return nil
}

// stitchPathShorten replaces runs of grid steps by the longest straight pieces
// that still pass segOK.
func stitchPathShorten(pts [][2]float64, segOK func(a, b [2]float64) bool) [][2]float64 {
	out := [][2]float64{pts[0]}
	for i := 0; i < len(pts)-1; {
		j := len(pts) - 1
		for j > i+1 && !segOK(pts[i], pts[j]) {
			j--
		}
		out = append(out, pts[j])
		i = j
	}
	return out
}
