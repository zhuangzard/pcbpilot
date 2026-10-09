package kicad

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Cross-probe inputs of the board manual: where every symbol sits on which
// schematic page, which footprints are left out of the BOM, and the SVG
// pictures of the schematic pages and the PCB layers (kicad-cli, page
// coordinates: 1 SVG user unit = 1 mm, origin = the page's top-left corner,
// which is KiCad's own coordinate origin for both editors).

// SchPage is one sheet instance of a schematic hierarchy.
type SchPage struct {
	Name string // "" for the root sheet, else the sheet path "A/B"
	File string // .kicad_sch path
	Path string // instance path "/<root uuid>/<sheet uuid>…"
}

// SchSymbol is one placed symbol unit (mm, y down, sheet coordinates).
type SchSymbol struct {
	Ref, Value, LibID string
	Page              int // index in the pages of ParseSchHierarchy
	Unit              int
	X, Y, Rot         float64
	Mirror            string
	Box               Box // body + pins
	InBOM, OnBoard    bool
}

// ParseSchHierarchy walks the hierarchy under root (.kicad_sch) and returns
// its sheet instances (root first, depth first in file order) and every
// symbol unit with the reference of its own instance ("#" power symbols
// and symbols whose library body is unknown are left out).
func ParseSchHierarchy(root string) ([]SchPage, []SchSymbol, error) {
	var pages []SchPage
	var syms []SchSymbol
	var walk func(file, name, path string, depth int) error
	walk = func(file, name, path string, depth int) error {
		if depth > 16 {
			return fmt.Errorf("%s: sheet hierarchy deeper than 16 (recursive sheet?)", file)
		}
		src, err := os.ReadFile(file)
		if err != nil {
			return err
		}
		n, err := parseSx(string(src))
		if err != nil {
			return fmt.Errorf("%s: %w", file, err)
		}
		if path == "" {
			path = "/" + atomOf(n.child("uuid"))
		}
		page := len(pages)
		pages = append(pages, SchPage{Name: name, File: file, Path: path})
		syms = append(syms, schPageSymbols(n, page, path)...)
		for _, c := range n.list {
			if c.head() != "sheet" {
				continue
			}
			sub := propVal(c, "Sheetfile")
			if sub == "" {
				continue
			}
			sn := propVal(c, "Sheetname")
			if name != "" {
				sn = name + "/" + sn
			}
			if err := walk(filepath.Join(filepath.Dir(file), sub), sn, path+"/"+atomOf(c.child("uuid")), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(root, "", "", 0); err != nil {
		return nil, nil, err
	}
	return pages, syms, nil
}

func atomOf(n *sexp) string {
	if n == nil || len(n.list) < 2 {
		return ""
	}
	return n.list[1].atom
}

// schPageSymbols lists the symbols of one parsed sheet as seen from the
// sheet instance path.
func schPageSymbols(root *sexp, page int, path string) []SchSymbol {
	libs := map[string]Box{}
	if ls := root.child("lib_symbols"); ls != nil {
		for _, s := range ls.list {
			if s.head() == "symbol" && len(s.list) > 1 {
				libs[s.list[1].atom] = libBox(s)
			}
		}
	}
	var out []SchSymbol
	for _, n := range root.list {
		if n.head() != "symbol" {
			continue
		}
		ref, unit := instanceRef(n, path)
		if ref == "" || strings.HasPrefix(ref, "#") {
			continue
		}
		box, ok := instanceBox(n, libs)
		if !ok {
			continue
		}
		at := n.child("at")
		s := SchSymbol{Ref: ref, Value: propVal(n, "Value"), LibID: atomOf(n.child("lib_id")), Page: page, Unit: unit,
			X: at.num(1), Y: at.num(2), Rot: at.num(3), Mirror: atomOf(n.child("mirror")), Box: box,
			InBOM: atomOf(n.child("in_bom")) != "no", OnBoard: atomOf(n.child("on_board")) != "no"}
		if u := n.child("unit"); s.Unit == 0 && u != nil {
			s.Unit = int(u.num(1))
		}
		out = append(out, s)
	}
	return out
}

// instanceRef is the symbol's reference (and unit) on the sheet instance
// path; the Reference field when the file has no instance for it.
func instanceRef(sym *sexp, path string) (string, int) {
	if inst := sym.child("instances"); inst != nil {
		for _, pr := range inst.list {
			if pr.head() != "project" {
				continue
			}
			for _, p := range pr.list {
				if p.head() == "path" && atomOf(p) == path {
					unit := 0
					if u := p.child("unit"); u != nil {
						unit = int(u.num(1))
					}
					return atomOf(p.child("reference")), unit
				}
			}
		}
	}
	return propVal(sym, "Reference"), 0
}

// PCBNotInBOM lists the footprints of a .kicad_pcb that are not BOM parts
// (attr exclude_from_bom or board_only): mounting holes, logos, test pads.
func PCBNotInBOM(pcb string) (map[string]bool, error) {
	src, err := os.ReadFile(pcb)
	if err != nil {
		return nil, err
	}
	n, err := parseSx(string(src))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", pcb, err)
	}
	out := map[string]bool{}
	for _, fp := range n.list {
		if fp.head() != "footprint" {
			continue
		}
		attr := fp.child("attr")
		if attr == nil {
			continue
		}
		for _, a := range attr.list[1:] {
			if a.atom == "exclude_from_bom" || a.atom == "board_only" {
				out[propVal(fp, "Reference")] = true
			}
		}
	}
	return out, nil
}

// CrossProbeLayers are the PCB layers the manual's cross-probe lens stacks
// (bottom first).
var CrossProbeLayers = []string{"B.Cu", "F.Cu", "F.Silkscreen", "Edge.Cuts"}

// ExportCrossProbeSVGs plots every page of the schematic hierarchy and each
// cross-probe PCB layer into dir with kicad-cli, all in page coordinates
// (mm, drawing sheet left out so the frame never hides a part). It returns
// the page SVG per page index of ParseSchHierarchy and the layer SVG per
// CrossProbeLayers entry.
func ExportCrossProbeSVGs(sch, pcb string, pages []SchPage, dir string) (pageSVG, layerSVG []string, err error) {
	cli, err := KicadCLI()
	if err != nil {
		return nil, nil, err
	}
	run := func(args ...string) error {
		var buf bytes.Buffer
		cmd := exec.Command(cli, args...)
		cmd.Stdout, cmd.Stderr = &buf, &buf
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("kicad-cli %s: %v: %s", strings.Join(args[:3], " "), err, strings.TrimSpace(buf.String()))
		}
		return nil
	}
	sdir := filepath.Join(dir, "sch")
	if err := run("sch", "export", "svg", "--exclude-drawing-sheet", "--no-background-color", "-o", sdir, sch); err != nil {
		return nil, nil, err
	}
	// kicad-cli names the root page <base>.svg and a sub-sheet
	// <base>-<sheet path with "/" → "-">.svg.
	base := strings.TrimSuffix(filepath.Base(sch), filepath.Ext(sch))
	for _, p := range pages {
		f := base
		if p.Name != "" {
			f += "-" + strings.ReplaceAll(p.Name, "/", "-")
		}
		f = filepath.Join(sdir, f+".svg")
		if _, err := os.Stat(f); err != nil {
			f = "" // not plotted under the expected name: the page has no picture
		}
		pageSVG = append(pageSVG, f)
	}
	for _, l := range CrossProbeLayers {
		f := filepath.Join(dir, "pcb-"+strings.ReplaceAll(l, ".", "_")+".svg")
		if err := run("pcb", "export", "svg", "--mode-single", "-l", l, "--page-size-mode", "1", "--exclude-drawing-sheet",
			"--black-and-white", "--drill-shape-opt", "2", "-o", f, pcb); err != nil {
			return nil, nil, err
		}
		layerSVG = append(layerSVG, f)
	}
	return pageSVG, layerSVG, nil
}

// Valid reports a finite, non-empty box.
func (b Box) Valid() bool {
	return !math.IsInf(b.MinX, 0) && !math.IsInf(b.MaxX, 0) && b.MaxX >= b.MinX && b.MaxY >= b.MinY
}
