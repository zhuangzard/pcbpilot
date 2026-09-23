package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func netPathRectPad(id, designator, number, net string, layer int, x, y, w, h float64) pcbPadP {
	return pcbPadP{
		ID: id, Designator: designator, Number: number, Net: net, Layer: layer,
		X: x, Y: y, W: w, H: h, Shape: "RECT", ShapeW: w, ShapeH: h, ShapeOK: true,
	}
}

func TestAnalyzePcbNetPath_OrderedWaypointLayerWidthAndViaEvidence(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-c3", "C3", "1", "+5V", 1, 0, 0, 40, 20),
		netPathRectPad("p-c4", "C4", "1", "+5V", 1, 100, 0, 40, 20),
		netPathRectPad("p-u2", "U2", "3", "+5V", 2, 200, 100, 30, 30),
	}
	tracks := []pcbTrack{
		// t1 starts at x=18, inside C3's real 40x20 copper area rather than at
		// its center. This is the regression guard against center-only anchoring.
		{ID: "t1", Net: "+5V", Layer: 1, X1: 18, Y1: 0, X2: 100, Y2: 0, Width: 20},
		{ID: "t2", Net: "+5V", Layer: 1, X1: 100, Y1: 0, X2: 140, Y2: 40, Width: 20},
		{ID: "t3", Net: "+5V", Layer: 2, X1: 140, Y1: 40, X2: 200, Y2: 100, Width: 8},
	}
	vias := []pcbViaP{{ID: "v1", Net: "+5V", X: 140, Y: 40, Dia: 24, Hole: 12}}

	rep, err := analyzePcbNetPath(pads, tracks, nil, vias, pcbNetPathOptions{From: "C3.1", Through: []string{"C4.1"}, To: "U2.3", Net: "+5V"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Connected || len(rep.Legs) != 2 || !rep.Legs[0].Connected || !rep.Legs[1].Connected {
		t.Fatalf("ordered path not proven: %+v", rep)
	}
	if got := strings.Join(rep.WaypointOrder, ">"); got != "C3.1>C4.1>U2.3" {
		t.Fatalf("waypoint order=%s", got)
	}
	if rep.ViaCount != 1 || rep.Primitives != 4 {
		t.Fatalf("vias/primitives=%d/%d, want 1/4", rep.ViaCount, rep.Primitives)
	}
	if fmt.Sprint(rep.Layers) != "[1 2]" || fmt.Sprint(rep.LayerSequence) != "[1 2]" {
		t.Fatalf("layers=%v sequence=%v", rep.Layers, rep.LayerSequence)
	}
	if fmt.Sprint(rep.WidthsMil) != "[8 20]" || rep.MinWidth == nil || *rep.MinWidth != 8 || rep.MaxWidth == nil || *rep.MaxWidth != 20 {
		t.Fatalf("width evidence=%v min=%v max=%v", rep.WidthsMil, rep.MinWidth, rep.MaxWidth)
	}
	throughAt, viaAt := -1, -1
	for i, s := range rep.Path {
		if s.Kind == "pad" && s.Ref == "C4.1" {
			throughAt = i
		}
		if s.Kind == "via" && s.PrimitiveID == "v1" {
			viaAt = i
		}
	}
	if throughAt < 0 || viaAt <= throughAt {
		t.Fatalf("combined path does not preserve the required waypoint before via: %+v", rep.Path)
	}
	if got := strings.Join(rep.ExcludedCopper, ","); !strings.Contains(got, "pours") || !strings.Contains(got, "PLANE") {
		t.Fatalf("excludedCopper=%v", rep.ExcludedCopper)
	}
	wantLength := 82.0 + math.Hypot(40, 40) + math.Hypot(60, 60)
	if math.Abs(rep.LengthMil-wantLength) > 1e-3 || rep.TurnCount != 1 {
		t.Fatalf("length/turns=%.4f/%d want %.4f/1", rep.LengthMil, rep.TurnCount, wantLength)
	}
	if rep.Legs[0].LengthMil <= 0 || rep.Legs[1].LengthMil <= 0 {
		t.Fatalf("leg measurements missing: %+v", rep.Legs)
	}
}

func TestAnalyzePcbNetPath_MeasuresTrackMidpointToEndpointSubsection(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-mid", "J1", "1", "SIG", 1, 50, 0, 2, 2),
		netPathRectPad("p-end", "U1", "1", "SIG", 1, 100, 0, 2, 2),
	}
	tracks := []pcbTrack{{ID: "trunk", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 100, Y2: 0, Width: 2}}

	rep, err := analyzePcbNetPath(pads, tracks, nil, nil, pcbNetPathOptions{From: "J1.1", To: "U1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Connected || math.Abs(rep.LengthMil-50) > 1e-4 || rep.TurnCount != 0 {
		t.Fatalf("midpoint→endpoint measurement=%.4fmil/%d, want 50/0: %+v", rep.LengthMil, rep.TurnCount, rep)
	}
}

func TestAnalyzePcbNetPath_MeasuresTrackMidpointToMidpointSubsection(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-a", "J1", "1", "SIG", 1, 25, 0, 2, 2),
		netPathRectPad("p-b", "U1", "1", "SIG", 1, 75, 0, 2, 2),
	}
	tracks := []pcbTrack{{ID: "trunk", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 100, Y2: 0, Width: 2}}

	rep, err := analyzePcbNetPath(pads, tracks, nil, nil, pcbNetPathOptions{From: "J1.1", To: "U1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Connected || math.Abs(rep.LengthMil-50) > 1e-4 || rep.TurnCount != 0 {
		t.Fatalf("midpoint→midpoint measurement=%.4fmil/%d, want 50/0: %+v", rep.LengthMil, rep.TurnCount, rep)
	}
}

func TestAnalyzePcbNetPath_MeasuresArcInteriorSubarc(t *testing.T) {
	const radius = 100.0
	mid := radius / math.Sqrt2
	pads := []pcbPadP{
		netPathRectPad("p-mid", "J1", "1", "CLK", 1, mid, mid, 2, 2),
		netPathRectPad("p-end", "U1", "1", "CLK", 1, 0, radius, 2, 2),
	}
	arcs := []pcbArc{{ID: "quarter", Net: "CLK", Layer: 1, X1: radius, Y1: 0, X2: 0, Y2: radius, Width: 2, ArcAngle: 90}}

	rep, err := analyzePcbNetPath(pads, nil, arcs, nil, pcbNetPathOptions{From: "J1.1", To: "U1.1"})
	if err != nil {
		t.Fatal(err)
	}
	want := radius * math.Pi / 4
	if !rep.Connected || math.Abs(rep.LengthMil-want) > 1e-3 || rep.TurnCount != 1 {
		t.Fatalf("arc interior subsection=%.4fmil/%d, want %.4f/1: %+v", rep.LengthMil, rep.TurnCount, want, rep)
	}
}

func TestAnalyzePcbNetPath_AmbiguousTraversedCenterlineContactIsUnknown(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-a", "J1", "1", "SIG", 1, 25, 8, 2, 2),
		netPathRectPad("p-b", "U1", "1", "SIG", 1, 100, 0, 2, 2),
	}
	tracks := []pcbTrack{
		{ID: "trunk", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 100, Y2: 0, Width: 10},
		{ID: "parallel-overlap", Net: "SIG", Layer: 1, X1: 25, Y1: 8, X2: 75, Y2: 8, Width: 10},
	}

	_, err := analyzePcbNetPath(pads, tracks, nil, nil, pcbNetPathOptions{From: "J1.1", To: "U1.1"})
	if err == nil || !strings.Contains(err.Error(), "measurement is unknown") || !strings.Contains(err.Error(), "distinct centerline junction") {
		t.Fatalf("ambiguous parallel copper must not report an actual length, got %v", err)
	}
}

func TestAnalyzePcbNetPath_OrderedWaypointRejectsBacktrackingBranch(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-j1", "J1", "1", "SIG", 1, -100, 0, 20, 20),
		netPathRectPad("p-r1", "R1", "1", "SIG", 1, 0, 100, 20, 20),
		netPathRectPad("p-u1", "U1", "1", "SIG", 1, 100, 0, 20, 20),
	}
	// Three terminal branches meet at one center. Every pair is connected, but a
	// simple J1→R1→U1 path does not exist: reaching terminal R1 requires returning
	// over the already-used center copper. Leg-by-leg BFS would falsely pass it.
	tracks := []pcbTrack{
		{ID: "left", Net: "SIG", Layer: 1, X1: -100, Y1: 0, X2: 0, Y2: 0, Width: 10},
		{ID: "up", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 0, Y2: 100, Width: 10},
		{ID: "right", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 100, Y2: 0, Width: 10},
	}
	rep, err := analyzePcbNetPath(pads, tracks, nil, nil, pcbNetPathOptions{From: "J1.1", Through: []string{"R1.1"}, To: "U1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Connected || !strings.Contains(rep.Reason, "single") {
		t.Fatalf("branch backtracking falsely proved ordered topology: %+v", rep)
	}
	if len(rep.Legs) != 2 || !rep.Legs[0].Connected || !rep.Legs[1].Connected {
		t.Fatalf("expected both individual legs to remain diagnostic-reachable: %+v", rep.Legs)
	}
}

func TestAnalyzePcbNetPath_SameNetAndXYCrossingDoNotBridgeLayers(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-j1", "J1", "1", "SIG", 1, 0, 0, 20, 20),
		netPathRectPad("p-u1", "U1", "1", "SIG", 2, 100, 0, 20, 20),
	}
	tracks := []pcbTrack{
		{ID: "top", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 50, Y2: 0, Width: 10},
		{ID: "bottom", Net: "SIG", Layer: 2, X1: 50, Y1: 0, X2: 100, Y2: 0, Width: 10},
	}
	rep, err := analyzePcbNetPath(pads, tracks, nil, nil, pcbNetPathOptions{From: "J1.1", To: "U1.1"})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Connected || len(rep.Legs) != 1 || rep.Legs[0].Connected {
		t.Fatalf("same net / same XY across layers was falsely treated as connected: %+v", rep)
	}
	if !strings.Contains(rep.Legs[0].Reason, "pours") {
		t.Fatalf("missing scope in failure reason: %q", rep.Legs[0].Reason)
	}
}

func TestAnalyzePcbNetPath_TopOnlyAndArcEndpointPath(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-c1", "C1", "1", "CLK", 1, 0, 0, 20, 20),
		netPathRectPad("p-u1", "U1", "5", "CLK", 1, 100, 100, 20, 20),
	}
	tracks := []pcbTrack{
		{ID: "t1", Net: "CLK", Layer: 1, X1: 0, Y1: 0, X2: 50, Y2: 0, Width: 10},
		{ID: "t2", Net: "CLK", Layer: 1, X1: 100, Y1: 50, X2: 100, Y2: 100, Width: 10},
	}
	arcs := []pcbArc{{ID: "a1", Net: "CLK", Layer: 1, X1: 50, Y1: 0, X2: 100, Y2: 50, Width: 12, ArcAngle: 90}}
	rep, err := analyzePcbNetPath(pads, tracks, arcs, nil, pcbNetPathOptions{From: "C1.1", To: "U1.5"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Connected || fmt.Sprint(rep.Layers) != "[1]" || fmt.Sprint(rep.LayerSequence) != "[1]" || rep.ViaCount != 0 {
		t.Fatalf("TOP-only arc path evidence wrong: %+v", rep)
	}
	foundArc := false
	for _, s := range rep.Path {
		if s.Kind == "arc" && s.PrimitiveID == "a1" && s.WidthMil == 12 && s.ArcAngle == 90 {
			foundArc = true
		}
	}
	if !foundArc {
		t.Fatalf("arc evidence missing: %+v", rep.Path)
	}
}

func TestAnalyzePcbNetPath_LayerConstraintIsAppliedDuringSearchAndExcludesVias(t *testing.T) {
	layer := 1
	pads := []pcbPadP{
		netPathRectPad("p-j1", "J1", "1", "SIG", 1, 0, 0, 20, 20),
		netPathRectPad("p-u1", "U1", "1", "SIG", 1, 120, 0, 20, 20),
	}
	tracks := []pcbTrack{
		// Shorter graph path changes layer through two physical vias.
		{ID: "a-entry", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 20, Y2: -20, Width: 8},
		{ID: "b-bottom", Net: "SIG", Layer: 2, X1: 20, Y1: -20, X2: 100, Y2: -20, Width: 8},
		{ID: "c-exit", Net: "SIG", Layer: 1, X1: 100, Y1: -20, X2: 120, Y2: 0, Width: 8},
		// Longer primitive chain remains wholly on TOP.
		{ID: "t1", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 20, Y2: 20, Width: 10},
		{ID: "t2", Net: "SIG", Layer: 1, X1: 20, Y1: 20, X2: 40, Y2: 20, Width: 10},
		{ID: "t3", Net: "SIG", Layer: 1, X1: 40, Y1: 20, X2: 60, Y2: 20, Width: 10},
		{ID: "t4", Net: "SIG", Layer: 1, X1: 60, Y1: 20, X2: 80, Y2: 20, Width: 10},
		{ID: "t5", Net: "SIG", Layer: 1, X1: 80, Y1: 20, X2: 100, Y2: 20, Width: 10},
		{ID: "t6", Net: "SIG", Layer: 1, X1: 100, Y1: 20, X2: 120, Y2: 0, Width: 10},
	}
	vias := []pcbViaP{
		{ID: "v1", Net: "SIG", X: 20, Y: -20, Dia: 24},
		{ID: "v2", Net: "SIG", X: 100, Y: -20, Dia: 24},
	}
	rep, err := analyzePcbNetPath(pads, tracks, nil, vias, pcbNetPathOptions{From: "J1.1", To: "U1.1", Layer: &layer})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Connected || rep.RequestedLayer == nil || *rep.RequestedLayer != 1 {
		t.Fatalf("layer-constrained path not proven: %+v", rep)
	}
	if rep.ViaCount != 0 || fmt.Sprint(rep.Layers) != "[1]" || fmt.Sprint(rep.LayerSequence) != "[1]" {
		t.Fatalf("TOP-only proof used another layer/via: layers=%v sequence=%v vias=%d", rep.Layers, rep.LayerSequence, rep.ViaCount)
	}
	for _, s := range rep.Path {
		if s.Kind == "via" || (s.Kind == "track" && s.Layer != 1) {
			t.Fatalf("restricted graph leaked a disallowed primitive: %+v", s)
		}
	}
	if !strings.Contains(strings.Join(rep.ExcludedCopper, ","), "physical vias") {
		t.Fatalf("physical-via exclusion not reported: %v", rep.ExcludedCopper)
	}

	// Remove the TOP-only chain: the same-net via/bottom route must not satisfy a
	// TOP-only proof merely because it returns to TOP at the destination.
	rep, err = analyzePcbNetPath(pads, tracks[:3], nil, vias, pcbNetPathOptions{From: "J1.1", To: "U1.1", Layer: &layer})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Connected {
		t.Fatalf("via-assisted path falsely passed --layer 1: %+v", rep)
	}
}

func TestAnalyzePcbNetPath_DirectViaOnPadIsNotFabricatedAsConnection(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-j1", "J1", "1", "SIG", 1, 0, 0, 30, 30),
		netPathRectPad("p-u1", "U1", "1", "SIG", 2, 100, 0, 20, 20),
	}
	vias := []pcbViaP{{ID: "v1", Net: "SIG", X: 20, Y: 0, Dia: 24}}
	bottom := pcbTrack{ID: "bottom", Net: "SIG", Layer: 2, X1: 20, Y1: 0, X2: 100, Y2: 0, Width: 10}
	opts := pcbNetPathOptions{From: "J1.1", To: "U1.1"}
	rep, err := analyzePcbNetPath(pads, []pcbTrack{bottom}, nil, vias, opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Connected {
		t.Fatalf("direct via-on-pad overlap fabricated a connection: %+v", rep)
	}

	// Add an explicit TOP dog-bone stub. The pad→track→via→BOTTOM track chain is
	// now listed copper and can be proven.
	topStub := pcbTrack{ID: "stub", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 20, Y2: 0, Width: 10}
	rep, err = analyzePcbNetPath(pads, []pcbTrack{topStub, bottom}, nil, vias, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Connected || rep.ViaCount != 1 {
		t.Fatalf("explicit track↔via stub did not prove the path: %+v", rep)
	}
}

func TestNetPathPadGeometryDoesNotUseRotatedOrEllipseAABBCorners(t *testing.T) {
	rotated := pcbNetPathNode{kind: "pad", net: "SIG", layer: 1, shape: "RECT", w: 40, h: 10, rotation: 45}
	insideAABBOutsideCopper := pcbNetPathNode{kind: "track", net: "SIG", layer: 1, width: 0.2, x1: 16, y1: 16, x2: 18, y2: 18}
	if netPathNodesTouch(rotated, insideAABBOutsideCopper, nil) {
		t.Fatal("rotated RECT AABB corner fabricated a pad-to-track contact")
	}
	actualLanding := pcbNetPathNode{kind: "track", net: "SIG", layer: 1, width: 0.2, x1: 12, y1: 12, x2: 14, y2: 14}
	if !netPathNodesTouch(rotated, actualLanding, nil) {
		t.Fatal("track on rotated RECT copper was not recognized")
	}

	ellipse := pcbNetPathNode{kind: "pad", net: "SIG", layer: 1, shape: "ELLIPSE", w: 40, h: 20}
	ellipseCorner := pcbNetPathNode{kind: "track", net: "SIG", layer: 1, width: 0.2, x1: 19, y1: 9, x2: 25, y2: 9}
	if netPathNodesTouch(ellipse, ellipseCorner, nil) {
		t.Fatal("ellipse AABB corner fabricated a pad-to-track contact")
	}
}

func TestAnalyzePcbNetPath_OrderedWaypointRejectsOverlappingTrackIDsAsUnknown(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-j1", "J1", "1", "SIG", 1, 0, 0, 10, 10),
		netPathRectPad("p-r1", "R1", "1", "SIG", 1, 50, 0, 10, 10),
		netPathRectPad("p-u1", "U1", "1", "SIG", 1, 100, 0, 10, 10),
	}
	tracks := []pcbTrack{
		{ID: "duplicate-a", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 100, Y2: 0, Width: 8},
		{ID: "duplicate-b", Net: "SIG", Layer: 1, X1: 25, Y1: 0, X2: 75, Y2: 0, Width: 8},
	}
	_, err := analyzePcbNetPath(pads, tracks, nil, nil, pcbNetPathOptions{From: "J1.1", Through: []string{"R1.1"}, To: "U1.1"})
	if err == nil || !strings.Contains(err.Error(), "overlap") || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("overlapping physical copper must make ordered proof unknown, got %v", err)
	}
}

func TestAnalyzePcbNetPath_OrderedWaypointRejectsParallelWideTrackCopperAsUnknown(t *testing.T) {
	pads := []pcbPadP{
		netPathRectPad("p-j1", "J1", "1", "SIG", 1, 0, 0, 2, 2),
		netPathRectPad("p-r1", "R1", "1", "SIG", 1, 0, 100, 2, 2),
		netPathRectPad("p-u1", "U1", "1", "SIG", 1, 4, 100, 2, 2),
	}
	tracks := []pcbTrack{
		{ID: "parallel-a", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 0, Y2: 100, Width: 10},
		{ID: "parallel-b", Net: "SIG", Layer: 1, X1: 4, Y1: 0, X2: 4, Y2: 100, Width: 10},
	}
	_, err := analyzePcbNetPath(pads, tracks, nil, nil, pcbNetPathOptions{From: "J1.1", Through: []string{"R1.1"}, To: "U1.1"})
	if err == nil || !strings.Contains(err.Error(), "unknown") || !strings.Contains(err.Error(), "copper-area overlap") {
		t.Fatalf("parallel 10mil tracks only 4mil apart must not manufacture an ordered PASS, got %v", err)
	}
}

func TestOrderedNetPathCopperAmbiguity_CoversTracksArcsViasAndAllowsOneJunction(t *testing.T) {
	tests := []struct {
		name   string
		tracks []pcbTrack
		arcs   []pcbArc
		vias   []pcbViaP
		want   string
	}{
		{
			name: "parallel track copper",
			tracks: []pcbTrack{
				{ID: "a", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 0, Y2: 100, Width: 10},
				{ID: "b", Net: "SIG", Layer: 1, X1: 4, Y1: 0, X2: 4, Y2: 100, Width: 10},
			},
			want: "copper-area overlap",
		},
		{
			name: "interior track cross",
			tracks: []pcbTrack{
				{ID: "horizontal", Net: "SIG", Layer: 1, X1: -20, Y1: 0, X2: 20, Y2: 0, Width: 2},
				{ID: "vertical", Net: "SIG", Layer: 1, X1: 0, Y1: -20, X2: 0, Y2: 20, Width: 2},
			},
			want: "interior centerline crossing",
		},
		{
			name: "track crosses arc body",
			tracks: []pcbTrack{
				{ID: "track", Net: "SIG", Layer: 1, X1: 5, Y1: 0, X2: 15, Y2: 0, Width: 2},
			},
			arcs: []pcbArc{
				{ID: "arc", Net: "SIG", Layer: 1, X1: 0, Y1: -10, X2: 0, Y2: 10, Width: 2, ArcAngle: 180},
			},
			want: "interior centerline crossing",
		},
		{
			name: "coincident arcs",
			arcs: []pcbArc{
				{ID: "arc-a", Net: "SIG", Layer: 1, X1: 0, Y1: -10, X2: 0, Y2: 10, Width: 2, ArcAngle: 180},
				{ID: "arc-b", Net: "SIG", Layer: 1, X1: 0, Y1: -10, X2: 0, Y2: 10, Width: 2, ArcAngle: 180},
			},
			want: "centerline overlap",
		},
		{
			name: "overlapping via annuli",
			vias: []pcbViaP{
				{ID: "via-a", Net: "SIG", X: 0, Y: 0, Dia: 24, Hole: 12},
				{ID: "via-b", Net: "SIG", X: 20, Y: 0, Dia: 24, Hole: 12},
			},
			want: "overlapping copper annuli",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, err := orderedNetPathCopperAmbiguity(tt.tracks, tt.arcs, tt.vias, "SIG", nil)
			if err != nil {
				t.Fatalf("unexpected normalization error: %v", err)
			}
			if !strings.Contains(reason, tt.want) {
				t.Fatalf("reason=%q, want substring %q", reason, tt.want)
			}
		})
	}

	for _, tracks := range [][]pcbTrack{
		{
			{ID: "left", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 50, Y2: 0, Width: 10},
			{ID: "right", Net: "SIG", Layer: 1, X1: 50, Y1: 0, X2: 100, Y2: 50, Width: 10},
		},
		{
			{ID: "trunk", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 100, Y2: 0, Width: 10},
			{ID: "branch", Net: "SIG", Layer: 1, X1: 50, Y1: 0, X2: 50, Y2: 50, Width: 10},
		},
	} {
		reason, err := orderedNetPathCopperAmbiguity(tracks, nil, nil, "SIG", nil)
		if err != nil || reason != "" {
			t.Fatalf("ordinary single endpoint/T junction was rejected: reason=%q err=%v", reason, err)
		}
	}
	normalArcs := []pcbArc{
		{ID: "arc-left", Net: "SIG", Layer: 1, X1: 0, Y1: 0, X2: 10, Y2: 10, Width: 2, ArcAngle: 90},
		{ID: "arc-right", Net: "SIG", Layer: 1, X1: 10, Y1: 10, X2: 20, Y2: 0, Width: 2, ArcAngle: -90},
	}
	if reason, err := orderedNetPathCopperAmbiguity(nil, normalArcs, nil, "SIG", nil); err != nil || reason != "" {
		t.Fatalf("two arcs sharing one ordinary endpoint were rejected: reason=%q err=%v", reason, err)
	}
}

func TestAnalyzePcbNetPath_UnsupportedPadGeometryIsFailClosed(t *testing.T) {
	goodA := netPathRectPad("p-a", "J1", "1", "SIG", 1, 0, 0, 10, 10)
	goodB := netPathRectPad("p-b", "U1", "1", "SIG", 1, 100, 0, 10, 10)
	unknownWaypoint := goodA
	unknownWaypoint.ShapeOK = false
	unknownWaypoint.Shape = "POLYGON"
	unknownWaypoint.ShapeIssue = "POLYGON pad geometry is not supported"
	if _, err := analyzePcbNetPath([]pcbPadP{unknownWaypoint, goodB}, nil, nil, nil, pcbNetPathOptions{From: "J1.1", To: "U1.1"}); err == nil || !strings.Contains(err.Error(), "waypoint") {
		t.Fatalf("unsupported waypoint geometry must be unknown/error, got %v", err)
	}

	bridge := pcbPadP{ID: "p-x", Designator: "X1", Number: "1", Net: "SIG", Layer: 1, X: 50, Y: 0, Shape: "POLYGON", ShapeIssue: "POLYGON unsupported"}
	if _, err := analyzePcbNetPath([]pcbPadP{goodA, goodB, bridge}, nil, nil, nil, pcbNetPathOptions{From: "J1.1", To: "U1.1"}); err == nil || !strings.Contains(err.Error(), "unknown, not failed") {
		t.Fatalf("unsupported same-net pad may bridge a failed graph, got %v", err)
	}
}

func TestParseNetPathReadbackIsStrictAndFinite(t *testing.T) {
	validPad := map[string]any{
		"primitiveId": "p1", "padNumber": "1", "net": "SIG", "layer": float64(1),
		"x": float64(0), "y": float64(0), "rotation": float64(0),
		"shape": []any{"RECT", float64(20), float64(10), float64(0)},
	}
	if _, err := parseNetPathPads(map[string]any{"components": []any{map[string]any{"designator": "J1", "pads": []any{validPad}}}}); err != nil {
		t.Fatalf("valid pad rejected: %v", err)
	}
	badPad := map[string]any{}
	for k, v := range validPad {
		badPad[k] = v
	}
	delete(badPad, "rotation")
	if _, err := parseNetPathPads(map[string]any{"components": []any{map[string]any{"designator": "J1", "pads": []any{badPad}}}}); err == nil {
		t.Fatal("missing pad rotation silently defaulted to zero")
	}
	if _, _, err := parseNetPathLines(map[string]any{"lines": []any{}}); err == nil || !strings.Contains(err.Error(), "arcs") {
		t.Fatalf("missing arcs array must report unavailable old readback, got %v", err)
	}
	badTrack := map[string]any{
		"primitiveId": "t1", "net": "SIG", "layer": float64(1), "startX": math.NaN(),
		"startY": float64(0), "endX": float64(10), "endY": float64(0), "lineWidth": float64(8),
	}
	if _, _, err := parseNetPathLines(map[string]any{"lines": []any{badTrack}, "arcs": []any{}, "arcsAvailable": true}); err == nil {
		t.Fatal("NaN track coordinate was accepted")
	}
	badVia := map[string]any{"primitiveId": "v1", "net": "SIG", "x": float64(0), "y": float64(0), "holeDiameter": float64(12)}
	if _, err := parseNetPathVias(map[string]any{"vias": []any{badVia}}); err == nil {
		t.Fatal("missing via diameter silently defaulted to zero")
	}
	invalidAnnulus := map[string]any{
		"primitiveId": "v2", "net": "SIG", "x": float64(0), "y": float64(0),
		"holeDiameter": float64(20), "diameter": float64(10),
	}
	if _, err := parseNetPathVias(map[string]any{"vias": []any{invalidAnnulus}}); err == nil || !strings.Contains(err.Error(), "greater than holeDiameter") {
		t.Fatalf("via diameter <= holeDiameter must be rejected, got %v", err)
	}
}

func TestNetPathGeometryEpsilonIsTight(t *testing.T) {
	a := pcbNetPathNode{x1: 0, y1: 0, x2: 0, y2: 0}
	near := pcbNetPathNode{x1: 0.0009, y1: 0, x2: 0.0009, y2: 0}
	far := pcbNetPathNode{x1: 0.0011, y1: 0, x2: 0.0011, y2: 0}
	if !endpointsNear(a, near, netPathGeomEps) || endpointsNear(a, far, netPathGeomEps) {
		t.Fatalf("epsilon=%g did not distinguish 0.0009mil from 0.0011mil", netPathGeomEps)
	}
}

func TestResolveNetPathWaypointsRejectsBadPadAndNetAssertions(t *testing.T) {
	pads := []pcbPadP{
		{Designator: "C1", Number: "1", Net: "+3V3"},
		{Designator: "U1", Number: "5", Net: "GND"},
	}
	for _, opts := range []pcbNetPathOptions{
		{From: "bad", To: "U1.5"},
		{From: "C9.1", To: "U1.5"},
		{From: "C1.1", To: "U1.5"},
		{From: "C1.1", To: "C1.1"},
		{From: "C1.1", To: "U1.5", Net: "+5V"},
	} {
		if _, _, err := resolveNetPathWaypoints(pads, opts); err == nil {
			t.Fatalf("opts=%+v unexpectedly accepted", opts)
		}
	}
}

func TestPcbNetPathCommandUsesOnlyTypedReadActions(t *testing.T) {
	var actions []string
	var payloads []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			fmt.Fprint(w, `{"service":"pcbpilot","windows":[{"windowId":"w1"}]}`)
			return
		}
		if r.URL.Path != "/action" {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Action  string         `json:"action"`
			Payload map[string]any `json:"payload"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		actions = append(actions, req.Action)
		payloads = append(payloads, req.Payload)
		switch req.Action {
		case "pcb.components.list":
			fmt.Fprint(w, `{"ok":true,"result":{"components":[{"designator":"C3","pads":[{"primitiveId":"p1","padNumber":"1","net":"+5V","layer":1,"x":0,"y":0,"rotation":0,"shape":["RECT",20,20,0],"width":20,"height":20}]},{"designator":"U2","pads":[{"primitiveId":"p2","padNumber":"3","net":"+5V","layer":1,"x":100,"y":0,"rotation":0,"shape":["RECT",20,20,0],"width":20,"height":20}]}]}}`)
		case "pcb.line.list":
			fmt.Fprint(w, `{"ok":true,"result":{"lines":[{"primitiveId":"t1","net":"+5V","layer":1,"startX":0,"startY":0,"endX":100,"endY":0,"lineWidth":20}],"arcs":[],"arcsAvailable":true}}`)
		case "pcb.via.list":
			fmt.Fprint(w, `{"ok":true,"result":{"vias":[]}}`)
		default:
			fmt.Fprintf(w, `{"ok":false,"error":{"message":"unexpected %s"}}`, req.Action)
		}
	}))
	defer srv.Close()
	host, port, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	cfg := &appConfig{host: host, ports: port + "-" + port}
	var out, errOut bytes.Buffer
	cmd := newPcbCmd(cfg, &out, &errOut)
	cmd.SetArgs([]string{"net-path", "--from", "C3.1", "--to", "U2.3", "--net", "+5V", "--layer", "1", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v (stderr=%s)", err, errOut.String())
	}
	if got := strings.Join(actions, ","); got != "pcb.components.list,pcb.line.list" {
		t.Fatalf("actions=%s", got)
	}
	if payloads[0]["includePads"] != true || payloads[1]["net"] != "+5V" || payloads[1]["layer"] != float64(1) {
		t.Fatalf("payloads=%+v", payloads)
	}
	var rep pcbNetPathReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		t.Fatalf("JSON output: %v\n%s", err, out.String())
	}
	if !rep.Connected || fmt.Sprint(rep.Layers) != "[1]" || rep.ViaCount != 0 || rep.RequestedLayer == nil || *rep.RequestedLayer != 1 {
		t.Fatalf("report=%+v", rep)
	}
	for _, a := range actions {
		if !strings.HasSuffix(a, ".list") {
			t.Fatalf("mutating/non-list action dispatched: %s", a)
		}
	}
	if !runIsReadOnly("pcb net-path --from C3.1 --to U2.3") {
		t.Fatal("pcbpilot apply does not classify pcb net-path as read-only")
	}
}

func TestPcbNetPathRejectsNonCopperLayerBeforeDispatch(t *testing.T) {
	cfg, captured, cleanup := newCapturingDaemon(t)
	defer cleanup()
	var out bytes.Buffer
	cmd := newPcbCmd(cfg, &out, &out)
	cmd.SetArgs([]string{"net-path", "--from", "C1.1", "--to", "U1.1", "--layer", "3"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "copper layer") {
		t.Fatalf("err=%v, want copper-layer validation", err)
	}
	if captured.action != "" {
		t.Fatalf("invalid layer dispatched %s", captured.action)
	}
}

func TestNetPathMeasureShortWide45DegreeNeighbors(t *testing.T) {
	nodes := []pcbNetPathNode{{kind: "pad", x: 0, y: 0}, {kind: "track", id: "long", x1: 0, y1: 0, x2: 0, y2: 40, width: 8}, {kind: "track", id: "short", x1: 0, y1: 40, x2: 4, y2: 44, width: 8}, {kind: "pad", x: 4, y: 44}}
	length, turns, err := measureNetPathCenterline(nodes, []int{0, 1, 2, 3})
	if err != nil || math.Abs(length-(40+math.Hypot(4, 4))) > 1e-4 || turns != 1 {
		t.Fatalf("short 45-degree junction: length=%v turns=%v err=%v", length, turns, err)
	}
}
func TestNetPathUniqueIntersectionPreferencePreservesRealAmbiguity(t *testing.T) {
	a := pcbNetPathNode{kind: "track", x1: 0, y1: 0, x2: 20, y2: 0, width: 8}
	b := pcbNetPathNode{kind: "track", x1: 10, y1: 0, x2: 30, y2: 0, width: 8}
	if _, err := netPathJunctionPointOnRoute(a, b); err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("positive-length overlap accepted: %v", err)
	}
	arc := pcbNetPathNode{kind: "arc", x1: 10, y1: 0, x2: 0, y2: -10, arcAngle: 270, width: 8}
	line := pcbNetPathNode{kind: "track", x1: -20, y1: 5, x2: 20, y2: 5, width: 8}
	intersections, err := netPathRouteCenterlineIntersections(line, arc)
	if err != nil || len(intersections) != 2 {
		t.Fatalf("invalid multiple-intersection fixture: %v %v", intersections, err)
	}
	if _, err := netPathJunctionPointOnRoute(line, arc); err == nil || !strings.Contains(err.Error(), "distinct centerline") {
		t.Fatalf("multiple true crossings accepted: %v", err)
	}
}

func TestAnalyzePcbNetPathPrefersExplicitGuardJunctionOverWidthShortcut(t *testing.T) {
	pads := []pcbPadP{netPathRectPad("a", "J1", "1", "GND", 1, 0, 0, 2, 2), netPathRectPad("b", "J2", "1", "GND", 1, 30, 20, 2, 2)}
	tracks := []pcbTrack{{ID: "lead", Net: "GND", Layer: 1, X1: 0, Y1: 0, X2: 0, Y2: 14, Width: 8}, {ID: "short45", Net: "GND", Layer: 1, X1: 0, Y1: 14, X2: 4, Y2: 18, Width: 8}, {ID: "explicit-landing", Net: "GND", Layer: 1, X1: 4, Y1: 18, X2: 4, Y2: 20, Width: 8}, {ID: "guard", Net: "GND", Layer: 1, X1: -30, Y1: 20, X2: 30, Y2: 20, Width: 8}}
	rep, err := analyzePcbNetPath(pads, tracks, nil, nil, pcbNetPathOptions{From: "J1.1", To: "J2.1", Net: "GND"})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Connected || rep.MeasurementPathKind != "exact-centerline" {
		t.Fatalf("did not select strong path: %+v", rep)
	}
	seen := false
	for _, p := range rep.Path {
		if p.PrimitiveID == "explicit-landing" {
			seen = true
		}
	}
	if !seen {
		t.Fatal("took width shortcut before the explicit landing")
	}
	want := 14 + math.Hypot(4, 4) + 2 + 26
	if math.Abs(rep.LengthMil-want) > 1e-4 {
		t.Fatalf("length=%f want %f", rep.LengthMil, want)
	}
	// Without the explicit landing only broad parallel copper contact remains.
	// It must not be mislabeled as an exact-centerline proof.
	pads = []pcbPadP{netPathRectPad("a", "J1", "1", "GND", 1, 25, 8, 2, 2), netPathRectPad("b", "J2", "1", "GND", 1, 100, 0, 2, 2)}
	tracks = []pcbTrack{{ID: "trunk", Net: "GND", Layer: 1, X1: 0, Y1: 0, X2: 100, Y2: 0, Width: 10}, {ID: "overlap", Net: "GND", Layer: 1, X1: 25, Y1: 8, X2: 75, Y2: 8, Width: 10}}
	if _, err := analyzePcbNetPath(pads, tracks, nil, nil, pcbNetPathOptions{From: "J1.1", To: "J2.1", Net: "GND"}); err == nil || !strings.Contains(err.Error(), "measurement is unknown") {
		t.Fatalf("width-only ambiguity accepted: %v", err)
	}
}
