package pcbauto

import (
	"math/rand"
	"os"
	"testing"
	"time"
)

func TestDebugChains(t *testing.T) {
	f := os.Getenv("PCBAUTO_CHAINS")
	if f == "" {
		t.Skip()
	}
	var b *Board
	if raw, err := os.ReadFile(f); err == nil {
		if b, err = FromSnapshot(raw); err != nil {
			t.Fatal(err)
		}
	} else {
		b = loadFixture(t, f)
	}
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	for _, ch := range c.Chains {
		ex, pm, _ := ChainCost(b, c, ch)
		t.Logf("inv=%v ex=%5.0f prot=%4.0f %v %v", chainInverted(b, c, ch), ex, pm, ch.Seq(), ch.Nets)
	}
}

func TestDebugAntenna(t *testing.T) {
	f := os.Getenv("PCBAUTO_ANT")
	if f == "" {
		t.Skip()
	}
	b := loadFixture(t, f)
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	for _, p := range b.Parts {
		if c.Kinds[p.Ref] == KindConnector || c.Kinds[p.Ref] == KindMechanical || c.Kinds[p.Ref] == KindAntenna {
			p.Fixed = true
		}
	}
	res, _ := Place(b, an, c, nil, PlaceOptions{Seed: 0})
	ant := b.Part("ANT1")
	t.Logf("ANT1 body %+v", ant.Body())
	for _, r := range []string{"R61", "L7", "R62", "R60", "C172", "C173", "C171", "RF1", "U19"} {
		p := b.Part(r)
		t.Logf("%s at %+v fixed=%v zone=%+v", r, p.Body().Center(), p.Fixed, res.Metrics)
		break
	}
	for _, r := range []string{"R61", "L7", "R62", "R60", "C172", "C173", "C171", "RF1", "U19"} {
		p := b.Part(r)
		t.Logf("%s at %.0f,%.0f fixed=%v", r, p.Body().Center().X, p.Body().Center().Y, p.Fixed)
	}
	for _, k := range b.Keepouts {
		t.Logf("keepout %+v", k)
	}
	t.Logf("outline %+v", b.Outline)
}

func TestDebugConstruct(t *testing.T) {
	f := os.Getenv("PCBAUTO_ANT")
	if f == "" {
		t.Skip()
	}
	b := loadFixture(t, f)
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	for _, p := range b.Parts {
		if k := c.Kinds[p.Ref]; k == KindConnector || k == KindMechanical || k == KindAntenna {
			p.Fixed = true
		}
	}
	pl := &placer{b: b, an: an, c: c, m: &Mechanics{Edge: map[string]MechEdge{}, Fixed: map[string]bool{}}, opt: PlaceOptions{Moves: 10, SpacingMil: 12, Timeout: time.Minute},
		rng: rand.New(rand.NewSource(1)), partNet: map[*Part][]int{}, zoneOf: map[*Part]Rect{}, decap: map[*Part]*Pad{}, spacing: 12}
	res := &PlaceResult{}
	pl.setup(res)
	t.Logf("U19 zone %+v region %+v", pl.zoneOf[b.Part("U19")], pl.region)
	pl.construct()
	t.Logf("U19 after construct %+v", b.Part("U19").Body().Center())
	for _, l := range c.Links {
		if l.From == "B-U19" || l.To == "B-U19" {
			t.Logf("link %s-%s %v %d", l.From, l.To, l.Kinds, len(l.Nets))
		}
	}
}
