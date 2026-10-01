package app

// cmd_sch_aesthetics.go — `pcbpilot sch aesthetics`: schematic aesthetics,
// report-only (Phase A of docs/reviews/2026-10-schematic-aesthetics).
//
// The schematic counterpart of `pcb aesthetics`. Pure measurement over one
// page snapshot: never a gate, weight 0, never moves anything. sch layout-lint,
// sch check, sch layout-score and the connectivity checks stay exactly as they
// are; this is an added soft layer ranked below connectivity and readability.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

func newSchAestheticsCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var (
		snapshot  string
		zone      string
		asJSON    bool
		all       bool
		style     string
		styleFile string
	)
	c := &cobra.Command{
		Use:   "aesthetics",
		Short: "Measure schematic aesthetics: wiring, layout, labels, bus lanes (report-only, weight 0)",
		Long: "度量一页原理图的美观度（只报告：权重 0，不是门，不改任何几何）。\n\n" +
			"输入（--snapshot，离线，自动识别）：\n" +
			"  sch list --include-pins --include-bbox --include-wires 的输出（可另并入 texts /\n" +
			"  rectangles / designators / buses 键）；sch layout-plan 输出；sch lib-layout / compose\n" +
			"  源（modules）；1.4 canonical 快照（无导线/标记：布线与标签项 skipped，不算满分）；\n" +
			"  zones 包需 --zone ID（区内局部坐标）。\n" +
			"不给 --snapshot 时只读现场当前页（components.list + text.list + rectangles.list +\n" +
			"designators.list + bus.list；可选项读不到就降级并在 notes 里说明）。\n\n" +
			"布线  W1 转折/连接  W2 异网交叉  W3 四通结点/同网歧义 X  W4 共线重叠  W5 T 结点质量\n" +
			"      W6 绕行比（线长 / 曼哈顿 MST）  W7 穿本体/标记/文字  W8 落格（5 / 10 units）\n" +
			"版面  L1 信号流向（电源上、地下、IN 左、OUT 右、接口件在左右边带）  L2 标记朝向一致\n" +
			"      L3 行列对齐  L4 间距均匀  L5 模块框整洁  L6 文字重叠  L7 版面均衡\n" +
			"标签  N1 长线宜改标签  N2 短程滥用标签  N3 总线/虚拟总线（索引网、SPI/I2C/UART/SDIO、\n" +
			"      MIPI/USB 组的标签是否同列、等距、同向；有原生总线记满分）\n\n" +
			"风格档与 pcb aesthetics 同名：functional | balanced（默认）| precision | auto（按器件/\n" +
			"引脚/密度选档并打印理由）| custom（--style-file）。风格只改软目标；碰连接、NC、归属或\n" +
			"任一门禁的键直接拒绝。\n\n" +
			"优先级：连接正确性 > 可读性 > 美观。sch layout-lint / sch check / layout-score / DRC\n" +
			"保持原样且优先；本命令永远 exit 0（输入错误除外）。",
		Example: "  pcbpilot sch list --include-pins --include-bbox --include-wires --project P --doc <page> > page.json\n" +
			"  pcbpilot sch aesthetics --snapshot page.json\n" +
			"  pcbpilot sch aesthetics --snapshot layout.json --style precision --json\n" +
			"  pcbpilot sch aesthetics --project P --doc <page>          # live read-only",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			prof, err := resolveSchAesStyle(style, styleFile)
			if err != nil {
				return err
			}
			var snap *schaes.Snapshot
			if snapshot != "" {
				raw, err := os.ReadFile(snapshot)
				if err != nil {
					return err
				}
				if zone != "" {
					zs, err := schaes.ParseZones(raw)
					if err != nil {
						return err
					}
					for _, z := range zs {
						if z.ID == zone {
							snap = z.Snapshot
						}
					}
					if snap == nil {
						return fmt.Errorf("zone %q not in %s", zone, snapshot)
					}
				} else if snap, err = schaes.Parse(raw); err != nil {
					return err
				}
			} else {
				if snap, err = liveSchAesSnapshot(cfg, *window, stderr); err != nil {
					return err
				}
			}
			rep := schaes.Analyze(snap, prof)
			fmt.Fprintf(stderr, "schematic aesthetics profile %s (planned weight %.2f, applied 0)%s\n", rep.Profile.Name, rep.Profile.Weight, schAesAutoReason(rep.Profile))
			if asJSON {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			RenderSchAesthetics(stdout, rep, all)
			return nil
		},
	}
	c.Flags().StringVar(&snapshot, "snapshot", "", "offline page snapshot JSON (sch list / layout-plan / lib-layout / canonical); omit to read the live active page")
	c.Flags().StringVar(&zone, "zone", "", "with a zones packet: score this zone (zone-local coordinates)")
	c.Flags().BoolVar(&asJSON, "json", false, "print the full JSON report")
	c.Flags().BoolVar(&all, "all", false, "list every recorded offender, not just the top 3 per metric")
	c.Flags().StringVar(&style, "style", "", "functional | balanced (default) | precision | auto")
	c.Flags().StringVar(&styleFile, "style-file", "", "custom style JSON (bare object or {\"schematic\":{…}}): base, weight, groupWeights, metricWeights, gridUnits, targetGridUnits, gridBlend, alignTolUnits, nearMissTolUnits, longWireUnits, longWireCrossings, shortLabelSpanUnits; hard-tier keys are rejected")
	return c
}

func resolveSchAesStyle(style, styleFile string) (*schaes.Profile, error) {
	if style != "" && styleFile != "" {
		return nil, fmt.Errorf("use either --style or --style-file")
	}
	if styleFile != "" {
		raw, err := os.ReadFile(styleFile)
		if err != nil {
			return nil, err
		}
		p, err := schaes.ParseStyle(raw)
		if err != nil {
			return nil, err
		}
		return &p, nil
	}
	switch style {
	case "":
		return nil, nil
	case "auto":
		return &schaes.Profile{Name: "auto"}, nil
	case "custom":
		return nil, fmt.Errorf("--style custom needs --style-file")
	}
	p, err := schaes.ProfileByName(style)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func schAesAutoReason(p schaes.Profile) string {
	if p.Auto == nil {
		return ""
	}
	return " — auto: " + p.Auto.Reason
}

// liveSchAesSnapshot reads the active page read-only. components.list (bbox,
// pins, wires) is required; texts / frames / designators / buses are
// best-effort and a failed read is recorded as a note (the metric that needs
// it is then skipped or measured on predicted boxes, never silently perfect).
func liveSchAesSnapshot(cfg *appConfig, window string, stderr io.Writer) (*schaes.Snapshot, error) {
	res, err := requestAction(cfg, "schematic.components.list", window,
		map[string]any{"includeBBox": true, "includePins": true, "includeWires": true})
	if err != nil {
		return nil, err
	}
	merged := map[string]any{}
	for k, v := range res.Result {
		merged[k] = v
	}
	var notes []string
	opt := func(action, key, src string) {
		r, err := requestAction(cfg, action, window, map[string]any{})
		if err != nil {
			notes = append(notes, fmt.Sprintf("%s unavailable (%v): %s not measured", action, err, key))
			return
		}
		if v, ok := r.Result[src]; ok {
			merged[key] = v
		}
	}
	opt("schematic.text.list", "texts", "texts")
	opt("schematic.rectangles.list", "rectangles", "rectangles")
	opt("schematic.designators.list", "designators", "designators")
	opt("schematic.bus.list", "buses", "buses")
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, err
	}
	snap, err := schaes.Parse(raw)
	if err != nil {
		return nil, err
	}
	snap.Notes = append(snap.Notes, notes...)
	for _, n := range notes {
		fmt.Fprintln(stderr, "sch aesthetics: "+n)
	}
	return snap, nil
}

// RenderSchAesthetics prints the human report.
func RenderSchAesthetics(w io.Writer, rep *schaes.Report, all bool) {
	fmt.Fprintf(w, "schematic aesthetics %.1f [%s]  (wiring %s × wired share %.3f, layout %s, labels %s)  — report-only, weight %.2f, source %s\n",
		rep.Score, rep.Verdict, grp(rep, schaes.GroupWiring), rep.WiredShare, grp(rep, schaes.GroupLayout), grp(rep, schaes.GroupLabels), rep.Weight, rep.Source)
	p := rep.Profile
	fmt.Fprintf(w, "profile %s (planned weight %.2f, grid %g/%g blend %.1f, align %.1f near-miss %.0f, long wire %.0f / %d crossings, short label %.0f)%s\n\n",
		p.Name, p.Weight, p.GridUnits, p.TargetGrid, p.GridBlend, p.AlignTol, p.NearMissTol, p.LongWire, p.LongCrossings, p.ShortLabel, schAesAutoReason(p))
	fmt.Fprintf(w, "%-3s %-44s %10s %6s  %s\n", "id", "metric", "value", "score", "detail")
	for _, m := range rep.Metrics {
		if m.Skipped {
			fmt.Fprintf(w, "%-3s %-44s %10s %6s  skipped: %s\n", m.ID, trunc(m.Name, 44), "–", "–", m.Reason)
			continue
		}
		fmt.Fprintf(w, "%-3s %-44s %10.3f %6.1f  %s\n", m.ID, trunc(m.Name, 44), m.Value, m.Score, m.Detail)
		n := 3
		if all {
			n = len(m.Worst)
		}
		for i, o := range m.Worst {
			if i >= n {
				break
			}
			var who []string
			for _, s := range []string{o.Ref, o.Net} {
				if s != "" {
					who = append(who, s)
				}
			}
			at := ""
			if o.At != nil {
				at = fmt.Sprintf(" @(%.0f,%.0f)", o.At.X, o.At.Y)
			}
			fmt.Fprintf(w, "      · %s%s  %s\n", strings.Join(who, " "), at, o.Note)
		}
	}
	if len(rep.BusCandidates) > 0 {
		fmt.Fprintf(w, "\nbus candidates (N3):\n")
		for _, l := range rep.Lanes {
			fmt.Fprintf(w, "  %-8s %-18s %-40s %s\n", l.Candidate.Kind, trunc(l.Candidate.Suggested, 18), trunc(strings.Join(l.Candidate.Members, ","), 40), l.Note)
		}
	}
	fmt.Fprintf(w, "\n%d measured, %d skipped. %s\n", rep.Measured, rep.Skipped, rep.Boundary)
	for _, n := range rep.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}

func grp(rep *schaes.Report, g string) string {
	if v, ok := rep.Groups[g]; ok {
		return fmt.Sprintf("%.1f", v)
	}
	return "–"
}
