package intent

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// WriteReport renders intent.md: the human reading of intent.json.
func WriteReport(w io.Writer, in *Intent) error {
	var b strings.Builder
	b.WriteString("# 设计意图 / design intent\n\n")
	fmt.Fprintf(&b, "Generator `%s`, schemaVersion %d. Machine contract: intent.json (this page is its reading).\n\n", in.Generator, in.SchemaVersion)
	if len(in.Sources.Schematic) > 0 {
		fmt.Fprintf(&b, "- schematic: %s\n", strings.Join(in.Sources.Schematic, ", "))
	}
	if in.Sources.Sim != "" {
		fmt.Fprintf(&b, "- sim: %s\n", in.Sources.Sim)
	}
	if in.Sources.Spec != "" {
		fmt.Fprintf(&b, "- spec: %s\n", in.Sources.Spec)
	}
	st := in.Standard
	fmt.Fprintf(&b, "- standard: %s, %s insulation, PD%d, MG %s, %.0f m, OVC %s, coated=%v", st.Name, st.Insulation, st.PollutionDegree, st.MaterialGroup, st.AltitudeM, st.OvervoltageCategory, st.Coated)
	if st.MOP != "" {
		fmt.Fprintf(&b, ", %d %s", st.MOPCount, st.MOP)
	}
	if len(st.Defaulted) > 0 {
		fmt.Fprintf(&b, " (defaulted: %s)", strings.Join(st.Defaulted, ", "))
	}
	b.WriteString("\n")
	if cu := in.Copper; cu != nil {
		fmt.Fprintf(&b, "- copper: %d layers, %.2g oz outer / %.2g oz inner, ΔT %.0f °C, %s (h=%.1f mil, εr=%.2f); fab %.1f/%.1f mil, via %.0f/%.0f mil\n",
			cu.Layers, cu.OuterOz, cu.InnerOz, cu.TempRiseC, cu.Stackup, cu.RefHeightMil, cu.Er, cu.MinTrackMil, cu.ClearanceMil, cu.ViaDrillMil, cu.ViaDiaMil)
	}
	if si := in.Simulation; si != nil {
		fmt.Fprintf(&b, "- simulation: %s, scenarios %s, converged=%v\n", si.Generator, strings.Join(si.Scenarios, ", "), si.Converged)
	}
	counts := map[string]int{}
	for _, f := range in.Findings {
		counts[f.Severity]++
	}
	fmt.Fprintf(&b, "- findings: %d error, %d warn, %d info\n\n", counts["error"], counts["warn"], counts["info"])

	b.WriteString("## 电路功能 / blocks\n\n| id | function | core | parts | summary |\n|---|---|---|---|---|\n")
	for _, bl := range in.Blocks {
		f := bl.Function
		if bl.SubFunction != "" {
			f += " (" + bl.SubFunction + ")"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", bl.ID, f, bl.Core, strings.Join(bl.Parts, " "), esc(bl.Summary))
	}
	for _, bl := range in.Blocks {
		if len(bl.Notes) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n**%s** notes:\n", bl.ID)
		for _, n := range bl.Notes {
			fmt.Fprintf(&b, "- %s\n", n)
		}
	}

	b.WriteString("\n## 电压域 / domains\n\n| id | kind | reference | Vrms | Vpeak | nets |\n|---|---|---|---:|---:|---|\n")
	for _, d := range in.Domains {
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s |\n", d.ID, d.Kind, d.Reference, trimFloat(d.WorkingVrms, 3), trimFloat(d.WorkingVpeak, 3), strings.Join(d.Nets, " "))
	}
	if len(in.Pairs) > 0 {
		b.WriteString("\n## 绝缘对 / pairs\n\n| a | b | insulation | Vrms | clearance mm | creepage mm | slot | ref |\n|---|---|---|---:|---:|---:|---|---|\n")
		for _, p := range in.Pairs {
			slot := "—"
			if p.SlotRequired {
				slot = fmt.Sprintf("%.1f mm", p.SlotWidthMm)
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %.2f | %.2f | %s | %s |\n", p.A, p.B, p.Insulation, trimFloat(p.WorkingVrms, 1), p.ClearanceMm, p.CreepageMm, slot, esc(p.StandardRef))
		}
	}

	b.WriteString("\n## 网络电气规划 / nets\n\n| net | role | class | block | V nom | V peak | I (A) | source | width o/i/min mil | vias | clr mil | Z Ω | pair |\n|---|---|---|---|---:|---:|---:|---|---|---:|---:|---:|---|\n")
	names := make([]string, 0, len(in.Nets))
	for n := range in.Nets {
		names = append(names, n)
	}
	sort.SliceStable(names, func(i, j int) bool {
		a, c := in.Nets[names[i]], in.Nets[names[j]]
		if rankRole(a.Role) != rankRole(c.Role) {
			return rankRole(a.Role) < rankRole(c.Role)
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		np := in.Nets[n]
		z := ""
		if np.ImpedanceOhm > 0 {
			z = trimFloat(np.ImpedanceOhm, 0)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s | %s | %s | %s/%s/%s | %d | %s | %s | %s |\n", n, np.Role, np.NetClass, np.Block,
			trimFloat(np.Voltage.Nom, 3), trimFloat(np.Voltage.Peak, 3), trimFloat(np.CurrentA, 4), np.CurrentSource,
			trimFloat(np.WidthMil.Outer, 2), trimFloat(np.WidthMil.Inner, 2), trimFloat(np.WidthMil.Min, 2), np.ViasPerTransition, trimFloat(np.ClearanceMil, 1), z, np.DiffPair)
	}
	b.WriteString("\n### why (power, switch, high-speed, HV)\n\n")
	for _, n := range names {
		np := in.Nets[n]
		if np.Role == "signal" && !strings.HasPrefix(np.NetClass, "HV_") {
			continue
		}
		fmt.Fprintf(&b, "- **%s**: %s\n", n, esc(strings.Join(np.Why, "; ")))
	}

	b.WriteString("\n## 网络类 / net classes\n\n| class | track mil | inner mil | min mil | clearance mil | via mil | Z Ω | nets |\n|---|---:|---:|---:|---:|---|---:|---|\n")
	for _, nc := range in.NetClasses {
		z := ""
		if nc.ImpedanceOhm > 0 {
			z = trimFloat(nc.ImpedanceOhm, 0)
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s | %s | %s/%s | %s | %s |\n", nc.Name, trimFloat(nc.TrackMil, 2), trimFloat(nc.InnerTrackMil, 2), trimFloat(nc.MinTrackMil, 2), trimFloat(nc.ClearanceMil, 1),
			trimFloat(nc.ViaDrillMil, 1), trimFloat(nc.ViaDiaMil, 1), z, strings.Join(nc.Nets, " "))
	}

	b.WriteString("\n## 设计提示 / findings\n\n")
	if len(in.Findings) == 0 {
		b.WriteString("none\n")
	}
	for _, f := range in.Findings {
		fmt.Fprintf(&b, "- **%s** `%s` %s", strings.ToUpper(f.Severity), f.Kind, f.Message)
		if f.Suggestion != "" {
			fmt.Fprintf(&b, " → %s", f.Suggestion)
		}
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func esc(s string) string { return strings.ReplaceAll(s, "|", "\\|") }

func rankRole(r string) int {
	for i, o := range []string{"power", "switch", "ground", "diff", "hs", "rf", "clock", "analog", "signal"} {
		if o == r {
			return i
		}
	}
	return 99
}
