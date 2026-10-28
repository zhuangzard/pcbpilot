package app

// kicad_rules_checks.go — the plan entries and the checks of `kicad rules`:
// plan-coverage, staged-readback / readback / effective-rules (pcbnew
// readback + kicad-cli DRC probes), copper-widths, via-size, via-current and
// the advisory isolation-placement proxy; user-rule conflicts.

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
)

const (
	nmPerMil = 25400.0
	// rulesTolMil is the copper check tolerance (1 µm): KiCad stores nm,
	// pcbpilot's snapshot rounds to 0.0001 mil.
	rulesTolMil = 0.04
	// probeUnderNm: a width probe sits this far below the floor; a pair
	// probe this far inside the pair clearance.
	probeUnderNm = 2000
	probeGapNm   = 5000
)

func milOfNm(v int64) float64 { return math.Round(float64(v)/nmPerMil*1e6) / 1e6 }

func firstClass(composite string) string { return strings.Split(composite, ",")[0] }

func hasClass(composite, cls string) bool {
	for _, c := range strings.Split(composite, ",") {
		if c == cls {
			return true
		}
	}
	return false
}

var reOwnedClass = regexp.MustCompile(`^PP(\d+_|D_|V_|F_)`)
var reDomainChars = regexp.MustCompile(`[^A-Za-z0-9_]`)

// reGeneratedClass: the class names bridge netclasses generates.
var reGeneratedClass = regexp.MustCompile(`^PP(\d+_W[0-9p]+|D_[A-Za-z0-9_]+|F_\d+_\d+_\d+_\d+)$`)

func domainClass(dom string) string { return "PPD_" + reDomainChars.ReplaceAllString(dom, "_") }

// ── board snapshot (the subset the checks read) ─────────────────────────────

type rulesRect struct {
	MinX float64 `json:"minX"`
	MinY float64 `json:"minY"`
	MaxX float64 `json:"maxX"`
	MaxY float64 `json:"maxY"`
}

type rulesSeg struct {
	ID     string   `json:"primitiveId"`
	Net    string   `json:"net"`
	Layer  int      `json:"layer"`
	X1     float64  `json:"startX"`
	Y1     float64  `json:"startY"`
	X2     float64  `json:"endX"`
	Y2     float64  `json:"endY"`
	MidX   *float64 `json:"midX,omitempty"`
	MidY   *float64 `json:"midY,omitempty"`
	Width  float64  `json:"lineWidth"`
	ArcDeg float64  `json:"arcAngle,omitempty"`
}

func (s rulesSeg) length() float64 {
	chord := math.Hypot(s.X2-s.X1, s.Y2-s.Y1)
	if s.MidX == nil || s.ArcDeg == 0 {
		return chord
	}
	th := math.Abs(s.ArcDeg) * math.Pi / 180
	if s := math.Sin(th / 2); s > 1e-9 {
		return chord / (2 * s) * th
	}
	return chord
}

type rulesSnap struct {
	CopperLayers int `json:"copperLayers"`
	Outline      *struct {
		BBox *rulesRect `json:"bbox"`
	} `json:"outline"`
	Components []struct {
		Designator string     `json:"designator"`
		Side       string     `json:"side"`
		BBox       *rulesRect `json:"bbox"`
		Courtyard  *rulesRect `json:"courtyard"`
		Pads       []struct {
			Net   string  `json:"net"`
			Layer int     `json:"layer"`
			X     float64 `json:"x"`
			Y     float64 `json:"y"`
			W     float64 `json:"width"`
			H     float64 `json:"height"`
		} `json:"pads"`
	} `json:"components"`
	Copper *struct {
		Lines []rulesSeg `json:"lines"`
		Arcs  []rulesSeg `json:"arcs"`
		Vias  []struct {
			ID    string  `json:"primitiveId"`
			Net   string  `json:"net"`
			X     float64 `json:"x"`
			Y     float64 `json:"y"`
			Dia   float64 `json:"diameter"`
			Drill float64 `json:"holeDiameter"`
		} `json:"vias"`
		Poured []any `json:"poured"`
	} `json:"copper"`
}

func decodeRulesSnap(raw []byte) (*rulesSnap, error) {
	var s rulesSnap
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("decode board snapshot: %w", err)
	}
	return &s, nil
}

// extent is the board's outline box (else the footprints'), for the probes.
func (s *rulesSnap) extent() rulesRect {
	if s.Outline != nil && s.Outline.BBox != nil {
		return *s.Outline.BBox
	}
	r := rulesRect{MinX: math.Inf(1), MinY: math.Inf(1), MaxX: math.Inf(-1), MaxY: math.Inf(-1)}
	for _, c := range s.Components {
		for _, p := range c.Pads {
			r.MinX, r.MinY = math.Min(r.MinX, p.X), math.Min(r.MinY, p.Y)
			r.MaxX, r.MaxY = math.Max(r.MaxX, p.X), math.Max(r.MaxY, p.Y)
		}
	}
	if math.IsInf(r.MinX, 0) {
		return rulesRect{}
	}
	return r
}

// ── plan entries ────────────────────────────────────────────────────────────

func (r *rulesRun) classOf(net string) (kicad.NetclassClassNM, bool) {
	for _, c := range r.full.ClassesNM {
		for _, n := range c.Nets {
			if n == net {
				return c, true
			}
		}
	}
	return kicad.NetclassClassNM{}, false
}

// planNets fills rep.Nets and the plan-coverage check.
func (r *rulesRun) planNets() {
	rep := r.rep
	cov := rulesCheck{Name: "plan-coverage", Status: gate.Pass}
	pass, cc, fail := 0, 0, 0
	for _, b := range r.bases {
		if _, ok := r.ri.Reqs[b.Net]; !ok {
			continue // not on the board
		}
		c, ok := r.classOf(b.Net)
		if !ok {
			continue
		}
		n := r.in.Nets[b.Net]
		e := rulesNetPlan{rulesNetBasis: b, Class: c.Name, Domain: n.Domain,
			OuterMil: milOfNm(c.NM.Track), InnerMil: milOfNm(c.NM.Inner), MinMil: milOfNm(c.NM.Min),
			ClearanceMil: milOfNm(c.NM.Clearance), ViaDiaMil: milOfNm(c.NM.ViaDiameter), ViaDrillMil: milOfNm(c.NM.ViaDrill)}
		if e.InnerMil <= 0 {
			e.InnerMil = e.OuterMil
		}
		if e.OuterMil > b.Width.OuterMil+reqEpsMil {
			e.Floors = append(e.Floors, fmt.Sprintf("outer width %g mil (board minimum or the net's own class; required %g)", e.OuterMil, b.Width.OuterMil))
		}
		if req := r.ri.Reqs[b.Net]; e.ClearanceMil > req.ClearanceMil+reqEpsMil {
			e.Floors = append(e.Floors, fmt.Sprintf("clearance %g mil (board minimum or the net's own class; required %g)", e.ClearanceMil, req.ClearanceMil))
		} else if req.ClearanceMil > n.ClearanceMil+reqEpsMil {
			e.Floors = append(e.Floors, fmt.Sprintf("clearance %g mil: the insulation pair clearance on every net of the pair (intent %g)", req.ClearanceMil, n.ClearanceMil))
		}
		e.Via.IntentCount = n.ViasPerTransition
		if n.Via != nil && n.Via.CountPerTransition > 0 {
			e.Via.IntentCount = n.Via.CountPerTransition
		}
		e.Via.Basis = fmt.Sprintf("via %g/%g mil (max of the intent class via, the net's class and the board minimums); count %d per transition from the intent", e.ViaDiaMil, e.ViaDrillMil, e.Via.IntentCount)
		if b.Current != nil && b.Current.A > 0 && (b.Role == "power" || b.Role == "ground" || b.Role == "switch") {
			need, per := viaCountNeed(b.Current.A, e.ViaDrillMil, rep.Copper.TempRiseC)
			e.Via.NeedCount, e.Via.PerViaA = need, math.Round(per*1000)/1000
			e.Via.Basis += fmt.Sprintf("; %.3f A needs %d via(s) of %g mil drill (%.3f A each, ΔT %g °C)", b.Current.A, need, e.ViaDrillMil, per, rep.Copper.TempRiseC)
			if need > e.Via.IntentCount && e.Via.IntentCount > 0 {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: the current needs %d via(s) per transition, the intent says %d (the via-current check of --check judges the copper)", b.Net, need, e.Via.IntentCount))
			}
		}
		switch b.Status {
		case "fail":
			fail++
			cov.Items = append(cov.Items, b.Net+": "+b.Detail)
		case "cannot-check":
			cc++
			cov.Items = append(cov.Items, b.Net+": "+b.Detail)
		default:
			pass++
		}
		rep.Nets = append(rep.Nets, e)
	}
	switch {
	case fail > 0:
		cov.Status = gate.Fail
	case cc > 0:
		cov.Status = gate.CannotCheck
	case len(rep.Nets) == 0:
		cov.Status, cov.Detail = gate.CannotCheck, "no intent net with a width is on the board: nothing planned"
	}
	if cov.Detail == "" {
		cov.Detail = fmt.Sprintf("%d net(s) planned: %d with a verified width basis, %d cannot-check, %d incompatible", len(rep.Nets), pass, cc, fail)
	}
	rep.check(cov)
}

// planIsolation lists the insulation pair rules.
func (r *rulesRun) planIsolation() {
	for _, p := range r.in.Pairs {
		if p.ClearanceMm <= 0 && p.CreepageMm <= 0 {
			continue
		}
		r.rep.Isolation = append(r.rep.Isolation, rulesIsoPlan{A: p.A, B: p.B, ClassA: domainClass(p.A), ClassB: domainClass(p.B),
			ClearanceMm: p.ClearanceMm, CreepageMm: p.CreepageMm, Bridges: p.Bridges,
			Note: "domain classes + a .kicad_dru clearance/creepage rule between them; the pair clearance is also each pair net's class clearance (route's behaviour: an over-constraint against nets outside the pair)"})
	}
}

// planFloor is the plan's track_width minimum for net e on a layer kind,
// resolved from the staged rules (the last pcbpilot-owned rule wins).
func (r *rulesRun) planFloor(e rulesNetPlan, layer string) int64 {
	v, _ := rulesTrackFloor(r.druRule, e.Class, layer)
	return v
}

// ── conflicts ───────────────────────────────────────────────────────────────

// conflicts lists user netclasses and .kicad_dru rules the plan would
// remove, outrank to another value or weaken.
func (r *rulesRun) conflicts() {
	rep := r.rep
	replace := map[string]bool{}
	for _, n := range r.o.replace {
		replace[n] = true
	}
	add := func(kind, name, detail string) {
		rep.Conflicts = append(rep.Conflicts, rulesConflict{Kind: kind, Name: name, Detail: detail, Replaced: replace[name]})
	}
	planned := map[string]rulesNetPlan{}
	for _, e := range rep.Nets {
		planned[e.Net] = e
	}
	for _, c := range r.rb0.Classes {
		if c.Name == "Default" {
			continue
		}
		if reOwnedClass.MatchString(c.Name) {
			// The prefixes are pcbpilot's ownership marker (KiCad keeps no
			// class description in the project): only a name pcbpilot never
			// generates is a user class that re-applying would remove.
			if !reGeneratedClass.MatchString(c.Name) {
				add("class-name", c.Name, "a user netclass with a pcbpilot class prefix (PPn_ / PPD_ / PPF_ / PPV_): re-applying pcbpilot's rules removes it")
			}
			continue
		}
		var hit []string
		for _, net := range sortedKeysOf(planned) {
			e := planned[net]
			if !hasClass(r.rb0.Nets[net].Class, c.Name) {
				continue
			}
			cls, _ := r.classOf(net)
			low := func(what string, have *int64, want int64) {
				if have != nil && *have < want {
					hit = append(hit, fmt.Sprintf("%s %s %s < %s", net, what, kicad.MmOfNm(*have), kicad.MmOfNm(want)))
				}
			}
			low("track", c.Track, cls.NM.Track)
			low("clearance", c.Clearance, cls.NM.Clearance)
			low("via", c.ViaDiameter, cls.NM.ViaDiameter)
			low("drill", c.ViaDrill, cls.NM.ViaDrill)
			_ = e
		}
		if len(hit) > 0 {
			add("class-override", c.Name, "pcbpilot's classes outrank this user netclass on: "+strings.Join(hit, "; "))
		}
	}
	text := string(r.data[r.dru])
	if text == "" {
		return
	}
	rules, err := kicad.ParseDRU(text)
	if err != nil {
		add("dru-unreadable", filepath.Base(r.dru), err.Error())
		return
	}
	classes := map[string]bool{}
	for _, e := range rep.Nets {
		for _, c := range strings.Split(r.rb0.Nets[e.Net].Class, ",") {
			classes[c] = true
		}
	}
	isoNets := map[string]bool{}
	for _, p := range r.in.Pairs {
		for _, d := range r.in.Domains {
			if d.ID == p.A || d.ID == p.B {
				for _, n := range d.Nets {
					isoNets[n] = true
				}
			}
			classes[domainClass(p.A)], classes[domainClass(p.B)] = true, true
		}
	}
	for _, u := range rules {
		if u.Block != "" {
			continue
		}
		if strings.HasPrefix(u.Name, kicad.DRURulesPrefix) || strings.HasPrefix(u.Name, kicad.DRURoutePrefix) {
			add("dru-name", u.Name, "a user rule outside pcbpilot's blocks carries a pcbpilot rule name")
			continue
		}
		mentions := func(names map[string]bool) bool {
			if strings.TrimSpace(u.Condition) == "" {
				return true
			}
			for n := range names {
				if strings.Contains(u.Condition, "'"+n+"'") || strings.Contains(u.Condition, "\""+n+"\"") {
					return true
				}
			}
			return false
		}
		netSet := map[string]bool{}
		for _, e := range rep.Nets {
			netSet[e.Net] = true
		}
		for _, c := range u.Constraints {
			switch c.Type {
			case "track_width":
				if !(mentions(netSet) || mentions(classes)) {
					continue
				}
				if c.Unparsed {
					add("dru-weakened", u.Name, "a user track_width rule that may cover pcbpilot nets has a value pcbpilot cannot compare; pcbpilot's later rule would override it")
					continue
				}
				for _, e := range rep.Nets {
					cls, _ := r.classOf(e.Net)
					floor := cls.NM.Min
					if floor <= 0 {
						floor = cls.NM.Track
					}
					if c.Min != nil && *c.Min > floor && (strings.TrimSpace(u.Condition) == "" || strings.Contains(u.Condition, e.Net) || mentions(map[string]bool{firstClass(r.rb0.Nets[e.Net].Class): true})) {
						add("dru-weakened", u.Name, fmt.Sprintf("the user rule asks track_width min %s; pcbpilot's later rule for %s allows %s", kicad.MmOfNm(*c.Min), e.Net, kicad.MmOfNm(floor)))
						break
					}
				}
			case "clearance", "creepage":
				if len(r.rep.Isolation) == 0 || !(mentions(isoNets) || mentions(classes)) {
					continue
				}
				for _, p := range r.rep.Isolation {
					want := p.ClearanceMm
					if c.Type == "creepage" {
						want = p.CreepageMm
					}
					if want <= 0 {
						continue
					}
					if c.Unparsed || (c.Min != nil && *c.Min > int64(math.Round(want*1e6))) {
						add("dru-weakened", u.Name, fmt.Sprintf("the user %s rule may ask more than pcbpilot's pair rule %s|%s (%g mm), which KiCad would apply instead (the later rule wins)", c.Type, p.A, p.B, want))
						break
					}
				}
			}
		}
	}
	sort.SliceStable(rep.Conflicts, func(i, j int) bool { return rep.Conflicts[i].Name < rep.Conflicts[j].Name })
}

// removeUserDRURules drops the named rules outside pcbpilot's blocks.
func removeUserDRURules(text string, names []string) (string, []string, error) {
	want := map[string]bool{}
	for _, n := range names {
		want[n] = true
	}
	user, rules, route, err := kicad.SplitDRU(text)
	if err != nil {
		return text, nil, err
	}
	var removed []string
	out := user
	for {
		changed := false
		for i := 0; i < len(out); i++ {
			if !strings.HasPrefix(out[i:], "(rule") {
				continue
			}
			end, name := sexprSpan(out, i)
			if end < 0 || !want[name] {
				continue
			}
			if end < len(out) && out[end] == '\n' {
				end++
			}
			out = out[:i] + out[end:]
			removed = append(removed, name)
			changed = true
			break
		}
		if !changed {
			break
		}
	}
	if len(removed) == 0 {
		return text, nil, nil
	}
	// Keep the owned blocks where they were: the rules block before the
	// route block, both after the user part.
	return strings.TrimRight(out, "\n") + "\n" + rules + route, removed, nil
}

// sexprSpan returns the end (exclusive) of the list starting at text[i] and
// its first quoted argument (-1 when unbalanced).
func sexprSpan(text string, i int) (int, string) {
	depth, name := 0, ""
	inStr, args := false, 0
	start := -1
	for j := i; j < len(text); j++ {
		ch := text[j]
		if inStr {
			switch ch {
			case '\\':
				j++
			case '"':
				inStr = false
				if args == 1 && name == "" && start >= 0 {
					name = text[start:j]
				}
			}
			continue
		}
		switch ch {
		case '"':
			inStr = true
			if depth == 1 {
				args++
				start = j + 1
			}
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j + 1, name
			}
		}
	}
	return -1, ""
}

// ── staged verification, readback ──────────────────────────────────────────

// verifyStaged reads the staged result back with pcbnew and proves its
// rules with kicad-cli DRC probes.
func (r *rulesRun) verifyStaged() {
	for i := range r.rep.Nets {
		e := &r.rep.Nets[i]
		e.OuterFloor = milOfNm(r.planFloor(*e, "outer"))
		if r.rb0.CopperLayers > 2 {
			e.InnerFloor = milOfNm(r.planFloor(*e, "inner"))
		}
	}
	if r.rep.checkStatus("staged-readback") != "" {
		return // the bridge or the block already failed it
	}
	chk := rulesCheck{Name: "staged-readback", Status: gate.Pass}
	rb, err := r.t.RulesRead(r.staged)
	if err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "pcbnew could not read the staged project: "+err.Error()
		r.rep.check(chk)
		return
	}
	r.rbStaged = rb
	chk.Items = append(chk.Items, r.compareToPlan(rb)...)
	// --check judges the board as it is (effective-rules probes the board);
	// the staged plan is only read back there.
	probe := rulesCheck{Status: gate.Pass, Detail: "DRC probes run on the board itself (effective-rules)"}
	if !r.o.check {
		probe = r.probeCheck(r.staged, "the staged project")
	}
	chk.Items = append(chk.Items, probe.Items...)
	chk.Info = probe.Info
	switch {
	case len(chk.Items) > 0:
		chk.Status = gate.Fail
	case probe.Status != gate.Pass:
		chk.Status = probe.Status
	}
	chk.Detail = fmt.Sprintf("pcbnew readback of the staged project: %d net(s) resolve to their class and sizes; %s", len(r.rep.Nets), probe.Detail)
	r.rep.check(chk)
}

// compareToPlan: every planned net resolves to exactly its class and nm
// sizes, every domain net carries its domain class, the pre-defined sizes
// are there, every class has a track_width minimum.
func (r *rulesRun) compareToPlan(rb *kicad.RulesReadback) []string {
	var bad []string
	for _, e := range r.rep.Nets {
		c, _ := r.classOf(e.Net)
		eff, ok := rb.Nets[e.Net]
		if !ok {
			bad = append(bad, e.Net+": not on the board KiCad loaded")
			continue
		}
		if firstClass(eff.Class) != c.Name || eff.Track != c.NM.Track || eff.Clearance != c.NM.Clearance || eff.ViaDiameter != c.NM.ViaDiameter || eff.ViaDrill != c.NM.ViaDrill {
			bad = append(bad, fmt.Sprintf("%s: KiCad resolves %s track/clearance/via/drill %s/%s/%s/%s, plan %s %s/%s/%s/%s", e.Net, eff.Class,
				kicad.MmOfNm(eff.Track), kicad.MmOfNm(eff.Clearance), kicad.MmOfNm(eff.ViaDiameter), kicad.MmOfNm(eff.ViaDrill),
				c.Name, kicad.MmOfNm(c.NM.Track), kicad.MmOfNm(c.NM.Clearance), kicad.MmOfNm(c.NM.ViaDiameter), kicad.MmOfNm(c.NM.ViaDrill)))
		}
		if r.planFloor(e, "outer") <= 0 {
			bad = append(bad, e.Net+": no track_width minimum on the outer layers")
		}
		if rb.CopperLayers > 2 && r.planFloor(e, "inner") <= 0 {
			bad = append(bad, e.Net+": no track_width minimum on the inner layers")
		}
	}
	for dom, nets := range r.full.Domains {
		cls := "PPD_" + dom
		for _, n := range nets {
			if eff, ok := rb.Nets[n]; !ok || !hasClass(eff.Class, cls) {
				bad = append(bad, fmt.Sprintf("%s: does not resolve to its insulation domain class %s (effective %q)", n, cls, rb.Nets[n].Class))
			}
		}
	}
	if s := r.full.Sizes; s != nil {
		have := map[int64]bool{}
		for _, t := range rb.Sizes.Tracks {
			have[t] = true
		}
		for _, t := range s.Tracks {
			if !have[t] {
				bad = append(bad, "pre-defined track width "+kicad.MmOfNm(t)+" missing")
			}
		}
		hv := map[[2]int64]bool{}
		for _, v := range rb.Sizes.Vias {
			hv[v] = true
		}
		for _, v := range s.Vias {
			if !hv[v] {
				bad = append(bad, fmt.Sprintf("pre-defined via %s/%s missing", kicad.MmOfNm(v[0]), kicad.MmOfNm(v[1])))
			}
		}
	}
	return bad
}

// readbackPublished reads the published project back with KiCad: the same
// resolution as the verified stage, the same .kicad_dru bytes, and the DRC
// probes on a copy of the published files.
func (r *rulesRun) readbackPublished() rulesCheck {
	chk := rulesCheck{Name: "readback", Status: gate.Pass}
	rb, err := r.t.RulesRead(r.o.pcb)
	if err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "pcbnew could not read the written project: "+err.Error()
		return chk
	}
	chk.Items = append(chk.Items, r.compareToPlan(rb)...)
	if r.rbStaged != nil {
		for _, n := range sortedKeysOf(r.rbStaged.Nets) {
			if rb.Nets[n] != r.rbStaged.Nets[n] {
				chk.Items = append(chk.Items, fmt.Sprintf("%s: KiCad resolves %+v on the written project, %+v on the verified stage", n, rb.Nets[n], r.rbStaged.Nets[n]))
			}
		}
	}
	for _, f := range []string{".kicad_pro", ".kicad_dru"} {
		want, _ := os.ReadFile(boardSibling(r.staged, f))
		have, err := os.ReadFile(boardSibling(r.o.pcb, f))
		if err != nil || string(want) != string(have) {
			chk.Items = append(chk.Items, "the written "+f+" is not the verified staged file")
		}
	}
	pub := filepath.Join(r.stage, "published", filepath.Base(r.o.pcb))
	_ = os.MkdirAll(filepath.Dir(pub), 0o755)
	if _, err := copyKicadBoard(r.o.pcb, pub); err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "copy for the DRC probes: "+err.Error()
		return chk
	}
	probe := r.probeCheck(pub, "the written project")
	chk.Items = append(chk.Items, probe.Items...)
	chk.Info = probe.Info
	switch {
	case len(chk.Items) > 0:
		chk.Status = gate.Fail
	case probe.Status != gate.Pass:
		chk.Status = probe.Status
	}
	chk.Detail = "pcbnew readback of the written project equals the verified stage; " + probe.Detail
	return chk
}

// ── DRC probes ──────────────────────────────────────────────────────────────

type rulesProbeExp struct {
	what  string
	kind  string // track_width | clearance
	ids   []int  // probe indexes
	why   string
	alias string
}

// probeCheck proves with kicad-cli DRC that the rules resolve on board (a
// scratch copy with its .kicad_pro / .kicad_dru): per planned class, a
// track just below the class's planned floor on an outer (and an inner)
// layer must be a track_width violation; per insulation pair, two tracks of
// the two domains just inside the pair clearance must be a clearance
// violation. The probes sit outside the board; only their own violations
// count.
func (r *rulesRun) probeCheck(board, what string) rulesCheck {
	chk := rulesCheck{Name: "drc-probes", Status: gate.Pass}
	ext := r.snap.extent()
	x0, y0 := ext.MinX-1500, ext.MaxY+1500
	var tracks []kicad.ProbeTrack
	var exps []rulesProbeExp
	row := 0.0
	step := 400.0
	for _, p := range r.rep.Isolation {
		step = math.Max(step, p.ClearanceMm/0.0254+200)
	}
	seen := map[string]bool{}
	for _, e := range r.rep.Nets {
		if seen[e.Class] {
			continue
		}
		seen[e.Class] = true
		for _, l := range []struct {
			kind string
			id   int
		}{{"outer", 1}, {"inner", 15}} {
			if l.id == 15 && r.rb0.CopperLayers <= 2 {
				continue
			}
			f := r.planFloor(e, l.kind)
			if f <= probeUnderNm {
				continue
			}
			y := y0 + row
			row += step
			tracks = append(tracks, kicad.ProbeTrack{Net: e.Net, Layer: l.id, StartX: x0, StartY: y, EndX: x0 + 100, EndY: y, WidthNm: f - probeUnderNm})
			exps = append(exps, rulesProbeExp{what: fmt.Sprintf("class %s (%s) on %s layers: a %s track", e.Class, e.Net, l.kind, kicad.MmOfNm(f-probeUnderNm)),
				kind: "track_width", ids: []int{len(tracks) - 1}, why: "track_width minimum " + kicad.MmOfNm(f)})
		}
	}
	domNets := map[string][]string{}
	for dom, nets := range r.full.Domains {
		domNets[dom] = nets
	}
	minW := r.rb0.Minimums.Track
	if minW < 150000 {
		minW = 150000
	}
	for _, p := range r.rep.Isolation {
		if p.ClearanceMm <= 0 {
			continue
		}
		an, bn := domNets[strings.TrimPrefix(p.ClassA, "PPD_")], domNets[strings.TrimPrefix(p.ClassB, "PPD_")]
		if len(an) == 0 || len(bn) == 0 {
			chk.Info = append(chk.Info, fmt.Sprintf("pair %s|%s: no net of one domain is on the board, nothing to probe", p.A, p.B))
			continue
		}
		gap := int64(math.Round(p.ClearanceMm*1e6)) - probeGapNm
		y := y0 + row
		row += step
		dy := float64(gap+minW) / nmPerMil
		tracks = append(tracks,
			kicad.ProbeTrack{Net: an[0], Layer: 1, StartX: x0, StartY: y, EndX: x0 + 100, EndY: y, WidthNm: minW},
			kicad.ProbeTrack{Net: bn[0], Layer: 1, StartX: x0, StartY: y + dy, EndX: x0 + 100, EndY: y + dy, WidthNm: minW})
		exps = append(exps, rulesProbeExp{what: fmt.Sprintf("pair %s|%s: %s and %s %s apart", p.A, p.B, an[0], bn[0], kicad.MmOfNm(gap)),
			kind: "clearance", ids: []int{len(tracks) - 2, len(tracks) - 1}, why: fmt.Sprintf("pair clearance %g mm", p.ClearanceMm)})
	}
	if len(tracks) == 0 {
		chk.Detail = "no probe needed"
		return chk
	}
	dir := filepath.Join(r.stage, "probe-"+fmt.Sprint(len(r.rep.Checks)))
	_ = os.MkdirAll(dir, 0o755)
	src := filepath.Join(dir, "src.kicad_pcb")
	if _, err := copyKicadBoard(board, src); err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "probe copy: "+err.Error()
		return chk
	}
	out := filepath.Join(dir, "probe.kicad_pcb")
	ids, err := r.t.RulesProbe(src, tracks, out)
	if err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "probe tracks: "+err.Error()
		return chk
	}
	// The probe board must carry exactly the project and rules under test.
	for _, ext := range []string{".kicad_pro", ".kicad_dru"} {
		if b, err := os.ReadFile(boardSibling(board, ext)); err == nil {
			_ = os.WriteFile(boardSibling(out, ext), b, 0o644)
		} else {
			_ = os.Remove(boardSibling(out, ext))
		}
	}
	drc, err := r.t.DRC(out, filepath.Join(dir, "probe-drc.json"))
	if err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "kicad-cli DRC on the probes: "+err.Error()
		return chk
	}
	hits := map[string]map[string]bool{} // rule → uuid set per violation key
	for _, v := range drc.Violations {
		for _, o := range v.Objs {
			if hits[v.Rule] == nil {
				hits[v.Rule] = map[string]bool{}
			}
			hits[v.Rule][o] = true
		}
	}
	pairHit := func(a, b string) bool {
		for _, v := range drc.Violations {
			if v.Rule != "clearance" {
				continue
			}
			ha, hb := false, false
			for _, o := range v.Objs {
				ha, hb = ha || o == a, hb || o == b
			}
			if ha && hb {
				return true
			}
		}
		return false
	}
	ok := 0
	for _, e := range exps {
		var got bool
		if e.kind == "track_width" {
			got = hits["track_width"][ids[e.ids[0]]]
		} else {
			got = pairHit(ids[e.ids[0]], ids[e.ids[1]])
		}
		if got {
			ok++
			continue
		}
		chk.Items = append(chk.Items, fmt.Sprintf("DRC probe on %s: %s raised no %s violation (%s does not resolve; is the .kicad_dru readable by KiCad?)", what, e.what, e.kind, e.why))
	}
	if len(chk.Items) > 0 {
		chk.Status = gate.Fail
	}
	chk.Detail = fmt.Sprintf("kicad-cli DRC flags %d of %d probe(s) on %s", ok, len(exps), what)
	return chk
}

// ── --check ─────────────────────────────────────────────────────────────────

// checkBoard gates the board as it is against the plan.
func (r *rulesRun) checkBoard() {
	r.effectiveRules()
	if r.o.stage == "placed" {
		for _, n := range []string{"copper-widths", "via-size", "via-current"} {
			r.rep.notApplicable(n)
		}
	} else {
		r.copperWidths()
		r.viaSize()
		r.viaCurrent()
	}
	r.placementProxy()
}

// notApplicable notes a check that does not apply (--stage placed).
func (rep *rulesReport) notApplicable(name string) {
	rep.Warnings = append(rep.Warnings, name+": not applicable (--stage placed: the board is not routed yet)")
}

// effectiveRules: what KiCad resolves on the board as it is meets the plan
// (class sizes, track_width minimums on every layer, isolation classes and
// rules), proven with DRC probes on a copy.
func (r *rulesRun) effectiveRules() {
	chk := rulesCheck{Name: "effective-rules", Status: gate.Pass}
	rb := r.rb0
	var rules []kicad.DRURule
	if t := string(r.data[r.dru]); t != "" {
		var err error
		if rules, err = kicad.ParseDRU(t); err != nil {
			chk.Items = append(chk.Items, ".kicad_dru: "+err.Error())
		}
	}
	for _, e := range r.rep.Nets {
		c, _ := r.classOf(e.Net)
		eff := rb.Nets[e.Net]
		lowv := func(what string, have, want int64) {
			if have < want {
				chk.Items = append(chk.Items, fmt.Sprintf("%s: %s %s (class %s) below the plan %s", e.Net, what, kicad.MmOfNm(have), eff.Class, kicad.MmOfNm(want)))
			}
		}
		lowv("track width", eff.Track, c.NM.Track)
		lowv("clearance", eff.Clearance, c.NM.Clearance)
		lowv("via diameter", eff.ViaDiameter, c.NM.ViaDiameter)
		lowv("via drill", eff.ViaDrill, c.NM.ViaDrill)
		for _, l := range []string{"outer", "inner"} {
			if l == "inner" && rb.CopperLayers <= 2 {
				continue
			}
			want := r.planFloor(e, l)
			have, _ := rulesTrackFloor(rules, firstClass(eff.Class), l)
			if have < want {
				what := "no track_width minimum rule"
				if have > 0 {
					what = "track_width minimum " + kicad.MmOfNm(have)
				}
				chk.Items = append(chk.Items, fmt.Sprintf("%s: %s on %s layers for class %s, plan %s (a netclass width is a routing default, not a minimum: run kicad rules --apply)", e.Net, what, l, firstClass(eff.Class), kicad.MmOfNm(want)))
			}
		}
	}
	for _, p := range r.rep.Isolation {
		name := "pcbpilot isolation " + p.ClassA + "|" + p.ClassB
		var found *kicad.DRURule
		for i := range rules {
			if rules[i].Name == name {
				found = &rules[i]
			}
		}
		if found == nil {
			chk.Items = append(chk.Items, fmt.Sprintf("pair %s|%s: no isolation rule %q in the .kicad_dru", p.A, p.B, name))
			continue
		}
		for _, c := range found.Constraints {
			want := p.ClearanceMm
			if c.Type == "creepage" {
				want = p.CreepageMm
			}
			if (c.Type == "clearance" || c.Type == "creepage") && want > 0 && (c.Min == nil || *c.Min < int64(math.Round(want*1e6))) {
				chk.Items = append(chk.Items, fmt.Sprintf("pair %s|%s: %s rule below %g mm", p.A, p.B, c.Type, want))
			}
		}
	}
	for dom, nets := range r.full.Domains {
		for _, n := range nets {
			if !hasClass(rb.Nets[n].Class, "PPD_"+dom) {
				chk.Items = append(chk.Items, fmt.Sprintf("%s: not in its insulation domain class PPD_%s (effective %q)", n, dom, rb.Nets[n].Class))
			}
		}
	}
	probe := r.probeCheck(r.stIn, "the board")
	chk.Items = append(chk.Items, probe.Items...)
	chk.Info = probe.Info
	switch {
	case len(chk.Items) > 0:
		chk.Status = gate.Fail
	case probe.Status != gate.Pass:
		chk.Status = probe.Status
	}
	chk.Detail = fmt.Sprintf("the board's netclasses and .kicad_dru rules vs the plan for %d net(s), %d pair(s); %s", len(r.rep.Nets), len(r.rep.Isolation), probe.Detail)
	r.rep.check(chk)
}

// copperWidths: every routed segment (lines and arcs, per layer) of a
// planned net is at least the layer's required width (requiredWidthMil), except an intent
// neck-down (pad of a listed ref, length ≤ max, width ≥ min); never below
// the planned floor. A narrow segment inside an own-net pour is
// cannot-check (the pour carries the current; post-layout sim judges it),
// an unrouted net is not-routed (cannot-check), never a pass.
func (r *rulesRun) copperWidths() {
	chk := rulesCheck{Name: "copper-widths", Status: gate.Pass}
	if r.snap.Copper == nil {
		chk.Status, chk.Detail = gate.CannotCheck, "the board snapshot has no copper"
		r.rep.check(chk)
		return
	}
	segs := append(append([]rulesSeg{}, r.snap.Copper.Lines...), r.snap.Copper.Arcs...)
	poured := pouredLookupKeyed(func(s string) string { return s }, r.snap.Copper.Poured)
	byNet := map[string][]rulesSeg{}
	for _, s := range segs {
		byNet[s.Net] = append(byNet[s.Net], s)
	}
	pads := map[string]int{}
	pouredNet := map[string]bool{}
	for _, p := range r.snap.Copper.Poured {
		if m, ok := p.(map[string]any); ok {
			pouredNet[fmt.Sprint(m["net"])] = true
		}
	}
	for _, c := range r.snap.Components {
		for _, p := range c.Pads {
			pads[p.Net]++
		}
	}
	var cc, fails, nets int
	for _, e := range r.rep.Nets {
		ss := byNet[e.Net]
		if len(ss) == 0 {
			if pads[e.Net] >= 2 && !pouredNet[e.Net] {
				cc++
				chk.Items = append(chk.Items, fmt.Sprintf("%s: not-routed (%d pads, no copper): copper adequacy cannot be checked", e.Net, pads[e.Net]))
			}
			continue
		}
		nets++
		n := r.in.Nets[e.Net]
		for _, s := range ss {
			inner := s.Layer >= 15
			// Copper is judged by the required width (intent / current, at
			// least the board minimum), not by a wider routing default a
			// class may carry; never below the planned DRC floor.
			bmin := milOfNm(r.rb0.Minimums.Track)
			req, floor := math.Max(e.Width.OuterMil, bmin), e.OuterFloor
			lname := "outer"
			if inner {
				req, floor, lname = math.Max(e.Width.InnerMil, bmin), e.InnerFloor, "inner"
			}
			if floor <= 0 || floor > req {
				floor = req
			}
			if s.Width+rulesTolMil >= req {
				continue
			}
			where := fmt.Sprintf("%s %s seg %s L%d %.2f mil (%s width %g mil)", e.Net, map[bool]string{true: "arc", false: "track"}[s.MidX != nil], short12(s.ID), s.Layer, s.Width, lname, req)
			if s.Width+rulesTolMil < floor {
				fails++
				chk.Items = append(chk.Items, where+fmt.Sprintf(": below the floor %g mil", floor))
				continue
			}
			if nd := r.neckdownFor(n, s); nd != "" {
				chk.Info = append(chk.Info, where+": intent neck-down "+nd)
				continue
			}
			if poured(specctra2(s)) {
				cc++
				chk.Items = append(chk.Items, where+": inside an own-net pour (zone neck: cannot-check here; post-layout sim judges the current)")
				continue
			}
			fails++
			chk.Items = append(chk.Items, where+": narrower than planned outside any intent neck-down")
		}
	}
	switch {
	case fails > 0:
		chk.Status = gate.Fail
	case cc > 0:
		chk.Status = gate.CannotCheck
	}
	chk.Detail = fmt.Sprintf("%d routed net(s): %d segment(s) below the planned width, %d cannot-check (tolerance %.2f mil)", nets, fails, cc, rulesTolMil)
	r.rep.check(chk)
}

// neckdownFor names the intent neck-down that covers s ("" = none).
func (r *rulesRun) neckdownFor(n *intentNet, s rulesSeg) string {
	if n == nil {
		return ""
	}
	for _, nd := range n.Neckdown {
		if s.Width+rulesTolMil < nd.MinWidthMil || s.length() > nd.MaxLengthMil+rulesTolMil {
			continue
		}
		for _, c := range r.snap.Components {
			if !rulesHasStr(nd.Refs, c.Designator) {
				continue
			}
			for _, p := range c.Pads {
				in := func(x, y float64) bool {
					return math.Abs(x-p.X) <= p.W/2+s.Width/2 && math.Abs(y-p.Y) <= p.H/2+s.Width/2
				}
				if p.Net == s.Net && (in(s.X1, s.Y1) || in(s.X2, s.Y2)) {
					return fmt.Sprintf("at %s (≤ %g mil long, ≥ %g mil)", c.Designator, nd.MaxLengthMil, nd.MinWidthMil)
				}
			}
		}
	}
	return ""
}

func rulesHasStr(xs []string, v string) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// viaSize: every via of a planned net is at least the planned via.
func (r *rulesRun) viaSize() {
	chk := rulesCheck{Name: "via-size", Status: gate.Pass}
	if r.snap.Copper == nil {
		chk.Status, chk.Detail = gate.CannotCheck, "the board snapshot has no copper"
		r.rep.check(chk)
		return
	}
	plan := map[string]rulesNetPlan{}
	for _, e := range r.rep.Nets {
		plan[e.Net] = e
	}
	n := 0
	for _, v := range r.snap.Copper.Vias {
		e, ok := plan[v.Net]
		if !ok {
			continue
		}
		n++
		if v.Dia+rulesTolMil < e.ViaDiaMil || v.Drill+rulesTolMil < e.ViaDrillMil {
			chk.Items = append(chk.Items, fmt.Sprintf("%s via %s at (%.1f,%.1f): %.2f/%.2f mil < plan %g/%g mil", v.Net, short12(v.ID), v.X, v.Y, v.Dia, v.Drill, e.ViaDiaMil, e.ViaDrillMil))
		}
	}
	if len(chk.Items) > 0 {
		chk.Status = gate.Fail
	}
	chk.Detail = fmt.Sprintf("%d via(s) on planned nets, %d below the planned via size", n, len(chk.Items))
	r.rep.check(chk)
}

// viaCurrent is route's via-current gate (via groups vs the intent current).
func (r *rulesRun) viaCurrent() {
	rep := &pcbCheckReport{}
	chk := rulesCheck{Name: "via-current"}
	if err := addViaCurrentFindings(rep, r.snapRaw, r.o.intent); err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, err.Error()
		r.rep.check(chk)
		return
	}
	g := intentCheckGate("via-current", "via groups vs the intent current (kicad route's gate)", rep, nil, nil)
	chk.Status, chk.Detail, chk.Items, chk.Info = gate.FromBool(g.Pass), g.Detail, g.Items, g.Info
	r.rep.check(chk)
}

// placementProxy: courtyard edge-to-edge distance of same-side footprints
// of two insulation domains vs the pair clearance. A placement proxy only
// (advisory): copper clearance and creepage are DRC's, with the pair rules.
func (r *rulesRun) placementProxy() {
	chk := rulesCheck{Name: "isolation-placement", Status: gate.Pass, Advisory: true}
	if len(r.rep.Isolation) == 0 {
		chk.Detail = "no insulation pair in the intent"
		r.rep.check(chk)
		return
	}
	domOf := map[string]string{}
	for _, d := range r.in.Domains {
		for _, n := range d.Nets {
			domOf[n] = d.ID
		}
	}
	type part struct {
		ref, side string
		box       rulesRect
		courtyard bool
		doms      map[string]bool
	}
	var parts []part
	for _, c := range r.snap.Components {
		p := part{ref: c.Designator, side: c.Side, doms: map[string]bool{}}
		switch {
		case c.Courtyard != nil:
			p.box, p.courtyard = *c.Courtyard, true
		case c.BBox != nil:
			p.box = *c.BBox
		default:
			continue
		}
		for _, pd := range c.Pads {
			if d := domOf[pd.Net]; d != "" {
				p.doms[d] = true
			}
		}
		parts = append(parts, p)
	}
	gapMil := func(a, b rulesRect) float64 {
		dx := math.Max(0, math.Max(a.MinX-b.MaxX, b.MinX-a.MaxX))
		dy := math.Max(0, math.Max(a.MinY-b.MaxY, b.MinY-a.MaxY))
		return math.Hypot(dx, dy)
	}
	checked := 0
	for _, pr := range r.rep.Isolation {
		need := pr.ClearanceMm / 0.0254
		for i, a := range parts {
			if a.doms[pr.A] && a.doms[pr.B] {
				if !rulesHasStr(pr.Bridges, a.ref) {
					chk.Items = append(chk.Items, fmt.Sprintf("%s spans %s and %s but is not declared as a bridge (intent pairs[].bridges)", a.ref, pr.A, pr.B))
				}
				continue
			}
			for _, b := range parts[i+1:] {
				if a.side != b.side || b.doms[pr.A] && b.doms[pr.B] {
					continue
				}
				if !(a.doms[pr.A] && b.doms[pr.B] || a.doms[pr.B] && b.doms[pr.A]) {
					continue
				}
				checked++
				if g := gapMil(a.box, b.box); g+rulesTolMil < need {
					src := "courtyard"
					if !a.courtyard || !b.courtyard {
						src = "bounding box (no courtyard)"
					}
					chk.Items = append(chk.Items, fmt.Sprintf("%s ↔ %s (%s side): %s edge-to-edge %.2f mm < pair %s|%s clearance %g mm",
						a.ref, b.ref, a.side, src, g*0.0254, pr.A, pr.B, pr.ClearanceMm))
				}
			}
		}
	}
	if len(chk.Items) > 0 {
		chk.Status = gate.Warn
	}
	chk.Detail = fmt.Sprintf("placement proxy, not copper clearance (DRC with the pair rules covers copper): %d same-side footprint pair(s) across insulation domains, %d closer than the pair clearance or undeclared bridges", checked, len(chk.Items))
	r.rep.check(chk)
}

// specctra2 is a rulesSeg as the shared pour lookup's track.
func specctra2(s rulesSeg) specctra.Track {
	return specctra.Track{ID: s.ID, Net: s.Net, Layer: s.Layer, X1: s.X1, Y1: s.Y1, X2: s.X2, Y2: s.Y2, Width: s.Width}
}
