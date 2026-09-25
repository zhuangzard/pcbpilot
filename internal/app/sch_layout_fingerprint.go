package app

import (
	"regexp"
	"strings"
)

// A conflict fingerprint names WHAT blocked a layout attempt: a naming lead
// (net + pin), a direct route (net) or a placement (component). Two attempts
// with the same fingerprint made no progress; spending more on the same
// search is how the stress run burned 65-1221 s per unsolvable merged zone.
var schematicConflictPatterns = []struct {
	kind string
	re   *regexp.Regexp
}{
	{"naming", regexp.MustCompile(`net (\S+) has no safe naming lead for island at pin (\S+) `)},
	{"route", regexp.MustCompile(`cannot route direct net (\S+) `)},
	{"place", regexp.MustCompile(`component (\S+) \(\S+\) placement search`)},
}

// schematicConflictFingerprint returns the LAST reported conflict in the
// error chain (the most recent observation), or "" when none is named.
func schematicConflictFingerprint(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	best, at := "", -1
	for _, p := range schematicConflictPatterns {
		for _, m := range p.re.FindAllStringSubmatchIndex(msg, -1) {
			if m[0] > at {
				parts := []string{p.kind}
				for i := 2; i+1 < len(m); i += 2 {
					parts = append(parts, msg[m[i]:m[i+1]])
				}
				best, at = strings.Join(parts, ":"), m[0]
			}
		}
	}
	return best
}
