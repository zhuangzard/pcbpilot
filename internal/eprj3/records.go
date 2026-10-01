// Package eprj3 is a read-only parser for EasyEDA Pro V4 folder projects
// (`.eprj3` index + `.esch2` / `.epcb2` / `.epan2` / `.ecfg` / `.evar` document
// files). It never writes. It powers `pcbpilot project inspect-eprj3` and is the
// basis for offline fixtures.
//
// Format knowledge comes from the official sources (all read 2026-10-01):
//   - easyeda/easyeda-format-skill (MIT): line format, eventual-consistency
//     rule, deletion semantics, JSON Schemas (a subset is vendored in schemas/);
//   - easyeda/easyeda-client-cli docs (`doc format`, "Coordinate Systems and
//     Units"): folder layout, 10 mil schematic unit, mil PCB unit, rotation rule;
//   - easyeda/kicad-to-easyeda-eprj3 (Apache-2.0): a real editor-written sample
//     (vendored under testdata/upstream-example).
package eprj3

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Record is one `{outer}||{inner}|` line.
type Record struct {
	Type   string
	ID     string // empty for DOCHEAD (no outer id)
	Ticket int64
	// HasTicket is false when the outer frame carries no ticket (real
	// editor-written DOCHEAD lines omit it).
	HasTicket bool
	// Inner is the payload JSON; nil when the atom is deleted (empty payload).
	Inner json.RawMessage
	Line  int // 1-based line number in the file
}

// Deleted reports whether the record is a deletion (empty inner payload).
func (r Record) Deleted() bool { return len(r.Inner) == 0 }

// key is the eventual-consistency identity inside one document: the id
// (type is NOT part of the key per the official rule); id-less singleton rows
// fall back to their type.
func (r Record) key() string {
	if r.ID != "" {
		return r.ID
	}
	return "\x00" + r.Type
}

// LineError is a malformed line.
type LineError struct {
	Line int
	Msg  string
}

func (e *LineError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// ParseLine parses one record line. Trailing CR is tolerated by the caller and
// reported separately; here the line has no terminator newline.
func ParseLine(b []byte, lineNo int) (Record, error) {
	rec := Record{Line: lineNo}
	trimmed := bytes.TrimLeft(b, " \t")
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return rec, &LineError{lineNo, "record must start with the outer JSON object `{`"}
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	var outer map[string]any
	if err := dec.Decode(&outer); err != nil {
		return rec, &LineError{lineNo, "outer frame is not valid JSON: " + err.Error()}
	}
	rest := trimmed[dec.InputOffset():]
	if !bytes.HasPrefix(rest, []byte("||")) {
		return rec, &LineError{lineNo, "missing `||` separator between outer frame and payload"}
	}
	payload := rest[2:]
	// The record terminator is a single trailing `|` (the editor omits it on
	// the file's last line). Strip exactly one.
	payload = bytes.TrimSuffix(payload, []byte("|"))
	t, ok := outer["type"].(string)
	if !ok || t == "" {
		return rec, &LineError{lineNo, "outer frame has no string `type`"}
	}
	rec.Type = t
	if v, present := outer["id"]; present {
		s, ok := v.(string)
		if !ok {
			return rec, &LineError{lineNo, "outer `id` is not a string"}
		}
		rec.ID = s
	}
	if v, present := outer["ticket"]; present {
		n, ok := v.(json.Number)
		if !ok {
			return rec, &LineError{lineNo, "outer `ticket` is not a number"}
		}
		i, err := n.Int64()
		if err != nil {
			return rec, &LineError{lineNo, "outer `ticket` is not an integer"}
		}
		rec.Ticket, rec.HasTicket = i, true
	}
	if len(payload) > 0 {
		if !json.Valid(payload) {
			return rec, &LineError{lineNo, "payload after `||` is not valid JSON"}
		}
		rec.Inner = append(json.RawMessage(nil), payload...)
	}
	return rec, nil
}

// Document is one DOCHEAD-delimited document inside a file.
type Document struct {
	DocType     string `json:"docType"`
	UUID        string `json:"uuid,omitempty"`
	Client      string `json:"client,omitempty"`
	EditVersion string `json:"editVersion,omitempty"`
	Version     string `json:"version,omitempty"`
	UpdateTime  int64  `json:"updateTime,omitempty"`
	Title       string `json:"title,omitempty"`
	StartLine   int    `json:"startLine"`
	// Records is every line of the document, in file order, DOCHEAD included.
	Records []Record `json:"-"`
	// live is the eventual-consistency view: per key, the winning record.
	live map[string]Record
	// conflicts counts keys where two lines share the winning ticket.
	conflicts []string
}

// Live returns the winning, non-deleted records (file order).
func (d *Document) Live() []Record {
	out := make([]Record, 0, len(d.live))
	for _, r := range d.Records {
		if w, ok := d.live[r.key()]; ok && w.Line == r.Line && !w.Deleted() {
			out = append(out, w)
		}
	}
	return out
}

// resolve applies the official eventual-consistency rule inside one document:
// larger ticket wins; within one document the client is constant, so an equal
// ticket keeps the later line and is reported as a conflict.
func (d *Document) resolve() {
	d.live = map[string]Record{}
	for _, r := range d.Records {
		k := r.key()
		prev, ok := d.live[k]
		switch {
		case !ok || r.Ticket > prev.Ticket:
			d.live[k] = r
		case r.Ticket == prev.Ticket:
			if r.HasTicket && prev.HasTicket && r.Type != "DOCHEAD" {
				d.conflicts = append(d.conflicts, fmt.Sprintf("id %q ticket %d at lines %d and %d", r.ID, r.Ticket, prev.Line, r.Line))
			}
			d.live[k] = r
		}
	}
}

// ParsedFile is one document file split into documents.
type ParsedFile struct {
	Documents []*Document
	Lines     int
	CRLF      int // lines ending in CR (format requires LF)
}

// ErrEmpty is returned for a file with no records.
var ErrEmpty = errors.New("file has no records")

// ParseFile splits a document file into DOCHEAD-delimited documents. Any
// malformed line is a hard error (the caller reports it with file:line).
func ParseFile(data []byte) (*ParsedFile, error) {
	pf := &ParsedFile{}
	var cur *Document
	lines := bytes.Split(data, []byte("\n"))
	for i, ln := range lines {
		lineNo := i + 1
		if bytes.HasSuffix(ln, []byte("\r")) {
			pf.CRLF++
			ln = ln[:len(ln)-1]
		}
		if len(bytes.TrimSpace(ln)) == 0 {
			continue
		}
		pf.Lines++
		rec, err := ParseLine(ln, lineNo)
		if err != nil {
			return pf, err
		}
		if rec.Type == "DOCHEAD" {
			d, err := newDocument(rec)
			if err != nil {
				return pf, err
			}
			cur = d
			pf.Documents = append(pf.Documents, d)
		}
		if cur == nil {
			return pf, &LineError{lineNo, fmt.Sprintf("record %q appears before any DOCHEAD", rec.Type)}
		}
		cur.Records = append(cur.Records, rec)
	}
	if len(pf.Documents) == 0 {
		return pf, ErrEmpty
	}
	for _, d := range pf.Documents {
		d.resolve()
		if m, ok := d.live["META"]; ok && !m.Deleted() {
			var meta struct {
				Title string `json:"title"`
			}
			_ = json.Unmarshal(m.Inner, &meta)
			d.Title = meta.Title
		}
	}
	return pf, nil
}

func newDocument(rec Record) (*Document, error) {
	if rec.Deleted() {
		return nil, &LineError{rec.Line, "DOCHEAD has an empty payload"}
	}
	var head struct {
		DocType     string `json:"docType"`
		UUID        string `json:"uuid"`
		Client      string `json:"client"`
		EditVersion string `json:"editVersion"`
		Version     any    `json:"version"`
		UpdateTime  int64  `json:"updateTime"`
	}
	if err := json.Unmarshal(rec.Inner, &head); err != nil {
		return nil, &LineError{rec.Line, "DOCHEAD payload is not an object: " + err.Error()}
	}
	if head.DocType == "" {
		return nil, &LineError{rec.Line, "DOCHEAD has no docType"}
	}
	d := &Document{
		DocType: head.DocType, UUID: head.UUID, Client: head.Client,
		EditVersion: head.EditVersion, UpdateTime: head.UpdateTime, StartLine: rec.Line,
	}
	if head.Version != nil {
		d.Version = strings.Trim(fmt.Sprint(head.Version), "\"")
	}
	return d, nil
}
