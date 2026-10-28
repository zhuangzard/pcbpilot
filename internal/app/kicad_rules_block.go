package app

// kicad_rules_block.go — the .kicad_dru block `kicad rules` owns, and the
// check `kicad route` makes against it.
//
// The block sits directly before the block bridge netclasses writes (the
// "route block"), so a later route run — which strips and re-appends its
// block at the end of the file — leaves the order, and so which rule KiCad
// applies (the last matching one), unchanged. It carries:
//
//	# request-sha256 <hex>    the shared derivation it was made from
//	# sizes-owned tracks=<nm,…> vias=<dia:drill,…>
//	(rule "pcbpilot-rules <class> outer" (layer outer) … track_width min/opt)
//	(rule "pcbpilot-rules <class> inner" (layer inner) … track_width min/opt)
//
// A netclass width is a routing default, not a minimum; these rules make the
// floor a DRC rule on every layer.

import (
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// rulesStamp is what a rules block records.
type rulesStamp struct {
	RequestSHA string
	Owned      kicad.SizeSet
}

// parseRulesStamp reads the stamp of the rules block of a .kicad_dru text
// (ok=false: no rules block).
func parseRulesStamp(dru string) (rulesStamp, bool) {
	_, blk, _, err := kicad.SplitDRU(dru)
	if err != nil || blk == "" {
		return rulesStamp{}, false
	}
	var st rulesStamp
	for _, l := range strings.Split(blk, "\n") {
		f := strings.Fields(strings.TrimPrefix(strings.TrimSpace(l), "#"))
		if len(f) < 2 {
			continue
		}
		switch f[0] {
		case "request-sha256":
			st.RequestSHA = f[1]
		case "sizes-owned":
			for _, kv := range f[1:] {
				k, v, _ := strings.Cut(kv, "=")
				for _, item := range strings.Split(v, ",") {
					if item == "" {
						continue
					}
					switch k {
					case "tracks":
						if n, err := strconv.ParseInt(item, 10, 64); err == nil {
							st.Owned.Tracks = append(st.Owned.Tracks, n)
						}
					case "vias":
						a, b, _ := strings.Cut(item, ":")
						d, e1 := strconv.ParseInt(a, 10, 64)
						r, e2 := strconv.ParseInt(b, 10, 64)
						if e1 == nil && e2 == nil {
							st.Owned.Vias = append(st.Owned.Vias, [2]int64{d, r})
						}
					}
				}
			}
		}
	}
	return st, st.RequestSHA != ""
}

// rulesBlockRule is one rule of the rules block.
type rulesBlockRule struct {
	Name    string `json:"name"`
	Class   string `json:"class"`
	Layer   string `json:"layer"` // outer | inner
	MinNm   int64  `json:"minNm"`
	OptNm   int64  `json:"optNm"`
	Comment string `json:"why"`
}

// rulesBlockRules are the track_width rules for the written classes: the
// floor is the class's intent minimum (the neck-down limit) when it has
// one, else the layer's full width; opt is the layer's width. Inner rules
// only on boards with inner layers.
func rulesBlockRules(classes []kicad.NetclassClassNM, copperLayers int) []rulesBlockRule {
	var out []rulesBlockRule
	for _, c := range classes {
		if !strings.HasPrefix(c.Name, "PP") || strings.HasPrefix(c.Name, "PPD_") || strings.HasPrefix(c.Name, "PPF_") {
			continue
		}
		q := c.NM
		floor := func(w int64) (int64, string) {
			if q.Min > 0 && q.Min < w {
				return q.Min, "intent minimum (neck-down limit)"
			}
			return w, "full width (the intent allows no neck-down)"
		}
		mn, why := floor(q.Track)
		out = append(out, rulesBlockRule{Name: kicad.DRURulesPrefix + c.Name + " outer", Class: c.Name, Layer: "outer", MinNm: mn, OptNm: q.Track, Comment: why})
		if copperLayers > 2 {
			inner := q.Inner
			if inner <= 0 {
				inner = q.Track
			}
			mn, why := floor(inner)
			out = append(out, rulesBlockRule{Name: kicad.DRURulesPrefix + c.Name + " inner", Class: c.Name, Layer: "inner", MinNm: mn, OptNm: inner, Comment: why})
		}
	}
	return out
}

// rulesBlockText renders the block (with markers, ending in a newline).
// It carries no plan revision (which hashes the input .kicad_dru): the
// same derivation must give the same bytes, so re-applying is a no-op.
func rulesBlockText(rules []rulesBlockRule, requestSHA string, owned kicad.SizeSet) string {
	var b strings.Builder
	b.WriteString(kicad.DRURulesBegin + "\n")
	b.WriteString("# owner: pcbpilot kicad rules (rules named \"" + kicad.DRURulesPrefix + "...\"); re-applying replaces this block\n")
	fmt.Fprintf(&b, "# request-sha256 %s\n", requestSHA)
	var ts, vs []string
	for _, t := range owned.Tracks {
		ts = append(ts, strconv.FormatInt(t, 10))
	}
	for _, v := range owned.Vias {
		vs = append(vs, fmt.Sprintf("%d:%d", v[0], v[1]))
	}
	fmt.Fprintf(&b, "# sizes-owned tracks=%s vias=%s\n", strings.Join(ts, ","), strings.Join(vs, ","))
	for _, r := range rules {
		fmt.Fprintf(&b, "(rule \"%s\"\n  (layer %s)\n  (condition \"A.hasNetclass('%s')\")\n  (constraint track_width (min %s) (opt %s)))\n",
			r.Name, r.Layer, r.Class, kicad.MmOfNm(r.MinNm), kicad.MmOfNm(r.OptNm))
	}
	b.WriteString(kicad.DRURulesEnd + "\n")
	return b.String()
}

// placeRulesBlock puts block into a .kicad_dru text: the old rules block is
// removed, and the new one goes right before the route block (at the end
// when there is none). user is the user part (outside both blocks).
func placeRulesBlock(dru, block string) (string, error) {
	// LF only: KiCad's Python writes the route block in text mode (CRLF on
	// Windows); the same rules must give the same bytes on every platform.
	dru = strings.ReplaceAll(dru, "\r\n", "\n")
	user, _, route, err := kicad.SplitDRU(dru)
	if err != nil {
		return "", err
	}
	text := strings.TrimRight(user, "\n")
	if text != "" {
		text += "\n"
	}
	if !strings.Contains(text, "(version") {
		text = "(version 1)\n" + text
	}
	return text + block + route, nil
}

// rulesTrackFloor resolves the track_width minimum KiCad applies to class
// cls on a layer kind (outer | inner) from the pcbpilot-owned rules of a
// parsed .kicad_dru: the last owned rule for that class and layer (KiCad
// applies the last matching rule). 0 = none.
func rulesTrackFloor(rules []kicad.DRURule, cls, layer string) (int64, string) {
	cond := "A.hasNetclass('" + cls + "')"
	var mn int64
	name := ""
	for _, r := range rules {
		if r.Block == "" || r.Condition != cond || (r.Layer != "" && r.Layer != layer) {
			continue
		}
		for _, c := range r.Constraints {
			if c.Type == "track_width" && c.Min != nil {
				mn, name = *c.Min, r.Name
			}
		}
	}
	return mn, name
}

// rulesStampCheck is kicad route's check of rules applied by kicad rules:
// when the input board's .kicad_dru has a rules block, route derives the
// rules again with kicad rules' width basis on route's own inputs (intent,
// --sim, --requirements, the board stackup); the request must be the one
// the block was made from, else the rules are stale and route stops. With
// no rules block nothing changes (route's own derivation, as before).
func (r *kicadRun) rulesStamp(ri kicadRulesInputs, inSnap *boardSnapshot) (kicadRulesInputs, error) {
	data, err := os.ReadFile(strings.TrimSuffix(r.o.pcb, ".kicad_pcb") + ".kicad_dru")
	if err != nil {
		return ri, nil
	}
	st, ok := parseRulesStamp(string(data))
	if !ok {
		return ri, nil
	}
	board, err := os.ReadFile(r.o.pcb)
	if err != nil {
		return ri, err
	}
	stack, _ := kicad.ReadStackupCopper(board)
	var sp *pcbauto.SimPower
	if r.o.sim != "" {
		raw, err := os.ReadFile(r.o.sim)
		if err != nil {
			return ri, err
		}
		if sp, err = pcbauto.ParseSim(raw); err != nil {
			return ri, err
		}
	}
	decl, _, err := loadDeclaredCurrents(r.o.requirements)
	if err != nil {
		return ri, err
	}
	widths, _ := planRulesWidths(r.in, sp, decl, copperBasis(stack, r.in))
	ri2 := deriveKicadRules(r.in, r.o.intent, widths)
	sum := map[string]any{"requestSha256": ri2.sha(), "appliedRequestSha256": st.RequestSHA}
	r.summary["kicadRules"] = sum
	if ri2.sha() != st.RequestSHA {
		sum["stale"] = true
		return ri, fmt.Errorf("RULES_STALE: the board carries rules from `kicad rules` (derivation %s) that no longer match the intent, --sim, --requirements and stackup given to route (derivation %s); rerun `pcbpilot kicad rules --pcb %s --intent %s --apply` with the same inputs, then route", short12(st.RequestSHA), short12(ri2.sha()), r.o.pcb, r.o.intent)
	}
	fmt.Fprintf(r.stderr, "rules: the board's kicad rules (derivation %s) match route's derivation\n", short12(st.RequestSHA))
	return ri2, nil
}

// viaCountNeed is the vias per layer transition a current needs at drill
// (mil): ceil(I / one via's ampacity).
func viaCountNeed(a, drillMil, tempRise float64) (int, float64) {
	per := pcbauto.ViaAmpacity(drillMil, pcbauto.DefaultViaPlatingMil, tempRise)
	if per <= 0 || a <= 0 {
		return 0, per
	}
	return int(math.Ceil(a/per - 1e-9)), per
}

func sortedInt64(v []int64) []int64 {
	out := append([]int64(nil), v...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
