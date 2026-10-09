package app

// route_result.go — the router-agnostic result every gate and the sign-off
// consume. A routing backend (fastroute today; a backend that writes the
// .kicad_pcb directly next) fills a routeResult; nothing downstream reads a
// backend's own report format.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// routeConn is one connection between two pads ("REF.PIN").
type routeConn struct {
	Net  string `json:"net"`
	From string `json:"from"`
	To   string `json:"to"`
}

func (c routeConn) String() string { return fmt.Sprintf("%s %s–%s", c.Net, c.From, c.To) }

// routeResult is the outcome of one routing backend run.
type routeResult struct {
	Router   string   `json:"router"`             // fastroute | …
	Version  string   `json:"version,omitempty"`  // backend version
	PatchSHA string   `json:"patchSha,omitempty"` // sha256 of the backend executable
	Args     []string `json:"args,omitempty"`
	// Session is the routed wiring to import (SES); Board a routed board the
	// backend wrote directly. One of them is set.
	Session string `json:"session,omitempty"`
	Board   string `json:"board,omitempty"`
	// Unrouted lists every connection left open; Blocked those of them the
	// backend found unroutable on the board as loaded (placement / escapes
	// must change). UnroutedCount is authoritative when the backend gives a
	// count without the list.
	Unrouted      []routeConn `json:"unrouted,omitempty"`
	Blocked       []routeConn `json:"blocked,omitempty"`
	UnroutedCount int         `json:"unroutedCount"`
	Violations    int         `json:"violations"`
	// Fixable are routing-caused clearance violations (named).
	Fixable     int      `json:"fixable"`
	FixableList []string `json:"fixableList,omitempty"`
	Report      string   `json:"report,omitempty"` // the backend's own report (evidence only)
	Round       int      `json:"round"`
	Seconds     float64  `json:"seconds"`
}

// fastrouteResult turns the kept fastroute run into a routeResult.
func fastrouteResult(run *fastrouteRun, o fastrouteOpts, dsn string) *routeResult {
	if run == nil {
		return nil
	}
	r := &routeResult{Router: "fastroute", Session: run.Session, UnroutedCount: run.Unrouted, Violations: run.Violations,
		Fixable: run.Fixable, FixableList: run.FixableList, Report: run.Report, Round: run.Round, Seconds: run.Seconds,
		Args: fastrouteArgs(o, dsn, run.Session, run.Report, ""), Version: run.Version, PatchSHA: executableSHA256(o.bin)}
	r.Unrouted, r.Blocked = run.UnroutedList, run.BlockedConns
	return r
}

func executableSHA256(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}

// decodeRouteResult reads a routeResult kept in a summary (the value itself,
// or its JSON form after a round trip).
func decodeRouteResult(v any) *routeResult {
	switch x := v.(type) {
	case *routeResult:
		return x
	case nil:
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var r routeResult
	if json.Unmarshal(b, &r) != nil || r.Router == "" {
		return nil
	}
	return &r
}
