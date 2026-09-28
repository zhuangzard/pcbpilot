package intent

import (
	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
)

// AnalogInfo summarises the analog SPICE run (`pcbpilot sim analog`) the
// intent carries (additive field intent.json "analog").
type AnalogInfo struct {
	Generator string         `json:"generator"`
	Ngspice   string         `json:"ngspice"` // version, or "missing"
	Blocks    int            `json:"blocks"`
	Simulated int            `json:"simulated"`
	ByClass   map[string]int `json:"byClass,omitempty"`
	Targets   int            `json:"targets"`
	Failing   int            `json:"failing"`
	Changes   int            `json:"changes"`
	Status    string         `json:"status"`
	Output    string         `json:"output,omitempty"` // analog.json path when written
	Note      string         `json:"note,omitempty"`
}

// AnalogInput is what intent derive feeds from the analog run.
type AnalogInput struct {
	Info     AnalogInfo
	Findings []*Finding
}

// FromAnalog converts an analog.json document into intent input: every
// analog finding becomes an intent finding (kind analog-*), refs/nets kept.
func FromAnalog(out *analogsim.Output, path string) *AnalogInput {
	if out == nil {
		return nil
	}
	in := &AnalogInput{Info: AnalogInfo{Generator: out.Generator, Blocks: out.Summary.Blocks, Simulated: out.Summary.Simulated,
		ByClass: out.Summary.ByClass, Targets: out.Summary.Targets, Failing: out.Summary.Failing, Changes: out.Summary.Changes,
		Status: out.Summary.Status, Output: path}}
	if out.Ngspice.Available {
		in.Info.Ngspice = out.Ngspice.Version
	} else {
		in.Info.Ngspice, in.Info.Note = "missing", analogsim.MissingNgspiceNote
	}
	for _, f := range out.Findings {
		refs, nets := f.Refs, f.Nets
		if refs == nil {
			refs = []string{}
		}
		if nets == nil {
			nets = []string{}
		}
		in.Findings = append(in.Findings, &Finding{Severity: f.Severity, Kind: f.Kind, Message: f.Message, Refs: refs, Nets: nets, Suggestion: f.Suggestion})
	}
	return in
}

// findAnalog adds the analog SPICE findings.
func (c *ctx) findAnalog() {
	a := c.in.Analog
	if a == nil {
		return
	}
	info := a.Info
	c.out.Analog = &info
	for _, f := range a.Findings {
		c.add(f.Severity, f.Kind, f.Message, f.Refs, f.Nets, f.Suggestion)
	}
}
