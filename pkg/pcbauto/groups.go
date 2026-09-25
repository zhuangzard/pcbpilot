package pcbauto

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Schematic module ownership → PCB blocks.
//
// buildBlocks infers which core each auxiliary serves from nets alone, and a
// rail shared by several ICs is ambiguous: on the ESP32 demo the buck's 22 µF
// output capacitor (+3V3) was handed to the USB-UART bridge as a decoupler and
// the buck's 100 nF output cap to the Wi-Fi module, because +3V3 reaches all
// three. The schematic already records the designer's answer — each part sits
// in exactly one module (core + dedicated periphery). ApplyGroups makes that
// ownership authoritative for the placer:
//
//   - a member whose inferred core differs moves to the group's core; it keeps
//     a pin tether when it shares a non-ground net with the core (a cap on a
//     core rail pin becomes that pin's decap) and otherwise tethers to the
//     group members it shares nets with (role chain);
//   - port protection (ESD/TVS/fuse) is NOT moved: it belongs at the connector
//     pin it clamps whatever schematic page drew it;
//   - a group with no core-kind part (a lone button, an LED + resistor) keeps
//     the inferred owners — there is nothing to tether to.
//
// Every applied or skipped decision is returned as a note for the report.

// Group is one schematic module: its core (optional; else the member with
// the most pads that is a core kind) and all its parts.
type Group struct {
	ID      string   `json:"id"`
	Core    string   `json:"core,omitempty"`
	Members []string `json:"members"`
}

// ParseGroups reads either {"groups":[{id,core,members}]} or a schematic
// composition ({"modules":[{id, placements:[{designator}]}]}).
func ParseGroups(raw []byte) ([]Group, error) {
	var doc struct {
		Groups  []Group `json:"groups"`
		Modules []struct {
			ID         string `json:"id"`
			Core       string `json:"core"`
			Placements []struct {
				Designator string `json:"designator"`
			} `json:"placements"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("groups: %w", err)
	}
	out := append([]Group(nil), doc.Groups...)
	for _, m := range doc.Modules {
		g := Group{ID: m.ID, Core: m.Core}
		for _, p := range m.Placements {
			if p.Designator != "" {
				g.Members = append(g.Members, p.Designator)
			}
		}
		out = append(out, g)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("groups: no groups or modules found")
	}
	return out, nil
}

// ApplyGroups re-homes auxiliaries to their schematic module's core.
func ApplyGroups(c *Circuit, b *Board, an *Analysis, groups []Group) []string {
	var notes []string
	blockByCore := map[string]*Block{}
	blockByID := map[string]*Block{}
	for _, bl := range c.Blocks {
		blockByID[bl.ID] = bl
		if bl.Core != "" {
			blockByCore[bl.Core] = bl
		}
	}
	seen := map[string]string{}
	for _, g := range groups {
		core := g.Core
		if core == "" {
			best := -1
			for _, ref := range g.Members {
				if p := b.Part(ref); p != nil && blockByCore[ref] != nil && len(p.Pads) > best {
					core, best = ref, len(p.Pads)
				}
			}
		}
		target := blockByCore[core]
		if target == nil {
			notes = append(notes, fmt.Sprintf("group %s: no core part — members keep their inferred owners", g.ID))
			continue
		}
		corePart := b.Part(core)
		for _, ref := range g.Members {
			if ref == core {
				continue
			}
			if prev, dup := seen[ref]; dup {
				notes = append(notes, fmt.Sprintf("group %s: %s already in group %s — ignored", g.ID, ref, prev))
				continue
			}
			seen[ref] = g.ID
			p := b.Part(ref)
			if p == nil {
				notes = append(notes, fmt.Sprintf("group %s: %s not on the board", g.ID, ref))
				continue
			}
			from := blockByID[c.BlockOf[ref]]
			if from == target {
				continue
			}
			if from != nil && memberRole(from, ref) == "protection" {
				notes = append(notes, fmt.Sprintf("group %s: %s stays at %s (port protection belongs at the connector pin it clamps)", g.ID, ref, from.Core))
				continue
			}
			m := groupMember(c, b, an, p, corePart, target, g)
			was := "unassigned"
			if from != nil {
				removeMember(from, ref)
				if from.Core != "" {
					was = from.Core
				}
			}
			notes = append(notes, fmt.Sprintf("group %s: %s %s → %s (%s)", g.ID, ref, was, core, m.Role))
			target.Parts = append(target.Parts, ref)
			target.Members = append(target.Members, m)
			c.BlockOf[ref] = target.ID
		}
	}
	// Drop blocks emptied by the moves (B-MISC), keep order otherwise.
	kept := c.Blocks[:0]
	for _, bl := range c.Blocks {
		if bl.Core == "" && len(bl.Parts) == 0 {
			continue
		}
		if len(bl.Parts) > 1 && bl.Core != "" {
			sort.Strings(bl.Parts[1:])
		}
		sortMembers(bl)
		kept = append(kept, bl)
	}
	c.Blocks = kept
	return notes
}

func memberRole(bl *Block, ref string) string {
	for _, m := range bl.Members {
		if m.Ref == ref {
			return m.Role
		}
	}
	return ""
}

func removeMember(bl *Block, ref string) {
	parts := bl.Parts[:0]
	for _, r := range bl.Parts {
		if r != ref {
			parts = append(parts, r)
		}
	}
	bl.Parts = parts
	ms := bl.Members[:0]
	for _, m := range bl.Members {
		if m.Ref != ref {
			ms = append(ms, m)
		}
	}
	bl.Members = ms
}

// sharedCorePad is the core pad on a non-ground net p also touches.
func sharedCorePad(an *Analysis, b *Board, p, core *Part) *Pad {
	for _, pd := range p.Pads {
		if pd.Net == "" || an.Plan(pd.Net, b.Rules).Role == RoleGround {
			continue
		}
		for _, cp := range core.Pads {
			if cp.Net == pd.Net {
				return cp
			}
		}
	}
	return nil
}

// groupMember decides how a re-homed part is tethered inside its module.
//
//   - it touches a core pad on a non-ground net: that pin — decap when it
//     decouples that rail, power-path when the core is a connector and the
//     net a power rail (input OR-ing diodes, fuses' neighbours), else signal;
//   - otherwise it hangs off another member on a shared net (a buck's output
//     caps on the inductor's +3V3 end): power-stage inside a converter block
//     for C/L/D parts, else group.
func groupMember(c *Circuit, b *Board, an *Analysis, p, core *Part, target *Block, g Group) Member {
	why := "schematic module " + g.ID
	if pin := sharedCorePad(an, b, p, core); pin != nil {
		m := Member{Ref: p.Ref, Role: "signal", Pin: pin.Key(), Why: why + ": on " + pin.Key()}
		switch {
		case c.isDecap(b, an, p) && pin.Net == c.decapRail(b, an, p):
			m.Role, m.Why = "decap", why+": decouples "+pin.Net+" at "+pin.Key()
		case c.Kinds[core.Ref] == KindConnector && an.Plan(pin.Net, b.Rules).Role == RolePower:
			m.Role, m.Why = "power-path", why+": power path from "+pin.Key()+" ("+pin.Net+")"
		}
		return m
	}
	stage := false
	for _, mm := range target.Members {
		if mm.Role == "power-stage" {
			stage = true
		}
	}
	// Prefer a power-stage member, then any tethered member, on a shared net.
	var best *Pad
	bestRank := 1 << 30
	for _, mm := range target.Members {
		q := b.Part(mm.Ref)
		if q == nil || q == p {
			continue
		}
		for _, pd := range p.Pads {
			if pd.Net == "" || an.Plan(pd.Net, b.Rules).Role == RoleGround {
				continue
			}
			for _, qd := range q.Pads {
				if qd.Net == pd.Net && roleRank(mm.Role) < bestRank {
					best, bestRank = qd, roleRank(mm.Role)
				}
			}
		}
	}
	if best == nil {
		return Member{Ref: p.Ref, Role: "chain", Why: why}
	}
	m := Member{Ref: p.Ref, Role: "group", Pin: best.Key(), Why: why + ": next to " + best.Key() + " (" + best.Net + ")"}
	switch c.Kinds[p.Ref] {
	case KindCapacitor, KindInductor, KindDiode:
		if stage {
			m.Role = "power-stage"
		}
	}
	return m
}
