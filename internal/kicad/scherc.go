package kicad

// scherc.go — kicad-cli ERC as JSON, the hierarchy's sheet paths, and the
// PWR_FLAG that clears "power input pin not driven" on a net whose power
// comes from off the schematic (a connector, a regulator modelled as
// passive pins).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
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

// ERCReport is kicad-cli's ERC of a hierarchy. Item positions are sheet
// millimetres: PosScale is the factor applied to what kicad-cli reported
// (100 for KiCad <= 10.0.6, see ercPosScale), PosNote says why. PosScale 0:
// the units could not be told, positions are as reported — not to place
// anything by.
type ERCReport struct {
	Errors       int            `json:"errors"`
	Warnings     int            `json:"warnings"`
	KicadVersion string         `json:"kicadVersion,omitempty"`
	PosScale     float64        `json:"posScale"`
	PosNote      string         `json:"posNote,omitempty"`
	Violations   []ERCViolation `json:"violations"`
}

type ercJSON struct {
	KicadVersion    string `json:"kicad_version"`
	CoordinateUnits string `json:"coordinate_units"`
	Sheets          []struct {
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
	cmd, cancel := SchCLICommand(cli, "sch", "erc", "--format", "json", "--severity-all", "-o", out, root)
	defer cancel()
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kicad-cli sch erc: %v: %s", err, strings.TrimSpace(buf.String()))
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("kicad-cli wrote no ERC report: %s", strings.TrimSpace(buf.String()))
	}
	files, _ := SheetPaths(root)
	return ParseERCReport(data, ERCParseOptions{
		Files: files,
		// a report without kicad_version (older kicad-cli) asks the binary
		CLIVersion: func() string {
			vc, vcancel := SchCLICommand(cli, "version")
			defer vcancel()
			b, _ := vc.Output()
			return strings.TrimSpace(string(b))
		},
		Anchors: SheetAnchors,
	})
}

// ERCParseOptions is what ParseERCReport needs besides the JSON.
type ERCParseOptions struct {
	Files      map[string]string      // uuid path → sheet file (SheetPaths)
	CLIVersion func() string          // asked only when the report names no kicad_version
	Anchors    func(file string) []Pt // a sheet's item points (SheetAnchors), for an unknown version
}

// ParseERCReport parses kicad-cli's ERC JSON. Item positions come out in mm
// (PosScale 100 or 1), or as reported with PosScale 0 when the units cannot
// be told (see ercPosScale).
func ParseERCReport(data []byte, opt ERCParseOptions) (*ERCReport, error) {
	var x ercJSON
	if err := json.Unmarshal(data, &x); err != nil {
		return nil, fmt.Errorf("parse ERC report: %w", err)
	}
	ver := strings.TrimSpace(x.KicadVersion)
	if ver == "" && opt.CLIVersion != nil {
		ver = opt.CLIVersion()
	}
	var pos []ercPos
	for _, s := range x.Sheets {
		for _, v := range s.Violations {
			for _, it := range v.Items {
				pos = append(pos, ercPos{opt.Files[s.UUIDPath], Pt{it.Pos.X, it.Pos.Y}})
			}
		}
	}
	scale, note := ercPosScale(ver, pos, opt.Anchors)
	r := &ERCReport{KicadVersion: ver, PosScale: scale, PosNote: note}
	k := scale
	if k == 0 {
		k = 1
	}
	for _, s := range x.Sheets {
		for _, v := range s.Violations {
			ev := ERCViolation{Sheet: s.Path, UUIDPath: s.UUIDPath, File: opt.Files[s.UUIDPath], Type: v.Type, Severity: v.Severity, Description: v.Description}
			for _, it := range v.Items {
				at := Pt{round4mm(it.Pos.X * k), round4mm(it.Pos.Y * k)}
				ev.Items = append(ev.Items, ERCItem{Description: it.Description, Pos: at, UUID: it.UUID})
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

// ercPos is a reported item position and its sheet file.
type ercPos struct {
	File string
	At   Pt
}

// kicad-cli up to 10.0.6 writes ERC item positions in 1/100 mm units while
// the report says "coordinate_units": "mm" (KiCad issue #25582, fixed in
// 10.0.7): a pin at (50.8, 54.61) is reported at (0.508, 0.5461).
const ercBugScale = 100

// ercAnchorTol is how close a scaled position must come to a sheet item:
// a 10.0.6 position is rounded to 0.0001 of 1/100 mm (0.005 mm after ×100).
const ercAnchorTol = 0.02

var ercVerRe = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// ercPosScale is the factor that turns a report's item positions into mm,
// 0 when it cannot be told. The version decides when it is known: 10.0.0–
// 10.0.6 are 100, 10.0.7+, later 10.x releases and 11+ are 1 — sheet
// geometry is not consulted (an item off the page is legal and proves
// nothing). An unknown or unparsable version (9.x, a 10.99 nightly, none)
// is decided by geometry: the scale under which a strict majority (more
// than half) of the positions land on a pin, symbol, wire end, label,
// junction, no-connect or sheet pin of their sheet (ercAnchorTol), the
// other scale on at most a third as many. Neither or both: 0 — consumers that place parts at reported
// positions must refuse rather than guess.
func ercPosScale(version string, pos []ercPos, anchors func(file string) []Pt) (float64, string) {
	if m := ercVerRe.FindStringSubmatch(version); m != nil {
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		patch, _ := strconv.Atoi(m[3])
		if major == 10 && minor == 0 && patch <= 6 {
			return ercBugScale, fmt.Sprintf("KiCad %s reports ERC positions 100× too small (#25582, fixed in 10.0.7): scaled ×100 to mm", version)
		}
		if (major == 10 && minor < 99) || major > 10 {
			return 1, ""
		}
	}
	if len(pos) == 0 {
		return 1, ""
	}
	m1, m100 := ercAnchorHits(pos, anchors)
	n := len(pos)
	// a strict majority for one scale, the other at most a third of it
	clear := func(a, b int) bool { return 2*a > n && 3*b <= a }
	switch {
	case clear(m100, m1):
		return ercBugScale, fmt.Sprintf("KiCad version %q not known for ERC positions; %d of %d positions land on sheet items only when scaled ×100 (#25582): scaled ×100 to mm", version, m100, n)
	case clear(m1, m100):
		return 1, fmt.Sprintf("KiCad version %q not known for ERC positions; %d of %d land on sheet items as mm", version, m1, n)
	}
	return 0, fmt.Sprintf("KiCad version %q not known for ERC positions and the sheets do not tell mm from 1/100 mm (%d of %d positions on sheet items as mm, %d ×100): positions are as reported, units unknown", version, m1, n, m100)
}

// ercAnchorHits counts the positions that land on an item of their sheet
// as reported and scaled ×100.
func ercAnchorHits(pos []ercPos, anchors func(file string) []Pt) (m1, m100 int) {
	if anchors == nil {
		return 0, 0
	}
	cache := map[string][]Pt{}
	near := func(pts []Pt, p Pt) bool {
		for _, a := range pts {
			if math.Abs(a.X-p.X) <= ercAnchorTol && math.Abs(a.Y-p.Y) <= ercAnchorTol {
				return true
			}
		}
		return false
	}
	for _, p := range pos {
		if p.File == "" {
			continue
		}
		pts, ok := cache[p.File]
		if !ok {
			pts = anchors(p.File)
			cache[p.File] = pts
		}
		if near(pts, p.At) {
			m1++
		}
		if near(pts, Pt{p.At.X * ercBugScale, p.At.Y * ercBugScale}) {
			m100++
		}
	}
	return m1, m100
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

// EnsurePwrFlagInLib adds PWR_FLAG to a project's pcbpilot_power.kicad_sym
// (written by sch-import) when it lacks it, so ERC finds the library symbol.
// A missing library file is not an error.
func EnsurePwrFlagInLib(dir string) error {
	p := filepath.Join(dir, "pcbpilot_power.kicad_sym")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	if strings.Contains(string(b), `(symbol "PWR_FLAG"`) {
		return nil
	}
	i := strings.LastIndex(string(b), ")")
	if i < 0 {
		return fmt.Errorf("%s: not a symbol library", p)
	}
	return os.WriteFile(p, []byte(string(b[:i])+"\t"+PwrFlagSymbolText()+"\n)\n"), 0o644)
}
