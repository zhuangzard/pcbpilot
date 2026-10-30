package kicad

// pinpad.go — pin↔pad acceptance check between a KiCad symbol and footprint.
// Every symbol pin number should have a footprint pad and vice versa,
// except explicitly declared exceptions (e.g. mechanical/NPTH pads).

import (
	"fmt"
	"sort"
	"strings"
)

// PinPadResult is the outcome of a pin↔pad consistency check.
type PinPadResult struct {
	OK            bool     `json:"ok"`
	UnmatchedPins []string `json:"unmatchedPins,omitempty"` // pin numbers with no matching pad
	UnmatchedPads []string `json:"unmatchedPads,omitempty"` // pad numbers with no matching pin
	MatchedCount  int      `json:"matchedCount"`            // pins successfully matched
	Errors        []string `json:"errors,omitempty"`        // human-readable messages
}

// footprintPadInfo describes one pad's number and type from a .kicad_mod file.
type footprintPadInfo struct {
	Number string
	Type   string // "smd", "thru_hole", "np_thru_hole"
}

// footprintPadInfos extracts pad number+type pairs from a .kicad_mod text.
func footprintPadInfos(fpText string) ([]footprintPadInfo, error) {
	root, err := parseSx(strings.TrimSpace(fpText))
	if err != nil {
		return nil, err
	}
	var pads []footprintPadInfo
	for _, ch := range root.list {
		if ch.head() == "pad" && len(ch.list) > 2 {
			pads = append(pads, footprintPadInfo{
				Number: ch.list[1].atom,
				Type:   ch.list[2].atom,
			})
		}
	}
	return pads, nil
}

// CheckPinPad checks that every symbol pin number has a footprint pad
// and vice versa.
//
// symbolText is a (symbol ...) S-expression (one symbol from a .kicad_sym).
// footprintText is a (footprint ...) S-expression (one .kicad_mod file).
// excludePads lists pad numbers to exclude from matching (e.g. mechanical NPTH
// pads with no corresponding symbol pin). Empty pad numbers ("") and
// np_thru_hole pads are always excluded automatically.
func CheckPinPad(symbolText, footprintText string, excludePads []string) (*PinPadResult, error) {
	// Extract pin numbers from symbol.
	pins, err := LibSymbolPins(symbolText)
	if err != nil {
		return nil, fmt.Errorf("parse symbol pins: %w", err)
	}

	// Extract pad numbers and types from footprint.
	padInfos, err := footprintPadInfos(footprintText)
	if err != nil {
		return nil, fmt.Errorf("parse footprint pads: %w", err)
	}

	// Build sets.
	pinSet := make(map[string]bool)
	for _, p := range pins {
		if p.Number != "" {
			pinSet[p.Number] = true
		}
	}

	excludeSet := make(map[string]bool)
	excludeSet[""] = true // always exclude blank pad numbers
	for _, e := range excludePads {
		excludeSet[e] = true
	}

	padSet := make(map[string]bool)
	for _, p := range padInfos {
		// Auto-exclude np_thru_hole pads (mechanical, no symbol pin).
		if p.Type == "np_thru_hole" {
			continue
		}
		if !excludeSet[p.Number] {
			padSet[p.Number] = true
		}
	}

	// Compare.
	var unmatchedPins []string
	for num := range pinSet {
		if !padSet[num] {
			unmatchedPins = append(unmatchedPins, num)
		}
	}
	sort.Strings(unmatchedPins)

	var unmatchedPads []string
	for num := range padSet {
		if !pinSet[num] {
			unmatchedPads = append(unmatchedPads, num)
		}
	}
	sort.Strings(unmatchedPads)

	matched := 0
	for num := range pinSet {
		if padSet[num] {
			matched++
		}
	}

	// Reject empty results: if neither side has any entries, something is wrong
	// (malformed input or wrong file type).
	if len(pinSet) == 0 && len(padSet) == 0 {
		return &PinPadResult{
			OK:     false,
			Errors: []string{"no pins found in symbol and no pads found in footprint"},
		}, nil
	}

	result := &PinPadResult{
		OK:            len(unmatchedPins) == 0 && len(unmatchedPads) == 0,
		UnmatchedPins: unmatchedPins,
		UnmatchedPads: unmatchedPads,
		MatchedCount:  matched,
	}

	// Build error messages.
	if len(unmatchedPins) > 0 {
		result.Errors = append(result.Errors,
			fmt.Sprintf("symbol pins with no footprint pad: %s", strings.Join(unmatchedPins, ", ")))
	}
	if len(unmatchedPads) > 0 {
		result.Errors = append(result.Errors,
			fmt.Sprintf("footprint pads with no symbol pin: %s", strings.Join(unmatchedPads, ", ")))
	}

	return result, nil
}
