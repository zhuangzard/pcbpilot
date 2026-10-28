// Package gate is the verdict vocabulary of pcbpilot's checks.
//
// A gate either ran and passed, ran and failed, or could not run (missing
// tool, crash, unparsable report): the last is CannotCheck and is never a
// pass. Only Pass satisfies OK, so a check that did not happen can never be
// reported as one that succeeded.
package gate

import "strings"

// Status is one gate's verdict.
type Status string

const (
	Pass        Status = "pass"
	Fail        Status = "fail"
	CannotCheck Status = "cannot-check"
	// Warn: ran and found something; a pass only for an Advisory gate.
	Warn Status = "warn"
	// Skip: not run because the user asked so (or, for an Advisory gate, it
	// does not apply). Never an overall pass for a required gate.
	Skip Status = "skip"
)

// OK is true only for Pass.
func (s Status) OK() bool { return s == Pass }

// ExitCode is the process exit code of an overall verdict: 0 pass, 6 fail,
// 5 for anything not verified (cannot-check, skip, or an unknown status).
func (s Status) ExitCode() int {
	switch s {
	case Pass:
		return 0
	case Fail:
		return 6
	}
	return 5
}

// Parse reads a status written by any pcbpilot report. Unknown text is
// CannotCheck.
func Parse(s string) Status {
	switch st := Status(strings.ToLower(strings.TrimSpace(s))); st {
	case Pass, Fail, CannotCheck, Warn, Skip:
		return st
	}
	return CannotCheck
}

// FromBool is Pass or Fail.
func FromBool(pass bool) Status {
	if pass {
		return Pass
	}
	return Fail
}

// Result is one gate as Aggregate sees it.
type Result struct {
	Name     string `json:"name"`
	Status   Status `json:"status"`
	Advisory bool   `json:"advisory,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// effective is r's contribution to the overall verdict.
func (r Result) effective() Status {
	switch s := Parse(string(r.Status)); s {
	case Warn:
		if r.Advisory {
			return Pass
		}
		return Fail
	case Skip:
		if r.Advisory {
			return Pass
		}
		return Skip
	default:
		return s
	}
}

// Aggregate is the overall verdict: Fail if any gate fails (a warn of a
// required gate counts as a failure); else CannotCheck if any gate could not
// run; else Skip if a required gate was skipped; else Pass. No gates at all
// is CannotCheck: nothing was verified.
func Aggregate(rs ...Result) Status {
	if len(rs) == 0 {
		return CannotCheck
	}
	rank := map[Status]int{Pass: 0, Skip: 1, CannotCheck: 2, Fail: 3}
	out := Pass
	for _, r := range rs {
		if s := r.effective(); rank[s] > rank[out] {
			out = s
		}
	}
	return out
}
