package geom

// Orient is the sign of the cross product (b-a)×(c-a): +1 when c is left of
// the directed line AB, -1 when right, 0 when collinear. Exact for points
// within ±MaxCoord.
func Orient(a, b, c Pt) int {
	return int(sign((b.X-a.X)*(c.Y-a.Y) - (b.Y-a.Y)*(c.X-a.X)))
}

// onBox reports whether p lies in the closed bounding box of AB; for a p
// collinear with AB this means p is on the segment.
func onBox(a, b, p Pt) bool {
	return min(a.X, b.X) <= p.X && p.X <= max(a.X, b.X) && min(a.Y, b.Y) <= p.Y && p.Y <= max(a.Y, b.Y)
}

// SegsIntersect reports whether the closed segments AB and CD share a point.
func SegsIntersect(a, b, c, d Pt) bool {
	o1, o2 := Orient(c, d, a), Orient(c, d, b)
	o3, o4 := Orient(a, b, c), Orient(a, b, d)
	if o1*o2 < 0 && o3*o4 < 0 {
		return true
	}
	return o1 == 0 && onBox(c, d, a) || o2 == 0 && onBox(c, d, b) ||
		o3 == 0 && onBox(a, b, c) || o4 == 0 && onBox(a, b, d)
}

// InPoly reports whether p lies in the closed region of the simple polygon
// pts (boundary included), by the crossing-number rule with exact
// orientation tests.
func InPoly(p Pt, pts []Pt) bool {
	in := false
	for i, j := 0, len(pts)-1; i < len(pts); j, i = i, i+1 {
		a, b := pts[j], pts[i]
		o := Orient(a, b, p)
		if o == 0 && onBox(a, b, p) {
			return true
		}
		// Edge straddles the horizontal through p; count it when the
		// crossing lies right of p.
		if (a.Y > p.Y) != (b.Y > p.Y) && (o > 0) == (b.Y > a.Y) {
			in = !in
		}
	}
	return in
}
