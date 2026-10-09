package kicad

// scherc.go — kicad-cli ERC as JSON, the hierarchy's sheet paths, and the
// PWR_FLAG that clears "power input pin not driven" on a net whose power
// comes from off the schematic (a connector, a regulator modelled as
// passive pins).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ERCItem is one object a violation names.
type ERCItem struct {
	Description string `json:"description"`
	Pos         Pt     `json:"pos"`
	UUID        string `json:"uuid"`
}

// ERCViolation is one ERC finding.
type ERCViolation struct {
	Sheet       string    `json:"sheet"` // hierarchical path ("/", "/Power/")
	UUIDPath    string    `json:"uuidPath"`
	File        string    `json:"file,omitempty"`
	Type        string    `json:"type"`
	Severity    string    `json:"severity"`
	Description string    `json:"description"`
	Items       []ERCItem `json:"items"`
}

// ERCReport is kicad-cli's ERC of a hierarchy.
type ERCReport struct {
	Errors     int            `json:"errors"`
	Warnings   int            `json:"warnings"`
	Violations []ERCViolation `json:"violations"`
}

type ercJSON struct {
	Sheets []struct {
		Path       string `json:"path"`
		UUIDPath   string `json:"uuid_path"`
		Violations []struct {
			Description string `json:"description"`
			Severity    string `json:"severity"`
			Type        string `json:"type"`
			Items       []struct {
				Description string `json:"description"`
				Pos         struct{ X, Y float64 }
				UUID        string `json:"uuid"`
			} `json:"items"`
		} `json:"violations"`
	} `json:"sheets"`
}

// RunERC runs `kicad-cli sch erc` (all severities, JSON) on a root sheet.
func RunERC(root string) (*ERCReport, error) {
	cli, err := KicadCLI()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "pcbpilot-erc-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "erc.json")
	var buf bytes.Buffer
	cmd := exec.Command(cli, "sch", "erc", "--format", "json", "--severity-all", "-o", out, root)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kicad-cli sch erc: %v: %s", err, strings.TrimSpace(buf.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("kicad-cli wrote no ERC report: %s", strings.TrimSpace(buf.String()))
	}
	var x ercJSON
	if err := json.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse ERC report: %w", err)
	}
	files, _ := SheetPaths(root)
	r := &ERCReport{}
	for _, s := range x.Sheets {
		for _, v := range s.Violations {
			ev := ERCViolation{Sheet: s.Path, UUIDPath: s.UUIDPath, File: files[s.UUIDPath], Type: v.Type, Severity: v.Severity, Description: v.Description}
			for _, it := range v.Items {
				ev.Items = append(ev.Items, ERCItem{Description: it.Description, Pos: Pt{it.Pos.X, it.Pos.Y}, UUID: it.UUID})
			}
			switch v.Severity {
			case "error":
				r.Errors++
			case "warning":
				r.Warnings++
			}
			r.Violations = append(r.Violations, ev)
		}
	}
	return r, nil
}

// SheetPaths maps each sheet instance's uuid path ("/root", "/root/sheet")
// to its file.
func SheetPaths(root string) (map[string]string, error) {
	out := map[string]string{}
	var walk func(file, path string, depth int) error
	walk = func(file, path string, depth int) error {
		if depth > 32 {
			return fmt.Errorf("sheet hierarchy deeper than 32 (recursive?)")
		}
		b, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		n, err := parseSx(string(b))
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if path == "" {
			if u := n.child("uuid"); u != nil && len(u.list) > 1 {
				path = "/" + u.list[1].atom
			}
		}
		out[path] = file
		for _, s := range n.list {
			if s.head() != "sheet" {
				continue
			}
			u := s.child("uuid")
			f := propVal(s, "Sheetfile")
			if f == "" {
				f = propVal(s, "Sheet file")
			}
			if u == nil || len(u.list) < 2 || f == "" {
				continue
			}
			if err := walk(filepath.Join(filepath.Dir(file), f), path+"/"+u.list[1].atom, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	return out, walk(root, "", 0)
}

var ercPinRe = regexp.MustCompile(`^Symbol (\S+) (?:Hidden )?[Pp]in (\S+) \[([^,\]]*)`)

// ERCPin parses "Symbol U1 Pin 3 [VCC, Power input, Line]" → U1, 3, VCC.
func ERCPin(desc string) (ref, pin, name string, ok bool) {
	m := ercPinRe.FindStringSubmatch(desc)
	if m == nil {
		return "", "", "", false
	}
	return m[1], m[2], m[3], true
}

// PwrFlagLibID is the lib_id of pcbpilot's PWR_FLAG.
const PwrFlagLibID = "pcbpilot_power:PWR_FLAG"

// PwrFlagSymbolText is a PWR_FLAG: a power symbol whose only pin is a
// visible power output (it drives the net it is wired to; it names no net).
func PwrFlagSymbolText() string {
	return `(symbol "PWR_FLAG"
		(power)
		(pin_numbers (hide yes))
		(pin_names (offset 0) (hide yes))
		(exclude_from_sim no)
		(in_bom no)
		(on_board yes)
		(property "Reference" "#FLG" (at 0 1.905 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Value" "PWR_FLAG" (at 0 3.81 0) (effects (font (size 1.27 1.27))))
		(property "Footprint" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Datasheet" "" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(property "Description" "Tells ERC where the power of a net comes from" (at 0 0 0) (hide yes) (effects (font (size 1.27 1.27))))
		(symbol "PWR_FLAG_0_0" (pin power_out line (at 0 0 90) (length 0) (name "" (effects (font (size 1.27 1.27)))) (number "1" (effects (font (size 1.27 1.27))))))
		(symbol "PWR_FLAG_0_1" (polyline (pts (xy 0 0) (xy 0 1.27) (xy -1.016 1.905) (xy 0 2.54) (xy 1.016 1.905) (xy 0 1.27)) (stroke (width 0) (type default)) (fill (type none))))
	)`
}

// AddPwrFlag wires a PWR_FLAG to the point at: a stub of length stub mm
// along direction dir (0 right, 1 up, 2 left, 3 down), the flag's body
// pointing the same way. Returns its reference.
func (e *SchEditor) AddPwrFlag(at Pt, dir int, stub float64) (string, error) {
	if err := e.AddLibSymbol(PwrFlagLibID, PwrFlagSymbolText()); err != nil {
		return "", err
	}
	end := Pt{round4mm(at.X + float64(dirs[dir].X)*stub), round4mm(at.Y + float64(dirs[dir].Y)*stub)}
	e.AddWire(at, end)
	// the body is above the pin at rotation 0 (up); turn it to dir
	rot := float64((dir - 1 + 4) % 4 * 90)
	n := e.flgNext()
	ref := fmt.Sprintf("#FLG%04d", n)
	va := SymbolXform(Pt{0, 3.81}, end, rot, "")
	_, err := e.PlaceSymbol(SymbolInstance{LibID: PwrFlagLibID, Ref: ref, At: end, Rot: rot, Value: "PWR_FLAG",
		Fields: []Field{{Name: "Value", Value: "PWR_FLAG", At: &va}}})
	return ref, err
}

var flgRe = regexp.MustCompile(`"#FLG0*(\d+)"`)

func (e *SchEditor) flgNext() int {
	n := 1
	texts := []string{e.src}
	texts = append(texts, e.items...)
	for _, t := range texts {
		for _, m := range flgRe.FindAllStringSubmatch(t, -1) {
			if v, _ := strconv.Atoi(m[1]); v >= n {
				n = v + 1
			}
		}
	}
	if e.flgFloor > n {
		n = e.flgFloor
	}
	return n
}

// SetFlagFloor makes new #FLG references start at least at n (numbering
// across the sheets of a project).
func (e *SchEditor) SetFlagFloor(n int) { e.flgFloor = n }

// MaxFlagRef is the highest #FLG number in text.
func MaxFlagRef(text string) int {
	n := 0
	for _, m := range flgRe.FindAllStringSubmatch(text, -1) {
		if v, _ := strconv.Atoi(m[1]); v > n {
			n = v
		}
	}
	return n
}
