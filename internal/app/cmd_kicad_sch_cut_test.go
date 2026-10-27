package app

import (
	"fmt"
	"regexp"
	"testing"
)

// sbCutSymbol removes the placed symbol of libID with unit from a sheet
// text (a test fixture edit pcbpilot itself refuses, e.g. on a reused sheet;
// KiCad-written files may have CRLF line ends).
func sbCutSymbol(t *testing.T, text, libID string, unit int) string {
	t.Helper()
	re := regexp.MustCompile(`\n\t\(symbol\r?\n\t\t\(lib_id "` + regexp.QuoteMeta(libID) + `"\)`)
	unitRe := regexp.MustCompile(fmt.Sprintf(`\n\t\t\(unit %d\)\r?\n`, unit))
	for _, m := range re.FindAllStringIndex(text, -1) {
		beg := m[0] + 1
		depth, end := 0, -1
		for i := beg; i < len(text) && end < 0; i++ {
			switch text[i] {
			case '(':
				depth++
			case ')':
				if depth--; depth == 0 {
					end = i + 1
				}
			case '"':
				for i++; i < len(text) && text[i] != '"'; i++ {
					if text[i] == '\\' {
						i++
					}
				}
			}
		}
		if end > 0 && unitRe.MatchString(text[beg:end]) {
			return text[:m[0]] + text[end:]
		}
	}
	t.Fatalf("no %s unit %d in the sheet", libID, unit)
	return text
}
