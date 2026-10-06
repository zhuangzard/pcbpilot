package tile

// Corner stitches (Ousterhout 1984). Every tile points to four neighbours;
// nil means the plane boundary:
//
//	rt  right-top:   the topmost tile on the right edge (holds y = MaxY-1)
//	tr  top-right:   the rightmost tile on the top edge (holds x = MaxX-1)
//	lb  left-bottom: the bottommost tile on the left edge (holds y = MinY)
//	bl  bottom-left: the leftmost tile on the bottom edge (holds x = MinX)
//
// Walking an edge uses the neighbours' own stitches: down the right edge by
// bl, up the left edge by tr, left along the top by lb, right along the
// bottom by rt.
const (
	rt = iota
	tr
	lb
	bl
)

// Side names one edge of a tile.
type Side uint8

const (
	Right Side = iota
	Top
	Left
	Bottom
)

// Neighbors calls fn with every tile that shares a stretch of side s with t,
// until fn returns false; it returns false when fn stopped. Right neighbours
// come top to bottom, top ones right to left, left ones bottom to top and
// bottom ones left to right. Tiles that touch t only at a corner are not
// neighbours.
func (t *Tile) Neighbors(s Side, fn func(*Tile) bool) bool {
	switch s {
	case Right:
		for n := t.stitches[rt]; n != nil && n.Rect.MaxY > t.Rect.MinY; n = n.stitches[bl] {
			if !fn(n) {
				return false
			}
		}
	case Top:
		for n := t.stitches[tr]; n != nil && n.Rect.MaxX > t.Rect.MinX; n = n.stitches[lb] {
			if !fn(n) {
				return false
			}
		}
	case Left:
		for n := t.stitches[lb]; n != nil && n.Rect.MinY < t.Rect.MaxY; n = n.stitches[tr] {
			if !fn(n) {
				return false
			}
		}
	case Bottom:
		for n := t.stitches[bl]; n != nil && n.Rect.MinX < t.Rect.MaxX; n = n.stitches[rt] {
			if !fn(n) {
				return false
			}
		}
	}
	return true
}

// splitY cuts t at y (MinY < y < MaxY). t keeps the lower part; the upper
// part is returned. Neighbours that now touch the upper part are restitched.
func (p *Plane) splitY(t *Tile, y int64) *Tile {
	u := &Tile{Kind: t.Kind, Owners: t.Owners}
	u.Rect = t.Rect
	u.Rect.MinY = y
	u.stitches[tr], u.stitches[rt], u.stitches[bl] = t.stitches[tr], t.stitches[rt], t
	l := t.stitches[lb]
	for l != nil && l.Rect.MaxY <= y {
		l = l.stitches[tr]
	}
	u.stitches[lb] = l
	for n := u.stitches[tr]; n != nil && n.Rect.MaxX > t.Rect.MinX; n = n.stitches[lb] {
		if n.stitches[bl] == t {
			n.stitches[bl] = u
		}
	}
	r := t.stitches[rt]
	for ; r != nil && r.Rect.MinY >= y; r = r.stitches[bl] {
		if r.stitches[lb] == t {
			r.stitches[lb] = u
		}
	}
	for n := l; n != nil && n.Rect.MinY < t.Rect.MaxY; n = n.stitches[tr] {
		if n.stitches[rt] == t {
			n.stitches[rt] = u
		}
	}
	t.stitches[tr], t.stitches[rt] = u, r
	t.Rect.MaxY = y
	p.n++
	return u
}

// splitX cuts t at x (MinX < x < MaxX). t keeps the left part; the right
// part is returned.
func (p *Plane) splitX(t *Tile, x int64) *Tile {
	r := &Tile{Kind: t.Kind, Owners: t.Owners}
	r.Rect = t.Rect
	r.Rect.MinX = x
	r.stitches[rt], r.stitches[tr], r.stitches[lb] = t.stitches[rt], t.stitches[tr], t
	b := t.stitches[bl]
	for b != nil && b.Rect.MaxX <= x {
		b = b.stitches[rt]
	}
	r.stitches[bl] = b
	a := t.stitches[tr]
	for ; a != nil && a.Rect.MinX >= x; a = a.stitches[lb] {
		if a.stitches[bl] == t {
			a.stitches[bl] = r
		}
	}
	for n := r.stitches[rt]; n != nil && n.Rect.MaxY > t.Rect.MinY; n = n.stitches[bl] {
		if n.stitches[lb] == t {
			n.stitches[lb] = r
		}
	}
	for n := b; n != nil && n.Rect.MinX < t.Rect.MaxX; n = n.stitches[rt] {
		if n.stitches[tr] == t {
			n.stitches[tr] = r
		}
	}
	t.stitches[tr], t.stitches[rt] = a, r
	t.Rect.MaxX = x
	p.n++
	return r
}

// joinY merges u into t; u lies directly on top of t with the same x span.
func (p *Plane) joinY(t, u *Tile) {
	for n := u.stitches[tr]; n != nil && n.Rect.MaxX > u.Rect.MinX; n = n.stitches[lb] {
		if n.stitches[bl] == u {
			n.stitches[bl] = t
		}
	}
	for n := u.stitches[rt]; n != nil && n.Rect.MaxY > u.Rect.MinY; n = n.stitches[bl] {
		if n.stitches[lb] == u {
			n.stitches[lb] = t
		}
	}
	for n := u.stitches[lb]; n != nil && n.Rect.MinY < u.Rect.MaxY; n = n.stitches[tr] {
		if n.stitches[rt] == u {
			n.stitches[rt] = t
		}
	}
	t.stitches[tr], t.stitches[rt] = u.stitches[tr], u.stitches[rt]
	t.Rect.MaxY = u.Rect.MaxY
	p.retire(u, t)
}

// joinX merges r into t; r lies directly right of t with the same y span.
func (p *Plane) joinX(t, r *Tile) {
	for n := r.stitches[tr]; n != nil && n.Rect.MaxX > r.Rect.MinX; n = n.stitches[lb] {
		if n.stitches[bl] == r {
			n.stitches[bl] = t
		}
	}
	for n := r.stitches[bl]; n != nil && n.Rect.MinX < r.Rect.MaxX; n = n.stitches[rt] {
		if n.stitches[tr] == r {
			n.stitches[tr] = t
		}
	}
	for n := r.stitches[rt]; n != nil && n.Rect.MaxY > r.Rect.MinY; n = n.stitches[bl] {
		if n.stitches[lb] == r {
			n.stitches[lb] = t
		}
	}
	t.stitches[rt], t.stitches[tr] = r.stitches[rt], r.stitches[tr]
	t.Rect.MaxX = r.Rect.MaxX
	p.retire(r, t)
}

// retire drops a tile absorbed by a join. Its stitches are cleared so a stale
// pointer held by a caller is recognisable (dead reports it).
func (p *Plane) retire(gone, into *Tile) {
	gone.stitches = [4]*Tile{}
	gone.Rect = emptyRect
	if p.hint == gone {
		p.hint = into
	}
	p.n--
}
