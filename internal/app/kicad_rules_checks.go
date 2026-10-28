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
// from the class values alone (expectedFloor), never from the generated
// rules text: the resolved rules must reach it (compareToPlan).
func (r *rulesRun) planFloor(e rulesNetPlan, layer string) int64 {
	c, _ := r.classOf(e.Net)
	return expectedFloor(c.NM, layer)
}

// resolvedFloor is what the staged rules resolve for e's class.
func (r *rulesRun) resolvedFloor(rules []kicad.DRURule, cls string, layer string) (int64, string) {
	return rulesTrackFloor(rules, cls, layer, r.ownedNames())
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
	defer func() {
		sort.SliceStable(rep.Conflicts, func(i, j int) bool { return rep.Conflicts[i].Name < rep.Conflicts[j].Name })
	}()
	// Classes. Default counts like any user class for the values the
	// project sets explicitly (those that differ from KiCad's built-in
	// netclass defaults); unset / KiCad-generated values are ignored.
	classes := append([]kicad.ReadClass{}, r.rb0.Classes...)
	def := kicad.ReadClass{Name: "Default"}
	explicit := func(have, builtin int64) *int64 {
		if have <= 0 || have == builtin {
			return nil
		}
		v := have
		return &v
	}
	d := r.rb0.Default
	def.Track, def.Clearance = explicit(d.Track, kicadDefaultTrackNm), explicit(d.Clearance, kicadDefaultClearanceNm)
	def.ViaDiameter, def.ViaDrill = explicit(d.ViaDiameter, kicadDefaultViaDiaNm), explicit(d.ViaDrill, kicadDefaultViaDrillNm)
	for _, c := range r.rb0.Classes {
		if c.Name == "Default" {
			def.Track, def.Clearance, def.ViaDiameter, def.ViaDrill = c.Track, c.Clearance, c.ViaDiameter, c.ViaDrill
		}
	}
	classes = append(classes, def)
	// sets reports whether a class of the net's composite (other than cls)
	// sets the value v — then Default does not supply it.
	setBy := func(net, field string) bool {
		for _, cn := range strings.Split(r.rb0.Nets[net].Class, ",") {
			for _, c := range r.rb0.Classes {
				if c.Name != cn || c.Name == "Default" {
					continue
				}
				switch field {
				case "track":
					if c.Track != nil {
						return true
					}
				case "clearance":
					if c.Clearance != nil {
						return true
					}
				case "via":
					if c.ViaDiameter != nil {
						return true
					}
				case "drill":
					if c.ViaDrill != nil {
						return true
					}
				}
			}
		}
		return false
	}
	seen := map[string]bool{}
	for _, c := range classes {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
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
		for _, e := range rep.Nets {
			net := e.Net
			if c.Name == "Default" {
				// Default governs what no other class of the net sets.
			} else if !hasClass(r.rb0.Nets[net].Class, c.Name) {
				continue
			}
			cls, _ := r.classOf(net)
			low := func(what string, have *int64, want int64) {
				if have == nil || *have >= want {
					return
				}
				if c.Name == "Default" && setBy(net, what) {
					return
				}
				hit = append(hit, fmt.Sprintf("%s %s %s < %s", net, what, kicad.MmOfNm(*have), kicad.MmOfNm(want)))
			}
			low("track", c.Track, cls.NM.Track)
			low("clearance", c.Clearance, cls.NM.Clearance)
			low("via", c.ViaDiameter, cls.NM.ViaDiameter)
			low("drill", c.ViaDrill, cls.NM.ViaDrill)
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
	ours := r.ownedNames()
	var maxClr int64
	for _, e := range rep.Nets {
		cls, _ := r.classOf(e.Net)
		maxClr = max(maxClr, cls.NM.Clearance)
	}
	domNames := map[string]bool{}
	for dom, nets := range r.full.Domains {
		domNames["PPD_"+dom] = true
		for _, n := range nets {
			domNames[n] = true
			for _, c := range strings.Split(r.rb0.Nets[n].Class, ",") {
				domNames[c] = true
			}
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
		for _, c := range u.Constraints {
			why := ""
			switch c.Type {
			case "track_width":
				// Unary: per planned net and per layer group the rule can
				// govern, against that layer's floor.
				for _, e := range rep.Nets {
					if druDisjoint(u.Condition, r.netNames(e)) {
						continue
					}
					cls, _ := r.classOf(e.Net)
					for _, l := range ruleLayers(u.Layer, r.rb0.CopperLayers) {
						floor := expectedFloor(cls.NM, l)
						switch {
						case c.Unparsed:
							why = fmt.Sprintf("a user track_width rule that may cover %s on %s layers has a value pcbpilot cannot compare", e.Net, l)
						case c.Min != nil && *c.Min > floor:
							why = fmt.Sprintf("the user rule asks track_width min %s for %s on %s layers; pcbpilot's later rule allows %s", kicad.MmOfNm(*c.Min), e.Net, l, kicad.MmOfNm(floor))
						}
						if why != "" {
							break
						}
					}
					if why != "" {
						break
					}
				}
			case "clearance":
				// Binary: overlapping unless both operands are pinned
				// outside pcbpilot's nets. A custom clearance replaces the
				// planned class clearance and is replaced by a pair rule.
				if druDisjointFor(u.Condition, ours, true) {
					continue
				}
				if c.Unparsed || c.Min == nil || *c.Min < maxClr || len(rep.Isolation) > 0 {
					why = "a user clearance rule that may cover pcbpilot nets: it would replace their planned class clearance or be replaced by a pair rule"
				}
			case "creepage":
				if len(rep.Isolation) > 0 && !druDisjointFor(u.Condition, domNames, true) {
					why = "a user creepage rule that may cover pcbpilot's insulation pair nets (the later rule wins)"
				}
			case "edge_clearance":
				if len(r.ri.Edges) > 0 && !druDisjoint(u.Condition, domNames) {
					why = "a user edge_clearance rule that may cover pcbpilot's domain edge rules (the later rule wins)"
				}
			}
			if why != "" {
				add("dru-weakened", u.Name, why+" (condition "+strconvQuote(u.Condition)+")")
				break
			}
		}
	}
}

// KiCad's built-in netclass defaults (a value equal to these in Default
// is KiCad's, not the project's).
const (
	kicadDefaultTrackNm     = 200000
	kicadDefaultClearanceNm = 200000
	kicadDefaultViaDiaNm    = 600000
	kicadDefaultViaDrillNm  = 300000
)

// netNames are the names a rule must avoid to be disjoint from net e: the
// net, its planned class and every class of its current composite.
func (r *rulesRun) netNames(e rulesNetPlan) map[string]bool {
	m := map[string]bool{e.Net: true, e.Class: true}
	for _, c := range strings.Split(r.rb0.Nets[e.Net].Class, ",") {
		m[c] = true
	}
	return m
}

var reInnerLayer = regexp.MustCompile(`^In\d+\.Cu$`)

// ruleLayers are the layer groups a rule's (layer …) can govern.
func ruleLayers(layer string, copperLayers int) []string {
	inner := copperLayers > 2
	switch {
	case layer == "":
		if inner {
			return []string{"outer", "inner"}
		}
		return []string{"outer"}
	case layer == "outer" || layer == "F.Cu" || layer == "B.Cu":
		return []string{"outer"}
	case (layer == "inner" || reInnerLayer.MatchString(layer)) && inner:
		return []string{"inner"}
	}
	return nil
}

func strconvQuote(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none: every item"
	}
	return "\"" + s + "\""
}

// ownedNames are the nets and netclasses a pcbpilot rule can apply to: the
// planned nets, every class in their effective composite, the classes
// pcbpilot writes, the insulation domains' nets and classes.
func (r *rulesRun) ownedNames() map[string]bool {
	m := map[string]bool{}
	for _, e := range r.rep.Nets {
		m[e.Net] = true
		m[e.Class] = true
		for _, c := range strings.Split(r.rb0.Nets[e.Net].Class, ",") {
			m[c] = true
		}
	}
	for _, c := range r.full.Classes {
		m[c.Name] = true
	}
	for dom, nets := range r.full.Domains {
		m["PPD_"+dom] = true
		for _, n := range nets {
			m[n] = true
			for _, c := range strings.Split(r.rb0.Nets[n].Class, ",") {
				m[c] = true
			}
		}
	}
	return m
}

var reDisjointTerm = regexp.MustCompile(`^([AB])\.(?:(?:NetName|NetClass)\s*==\s*'([^']*)'|hasNetclass\('([^']*)'\)|(?:NetName|NetClass)\s*==\s*"([^"]*)")$`)

// druDisjoint: a unary condition (track_width, edge_clearance: one item,
// A) provably matches none of names — every '||' alternative has an '&&'
// term comparing a net name or netclass with a literal (no wildcard)
// outside names. Anything else (empty, property tests such as
// A.Type == 'Track', wildcards, functions) may overlap.
func druDisjoint(cond string, names map[string]bool) bool {
	return druDisjointFor(cond, names, false)
}

// druDisjointFor with binary=true is for two-item constraints (clearance,
// creepage): KiCad evaluates the rule for the pair (A, B) in both orders,
// so `A.NetName == 'VCC' && B.NetName == 'OTHER'` and a single-sided
// `A.NetName == 'OTHER'` still govern a VCC–OTHER pair. Every alternative
// must then pin BOTH operands to literals outside names.
func druDisjointFor(cond string, names map[string]bool, binary bool) bool {
	if binary {
		return druDisjointPair(cond, names, nil)
	}
	return druDisjointPair(cond, names, names)
}

// druDisjointPair: a two-item condition provably never governs a pair of
// one item named in x and one in y (in either order: KiCad evaluates both).
// nil y is "any item": then both operands must be pinned outside x. A
// unary use passes y = x (only A matters: a B term is ignored).
func druDisjointPair(cond string, x, y map[string]bool) bool {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return false
	}
	trim := func(s string) string {
		s = strings.TrimSpace(s)
		for strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") && balanced(s[1:len(s)-1]) {
			s = strings.TrimSpace(s[1 : len(s)-1])
		}
		return s
	}
	// outside reports an operand pinned to a literal not in set (a nil set
	// contains everything).
	for _, alt := range splitTop(trim(cond), "||") {
		lits := map[string][]string{}
		for _, term := range splitTop(trim(alt), "&&") {
			m := reDisjointTerm.FindStringSubmatch(trim(term))
			if m == nil {
				continue
			}
			if lit := m[2] + m[3] + m[4]; lit != "" && !strings.ContainsAny(lit, "*?") {
				lits[m[1]] = append(lits[m[1]], lit)
			}
		}
		outside := func(op string, set map[string]bool) bool {
			if set == nil {
				return false
			}
			for _, l := range lits[op] {
				if !set[l] {
					return true
				}
			}
			return false
		}
		unary := y != nil && sameSet(x, y)
		if unary {
			if !outside("A", x) && !outside("B", x) {
				return false
			}
			continue
		}
		if y == nil {
			if !(outside("A", x) && outside("B", x)) {
				return false
			}
			continue
		}
		// (A=x-item, B=y-item) and (A=y-item, B=x-item) must both be excluded.
		if !((outside("A", x) || outside("B", y)) && (outside("A", y) || outside("B", x))) {
			return false
		}
	}
	return true
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}

// splitTop splits s on op outside parentheses and quotes.
func splitTop(s, op string) []string {
	var out []string
	depth, start := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '\'' || ch == '"':
			quote = ch
		case ch == '(':
			depth++
		case ch == ')':
			depth--
		case depth == 0 && strings.HasPrefix(s[i:], op):
			out = append(out, s[start:i])
			i += len(op) - 1
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func balanced(s string) bool {
	d := 0
	var quote byte
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			}
		case ch == '\'' || ch == '"':
			quote = ch
		case ch == '(':
			d++
		case ch == ')':
			d--
			if d < 0 {
				return false
			}
		}
	}
	return d == 0
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
	if b, u := r.clearanceEdgeProblems(r.druRule); len(b)+len(u) > 0 {
		chk.Items = append(chk.Items, append(b, u...)...)
	}
	// --check judges the board as it is (effective-rules probes the board);
	// the staged plan is only read back there.
	probe := rulesCheck{Status: gate.Pass, Detail: "DRC probes run on the board itself (effective-rules)"}
	if !r.o.check {
		probe = r.probeCheck(r.staged, "the staged project")
	}
	chk.Info = append(chk.Info, probe.Info...)
	if probe.Status == gate.Fail {
		chk.Items = append(chk.Items, probe.Items...)
	} else {
		chk.Info = append(chk.Info, probe.Items...) // cannot-check: why, not a failure
	}
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
		// Every enabled copper layer on its own: group rules and rules
		// naming one layer, last match wins.
		for _, l := range copperLayerNames(rb.CopperLayers) {
			want := r.planFloor(e, layerGroup(l))
			have, by := r.resolvedFloor(r.druRule, c.Name, l)
			if have < want {
				bad = append(bad, fmt.Sprintf("%s: the staged rules resolve track_width min %s on %s (rule %q), plan %s", e.Net, kicad.MmOfNm(have), l, by, kicad.MmOfNm(want)))
			}
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
	chk.Info = append(chk.Info, probe.Info...)
	if probe.Status == gate.Fail {
		chk.Items = append(chk.Items, probe.Items...)
	} else {
		chk.Info = append(chk.Info, probe.Items...) // cannot-check: why, not a failure
	}
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
	src   string // dru (width, insulation pair) | class (class clearance)
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
	for _, c := range r.full.ClassesNM {
		step = math.Max(step, float64(c.NM.Clearance)/nmPerMil+200)
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
	minW := r.rb0.Minimums.Track
	if minW < 150000 {
		minW = 150000
	}
	// Ordinary clearance: per planned class, a track of the class next to a
	// track of an unplanned net with the lowest clearance, just inside the
	// class clearance.
	partner, pclr := "", int64(-1)
	inPlan := map[string]bool{}
	for _, e := range r.rep.Nets {
		inPlan[e.Net] = true
	}
	for _, nets := range r.full.Domains {
		for _, n := range nets {
			inPlan[n] = true
		}
	}
	for _, n := range sortedKeysOf(r.rb0.Nets) {
		if eff := r.rb0.Nets[n]; !inPlan[n] && (pclr < 0 || eff.Clearance < pclr) {
			partner, pclr = n, eff.Clearance
		}
	}
	seenClr := map[string]bool{}
	for _, e := range r.rep.Nets {
		c, _ := r.classOf(e.Net)
		if seenClr[c.Name] {
			continue
		}
		seenClr[c.Name] = true
		if partner == "" {
			chk.Info = append(chk.Info, "no unplanned net on the board to probe class clearances against")
			break
		}
		gap := c.NM.Clearance - probeGapNm
		if gap <= 0 {
			continue
		}
		y := y0 + row
		row += step
		dy := float64(gap+minW) / nmPerMil
		tracks = append(tracks,
			kicad.ProbeTrack{Net: e.Net, Layer: 1, StartX: x0, StartY: y, EndX: x0 + 100, EndY: y, WidthNm: minW},
			kicad.ProbeTrack{Net: partner, Layer: 1, StartX: x0, StartY: y + dy, EndX: x0 + 100, EndY: y + dy, WidthNm: minW})
		exps = append(exps, rulesProbeExp{what: fmt.Sprintf("class %s clearance: %s and %s %s apart", c.Name, e.Net, partner, kicad.MmOfNm(gap)),
			kind: "clearance", ids: []int{len(tracks) - 2, len(tracks) - 1}, why: "class clearance " + kicad.MmOfNm(c.NM.Clearance), src: "class"})
	}
	domNets := map[string][]string{}
	for dom, nets := range r.full.Domains {
		domNets[dom] = nets
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
	r.probeRuns++
	dir := filepath.Join(r.stage, fmt.Sprintf("probe-%d", r.probeRuns))
	// Three DRC runs on the same probes: with the rules under test, and two
	// negative controls, one per source of a pcbpilot rule. The DRU control
	// keeps the project (classes) and removes pcbpilot's .kicad_dru blocks
	// (the user part kept): it attributes width and insulation-pair probes
	// to the .kicad_dru. The class control keeps the .kicad_dru and removes
	// pcbpilot's netclasses / patterns from the project: it attributes the
	// class-clearance probes to the netclasses. A probe proves its rule only
	// when it is flagged with the rules and not in its control; one flagged
	// in both is enforced by something else (the board minimum, a class)
	// and proves nothing. The .kicad_dru counts as loaded only through a
	// DRU-attributable probe.
	user := ""
	if b, err := os.ReadFile(boardSibling(board, ".kicad_dru")); err == nil {
		u, _, _, err := kicad.SplitDRU(strings.ReplaceAll(string(b), "\r\n", "\n"))
		if err != nil {
			chk.Status, chk.Detail = gate.CannotCheck, "probe control: "+err.Error()
			return chk
		}
		user = u
	}
	pro, _ := os.ReadFile(boardSibling(board, ".kicad_pro"))
	var ctlPro []byte
	if pro != nil {
		var err error
		if ctlPro, err = stripOwnedProject(pro); err != nil {
			chk.Status, chk.Detail = gate.CannotCheck, "probe control project: "+err.Error()
			return chk
		}
	}
	druText, _ := os.ReadFile(boardSibling(board, ".kicad_dru"))
	run := func(sub string, pro, dru []byte) (*kicad.DRCReport, []string, error) {
		d := filepath.Join(dir, sub)
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, nil, err
		}
		src := filepath.Join(d, "src.kicad_pcb")
		if _, err := copyKicadBoard(board, src); err != nil {
			return nil, nil, err
		}
		out := filepath.Join(d, "probe.kicad_pcb")
		ids, err := r.t.RulesProbe(src, tracks, out)
		if err != nil {
			return nil, nil, err
		}
		// The probe board carries exactly the project and rules of this run.
		_ = os.Remove(boardSibling(out, ".kicad_pro"))
		_ = os.Remove(boardSibling(out, ".kicad_dru"))
		if pro != nil {
			_ = os.WriteFile(boardSibling(out, ".kicad_pro"), pro, 0o644)
		}
		if strings.TrimSpace(string(dru)) != "" {
			_ = os.WriteFile(boardSibling(out, ".kicad_dru"), dru, 0o644)
		}
		rep, err := r.t.DRC(out, filepath.Join(d, "probe-drc.json"))
		return rep, ids, err
	}
	drc, ids, err := run("rules", pro, druText)
	if err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "kicad-cli DRC on the probes: "+err.Error()
		return chk
	}
	ctlDRU, dids, err := run("control-dru", pro, []byte(user))
	if err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "kicad-cli DRC on the .kicad_dru control probes: "+err.Error()
		return chk
	}
	ctlCls, cids, err := run("control-classes", ctlPro, druText)
	if err != nil {
		chk.Status, chk.Detail = gate.CannotCheck, "kicad-cli DRC on the netclass control probes: "+err.Error()
		return chk
	}
	flagged := func(rep *kicad.DRCReport, ids []string, e rulesProbeExp) bool {
		for _, v := range rep.Violations {
			if v.Rule != e.kind {
				continue
			}
			hit := 0
			for _, want := range e.ids {
				for _, o := range v.Objs {
					if o == ids[want] {
						hit++
						break
					}
				}
			}
			if hit == len(e.ids) {
				return true
			}
		}
		return false
	}
	druAttr, clsAttr, druProbes, failed := 0, 0, 0, 0
	for _, e := range exps {
		withRules := flagged(drc, ids, e)
		var without bool
		if e.src == "class" {
			without = flagged(ctlCls, cids, e)
		} else {
			druProbes++
			without = flagged(ctlDRU, dids, e)
		}
		switch {
		case !withRules:
			failed++
			chk.Items = append(chk.Items, fmt.Sprintf("DRC probe on %s: %s raised no %s violation (%s does not resolve; is the .kicad_dru readable by KiCad?)", what, e.what, e.kind, e.why))
		case without:
			chk.Info = append(chk.Info, fmt.Sprintf("probe %s: also flagged in the %s control (the board minimum or another rule enforces it): not attributable to pcbpilot's %s", e.what,
				map[bool]string{true: "netclass", false: ".kicad_dru"}[e.src == "class"], map[bool]string{true: "netclasses", false: ".kicad_dru"}[e.src == "class"]))
		case e.src == "class":
			clsAttr++
		default:
			druAttr++
		}
	}
	switch {
	case len(chk.Items) > 0:
		chk.Status = gate.Fail
	case druProbes > 0 && druAttr == 0:
		chk.Status = gate.CannotCheck
		chk.Items = append(chk.Items, "no DRC probe is attributable to pcbpilot's .kicad_dru rules (every width / pair probe is also flagged with the .kicad_dru blocks removed): that KiCad loads and applies the .kicad_dru is not proven, so no .kicad_dru rule (minimum widths, insulation clearance / creepage, edge) counts as verified")
	}
	chk.Detail = fmt.Sprintf("kicad-cli DRC on %s: %d of %d .kicad_dru probe(s) attributable to the .kicad_dru, %d of %d class-clearance probe(s) attributable to the netclasses (flagged with the rules, not in their control), %d failed",
		what, druAttr, druProbes, clsAttr, len(exps)-druProbes, failed)
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
		// Every enabled copper layer on its own (a user rule limited to
		// B.Cu or In2.Cu can weaken just that layer).
		for _, l := range copperLayerNames(rb.CopperLayers) {
			want := r.planFloor(e, layerGroup(l))
			have, by := r.resolvedFloor(rules, firstClass(eff.Class), l)
			if have < want {
				what := "no track_width minimum rule"
				if have > 0 || by != "" {
					what = fmt.Sprintf("track_width minimum %s (rule %q)", kicad.MmOfNm(have), by)
				}
				chk.Items = append(chk.Items, fmt.Sprintf("%s: %s on %s for class %s, plan %s (a netclass width is a routing default, not a minimum: run kicad rules --apply)", e.Net, what, l, firstClass(eff.Class), kicad.MmOfNm(want)))
			}
		}
	}
	for _, p := range r.rep.Isolation {
		chk.Items = append(chk.Items, r.isolationRuleProblems(rules, p)...)
	}
	bad, unknown := r.clearanceEdgeProblems(rules)
	chk.Items = append(chk.Items, bad...)
	var cannot []string
	cannot = append(cannot, unknown...)
	for dom, nets := range r.full.Domains {
		for _, n := range nets {
			if !hasClass(rb.Nets[n].Class, "PPD_"+dom) {
				chk.Items = append(chk.Items, fmt.Sprintf("%s: not in its insulation domain class PPD_%s (effective %q)", n, dom, rb.Nets[n].Class))
			}
		}
	}
	probe := r.probeCheck(r.stIn, "the board")
	chk.Info = append(chk.Info, probe.Info...)
	if probe.Status == gate.Fail {
		chk.Items = append(chk.Items, probe.Items...)
	} else {
		chk.Info = append(chk.Info, probe.Items...) // cannot-check: why, not a failure
	}
	chk.Info = append(chk.Info, cannot...)
	switch {
	case len(chk.Items) > 0:
		chk.Status = gate.Fail
	case len(cannot) > 0:
		chk.Status = gate.CannotCheck
	case probe.Status != gate.Pass:
		chk.Status = probe.Status
	}
	chk.Detail = fmt.Sprintf("the board's netclasses and .kicad_dru rules vs the plan for %d net(s), %d pair(s), %d domain edge rule(s); %s", len(r.rep.Nets), len(r.rep.Isolation), len(r.ri.Edges), probe.Detail)
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
			switch {
			case pouredNet[e.Net]:
				// Copper only in pours (or a pour island): neither the pour's
				// neck widths nor that it joins the pads is judged here.
				cc++
				chk.Items = append(chk.Items, fmt.Sprintf("%s: zone-only (%d pad(s), no track, copper only in pours): pour widths and connectivity are not verified here (post-layout sim / DRC unconnected judge them): cannot-check", e.Net, pads[e.Net]))
			case pads[e.Net] >= 2:
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
	if err := addViaCurrentFindings(rep, r.snapRaw, r.intentCopy); err != nil {
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

// isoCondition is the exact condition bridge netclasses writes for a pair.
func isoCondition(a, b string) string {
	return fmt.Sprintf("(A.hasNetclass('%s') && B.hasNetclass('%s')) || (A.hasNetclass('%s') && B.hasNetclass('%s'))", a, b, b, a)
}

// isolationRuleProblems: the pair's rule exists with exactly the pair
// condition, on every layer, carries every requested constraint at its
// value (clearance; creepage when the pair has one), and no later rule
// that may match the pair's items sets clearance or creepage again (KiCad
// applies the last matching rule).
func (r *rulesRun) isolationRuleProblems(rules []kicad.DRURule, p rulesIsoPlan) []string {
	var bad []string
	name := "pcbpilot isolation " + p.ClassA + "|" + p.ClassB
	cond := isoCondition(p.ClassA, p.ClassB)
	at := -1
	for i, u := range rules {
		if u.Name == name {
			at = i
		}
	}
	if at < 0 {
		return []string{fmt.Sprintf("pair %s|%s: no isolation rule %q in the .kicad_dru", p.A, p.B, name)}
	}
	u := rules[at]
	if u.Condition != cond {
		bad = append(bad, fmt.Sprintf("pair %s|%s: rule %q has condition %q, not the pair condition %q", p.A, p.B, name, u.Condition, cond))
	}
	if u.Layer != "" {
		bad = append(bad, fmt.Sprintf("pair %s|%s: rule %q is limited to layer %s", p.A, p.B, name, u.Layer))
	}
	for _, want := range []struct {
		typ string
		mm  float64
	}{{"clearance", p.ClearanceMm}, {"creepage", p.CreepageMm}} {
		if want.mm <= 0 {
			continue
		}
		ok := false
		for _, c := range u.Constraints {
			if c.Type == want.typ && c.Min != nil && *c.Min >= int64(math.Round(want.mm*1e6)) {
				ok = true
			}
		}
		if !ok {
			bad = append(bad, fmt.Sprintf("pair %s|%s: rule %q has no %s min ≥ %g mm", p.A, p.B, name, want.typ, want.mm))
		}
	}
	side := func(cls string) map[string]bool {
		m := map[string]bool{cls: true}
		for _, n := range r.full.Domains[strings.TrimPrefix(cls, "PPD_")] {
			m[n] = true
			for _, c := range strings.Split(r.rb0.Nets[n].Class, ",") {
				m[c] = true
			}
		}
		return m
	}
	sa, sb := side(p.ClassA), side(p.ClassB)
	for _, later := range rules[at+1:] {
		if druDisjointPair(later.Condition, sa, sb) {
			continue
		}
		for _, c := range later.Constraints {
			if c.Type == "clearance" || c.Type == "creepage" {
				bad = append(bad, fmt.Sprintf("pair %s|%s: the later rule %q may set %s for the pair's items and would win over %q", p.A, p.B, later.Name, c.Type, name))
				break
			}
		}
	}
	return bad
}

// clearanceEdgeProblems resolves the ordinary clearance of every planned
// net and every planned domain edge distance through KiCad's custom-rule
// semantics (a custom rule replaces the netclass value; the last matching
// rule wins). bad: a rule that provably lowers a value below the plan, or
// a missing / weakened pcbpilot edge rule; unknown: a rule that may govern
// a planned item with a value that cannot be compared (cannot-check).
func (r *rulesRun) clearanceEdgeProblems(rules []kicad.DRURule) (bad, unknown []string) {
	for _, e := range r.rep.Nets {
		cls, _ := r.classOf(e.Net)
		names := r.netNames(e)
		for _, u := range rules {
			if strings.HasPrefix(u.Name, "pcbpilot isolation ") && u.Block == "route" {
				continue // pair-only; judged by isolationRuleProblems
			}
			if druDisjointFor(u.Condition, names, true) {
				continue
			}
			for _, c := range u.Constraints {
				if c.Type != "clearance" {
					continue
				}
				switch {
				case c.Unparsed || c.Min == nil:
					unknown = append(unknown, fmt.Sprintf("%s: rule %q may set its clearance with a value pcbpilot cannot compare", e.Net, u.Name))
				case *c.Min < cls.NM.Clearance:
					bad = append(bad, fmt.Sprintf("%s: rule %q may lower its clearance to %s (plan %s; a custom rule replaces the netclass clearance)", e.Net, u.Name, kicad.MmOfNm(*c.Min), kicad.MmOfNm(cls.NM.Clearance)))
				}
			}
		}
	}
	for _, ed := range r.ri.Edges {
		dom := reDomainChars.ReplaceAllString(fmt.Sprint(ed["domain"]), "_")
		cls := "PPD_" + dom
		milv, _ := ed["mil"].(float64)
		want := int64(math.Round(milv*0.0254*1e4) * 100) // the bridge writes 4-decimal mm
		name := "pcbpilot edge " + cls
		cond := "A.hasNetclass('" + cls + "')"
		names := map[string]bool{cls: true}
		for _, n := range r.full.Domains[dom] {
			names[n] = true
			for _, c := range strings.Split(r.rb0.Nets[n].Class, ",") {
				names[c] = true
			}
		}
		at := -1
		for i, u := range rules {
			if u.Name == name && u.Block != "" {
				at = i
			}
		}
		if at < 0 {
			bad = append(bad, fmt.Sprintf("domain %s: no edge rule %q in the .kicad_dru", dom, name))
			continue
		}
		u := rules[at]
		ok := u.Condition == cond && u.Layer == ""
		for _, c := range u.Constraints {
			if c.Type == "edge_clearance" && (c.Min == nil || *c.Min < want) {
				ok = false
			}
		}
		if !ok || !hasConstraint(u, "edge_clearance") {
			bad = append(bad, fmt.Sprintf("domain %s: rule %q is not edge_clearance min %s on %q", dom, name, kicad.MmOfNm(want), cond))
		}
		for _, later := range rules[at+1:] {
			if druDisjoint(later.Condition, names) {
				continue
			}
			for _, c := range later.Constraints {
				if c.Type != "edge_clearance" {
					continue
				}
				switch {
				case c.Unparsed || c.Min == nil:
					unknown = append(unknown, fmt.Sprintf("domain %s: the later rule %q may set edge_clearance with a value pcbpilot cannot compare", dom, later.Name))
				case *c.Min < want:
					bad = append(bad, fmt.Sprintf("domain %s: the later rule %q may lower edge_clearance to %s (plan %s)", dom, later.Name, kicad.MmOfNm(*c.Min), kicad.MmOfNm(want)))
				}
			}
		}
	}
	return bad, unknown
}

func hasConstraint(u kicad.DRURule, typ string) bool {
	for _, c := range u.Constraints {
		if c.Type == typ {
			return true
		}
	}
	return false
}

// stripOwnedProject is a .kicad_pro without pcbpilot's netclasses and
// their pattern assignments (the negative-control project of the probes).
func stripOwnedProject(b []byte) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	ns, _ := m["net_settings"].(map[string]any)
	if ns == nil {
		return b, nil
	}
	if cs, ok := ns["classes"].([]any); ok {
		var keep []any
		for _, c := range cs {
			if cm, ok := c.(map[string]any); ok && reOwnedClass.MatchString(fmt.Sprint(cm["name"])) {
				continue
			}
			keep = append(keep, c)
		}
		ns["classes"] = keep
	}
	if ps, ok := ns["netclass_patterns"].([]any); ok {
		var keep []any
		for _, p := range ps {
			if pm, ok := p.(map[string]any); ok && reOwnedClass.MatchString(fmt.Sprint(pm["netclass"])) {
				continue
			}
			keep = append(keep, p)
		}
		ns["netclass_patterns"] = keep
	}
	return json.MarshalIndent(m, "", "  ")
}
