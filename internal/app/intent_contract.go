package app

// intent_contract.go — the CONSUMER side of the schematic electrical-intent
// contract (intent.json). A sibling producer derives it from the schematic +
// DC simulation; `pcb rules apply/check` and `sch intent-annotate` only read the
// subset below. Unknown fields are ignored so additive producer changes never
// break these consumers; the fixed fields are validated before any EDA call.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/netname"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/safety"
)

type designIntent struct {
	Nets       map[string]*intentNet `json:"nets"`
	Pairs      []intentPair          `json:"pairs"`
	NetClasses []intentNetClass      `json:"netClasses"`
	Blocks     []intentBlock         `json:"blocks"`
	Domains    []intentDomain        `json:"domains"`
	Findings   []intentFinding       `json:"findings"`
	// Standard is the safety frame (used for domain edge distances).
	Standard safety.Standard `json:"standard"`
	// Edge is the additive board-edge safety distance (nil in intents
	// derived before it existed: the consumers then use the defaults).
	Edge *intentEdge `json:"edge,omitempty"`
	// Copper is the producer's copper block (weights, allowed rise).
	Copper *intentCopper `json:"copper,omitempty"`

	// sha256 of the raw file bytes; provenance for reports and annotations.
	sourceSHA string
}

type intentCopper struct {
	OuterOz   float64 `json:"outerOz"`
	InnerOz   float64 `json:"innerOz"`
	TempRiseC float64 `json:"tempRiseC"`
}

type intentNet struct {
	Role              string        `json:"role"`
	Domain            string        `json:"domain"`
	Voltage           intentVoltage `json:"voltage"`
	CurrentA          float64       `json:"currentA"`
	WidthMil          intentWidth   `json:"widthMil"`
	ViasPerTransition int           `json:"viasPerTransition"`
	Via               *intentVia    `json:"via,omitempty"` // additive producer field
	ClearanceMil      float64       `json:"clearanceMil"`
	ImpedanceOhm      float64       `json:"impedanceOhm"`
	DiffPair          string        `json:"diffPair"`
	NetClass          string        `json:"netClass"`
	PairGapMil        float64       `json:"pairGapMil,omitempty"` // additive producer field
	Why               []string      `json:"why"`
	// Additive high-speed producer fields (optional).
	Interface    string  `json:"interface,omitempty"`
	LengthGroup  string  `json:"lengthGroup,omitempty"`
	LengthTolMil float64 `json:"lengthTolMil,omitempty"`
	MaxSkewMil   float64 `json:"maxSkewMil,omitempty"`
	MaxVias      int     `json:"maxVias,omitempty"`
}

type intentVoltage struct {
	Nom  float64 `json:"nom"`
	Peak float64 `json:"peak"`
}

type intentWidth struct {
	Outer float64 `json:"outer"`
	Inner float64 `json:"inner"`
	Min   float64 `json:"min"`
}

// intentVia is a net's current-sized via per layer transition (pkg/intent
// NetVia; pcbauto.SizeVias).
type intentVia struct {
	DrillMil           float64 `json:"drillMil"`
	DiaMil             float64 `json:"diaMil"`
	CountPerTransition int     `json:"countPerTransition"`
	AmpacityA          float64 `json:"ampacityA"`
	MarginPct          float64 `json:"marginPct"`
	Why                string  `json:"why"`
}

type intentPair struct {
	A            string   `json:"a"`
	B            string   `json:"b"`
	ClearanceMm  float64  `json:"clearanceMm"`
	CreepageMm   float64  `json:"creepageMm"`
	SlotRequired bool     `json:"slotRequired"`
	Why          []string `json:"why"`
}

type intentNetClass struct {
	Name         string   `json:"name"`
	Nets         []string `json:"nets"`
	TrackMil     float64  `json:"trackMil"`
	ClearanceMil float64  `json:"clearanceMil"`
	ViaDrillMil  float64  `json:"viaDrillMil"`
	ViaDiaMil    float64  `json:"viaDiaMil"`
	// Additive producer fields (optional).
	InnerTrackMil float64 `json:"innerTrackMil,omitempty"`
	MinTrackMil   float64 `json:"minTrackMil,omitempty"`
	DiffGapMil    float64 `json:"diffGapMil,omitempty"`
}

type intentBlock struct {
	ID       string   `json:"id"`
	Function string   `json:"function"`
	Core     string   `json:"core"`
	Parts    []string `json:"parts"`
	Nets     []string `json:"nets"`
	Summary  string   `json:"summary"`
}

type intentDomain struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Nets         []string `json:"nets"`
	WorkingVrms  float64  `json:"workingVrms,omitempty"`
	WorkingVpeak float64  `json:"workingVpeak,omitempty"`
}

// intentEdge mirrors intent.json "edge" (pkg/intent Edge = pcbauto.IntentEdge).
type intentEdge struct {
	OuterMil float64                      `json:"outerMil"`
	InnerMil float64                      `json:"innerMil"`
	VcutMil  float64                      `json:"vcutMil"`
	EdgeKind string                       `json:"edgeKind"`
	ByDomain map[string]*intentEdgeDomain `json:"byDomain,omitempty"`
	Why      []string                     `json:"why,omitempty"`
}

type intentEdgeDomain struct {
	Mil         float64  `json:"mil"`
	ClearanceMm float64  `json:"clearanceMm,omitempty"`
	CreepageMm  float64  `json:"creepageMm,omitempty"`
	Insulation  string   `json:"insulation,omitempty"`
	Why         []string `json:"why,omitempty"`
}

type intentFinding struct {
	Severity   string `json:"severity"`
	Kind       string `json:"kind"`
	Message    string `json:"message"`
	Suggestion string `json:"suggestion"`
}

// loadDesignIntent reads and validates an intent.json file.
func loadDesignIntent(path string) (*designIntent, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read intent: %w", err)
	}
	return parseDesignIntent(raw)
}

func parseDesignIntent(raw []byte) (*designIntent, error) {
	var in designIntent
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, fmt.Errorf("parse intent: %w", err)
	}
	sum := sha256.Sum256(raw)
	in.sourceSHA = hex.EncodeToString(sum[:])
	if err := in.validate(); err != nil {
		return nil, err
	}
	return &in, nil
}

var intentRoles = map[string]bool{"power": true, "ground": true, "signal": true, "switch": true, "hs": true, "diff": true, "rf": true, "analog": true, "clock": true, "": true}

func (in *designIntent) validate() error {
	if len(in.Nets) == 0 && len(in.NetClasses) == 0 {
		return fmt.Errorf("intent: no nets and no netClasses — nothing to apply")
	}
	nonNeg := func(where string, vals ...float64) error {
		for _, v := range vals {
			if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
				return fmt.Errorf("intent: %s has a negative or non-finite dimension (%v)", where, v)
			}
		}
		return nil
	}
	for name, n := range in.Nets {
		if strings.TrimSpace(name) == "" || n == nil {
			return fmt.Errorf("intent: nets contains an empty name or null entry")
		}
		if !intentRoles[n.Role] {
			return fmt.Errorf("intent: net %s has unknown role %q", name, n.Role)
		}
		if err := nonNeg("net "+name, n.CurrentA, n.WidthMil.Outer, n.WidthMil.Inner, n.WidthMil.Min, n.ClearanceMil, n.ImpedanceOhm, n.PairGapMil, n.LengthTolMil, n.MaxSkewMil, float64(n.MaxVias)); err != nil {
			return err
		}
		if v := n.Via; v != nil {
			if err := nonNeg("net "+name+" via", v.DrillMil, v.DiaMil, v.AmpacityA, float64(v.CountPerTransition)); err != nil {
				return err
			}
			if v.DrillMil > 0 && v.DiaMil > 0 && v.DrillMil >= v.DiaMil {
				return fmt.Errorf("intent: net %s via drill %.2f mil must be smaller than diameter %.2f mil", name, v.DrillMil, v.DiaMil)
			}
		}
	}
	seen := map[string]bool{}
	for _, c := range in.NetClasses {
		if err := validIntentName(c.Name); err != nil {
			return fmt.Errorf("intent: netClasses: %w", err)
		}
		if seen[c.Name] {
			return fmt.Errorf("intent: duplicate netClass %s", c.Name)
		}
		seen[c.Name] = true
		if err := nonNeg("netClass "+c.Name, c.TrackMil, c.ClearanceMil, c.ViaDrillMil, c.ViaDiaMil, c.InnerTrackMil, c.MinTrackMil, c.DiffGapMil); err != nil {
			return err
		}
		if c.ViaDiaMil > 0 && c.ViaDrillMil > 0 && c.ViaDrillMil >= c.ViaDiaMil {
			return fmt.Errorf("intent: netClass %s via drill %.2f mil must be smaller than diameter %.2f mil", c.Name, c.ViaDrillMil, c.ViaDiaMil)
		}
	}
	for _, p := range in.Pairs {
		if err := nonNeg("pair "+p.A+"/"+p.B, p.ClearanceMm, p.CreepageMm); err != nil {
			return err
		}
	}
	if e := in.Edge; e != nil {
		if _, err := pcbauto.NormEdgeKind(e.EdgeKind); err != nil {
			return fmt.Errorf("intent: edge: %w", err)
		}
		if err := nonNeg("edge", e.OuterMil, e.InnerMil, e.VcutMil); err != nil {
			return err
		}
		for id, d := range e.ByDomain {
			if d == nil {
				return fmt.Errorf("intent: edge.byDomain[%s] is null", id)
			}
			if err := nonNeg("edge.byDomain["+id+"]", d.Mil, d.ClearanceMm, d.CreepageMm); err != nil {
				return err
			}
		}
	}
	return nil
}

// edgePolicy resolves the intent's board-edge distance with the same
// rules pcb auto uses (defaults when the intent predates "edge"; domain
// distances from the intent domains + standard when byDomain is absent).
func (in *designIntent) edgePolicy() *pcbauto.EdgePolicy {
	pi := &pcbauto.Intent{Nets: map[string]*pcbauto.IntentNet{}}
	for _, d := range in.Domains {
		pi.Domains = append(pi.Domains, pcbauto.IntentDomain{ID: d.ID, Kind: d.Kind, Nets: d.Nets, WorkingVrms: d.WorkingVrms, WorkingVpeak: d.WorkingVpeak})
	}
	for name, n := range in.Nets {
		pi.Nets[name] = &pcbauto.IntentNet{Domain: n.Domain}
	}
	pi.Standard = in.Standard
	if e := in.Edge; e != nil {
		pe := &pcbauto.IntentEdge{OuterMil: e.OuterMil, InnerMil: e.InnerMil, VcutMil: e.VcutMil, EdgeKind: e.EdgeKind, Why: e.Why}
		for id, d := range e.ByDomain {
			if pe.ByDomain == nil {
				pe.ByDomain = map[string]*pcbauto.EdgeDomain{}
			}
			pe.ByDomain[id] = &pcbauto.EdgeDomain{Mil: d.Mil, ClearanceMm: d.ClearanceMm, CreepageMm: d.CreepageMm, Insulation: d.Insulation, Why: d.Why}
		}
		pi.Edge = pe
	}
	return pcbauto.EdgeFromIntent(pi, nil)
}

// validIntentName guards names that become EDA rule/class keys.
func validIntentName(name string) error {
	if strings.TrimSpace(name) == "" || name != strings.TrimSpace(name) {
		return fmt.Errorf("name %q must be non-empty without surrounding spaces", name)
	}
	if name == "__proto__" || name == "constructor" || name == "prototype" {
		return fmt.Errorf("name %q is reserved", name)
	}
	if strings.ContainsAny(name, "\n\r\t\"\\") {
		return fmt.Errorf("name %q contains control or quote characters", name)
	}
	return nil
}

// onBoard is the intent with every net name resolved to the board's net
// identity (pkg/netname): an intent derived from display names ("VCC_PIC",
// "VPP/MCLR") names the hierarchical KiCad board's "/pic_sockets/VCC_PIC" /
// "/VPP{slash}MCLR". Names already on the board and unknown names stay.
// Never merged, never guessed: every net reference (nets, diff pairs, net
// classes, domains, blocks) is resolved first, and distinct names reaching
// one board net (aliases, or an alias next to the exact identity) or a
// name fitting several board nets is an error before anything is
// rewritten. The receiver is not changed; renamed lists "intent → board".
func (in *designIntent) onBoard(boardNets []string) (*designIntent, []string, error) {
	if in == nil {
		return in, nil, nil
	}
	refs := map[string]bool{}
	for name, n := range in.Nets {
		refs[name] = true
		if n != nil && n.DiffPair != "" {
			refs[n.DiffPair] = true
		}
	}
	for _, nc := range in.NetClasses {
		for _, n := range nc.Nets {
			refs[n] = true
		}
	}
	for _, d := range in.Domains {
		for _, n := range d.Nets {
			refs[n] = true
		}
	}
	for _, b := range in.Blocks {
		for _, n := range b.Nets {
			refs[n] = true
		}
	}
	r := netname.NewResolver(boardNets)
	var problems []string
	dest := map[string][]string{} // board net → distinct intent names reaching it
	for _, name := range sortedKeysOf(refs) {
		switch id, m := r.Resolve(name); m {
		case netname.Ambiguous:
			problems = append(problems, fmt.Sprintf("%q could be board net %s", name, strings.Join(r.Candidates(name), " or ")))
		case netname.Found:
			dest[id] = append(dest[id], name)
		}
	}
	to := map[string]string{}
	for _, id := range sortedKeysOf(dest) {
		if srcs := dest[id]; len(srcs) > 1 {
			problems = append(problems, fmt.Sprintf("intent names %s all name board net %s", strings.Join(srcs, ", "), id))
		} else if srcs[0] != id {
			to[srcs[0]] = id
		}
	}
	if len(problems) > 0 {
		return nil, nil, fmt.Errorf("intent nets do not resolve to one board net each (pkg/netname; never merged or guessed): %s (fix: name each intent net by the board's full net name, e.g. re-derive the intent from kicad netlist output)", strings.Join(problems, "; "))
	}
	var renamed []string
	for _, n := range sortedKeysOf(to) {
		renamed = append(renamed, n+" → "+to[n])
	}
	if len(to) == 0 {
		return in, nil, nil
	}
	conv := func(n string) string {
		if id, ok := to[n]; ok {
			return id
		}
		return n
	}
	list := func(ns []string) []string {
		out := make([]string, len(ns))
		for i, n := range ns {
			out[i] = conv(n)
		}
		return out
	}
	cp := *in
	cp.Nets = make(map[string]*intentNet, len(in.Nets))
	for name, n := range in.Nets {
		if n != nil && n.DiffPair != "" {
			x := *n
			x.DiffPair = conv(n.DiffPair)
			n = &x
		}
		cp.Nets[conv(name)] = n
	}
	cp.NetClasses = append([]intentNetClass(nil), in.NetClasses...)
	for i := range cp.NetClasses {
		cp.NetClasses[i].Nets = list(cp.NetClasses[i].Nets)
	}
	cp.Blocks = append([]intentBlock(nil), in.Blocks...)
	for i := range cp.Blocks {
		cp.Blocks[i].Nets = list(cp.Blocks[i].Nets)
	}
	cp.Domains = append([]intentDomain(nil), in.Domains...)
	for i := range cp.Domains {
		cp.Domains[i].Nets = list(cp.Domains[i].Nets)
	}
	return &cp, renamed, nil
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// sortedNetNames returns intent net names in a stable order.
func (in *designIntent) sortedNetNames() []string {
	out := make([]string, 0, len(in.Nets))
	for n := range in.Nets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}
