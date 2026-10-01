package eprj3

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Severity levels for findings.
const (
	SevError   = "error"   // the input cannot be read as an eprj3 project/document
	SevWarning = "warning" // readable, but inconsistent with the documented format
	SevInfo    = "info"    // noteworthy, not a defect
)

// Finding is one inspection result.
type Finding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
	Count    int    `json:"count,omitempty"`
}

// Counts are live (eventual-consistency winners, deletions excluded) object
// counts for one document. Fields not meaningful for a docType stay zero.
type Counts struct {
	Components int `json:"components"`
	Wires      int `json:"wires"`
	Nets       int `json:"nets"`
	Tracks     int `json:"tracks"`
	Vias       int `json:"vias"`
	Pours      int `json:"pours"`
}

// DocReport describes one document.
type DocReport struct {
	File        string         `json:"file"`
	DocType     string         `json:"docType"`
	Title       string         `json:"title,omitempty"`
	UUID        string         `json:"uuid,omitempty"`
	StartLine   int            `json:"startLine"`
	EditVersion string         `json:"editVersion,omitempty"`
	Version     string         `json:"version,omitempty"`
	UpdateTime  int64          `json:"updateTime,omitempty"`
	Records     int            `json:"records"`
	LiveRecords int            `json:"liveRecords"`
	Deleted     int            `json:"deletedRecords"`
	Superseded  int            `json:"supersededRecords"`
	Counts      *Counts        `json:"counts,omitempty"`
	NetNames    []string       `json:"netNames,omitempty"`
	Types       map[string]int `json:"liveRecordTypes"`
	Coordinates *Coordinates   `json:"coordinates,omitempty"`
}

// Coordinates is the documented unit / axis convention plus what the file
// itself declares.
type Coordinates struct {
	Unit             string   `json:"unit"`
	Axis             string   `json:"axis"`
	Rotation         string   `json:"rotation"`
	CanvasUnit       string   `json:"canvasDisplayUnit,omitempty"`
	YAxisDirection   []string `json:"yAxisDirectionMarkers,omitempty"`
	ConventionSource string   `json:"conventionSource"`
}

// FileReport groups the documents of one file. Main is the file's own
// document (SCH_PAGE / PCB / PANEL / SCH); the others are embedded library
// documents (SYMBOL / FOOTPRINT / DEVICE / BLOB ...).
type FileReport struct {
	Path            string         `json:"path"`
	Kind            string         `json:"kind"`
	Lines           int            `json:"lines"`
	Main            *DocReport     `json:"main,omitempty"`
	LibraryDocTypes map[string]int `json:"libraryDocTypes,omitempty"`
	Documents       []*DocReport   `json:"documents"`
}

// Inventory is the project index summary.
type Inventory struct {
	Name        string   `json:"name"`
	Format      string   `json:"format"`
	Boards      []string `json:"boards"`
	Schematics  []string `json:"schematics"`
	Sheets      []string `json:"sheets"`
	PCBs        []string `json:"pcbs"`
	Panels      []string `json:"panels"`
	DefaultPage string   `json:"defaultSheet,omitempty"`
}

// VersionMarkers summarizes the format-generation evidence.
type VersionMarkers struct {
	EditVersions []string `json:"editVersions,omitempty"`
	Generation   string   `json:"generation"`
	Evidence     []string `json:"evidence"`
}

// SchemaSummary aggregates schema validation.
type SchemaSummary struct {
	Source              string         `json:"source"`
	Validated           int            `json:"validatedRecords"`
	Unvalidated         map[string]int `json:"unvalidatedRecordTypes,omitempty"`
	Violations          int            `json:"violations"`
	DocumentedDeviation int            `json:"documentedDeviations"`
	DeviationNotes      []string       `json:"documentedDeviationNotes,omitempty"`
	// Findings aggregates violations by (docType, record type, path, rule).
	// They are advisory: the official schemas describe generator output, and
	// editor-written files legitimately deviate (nulls, omitted fields).
	Findings []SchemaFinding `json:"findings"`
	Note     string          `json:"note"`
	index    map[string]int
}

// SchemaFinding is one aggregated schema violation.
type SchemaFinding struct {
	DocType    string `json:"docType"`
	RecordType string `json:"recordType"`
	Schema     string `json:"schema"`
	Path       string `json:"path"`
	Rule       string `json:"rule"`
	Message    string `json:"message"`
	Count      int    `json:"count"`
	FirstFile  string `json:"firstFile"`
	FirstLine  int    `json:"firstLine"`
}

// Report is the `inspect-eprj3` result.
type Report struct {
	Input          string          `json:"input"`
	Mode           string          `json:"mode"` // project | file
	Root           string          `json:"root,omitempty"`
	Inventory      *Inventory      `json:"inventory,omitempty"`
	Files          []*FileReport   `json:"files"`
	Totals         Counts          `json:"totals"`
	VersionMarkers VersionMarkers  `json:"versionMarkers"`
	Schema         SchemaSummary   `json:"schema"`
	Findings       []Finding       `json:"findings"`
	Conventions    []ConventionRow `json:"conventions"`
	Errors         int             `json:"errors"`
	Warnings       int             `json:"warnings"`
}

// OK reports whether the inspection found no errors.
func (r *Report) OK() bool { return r.Errors == 0 }

var docExts = map[string]string{
	".esch2": "schematic-sheet",
	".epcb2": "pcb",
	".epan2": "panel",
	".ecfg":  "schematic-config",
	".evar":  "assembly-variant",
}

// Inspect reads a project directory, an `.eprj3` index file, or a single
// document file. It returns an error only when the input path itself is
// unusable; format problems become findings (errors count in Report.Errors).
func Inspect(input string) (*Report, error) {
	st, err := os.Stat(input)
	if err != nil {
		return nil, err
	}
	r := &Report{Input: input, Conventions: Conventions()}
	switch {
	case st.IsDir():
		idx, err := findIndex(input)
		if err != nil {
			return nil, err
		}
		inspectProject(r, idx)
	case strings.EqualFold(filepath.Ext(input), ".eprj3"):
		inspectProject(r, input)
	default:
		if _, ok := docExts[strings.ToLower(filepath.Ext(input))]; !ok {
			return nil, fmt.Errorf("%s: not an eprj3 project directory, .eprj3 index, or .esch2/.epcb2/.epan2/.ecfg/.evar document", input)
		}
		r.Mode = "file"
		r.inspectDocFile(input, filepath.Base(input))
	}
	r.finish()
	return r, nil
}

func findIndex(dir string) (string, error) {
	m, err := filepath.Glob(filepath.Join(dir, "*.eprj3"))
	if err != nil {
		return "", err
	}
	switch len(m) {
	case 0:
		return "", fmt.Errorf("%s: no .eprj3 index file in this directory (pass the project root folder)", dir)
	case 1:
		return m[0], nil
	}
	return "", fmt.Errorf("%s: %d .eprj3 index files; pass one explicitly", dir, len(m))
}

type projectIndex struct {
	Name         string `json:"name"`
	Format       string `json:"format"`
	DefaultSheet string `json:"default_sheet"`
	Profile      *struct {
		Boards map[string]struct {
			Title string `json:"title"`
		} `json:"boards"`
		Schematics map[string]struct {
			Name  string `json:"name"`
			Board string `json:"board"`
		} `json:"schematics"`
		Sheets map[string]struct {
			Title         string `json:"title"`
			SchematicUUID string `json:"schematic_uuid"`
			ZIndex        any    `json:"zIndex"`
		} `json:"sheets"`
		PCBs map[string]struct {
			Title string `json:"title"`
		} `json:"pcbs"`
		Panels map[string]struct {
			Title string `json:"title"`
		} `json:"panels"`
	} `json:"profile"`
}

func (r *Report) add(f Finding) { r.Findings = append(r.Findings, f) }

func inspectProject(r *Report, indexPath string) {
	r.Mode = "project"
	root := filepath.Dir(indexPath)
	r.Root = root
	rel := filepath.Base(indexPath)
	raw, err := os.ReadFile(indexPath)
	if err != nil {
		r.add(Finding{Severity: SevError, Code: "index-unreadable", File: rel, Message: err.Error()})
		return
	}
	var idx projectIndex
	if err := json.Unmarshal(raw, &idx); err != nil {
		r.add(Finding{Severity: SevError, Code: "index-not-json", File: rel, Message: "the .eprj3 index is not a JSON object: " + err.Error()})
		return
	}
	if idx.Profile == nil {
		r.add(Finding{Severity: SevError, Code: "index-no-profile", File: rel, Message: "the .eprj3 index has no `profile` (document tree)"})
		return
	}
	inv := &Inventory{Name: idx.Name, Format: idx.Format, DefaultPage: idx.DefaultSheet,
		Boards: []string{}, Schematics: []string{}, Sheets: []string{}, PCBs: []string{}, Panels: []string{}}
	r.Inventory = inv
	if idx.Format != "folder" {
		r.add(Finding{Severity: SevWarning, Code: "index-format", File: rel, Message: fmt.Sprintf("index `format` is %q; the documented eprj3 value is \"folder\"", idx.Format)})
	}
	base := strings.TrimSuffix(rel, filepath.Ext(rel))
	if idx.Name != "" && idx.Name != base {
		r.add(Finding{Severity: SevInfo, Code: "index-name", File: rel, Message: fmt.Sprintf("index name %q differs from file name %q (the editor takes the project name from the folder/file)", idx.Name, base)})
	}
	for _, b := range sortedKeys(idx.Profile.Boards) {
		inv.Boards = append(inv.Boards, idx.Profile.Boards[b].Title)
	}
	// expected maps a project-relative file path to the uuid the index gives it.
	expected := map[string]string{}
	schName := map[string]string{}
	for _, u := range sortedKeys(idx.Profile.Schematics) {
		s := idx.Profile.Schematics[u]
		inv.Schematics = append(inv.Schematics, s.Name)
		schName[u] = s.Name
	}
	for _, u := range sortedKeys(idx.Profile.Sheets) {
		sh := idx.Profile.Sheets[u]
		name, ok := schName[sh.SchematicUUID]
		if !ok {
			r.add(Finding{Severity: SevWarning, Code: "index-orphan-sheet", File: rel, Message: fmt.Sprintf("sheet %q references unknown schematic %q", sh.Title, sh.SchematicUUID)})
			continue
		}
		inv.Sheets = append(inv.Sheets, name+"/"+sh.Title)
		expected[filepath.ToSlash(filepath.Join("sch", name, sh.Title+".esch2"))] = u
	}
	for _, u := range sortedKeys(idx.Profile.PCBs) {
		t := idx.Profile.PCBs[u].Title
		inv.PCBs = append(inv.PCBs, t)
		expected["pcb/"+t+".epcb2"] = u
	}
	for _, u := range sortedKeys(idx.Profile.Panels) {
		t := idx.Profile.Panels[u].Title
		inv.Panels = append(inv.Panels, t)
		expected["panel/"+t+".epan2"] = u
	}
	// Walk every document file under the project.
	var found []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if _, ok := docExts[strings.ToLower(filepath.Ext(p))]; ok {
			relp, _ := filepath.Rel(root, p)
			found = append(found, filepath.ToSlash(relp))
		}
		return nil
	})
	sort.Strings(found)
	seen := map[string]bool{}
	for _, relp := range found {
		seen[relp] = true
		fr := r.inspectDocFile(filepath.Join(root, filepath.FromSlash(relp)), relp)
		uuid, listed := expected[relp]
		ext := filepath.Ext(relp)
		switch {
		case listed:
			if fr != nil && fr.Main != nil && fr.Main.UUID != "" && fr.Main.UUID != uuid {
				r.add(Finding{Severity: SevWarning, Code: "uuid-mismatch", File: relp, Message: fmt.Sprintf("document uuid %s differs from the index entry %s", fr.Main.UUID, uuid)})
			}
		case ext == ".esch2" || ext == ".epcb2" || ext == ".epan2":
			r.add(Finding{Severity: SevWarning, Code: "unlisted-document", File: relp, Message: "document file is not listed in the .eprj3 profile (the editor names documents by file name; check the title)"})
		}
	}
	for _, relp := range sortedKeys(expected) {
		if !seen[relp] {
			r.add(Finding{Severity: SevWarning, Code: "missing-document", File: relp, Message: "listed in the .eprj3 profile but the file is absent (an editor-created document is written on first save)"})
		}
	}
}

func (r *Report) inspectDocFile(abs, rel string) *FileReport {
	data, err := os.ReadFile(abs)
	if err != nil {
		r.add(Finding{Severity: SevError, Code: "file-unreadable", File: rel, Message: err.Error()})
		return nil
	}
	fr := &FileReport{Path: rel, Kind: docExts[strings.ToLower(filepath.Ext(abs))]}
	r.Files = append(r.Files, fr)
	pf, err := ParseFile(data)
	fr.Lines = pf.Lines
	if pf.CRLF > 0 {
		r.add(Finding{Severity: SevWarning, Code: "crlf", File: rel, Count: pf.CRLF, Message: "lines end in CRLF; the documented terminator is `|` + LF"})
	}
	if err != nil {
		var le *LineError
		switch {
		case errors.As(err, &le):
			r.add(Finding{Severity: SevError, Code: "malformed-line", File: rel, Line: le.Line, Message: le.Msg})
		case errors.Is(err, ErrEmpty):
			r.add(Finding{Severity: SevError, Code: "empty-file", File: rel, Message: "no records (every document file starts with a DOCHEAD line)"})
		default:
			r.add(Finding{Severity: SevError, Code: "parse-failed", File: rel, Message: err.Error()})
		}
		return fr
	}
	main := mainDocType(fr.Kind)
	for _, d := range pf.Documents {
		dr := r.inspectDocument(rel, d)
		fr.Documents = append(fr.Documents, dr)
		if fr.Main == nil && d.DocType == main {
			fr.Main = dr
		} else {
			if fr.LibraryDocTypes == nil {
				fr.LibraryDocTypes = map[string]int{}
			}
			fr.LibraryDocTypes[d.DocType]++
		}
	}
	if fr.Main == nil && main != "" {
		r.add(Finding{Severity: SevWarning, Code: "no-main-document", File: rel, Message: fmt.Sprintf("no %s document in a %s file", main, filepath.Ext(rel))})
	}
	if fr.Main != nil && fr.Main.Counts != nil {
		r.Totals.Components += fr.Main.Counts.Components
		r.Totals.Wires += fr.Main.Counts.Wires
		r.Totals.Nets += fr.Main.Counts.Nets
		r.Totals.Tracks += fr.Main.Counts.Tracks
		r.Totals.Vias += fr.Main.Counts.Vias
		r.Totals.Pours += fr.Main.Counts.Pours
	}
	r.checkReferences(rel, pf)
	return fr
}

func mainDocType(kind string) string {
	switch kind {
	case "schematic-sheet":
		return "SCH_PAGE"
	case "pcb":
		return "PCB"
	case "panel":
		return "PANEL"
	case "schematic-config":
		return "SCH"
	}
	return ""
}

// copperLayer reports whether a PCB layerId is a copper layer (1 top, 2
// bottom, 15–46 inner 1–32), per the official ELayerCode table.
func copperLayer(id float64) bool {
	return id == 1 || id == 2 || (id >= 15 && id <= 46)
}

func (r *Report) inspectDocument(file string, d *Document) *DocReport {
	dr := &DocReport{
		File: file, DocType: d.DocType, Title: d.Title, UUID: d.UUID, StartLine: d.StartLine,
		EditVersion: d.EditVersion, Version: d.Version, UpdateTime: d.UpdateTime,
		Records: len(d.Records), Types: map[string]int{},
	}
	live := d.Live()
	dr.LiveRecords = len(live)
	winners := map[int]bool{}
	for _, w := range d.live {
		winners[w.Line] = true
		if w.Deleted() {
			dr.Deleted++
		}
	}
	for _, rec := range d.Records {
		if !winners[rec.Line] {
			dr.Superseded++
		}
	}
	for _, c := range d.conflicts {
		r.add(Finding{Severity: SevWarning, Code: "ticket-collision", File: file, Message: "two records share the winning ticket: " + c})
	}
	yDirs := map[string]bool{}
	var canvasUnit string
	nets := map[string]bool{}
	counts := &Counts{}
	for _, rec := range live {
		dr.Types[rec.Type]++
		var obj map[string]any
		_ = json.Unmarshal(rec.Inner, &obj)
		if s, ok := obj["yAxisDirection"].(string); ok {
			yDirs[s] = true
		}
		if rec.Type == "CANVAS" {
			canvasUnit, _ = obj["unit"].(string)
		}
		r.validateRecord(file, d.DocType, rec, obj)
		switch d.DocType {
		case "SCH_PAGE":
			switch rec.Type {
			case "COMPONENT":
				counts.Components++
			case "WIRE":
				counts.Wires++
			case "ATTR":
				key, _ := obj["key"].(string)
				val, _ := obj["value"].(string)
				if (key == "NET" || key == "Global Net Name") && val != "" {
					nets[val] = true
				}
			}
		case "PCB":
			switch rec.Type {
			case "COMPONENT":
				counts.Components++
			case "NET":
				if name := netNameFromID(rec.ID); name != "" {
					nets[name] = true
				}
			case "LINE", "ARC":
				if l, ok := obj["layerId"].(float64); ok && copperLayer(l) {
					counts.Tracks++
				}
			case "VIA":
				counts.Vias++
			case "POUR":
				counts.Pours++
			}
		}
	}
	if d.DocType == "SCH_PAGE" || d.DocType == "PCB" {
		counts.Nets = len(nets)
		dr.Counts = counts
		dr.NetNames = sortedKeys(nets)
	}
	if c := coordinatesFor(d.DocType); c != nil {
		c.CanvasUnit = canvasUnit
		c.YAxisDirection = sortedKeys(yDirs)
		dr.Coordinates = c
	}
	return dr
}

// netNameFromID decodes the PCB NET outer id, an array literal
// `["NET","<name>"]`; a plain id is returned as-is.
func netNameFromID(id string) string {
	var parts []string
	if json.Unmarshal([]byte(id), &parts) == nil {
		if len(parts) == 2 && parts[0] == "NET" {
			return parts[1]
		}
		return ""
	}
	return id
}

func (r *Report) validateRecord(file, docType string, rec Record, obj map[string]any) {
	name := schemaFor(docType, rec.Type)
	if name == "" {
		if docType == "SCH_PAGE" || docType == "PCB" || docType == "SCH" || docType == "PANEL" {
			if r.Schema.Unvalidated == nil {
				r.Schema.Unvalidated = map[string]int{}
			}
			r.Schema.Unvalidated[docType+"/"+rec.Type]++
		}
		return
	}
	schemas, err := loadSchemas()
	if err != nil {
		r.add(Finding{Severity: SevError, Code: "schema-load", Message: err.Error()})
		return
	}
	n := schemas[name]
	if n == nil {
		return
	}
	r.Schema.Validated++
	var v any
	if err := json.Unmarshal(rec.Inner, &v); err != nil {
		return
	}
	var out []violation
	validateValue(n, v, "", &out)
	for _, viol := range out {
		if note, ok := documentedDeviation(viol); ok {
			r.Schema.DocumentedDeviation++
			if !containsStr(r.Schema.DeviationNotes, note) {
				r.Schema.DeviationNotes = append(r.Schema.DeviationNotes, note)
			}
			continue
		}
		r.Schema.Violations++
		key := strings.Join([]string{docType, rec.Type, name, viol.Path, viol.Rule}, "\x00")
		if r.Schema.index == nil {
			r.Schema.index = map[string]int{}
		}
		if i, ok := r.Schema.index[key]; ok {
			r.Schema.Findings[i].Count++
			continue
		}
		r.Schema.index[key] = len(r.Schema.Findings)
		r.Schema.Findings = append(r.Schema.Findings, SchemaFinding{
			DocType: docType, RecordType: rec.Type, Schema: name, Path: orRoot(viol.Path), Rule: viol.Rule,
			Message: viol.Msg, Count: 1, FirstFile: file, FirstLine: rec.Line,
		})
	}
}

func orRoot(p string) string {
	if p == "" {
		return "<record>"
	}
	return p
}

// checkReferences runs the documented write rules that a reader can check:
// schematic COMPONENT.partId resolves to a PART in the same file, ATTR.parentId
// resolves to a live record of the same document, PAD_NET names a live PCB
// COMPONENT.
func (r *Report) checkReferences(file string, pf *ParsedFile) {
	parts := map[string]bool{}
	for _, d := range pf.Documents {
		for _, rec := range d.Live() {
			if rec.Type == "PART" {
				parts[rec.ID] = true
			}
		}
	}
	for _, d := range pf.Documents {
		ids := map[string]bool{}
		live := d.Live()
		for _, rec := range live {
			ids[rec.ID] = true
		}
		for _, rec := range live {
			var obj map[string]any
			_ = json.Unmarshal(rec.Inner, &obj)
			switch {
			case d.DocType == "SCH_PAGE" && rec.Type == "COMPONENT":
				if pid, _ := obj["partId"].(string); pid != "" && !parts[pid] {
					r.add(Finding{Severity: SevWarning, Code: "dangling-partId", File: file, Line: rec.Line, Message: fmt.Sprintf("COMPONENT %s partId %q has no PART in this file's SYMBOL documents", rec.ID, pid)})
				}
			case rec.Type == "ATTR":
				if pid, _ := obj["parentId"].(string); pid != "" && !ids[pid] {
					r.add(Finding{Severity: SevWarning, Code: "dangling-parentId", File: file, Line: rec.Line, Message: fmt.Sprintf("ATTR %s parentId %q is not a live record of this %s document", rec.ID, pid, d.DocType)})
				}
			case d.DocType == "PCB" && rec.Type == "PAD_NET":
				var key []string
				if json.Unmarshal([]byte(rec.ID), &key) == nil && len(key) >= 2 && !ids[key[1]] {
					r.add(Finding{Severity: SevWarning, Code: "dangling-pad-net", File: file, Line: rec.Line, Message: fmt.Sprintf("PAD_NET names component %q, which is not a live COMPONENT", key[1])})
				}
			}
		}
	}
}

func (r *Report) finish() {
	editVersions := map[string]bool{}
	gen := map[string]bool{}
	var evidence []string
	addEv := func(g, e string) {
		gen[g] = true
		if !containsStr(evidence, e) {
			evidence = append(evidence, e)
		}
	}
	for _, f := range r.Files {
		for _, d := range f.Documents {
			if d.EditVersion != "" {
				editVersions[d.EditVersion] = true
				if major, err := strconv.Atoi(strings.SplitN(d.EditVersion, ".", 2)[0]); err == nil {
					if major >= 4 {
						addEv("V4", "DOCHEAD editVersion "+d.EditVersion)
					} else {
						addEv("V3", "DOCHEAD editVersion "+d.EditVersion)
					}
				}
			}
			switch d.DocType {
			case "PROJECT_CONFIG":
				addEv("V3", "docType PROJECT_CONFIG (renamed CONFIG in V4)")
			case "CONFIG", "EDIT_HEAD", "PANEL_LIB", "FONT", "SIMULATION", "SIMULATION_SCH", "COMPONENT_GROUP_DATA":
				addEv("V4", "docType "+d.DocType+" (V4-only)")
			}
			if d.Types["CARC"] > 0 {
				addEv("V3", "CARC record (V4 unified arcs into ARC + arcType)")
			}
			if len(d.Coordinates.yAxisMarkers()) > 0 {
				addEv("V4", "yAxisDirection markers (eprj3 local-file marker, V4)")
			}
		}
	}
	if r.Mode == "project" && r.Inventory != nil && r.Inventory.Format == "folder" {
		addEv("V4", "folder project (.eprj3 + .esch2/.epcb2) — the format the V4 desktop client writes")
	}
	r.VersionMarkers.EditVersions = sortedKeys(editVersions)
	switch {
	case gen["V4"] && gen["V3"]:
		r.VersionMarkers.Generation = "mixed"
	case gen["V4"]:
		r.VersionMarkers.Generation = "V4"
	case gen["V3"]:
		r.VersionMarkers.Generation = "V3"
	default:
		r.VersionMarkers.Generation = "unknown"
	}
	if evidence == nil {
		evidence = []string{}
	}
	r.VersionMarkers.Evidence = evidence
	if r.VersionMarkers.Generation == "mixed" {
		r.add(Finding{Severity: SevWarning, Code: "mixed-generation", Message: "both V3 and V4 markers present: " + strings.Join(evidence, "; ")})
	}
	if r.Schema.Findings == nil {
		r.Schema.Findings = []SchemaFinding{}
	}
	sort.SliceStable(r.Schema.Findings, func(i, j int) bool { return r.Schema.Findings[i].Count > r.Schema.Findings[j].Count })
	r.Schema.Note = "advisory: the official schemas describe generator output; editor-written files (observed 4.1.36) omit fields and write null where the schema declares boolean/string. Structural problems are reported in findings."
	r.Schema.Source = "easyeda/easyeda-format-skill@" + vendoredCommit() + " (MIT, subset vendored in internal/eprj3/schemas)"
	sort.SliceStable(r.Findings, func(i, j int) bool {
		return sevRank(r.Findings[i].Severity) < sevRank(r.Findings[j].Severity)
	})
	for _, f := range r.Findings {
		switch f.Severity {
		case SevError:
			r.Errors++
		case SevWarning:
			r.Warnings++
		}
	}
	if r.Findings == nil {
		r.Findings = []Finding{}
	}
	if r.Files == nil {
		r.Files = []*FileReport{}
	}
}

func (c *Coordinates) yAxisMarkers() []string {
	if c == nil {
		return nil
	}
	return c.YAxisDirection
}

func sevRank(s string) int {
	switch s {
	case SevError:
		return 0
	case SevWarning:
		return 1
	}
	return 2
}

func vendoredCommit() string {
	raw, err := schemaFS.ReadFile("schemas/SOURCE.json")
	if err != nil {
		return "unknown"
	}
	var s struct {
		Commit string `json:"commit"`
	}
	_ = json.Unmarshal(raw, &s)
	if len(s.Commit) > 12 {
		return s.Commit[:12]
	}
	return s.Commit
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
