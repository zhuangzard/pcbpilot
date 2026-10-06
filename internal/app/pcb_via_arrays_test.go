package app

import (
	"math"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

var openBoard = layoutBBox{MinX: -2000, MinY: -2000, MaxX: 2000, MaxY: 2000}

// +12V changes from TOP (going +x) to Inner1 (going -x) through one 12/24
// via; the intent wants 3 per transition.
func plus12Transition() ([]specctra.Track, []widenVia) {
	return []specctra.Track{
		{ID: "top", Net: "+12V", Layer: 1, X1: 0, Y1: 0, X2: 500, Y2: 0, Width: 21.65},
		{ID: "in1", Net: "+12V", Layer: 15, X1: 0, Y1: 0, X2: -500, Y2: 0, Width: 41.34},
	}, []widenVia{{Net: "+12V", X: 0, Y: 0, Diameter: 24}}
}

var plus12Need = map[string]viaNeed{"+12V": {Count: 3, DiaMil: 24, DrillMil: 12, OuterMil: 21.65, InnerMil: 41.34}}

func TestPlanViaArrays(t *testing.T) {
	tracks, vias := plus12Transition()
	p := planViaArrays(tracks, vias, nil, plus12Need, 6, openBoard)
	if len(p.Vias) != 2 || len(p.Shortfall) != 0 {
		t.Fatalf("plan = %+v", p)
	}
	// The first candidate (pitch 30 mil east) sits on the TOP track, so it
	// needs only an Inner1 stub at the inner width.
	if p.Vias[0].X != 30 || p.Vias[0].Y != 0 {
		t.Fatalf("first via at %+v", p.Vias[0])
	}
	var in1 int
	for _, s := range p.Stubs {
		if s.Layer == 15 && s.Width == 41.34 {
			in1++
		}
	}
	if in1 == 0 {
		t.Fatalf("no Inner1 stub at 41.34 mil: %+v", p.Stubs)
	}
}

func TestPlanViaArraysClearance(t *testing.T) {
	tracks, vias := plus12Transition()
	// Other-net copper all around: GND tracks 20 mil above and below on
	// every layer leave no room for a second via.
	for _, l := range []int{1, 15} {
		tracks = append(tracks,
			specctra.Track{Net: "GND", Layer: l, X1: -500, Y1: 20, X2: 500, Y2: 20, Width: 10},
			specctra.Track{Net: "GND", Layer: l, X1: -500, Y1: -20, X2: 500, Y2: -20, Width: 10})
	}
	p := planViaArrays(tracks, vias, nil, plus12Need, 6, openBoard)
	if len(p.Vias) != 0 || len(p.Shortfall) != 1 || !strings.Contains(p.Shortfall[0], "1 of 3") {
		t.Fatalf("plan = %+v", p)
	}
	// Enough vias already: nothing to add.
	vias = append(vias, widenVia{Net: "+12V", X: 30, Y: 0, Diameter: 24}, widenVia{Net: "+12V", X: -30, Y: 0, Diameter: 24})
	if p := planViaArrays(tracks, vias, nil, plus12Need, 6, openBoard); len(p.Vias) != 0 || len(p.Shortfall) != 0 {
		t.Fatalf("complete transition changed: %+v", p)
	}
}

func TestIntentViaNeeds(t *testing.T) {
	in, err := parseDesignIntent([]byte(`{"schemaVersion":1,"nets":{
 "+12V":{"role":"power","widthMil":{"outer":21.65,"inner":41.34,"min":21.65},"viasPerTransition":3,"via":{"drillMil":12,"diaMil":24,"countPerTransition":3}},
 "SIG":{"role":"signal","widthMil":{"outer":6,"min":6},"viasPerTransition":1}}}`))
	if err != nil {
		t.Fatal(err)
	}
	n := intentViaNeeds(in)
	if len(n) != 1 || n["+12V"].Count != 3 || n["+12V"].DiaMil != 24 || n["+12V"].InnerMil != 41.34 {
		t.Fatalf("needs = %+v", n)
	}
}

// Every array via lies within the via-current check's grouping link of the
// transition (else the check rates it alone — Gas Module v14).
func TestPlanViaArraysStayLinked(t *testing.T) {
	tracks, vias := plus12Transition()
	// Block the near rings with other-net copper on the left and right so
	// only far sites remain; none of them may be used unless linked.
	p := planViaArrays(tracks, vias, nil, plus12Need, 6, openBoard)
	pts := [][2]float64{{0, 0}}
	for _, v := range p.Vias {
		ok := false
		for _, q := range pts {
			if math.Hypot(v.X-q[0], v.Y-q[1]) <= 60 {
				ok = true
			}
		}
		if !ok {
			t.Fatalf("via %+v not linked to its transition", v)
		}
		pts = append(pts, [2]float64{v.X, v.Y})
	}
}
