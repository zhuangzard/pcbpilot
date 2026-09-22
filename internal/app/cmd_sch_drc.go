package app

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ── sch drc: official SDK electrical rule check ────────────────────────────
//
// EasyEDA's schematic DRC SDK may return only boolean/aggregate data even when
// includeVerboseError=true. The connector normalizes whatever shape the runtime
// provides into drcReport; itemized UI-panel warnings are reconstructed by
// `sch check`, not by this command.

// drcViolation mirrors one normalized violation from the connector.
type drcViolation struct {
	Level        string          `json:"level"`
	Type         string          `json:"type,omitempty"`
	Rule         string          `json:"rule,omitempty"`
	Message      string          `json:"message,omitempty"`
	PrimitiveIDs []string        `json:"primitiveIds,omitempty"`
	Designators  []string        `json:"designators,omitempty"`
	X            *float64        `json:"x,omitempty"`
	Y            *float64        `json:"y,omitempty"`
	Count        *int            `json:"count,omitempty"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}

// drcSummary mirrors the connector's severity tally.
type drcSummary struct {
	Fatal   int `json:"fatal"`
	Error   int `json:"error"`
	Warn    int `json:"warn"`
	Info    int `json:"info"`
	Unknown int `json:"unknown"`
	Total   int `json:"total"`
}

// drcReport is the normalized DRC result the connector returns.
type drcReport struct {
	Passed           bool           `json:"passed"`
	NativePassed     bool           `json:"nativePassed"`
	Strict           bool           `json:"strict"`
	Fatal            *int           `json:"fatal"`
	Summary          *drcSummary    `json:"summary"`
	Violations       []drcViolation `json:"violations"`
	CountsAvailable  bool           `json:"countsAvailable"`
	DetailsAvailable bool           `json:"detailsAvailable"`
}

// runSchDrc runs schematic DRC, renders the normalized violations, and returns a
// non-nil error (non-zero exit) only when the fatal count is > 0.
func runSchDrc(cfg *appConfig, window string, strict, verbose, asJSON bool, stdout, stderr io.Writer) error {
	payload := map[string]any{
		// Always request the verbose/array overload — it's what yields per-item
		// detail. The connector reads this field (no longer hardcoded). issue #7
		"includeVerboseError": true,
	}
	if strict {
		payload["strict"] = true
	}

	res, err := requestAction(cfg, "schematic.drc.check", window, payload)
	if err != nil {
		return err
	}

	rep, perr := parseDrcReport(res.Result)
	if perr != nil {
		// Fall back to streaming the raw result so the user still sees something
		// useful if the shape is unexpected.
		if b, mErr := json.MarshalIndent(res.Result, "", "  "); mErr == nil {
			_, _ = stdout.Write(b)
			fmt.Fprintln(stdout)
		}
		return perr
	}

	if asJSON {
		// Same {id,type,version,ok,result} envelope as the transparent commands,
		// so the whole sch family parses uniformly (#66).
		if err := encodeResultEnvelope(res, rep, stdout); err != nil {
			return err
		}
	} else {
		renderDrcReport(rep, verbose, stdout)
	}

	if !rep.Passed {
		if rep.Fatal != nil && *rep.Fatal > 0 {
			return fmt.Errorf("sch drc: native verdict failed with %d fatal violation(s)", *rep.Fatal)
		}
		return fmt.Errorf("sch drc: native verdict failed (strict=%t)", rep.Strict)
	}
	return nil
}

// parseDrcReport re-marshals the generic result map into the typed drcReport.
func parseDrcReport(result map[string]any) (drcReport, error) {
	var rep drcReport
	if result == nil {
		return rep, fmt.Errorf("empty DRC result")
	}
	for _, key := range []string{"passed", "nativePassed", "strict", "countsAvailable", "detailsAvailable"} {
		if _, ok := result[key].(bool); !ok {
			return rep, fmt.Errorf("DRC result has no boolean %s field", key)
		}
	}
	b, err := json.Marshal(result)
	if err != nil {
		return rep, err
	}
	if err := json.Unmarshal(b, &rep); err != nil {
		return rep, fmt.Errorf("unexpected DRC result shape: %w", err)
	}
	if rep.Passed != rep.NativePassed {
		return rep, fmt.Errorf("DRC passed/nativePassed verdict mismatch")
	}
	if rep.CountsAvailable {
		if rep.Fatal == nil || rep.Summary == nil {
			return rep, fmt.Errorf("DRC claimed countsAvailable without fatal/summary counts")
		}
	} else if rep.Fatal != nil || rep.Summary != nil {
		return rep, fmt.Errorf("DRC returned counts while countsAvailable=false")
	}
	return rep, nil
}

// drcLevelTag maps a severity to the left-column tag used in the human view.
func drcLevelTag(level string) string {
	switch strings.ToLower(level) {
	case "fatal":
		return "FATAL"
	case "error":
		return "ERROR"
	case "warn":
		return "WARN"
	case "info":
		return "INFO"
	default:
		return "?????"
	}
}

// renderDrcReport prints a compact, per-violation human summary.
func renderDrcReport(rep drcReport, verbose bool, w io.Writer) {
	if rep.Summary == nil {
		fmt.Fprintf(w, "sch drc: native verdict %s (strict=%t) — counts unavailable\n",
			drcVerdictLabel(rep.Passed), rep.Strict)
	} else {
		s := rep.Summary
		fmt.Fprintf(w, "sch drc: %d reported violation(s) — %d fatal, %d error, %d warn, %d info\n",
			s.Total, s.Fatal, s.Error, s.Warn, s.Info)
	}

	for _, v := range rep.Violations {
		tag := drcLevelTag(v.Level)
		rule := v.Rule
		if rule == "" {
			rule = v.Type
		}
		if rule == "" {
			rule = "-"
		}
		msg := v.Message
		if msg == "" && v.Count != nil {
			// Aggregate-only node: the build gave a count but no per-item detail.
			msg = fmt.Sprintf("%d issue(s) — EDA returned no per-item detail", *v.Count)
		}
		line := fmt.Sprintf("  %-5s  %s  %s", tag, rule, msg)
		if v.X != nil && v.Y != nil {
			line += fmt.Sprintf("  @(%.2f,%.2f)", *v.X, *v.Y)
		}
		if refs := append(append([]string{}, v.Designators...), v.PrimitiveIDs...); len(refs) > 0 {
			line += "  [" + strings.Join(refs, ",") + "]"
		}
		fmt.Fprintln(w, line)
	}

	if verbose {
		for _, v := range rep.Violations {
			if len(v.Raw) > 0 {
				fmt.Fprintf(w, "    raw: %s\n", string(v.Raw))
			}
		}
	}

	if rep.Passed {
		if rep.Summary == nil {
			fmt.Fprintln(w, "✓ Native DRC passed; warning/error counts are unavailable")
		} else if rep.Summary.Total == 0 {
			fmt.Fprintln(w, "✓ Native DRC passed — 0 reported violations")
		} else {
			fmt.Fprintf(w, "✓ Native DRC passed — %d reported item(s) remain advisory in this mode\n", rep.Summary.Total)
		}
		return
	}
	if rep.Fatal != nil && *rep.Fatal > 0 {
		fmt.Fprintf(w, "✗ Native DRC failed with %d fatal violation(s)\n", *rep.Fatal)
	} else {
		fmt.Fprintf(w, "✗ Native DRC failed (strict=%t); review the EasyEDA DRC panel\n", rep.Strict)
	}
}

func drcVerdictLabel(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}
