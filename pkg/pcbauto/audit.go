package pcbauto

// audit recomputes every net's claims from its current paths and fan-out and
// counts cells claimed by two different nets. It is independent of the
// incremental use[] bookkeeping and is used by tests to localise bugs.
func (r *router) audit() (overlap int, mismatch int) {
	gr := r.gr
	owner := make([]int32, len(gr.use))
	for i := range owner {
		owner[i] = -1
	}
	count := make([]uint16, len(gr.use))
	for _, n := range r.nets {
		var cl []int32
		for _, p := range n.paths {
			cl = r.claimNodes(n, p.nodes, cl)
		}
		cl = append(cl, n.fixed...)
		cl = dedup(cl)
		for _, i := range cl {
			count[i]++
			if owner[i] >= 0 && owner[i] != n.id {
				overlap++
			}
			owner[i] = n.id
		}
	}
	for i := range count {
		if count[i] != gr.use[i] {
			mismatch++
		}
	}
	return
}

// minusFixed drops cells already claimed by n's fixed (fan-out) copper so a
// cell is counted once per net in use[].
func (r *router) minusFixed(n *rnet, list []int32) []int32 {
	if len(n.fixed) == 0 {
		return list
	}
	r.claimCur++
	for _, i := range n.fixed {
		r.claimStamp[i] = r.claimCur
	}
	out := list[:0]
	for _, i := range list {
		if r.claimStamp[i] != r.claimCur {
			out = append(out, i)
		}
	}
	return out
}

// repairPartial drops only the paths of n that overlap other nets, keeps the
// rest of the net, and (when reroute) reconnects the resulting islands with
// overlap forbidden. Without reroute the islands are reported unrouted.
func (r *router) repairPartial(n *rnet, reroute bool) {
	bad := map[int32]bool{}
	for _, i := range n.claims {
		if r.gr.use[i] > 1 {
			bad[i] = true
		}
	}
	var keep []rpath
	for _, p := range n.paths {
		hit := false
		for _, i := range r.claimNodes(n, p.nodes, nil) {
			if bad[i] {
				hit = true
				break
			}
		}
		if !hit {
			keep = append(keep, p)
		}
	}
	r.applyClaims(n.claims, -1)
	n.claims, n.paths = nil, keep
	groups := r.pathGroups(n)
	if reroute && len(groups) > 1 {
		saved := n.groups
		n.groups = groups
		r.routeNetKeep(n, true)
		n.groups = saved
		return
	}
	for _, p := range n.paths {
		n.claims = r.claimNodes(n, p.nodes, n.claims)
	}
	n.claims = r.minusFixed(n, dedup(n.claims))
	r.applyClaims(n.claims, +1)
	n.failed = nil
	for _, g := range groups[min(1, len(groups)):] {
		n.failed = append(n.failed, Unrouted{Net: n.name, Pads: padKeys(g), Reason: "unresolved-congestion"})
	}
}

// pathGroups partitions n's pads into the islands its current paths connect.
func (r *router) pathGroups(n *rnet) [][]*Pad {
	parent := map[int32]int32{}
	var find func(int32) int32
	find = func(i int32) int32 {
		if _, ok := parent[i]; !ok {
			parent[i] = i
		}
		for parent[i] != i {
			parent[i] = parent[parent[i]]
			i = parent[i]
		}
		return i
	}
	union := func(a, b int32) {
		if ra, rb := find(a), find(b); ra != rb {
			parent[ra] = rb
		}
	}
	for _, p := range n.paths {
		for k := 1; k < len(p.nodes); k++ {
			union(p.nodes[k-1], p.nodes[k])
		}
	}
	// Pads join through any of their access nodes the paths touch; a pad's
	// own access nodes are one piece of copper.
	var pads []*Pad
	for _, g := range n.groups {
		pads = append(pads, g...)
	}
	padRoot := make([]int32, len(pads))
	for k, pd := range pads {
		acc := r.access(n, pd)
		root := int32(-1 - k)
		for _, a := range acc {
			if _, touched := parent[a]; touched {
				if root < 0 {
					root = find(a)
				} else {
					union(a, root)
					root = find(a)
				}
			}
		}
		padRoot[k] = root
	}
	byRoot := map[int32][]*Pad{}
	var order []int32
	for k, pd := range pads {
		rt := padRoot[k]
		if rt >= 0 {
			rt = find(rt)
		}
		if _, ok := byRoot[rt]; !ok {
			order = append(order, rt)
		}
		byRoot[rt] = append(byRoot[rt], pd)
	}
	out := make([][]*Pad, 0, len(order))
	for _, rt := range order {
		out = append(out, byRoot[rt])
	}
	return out
}
