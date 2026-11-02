package pcbauto

import (
	"math"
	"strings"
	"testing"
)

func mechSpec(t *testing.T, js string) *MechSpec {
	t.Helper()
	m, err := ParseMech([]byte(js))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A board with an outline (a KiCad board seldom starts at 0,0) keeps it
// when the spec gives the same size, and the spec's coordinates count from
// that outline's lower-left corner.
func TestMechKeepsAnExistingOutline(t *testing.T) {
	ll := Point{4724.41, -7874.02}
	outline := RoundedRect(Rect{ll.X, ll.Y, ll.X + 260/0.0254, ll.Y + 200/0.0254}, 3/0.0254)
	b := &Board{Outline: append([]Point(nil), outline...)}
	if _, err := ApplyMech(b, mechSpec(t, `{"units":"mm","board":{"width":260,"height":200,"cornerRadius":3},"holes":[{"name":"MH1","x":5,"y":5,"dia":3.2}]}`)); err != nil {
		t.Fatal(err)
	}
	if len(b.Outline) != len(outline) || b.Outline[0] != outline[0] {
		t.Fatal("the existing outline was replaced")
	}
	h := b.Holes[len(b.Holes)-1]
	if want := ll.Add(Point{5 / 0.0254, 5 / 0.0254}); h.C.Dist(want) > 1e-6 {
		t.Fatalf("hole at %v, want %v (5 mm from the board's lower-left corner)", h.C, want)
	}
}

// Another size is refused unless replaceOutline, which keeps the origin.
func TestMechOutlineReplaceIsOptIn(t *testing.T) {
	ll := Point{1000, -3000}
	b := &Board{Outline: Rect{ll.X, ll.Y, ll.X + 4000, ll.Y + 3000}.Corners()}
	_, err := ApplyMech(b, mechSpec(t, `{"units":"mil","board":{"width":5000,"height":3000}}`))
	if err == nil || !strings.Contains(err.Error(), "replaceOutline") {
		t.Fatalf("a different size was accepted silently: %v", err)
	}
	if _, err := ApplyMech(b, mechSpec(t, `{"units":"mil","board":{"width":5000,"height":3000,"replaceOutline":true}}`)); err != nil {
		t.Fatal(err)
	}
	bb := b.Bounds()
	if math.Abs(bb.MinX-ll.X) > 1e-9 || math.Abs(bb.MinY-ll.Y) > 1e-9 || math.Abs(bb.W()-5000) > 1e-9 {
		t.Fatalf("replaced outline %v, want 5000 x 3000 at %v", bb, ll)
	}
	// No outline yet: the spec's, at its own origin, as before.
	nb := &Board{}
	if _, err := ApplyMech(nb, mechSpec(t, `{"units":"mil","board":{"width":2000,"height":1000}}`)); err != nil {
		t.Fatal(err)
	}
	if bb := nb.Bounds(); bb.MinX != 0 || bb.MinY != 0 || bb.W() != 2000 {
		t.Fatalf("new outline %v", bb)
	}
}
