package kicad

// pcbsync_refs.go — net renames against the project and rules. The board's
// copper moves with a renamed net, but the project's netclass assignments
// and patterns and the custom rules (.kicad_dru) select nets by text. v0.11
// is conservative: a rename is refused whenever any of them involves
// either name, or a rule selects nets in a way that cannot be evaluated
// literally. Nothing is rewritten.

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// ErrRenameReferenced: a planned net rename touches a netclass
// assignment, a netclass pattern or a custom rule.
const ErrRenameReferenced = "E_RENAME_REFERENCED"

// druNetCond is a literal net condition: A.NetName == 'X' / B.NetClass != "X".
var (
	druNetCond    = regexp.MustCompile(`\b[AB]\.(NetName|NetClass)\s*(==|!=)\s*(?:'([^']*)'|"([^"]*)")`)
	druNetMention = regexp.MustCompile(`NetName|NetClass|inDiffPair|isCoupledDiffPair|matches\s*\(|memberOf`)
)

// RenameReferences lists, per planned renameNet, why it is refused:
//   - a netclass_assignments entry for either name;
//   - a netclass_patterns pattern that matches either name, read the ways
//     KiCad may (wildcard * ?, and as a regular expression);
//   - a .kicad_dru that names either net, or selects nets / classes other
//     than by literal comparisons (matches(), wildcards in a literal,
//     diff-pair and membership functions): its effect on the renamed net
//     cannot be decided.
//
// A project that is not JSON is itself a refusal: it cannot be checked.
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
	fix := "update the netclass assignments / patterns and custom rules for the new name in KiCad (Board Setup > Net Classes / Custom Rules) and sync again, or pass --rename-nets=false to re-net the pads one by one instead"
	if len(pro) > 0 {
		if err := json.Unmarshal(pro, &ns); err != nil {
			return []SyncError{{Code: ErrRenameReferenced, Detail: "the .kicad_pro is not JSON, so its netclass assignments cannot be checked before a net rename: " + err.Error(), Fix: fix}}
		}
	}
	druText := string(dru)
	// Rule conditions that are not literal comparisons of a plain name.
	var opaque []string
	if strings.TrimSpace(druText) != "" {
		literal := 0
		for _, m := range druNetCond.FindAllStringSubmatch(druText, -1) {
			if strings.ContainsAny(m[3]+m[4], "*?") {
				opaque = append(opaque, strings.TrimSpace(m[0])+" (wildcard)")
				continue
			}
			literal++
		}
		mentions := druNetMention.FindAllString(druText, -1)
		if len(mentions) > literal {
			for _, m := range mentions {
				if m != "NetName" && m != "NetClass" {
					opaque = append(opaque, m)
				}
			}
			if len(opaque) == 0 {
				opaque = append(opaque, "a NetName / NetClass condition that is not a literal == / != comparison")
			}
		}
	}
	var out []SyncError
	for _, r := range renames {
		var where []string
		for _, name := range r {
			if _, ok := ns.NetSettings.Assignments[name]; ok {
				where = append(where, fmt.Sprintf("netclass assignment of %s in the .kicad_pro", name))
			}
			for _, pt := range ns.NetSettings.Patterns {
				if patternMatches(pt.Pattern, name) {
					where = append(where, fmt.Sprintf("netclass pattern %q (class %s) matches %s in the .kicad_pro", pt.Pattern, pt.Netclass, name))
				}
			}
			if druText != "" && strings.Contains(druText, name) {
				where = append(where, fmt.Sprintf("the .kicad_dru names %s", name))
			}
		}
		for _, o := range opaque {
			where = append(where, "the .kicad_dru selects nets in a way that cannot be checked literally: "+o)
		}
		if len(where) == 0 {
			continue
		}
		out = append(out, SyncError{Code: ErrRenameReferenced,
			Detail: fmt.Sprintf("net %s → %s is not renamed: %s", r[0], r[1], strings.Join(dedupe(where), "; ")), Fix: fix})
	}
	return out
}

func dedupe(ss []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// patternMatches reads a netclass pattern both ways KiCad may: as a
// wildcard (* ?) and as a regular expression.
func patternMatches(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	if wildMatch(pattern, name) {
		return true
	}
	re, err := regexp.Compile(pattern)
	return err == nil && re.MatchString(name)
}

// wildMatch is KiCad's wildcard match (* and ?), case sensitive like net
// names.
func wildMatch(pattern, name string) bool {
	// path.Match treats '/' specially; net names contain '/', so escape the
	// pattern's other metacharacters and match on a '/'-free mapping.
	const slash = "\x00"
	p := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(strings.ReplaceAll(pattern, "/", slash))
	ok, err := path.Match(p, strings.ReplaceAll(name, "/", slash))
	return err == nil && ok
}
