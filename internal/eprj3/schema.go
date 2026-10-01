package eprj3

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// The vendored schemas are a subset of easyeda/easyeda-format-skill (MIT), see
// schemas/SOURCE.json and schemas/LICENSE. Regenerate with vendor_schemas.py.
//
//go:embed schemas/*.json
var schemaFS embed.FS

// schemaNode is the JSON-Schema subset the official schemas actually use
// (checked over all 297 files at the pinned commit: no $ref / oneOf / allOf /
// additionalProperties / const / format).
type schemaNode struct {
	Type       json.RawMessage        `json:"type"`
	Properties map[string]*schemaNode `json:"properties"`
	Required   []string               `json:"required"`
	Enum       []any                  `json:"enum"`
	Items      *schemaNode            `json:"items"`
	Minimum    *float64               `json:"minimum"`
	Maximum    *float64               `json:"maximum"`
	Pattern    string                 `json:"pattern"`
	AnyOf      []*schemaNode          `json:"anyOf"`

	re *regexp.Regexp
}

var (
	schemaOnce  sync.Once
	schemaCache map[string]*schemaNode
	schemaErr   error
)

func loadSchemas() (map[string]*schemaNode, error) {
	schemaOnce.Do(func() {
		schemaCache = map[string]*schemaNode{}
		entries, err := schemaFS.ReadDir("schemas")
		if err != nil {
			schemaErr = err
			return
		}
		for _, e := range entries {
			name := strings.TrimSuffix(e.Name(), ".json")
			if name == "SOURCE" {
				continue
			}
			raw, err := schemaFS.ReadFile(path.Join("schemas", e.Name()))
			if err != nil {
				schemaErr = err
				return
			}
			var n schemaNode
			if err := json.Unmarshal(raw, &n); err != nil {
				schemaErr = fmt.Errorf("schema %s: %w", name, err)
				return
			}
			if err := compilePatterns(&n); err != nil {
				schemaErr = fmt.Errorf("schema %s: %w", name, err)
				return
			}
			schemaCache[name] = &n
		}
	})
	return schemaCache, schemaErr
}

func compilePatterns(n *schemaNode) error {
	if n == nil {
		return nil
	}
	if n.Pattern != "" {
		re, err := regexp.Compile(n.Pattern)
		if err != nil {
			return err
		}
		n.re = re
	}
	for _, p := range n.Properties {
		if err := compilePatterns(p); err != nil {
			return err
		}
	}
	for _, a := range n.AnyOf {
		if err := compilePatterns(a); err != nil {
			return err
		}
	}
	return compilePatterns(n.Items)
}

// schemaFor maps (docType, record type) to a vendored schema name. Keep in
// sync with SCHEMAS in vendor_schemas.py. Record types not listed are counted
// but not schema-validated (reported as unvalidated, never silently "valid").
func schemaFor(docType, recType string) string {
	if recType == "DOCHEAD" {
		return "t-doc-head"
	}
	m := map[string]map[string]string{
		"SCH_PAGE": {"META": "tm-sheet", "CANVAS": "t-sch-canvas", "COMPONENT": "tm-sch-component",
			"WIRE": "t-wire", "LINE": "t-sch-line", "ATTR": "t-sch-attr"},
		"SCH": {"META": "tm-schematic"},
		"PCB": {"META": "tm-pcb", "CANVAS": "t-canvas", "COMPONENT": "tm-pcb-component", "NET": "t-net",
			"VIA": "t-pcb-via", "LINE": "t-pcb-line", "ARC": "t-pcb-arc", "POUR": "t-pcb-pour",
			"POURED": "t-pcb-poured", "PAD_NET": "t-pad-net-wire", "ATTR": "t-pcb-attr", "PAD": "t-pcb-pad"},
		"PANEL": {"META": "tm-panel", "CANVAS": "t-panel-canvas"},
	}
	return m[docType][recType]
}

// violation is one schema mismatch at a JSON path.
type violation struct {
	Path string
	Rule string // required | type | enum | pattern | minimum | maximum | anyOf
	Msg  string
	// Value is the offending value (for documented-deviation matching).
	Value any
}

func validateValue(n *schemaNode, v any, p string, out *[]violation) {
	if n == nil {
		return
	}
	if len(n.AnyOf) > 0 {
		ok := false
		for _, alt := range n.AnyOf {
			var tmp []violation
			validateValue(alt, v, p, &tmp)
			if len(tmp) == 0 {
				ok = true
				break
			}
		}
		if !ok {
			*out = append(*out, violation{p, "anyOf", "matches none of the anyOf alternatives", v})
		}
	}
	if types := n.types(); len(types) > 0 && !typeMatches(types, v) {
		*out = append(*out, violation{p, "type", fmt.Sprintf("expected %s, got %s", strings.Join(types, "|"), jsonType(v)), v})
		return
	}
	if len(n.Enum) > 0 {
		found := false
		for _, e := range n.Enum {
			if jsonEqual(e, v) {
				found = true
				break
			}
		}
		if !found {
			*out = append(*out, violation{p, "enum", fmt.Sprintf("value %v not in enum", short(v)), v})
		}
	}
	switch x := v.(type) {
	case string:
		if n.re != nil && !n.re.MatchString(x) {
			*out = append(*out, violation{p, "pattern", fmt.Sprintf("%q does not match %s", x, n.Pattern), v})
		}
	case float64:
		if n.Minimum != nil && x < *n.Minimum {
			*out = append(*out, violation{p, "minimum", fmt.Sprintf("%v < minimum %v", x, *n.Minimum), v})
		}
		if n.Maximum != nil && x > *n.Maximum {
			*out = append(*out, violation{p, "maximum", fmt.Sprintf("%v > maximum %v", x, *n.Maximum), v})
		}
	case map[string]any:
		for _, req := range n.Required {
			if _, ok := x[req]; !ok {
				*out = append(*out, violation{join(p, req), "required", "required field missing", nil})
			}
		}
		keys := make([]string, 0, len(n.Properties))
		for k := range n.Properties {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if sub, ok := x[k]; ok {
				validateValue(n.Properties[k], sub, join(p, k), out)
			}
		}
	case []any:
		if n.Items != nil {
			for i, it := range x {
				validateValue(n.Items, it, fmt.Sprintf("%s[%d]", p, i), out)
			}
		}
	}
}

func (n *schemaNode) types() []string {
	if len(n.Type) == 0 {
		return nil
	}
	var one string
	if json.Unmarshal(n.Type, &one) == nil {
		return []string{one}
	}
	var many []string
	_ = json.Unmarshal(n.Type, &many)
	return many
}

func typeMatches(types []string, v any) bool {
	got := jsonType(v)
	for _, t := range types {
		if t == got || (t == "number" && got == "integer") {
			return true
		}
	}
	return false
}

func jsonType(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case string:
		return "string"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func jsonEqual(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func join(p, k string) string {
	if p == "" {
		return k
	}
	return p + "." + k
}

func short(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 60 {
		s = s[:57] + "..."
	}
	return s
}

// documentedDeviation reports whether a violation is a deviation the official
// schema itself documents as real editor output (so it is counted separately
// instead of drowning real findings). Each entry quotes its source.
func documentedDeviation(v violation) (string, bool) {
	// t-pcb-via.json / t-pcb-*.json `groupId` description (pinned commit):
	// ungrouped primitives are written as the NUMBER 0 although the declared
	// type is string; "放宽类型会波及 pro-* 仓的消费方 … 此处暂以说明为准".
	if v.Rule == "type" && (v.Path == "groupId" || strings.HasSuffix(v.Path, ".groupId")) {
		if f, ok := v.Value.(float64); ok && f == 0 {
			return "groupId written as number 0 for ungrouped primitives (documented in the official schema description)", true
		}
	}
	return "", false
}
