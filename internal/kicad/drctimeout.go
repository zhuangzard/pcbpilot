package kicad

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvDRCTimeout overrides the time one kicad-cli DRC run may take (a Go
// duration, e.g. 90m).
const EnvDRCTimeout = "PCBPILOT_KICAD_DRC_TIMEOUT"

// DRCTimeoutFlag is the `kicad drc` flag that overrides the limit of its
// own runs (it wins over EnvDRCTimeout).
const DRCTimeoutFlag = "--timeout"

// DRC time limit terms: the board file adds drcPerBoardMB per MB, the
// project's custom rules file drcPerRules100kB per 100 kB (kicad-cli
// evaluates every rule's condition against the board's items: a 400 kB
// file of some thousand rules ran past the 30 min floor on a board whose
// own size added nothing), and the sum stops at drcTimeoutCap.
const (
	drcPerBoardMB    = 2 * time.Minute
	drcPerRules100kB = 5 * time.Minute
	drcTimeoutCap    = 2 * time.Hour
	// drcLargeRules: from this size on a timeout names the rules file.
	drcLargeRules = 100 << 10
)

// ErrDRCTimeout marks a DRC run stopped at its time limit (it also wraps
// ErrDRCReport: no report is a check that did not happen).
var ErrDRCTimeout = errors.New("kicad-cli DRC timed out")

// drcRulesSize is the size of pcb's custom rules file (<board>.kicad_dru
// next to it: the project's own, or its staged copy), 0 without one.
func drcRulesSize(pcb string) int64 {
	st, err := os.Stat(strings.TrimSuffix(pcb, filepath.Ext(pcb)) + ".kicad_dru")
	if err != nil || st.IsDir() {
		return 0
	}
	return st.Size()
}

// DRCTimeoutFor is the time limit of one kicad-cli DRC run on pcb: the
// EnvDRCTimeout value, else floor + 2 min per MB of board file (DRC time
// grows with the copper: a 15 MB routed 1443-part board took ~20 min on a
// loaded machine) + 5 min per 100 kB of <board>.kicad_dru next to it, at
// most 2 h. floor is the caller's minimum (the historical fixed limit).
func DRCTimeoutFor(pcb string, floor time.Duration) time.Duration {
	if v := os.Getenv(EnvDRCTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	d := floor
	if st, err := os.Stat(pcb); err == nil {
		d += time.Duration(st.Size()/(1<<20)) * drcPerBoardMB
	}
	d += time.Duration(drcRulesSize(pcb)/(100<<10)) * drcPerRules100kB
	return min(d, drcTimeoutCap)
}

// DRCTimeoutWith is the limit of one run: override when positive (the
// --timeout flag), else DRCTimeoutFor (the environment, then the computed
// limit).
func DRCTimeoutWith(pcb string, floor, override time.Duration) time.Duration {
	if override > 0 {
		return override
	}
	return DRCTimeoutFor(pcb, floor)
}

// drcTimeoutError names the run that timed out, its limit and how to raise
// it; a large custom rules file next to pcb is named with its size.
func drcTimeoutError(pcb string, limit time.Duration) error {
	rules := ""
	if n := drcRulesSize(pcb); n >= drcLargeRules {
		rules = fmt.Sprintf("; its custom rules file %s is %d kB, and every rule is evaluated against the board",
			filepath.Base(strings.TrimSuffix(pcb, filepath.Ext(pcb))+".kicad_dru"), n>>10)
	}
	return fmt.Errorf("%w: %w: kicad-cli pcb drc on %s timed out after %s%s (the limit grows with the board file and the custom rules file, at most %s; raise it with %s <duration> on `pcbpilot kicad drc` (--drc-timeout on `kicad fab --preflight`), e.g. %s 90m, or set %s=90m for any command that runs DRC)",
		ErrDRCReport, ErrDRCTimeout, pcb, limit, rules, drcTimeoutCap, DRCTimeoutFlag, DRCTimeoutFlag, EnvDRCTimeout)
}
