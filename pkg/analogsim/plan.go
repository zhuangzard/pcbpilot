package analogsim

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// PlanKind identifies the value-change plan document.
const PlanKind = "pcbpilot.schematic-value-plan"

// BuildPlan wraps the accepted changes into the typed plan (nil = no changes).
func BuildPlan(changes []Change) *Plan {
	if len(changes) == 0 {
		return nil
	}
	sort.SliceStable(changes, func(i, j int) bool {
		if changes[i].Block != changes[j].Block {
			return blockLess(changes[i].Block, changes[j].Block)
		}
		return refLess(changes[i].Ref, changes[j].Ref)
	})
	p := &Plan{SchemaVersion: SchemaVersion, Kind: PlanKind, Generator: Generator, RequiresUserConfirmation: true, Changes: changes,
		Apply: []string{
			"1. show the user every change (ref, old → new, reason, before/after metrics) and wait for an explicit yes — value changes are schematic edits",
			"2. pcbpilot --project <P> sch list --page <page> > sch-list.json   # fresh primitive IDs of the page(s) holding the refs",
			"3. pcbpilot sim analog compile-plan --plan plan.json --components sch-list.json --out value-playbook.json",
			"4. pcbpilot --project <P> apply value-playbook.json --dry-run, then pcbpilot --project <P> apply value-playbook.json",
		},
		AfterApply: []string{
			"pcbpilot sch save, then re-export sch connectivity / sch list and check the values read back",
			"pcbpilot sim analog … (confirm the new metrics), pcbpilot intent derive …, pcbpilot report design … (next report version)",
		},
	}
	for _, c := range changes {
		if c.NeedsPartSelection {
			p.Notes = append(p.Notes, fmt.Sprintf("%s → %s has no stocked part in standard-parts.json: select one first (pcbpilot lib by-lcsc / parts-select.py, hint %q) and fill changes[].part — a Value-only edit leaves the old LCSC/MPN on the BOM", c.Ref, c.To, c.SearchHint))
		}
	}
	return p
}

// ParsePlan decodes a plan file.
func ParsePlan(b []byte) (*Plan, error) {
	var p Plan
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	if p.Kind != PlanKind {
		return nil, fmt.Errorf("not a %s document (kind %q)", PlanKind, p.Kind)
	}
	if p.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("plan schemaVersion %d unsupported", p.SchemaVersion)
	}
	return &p, nil
}

// PlaybookOptions tune CompilePlaybook.
type PlaybookOptions struct {
	// AllowValueOnly compiles set-value changes as a Value attribute edit
	// (the LCSC/MPN stay those of the old part).
	AllowValueOnly bool
	Project        string
}

// CompilePlaybook turns a plan plus a fresh `sch list` into a `pcbpilot
// apply` playbook: schematic.component.replace (stocked part, keeps
// designator and uniqueId) or schematic.component.modify (Value only).
// Every step asks for confirmation.
func CompilePlaybook(p *Plan, components []map[string]any, o PlaybookOptions) (map[string]any, []string, error) {
	ids := map[string][]string{}
	for _, c := range components {
		des, _ := c["designator"].(string)
		id, _ := c["primitiveId"].(string)
		if des != "" && id != "" {
			ids[des] = append(ids[des], id)
		}
	}
	var steps []map[string]any
	var warns []string
	var missing []string
	for _, ch := range p.Changes {
		pids := ids[ch.Ref]
		if len(pids) == 0 {
			missing = append(missing, ch.Ref)
			continue
		}
		if len(pids) > 1 {
			return nil, nil, fmt.Errorf("%s appears %d times in the component list (multi-page duplicates?) — pass the list of the page that holds it", ch.Ref, len(pids))
		}
		t := true
		step := map[string]any{"id": "value-" + ch.Ref, "name": fmt.Sprintf("%s %s → %s (%s)", ch.Ref, ch.From, ch.To, ch.Block), "confirm": t}
		switch {
		case ch.Part != nil && ch.Part.LCSC != "":
			step["action"] = "schematic.component.replace"
			step["payload"] = map[string]any{"primitiveId": pids[0], "lcsc": ch.Part.LCSC}
		case o.AllowValueOnly:
			step["action"] = "schematic.component.modify"
			step["payload"] = map[string]any{"primitiveId": pids[0], "patch": map[string]any{"otherProperty": map[string]any{"Value": ch.To}}}
			warns = append(warns, ch.Ref+": Value-only edit — the LCSC/MPN still name the old part; fix the BOM before ordering")
		default:
			return nil, nil, fmt.Errorf("%s → %s has no stocked part (changes[].part): select one first or pass --allow-value-only", ch.Ref, ch.To)
		}
		steps = append(steps, step)
	}
	if len(missing) > 0 {
		return nil, nil, fmt.Errorf("refs not in the component list: %s (wrong page? pull a fresh sch list)", strings.Join(missing, ", "))
	}
	steps = append(steps, map[string]any{"id": "save", "action": "schematic.save", "payload": map[string]any{}})
	pb := map[string]any{
		"version": 1,
		"meta": map[string]any{"name": "analog value changes", "description": "compiled by pcbpilot sim analog compile-plan; every change was confirmed by the user before apply",
			"project": o.Project},
		"defaults": map[string]any{"continueOnError": false},
		"steps":    steps,
	}
	return pb, warns, nil
}

// ApplyPlanToDesign overrides the design's part values with the plan (what-if
// preview in memory; the schematic is not touched). Stocked parts also carry
// their MPN / LCSC.
func ApplyPlanToDesign(d *powersim.Design, p *Plan) (int, error) {
	byRef := map[string]*powersim.Part{}
	for _, part := range d.Parts {
		byRef[part.Ref] = part
	}
	n := 0
	for _, ch := range p.Changes {
		part := byRef[ch.Ref]
		if part == nil {
			return n, fmt.Errorf("what-if: %s not in the design", ch.Ref)
		}
		part.Value = ch.To
		if ch.Part != nil {
			if ch.Part.MPN != "" {
				part.MPN = ch.Part.MPN
			}
			part.LCSC = ch.Part.LCSC
		}
		n++
	}
	return n, nil
}
