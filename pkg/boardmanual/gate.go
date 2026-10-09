package boardmanual

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// GateName is the summary.json gates[] name of the manual check.
const GateName = "board-manual"

// GateCheck is the board-manual gate: it returns every reason the manual is not
// complete. Empty = pass. notes is nil when the notes file is missing.
//
//   - the notes file is missing;
//   - a connector lacks 是什么 (purpose) / 接什么 (connectsTo) / 作用
//     (systemRole) / 什么时候用、怎么用 (usage) / 注意 (cautions);
//   - a connector pin lacks a device/role description (pinNotes);
//   - an LED lacks a meaning (hardware or every firmware mode);
//   - the power-input section is missing;
//   - any field still contains "TODO" (openItems[] is the tracked list and
//     is exempt);
//   - a notes connector / pin / net / LED / probe disagrees with the board;
//   - (KiCad projects) a BOM part lacks its schematic symbol or its
//     footprint in the cross-probe map, or a schematic page has no plot.
func GateCheck(m *Manual, notes *Notes) []string {
	if notes == nil {
		return append([]string{"notes file missing: the manual has no human text (write pcbpilot.manual-notes.json)"}, crossProbeItems(m)...)
	}
	var out []string
	add := func(f string, a ...any) { out = append(out, fmt.Sprintf(f, a...)) }
	for _, c := range m.Connectors {
		cn := notes.Connectors[c.Ref]
		var miss []string
		for _, f := range []struct{ name, v string }{
			{"是什么 purpose", cn.Purpose}, {"接什么 connectsTo", cn.ConnectsTo}, {"作用 systemRole", cn.SystemRole},
		} {
			if strings.TrimSpace(f.v) == "" {
				miss = append(miss, f.name)
			}
		}
		if len(cn.Usage) == 0 {
			miss = append(miss, "什么时候用/怎么用 usage")
		}
		if len(cn.Cautions) == 0 {
			miss = append(miss, "注意 cautions")
		}
		if strings.TrimSpace(cn.Name) == "" {
			miss = append(miss, "name")
		}
		if len(miss) > 0 {
			add("connector %s: missing %s", c.Ref, strings.Join(miss, ", "))
		}
		var pins []string
		for _, r := range c.Pins {
			if strings.TrimSpace(r.Note) == "" {
				pins = append(pins, r.Pin)
			}
		}
		if len(pins) > 0 {
			add("connector %s: pin(s) %s have no device/role description (pinNotes)", c.Ref, strings.Join(pins, ","))
		}
	}
	for _, l := range m.LEDs {
		ok := strings.TrimSpace(l.Hardware) != ""
		if !ok && len(l.Modes) > 0 {
			ok = true
			for _, s := range l.Modes {
				if strings.TrimSpace(s) == "" || s == "—" {
					ok = false
				}
			}
		}
		if !ok {
			add("LED %s: no meaning (hardware, or modes for every firmware in firmwares[])", l.Ref)
		}
	}
	in := notes.Power.Input
	if m.Power.InputRef == "" || strings.TrimSpace(in.Polarity) == "" || strings.TrimSpace(in.RecommendedSupply) == "" {
		add("power input: missing (power.input.connector, polarity and recommendedSupply are required)")
	} else if m.Board != nil && m.Board.Part(m.Power.InputRef) == nil {
		add("power input: connector %s is not on the board", m.Power.InputRef)
	}
	ms := m.Mech
	if m.Board == nil || len(m.Board.Outline.Points) < 3 || ms.WidthMM <= 0 {
		add("mechanical: no board outline in the dump")
	}
	if !strings.Contains(ms.SVG, "<svg") {
		add("mechanical: dimensioned drawing missing")
	}
	mount := 0
	for _, h := range ms.Holes {
		if h.Ref != "" && !strings.Contains(h.Function, "peg") && !strings.Contains(h.Function, "定位") {
			mount++
		}
	}
	if mount == 0 {
		add("mechanical: no mounting holes found in the dump (footprint holes, H* parts or round multi-layer fills)")
	}
	if strings.TrimSpace(ms.Thickness) == "" {
		add("mechanical: board thickness unknown (mechanical.thicknessMm or the intent stackup)")
	}
	if len(ms.Heights) == 0 {
		add("mechanical: no part heights (mechanical.heights[] with a source)")
	}
	for _, h := range ms.Heights {
		if h.HeightMm <= 0 || strings.TrimSpace(h.Source) == "" {
			add("mechanical: height of %s needs heightMm and a source", h.Ref)
		}
	}
	switch {
	case !m.Sim.HasPost:
		add("simulation: post.json (sim post-layout of this board) missing")
	case m.BoardSHA == "" || (m.Sim.PostBoardSHA != m.BoardSHA && m.Sim.PostFileSHA != m.BoardSHA):
		add("simulation: post.json was computed on board %s, the manual's board is %s (re-run sim post-layout)", short12(m.Sim.PostBoardSHA), short12(m.BoardSHA))
	}
	out = append(out, crossProbeItems(m)...)
	for _, p := range todoPaths(m) {
		add("TODO left in %s", p)
	}
	for _, ch := range m.Checks {
		add("notes vs board %s: notes %q, board %q (%s)", ch.Where, ch.Doc, ch.Board, ch.Note)
	}
	return out
}

// crossProbeItems are the cross-probe map's gate items (none without one).
func crossProbeItems(m *Manual) []string {
	var out []string
	if xp := m.CrossProbe; xp != nil {
		for _, s := range xp.Missing {
			out = append(out, "cross-probe: "+s)
		}
	}
	return out
}

// todoPaths lists the fields of the computed manual that still say TODO
// (the SVGs and the tracked open items excluded).
func todoPaths(m *Manual) []string {
	b, err := json.Marshal(m)
	if err != nil {
		return []string{"(manual not serialisable: " + err.Error() + ")"}
	}
	var v any
	_ = json.Unmarshal(b, &v)
	var out []string
	var walk func(path string, x any)
	walk = func(path string, x any) {
		switch t := x.(type) {
		case map[string]any:
			keys := make([]string, 0, len(t))
			for k := range t {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				switch k {
				case "BoardSVG", "ProbeSVG", "LEDSVG", "SVG", "SVGDataURI", "CSVDataURI", "Maps", "Sim", "TODO", "Sources", "NotesSources", "Changes", "Checks", "CrossProbe":
					continue
				}
				walk(path+"."+k, t[k])
			}
		case []any:
			for i, e := range t {
				walk(fmt.Sprintf("%s[%d]", path, i), e)
			}
		case string:
			if strings.Contains(strings.ToUpper(t), "TODO") {
				out = append(out, strings.TrimPrefix(path, "."))
			}
		}
	}
	walk("", v)
	return out
}

func short12(s string) string {
	if s == "" {
		return "(none)"
	}
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
