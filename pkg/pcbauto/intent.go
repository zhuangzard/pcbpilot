package pcbauto

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

// Intent is the part of intent.json (`pcbpilot intent derive`) the engine
// consumes: per-net electrical intent, net classes, voltage domains and the
// insulation required between them. Where the intent declares a value it
// wins over --power / --sim / inference (NetPlan.Source "intent").
type Intent struct {
	Standard   safety.Standard       `json:"standard"`
	Domains    []IntentDomain        `json:"domains"`
	Nets       map[string]*IntentNet `json:"nets"`
	Pairs      []IntentPair          `json:"pairs"`
	NetClasses []IntentNetClass      `json:"netClasses"`
	Findings   []IntentFinding       `json:"findings"`
}

// IntentDomain is one voltage/reference domain.
type IntentDomain struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"` // SELV | hazardous | mains | patient | floating | isolated-secondary
	Nets         []string `json:"nets"`
	Reference    string   `json:"reference"`
	WorkingVrms  float64  `json:"workingVrms"`
	WorkingVpeak float64  `json:"workingVpeak"`
}

// IntentVoltage is a net's voltage envelope.
type IntentVoltage struct {
	Nom  float64 `json:"nom"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Peak float64 `json:"peak"`
}

// IntentPin is one pin's current on a net.
type IntentPin struct {
	Ref      string  `json:"ref"`
	Pin      string  `json:"pin"`
	CurrentA float64 `json:"currentA"`
	Dir      string  `json:"dir"`
}

// IntentWidth is a net's declared width (mil).
type IntentWidth struct {
	Outer float64 `json:"outer"`
	Inner float64 `json:"inner"`
	Min   float64 `json:"min"`
}

// IntentNet is the per-net electrical intent.
type IntentNet struct {
	Role              string        `json:"role"`
	Domain            string        `json:"domain"`
	Voltage           IntentVoltage `json:"voltage"`
	CurrentA          float64       `json:"currentA"`
	Pins              []IntentPin   `json:"pins"`
	WidthMil          IntentWidth   `json:"widthMil"`
	ViasPerTransition int           `json:"viasPerTransition"`
	ClearanceMil      float64       `json:"clearanceMil"`
	ImpedanceOhm      float64       `json:"impedanceOhm"`
	DiffPair          string        `json:"diffPair"`
	NetClass          string        `json:"netClass"`
	Why               []string      `json:"why"`
	// FloatsOn / RelVoltage: a gate-drive net riding on a switch node and
	// its voltage relative to it (intent derive).
	FloatsOn   string         `json:"floatsOn,omitempty"`
	RelVoltage *IntentVoltage `json:"relVoltage,omitempty"`
}

// IntentPair is the insulation between two domains ("domain:ID").
type IntentPair struct {
	A            string   `json:"a"`
	B            string   `json:"b"`
	WorkingVrms  float64  `json:"workingVrms"`
	WorkingVpeak float64  `json:"workingVpeak"`
	Insulation   string   `json:"insulation"`
	ClearanceMm  float64  `json:"clearanceMm"`
	CreepageMm   float64  `json:"creepageMm"`
	SlotRequired bool     `json:"slotRequired"`
	SlotWidthMm  float64  `json:"slotWidthMm"`
	StandardRef  string   `json:"standardRef"`
	Why          []string `json:"why"`
	// Optional refinements (not in the base contract).
	MOP       string  `json:"mop,omitempty"`
	MOPCount  int     `json:"mopCount,omitempty"`
	Transient string  `json:"transient,omitempty"`
	MainsVrms float64 `json:"mainsVrms,omitempty"`
}

// IntentNetClass is a named rule set for a group of nets.
type IntentNetClass struct {
	Name         string   `json:"name"`
	Nets         []string `json:"nets"`
	TrackMil     float64  `json:"trackMil"`
	ClearanceMil float64  `json:"clearanceMil"`
	ViaDrillMil  float64  `json:"viaDrillMil"`
	ViaDiaMil    float64  `json:"viaDiaMil"`
}

// IntentFinding is a diagnostic the intent derivation raised.
type IntentFinding struct {
	Severity   string   `json:"severity"`
	Kind       string   `json:"kind"`
	Message    string   `json:"message"`
	Refs       []string `json:"refs"`
	Nets       []string `json:"nets"`
	Suggestion string   `json:"suggestion"`
}

// ParseIntent decodes and validates intent.json.
func ParseIntent(raw []byte) (*Intent, error) {
	var in Intent
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("intent: %w", err)
	}
	seen := map[string]bool{}
	for _, d := range in.Domains {
		if d.ID == "" {
			return nil, fmt.Errorf("intent: domain without id")
		}
		if seen[d.ID] {
			return nil, fmt.Errorf("intent: duplicate domain %q", d.ID)
		}
		seen[d.ID] = true
	}
	for i, p := range in.Pairs {
		a, b := pairDomain(p.A), pairDomain(p.B)
		if a == "" || b == "" || a == b {
			return nil, fmt.Errorf("intent: pairs[%d] needs two different domains (a=%q b=%q)", i, p.A, p.B)
		}
		if !seen[a] || !seen[b] {
			return nil, fmt.Errorf("intent: pairs[%d] names an undeclared domain (%q, %q)", i, a, b)
		}
		for _, v := range []float64{p.WorkingVrms, p.WorkingVpeak, p.ClearanceMm, p.CreepageMm, p.SlotWidthMm} {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return nil, fmt.Errorf("intent: pairs[%d] has a negative or non-finite value", i)
			}
		}
	}
	return &in, nil
}

// pairDomain strips the "domain:" prefix of a pair end.
func pairDomain(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, ":"); i >= 0 && strings.EqualFold(s[:i], "domain") {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

// ---- isolation rules --------------------------------------------------------

// IsoPair is the resolved insulation requirement between two domains.
type IsoPair struct {
	A            string        `json:"a"`
	B            string        `json:"b"`
	WorkingVrms  float64       `json:"workingVrms"`
	WorkingVpeak float64       `json:"workingVpeak"`
	Insulation   string        `json:"insulation"`
	ClearanceMil float64       `json:"clearanceMil"`
	CreepageMil  float64       `json:"creepageMil"`
	SlotWidthMil float64       `json:"slotWidthMil"`
	Source       string        `json:"source"` // intent | computed
	Ref          string        `json:"ref"`
	Why          []string      `json:"why,omitempty"`
	Computed     safety.Result `json:"computed"`
}

// IsoRules are the domain-to-domain insulation rules of an intent.
type IsoRules struct {
	Standard  safety.Standard   `json:"standard"`
	Domains   []IntentDomain    `json:"domains"`
	NetDomain map[string]string `json:"netDomain"`
	Pairs     []*IsoPair        `json:"pairs"`
	Notes     []string          `json:"notes,omitempty"`

	byKey map[[2]string]*IsoPair
	kind  map[string]string
}

func isoKey(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// Pair returns the requirement between two domains (nil = none).
func (s *IsoRules) Pair(a, b string) *IsoPair {
	if s == nil || a == "" || b == "" || a == b {
		return nil
	}
	return s.byKey[isoKey(a, b)]
}

// NetPair returns the requirement between the domains of two nets.
func (s *IsoRules) NetPair(netA, netB string) *IsoPair {
	if s == nil {
		return nil
	}
	return s.Pair(s.NetDomain[netA], s.NetDomain[netB])
}

// MaxClearanceMil is the largest pair clearance / creepage (DRC reach).
func (s *IsoRules) MaxClearanceMil() float64 {
	m := 0.0
	if s != nil {
		for _, p := range s.Pairs {
			m = math.Max(m, math.Max(p.ClearanceMil, p.CreepageMil))
		}
	}
	return m
}

// Kind returns a domain's declared kind.
func (s *IsoRules) Kind(d string) string {
	if s == nil {
		return ""
	}
	return s.kind[d]
}

func hazardousKind(k string) bool {
	k = strings.ToLower(k)
	return k == "mains" || k == "hazardous"
}

// buildIsoRules resolves an intent against the board's nets.
func buildIsoRules(b *Board, in *Intent) *IsoRules {
	s := &IsoRules{Standard: in.Standard, Domains: in.Domains, NetDomain: map[string]string{}, byKey: map[[2]string]*IsoPair{}, kind: map[string]string{}}
	boardNet := map[string]string{} // upper → board spelling
	for _, n := range b.Nets() {
		boardNet[upper(n.Name)] = n.Name
	}
	resolve := func(name string) string {
		if bn, ok := boardNet[upper(name)]; ok {
			return bn
		}
		return ""
	}
	dom := map[string]*IntentDomain{}
	for i := range in.Domains {
		d := &in.Domains[i]
		dom[d.ID] = d
		s.kind[d.ID] = d.Kind
		for _, n := range d.Nets {
			if bn := resolve(n); bn != "" {
				s.NetDomain[bn] = d.ID
			}
		}
	}
	for name, n := range in.Nets {
		if n == nil || n.Domain == "" || dom[n.Domain] == nil {
			continue
		}
		if bn := resolve(name); bn != "" {
			s.NetDomain[bn] = n.Domain
		}
	}
	for _, ip := range in.Pairs {
		a, bb := pairDomain(ip.A), pairDomain(ip.B)
		da, db := dom[a], dom[bb]
		sp := safety.Pair{A: a, B: bb, WorkingVrms: ip.WorkingVrms, WorkingVpeak: ip.WorkingVpeak,
			Insulation: ip.Insulation, MOP: ip.MOP, MOPCount: ip.MOPCount, Transient: ip.Transient, MainsVrms: ip.MainsVrms}
		if sp.WorkingVrms == 0 && sp.WorkingVpeak == 0 {
			sp.WorkingVrms = math.Max(da.WorkingVrms, db.WorkingVrms)
			sp.WorkingVpeak = math.Max(da.WorkingVpeak, db.WorkingVpeak)
		}
		if sp.Transient == "" {
			sp.Transient = pairTransient(da.Kind, db.Kind)
		}
		res := safety.Distances(sp, in.Standard)
		p := &IsoPair{A: a, B: bb, WorkingVrms: sp.WorkingVrms, WorkingVpeak: sp.WorkingVpeak, Insulation: res.Insulation,
			Source: "computed", Ref: res.Ref, Computed: res}
		clr, creep := res.ClearanceMm, res.CreepageMm
		if ip.ClearanceMm > 0 || ip.CreepageMm > 0 {
			p.Source = "intent"
			if ip.StandardRef != "" {
				p.Ref = ip.StandardRef
			}
			if ip.ClearanceMm > 0 {
				if ip.ClearanceMm+1e-9 < clr {
					s.Notes = append(s.Notes, sprintf("pair %s|%s: intent clearance %.2f mm is below the computed %.2f mm (%s) — intent kept, verify", a, bb, ip.ClearanceMm, clr, res.Ref))
				}
				clr = ip.ClearanceMm
			}
			if ip.CreepageMm > 0 {
				if ip.CreepageMm+1e-9 < creep {
					s.Notes = append(s.Notes, sprintf("pair %s|%s: intent creepage %.2f mm is below the computed %.2f mm (%s) — intent kept, verify", a, bb, ip.CreepageMm, creep, res.Ref))
				}
				creep = ip.CreepageMm
			}
		}
		if ip.Insulation != "" {
			p.Insulation = safety.NormInsulation(ip.Insulation)
		}
		creep = math.Max(creep, clr)
		p.ClearanceMil, p.CreepageMil = mmToMil(clr), mmToMil(creep)
		sw := res.SlotWidthMm
		if ip.SlotWidthMm > sw {
			sw = ip.SlotWidthMm
		}
		if sw <= 0 && p.Insulation != "functional" {
			sw = safety.SlotMinWidth(in.Standard.PollutionDegree)
		}
		p.SlotWidthMil = mmToMil(sw)
		p.Why = append(p.Why, ip.Why...)
		p.Why = append(p.Why, res.Why...)
		p.Why = append(p.Why, res.Warnings...)
		if old := s.byKey[isoKey(a, bb)]; old != nil {
			// Duplicate declarations: keep the stricter one.
			if p.CreepageMil <= old.CreepageMil && p.ClearanceMil <= old.ClearanceMil {
				continue
			}
			*old = *p
			continue
		}
		s.byKey[isoKey(a, bb)] = p
		s.Pairs = append(s.Pairs, p)
	}
	return s
}

// pairTransient infers the transient regime from the domain kinds.
func pairTransient(ka, kb string) string {
	ka, kb = strings.ToLower(ka), strings.ToLower(kb)
	if ka == "mains" || kb == "mains" {
		return "mains"
	}
	secondary := func(k string) bool {
		return k == "selv" || k == "isolated-secondary" || k == "floating" || k == "patient"
	}
	if secondary(ka) && secondary(kb) {
		return "secondary"
	}
	return ""
}

func mmToMil(mm float64) float64 { return math.Round(mm/0.0254*100) / 100 }

// ---- per-net intent ---------------------------------------------------------

// intentRole maps the intent role vocabulary onto NetRole ("" = keep).
func intentRole(r string) NetRole {
	switch strings.ToLower(r) {
	case "ground", "gnd", "return":
		return RoleGround
	case "power", "rail", "supply":
		return RolePower
	case "signal", "gpio", "data":
		return RoleSignal
	case "diff", "differential":
		return RoleDiff
	case "clock", "crystal":
		return RoleClock
	case "analog", "sense":
		return RoleAnalog
	case "rf":
		return RoleRF
	case "switch", "switch-node", "sw":
		return RoleSwitch
	}
	return ""
}

// intentLookup finds a net's intent record case-insensitively.
func intentLookup(in *Intent, net string) *IntentNet {
	if in == nil || in.Nets == nil {
		return nil
	}
	if n := in.Nets[net]; n != nil {
		return n
	}
	for name, n := range in.Nets {
		if upper(name) == upper(net) {
			return n
		}
	}
	return nil
}

// intentClass returns the net class that lists (or is named by) the net.
func intentClass(in *Intent, net string, n *IntentNet) *IntentNetClass {
	if in == nil {
		return nil
	}
	for i := range in.NetClasses {
		c := &in.NetClasses[i]
		if n != nil && n.NetClass != "" && strings.EqualFold(c.Name, n.NetClass) {
			return c
		}
		for _, m := range c.Nets {
			if upper(m) == upper(net) {
				return c
			}
		}
	}
	return nil
}

// applyIntentBase sets role / voltage / current / pairing from the intent
// (before widths are computed).
func applyIntentBase(np *NetPlan, n *IntentNet) {
	np.Source = "intent"
	if r := intentRole(n.Role); r != "" {
		np.Role = r
	}
	switch {
	case n.Voltage.Nom != 0:
		np.Voltage = math.Abs(n.Voltage.Nom)
	case n.Voltage.Max != 0:
		np.Voltage = math.Abs(n.Voltage.Max)
	}
	if n.CurrentA > 0 {
		np.CurrentA = n.CurrentA
	}
	if n.DiffPair != "" {
		np.PairWith, np.Role = n.DiffPair, RoleDiff
	}
	np.Why = append(np.Why, n.Why...)
}

// applyIntentRules overrides widths / vias / clearance after the engine's
// own computation: net-level declarations win exactly (never below the
// process minimum); a class width is a floor over the current-based width.
func applyIntentRules(np *NetPlan, n *IntentNet, cls *IntentNetClass, r Rules) {
	if n != nil {
		if n.WidthMil.Outer > 0 {
			np.WidthMil = math.Max(n.WidthMil.Outer, r.MinTrack)
			np.Why = append(np.Why, whyf("intent width %.1f mil outer", np.WidthMil))
		}
		if n.WidthMil.Inner > 0 {
			np.InnerWidthMil = math.Max(n.WidthMil.Inner, r.MinTrack)
		} else if n.WidthMil.Outer > 0 {
			np.InnerWidthMil = math.Max(np.InnerWidthMil, np.WidthMil)
		}
		if n.ViasPerTransition > 0 {
			np.ViasPerTransition = n.ViasPerTransition
		}
		if n.ClearanceMil > 0 {
			np.ClearanceMil = math.Max(n.ClearanceMil, r.Clearance)
			np.Why = append(np.Why, whyf("intent clearance %.1f mil", np.ClearanceMil))
		}
	}
	if cls != nil {
		if (n == nil || n.WidthMil.Outer <= 0) && cls.TrackMil > np.WidthMil {
			np.Why = append(np.Why, whyf("net class %s: width %.1f → %.1f mil", cls.Name, np.WidthMil, cls.TrackMil))
			np.WidthMil = cls.TrackMil
			np.InnerWidthMil = math.Max(np.InnerWidthMil, cls.TrackMil)
		}
		if (n == nil || n.ClearanceMil <= 0) && cls.ClearanceMil > np.ClearanceMil {
			np.ClearanceMil = cls.ClearanceMil
			np.Why = append(np.Why, whyf("net class %s: clearance %.1f mil", cls.Name, cls.ClearanceMil))
		}
	}
}

// ---- circuit domains from the intent ------------------------------------------

// IsBridge reports whether a part spans two domains: an isolator kind, or
// (with an intent) any part with pads on two domains that need insulation
// between them (a Y capacitor, a transformer, an opto).
func (c *Circuit) IsBridge(ref string) bool {
	return c.Kinds[ref].Bridges() || c.Bridge[ref]
}

// intentDomains replaces the inferred domains, bridges and barriers with the
// intent's.
func (c *Circuit) intentDomains(b *Board, an *Analysis) {
	iso := an.Iso
	c.Domains, c.Barriers = nil, nil
	c.DomainOf = map[string]string{}
	c.Bridge = map[string]bool{}
	partDoms := map[string][]string{}
	netParts := map[string][]string{}
	for _, p := range b.Parts {
		seen := map[string]bool{}
		for _, pd := range p.Pads {
			if pd.Net == "" {
				continue
			}
			netParts[pd.Net] = append(netParts[pd.Net], p.Ref)
			if d := iso.NetDomain[pd.Net]; d != "" && !seen[d] {
				seen[d] = true
				partDoms[p.Ref] = append(partDoms[p.Ref], d)
			}
		}
	}
	assign := map[string]string{}
	for _, p := range b.Parts {
		ds := partDoms[p.Ref]
		switch {
		case len(ds) == 1:
			assign[p.Ref] = ds[0]
		case len(ds) >= 2:
			// Two domains on one part: a bridge when any pair of them is
			// insulated; otherwise (a declared split with no requirement)
			// it belongs to the first.
			bridge := false
			for i := range ds {
				for j := i + 1; j < len(ds); j++ {
					if iso.Pair(ds[i], ds[j]) != nil {
						bridge = true
					}
				}
			}
			if bridge {
				c.Bridge[p.Ref] = true
			} else {
				sort.Strings(ds)
				assign[p.Ref] = ds[0]
			}
		}
	}
	// Parts on nets outside every domain inherit through shared nets.
	queue := make([]string, 0, len(assign))
	for ref := range assign {
		queue = append(queue, ref)
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		for _, pd := range b.Part(ref).Pads {
			if pd.Net == "" || iso.NetDomain[pd.Net] != "" {
				continue
			}
			for _, other := range netParts[pd.Net] {
				if _, done := assign[other]; done || c.IsBridge(other) {
					continue
				}
				assign[other] = assign[ref]
				queue = append(queue, other)
			}
		}
	}
	byID := map[string]*Domain{}
	for _, d := range iso.Domains {
		peak := d.WorkingVpeak
		if peak <= 0 {
			peak = d.WorkingVrms * math.Sqrt2 // AC assumed when only rms is given
		}
		dom := &Domain{ID: d.ID, Ground: d.Reference, Kind: d.Kind, Mains: strings.EqualFold(d.Kind, "mains"), MaxVoltage: peak}
		dom.Hazardous = hazardousKind(d.Kind) || dom.MaxVoltage > 60
		byID[d.ID] = dom
	}
	for _, p := range b.Parts {
		d := assign[p.Ref]
		if d == "" {
			if c.IsBridge(p.Ref) {
				continue
			}
			d = "UNREFERENCED"
		}
		dom := byID[d]
		if dom == nil {
			dom = &Domain{ID: d}
			byID[d] = dom
		}
		dom.Parts = append(dom.Parts, p.Ref)
		c.DomainOf[p.Ref] = d
	}
	for net, d := range iso.NetDomain {
		if dom := byID[d]; dom != nil {
			dom.Nets = append(dom.Nets, net)
		}
	}
	for _, dom := range byID {
		if len(dom.Parts) == 0 && len(dom.Nets) == 0 {
			continue
		}
		sort.Strings(dom.Nets)
		c.Domains = append(c.Domains, dom)
	}
	sort.Slice(c.Domains, func(i, j int) bool { return c.Domains[i].ID < c.Domains[j].ID })
	c.Notes = append(c.Notes, sprintf("domains from intent (%s): %d domain(s), %d insulation pair(s)", safety.NormStandard(iso.Standard.Name), len(c.Domains), len(iso.Pairs)))
	// Barriers from the pairs.
	for _, ip := range iso.Pairs {
		br := &Barrier{A: ip.A, B: ip.B, WorkingV: ip.WorkingVpeak, Insulation: ip.Insulation,
			CreepageMil: ip.CreepageMil, ClearanceMil: ip.ClearanceMil, SlotWidthMil: ip.SlotWidthMil, Ref: ip.Ref, Source: ip.Source}
		for _, p := range b.Parts {
			ds := partDoms[p.Ref]
			if containsStr(ds, ip.A) && containsStr(ds, ip.B) {
				br.Bridges = append(br.Bridges, p.Ref)
			}
		}
		br.Why = append(br.Why, sprintf("%s insulation %s|%s at %.0f Vrms / %.0f Vpk: creepage %.2f mm, clearance %.2f mm (%s, %s)",
			ip.Insulation, ip.A, ip.B, ip.WorkingVrms, ip.WorkingVpeak, ip.CreepageMil*0.0254, ip.ClearanceMil*0.0254, ip.Source, ip.Ref))
		for _, ref := range br.Bridges {
			if g := bridgeGeometry(b.Part(ref), iso, ip); g != nil && g.GapMil < ip.CreepageMil {
				br.SlotUnder = append(br.SlotUnder, ref)
				br.Why = append(br.Why, sprintf("%s: %s-side and %s-side pads %.0f mil apart < %.0f mil creepage — milled slot ≥ %.2f mm under the body", ref, ip.A, ip.B, g.GapMil, ip.CreepageMil, ip.SlotWidthMil*0.0254))
			}
		}
		c.Barriers = append(c.Barriers, br)
	}
	sort.Slice(c.Barriers, func(i, j int) bool { return c.Barriers[i].A+c.Barriers[i].B < c.Barriers[j].A+c.Barriers[j].B })
}
