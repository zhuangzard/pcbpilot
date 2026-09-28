package pcbauto

import (
	"os"
	"strings"
	"testing"
)

// Three presets on one routed board: the aesthetics weights differ, every
// hard group (electrical, efficiency) and the overall score are identical.
func TestAesProfilesOnlyTouchSoftObjectives(t *testing.T) {
	raw, err := os.ReadFile("testdata/esp32-v05-fixed.routed.json")
	if err != nil {
		t.Fatal(err)
	}
	in, err := AesInputFromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	b := in.Board
	an := Analyze(b, PowerSpec{}, nil)
	c := Understand(b, an)
	rr := &RouteResult{Tracks: in.Tracks, Vias: in.Vias, Stats: RouteStats{Completion: 100, Vias: len(in.Vias)}}
	var base *JointScore
	weights := map[float64]string{}
	for _, name := range []string{"functional", "balanced", "precision"} {
		p, err := AesProfileByName(name)
		if err != nil {
			t.Fatal(err)
		}
		js := Joint(b, an, c, nil, rr, nil, JointOptions{PlacementScore: -1, Aesthetics: true, AesProfile: &p})
		if js.Aesthetics == nil || js.Aesthetics.Profile.Name != name {
			t.Fatalf("%s: aesthetics block missing or wrong profile", name)
		}
		if js.Aesthetics.Weight != 0 {
			t.Errorf("%s: Phase A applies weight %v, want 0", name, js.Aesthetics.Weight)
		}
		weights[js.Aesthetics.Profile.Weight] = name
		if base == nil {
			base = js
			continue
		}
		if js.Overall != base.Overall || js.Quality != base.Quality || js.CompletionFactor != base.CompletionFactor {
			t.Errorf("%s changed the joint: overall %v/%v quality %v/%v", name, js.Overall, base.Overall, js.Quality, base.Quality)
		}
		for g, v := range base.Groups {
			if js.Groups[g] != v {
				t.Errorf("%s changed group %s: %v → %v", name, g, v, js.Groups[g])
			}
		}
	}
	if len(weights) != 3 {
		t.Errorf("presets do not carry distinct aesthetics weights: %v", weights)
	}
}

// The presets score looks differently: precision's 25 mil target grid and
// symmetry requirement are stricter than functional's audit-only 5 mil.
func TestAesProfilesChangeLooksScoring(t *testing.T) {
	raw, _ := os.ReadFile(fixtureDir + "/lckfb-k230-canmv.json")
	score := func(name string) *AestheticsReport {
		in, err := AesInputFromSnapshot(raw)
		if err != nil {
			t.Fatal(err)
		}
		p, _ := AesProfileByName(name)
		in.Profile = &p
		return Aesthetics(in)
	}
	f, pr := score("functional"), score("precision")
	if !(metric(t, pr, "P9").Score < metric(t, f, "P9").Score) {
		t.Errorf("precision P9 %.1f not stricter than functional %.1f", metric(t, pr, "P9").Score, metric(t, f, "P9").Score)
	}
	if !(metric(t, pr, "P4").Score < metric(t, f, "P4").Score) {
		t.Errorf("precision (symmetry required) P4 %.1f not stricter than functional %.1f", metric(t, pr, "P4").Score, metric(t, f, "P4").Score)
	}
}

func TestAesStyleRejectsHardConstraints(t *testing.T) {
	bad := map[string]string{
		"clearance":     `{"profile":"custom","base":"balanced","clearanceMil":3}`,
		"track width":   `{"aesthetics":{"profile":"precision","trackWidthMil":4}}`,
		"creepage":      `{"profile":"custom","base":"balanced","creepageMm":1}`,
		"via drill":     `{"profile":"custom","base":"balanced","slack":{"viaDrillMil":8}}`,
		"drc":           `{"profile":"custom","base":"balanced","drcIgnore":["clearance"]}`,
		"completion":    `{"profile":"custom","base":"balanced","minCompletion":80}`,
		"edge distance": `{"profile":"custom","base":"balanced","edgeClearanceMil":5}`,
		"neck":          `{"profile":"custom","base":"balanced","allowNecks":false}`,
		"unknown key":   `{"profile":"custom","base":"balanced","prettiness":11}`,
		"weight > 0.3":  `{"profile":"custom","base":"balanced","weight":0.5}`,
		"bad metric":    `{"profile":"custom","base":"balanced","metricWeights":{"electrical":2}}`,
		"bad grid":      `{"profile":"custom","base":"balanced","placementGridMil":7}`,
		"slack too big": `{"profile":"custom","base":"balanced","slack":{"wirelengthPct":40}}`,
		"unknown base":  `{"profile":"custom","base":"pretty"}`,
	}
	hard := map[string]bool{"clearance": true, "track width": true, "creepage": true, "via drill": true, "drc": true, "completion": true, "edge distance": true, "neck": true}
	for name, doc := range bad {
		if _, err := ParseAesStyle([]byte(doc)); err == nil {
			t.Errorf("%s: style accepted: %s", name, doc)
		} else if hard[name] {
			if !strings.Contains(err.Error(), "hard constraint") {
				t.Errorf("%s: error does not name the hard constraint: %v", name, err)
			}
		}
	}
	good := `{"aesthetics":{"profile":"custom","base":"precision","weight":0.15,"metricWeights":{"P4":3},"alignTolMil":1.5,"slack":{"wirelengthPct":3,"areaPct":1,"extraVias":2}},"steps":{}}`
	p, err := ParseAesStyle([]byte(good))
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "custom" || p.Weight != 0.15 || p.PlacementGridMil != 25 || !p.SymmetryRequired || p.metricWeight("P4") != 3 || p.Slack.ExtraVias != 2 {
		t.Errorf("custom profile %+v", p)
	}
	if p, err := ParseAesStyle([]byte(`{"profile":"auto"}`)); err != nil || p.Name != "auto" {
		t.Errorf("auto: %+v %v", p, err)
	}
	for name := range AesProfiles {
		p, _ := AesProfileByName(name)
		if err := p.Validate(); err != nil {
			t.Errorf("preset %s invalid: %v", name, err)
		}
	}
}

func TestAesAutoProfile(t *testing.T) {
	want := map[string]string{
		fixtureDir + "/lckfb-rk3568-4layer.json":     "functional",
		fixtureDir + "/lckfb-k230-canmv.json":        "functional",
		fixtureDir + "/lckfb-mipi-3in1-adapter.json": "balanced",
		"testdata/esp32-v05-fixed.routed.json":       "balanced",
	}
	for f, w := range want {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		in, _ := AesInputFromSnapshot(raw)
		in.Profile = &AesProfile{Name: "auto"}
		rep := Aesthetics(in)
		if rep.Profile.Auto == nil || rep.Profile.Name != w {
			t.Errorf("%s: auto chose %q (%v), want %s", f, rep.Profile.Name, rep.Profile.Auto, w)
			continue
		}
		if !strings.Contains(rep.Profile.Auto.Reason, "complexity index") {
			t.Errorf("%s: reason %q", f, rep.Profile.Auto.Reason)
		}
	}
}
