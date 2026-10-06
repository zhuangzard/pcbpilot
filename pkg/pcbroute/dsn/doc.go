// Package dsn reads and writes the Specctra files of routing engine v2
// (pkg/pcbroute): DSN into a board and its rules, pcbauto Board to DSN for the
// bench, and SES out and back in (spec 04 §2, §3.8; PLAN.md M3).
//
// The S-expression reader is reused from internal/pcb/specctra by import
// (PLAN.md Q12). M0 freezes Read and WriteSES; M3 replaces their bodies.
package dsn

import (
	"errors"
	"io"

	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/board"
	"github.com/zhuangzard/pcbpilot/pkg/pcbroute/rules"
)

var errNotImplemented = errors.New("dsn: not implemented (PLAN.md M3)")

// Read parses a DSN design into bb (nets, pads, fixed copper, keep-outs) and
// rb (layers, classes, rule scopes, vias).
func Read(src []byte, bb board.Builder, rb rules.Builder) error {
	return errNotImplemented
}

// WriteSES writes the routed copper of v as a Specctra session.
func WriteSES(w io.Writer, v board.View, rs rules.Resolver) error {
	return errNotImplemented
}
