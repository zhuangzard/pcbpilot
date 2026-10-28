package app

// kicad_sch_review.go — the evidence and waivers of the schematic design
// review (kicad.Review): `kicad sch-check --review` and the sch-review gate
// of `kicad sch-build` / `sch-edit`. The review itself is in
// internal/kicad/schreview.go; this file reads intent.json (nets, blocks,
// the optional review notes) and a `sim power` document into
// kicad.ReviewHints, applies --waivers by fingerprint and records the
// input hashes.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zhuangzard/pcbpilot/internal/kicad"
	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/netname"
)

// schReviewGate is the gate (and waiver) name of the review.
const schReviewGate = "sch-review"

// reviewIntentDoc is the subset of intent.json the review reads; unknown
// fields are ignored (intent.json is additive).
type reviewIntentDoc struct {
	Nets map[string]struct {
		Role    string `json:"role"`
		Voltage struct {
			Nom float64 `json:"nom"`
		} `json:"voltage"`
		CurrentSource string `json:"currentSource"`
	} `json:"nets"`
	Blocks []struct {
		Function string `json:"function"`
		Core     string `json:"core"`
	} `json:"blocks"`
	Review *intent.ReviewNotes `json:"review"`
}

// reviewSimDoc is the subset of a `sim power` document the review reads.
type reviewSimDoc struct {
	Results []struct {
		Scenario string `json:"scenario"`
		Nets     map[string]struct {
			Voltage  float64 `json:"voltage"`
			Floating bool    `json:"floating"`
		} `json:"nets"`
	} `json:"results"`
}

type schReviewOpts struct {
	root       string
	nl         *kicad.SchNetlist // nil: exported from root
	erc        *kicad.ERCReport  // nil: run on root
	intentPath string
	simPath    string
	waivers    []gateWaiver
}

// runSchReview runs the review on a root sheet. Errors are tool or input
// errors (no netlist, unreadable intent), never a verdict.
func runSchReview(o schReviewOpts) (*kicad.SchReview, error) {
	t0 := time.Now()
	var inputs []kicad.ReviewInputFile
	addInput := func(kind, p string) {
		f := kicad.ReviewInputFile{Kind: kind, Path: p}
		if b, err := os.ReadFile(p); err == nil {
			f.SHA256 = fileSHA(b)
		}
		inputs = append(inputs, f)
	}
	files, _ := kicad.SheetFiles(o.root)
	for _, f := range files {
		addInput("sheet", f)
	}
	nl := o.nl
	if nl == nil {
		var err error
		if nl, err = kicad.ExportSchNetlist(o.root); err != nil {
			return nil, err
		}
	}
	erc := o.erc
	if erc == nil {
		var err error
		if erc, err = kicad.RunERC(o.root); err != nil {
			return nil, err
		}
	}
	ids := make([]string, 0, len(nl.Nets))
	for _, n := range nl.Nets {
		ids = append(ids, n.Name)
	}
	hints, notes, err := loadReviewHints(o.intentPath, o.simPath, ids, nl)
	if err != nil {
		return nil, err
	}
	if o.intentPath != "" {
		addInput("intent", o.intentPath)
	}
	if o.simPath != "" {
		addInput("sim", o.simPath)
	}
	in := kicad.ReviewInput{Netlist: nl, ERC: erc, ERCIgnored: kicadERCIgnored(o.root), Hints: hints}
	in.Hier, in.HierSheets, in.HierErr = kicad.SheetHierarchyMismatches(o.root)
	in.HierRan = true
	rep, err := kicad.Review(in)
	if err != nil {
		return nil, err
	}
	rep.Inputs, rep.Notes = inputs, notes
	rep.Judge(reviewWaiver(o.waivers))
	rep.RuntimeMs = float64(time.Since(t0).Microseconds()) / 1000
	return rep, nil
}

// reviewWaiver matches a waiver of gate sch-review whose match is the
// finding's fingerprint (exactly: never message text or coordinates).
func reviewWaiver(ws []gateWaiver) func(string) (string, bool) {
	return func(fp string) (string, bool) {
		for _, w := range ws {
			if w.Gate == schReviewGate && w.Match == fp {
				return fmt.Sprintf("waived: %s (%s)", w.Reason, w.By), true
			}
		}
		return "", false
	}
}

// kicadERCIgnored lists the ERC rules the project next to root sets to
// "ignore" (kicad-cli then does not report them at all).
func kicadERCIgnored(root string) map[string]bool {
	out := map[string]bool{}
	b, err := os.ReadFile(strings.TrimSuffix(root, filepath.Ext(root)) + ".kicad_pro")
	if err != nil {
		return out
	}
	var pro struct {
		ERC struct {
			Rules map[string]string `json:"rule_severities"`
		} `json:"erc"`
	}
	if json.Unmarshal(b, &pro) != nil {
		return out
	}
	for k, v := range pro.ERC.Rules {
		if v == "ignore" {
			out[k] = true
		}
	}
	return out
}

// loadReviewHints reads intent.json and a sim power document into hints
// keyed by the netlist's net identities. A declared name that matches no
// net or part is a note, not a guess.
func loadReviewHints(intentPath, simPath string, ids []string, nl *kicad.SchNetlist) (kicad.ReviewHints, []string, error) {
	h := kicad.ReviewHints{NetRole: map[string]string{}, NetVoltage: map[string]float64{}, VoltageSource: map[string]string{},
		Regulators: map[string]string{}, PullUp: map[string]string{}, PullDown: map[string]string{}, Tolerant: map[string]string{},
		PullUpNet: map[string]string{}, ReferenceRail: map[string]string{}, SinglePinNet: map[string]string{},
		TestPoint: map[string]string{}, Module: map[string]string{}, Capless: map[string]string{}}
	var notes []string
	res := netname.NewResolver(ids)
	net := func(name, what string) (string, bool) {
		id, m := res.Resolve(name)
		switch m {
		case netname.Found:
			return id, true
		case netname.Ambiguous:
			notes = append(notes, fmt.Sprintf("%s: net %q is ambiguous (%s); use its full name", what, name, strings.Join(res.Candidates(name), ", ")))
		default:
			notes = append(notes, fmt.Sprintf("%s: no net %q in the schematic", what, name))
		}
		return "", false
	}
	refs := map[string]bool{}
	for _, c := range nl.Components {
		refs[c.Ref] = true
	}
	if intentPath != "" {
		b, err := os.ReadFile(intentPath)
		if err != nil {
			return h, nil, fmt.Errorf("--intent: %w", err)
		}
		var doc reviewIntentDoc
		if err := json.Unmarshal(b, &doc); err != nil {
			return h, nil, fmt.Errorf("--intent %s: %w", intentPath, err)
		}
		if doc.Nets == nil {
			return h, nil, fmt.Errorf("--intent %s: no nets{} (not an intent.json from `pcbpilot intent derive`)", intentPath)
		}
		h.Intent = intentPath
		names := make([]string, 0, len(doc.Nets))
		for n := range doc.Nets {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			id, m := res.Resolve(n)
			if m != netname.Found {
				continue // intent may know nets the review does not (e.g. older schematic)
			}
			np := doc.Nets[n]
			h.NetRole[id] = np.Role
			if np.Voltage.Nom != 0 || np.Role == "power" {
				h.NetVoltage[id] = np.Voltage.Nom
				h.VoltageSource[id] = "intent " + filepath.Base(intentPath)
			}
		}
		for _, bl := range doc.Blocks {
			switch bl.Function {
			case "ldo", "buck", "boost":
				if bl.Core != "" {
					h.Regulators[bl.Core] = "intent block " + bl.Function
				}
			}
		}
		if r := doc.Review; r != nil {
			src := func(f string) string { return "intent review." + f }
			pins := func(xs []string, m map[string]string, f string) {
				for _, x := range xs {
					ref, pin, ok := strings.Cut(strings.Replace(x, ":", ".", 1), ".")
					if !ok || !refs[ref] {
						notes = append(notes, fmt.Sprintf("intent review.%s: %q is not REF.PIN of a part in the schematic", f, x))
						continue
					}
					m[ref+"."+pin] = src(f)
					m[ref+"."+strings.ToUpper(pin)] = src(f)
				}
			}
			pins(r.InternalPullups, h.PullUp, "internalPullups")
			pins(r.InternalPulldowns, h.PullDown, "internalPulldowns")
			pins(r.TolerantPins, h.Tolerant, "tolerantPins")
			nets := func(xs []string, m map[string]string, f string) {
				for _, x := range xs {
					if id, ok := net(x, "intent review."+f); ok {
						m[id] = src(f)
					}
				}
			}
			nets(r.PulledUpNets, h.PullUpNet, "pulledUpNets")
			nets(r.ReferenceRails, h.ReferenceRail, "referenceRails")
			nets(r.SinglePinNets, h.SinglePinNet, "singlePinNets")
			parts := func(xs []string, m map[string]string, f string) {
				for _, x := range xs {
					if !refs[x] {
						notes = append(notes, fmt.Sprintf("intent review.%s: no part %q in the schematic", f, x))
						continue
					}
					m[x] = src(f)
				}
			}
			parts(r.TestPoints, h.TestPoint, "testPoints")
			parts(r.Modules, h.Module, "modules")
			parts(r.CaplessRegulators, h.Capless, "caplessRegulators")
			h.Aliases = r.Aliases
		}
	}
	if simPath != "" {
		b, err := os.ReadFile(simPath)
		if err != nil {
			return h, nil, fmt.Errorf("--sim: %w", err)
		}
		var doc reviewSimDoc
		if err := json.Unmarshal(b, &doc); err != nil {
			return h, nil, fmt.Errorf("--sim %s: %w", simPath, err)
		}
		if len(doc.Results) == 0 {
			return h, nil, fmt.Errorf("--sim %s: no results[] (not a `pcbpilot sim power` document)", simPath)
		}
		h.Sim = simPath
		r := doc.Results[0]
		for _, x := range doc.Results {
			if x.Scenario == "worst" {
				r = x
			}
		}
		for n, nr := range r.Nets {
			id, m := res.Resolve(n)
			if m != netname.Found || nr.Floating {
				continue
			}
			if _, ok := h.NetVoltage[id]; !ok { // intent (declared rails) wins
				h.NetVoltage[id] = nr.Voltage
				h.VoltageSource[id] = "sim " + filepath.Base(simPath) + " (" + r.Scenario + ")"
			}
		}
	}
	sort.Strings(notes)
	return h, notes, nil
}

// reviewMode parses --sch-review of sch-build / sch-edit.
func reviewMode(s string) (string, error) {
	switch s {
	case "", "advisory":
		return "advisory", nil
	case "off", "strict":
		return s, nil
	}
	return "", fmt.Errorf("--sch-review %q: want off, advisory or strict", s)
}
