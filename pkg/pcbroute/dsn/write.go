package dsn

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// WriteDSN writes a placed pcbauto board as a Specctra design: the input
// both routers get on the bench (gap §4.2 (A)). It writes mil at resolution
// 1000 and EasyEDA's layer names and line layout (TopLayer, Inner1 …,
// BottomLayer; one (shape …) per line), so specctra.FixDSN and
// ApplyNetRequirements apply to it unchanged.
//
// Every part is its own image placed at the origin, front, rotation 0, with
// its pins at their absolute positions and each pad's rotation baked into the
// padstack shape, so no transform is lost. A pin number repeated within a
// part gets a suffix "_2", "_3", … to keep pin references unique. Only board
// rules are written (width, clearance, one via); net classes are FixDSN's and
// ApplyNetRequirements' job. Keep-outs that forbid copper (or only vias) and
// holes (drill plus the copper distance the hole requires beyond the board
// clearance) become keep-out areas.
func WriteDSN(w io.Writer, b *pcbauto.Board) error {
	if len(b.Outline) < 3 {
		return fmt.Errorf("dsn: board has no outline")
	}
	n := b.CopperLayers
	if n < 2 {
		n = 2
	}
	names := make([]string, n)
	names[0], names[n-1] = "TopLayer", "BottomLayer"
	for k := 1; k < n-1; k++ {
		names[k] = fmt.Sprintf("Inner%d", k)
	}
	padLayers := func(id int) ([]string, error) {
		switch {
		case id == pcbauto.LayerMulti:
			return names, nil
		case id == pcbauto.LayerTop:
			return names[:1], nil
		case id == pcbauto.LayerBottom:
			return names[n-1:], nil
		case id >= pcbauto.LayerInner1 && id-pcbauto.LayerInner1+1 < n-1:
			return names[id-pcbauto.LayerInner1+1 : id-pcbauto.LayerInner1+2], nil
		}
		return nil, fmt.Errorf("dsn: pad layer %d not on a %d-layer board", id, n)
	}

	// Padstacks are shared by equal shape text, named in first-use order.
	psName := map[string]string{}
	var psOrder []string
	type pin struct {
		ps, id string
		x, y   float64
	}
	images := make([][]pin, len(b.Parts))
	netPins := map[string][]string{}
	for i, p := range b.Parts {
		seen := map[string]int{}
		for _, pd := range p.Pads {
			ls, err := padLayers(pd.Layer)
			if err != nil {
				return fmt.Errorf("%w (pad %s)", err, pd.Key())
			}
			var sb strings.Builder
			for _, l := range ls {
				fmt.Fprintf(&sb, "      (shape %s)\n", padShape(pd.Box, l))
			}
			key := sb.String()
			name, ok := psName[key]
			if !ok {
				name = fmt.Sprintf("ps%d", len(psOrder)+1)
				psName[key] = name
				psOrder = append(psOrder, key)
			}
			id := pd.Number
			if seen[id]++; seen[id] > 1 {
				id = fmt.Sprintf("%s_%d", id, seen[pd.Number])
			}
			images[i] = append(images[i], pin{name, id, pd.Box.C.X, pd.Box.C.Y})
			if pd.Net != "" {
				netPins[pd.Net] = append(netPins[pd.Net], p.Ref+"-"+id)
			}
		}
	}

	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "(PCB \"pcbroute\"\n  (parser\n    (host_cad \"pcbpilot\")\n    (host_version \"engine v2\")\n  )\n")
	fmt.Fprintf(bw, "  (resolution mil 1000)\n  (structure\n")
	fmt.Fprintf(bw, "    (boundary (path signal 0%s))\n", coords(b.Outline))
	via := "via_" + fnum(b.Rules.ViaDia)
	fmt.Fprintf(bw, "    (via %s)\n", via)
	fmt.Fprintf(bw, "    (rule (width %s) (clearance %s))\n", fnum(b.Rules.TrackWidth), fnum(b.Rules.Clearance))
	for _, l := range names {
		fmt.Fprintf(bw, "    (layer %s\n      (type signal)\n    )\n", l)
	}
	for i, k := range b.Keepouts {
		kind := "keepout"
		switch {
		case k.NoCopper:
		case k.NoVias:
			kind = "via_keepout"
		default:
			continue // a placement-only keep-out does not concern routing
		}
		ls := []string{"signal"}
		if len(k.Layers) > 0 {
			ls = nil
			for _, id := range k.Layers {
				l, err := padLayers(id)
				if err != nil {
					return fmt.Errorf("%w (keep-out %d)", err, i)
				}
				ls = append(ls, l...)
			}
		}
		for _, l := range ls {
			fmt.Fprintf(bw, "    (%s \"ko%d\" (polygon %s 0%s))\n", kind, i+1, l, coords(k.Poly))
		}
	}
	for i, h := range b.Holes {
		extra := h.Required(b.Rules) - b.Rules.Clearance
		if len(h.Poly) >= 3 {
			fmt.Fprintf(bw, "    (keepout \"hole%d\" (polygon signal 0%s))\n", i+1, coords(h.Poly))
			continue
		}
		fmt.Fprintf(bw, "    (keepout \"hole%d\" (circle signal %s %s %s))\n", i+1,
			fnum(h.Dia+2*math.Max(extra, 0)), fnum(h.C.X), fnum(h.C.Y))
	}
	fmt.Fprintf(bw, "  )\n  (placement\n")
	for _, p := range b.Parts {
		r := atom(p.Ref)
		fmt.Fprintf(bw, "    (component %s\n      (place %s 0 0 front 0)\n    )\n", r, r)
	}
	fmt.Fprintf(bw, "  )\n  (library\n")
	for i, p := range b.Parts {
		fmt.Fprintf(bw, "    (image %s\n", atom(p.Ref))
		for _, pn := range images[i] {
			fmt.Fprintf(bw, "      (pin %s %s %s %s)\n", pn.ps, atom(pn.id), fnum(pn.x), fnum(pn.y))
		}
		fmt.Fprintf(bw, "    )\n")
	}
	for i, key := range psOrder {
		fmt.Fprintf(bw, "    (padstack ps%d\n%s      (attach off)\n    )\n", i+1, key)
	}
	fmt.Fprintf(bw, "    (padstack %s\n", via)
	for _, l := range names {
		fmt.Fprintf(bw, "      (shape (circle %s %s))\n", l, fnum(b.Rules.ViaDia))
	}
	fmt.Fprintf(bw, "      (attach off)\n    )\n  )\n  (network\n")
	for _, nt := range b.Nets() {
		fmt.Fprintf(bw, "    (net %s\n      (pins", atom(nt.Name))
		for _, ref := range netPins[nt.Name] {
			fmt.Fprintf(bw, " %s", atom(ref))
		}
		fmt.Fprintf(bw, ")\n    )\n")
	}
	fmt.Fprintf(bw, "  )\n  (wiring\n  )\n)\n")
	return bw.Flush()
}

// padShape is a pad's copper on one layer, relative to the pin position, with
// the pad rotation applied.
func padShape(bx pcbauto.OrientedBox, layer string) string {
	t := newXform(0, 0, bx.Rot, false)
	hw, hh := bx.W/2, bx.H/2
	if bx.Round {
		if bx.W == bx.H {
			return fmt.Sprintf("(circle %s %s)", layer, fnum(bx.W))
		}
		// Stadium: a path of the short side's width along the long axis.
		r := math.Min(hw, hh)
		ax, ay := t.apply(hw-r, hh-r)
		bxx, by := t.apply(-(hw - r), -(hh - r))
		return fmt.Sprintf("(path %s %s %s %s %s %s)", layer, fnum(2*r), fnum(bxx), fnum(by), fnum(ax), fnum(ay))
	}
	if q := bx.Rot / 90; q == math.Trunc(q) {
		if int(q)%2 != 0 {
			hw, hh = hh, hw
		}
		return fmt.Sprintf("(rect %s %s %s %s %s)", layer, fnum(-hw), fnum(-hh), fnum(hw), fnum(hh))
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "(polygon %s 0", layer)
	for _, c := range [][2]float64{{-hw, -hh}, {hw, -hh}, {hw, hh}, {-hw, hh}, {-hw, -hh}} {
		x, y := t.apply(c[0], c[1])
		fmt.Fprintf(&sb, " %s %s", fnum(x), fnum(y))
	}
	sb.WriteString(")")
	return sb.String()
}

// coords formats a polygon, closed, with a leading space.
func coords(p []pcbauto.Point) string {
	var sb strings.Builder
	for i := 0; i <= len(p); i++ {
		q := p[i%len(p)]
		fmt.Fprintf(&sb, " %s %s", fnum(q.X), fnum(q.Y))
	}
	return sb.String()
}

// fnum formats mil at the 0.001 mil resolution, without trailing zeros.
func fnum(v float64) string {
	v = math.Round(v*1000) / 1000
	if v == 0 {
		v = 0 // no "-0"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// atom double-quotes a name that is not a plain token. The shared reader has
// no escapes, so a name must not contain '"'.
func atom(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\r\n()") {
		return s
	}
	return `"` + s + `"`
}
