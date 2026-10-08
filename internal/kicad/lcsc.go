package kicad

// lcsc.go — LCSC / JLC part numbers on KiCad designs: reading them from the
// board and schematic, gating fab output on them, and writing them back
// (`pcbpilot kicad lcsc --set`, through the embedded fab.py).

import (
	"bytes"
	_ "embed"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// LCSCFieldNames are the symbol/footprint field names accepted as the LCSC
// number, in priority order. "LCSC Part #" is what JLC's KiCad 10 guide tells
// users to add; the others are what JLC's own docs, the JLC Fabrication
// Toolkit and EasyEDA→KiCad converters write.
var LCSCFieldNames = []string{"LCSC Part #", "LCSC", "LCSC Part", "JLCPCB Part #"}

// DefaultLCSCField is the field --set creates when a part has none yet.
const DefaultLCSCField = "LCSC Part #"

var lcscRe = regexp.MustCompile(`^C[0-9]{1,12}$`)

// ValidLCSC reports whether s looks like an LCSC part number (C + digits).
func ValidLCSC(s string) bool { return lcscRe.MatchString(s) }

// LookupLCSC returns the first non-empty accepted LCSC field (case-insensitive
// name match) and the field name it came from.
func LookupLCSC(fields map[string]string) (value, field string) {
	for _, want := range LCSCFieldNames {
		for k, v := range fields {
			if strings.EqualFold(strings.TrimSpace(k), want) && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v), k
			}
		}
	}
	return "", ""
}

// Part is one placed footprint with its effective BOM data.
type Part struct {
	Ref        string `json:"ref"`
	Value      string `json:"value"`
	Footprint  string `json:"footprint"`
	Layer      string `json:"layer,omitempty"`
	LCSC       string `json:"lcsc,omitempty"`
	LCSCSource string `json:"lcscSource,omitempty"` // "pcb:<field>" | "sch:<field>"
	DNP        bool   `json:"dnp,omitempty"`
	ExcludeBOM bool   `json:"excludeFromBom,omitempty"`
	ExcludePos bool   `json:"excludeFromPos,omitempty"`
}

// Assembled: JLC places it (not DNP, in the BOM, in the position file).
func (p Part) Assembled() bool { return !p.DNP && !p.ExcludeBOM && !p.ExcludePos }

// SchPart is one row of the schematic BOM export (one symbol reference).
type SchPart struct {
	Ref, Value, Footprint string
	DNP, ExcludeBOM       bool
	LCSC, LCSCField       string
}

// schBOMFields are passed to `kicad-cli sch export bom --fields`.
func schBOMFields() []string {
	return append([]string{"Reference", "Value", "Footprint", "${DNP}", "${EXCLUDE_FROM_BOM}"}, LCSCFieldNames...)
}

// SchBOMArgs builds the kicad-cli call: one row per reference, no ranges.
func SchBOMArgs(sch, out string) []string {
	f := schBOMFields()
	labels := make([]string, len(f))
	for i, n := range f {
		labels[i] = strings.Trim(n, "${}")
	}
	return []string{"sch", "export", "bom",
		"--output", out,
		"--fields", strings.Join(f, ","),
		"--labels", strings.Join(labels, ","),
		"--group-by", "",
		"--ref-range-delimiter", "",
		sch}
}

// ParseSchBOM parses the CSV written by SchBOMArgs.
func ParseSchBOM(b []byte) ([]SchPart, error) {
	recs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("schematic bom csv: %w", err)
	}
	if len(recs) == 0 {
		return nil, nil
	}
	hdr := recs[0]
	var out []SchPart
	for _, rec := range recs[1:] {
		fields := map[string]string{}
		for i, h := range hdr {
			if i < len(rec) {
				fields[h] = rec[i]
			}
		}
		sp := SchPart{Value: fields["Value"], Footprint: fields["Footprint"],
			DNP: fields["DNP"] != "", ExcludeBOM: fields["EXCLUDE_FROM_BOM"] != ""}
		sp.LCSC, sp.LCSCField = LookupLCSC(fields)
		// Grouped rows ("R1,R2") are split defensively even though --group-by ""
		// already yields one row per reference.
		for _, ref := range strings.Split(fields["Reference"], ",") {
			if ref = strings.TrimSpace(ref); ref != "" {
				c := sp
				c.Ref = ref
				out = append(out, c)
			}
		}
	}
	return out, nil
}

// ReadSchematicParts runs kicad-cli on the (root) schematic — hierarchy is
// resolved by KiCad — and parses the result.
func ReadSchematicParts(cli, sch string) ([]SchPart, error) {
	tmp, err := os.MkdirTemp("", "pcbpilot-kicad-bom-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "bom.csv")
	if err := runCLI(cli, SchBOMArgs(sch, out), nil); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	return ParseSchBOM(b)
}

// MergeParts combines board footprints (the set of placed parts) with the
// schematic rows. The schematic's LCSC field wins when both have one (the
// schematic is the source the board is updated from); DNP / exclude flags are
// OR-ed. A board footprint the schematic BOM does not list keeps the board's
// own fields and flags (so it is still gated) and is reported as a warning:
// kicad-cli omits excluded-from-BOM symbols, so an out-of-sync board must be
// fixed rather than guessed at.
func MergeParts(bd *Board, sch []SchPart, haveSch bool) ([]Part, []string) {
	byRef := map[string]SchPart{}
	for _, s := range sch {
		byRef[s.Ref] = s
	}
	var parts []Part
	var warns []string
	for _, f := range bd.Footprints {
		if f.Ref == "" {
			continue
		}
		p := Part{Ref: f.Ref, Value: f.Value, Footprint: f.LibID, Layer: f.Layer,
			DNP: f.DNP, ExcludeBOM: f.ExcludeBOM, ExcludePos: f.ExcludePos}
		if v, fld := LookupLCSC(f.Fields); v != "" {
			p.LCSC, p.LCSCSource = v, "pcb:"+fld
		}
		if haveSch {
			s, ok := byRef[f.Ref]
			if !ok {
				if p.Assembled() {
					warns = append(warns, fmt.Sprintf("%s (%s) is on the board but not in the schematic BOM (board-only part, or excluded from BOM only in the schematic) — using the board's fields; run Update PCB from Schematic if out of sync", f.Ref, f.LibID))
				}
			} else {
				p.DNP = p.DNP || s.DNP
				p.ExcludeBOM = p.ExcludeBOM || s.ExcludeBOM
				if s.LCSC != "" {
					if p.LCSC != "" && p.LCSC != s.LCSC {
						warns = append(warns, fmt.Sprintf("%s: schematic LCSC %s differs from board %s — using the schematic's (run Update PCB from Schematic)", f.Ref, s.LCSC, p.LCSC))
					}
					p.LCSC, p.LCSCSource = s.LCSC, "sch:"+s.LCSCField
				}
				if s.Value != "" {
					p.Value = s.Value
				}
			}
		}
		parts = append(parts, p)
	}
	sort.SliceStable(parts, func(a, b int) bool { return refLess(parts[a].Ref, parts[b].Ref) })
	return parts, warns
}

// MissingLCSC is one assembled part failing the LCSC gate.
type MissingLCSC struct {
	Ref       string `json:"ref"`
	Value     string `json:"value"`
	Footprint string `json:"footprint"`
	LCSC      string `json:"lcsc,omitempty"` // set for an invalid value
}

// CheckLCSC returns assembled parts with no LCSC number, and those whose
// value is not C<digits>.
func CheckLCSC(parts []Part) (missing, invalid []MissingLCSC) {
	for _, p := range parts {
		if !p.Assembled() {
			continue
		}
		m := MissingLCSC{Ref: p.Ref, Value: p.Value, Footprint: p.Footprint}
		switch {
		case p.LCSC == "":
			missing = append(missing, m)
		case !ValidLCSC(p.LCSC):
			m.LCSC = p.LCSC
			invalid = append(invalid, m)
		}
	}
	return missing, invalid
}

// ── writing LCSC fields (embedded fab.py) ──────────────────────────────────

//go:embed fab.py
var fabPy []byte

// FabPy returns the embedded helper script (for tests / inspection).
func FabPy() []byte { return fabPy }

// SetResult is fab.py's JSON reply for one file.
type SetResult struct {
	File     string            `json:"file"`
	Changed  map[string]string `json:"changed"`  // ref → "field=old→new"
	NotFound []string          `json:"notFound"` // refs absent from that file
}

// ParseAssignments parses REF=Cxxxx pairs.
func ParseAssignments(pairs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pairs {
		for _, one := range strings.Split(p, ",") {
			one = strings.TrimSpace(one)
			if one == "" {
				continue
			}
			ref, val, ok := strings.Cut(one, "=")
			ref, val = strings.TrimSpace(ref), strings.ToUpper(strings.TrimSpace(val))
			if !ok || ref == "" {
				return nil, fmt.Errorf("--set %q: want REF=C123456", one)
			}
			if !ValidLCSC(val) {
				return nil, fmt.Errorf("--set %s=%s: not an LCSC part number (C followed by digits)", ref, val)
			}
			out[ref] = val
		}
	}
	return out, nil
}

// SetLCSC writes the assignments into the board (through pcbnew, so the file
// is re-saved by KiCad itself) and/or the schematic (a surgical text edit of
// the symbol's property list; KiCad 10 has no schematic Python API). field is
// the field to create when a part has no accepted LCSC field yet; an existing
// accepted field is updated in place.
func SetLCSC(tools FabTools, pcb, sch string, assign map[string]string, field string) ([]SetResult, error) {
	if field == "" {
		field = DefaultLCSCField
	}
	tmp, err := os.MkdirTemp("", "pcbpilot-kicad-fab-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	script := filepath.Join(tmp, "fab.py")
	if err := os.WriteFile(script, fabPy, 0o644); err != nil {
		return nil, err
	}
	aj, _ := json.Marshal(assign)
	names, _ := json.Marshal(LCSCFieldNames)
	var results []SetResult
	run := func(py, sub, file string) error {
		cmd := exec.Command(py, script, sub, "--file", file, "--assign", string(aj),
			"--field", field, "--accepted", string(names))
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("fab.py %s %s: %w\n%s", sub, file, err, se.String())
		}
		var r SetResult
		if err := json.Unmarshal(so.Bytes(), &r); err != nil {
			return fmt.Errorf("fab.py %s: bad reply %q: %w", sub, so.String(), err)
		}
		results = append(results, r)
		return nil
	}
	if sch != "" {
		// Plain python is enough for the text edit; prefer KiCad's for consistency.
		py := tools.Python
		if py == "" {
			py = "python3"
		}
		if err := run(py, "set-sch", sch); err != nil {
			return results, err
		}
	}
	if pcb != "" {
		if tools.Python == "" {
			return results, fmt.Errorf("writing the board needs KiCad's python (pcbnew); set %s", envFabPython)
		}
		if err := run(tools.Python, "set-pcb", pcb); err != nil {
			return results, err
		}
	}
	return results, nil
}
