package kicad

// pcbsync_refs.go — what a net rename would leave behind: the board's
// copper moves with the net, but the project's netclass assignments and
// patterns and the custom rules (.kicad_dru) name nets by text. v0.11
// refuses such a rename instead of rewriting those files.

import (
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"
)

// ErrRenameReferenced: a planned net rename would orphan a netclass
// assignment, a netclass pattern or a custom rule naming the old net.
const ErrRenameReferenced = "E_RENAME_REFERENCED"

// RenameReferences lists, per planned renameNet, every reference to the
// old name in the project (net_settings.netclass_assignments keys,
// net_settings.netclass_patterns whose pattern matches the old name but not
// the new one) and in the custom rules (the old name quoted). A project
// that is not JSON is itself an error: its references cannot be checked.
func RenameReferences(pro, dru []byte, p *SyncPlan) []SyncError {
	var renames [][2]string
	for _, o := range p.Ops {
		if o.Op == "renameNet" && !o.Info {
			renames = append(renames, [2]string{o.From, o.To})
		}
	}
	if len(renames) == 0 {
		return nil
	}
	var ns struct {
		NetSettings struct {
			Assignments map[string]json.RawMessage `json:"netclass_assignments"`
			Patterns    []struct {
				Netclass string `json:"netclass"`
				Pattern  string `json:"pattern"`
			} `json:"netclass_patterns"`
		} `json:"net_settings"`
	}
	var out []SyncError
	if len(pro) > 0 {
		if err := json.Unmarshal(pro, &ns); err != nil {
			return []SyncError{{Code: ErrRenameReferenced, Detail: "the .kicad_pro is not JSON, so its netclass assignments cannot be checked before a net rename: " + err.Error(),
				Fix: "repair the project file, or pass --rename-nets=false"}}
		}
	}
	for _, r := range renames {
		from, to := r[0], r[1]
		var where []string
		if _, ok := ns.NetSettings.Assignments[from]; ok {
			where = append(where, "netclass assignment of "+from+" in the .kicad_pro")
		}
		for _, pt := range ns.NetSettings.Patterns {
			if wildMatch(pt.Pattern, from) && !wildMatch(pt.Pattern, to) {
				where = append(where, fmt.Sprintf("netclass pattern %q (class %s) in the .kicad_pro", pt.Pattern, pt.Netclass))
			}
		}
		if t := string(dru); t != "" && (strings.Contains(t, "'"+from+"'") || strings.Contains(t, `"`+from+`"`)) {
			where = append(where, "a rule naming "+from+" in the .kicad_dru")
		}
		if len(where) == 0 {
			continue
		}
		sort.Strings(where)
		out = append(out, SyncError{Code: ErrRenameReferenced, Detail: fmt.Sprintf("renaming net %s to %s would leave %s pointing at the old name",
			from, to, strings.Join(where, "; ")),
			Fix: "update those references to " + to + " in KiCad (Board Setup > Net Classes / Custom Rules) and sync again, or pass --rename-nets=false"})
	}
	return out
}

// wildMatch is KiCad's netclass pattern wildcard match (* and ?), case
// sensitive like net names.
func wildMatch(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	// path.Match treats '/' specially; net names contain '/', so escape the
	// pattern's other metacharacters and match on a '/'-free mapping.
	const slash = "\x00"
	p := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(strings.ReplaceAll(pattern, "/", slash))
	ok, err := path.Match(p, strings.ReplaceAll(name, "/", slash))
	return err == nil && ok
}
