package app

// cmd_kicad_rules_fake_test.go — a fake KiCad for kicad rules unit tests.
// It keeps the netclasses in a small JSON .kicad_pro ("rulesFakePro"),
// writes a route block like bridge netclasses, resolves effective classes
// by pattern and priority, and answers DRC probes from the .kicad_dru with
// the same owned-rule resolution kicad rules plans with.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

type rulesFakeClass struct {
	Name      string `json:"name"`
	Priority  int    `json:"priority"`
	Track     *int64 `json:"track,omitempty"`
	Clearance *int64 `json:"clearance,omitempty"`
	ViaDia    *int64 `json:"viaDiameter,omitempty"`
	ViaDrill  *int64 `json:"viaDrill,omitempty"`
}

type rulesFakePro struct {
	Fake     bool             `json:"fakeProject"`
	Default  kicad.ClassNM    `json:"default"`
	Classes  []rulesFakeClass `json:"-"`
	Patterns [][2]string      `json:"-"`
	Minimums kicad.Minimums   `json:"minimums"`
	Sizes    kicad.SizeSet    `json:"sizes"`
}

// The classes and patterns live where KiCad keeps them (net_settings.classes
// / net_settings.netclass_patterns), so stripOwnedProject works on the fake.
type rulesFakeNS struct {
	Classes  []rulesFakeClass `json:"classes"`
	Patterns []struct {
		Pattern  string `json:"pattern"`
		Netclass string `json:"netclass"`
	} `json:"netclass_patterns"`
}

type rulesFakeProAlias rulesFakePro

func (p rulesFakePro) MarshalJSON() ([]byte, error) {
	ns := rulesFakeNS{Classes: p.Classes}
	for _, pt := range p.Patterns {
		ns.Patterns = append(ns.Patterns, struct {
			Pattern  string `json:"pattern"`
			Netclass string `json:"netclass"`
		}{pt[0], pt[1]})
	}
	return json.Marshal(struct {
		rulesFakeProAlias
		NS rulesFakeNS `json:"net_settings"`
	}{rulesFakeProAlias(p), ns})
}

func (p *rulesFakePro) UnmarshalJSON(b []byte) error {
	var v struct {
		rulesFakeProAlias
		NS rulesFakeNS `json:"net_settings"`
	}
	v.rulesFakeProAlias = rulesFakeProAlias(*p)
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = rulesFakePro(v.rulesFakeProAlias)
	p.Classes, p.Patterns = v.NS.Classes, nil
	for _, pt := range v.NS.Patterns {
		p.Patterns = append(p.Patterns, [2]string{pt.Pattern, pt.Netclass})
	}
	return nil
}

type rulesFakeKiCad struct {
	t      *testing.T
	snap   []byte
	nets   []string
	layers int
	probes map[string][]kicad.ProbeTrack
	ids    map[string][]string
	// drcCalls counts DRC runs; drcErr makes DRC fail.
	drcCalls int
	drcErr   error
	// readErr makes RulesRead fail.
	readErr error
	// ignoreDRU: DRC behaves as if the .kicad_dru did not parse.
	ignoreDRU bool
}

func rulesI64(v int64) *int64 { return &v }

func rulesFakeDefault() rulesFakePro {
	return rulesFakePro{Fake: true, Default: kicad.ClassNM{Track: 200000, Clearance: 200000, ViaDiameter: 600000, ViaDrill: 300000},
		Minimums: kicad.Minimums{Track: 150000, Clearance: 150000, ViaDiameter: 500000, ViaDrill: 250000, ViaAnnular: 100000}}
}

func rulesFakeRead(pcb string) rulesFakePro {
	p := rulesFakeDefault()
	b, err := os.ReadFile(boardSibling(pcb, ".kicad_pro"))
	if err == nil {
		_ = json.Unmarshal(b, &p)
	}
	return p
}

func rulesFakeWrite(path string, p rulesFakePro) error {
	b, _ := json.MarshalIndent(p, "", "  ")
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func (f *rulesFakeKiCad) Version() (string, error) { return "fake-10.0.6", nil }

func (f *rulesFakeKiCad) Snapshot(pcb string) ([]byte, error) { return f.snap, nil }

// effective is the composite of every class whose pattern names net, in
// priority order, Default filling the rest.
func (p rulesFakePro) effective(net string) kicad.EffectiveNM {
	var hit []rulesFakeClass
	for _, pt := range p.Patterns {
		if pt[0] != net {
			continue
		}
		for _, c := range p.Classes {
			if c.Name == pt[1] {
				hit = append(hit, c)
			}
		}
	}
	sort.SliceStable(hit, func(i, j int) bool { return hit[i].Priority < hit[j].Priority })
	e := kicad.EffectiveNM{Class: "Default", Track: p.Default.Track, Clearance: p.Default.Clearance, ViaDiameter: p.Default.ViaDiameter, ViaDrill: p.Default.ViaDrill}
	var names []string
	set := map[string]bool{}
	for _, c := range hit {
		names = append(names, c.Name)
		pick := func(dst *int64, v *int64, k string) {
			if v != nil && !set[k] {
				*dst, set[k] = *v, true
			}
		}
		pick(&e.Track, c.Track, "t")
		pick(&e.Clearance, c.Clearance, "c")
		pick(&e.ViaDiameter, c.ViaDia, "d")
		pick(&e.ViaDrill, c.ViaDrill, "r")
	}
	if len(names) > 0 {
		e.Class = strings.Join(names, ",")
	}
	return e
}

func (f *rulesFakeKiCad) RulesRead(pcb string) (*kicad.RulesReadback, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	p := rulesFakeRead(pcb)
	rb := &kicad.RulesReadback{KiCadVersion: "fake", CopperLayers: f.layers, Minimums: p.Minimums, Patterns: p.Patterns, Nets: map[string]kicad.EffectiveNM{}, Sizes: p.Sizes}
	rb.Default = kicad.EffectiveNM{Class: "Default", Track: p.Default.Track, Clearance: p.Default.Clearance, ViaDiameter: p.Default.ViaDiameter, ViaDrill: p.Default.ViaDrill}
	for _, c := range p.Classes {
		pr := c.Priority
		rb.Classes = append(rb.Classes, kicad.ReadClass{Name: c.Name, Priority: &pr, Track: c.Track, Clearance: c.Clearance, ViaDiameter: c.ViaDia, ViaDrill: c.ViaDrill})
	}
	for _, n := range f.nets {
		rb.Nets[n] = p.effective(n)
	}
	return rb, nil
}

func rulesNmMil(v float64) int64 { return int64(math.Round(v * 25400)) }

// NetclassesReq is a small bridge netclasses: drop owned classes, one PP
// class per distinct requirement (max of intent, effective, minimums),
// domain classes, the route block, the pre-defined sizes.
func (f *rulesFakeKiCad) NetclassesReq(pcb string, req kicad.NetclassRequest, out string) (*kicad.NetclassFull, error) {
	p := rulesFakeRead(pcb)
	var keep []rulesFakeClass
	for _, c := range p.Classes {
		if !reOwnedClass.MatchString(c.Name) {
			keep = append(keep, c)
		}
	}
	var pats [][2]string
	for _, pt := range p.Patterns {
		if !reOwnedClass.MatchString(pt[1]) {
			pats = append(pats, pt)
		}
	}
	p.Classes, p.Patterns = keep, pats
	have := map[string]bool{}
	for _, n := range f.nets {
		have[n] = true
	}
	res := &kicad.NetclassFull{}
	type key struct{ t, i, m, c, vd, vr int64 }
	groups := map[key][]string{}
	for _, net := range sortedKeysOf(req.Nets) {
		r := req.Nets[net]
		if !have[net] {
			res.MissingNets = append(res.MissingNets, net)
			continue
		}
		e := p.effective(net)
		mx := func(a ...int64) int64 {
			m := a[0]
			for _, v := range a[1:] {
				if v > m {
					m = v
				}
			}
			return m
		}
		inner := rulesNmMil(r.InnerMil)
		if inner == 0 {
			inner = rulesNmMil(r.OuterMil)
		}
		k := key{mx(rulesNmMil(r.OuterMil), e.Track, p.Minimums.Track), mx(inner, p.Minimums.Track), rulesNmMil(r.MinMil),
			mx(rulesNmMil(r.ClearanceMil), e.Clearance, p.Minimums.Clearance), mx(rulesNmMil(r.ViaDiaMil), e.ViaDiameter, p.Minimums.ViaDiameter),
			mx(rulesNmMil(r.ViaDrillMil), e.ViaDrill, p.Minimums.ViaDrill)}
		groups[k] = append(groups[k], net)
	}
	var ks []key
	for k := range groups {
		ks = append(ks, k)
	}
	sort.Slice(ks, func(i, j int) bool {
		a, b := ks[i], ks[j]
		return fmt.Sprint(a) < fmt.Sprint(b)
	})
	var rules []string
	for i, k := range ks {
		name := fmt.Sprintf("PP%d_W%s", i+1, strings.ReplaceAll(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", float64(k.t)/25400), "0"), "."), ".", "p"))
		p.Classes = append(p.Classes, rulesFakeClass{Name: name, Priority: -3000000 + i, Track: rulesI64(k.t), Clearance: rulesI64(k.c), ViaDia: rulesI64(k.vd), ViaDrill: rulesI64(k.vr)})
		for _, n := range groups[k] {
			p.Patterns = append(p.Patterns, [2]string{n, name})
		}
		nc := kicad.NetclassClassNM{NetClass: kicad.NetClass{Name: name, Nets: groups[k], TrackWidthMil: milOfNm(k.t), InnerWidthMil: milOfNm(k.i), MinMil: milOfNm(k.m), ClearanceMil: milOfNm(k.c),
			ViaDiameterMil: milOfNm(k.vd), ViaDrillMil: milOfNm(k.vr)}, NM: kicad.ClassNM{Track: k.t, Inner: k.i, Min: k.m, Clearance: k.c, ViaDiameter: k.vd, ViaDrill: k.vr}}
		res.Classes = append(res.Classes, nc.NetClass)
		res.ClassesNM = append(res.ClassesNM, nc)
		if k.m > 0 {
			rules = append(rules, fmt.Sprintf("(rule \"pcbpilot %s min\"\n  (condition \"A.hasNetclass('%s')\")\n  (constraint track_width (min %s)))", name, name, kicad.MmOfNm(k.m)))
		}
		if k.i > k.t {
			lo := k.m
			if lo <= 0 {
				lo = p.Minimums.Track
			}
			rules = append(rules, fmt.Sprintf("(rule \"pcbpilot %s inner\"\n  (layer inner)\n  (condition \"A.hasNetclass('%s')\")\n  (constraint track_width (min %s) (opt %s)))", name, name, kicad.MmOfNm(lo), kicad.MmOfNm(k.i)))
		}
	}
	res.Domains = map[string][]string{}
	for i, pr := range req.Isolation {
		for _, side := range []string{"a", "b"} {
			dom := reDomainChars.ReplaceAllString(fmt.Sprint(pr[side]), "_")
			nets, _ := pr[side+"Nets"].([]string)
			cls := "PPD_" + dom
			if _, ok := res.Domains[dom]; !ok {
				p.Classes = append(p.Classes, rulesFakeClass{Name: cls, Priority: -1000000 + i})
			}
			for _, n := range nets {
				if have[n] && !rulesHasStr(res.Domains[dom], n) {
					res.Domains[dom] = append(res.Domains[dom], n)
					p.Patterns = append(p.Patterns, [2]string{n, cls})
				}
			}
		}
		da, db := domainClass(fmt.Sprint(pr["a"])), domainClass(fmt.Sprint(pr["b"]))
		var cons []string
		if v, _ := pr["clearanceMm"].(float64); v > 0 {
			cons = append(cons, fmt.Sprintf("(constraint clearance (min %gmm))", v))
		}
		if v, _ := pr["creepageMm"].(float64); v > 0 {
			cons = append(cons, fmt.Sprintf("(constraint creepage (min %gmm))", v))
		}
		rules = append(rules, fmt.Sprintf("(rule \"pcbpilot isolation %s|%s\"\n  (condition \"(A.hasNetclass('%s') && B.hasNetclass('%s')) || (A.hasNetclass('%s') && B.hasNetclass('%s'))\")\n  %s)", da, db, da, db, db, da, strings.Join(cons, "\n  ")))
	}
	for i, ed := range req.Edges {
		dom := reDomainChars.ReplaceAllString(fmt.Sprint(ed["domain"]), "_")
		cls := "PPD_" + dom
		nets, _ := ed["nets"].([]string)
		if _, ok := res.Domains[dom]; !ok {
			res.Domains[dom] = nil
			p.Classes = append(p.Classes, rulesFakeClass{Name: cls, Priority: -900000 + i})
		}
		for _, n := range nets {
			if have[n] && !rulesHasStr(res.Domains[dom], n) {
				res.Domains[dom] = append(res.Domains[dom], n)
				p.Patterns = append(p.Patterns, [2]string{n, cls})
			}
		}
		milv, _ := ed["mil"].(float64)
		mm := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.4f", milv*0.0254), "0"), ".")
		rules = append(rules, fmt.Sprintf("(rule \"pcbpilot edge %s\"\n  (condition \"A.hasNetclass('%s')\")\n  (constraint edge_clearance (min %smm)))", cls, cls, mm))
	}
	if req.Sizes != nil {
		old := map[int64]bool{}
		for _, t := range req.Sizes.Tracks {
			old[t] = true
		}
		var tracks []int64
		for _, t := range p.Sizes.Tracks {
			if !old[t] {
				tracks = append(tracks, t)
			}
		}
		base := map[int64]bool{}
		for _, t := range tracks {
			base[t] = true
		}
		sr := &kicad.SizesResult{}
		for _, c := range res.ClassesNM {
			for _, w := range []int64{c.NM.Track, c.NM.Inner} {
				if !base[w] {
					base[w] = true
					tracks = append(tracks, w)
					sr.Owned.Tracks = append(sr.Owned.Tracks, w)
				}
			}
		}
		sort.Slice(tracks, func(i, j int) bool { return tracks[i] < tracks[j] })
		sort.Slice(sr.Owned.Tracks, func(i, j int) bool { return sr.Owned.Tracks[i] < sr.Owned.Tracks[j] })
		p.Sizes.Tracks, sr.Tracks = tracks, tracks
		res.Sizes = sr
	}
	if err := rulesFakeWrite(boardSibling(out, ".kicad_pro"), p); err != nil {
		return nil, err
	}
	src, _ := os.ReadFile(boardSibling(pcb, ".kicad_dru"))
	user, ours, _, err := kicad.SplitDRU(string(src))
	if err != nil {
		return nil, err
	}
	text := user + ours
	if len(rules) > 0 {
		if !strings.Contains(text, "(version") {
			text = "(version 1)\n" + text
		}
		text = strings.TrimRight(text, "\n") + "\n" + kicad.DRURouteBegin + "\n" + strings.Join(rules, "\n") + "\n" + kicad.DRURouteEnd + "\n"
	}
	if strings.TrimSpace(text) != "" {
		if err := os.WriteFile(boardSibling(out, ".kicad_dru"), []byte(text), 0o644); err != nil {
			return nil, err
		}
	}
	b, _ := os.ReadFile(pcb)
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return nil, err
	}
	res.Out, res.DRURules = out, len(rules)
	return res, nil
}

func (f *rulesFakeKiCad) RulesProbe(pcb string, tracks []kicad.ProbeTrack, out string) ([]string, error) {
	b, _ := os.ReadFile(pcb)
	if err := os.WriteFile(out, b, 0o644); err != nil {
		return nil, err
	}
	var ids []string
	for i := range tracks {
		ids = append(ids, fmt.Sprintf("probe-%d", i))
	}
	if f.probes == nil {
		f.probes, f.ids = map[string][]kicad.ProbeTrack{}, map[string][]string{}
	}
	f.probes[out], f.ids[out] = tracks, ids
	return ids, nil
}

// DRC flags a probe below its class's owned track_width floor, and two
// probes of different nets closer than max(their class clearances, an
// isolation rule between their domain classes).
func (f *rulesFakeKiCad) DRC(pcb, out string) (*kicad.DRCReport, error) {
	f.drcCalls++
	if f.drcErr != nil {
		return nil, f.drcErr
	}
	p := rulesFakeRead(pcb)
	var rules []kicad.DRURule
	if b, err := os.ReadFile(boardSibling(pcb, ".kicad_dru")); err == nil && !f.ignoreDRU {
		rules, _ = kicad.ParseDRU(string(b))
	}
	rep := &kicad.DRCReport{Counts: map[string]int{}}
	ts, ids := f.probes[pcb], f.ids[pcb]
	for i, t := range ts {
		eff := p.effective(t.Net)
		layer := "outer"
		if t.Layer >= 15 {
			layer = "inner"
		}
		floor := p.Minimums.Track
		for _, c := range strings.Split(eff.Class, ",") {
			if v, _ := rulesTrackFloor(rules, c, layer); v > floor {
				floor = v
			}
		}
		if t.WidthNm < floor {
			rep.Violations = append(rep.Violations, kicad.Violation{Rule: "track_width", Objs: []string{ids[i]}})
		}
	}
	for i := 0; i+1 < len(ts); i++ {
		a, b := ts[i], ts[i+1]
		if a.Net == b.Net || a.StartX != b.StartX {
			continue
		}
		gap := int64(math.Round(math.Abs(b.StartY-a.StartY)*25400)) - (a.WidthNm+b.WidthNm)/2
		need := max(p.effective(a.Net).Clearance, p.effective(b.Net).Clearance)
		for _, r := range rules {
			if strings.HasPrefix(r.Name, "pcbpilot isolation ") {
				for _, c := range r.Constraints {
					if c.Type == "clearance" && c.Min != nil && *c.Min > need {
						need = *c.Min
					}
				}
			}
		}
		if gap < need {
			rep.Violations = append(rep.Violations, kicad.Violation{Rule: "clearance", Objs: []string{ids[i], ids[i+1]}})
		}
	}
	rep.Total = len(rep.Violations)
	return rep, nil
}
