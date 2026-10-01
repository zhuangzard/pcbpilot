package apidoc

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ParseDTS is the Go port of gen.py: it walks the `declare global { class X {…} }`
// blocks of @jlceda/pro-api-types index.d.ts and returns one Method per runtime
// `eda.<prop>.<method>` (only classes reachable from the `class EDA` surface
// map, named by their runtime property; overloads de-duplicated by signature).
// TestParseDTSMatchesGenPy keeps the two implementations equivalent.
func ParseDTS(data []byte) ([]Method, error) {
	var (
		classRE     = regexp.MustCompile(`^\s*class\s+([A-Za-z0-9_$]+)`)
		methodRE    = regexp.MustCompile(`^\s*(?:(public|private|protected)\s+)?(?:static\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*(?:<[^(]*>)?\s*\(`)
		modifierRE  = regexp.MustCompile(`^(?:public|static)\s+`)
		stabilityRE = regexp.MustCompile(`@(alpha|beta|deprecated|internal)\b`)
		edaPropRE   = regexp.MustCompile(`^\s*(?:(?:public|readonly)\s+)*([a-z][A-Za-z0-9_]*):\s*([A-Z][A-Za-z0-9_$|\s]*[A-Za-z0-9_$]);`)
	)
	skip := map[string]bool{"constructor": true, "if": true, "for": true, "while": true,
		"switch": true, "catch": true, "function": true, "return": true}

	type member struct{ method, sig, summary, stability string }
	byClass := map[string][]member{}
	type prop struct{ name, typeExpr string }
	var edaProps []prop
	var curCls string
	var docSummary, docStability string
	inDoc := false
	var docLines []string

	// pending is a multi-line member signature being collected (0.4 splits
	// object types over many lines); it completes when brackets balance.
	type pendingSig struct {
		cls   string
		idx   int
		parts []string
		bal   int
	}
	var pending *pendingSig
	finish := func(p *pendingSig) {
		byClass[p.cls][p.idx].sig = normalizeSig(p.parts)
	}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		stripped := strings.TrimSpace(line)
		if pending != nil {
			pending.parts = append(pending.parts, stripped)
			pending.bal += bracketBalance(stripped)
			if pending.bal <= 0 || len(pending.parts) > 200 {
				finish(pending)
				pending = nil
			}
			continue
		}
		if m := classRE.FindStringSubmatch(line); m != nil {
			curCls = m[1]
			docSummary, docStability, inDoc, docLines = "", "", false, nil
			continue
		}
		if curCls == "EDA" {
			if pm := edaPropRE.FindStringSubmatch(line); pm != nil {
				edaProps = append(edaProps, prop{pm[1], pm[2]})
				continue
			}
		}
		if strings.HasPrefix(stripped, "/**") {
			inDoc = true
			docLines = nil
			if strings.Contains(stripped, "*/") {
				inDoc = false
			}
			continue
		}
		if inDoc {
			docLines = append(docLines, stripped)
			if strings.Contains(stripped, "*/") {
				inDoc = false
				joined := strings.Join(docLines, " ")
				docStability = ""
				if s := stabilityRE.FindStringSubmatch(joined); s != nil {
					docStability = s[1]
				}
				docSummary = ""
				for _, dl := range docLines {
					t := strings.TrimSpace(strings.TrimLeft(dl, "*"))
					if t != "" && !strings.HasPrefix(t, "@") && t != "/" && !strings.HasSuffix(t, "*/") && t != "*/" {
						docSummary = t
						break
					}
				}
			}
			continue
		}
		if curCls != "" {
			if mm := methodRE.FindStringSubmatch(line); mm != nil && !skip[mm[2]] && mm[1] != "private" && mm[1] != "protected" {
				first := modifierRE.ReplaceAllString(stripped, "")
				byClass[curCls] = append(byClass[curCls], member{mm[2], "", docSummary, docStability})
				p := &pendingSig{cls: curCls, idx: len(byClass[curCls]) - 1, parts: []string{first}, bal: bracketBalance(first)}
				if p.bal <= 0 {
					finish(p)
				} else {
					pending = p
				}
			}
			if stripped != "" && !strings.HasPrefix(stripped, "*") {
				docSummary, docStability = "", ""
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(edaProps) == 0 {
		return nil, errors.New("no `class EDA` surface map found — not a pro-api-types index.d.ts, or its layout changed")
	}
	seen := map[string]bool{}
	var out []Method
	for _, p := range edaProps {
		ns := "eda." + p.name
		for _, cls := range strings.Split(p.typeExpr, "|") {
			cls = strings.TrimSpace(cls)
			for _, r := range byClass[cls] {
				key := ns + "\x00" + r.method + "\x00" + r.sig
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, Method{NS: ns, Method: r.method, Sig: r.sig, Summary: r.summary, Stability: r.stability})
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("parsed %d eda properties but no methods", len(edaProps))
	}
	return out, nil
}

var (
	sigSpaceRE = regexp.MustCompile(`\s+`)
	sigSemiRE  = regexp.MustCompile(`\s*;\s*}`)
	sigCommaRE = regexp.MustCompile(`,\s*([)}\]])`)
)

func bracketBalance(s string) int {
	return strings.Count(s, "(") + strings.Count(s, "{") + strings.Count(s, "[") -
		strings.Count(s, ")") - strings.Count(s, "}") - strings.Count(s, "]")
}

// normalizeSig joins a (possibly multi-line) declaration into one comparable
// line; identical rules to gen.py normalize_sig.
func normalizeSig(parts []string) string {
	sig := strings.TrimSpace(sigSpaceRE.ReplaceAllString(strings.Join(parts, " "), " "))
	sig = sigSemiRE.ReplaceAllString(sig, " }")
	return sigCommaRE.ReplaceAllString(sig, "$1")
}
