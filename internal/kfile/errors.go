package kfile

import (
	"errors"
	"fmt"
)

// Code is a stable machine-readable error code.
type Code string

const (
	CodeLocked     Code = "E_KICAD_LOCKED"
	CodeChanged    Code = "E_FILE_CHANGED"
	CodeRevision   Code = "E_REVISION_MISMATCH"
	CodeSharing    Code = "E_SHARING_VIOLATION"
	CodeTxnPending Code = "E_TXN_PENDING"
	CodeReadOnly   Code = "E_READONLY"
	// CodeWriterBusy: another pcbpilot process holds the project writer lock.
	CodeWriterBusy Code = "E_WRITER_BUSY"
)

// Error is every refusal of this package: a code, the file it concerns, what
// was found and how to fix it.
type Error struct {
	Code   Code       `json:"code"`
	Path   string     `json:"path,omitempty"`
	Detail string     `json:"detail"`
	Fix    string     `json:"fix,omitempty"`
	Locks  []LockInfo `json:"locks,omitempty"`
	Err    error      `json:"-"`
}

func (e *Error) Error() string {
	s := string(e.Code)
	if e.Path != "" {
		s += " " + e.Path
	}
	s += ": " + e.Detail
	if e.Fix != "" {
		s += " (fix: " + e.Fix + ")"
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf returns the Code of the first *Error in err's chain, or "".
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// ExitCode maps err to the process exit code: 0 for nil, 4 when nothing was
// written because the file is locked, changed or at another revision (or
// another pcbpilot writer is busy), 1 otherwise.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	switch CodeOf(err) {
	case CodeLocked, CodeChanged, CodeRevision, CodeWriterBusy:
		return 4
	}
	return 1
}

func changedErr(path, detail string) *Error {
	return &Error{Code: CodeChanged, Path: path, Detail: detail,
		Fix: "the file was modified after pcbpilot read it; re-run so the change is computed from the current file"}
}

func short(sha string) string {
	if sha == "" {
		return "(none)"
	}
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

func wrapf(err error, format string, a ...any) error {
	return fmt.Errorf(format+": %w", append(a, err)...)
}
