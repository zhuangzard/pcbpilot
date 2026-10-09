package app

import (
	"fmt"
	"os"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/boardmanual"
)

// loadCrossProbe reads the KiCad project behind the manual's cross-probe
// lens: the symbols of the schematic hierarchy, the footprints left out of
// the BOM, and the kicad-cli plots of every page and lens layer.
func loadCrossProbe(sch, pcb string) (*boardmanual.CrossProbeInput, error) {
	pages, syms, err := kicad.ParseSchHierarchy(sch)
	if err != nil {
		return nil, fmt.Errorf("--kicad-sch: %w", err)
	}
	notInBOM, err := kicad.PCBNotInBOM(pcb)
	if err != nil {
		return nil, fmt.Errorf("--kicad-pcb: %w", err)
	}
	dir, err := os.MkdirTemp("", "pcbpilot-xprobe-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	pageSVG, layerSVG, err := kicad.ExportCrossProbeSVGs(sch, pcb, pages, dir)
	if err != nil {
		return nil, err
	}
	in := &boardmanual.CrossProbeInput{NotInBOM: notInBOM}
	for i, p := range pages {
		cp := boardmanual.CPPage{Name: p.Name}
		if pageSVG[i] != "" {
			if cp.SVG, err = os.ReadFile(pageSVG[i]); err != nil {
				return nil, err
			}
		}
		in.Pages = append(in.Pages, cp)
	}
	for i, l := range kicad.CrossProbeLayers {
		b, err := os.ReadFile(layerSVG[i])
		if err != nil {
			return nil, err
		}
		in.Layers = append(in.Layers, boardmanual.CPLayer{Name: l, SVG: b})
	}
	for _, s := range syms {
		in.Symbols = append(in.Symbols, boardmanual.CPSymbol{Ref: s.Ref, Value: s.Value, Page: s.Page,
			MinX: s.Box.MinX, MinY: s.Box.MinY, MaxX: s.Box.MaxX, MaxY: s.Box.MaxY, Rot: s.Rot, InBOM: s.InBOM, OnBoard: s.OnBoard})
	}
	return in, nil
}

// kicadLensPCB is the --kicad-pcb of a kicad route's manual: the routed
// board, when the run has a schematic (no schematic, no lens).
func kicadLensPCB(sch, routed string) string {
	if sch == "" {
		return ""
	}
	return routed
}
