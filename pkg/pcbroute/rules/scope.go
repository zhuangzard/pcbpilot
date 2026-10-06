package rules

import (
	"math"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// scopeKey is a Scope without its region shape, with the fields its kind
// does not use cleared, so equal scopes share one RuleSet.
type scopeKey struct {
	kind          ScopeKind
	layer         geom.LayerID
	class, class2 ClassID
	net           geom.NetID
}

func keyOf(s Scope) scopeKey {
	k := scopeKey{kind: s.Kind}
	switch s.Kind {
	case ScopeLayer:
		k.layer = s.Layer
	case ScopeClass:
		k.class = s.Class
	case ScopeClassLayer:
		k.class, k.layer = s.Class, s.Layer
	case ScopeNet:
		k.net = s.Net
	case ScopeNetLayer, ScopeIntent:
		k.net, k.layer = s.Net, s.Layer
	case ScopeClassClass, ScopeClassClassLayer:
		k.class, k.class2 = min(s.Class, s.Class2), max(s.Class, s.Class2)
		if s.Kind == ScopeClassClassLayer {
			k.layer = s.Layer
		}
	}
	return k
}

func (b *Book) scope(k scopeKey) *RuleSet { return b.scopes[k] }

// region is a ScopeRegion rule. A non-zero class or net narrows it to the
// objects of that class or net (ClassID 0, the default class, cannot narrow).
type region struct {
	shape geom.Shape
	class ClassID
	net   geom.NetID
	rs    RuleSet
}

// applies reports whether the region covers object o of class c.
func (r *region) applies(o Obj, c ClassID) bool {
	if r.net != 0 && o.Net != r.net || r.class != 0 && c != r.class {
		return false
	}
	return contains(r.shape, o.At)
}

// contains is a closed point-in-shape test for region rules. Exactness on the
// boundary does not matter here: a region only selects which rule applies.
func contains(s geom.Shape, p geom.Pt) bool {
	switch s := s.(type) {
	case geom.Rect:
		return p.X >= s.MinX && p.X < s.MaxX && p.Y >= s.MinY && p.Y < s.MaxY
	case geom.Circle:
		dx, dy := float64(p.X-s.C.X), float64(p.Y-s.C.Y)
		return dx*dx+dy*dy <= float64(s.R)*float64(s.R)
	case geom.Seg:
		return segDist(p, s.A, s.B) <= float64(s.HalfW)
	case geom.Poly:
		return inPoly(s.Pts, p)
	}
	return false
}

func segDist(p, a, b geom.Pt) float64 {
	ax, ay := float64(a.X), float64(a.Y)
	dx, dy := float64(b.X)-ax, float64(b.Y)-ay
	px, py := float64(p.X)-ax, float64(p.Y)-ay
	t := 0.0
	if l := dx*dx + dy*dy; l > 0 {
		t = max(0, min(1, (px*dx+py*dy)/l))
	}
	ex, ey := px-t*dx, py-t*dy
	return math.Sqrt(ex*ex + ey*ey)
}

// inPoly is the even-odd rule, with points on an edge counted as inside.
func inPoly(pts []geom.Pt, p geom.Pt) bool {
	in := false
	for i, n := 0, len(pts); i < n; i++ {
		a, b := pts[i], pts[(i+1)%n]
		if onSeg(a, b, p) {
			return true
		}
		if (a.Y > p.Y) != (b.Y > p.Y) {
			x := float64(a.X) + float64(p.Y-a.Y)*float64(b.X-a.X)/float64(b.Y-a.Y)
			if float64(p.X) < x {
				in = !in
			}
		}
	}
	return in
}

func onSeg(a, b, p geom.Pt) bool {
	cross := float64(b.X-a.X)*float64(p.Y-a.Y) - float64(b.Y-a.Y)*float64(p.X-a.X)
	return cross == 0 && p.X >= min(a.X, b.X) && p.X <= max(a.X, b.X) && p.Y >= min(a.Y, b.Y) && p.Y <= max(a.Y, b.Y)
}
