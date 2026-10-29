package kicad

// lcsc_alt.go — `kicad lcsc --alternatives`: for BOM lines that cannot be
// ordered as they are (out of stock, short of the order quantity, discontinued,
// unknown to the catalog) or that sit in the extended library, propose
// replacement LCSC numbers from the live catalog. It never edits the design:
// the result is a JSON plan; `kicad lcsc --set @plan.json` applies it, and
// refuses when the design no longer matches the plan's "expect".
//
// A replacement is only proposed when every hard filter can be verified:
// buyable, enough catalog stock (reserved across lines), a confidently matching
// package for the *board footprint*, and — for the families whose key
// parameters are known (chip resistors, ceramic capacitors) — every key
// parameter present on both sides and acceptable. Anything unverifiable is
// listed as a candidate with the reason and never reaches "assignments".

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/gate"
)

// Alternatives plan identification.
const (
	AltReportKind    = "pcbpilot.lcsc-alternatives"
	AltReportVersion = 1
)

// ParamCheck is one key-parameter comparison.
type ParamCheck struct {
	Name      string `json:"name"`
	Rule      string `json:"rule"` // "=" | ">=" | "<="
	Current   string `json:"current"`
	Candidate string `json:"candidate"`
	Status    string `json:"status"` // ok | fail | unverified
}

// AltCandidate is one candidate replacement for a line.
type AltCandidate struct {
	LCSC       string       `json:"lcsc"`
	MPN        string       `json:"mpn"`
	Brand      string       `json:"brand,omitempty"`
	Package    string       `json:"package"`
	Base       bool         `json:"base"`
	Preferred  bool         `json:"preferred,omitempty"`
	Stock      int64        `json:"stock"`
	UnitPrice  *float64     `json:"unitPrice,omitempty"`
	SameMPN    bool         `json:"sameMpn,omitempty"`
	Eligible   bool         `json:"eligible"` // every hard filter verified
	Params     []ParamCheck `json:"params,omitempty"`
	Unverified []string     `json:"unverified,omitempty"` // why it cannot be proposed
}

// AltProposal is the replacement chosen for a line.
type AltProposal struct {
	LCSC           string `json:"lcsc"`
	RequiresReview bool   `json:"requiresReview"` // always true: a person confirms every substitution
	Why            string `json:"why"`
}

// AltLine is one BOM line (refs sharing an LCSC number and footprint).
type AltLine struct {
	Refs       []string       `json:"refs"`
	Value      string         `json:"value"`
	Footprint  string         `json:"footprint"`
	Current    *CatalogPart   `json:"current,omitempty"`
	LCSC       string         `json:"lcsc"`
	Need       *int64         `json:"need,omitempty"`
	Reasons    []string       `json:"reasons"`
	Unknown    bool           `json:"unverifiable,omitempty"` // catalog record lacks stock / buyable: cannot tell
	Blocking   bool           `json:"blocking"`               // cannot be ordered as is (extended-library alone is not blocking)
	Candidates []AltCandidate `json:"candidates,omitempty"`
	Proposal   *AltProposal   `json:"proposal"`
	Error      string         `json:"error,omitempty"`
}

// AltReport is the plan document.
type AltReport struct {
	Kind        string            `json:"kind"`
	Version     int               `json:"version"`
	Verdict     gate.Status       `json:"verdict"`
	ExitCode    int               `json:"exitCode"`
	Boards      int               `json:"boards"`
	QueriedAt   string            `json:"queriedAt"`
	Lines       []AltLine         `json:"lines"`
	Assignments map[string]string `json:"assignments"` // ref → proposed LCSC (what --set applies)
	Expect      map[string]string `json:"expect"`      // ref → LCSC the design must still carry
	// Fingerprint binds the plan to the substitution-relevant design data (see PlanFingerprint).
	Fingerprint string   `json:"fingerprint,omitempty"`
	Notes       []string `json:"notes,omitempty"`
	Warnings    []string `json:"warnings,omitempty"`
}

// AltInput is everything Alternatives needs.
type AltInput struct {
	Parts     []Part
	DesignMPN map[string]string
	Boards    int
	Catalog   Catalog
	Now       time.Time
	Limit     int // candidates listed per line
	Warnings  []string
}

var altNotes = []string{
	"nothing in the design was changed; apply with: pcbpilot kicad lcsc --pcb <board> --sch <sch> --set @<this file>",
	"every proposal needs a person's review (value, tolerance, voltage/power margin, manufacturer, datasheet) before it is applied",
	"package equality means chip size or family + pin count only; body width and pad geometry are not compared",
	"ranking: same MPN, basic library, JLC preferred, tighter tolerance, lower unit price; stock is reserved across lines so two lines never claim the same stock twice",
}

// keyParam: a parameter that must hold for a replacement.
type keyParam struct {
	name string
	rule string // "=" | ">=" | "<="
	text bool   // compare as normalised text (dielectric, film type) instead of a quantity
}

// keyParams returns the verified-parameter set of the part's family, or nil
// when the family has none (then only a same-MPN part can be proposed).
func keyParams(p *CatalogPart) []keyParam {
	cat := strings.ToLower(p.Category)
	switch {
	case strings.Contains(cat, "chip resistor") && strings.Contains(cat, "surface mount"):
		return []keyParam{{"Resistance", "=", false}, {"Power(Watts)", ">=", false}, {"Tolerance", "<=", false}, {"Type", "=", true}}
	case strings.Contains(cat, "multilayer ceramic capacitors"):
		return []keyParam{{"Capacitance", "=", false}, {"Voltage Rating", ">=", false}, {"Tolerance", "<=", false}, {"Temperature Coefficient", "=", true}}
	}
	return nil
}

func normText(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "", "-", "", "_", "").Replace(s)
	if s == "NP0" {
		s = "C0G"
	}
	return s
}

// compareParam checks one key parameter; a value missing or unreadable on
// either side is "unverified", never ok.
func compareParam(kp keyParam, cur, cand *CatalogPart) ParamCheck {
	pc := ParamCheck{Name: kp.name, Rule: kp.rule, Status: "unverified"}
	cv, cok := cur.attr(kp.name)
	nv, nok := cand.attr(kp.name)
	pc.Current, pc.Candidate = cv, nv
	if !cok || !nok {
		return pc
	}
	if kp.text {
		if normText(cv) == normText(nv) {
			pc.Status = "ok"
		} else {
			pc.Status = "fail"
		}
		return pc
	}
	a, aok := ParseQuantity(cv)
	b, bok := ParseQuantity(nv)
	if !aok || !bok || a.Unit != b.Unit {
		return pc
	}
	var good bool
	switch kp.rule {
	case "=":
		good = sameQty(a, b)
	case ">=":
		good = b.V >= a.V || sameQty(a, b)
	case "<=":
		good = b.V <= a.V || sameQty(a, b)
	}
	pc.Status = "fail"
	if good {
		pc.Status = "ok"
	}
	return pc
}

// toleranceOf is the candidate's tolerance in percent (for ranking); 0, false if unreadable.
func toleranceOf(p *CatalogPart) (float64, bool) {
	v, ok := p.attr("Tolerance")
	if !ok {
		return 0, false
	}
	q, ok := ParseQuantity(v)
	if !ok || q.Unit != "%" {
		return 0, false
	}
	return q.V, true
}

// lineReasons says why a line needs an alternative; blocking is false for the extended library alone.
func lineReasons(r CatalogLookup, need int64) (reasons []string, blocking bool) {
	switch {
	case r.Err != "":
		return []string{"catalog-unavailable"}, true
	case r.Part == nil:
		return []string{"not-in-catalog"}, true
	}
	p := r.Part
	if p.Buyable == nil {
		reasons = append(reasons, "buyable-unknown")
	}
	if p.Stock == nil {
		reasons = append(reasons, "stock-unknown")
	}
	if p.Buyable != nil && (!*p.Buyable || p.NoBuyReason != "" || p.MergedCode != "") {
		reasons, blocking = append(reasons, "not-buyable"), true
	}
	if p.Loss == nil {
		// No lossNumber is an unknown allowance, not a zero one.
		reasons = append(reasons, "loss-unknown")
	}
	if p.Stock != nil {
		switch {
		case *p.Stock == 0:
			reasons, blocking = append(reasons, "out-of-stock"), true
		case *p.Stock < need:
			reasons, blocking = append(reasons, fmt.Sprintf("stock-below-need (%d < %d)", *p.Stock, need)), true
		}
	}
	if b, known := p.IsBase(); known && !b {
		reasons = append(reasons, "extended-library")
	}
	return reasons, blocking
}

// Alternatives builds the plan.
func Alternatives(in AltInput) *AltReport {
	if in.Boards < 1 {
		in.Boards = 1
	}
	if in.Limit < 1 {
		in.Limit = 3
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}
	rep := &AltReport{Kind: AltReportKind, Version: AltReportVersion, Boards: in.Boards,
		QueriedAt: in.Now.UTC().Format(time.RFC3339), Assignments: map[string]string{}, Expect: map[string]string{},
		Notes: altNotes, Warnings: in.Warnings, Lines: []AltLine{}}

	type lkey struct{ code, fp string }
	groups := map[lkey]*AltLine{}
	var order []lkey
	placements := map[string]int{}
	var codes []string
	for _, p := range in.Parts {
		if !p.Assembled() || !ValidLCSC(p.LCSC) {
			continue
		}
		k := lkey{p.LCSC, footprintName(p.Footprint)}
		g := groups[k]
		if g == nil {
			g = &AltLine{Value: p.Value, Footprint: p.Footprint, LCSC: p.LCSC, Reasons: []string{}}
			groups[k] = g
			order = append(order, k)
		}
		g.Refs = append(g.Refs, p.Ref)
		if placements[p.LCSC] == 0 {
			codes = append(codes, p.LCSC)
		}
		placements[p.LCSC]++
	}
	sort.Strings(codes)
	sort.SliceStable(order, func(a, b int) bool {
		ga, gb := groups[order[a]], groups[order[b]]
		sortRefs(ga.Refs)
		sortRefs(gb.Refs)
		return refLess(ga.Refs[0], gb.Refs[0])
	})
	var lookups map[string]CatalogLookup
	var catErr string
	if len(codes) > 0 {
		var err error
		if lookups, err = in.Catalog.Lookup(codes); err != nil {
			catErr = err.Error()
		}
	}
	look := func(code string) CatalogLookup {
		if catErr != "" {
			return CatalogLookup{Err: catErr}
		}
		if r, ok := lookups[code]; ok {
			return r
		}
		return CatalogLookup{Err: "the catalog helper returned no answer for " + code}
	}
	need := map[string]int64{}
	for _, code := range codes {
		var loss int64
		if r := look(code); r.Part != nil && r.Part.Loss != nil {
			loss = *r.Part.Loss
		}
		need[code] = int64(placements[code])*int64(in.Boards) + loss
	}

	// Stock already spoken for by lines that keep their number; candidates draw on the rest.
	reserved := map[string]int64{}
	uncertain := map[string]bool{} // retained number whose stock or loss allowance is unknown: the rest cannot be established
	flagged := map[string]bool{}
	for _, k := range order {
		g := groups[k]
		r := look(g.LCSC)
		reasons, blocking := lineReasons(r, need[g.LCSC])
		g.Reasons, g.Blocking, g.Current, g.Error = append(g.Reasons, reasons...), blocking, r.Part, r.Err
		for _, x := range reasons {
			if strings.HasSuffix(x, "-unknown") {
				g.Unknown = true
			}
		}
		n := need[g.LCSC]
		g.Need = &n
		if len(reasons) > 0 {
			flagged[g.LCSC] = true
		}
	}
	held := map[string]bool{}
	for _, k := range order {
		// Every line that keeps its number claims its demand (once per number), including
		// extended-only lines that may find no basic replacement and lines whose buyability is unknown.
		g := groups[k]
		if g.Blocking || held[g.LCSC] {
			continue
		}
		held[g.LCSC] = true
		reserved[g.LCSC] += need[g.LCSC]
		if g.Current == nil || g.Current.Stock == nil || g.Current.Loss == nil {
			uncertain[g.LCSC] = true
		}
	}

	var verdicts []gate.Result
	for _, k := range order {
		g := groups[k]
		if len(g.Reasons) > 0 {
			proposeFor(in, g, reserved, uncertain, rep)
		}
		if g.Blocking {
			st := gate.Pass
			if g.Proposal == nil {
				// An error (catalog unreachable, search failed) means we could not
				// look; otherwise we looked and found nothing safe.
				if g.Error != "" {
					st = gate.CannotCheck
				} else {
					st = gate.Fail
				}
			}
			verdicts = append(verdicts, gate.Result{Name: refList(g.Refs), Status: st})
		} else if g.Error != "" && g.Proposal == nil {
			// A search that failed on an extended-only line: we could not look, so no clean pass.
			verdicts = append(verdicts, gate.Result{Name: refList(g.Refs), Status: gate.CannotCheck, Detail: g.Error})
		}
		if g.Unknown {
			verdicts = append(verdicts, gate.Result{Name: refList(g.Refs), Status: gate.CannotCheck})
		}
		rep.Lines = append(rep.Lines, *g)
	}
	skipped := 0
	for _, p := range in.Parts {
		if p.Assembled() && !ValidLCSC(p.LCSC) {
			skipped++
		}
	}
	if skipped > 0 {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf("%d assembled part(s) have no valid LCSC number and are not covered (see lcsc --check); the verdict cannot be a pass", skipped))
		verdicts = append(verdicts, gate.Result{Name: "unnumbered-parts", Status: gate.CannotCheck})
	}
	if len(codes) == 0 {
		verdicts = append(verdicts, gate.Result{Name: "bom", Status: gate.CannotCheck, Detail: "no assembled part with a valid LCSC number"})
	}
	if len(verdicts) == 0 {
		verdicts = append(verdicts, gate.Result{Name: "bom", Status: gate.Pass})
	}
	rep.Verdict = gate.Aggregate(verdicts...)
	rep.ExitCode = rep.Verdict.ExitCode()
	if len(rep.Assignments) > 0 {
		rep.Fingerprint = PlanFingerprint(in.Parts, in.DesignMPN, in.Boards, PlanCodes(rep.Assignments, rep.Expect))
	}
	return rep
}

func (g *AltLine) hasReason(r string) bool {
	for _, x := range g.Reasons {
		if strings.HasPrefix(x, r) {
			return true
		}
	}
	return false
}

// proposeFor searches, filters, ranks and (maybe) proposes for one flagged line.
func proposeFor(in AltInput, g *AltLine, reserved map[string]int64, uncertain map[string]bool, rep *AltReport) {
	cur := g.Current
	// A number the catalog does not know: the question is still searched, from the design's own
	// MPN and value + footprint package, but with no catalog record to compare against no
	// candidate can be verified, so none is ever eligible (insufficient evidence).
	insufficient := cur == nil
	if insufficient {
		if g.Error != "" { // the catalog could not be asked at all
			return
		}
		cur = &CatalogPart{LCSC: g.LCSC}
		g.Reasons = append(g.Reasons, "insufficient-evidence")
	}
	needNew := func(c *CatalogPart) int64 {
		var loss int64
		if c.Loss != nil {
			loss = *c.Loss
		}
		return int64(len(g.Refs))*int64(in.Boards) + loss
	}
	// Queries: the same MPN under other numbers, and value + package for known families.
	mpn := cur.MPN
	if mpn == "" {
		for _, ref := range g.Refs {
			if m := in.DesignMPN[ref]; m != "" {
				mpn = m
				break
			}
		}
	}
	var queries []string
	if mpn != "" {
		queries = append(queries, mpn)
	}
	if insufficient && strings.TrimSpace(g.Value) != "" {
		if fp, ok := footprintPackage(g.Footprint); ok && fp.chip != "" {
			queries = append(queries, strings.TrimSpace(g.Value)+" "+fp.chip)
		}
	}
	kps := keyParams(cur)
	if kps != nil {
		if v, ok := cur.attr(kps[0].name); ok {
			q := v
			if cur.Package != "" {
				q += " " + cur.Package
			}
			queries = append(queries, q)
		}
	}
	seen := map[string]bool{g.LCSC: true}
	var found []CatalogPart
	for _, q := range queries {
		hits, err := in.Catalog.Search(q, len(g.Refs)*in.Boards)
		if err != nil {
			g.Error = "catalog search failed: " + err.Error()
			continue
		}
		for _, h := range hits {
			if h.LCSC != "" && !seen[h.LCSC] {
				seen[h.LCSC] = true
				found = append(found, h)
			}
		}
	}

	var cands []AltCandidate
	for i := range found {
		h := &found[i]
		c := AltCandidate{LCSC: h.LCSC, MPN: h.MPN, Brand: h.Brand, Package: h.Package, Preferred: h.Preferred}
		if b, _ := h.IsBase(); b {
			c.Base = true
		}
		if h.Stock != nil {
			c.Stock = *h.Stock
		}
		if pr, ok := h.UnitPrice(int64(len(g.Refs)) * int64(in.Boards)); ok {
			c.UnitPrice = &pr
		}
		c.SameMPN = NormMPN(h.MPN) != "" && NormMPN(h.MPN) == NormMPN(mpn)
		c.Eligible = true
		bad := func(why string) {
			c.Eligible = false
			c.Unverified = append(c.Unverified, why)
		}
		if c.SameMPN && (NormMPN(cur.Brand) == "" || NormMPN(h.Brand) != NormMPN(cur.Brand)) {
			// The same type number from another maker is another part, and a missing
			// brand on either side cannot show it is the same one.
			c.SameMPN = false
			if NormMPN(cur.Brand) == "" || NormMPN(h.Brand) == "" {
				bad("same MPN but the manufacturer is not known on both sides (current " + quoteOrEmpty(cur.Brand) + ", candidate " + quoteOrEmpty(h.Brand) + "): not verified as the same part")
			}
		}
		if insufficient {
			bad("the current number has no catalog record: insufficient evidence to verify a replacement")
		}
		switch {
		case h.Buyable == nil:
			bad("catalog does not say whether it can be bought")
		case !*h.Buyable || h.NoBuyReason != "" || h.MergedCode != "":
			bad("not buyable")
		}
		switch {
		case h.Stock == nil:
			bad("catalog gives no stock figure")
		case h.Loss == nil:
			bad("catalog gives no loss allowance (lossNumber): the need cannot be established")
		case uncertain[h.LCSC]:
			bad("another line keeps this number and its stock or loss allowance is unknown: the remaining stock cannot be established")
		case *h.Stock-reserved[h.LCSC] < needNew(h):
			bad(fmt.Sprintf("catalog stock %d (%d already claimed) < need %d", *h.Stock, reserved[h.LCSC], needNew(h)))
		}
		if cm, why := PackageCompat(g.Footprint, h.Package); cm != CompatMatch {
			bad("package: " + string(cm) + " — " + why)
		}
		if !c.SameMPN {
			if kps == nil {
				bad("no verified key parameters for this part family; only a same-MPN part can be proposed")
			} else {
				if strings.ToLower(h.Category) != strings.ToLower(cur.Category) {
					bad("different catalog category: " + h.Category)
				}
				for _, kp := range kps {
					pc := compareParam(kp, cur, h)
					c.Params = append(c.Params, pc)
					switch pc.Status {
					case "fail":
						bad(fmt.Sprintf("%s %s: candidate %q vs current %q", pc.Name, pc.Rule, pc.Candidate, pc.Current))
					case "unverified":
						bad(fmt.Sprintf("%s unverified (current %q, candidate %q)", pc.Name, pc.Current, pc.Candidate))
					}
				}
			}
		}
		// A line in the extended library (whether or not it is also blocked) needs a basic replacement.
		if g.hasReason("extended-library") && !c.Base {
			bad("not a basic-library part")
		}
		// Hard failures (not buyable, params failing) are not worth listing as candidates at all.
		if !c.Eligible && hardReject(c) {
			continue
		}
		cands = append(cands, c)
	}
	sort.SliceStable(cands, func(a, b int) bool { return betterCandidate(cands[a], cands[b], found) })
	if len(cands) > in.Limit {
		cands = cands[:in.Limit]
	}
	g.Candidates = cands
	for _, c := range cands {
		if !c.Eligible {
			continue
		}
		why := "same MPN under another LCSC number"
		if !c.SameMPN {
			why = "all key parameters verified (" + paramSummary(c.Params) + ")"
		}
		if c.Base && !hasBase(g.Current) {
			why += "; basic library"
		}
		g.Proposal = &AltProposal{LCSC: c.LCSC, RequiresReview: true, Why: why}
		for _, h := range found {
			if h.LCSC == c.LCSC {
				reserved[c.LCSC] += needNew(&h)
			}
		}
		for _, ref := range g.Refs {
			rep.Assignments[ref] = c.LCSC
			rep.Expect[ref] = g.LCSC
		}
		return
	}
}

func hasBase(p *CatalogPart) bool {
	if p == nil {
		return false
	}
	b, _ := p.IsBase()
	return b
}

// hardReject: candidates that are plainly not replacements (a failed key
// parameter, an unbuyable or foreign-category part) are dropped from the listing.
func hardReject(c AltCandidate) bool {
	for _, p := range c.Params {
		if p.Status == "fail" {
			return true
		}
	}
	for _, u := range c.Unverified {
		if u == "not buyable" || strings.HasPrefix(u, "different catalog category") {
			return true
		}
	}
	return false
}

func paramSummary(ps []ParamCheck) string {
	var s []string
	for _, p := range ps {
		s = append(s, p.Name+" "+p.Rule+" "+p.Current)
	}
	return strings.Join(s, ", ")
}

// betterCandidate: eligible first, then same MPN, basic, preferred, tighter
// tolerance, lower unit price, lower C-number.
func betterCandidate(a, b AltCandidate, pool []CatalogPart) bool {
	if a.Eligible != b.Eligible {
		return a.Eligible
	}
	if a.SameMPN != b.SameMPN {
		return a.SameMPN
	}
	if a.Base != b.Base {
		return a.Base
	}
	if a.Preferred != b.Preferred {
		return a.Preferred
	}
	ta, aok := tolOf(a.LCSC, pool)
	tb, bok := tolOf(b.LCSC, pool)
	if aok && bok && ta != tb {
		return ta < tb
	}
	if a.UnitPrice != nil && b.UnitPrice != nil && *a.UnitPrice != *b.UnitPrice {
		return *a.UnitPrice < *b.UnitPrice
	}
	return a.LCSC < b.LCSC
}

func tolOf(code string, pool []CatalogPart) (float64, bool) {
	for i := range pool {
		if pool[i].LCSC == code {
			return toleranceOf(&pool[i])
		}
	}
	return 0, false
}
