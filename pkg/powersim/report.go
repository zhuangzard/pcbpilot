package powersim

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// WriteReport renders a human Markdown summary of out.
func WriteReport(w io.Writer, out *Output) error {
	var b strings.Builder
	fmt.Fprintf(&b, "# 电源仿真报告 / power simulation\n\n")
	fmt.Fprintf(&b, "Generator `%s`, schemaVersion %d. DC operating point + averaged power tree — not a transient SPICE run.\n\n", out.Generator, out.SchemaVersion)
	if out.Inputs != nil {
		if len(out.Inputs.Connectivity) > 0 {
			fmt.Fprintf(&b, "- connectivity: %s\n", strings.Join(out.Inputs.Connectivity, ", "))
		}
		if len(out.Inputs.Values) > 0 {
			fmt.Fprintf(&b, "- values: %s\n", strings.Join(out.Inputs.Values, ", "))
		}
		if len(out.Inputs.Libraries) > 0 {
			fmt.Fprintf(&b, "- power models: %s\n", strings.Join(out.Inputs.Libraries, ", "))
		}
		if out.Inputs.Live != "" {
			fmt.Fprintf(&b, "- live: %s\n", out.Inputs.Live)
		}
		b.WriteString("\n")
	}
	// Rails per scenario.
	b.WriteString("## Rails\n\n| scenario | net | role | V | I (A) | P = V·I (W) |\n|---|---|---|---:|---:|---:|\n")
	for _, res := range out.Results {
		for _, net := range sortedKeys(res.Nets) {
			nr := res.Nets[net]
			if nr.Role != "power" && nr.Role != "switch" {
				continue
			}
			v := fmt.Sprintf("%.3f", nr.Voltage)
			if nr.VoltageMin != nil && nr.VoltageMax != nil && *nr.VoltageMin != *nr.VoltageMax {
				v = fmt.Sprintf("%.3f…%.3f", *nr.VoltageMin, *nr.VoltageMax)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %.4f | %.4f |\n", res.Scenario, net, nr.Role, v, nr.CurrentA, nr.Voltage*nr.CurrentA)
		}
	}
	// Regulators.
	b.WriteString("\n## Regulators\n\n| scenario | ref | mode | Vin | Vout | Iin (A) | Iout (A) | η | loss (W) |\n|---|---|---|---:|---:|---:|---:|---:|---:|\n")
	for _, res := range out.Results {
		if res.Scenario == "worst" {
			continue
		}
		for _, ref := range sortedKeys(res.Parts) {
			pr := res.Parts[ref]
			if pr.Model != KindBuck && pr.Model != KindLDO {
				continue
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %.3f | %.3f | %.4f | %.4f | %.3f | %.4f |\n", res.Scenario, ref, pr.Mode, pr.VinV, pr.VoutV, pr.InputA, pr.OutputA, pr.Efficiency, pr.PowerW)
		}
	}
	worst := out.Results[len(out.Results)-1]
	// Part power.
	fmt.Fprintf(&b, "\n## Part power (%s)\n\n| ref | model | P (W) | confidence | from | notes |\n|---|---|---:|---|---|---|\n", worst.Scenario)
	refs := sortedKeys(worst.Parts)
	sort.SliceStable(refs, func(i, j int) bool {
		return math.Abs(worst.Parts[refs[i]].PowerW) > math.Abs(worst.Parts[refs[j]].PowerW)
	})
	for _, ref := range refs {
		pr := worst.Parts[ref]
		if math.Abs(pr.PowerW) < 1e-6 && len(pr.Notes) == 0 {
			continue
		}
		p := fmt.Sprintf("%.4f", pr.PowerW)
		if pr.SuppliedW > 0 {
			p = fmt.Sprintf("supplies %.4f", pr.SuppliedW)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", ref, firstNonEmpty(pr.ModelID, pr.Model), p, pr.Confidence, pr.Scenario, strings.Join(pr.Notes, "<br>"))
	}
	// Pin currents.
	fmt.Fprintf(&b, "\n## Pin currents ≥ 1 mA (%s)\n\n| net | ref.pin | name | I (A) | dir | scenario |\n|---|---|---|---:|---|---|\n", worst.Scenario)
	for _, net := range sortedKeys(worst.Nets) {
		for _, p := range worst.Nets[net].Pins {
			if p.CurrentA < 1e-3 {
				continue
			}
			fmt.Fprintf(&b, "| %s | %s.%s | %s | %.4f | %s | %s |\n", net, p.Ref, p.Pin, p.Name, p.CurrentA, p.Dir, p.Scenario)
		}
	}
	if len(worst.Ripple) > 0 {
		fmt.Fprintf(&b, "\n## Switching ripple (%s)\n\n| key | Ipk (A) | Irms (A) | Iavg (A) | ΔI (A) | D | regulator |\n|---|---:|---:|---:|---:|---:|---|\n", worst.Scenario)
		for _, k := range sortedKeys(worst.Ripple) {
			r := worst.Ripple[k]
			fmt.Fprintf(&b, "| %s | %.4f | %.4f | %.4f | %.4f | %.3f | %s |\n", k, r.IPeakA, r.IRmsA, r.IAvgA, r.DeltaIA, r.Duty, r.Regulator)
		}
	}
	if len(out.Models) > 0 {
		b.WriteString("\n## Models\n\n| ref | kind | model | match | confidence |\n|---|---|---|---|---|\n")
		for _, m := range out.Models {
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", m.Ref, m.Kind, m.ModelID, m.Match, m.Confidence)
		}
	}
	if out.SpiceCheck != nil {
		sc := out.SpiceCheck
		fmt.Fprintf(&b, "\n## ngspice cross-check (%s)\n\n", sc.Scenario)
		if sc.Ran {
			fmt.Fprintf(&b, "%d nodes, max |ΔV| = %.3g V (%s), tolerance %.3g V → pass=%v\n", sc.Nodes, sc.MaxDiffV, sc.WorstNet, sc.TolV, sc.Pass)
		} else {
			fmt.Fprintf(&b, "skipped: %s\n", sc.Skipped)
		}
	}
	b.WriteString("\n## Warnings\n\n")
	if len(worst.Warnings) == 0 {
		b.WriteString("- none\n")
	}
	for _, s := range worst.Warnings {
		fmt.Fprintf(&b, "- %s\n", s)
	}
	b.WriteString("\n## Assumptions\n\n")
	for _, s := range worst.Assumptions {
		fmt.Fprintf(&b, "- %s\n", s)
	}
	_, err := io.WriteString(w, b.String())
	return err
}
