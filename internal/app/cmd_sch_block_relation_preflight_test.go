package app

import (
	"reflect"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/blocks"
)

func relationalCH340Input(t *testing.T) bapInput {
	t.Helper()
	b, ok, err := blocks.Get("block.ch340c_usb_serial")
	if err != nil || !ok {
		t.Fatal(err)
	}
	l, err := b.SchematicLayout()
	if err != nil {
		t.Fatal(err)
	}
	d := map[string]bapDevice{}
	for _, p := range b.Parts {
		d[p.Part] = bapDevice{DeviceUUID: "test-device", LibraryUUID: "test-library"}
	}
	return bapInput{Block: b, Topology: bslBlockNets(b), Devices: d, Layout: l, OriginX: 400, OriginY: 300,
		Sheet: &layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825}}
}

func TestRelationalCH340PreflightFitsA4AndUsesAnchorOrigin(t *testing.T) {
	in := relationalCH340Input(t)
	// Regression: the old seven-role grid could put J_USB at x=1400, and
	// the declared anchor was shifted by its alphabetic grid cell offset.
	in.OriginX = 1100
	in.AtExplicit = true
	p, err := planBlockApply(in)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]bapPlacement{}
	for _, v := range p.Placements {
		by[v.Role] = v
		if !strings.HasSuffix(v.Source, "-seed") {
			t.Fatalf("unmeasured geometry claimed as %q", v.Source)
		}
		box := layoutBBox{MinX: v.X - bapRoleHalfExtent(v.PartKey), MaxX: v.X + bapRoleHalfExtent(v.PartKey), MinY: v.Y - bapPartMargin, MaxY: v.Y + bapPartMargin}
		if !boxInside(box, schUsableArea(*in.Sheet)) {
			t.Fatalf("%s outside A4: %+v", v.Role, box)
		}
	}
	if by["U"].X != p.Origin.X || by["U"].Y != p.Origin.Y {
		t.Fatal("origin must refer to anchor")
	}
	if !(by["J_USB"].X < by["D_ESD"].X && by["D_ESD"].X < by["U"].X) {
		t.Fatal("USB flow direction lost")
	}
	if by["J_USB"].Y != by["U"].Y || by["R_CC1"].Y != by["R_CC2"].Y {
		t.Fatal("flow/pair alignment lost")
	}
	if !p.Origin.Relocated {
		t.Fatal("out-of-bounds requested origin was not relocated")
	}
	q, err := planBlockApply(in)
	if err != nil || !reflect.DeepEqual(p, q) {
		t.Fatal("same input must be deterministic")
	}
}

func TestRelationalPreflightRejectsTinySheet(t *testing.T) {
	in := relationalCH340Input(t)
	in.Sheet = &layoutBBox{MinX: 0, MinY: 0, MaxX: 100, MaxY: 100}
	if _, err := planBlockApply(in); err == nil || !strings.Contains(err.Error(), "no in-sheet envelope") {
		t.Fatalf("must not fall back outside sheet: %v", err)
	}
}

func TestRelationalSeedTranslationAndIncompleteGuard(t *testing.T) {
	in := relationalCH340Input(t)
	in.Sheet = nil
	p, err := planBlockApply(in)
	if err != nil {
		t.Fatal(err)
	}
	in.OriginX += 100
	in.OriginY += 50
	q, err := planBlockApply(in)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range p.Placements {
		if q.Placements[i].X-v.X != 100 || q.Placements[i].Y-v.Y != 50 {
			t.Fatal("seed not translation invariant")
		}
	}
	if bslRequireSolved(p, nil) == nil {
		t.Fatal("missing anchor geometry accepted")
	}
	if bslRequireSolved(p, &bslAnchorGeom{}) == nil {
		t.Fatal("unresolved seed positions accepted")
	}
	for i := range p.Placements {
		p.Placements[i].Source = "flow"
	}
	if err := bslRequireSolved(p, &bslAnchorGeom{}); err != nil {
		t.Fatal(err)
	}
}

func TestRelationalPreflightRejectsOccupiedSheet(t *testing.T) {
	in := relationalCH340Input(t)
	in.Obstacles = []layoutBBox{*in.Sheet}
	if _, err := planBlockApply(in); err == nil || !strings.Contains(err.Error(), "relational layout preflight") {
		t.Fatalf("must not fall back over existing objects: %v", err)
	}
}
