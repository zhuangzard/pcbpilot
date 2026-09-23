// Package apidoc embeds a searchable index of the official `eda.*` API surface,
// generated from @jlceda/pro-api-types (see gen.py). It powers `pcbpilot api
// search/ls`, the self-discovery loop for "what eda.* call do I need" when
// scoping a new typed action or a debug.exec_js snippet.
package apidoc

import (
	_ "embed"
	"encoding/json"
	"sort"
	"strings"
)

//go:embed api-index.json
var indexJSON []byte

// Method is one `eda.<ns>.<method>` entry.
type Method struct {
	NS        string `json:"ns"`
	Method    string `json:"method"`
	Sig       string `json:"sig"`
	Summary   string `json:"summary"`
	Stability string `json:"stability"`
}

type index struct {
	Source         string   `json:"source"`
	NamespaceCount int      `json:"namespaceCount"`
	MethodCount    int      `json:"methodCount"`
	Records        []Method `json:"records"`
}

var loaded index

func init() {
	// A malformed embed is a build-time-fixable bug; fail loud-but-empty rather
	// than panic so the rest of the CLI still works.
	_ = json.Unmarshal(indexJSON, &loaded)
}

// Source returns the upstream package the index was generated from.
func Source() string { return loaded.Source }

// Counts returns namespace and method totals.
func Counts() (namespaces, methods int) {
	return loaded.NamespaceCount, loaded.MethodCount
}

// Namespaces returns every `eda.*` namespace, optionally filtered by a
// case-insensitive substring, sorted.
func Namespaces(filter string) []string {
	f := strings.ToLower(filter)
	set := map[string]int{}
	for _, m := range loaded.Records {
		if f == "" || strings.Contains(strings.ToLower(m.NS), f) {
			set[m.NS]++
		}
	}
	out := make([]string, 0, len(set))
	for ns := range set {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// MethodsIn returns the methods of one namespace (exact, case-insensitive),
// sorted by method name.
func MethodsIn(ns string) []Method {
	target := strings.ToLower(ns)
	var out []Method
	for _, m := range loaded.Records {
		if strings.ToLower(m.NS) == target {
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Method < out[j].Method })
	return out
}

// Search ranks methods by how well they match the space-separated query terms.
// Every term must appear somewhere (ns/method/summary/sig); results are ranked
// by a method-name-weighted score, then alphabetically. limit<=0 means no cap.
func Search(query string, limit int) []Method {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return nil
	}
	type scored struct {
		m     Method
		score int
	}
	var hits []scored
	for _, m := range loaded.Records {
		ns := strings.ToLower(m.NS)
		method := strings.ToLower(m.Method)
		summary := strings.ToLower(m.Summary)
		hay := ns + " " + method + " " + summary + " " + strings.ToLower(m.Sig)
		score, ok := 0, true
		for _, t := range terms {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
			switch {
			case method == t:
				score += 100
			case strings.Contains(method, t):
				score += 40
			case strings.Contains(ns, t):
				score += 10
			default:
				score += 5 // summary / sig only
			}
		}
		if ok {
			hits = append(hits, scored{m, score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		if hits[i].m.NS != hits[j].m.NS {
			return hits[i].m.NS < hits[j].m.NS
		}
		return hits[i].m.Method < hits[j].m.Method
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	out := make([]Method, len(hits))
	for i, h := range hits {
		out[i] = h.m
	}
	return out
}
