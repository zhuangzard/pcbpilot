package dsn

import (
	"fmt"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/pcb/specctra"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/geom"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

// DSN rule scopes (spec 04 §2):
//
//	(structure (rule …))                          pcb
//	(structure (layer L … (rule …)))              layer
//	(class C nets… (rule …) (layer_rule L (rule …)) (circuit …))  class, class-layer
//	(network (net N … (rule …) (layer_rule …)))   net, net-layer
//	(class_class (classes C1 C2) (rule …) (layer_rule …))  class_class(-layer)
//	(structure (region … (region_net|region_class X) (rule …)))  region
//
// Classes are numbered 1, 2, … in file order; a net in no class is class 0.
// A class lists its nets by name (EasyEDA's single quotes are stripped); a
// class without nets, such as EasyEDA's '' class, therefore governs nothing.

// objKinds maps Specctra clearance object names to rule object kinds.
var objKinds = map[string]rules.ObjKind{
	"pin": rules.Pin, "smd": rules.SMD, "via": rules.Via, "wire": rules.Wire,
	"area": rules.Area, "testpoint": rules.TestPoint,
}

// allKinds lists every object kind, for expanding generic clearances.
var allKinds = []rules.ObjKind{rules.Pin, rules.SMD, rules.Via, rules.Wire, rules.Area, rules.TestPoint}

// clrAcc collects one scope's clearances by specificity: a plain clearance
// (or type default) applies to every object pair, default_X to X against
// anything, and an X_Y type to that pair only. More specific wins regardless
// of file order, because rules.Builder.Set has no "generic" key.
type clrAcc struct {
	generic *int64
	deflt   map[rules.ObjKind]int64
	pair    map[rules.ClrType]int64
}

func (a *clrAcc) empty() bool { return a.generic == nil && len(a.deflt) == 0 && len(a.pair) == 0 }

func (a *clrAcc) result() map[rules.ClrType]int64 {
	if a.empty() {
		return nil
	}
	out := map[rules.ClrType]int64{}
	if a.generic != nil {
		for i, x := range allKinds {
			for _, y := range allKinds[i:] {
				out[rules.ClrType{A: x, B: y}] = *a.generic
			}
		}
	}
	for _, x := range allKinds { // deterministic order over the map
		v, ok := a.deflt[x]
		if !ok {
			continue
		}
		for _, y := range allKinds {
			out[pairType(x, y)] = v
		}
	}
	for t, v := range a.pair {
		out[t] = v
	}
	return out
}

// pairType is the canonical (A <= B) clearance type of two object kinds.
func pairType(x, y rules.ObjKind) rules.ClrType {
	if y < x {
		x, y = y, x
	}
	return rules.ClrType{A: x, B: y}
}

// pendingScope is one scope's rules while the file is read.
type pendingScope struct {
	scope rules.Scope
	rs    rules.RuleSet
	clr   clrAcc
}

type scopeSet struct {
	order []*pendingScope
	byKey map[string]*pendingScope
}

// get returns the scope's accumulator; region scopes are never merged.
func (s *scopeSet) get(sc rules.Scope) *pendingScope {
	key := ""
	if sc.Kind != rules.ScopeRegion {
		key = fmt.Sprintf("%d/%d/%d/%d/%d", sc.Kind, sc.Layer, sc.Class, sc.Class2, sc.Net)
		if p := s.byKey[key]; p != nil {
			return p
		}
	}
	p := &pendingScope{scope: sc}
	s.order = append(s.order, p)
	if key != "" {
		s.byKey[key] = p
	}
	return p
}

// readRules loads the via catalogue, the classes and every rule scope.
func (r *reader) readRules(st, network *specctra.Expr) error {
	ss := &scopeSet{byKey: map[string]*pendingScope{}}
	vias := map[string]bool{}
	addVias := func(names []string) error {
		for _, n := range names {
			if vias[n] {
				continue
			}
			land, from, to, err := r.viaLand(n)
			if err != nil {
				return err
			}
			vias[n] = true
			// The DSN padstack has no drill, so Drill stays 0 (unknown).
			r.rb.AddVia(rules.ViaType{Name: n, Pad: 2 * land, From: from, To: to})
		}
		return nil
	}

	pcb := rules.Scope{Kind: rules.ScopePCB}
	for _, c := range st.List[1:] {
		if !c.IsList {
			continue
		}
		switch c.Head() {
		case "via":
			names := c.Atoms()
			if err := addVias(names); err != nil {
				return err
			}
			if len(names) > 0 {
				ss.get(pcb).rs.UseVia = names
			}
		case "rule":
			if err := r.rule(c, ss.get(pcb)); err != nil {
				return err
			}
		case "layer":
			a := c.Atoms()
			if len(a) == 0 {
				continue
			}
			for _, ru := range c.Children("rule") {
				if err := r.rule(ru, ss.get(rules.Scope{Kind: rules.ScopeLayer, Layer: r.layerIdx[a[0]]})); err != nil {
					return err
				}
			}
		case "region":
			if err := r.region(c, ss); err != nil {
				return err
			}
		}
	}
	if network == nil {
		r.flush(ss)
		return nil
	}

	classes := map[string]rules.ClassID{}
	for i, cl := range network.Children("class") {
		a := cl.Atoms()
		if len(a) == 0 {
			continue
		}
		id := rules.ClassID(i + 1)
		classes[unquote(a[0])] = id
		for _, n := range a[1:] {
			net, ok := r.nets[unquote(n)]
			if !ok {
				r.warnf("class %s: net %s is not in the design, skipped", a[0], n)
				continue
			}
			r.rb.SetClass(net, id)
		}
		sc := rules.Scope{Kind: rules.ScopeClass, Class: id}
		if err := r.scopeBody(cl, sc, rules.ScopeClassLayer, ss, addVias); err != nil {
			return fmt.Errorf("class %s: %w", a[0], err)
		}
	}
	for _, n := range network.Children("net") {
		a := n.Atoms()
		if len(a) == 0 {
			continue
		}
		sc := rules.Scope{Kind: rules.ScopeNet, Net: r.net(a[0])}
		if err := r.scopeBody(n, sc, rules.ScopeNetLayer, ss, addVias); err != nil {
			return fmt.Errorf("net %s: %w", a[0], err)
		}
	}
	for _, cc := range network.Children("class_class") {
		cls := cc.Child("classes")
		if cls == nil || len(cls.Atoms()) != 2 {
			r.warnf("class_class without two (classes), skipped")
			continue
		}
		c1, ok1 := classes[unquote(cls.Atoms()[0])]
		c2, ok2 := classes[unquote(cls.Atoms()[1])]
		if !ok1 || !ok2 {
			r.warnf("class_class %v: unknown class, skipped", cls.Atoms())
			continue
		}
		sc := rules.Scope{Kind: rules.ScopeClassClass, Class: min(c1, c2), Class2: max(c1, c2)}
		if err := r.scopeBody(cc, sc, rules.ScopeClassClassLayer, ss, addVias); err != nil {
			return fmt.Errorf("class_class: %w", err)
		}
	}
	r.flush(ss)
	return nil
}

// scopeBody reads the (rule), (layer_rule) and (circuit) children of a class,
// net or class_class. layerKind is the scope kind of its layer_rule entries.
func (r *reader) scopeBody(e *specctra.Expr, sc rules.Scope, layerKind rules.ScopeKind, ss *scopeSet,
	addVias func([]string) error) error {
	for _, ru := range e.Children("rule") {
		if err := r.rule(ru, ss.get(sc)); err != nil {
			return err
		}
	}
	for _, lr := range e.Children("layer_rule") {
		for _, ln := range lr.Atoms() {
			id, ok := r.layerIdx[ln]
			if !ok {
				r.warnf("layer_rule: unknown layer %q, skipped", ln)
				continue
			}
			lsc := sc
			lsc.Kind, lsc.Layer = layerKind, id
			for _, ru := range lr.Children("rule") {
				if err := r.rule(ru, ss.get(lsc)); err != nil {
					return err
				}
			}
		}
	}
	if ci := e.Child("circuit"); ci != nil {
		if uv := ci.Child("use_via"); uv != nil && len(uv.Atoms()) > 0 {
			if err := addVias(uv.Atoms()); err != nil {
				return err
			}
			ss.get(sc).rs.UseVia = uv.Atoms()
		}
		if ua := ci.Child("use_array"); ua != nil {
			a := ua.Atoms()
			var rows, cols int
			if len(a) != 3 {
				r.warnf("use_array %v malformed, skipped", a)
			} else if _, err := fmt.Sscan(a[1], &rows); err != nil {
				r.warnf("use_array %v malformed, skipped", a)
			} else if _, err := fmt.Sscan(a[2], &cols); err != nil {
				r.warnf("use_array %v malformed, skipped", a)
			} else {
				ss.get(sc).rs.ViaArray = &rules.ViaArraySpec{Template: a[0], Rows: rows, Cols: cols}
			}
		}
	}
	return nil
}

// region reads (region [id] shape [(region_net N)] (rule …)). region_class is
// not supported: classes are declared after the structure.
func (r *reader) region(c *specctra.Expr, ss *scopeSet) error {
	var scopes []rules.Scope
	if err := r.area(c, func(l geom.LayerID, s geom.Shape) {
		sc := rules.Scope{Kind: rules.ScopeRegion, Layer: l, Region: s}
		if n := childAtom(c, "region_net"); n != "" {
			sc.Net = r.net(n)
		}
		scopes = append(scopes, sc)
	}); err != nil {
		return err
	}
	if childAtom(c, "region_class") != "" {
		r.warnf("region: region_class is not supported, the rule applies to every net")
	}
	for _, sc := range scopes {
		p := ss.get(sc)
		for _, ru := range c.Children("rule") {
			if err := r.rule(ru, p); err != nil {
				return err
			}
		}
	}
	return nil
}

// rule merges one (rule …) descriptor into p.
func (r *reader) rule(ru *specctra.Expr, p *pendingScope) error {
	for _, c := range ru.List[1:] {
		if !c.IsList {
			continue
		}
		a := c.Atoms()
		switch c.Head() {
		case "width":
			if len(a) == 0 {
				return fmt.Errorf("(width) without value")
			}
			v, err := num(a[0])
			if err != nil {
				return fmt.Errorf("width: %w", err)
			}
			w := r.nm(v)
			p.rs.Width = &w
		case "clear", "clearance":
			if len(a) == 0 {
				return fmt.Errorf("(%s) without value", c.Head())
			}
			v, err := num(a[0])
			if err != nil {
				return fmt.Errorf("%s: %w", c.Head(), err)
			}
			r.clearance(&p.clr, r.nm(v), c)
		}
	}
	return nil
}

// clearance records value v for every (type …) of c, or as the generic
// clearance when c has no type. Unsupported types are warned and ignored.
func (r *reader) clearance(acc *clrAcc, v int64, c *specctra.Expr) {
	var types []string
	for _, t := range c.Children("type") {
		types = append(types, t.Atoms()...)
	}
	if len(types) == 0 {
		acc.generic = &v
		return
	}
	for _, t := range types {
		switch t {
		case "default":
			acc.generic = &v
			continue
		case "smd_via_same_net":
			setPair(acc, rules.ClrType{A: rules.SMD, B: rules.Via, Special: rules.SameNetSMDVia}, v)
			continue
		case "via_via_same_net":
			setPair(acc, rules.ClrType{A: rules.Via, B: rules.Via, Special: rules.SameNetViaVia}, v)
			continue
		}
		x, y, ok := strings.Cut(t, "_")
		kx, okx := objKinds[x]
		ky, oky := objKinds[y]
		switch {
		case ok && x == "default" && oky:
			setDefault(acc, ky, v)
		case ok && y == "default" && okx:
			setDefault(acc, kx, v)
		case ok && okx && oky:
			setPair(acc, pairType(kx, ky), v)
		default:
			r.warnf("clearance type %q is not supported, ignored", t)
		}
	}
}

func setPair(acc *clrAcc, t rules.ClrType, v int64) {
	if acc.pair == nil {
		acc.pair = map[rules.ClrType]int64{}
	}
	acc.pair[t] = v
}

func setDefault(acc *clrAcc, k rules.ObjKind, v int64) {
	if acc.deflt == nil {
		acc.deflt = map[rules.ObjKind]int64{}
	}
	acc.deflt[k] = v
}

// flush hands every scope to the builder in first-seen order.
func (r *reader) flush(ss *scopeSet) {
	for _, p := range ss.order {
		p.rs.Clearance = p.clr.result()
		r.rb.Set(p.scope, p.rs)
	}
}

// unquote strips the single quotes EasyEDA puts around class member names.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return s[1 : len(s)-1]
	}
	return s
}
