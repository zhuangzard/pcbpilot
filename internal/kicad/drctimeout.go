package kicad

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// EnvDRCTimeout overrides the time one kicad-cli DRC run may take (a Go
// duration, e.g. 90m).
const EnvDRCTimeout = "PCBPILOT_KICAD_DRC_TIMEOUT"

// ErrDRCTimeout marks a DRC run stopped at its time limit (it also wraps
// ErrDRCReport: no report is a check that did not happen).
var ErrDRCTimeout = errors.New("kicad-cli DRC timed out")

// DRCTimeoutFor is the time limit of one kicad-cli DRC run on pcb: the
// EnvDRCTimeout value, else floor + 2 min per MB of board file (DRC time
// grows with the copper: a 15 MB routed 1443-part board took ~20 min on a
// loaded machine), at most 2 h. floor is the caller's minimum (the
// historical fixed limit).
func DRCTimeoutFor(pcb string, floor time.Duration) time.Duration {
	if v := os.Getenv(EnvDRCTimeout); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	d := floor
	if st, err := os.Stat(pcb); err == nil {
		d += time.Duration(st.Size()/(1<<20)) * 2 * time.Minute
	}
	return min(d, 2*time.Hour)
}

// drcTimeoutError names the run that timed out, its limit and how to raise it.
func drcTimeoutError(pcb string, limit time.Duration) error {
	return fmt.Errorf("%w: %w: kicad-cli pcb drc on %s timed out after %s (the limit scales with the board size; set %s to change it)",
		ErrDRCReport, ErrDRCTimeout, pcb, limit, EnvDRCTimeout)
}
