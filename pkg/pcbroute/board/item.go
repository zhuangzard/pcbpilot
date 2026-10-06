package board

import (
	"fmt"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
)

// onLayer reports whether it exists on layer l; AllLayers on either side
// matches every layer.
func onLayer(it Item, l geom.LayerID) bool {
	return l == geom.AllLayers || it.From == geom.AllLayers || it.From <= l && l <= it.To
}

// shareLayer reports whether a and b have a copper layer in common.
func shareLayer(a, b Item) bool {
	if a.From == geom.AllLayers || b.From == geom.AllLayers {
		return true
	}
	return a.From <= b.To && b.From <= a.To
}

// conducts reports whether it is copper that can join a net: keep-outs and
// the board edge never do, nor does anything without a net.
func conducts(it Item) bool {
	return it.Net != 0 && it.Kind != Keepout && it.Kind != Edge
}

// touches reports whether two same-net copper items are joined: they share a
// layer and their shapes overlap or meet. "Meet" is geom.Within(…, 1), i.e.
// closer than 1 nm, which on the integer lattice is contact.
func touches(a, b Item) bool {
	return a.Net == b.Net && conducts(a) && conducts(b) && shareLayer(a, b) && geom.Within(a.Shape, b.Shape, 1)
}

// layers returns the layers whose index entries hold it: AllLayers once,
// otherwise every layer of its span.
func layers(it Item) []geom.LayerID {
	if it.From == geom.AllLayers {
		return []geom.LayerID{geom.AllLayers}
	}
	out := make([]geom.LayerID, 0, it.To-it.From+1)
	for l := it.From; l <= it.To; l++ {
		out = append(out, l)
	}
	return out
}

// center is the centre of it's bounds (rounded down), the reference point of
// a terminal.
func center(it Item) geom.Pt {
	b := it.Shape.Bounds()
	return geom.Pt{X: (b.MinX + b.MaxX - 1) >> 1, Y: (b.MinY + b.MaxY - 1) >> 1}
}

// checkItem validates an item against the board's nets and layers
// (nlayers 0: unknown, no upper bound).
func checkItem(it Item, nnets, nlayers int) error {
	if it.Shape == nil {
		return fmt.Errorf("board: item %d has no shape", it.ID)
	}
	if err := geom.Check(it.Shape); err != nil {
		return fmt.Errorf("board: item %d: %w", it.ID, err)
	}
	if it.Net < 0 || int(it.Net) > nnets {
		return fmt.Errorf("board: item %d: net %d not on the board", it.ID, it.Net)
	}
	switch {
	case it.From == geom.AllLayers && it.To == geom.AllLayers:
	case it.From < 0 || it.To < it.From:
		return fmt.Errorf("board: item %d: bad layer span %d..%d", it.ID, it.From, it.To)
	case nlayers > 0 && int(it.To) >= nlayers:
		return fmt.Errorf("board: item %d: layer %d beyond %d layers", it.ID, it.To, nlayers)
	}
	return nil
}
