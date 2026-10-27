package kicad

// schnet_identity.go — the identity of each net of a kicad-cli XML netlist
// (pkg/netname): KiCad's full name, escaped as on the board. The XML writes
// names unescaped, so "/pic_sockets/VCC_PIC" may be the sub-sheet label
// "VCC_PIC" (identity "/pic_sockets/VCC_PIC") or a root label
// "pic_sockets/VCC_PIC" ("/pic_sockets{slash}VCC_PIC"). The reading comes
// from label evidence only: exactly one reading whose sheet file has a
// label, hierarchical label or sheet pin with that text. The sheets of the
// net's pins prove nothing (a net crosses sheets through hierarchical
// ports), so they are not used. No evidence, several readings with
// evidence, or two nets with one identity is ErrNetAmbiguous: every
// consumer then stops instead of matching a guessed (possibly shorted) net.

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/pkg/netname"
)

// ErrNetAmbiguous: a net's sheet path cannot be told from the schematic.
var ErrNetAmbiguous = errors.New("KiCad net name is ambiguous")

var (
	reSchLabelText = regexp.MustCompile(`\((?:label|hierarchical_label) "((?:[^"\\]|\\.)*)"`)
	// Sheet pins ("(pin "NAME" input …"): library and instance pins never
	// carry an electrical type right after a quoted name.
	reSchSheetPin = regexp.MustCompile(`\(pin "((?:[^"\\]|\\.)*)" (?:input|output|bidirectional|tri_state|passive)\b`)
)

// sheetNetTexts are the texts that name a net local to a sheet file —
// local and hierarchical labels, sheet pins — unescaped; nil when the file
// cannot be read (no evidence).
func sheetNetTexts(path string) map[string]bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	unq := strings.NewReplacer(`\"`, `"`, `\\`, `\`)
	out := map[string]bool{}
	for _, re := range []*regexp.Regexp{reSchLabelText, reSchSheetPin} {
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			out[netname.Unescape(unq.Replace(m[1]))] = true
		}
	}
	return out
}

// netIdentities rewrites every net name to its identity. files maps sheet
// paths to their sheet files (label evidence; may be empty).
func (nl *SchNetlist) netIdentities(files map[string]string) error {
	cache := map[string]map[string]bool{}
	texts := func(sheet string) map[string]bool {
		f, ok := files[sheet]
		if !ok {
			return nil
		}
		if t, seen := cache[f]; seen {
			return t
		}
		cache[f] = sheetNetTexts(f)
		return cache[f]
	}
	var errs []string
	byID := map[string]string{} // identity → XML net code
	for i := range nl.Nets {
		n := &nl.Nets[i]
		cands := netname.Candidates(n.Name, nl.Sheets)
		prefix := ""
		switch len(cands) {
		case 0:
		case 1:
			prefix = cands[0]
		default:
			var why string
			if prefix, why = decideSheet(n, cands, texts); prefix == "" {
				errs = append(errs, why)
				continue
			}
		}
		id := netname.Join(prefix, n.Name)
		if code, dup := byID[id]; dup {
			errs = append(errs, fmt.Sprintf("nets %s and %s both read as %q", code, n.Code, id))
			continue
		}
		byID[id] = n.Code
		n.Name = id
	}
	if len(errs) > 0 {
		return fmt.Errorf("%w: %s (fix: give these nets distinct names in the schematic — kicad-cli's XML writes a root label \"Sheet/NAME\" and the label \"NAME\" of sub-sheet Sheet identically, so their pins cannot be matched with the board)", ErrNetAmbiguous, strings.Join(errs, "; "))
	}
	return nil
}

// decideSheet picks the sheet path of net n among cands (each a reading
// of its XML name): the one reading whose sheet file has a label /
// hierarchical label / sheet pin with the rest of the name, every
// candidate's file having been read. "" and why otherwise.
func decideSheet(n *SchNet, cands []string, texts func(string) map[string]bool) (string, string) {
	var viable, unread []string
	for _, c := range cands {
		t := texts(c)
		switch {
		case t == nil:
			unread = append(unread, c)
		case t[n.Name[len(c):]]:
			viable = append(viable, c)
		}
	}
	if len(unread) == 0 && len(viable) == 1 {
		return viable[0], ""
	}
	set := viable
	if len(unread) > 0 || len(viable) == 0 {
		set = cands
	}
	var reads []string
	for _, c := range set {
		reads = append(reads, fmt.Sprintf("sheet %s label %q", c, n.Name[len(c):]))
	}
	sort.Strings(reads)
	why := "labels on several sheets fit"
	switch {
	case len(unread) > 0:
		why = "sheet files " + strings.Join(unread, ", ") + " not readable"
	case len(viable) == 0:
		why = "no sheet has such a label"
	}
	return "", fmt.Sprintf("net %s %q could be %s (%s)", n.Code, n.Name, strings.Join(reads, " or "), why)
}
