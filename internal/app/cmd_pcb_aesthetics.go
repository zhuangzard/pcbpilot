package app

// cmd_pcb_aesthetics.go — `pcbpilot pcb aesthetics`：离线度量布局 + 布线美观度。
//
// Phase A（docs/reviews/2026-09-routing-aesthetics/README.md §5A）：只报告。
// 分数不进 joint 总分（权重 0）、不进交付门槛、不驱动任何生成；电气规则永远优先，
// 差分 / RF / 等长 / 电源过孔阵列 / 隔离带与开槽在度量里硬编码豁免。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

func newPcbAestheticsCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		boardPath string
		asJSON    bool
		all       bool
		noExempt  bool
		style     string
		styleFile string
	)
	c := &cobra.Command{
		Use:   "aesthetics",
		Short: "Measure placement + routing aesthetics offline (report-only, weight 0)",
		Long: "离线度量一块板的布局与布线美观度（纯几何，不连编辑器）。\n\n" +
			"输入：`pcb dump [--include-copper]` 的 JSON，或 `pcb auto run` 写出的\n" +
			"board.routed.json（同一 schema）。没有 copper 段时只算布局项。\n\n" +
			"布局  P1 行列共线  P2 阵列等距  P3 朝向一致  P4 同构子电路对称（检测组 +\n" +
			"      镜像轴 + 镜像误差）  P5 模块矩形度  P6 边距一致  P7 留白均匀\n" +
			"      P8 位号丝印  P9 原点落格（5/25 mil）\n" +
			"布线  R1 非 8 向长度占比  R2 焊盘入线  R3 S 形小错位  R4 转折密度\n" +
			"      R5 层方向纪律  R6 平行等距 CV  R7 过孔/顶点落格  R8 单网最大绕行  R9 悬空 stub\n\n" +
			"风格档（--style / --style-file）：functional（密板，计划权重≈0.05、宽容差、零额外线长/面积）、\n" +
			"balanced（默认）、precision（25 mil 格、紧对齐、检出对称必须对称，可花 +5% 线长 / +3% 面积 /\n" +
			"+4 过孔换整齐）、custom（逐项权重与容差）、auto（按器件/网络数、引脚密度、层数、高速/高压、\n" +
			"RUDY 拥塞算复杂度指数自动选档并打印理由）。风格只改软目标，碰任何硬约束的键直接拒绝。\n\n" +
			"只报告：权重 0，不计入 joint 综合分、不是交付门槛。电气优先：差分、RF、等长、\n" +
			"电源过孔阵列、隔离带/开槽在度量前硬编码豁免（--no-exemptions 仅供诊断/对账）。",
		Example: "  pcbpilot pcb aesthetics --board board.routed.json\n" +
			"  pcbpilot pcb aesthetics --board dump.json --json\n" +
			"  pcbpilot pcb aesthetics --board dump.json --all   # 每项列出全部最差对象",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if boardPath == "" {
				return fmt.Errorf("--board is required (pcb dump JSON or board.routed.json)")
			}
			raw, err := os.ReadFile(boardPath)
			if err != nil {
				return err
			}
			in, err := pcbauto.AesInputFromSnapshot(raw)
			if err != nil {
				return err
			}
			in.NoExemptions = noExempt
			if in.Profile, err = resolveAesStyle(style, styleFile); err != nil {
				return err
			}
			rep := pcbauto.Aesthetics(in)
			fmt.Fprintf(stderr, "aesthetics profile %s (planned joint weight %.2f)%s\n", rep.Profile.Name, rep.Profile.Weight, autoReason(rep.Profile))
			if asJSON {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(rep)
			}
			renderAesthetics(stdout, rep, all)
			return nil
		},
	}
	c.Flags().StringVar(&boardPath, "board", "", "pcb dump JSON (with --include-copper for routing metrics) or pcb auto board.routed.json")
	c.Flags().BoolVar(&asJSON, "json", false, "print the full JSON report")
	c.Flags().BoolVar(&all, "all", false, "list every recorded offender, not just the top 3 per metric")
	c.Flags().BoolVar(&noExempt, "no-exemptions", false, "diagnostic: measure diff/RF/tuned nets, via arrays and isolation copper too")
	c.Flags().StringVar(&style, "style", "", "style profile: functional | balanced (default) | precision | auto (complexity index picks one)")
	c.Flags().StringVar(&styleFile, "style-file", "", "style JSON: the 'aesthetics' object of pcbpilot.project.json or the bare object (profile custom: base, weight, metricWeights, alignTolMil, nearMissTolMil, placementGridMil, gridBlend, symmetryRequired, slack, electricalTolerance ≤0.5 — the per-item electrical tolerance of the aesthetics stages, placementViaAllowance ≤ the preset's (balanced/precision 1, functional 0) — vias the placement stage may add when the electrical group does not drop); hard-constraint keys are rejected")
	return c
}

func renderAesthetics(w io.Writer, rep *pcbauto.AestheticsReport, all bool) {
	fmt.Fprintf(w, "aesthetics %.1f  (placement %.1f, routing %.1f × routed share %.3f)  — report-only, weight %.2f\n",
		rep.Score, rep.Placement, rep.Routing, rep.RoutedShare, rep.Weight)
	fmt.Fprintf(w, "profile %s (planned weight %.2f, align %.1f mil, grid %g mil, symmetry required %v, slack +%.0f%% wire / +%.0f%% area / +%d vias)%s\n\n",
		rep.Profile.Name, rep.Profile.Weight, rep.Profile.AlignTolMil, rep.Profile.PlacementGridMil, rep.Profile.SymmetryRequired,
		rep.Profile.Slack.WirelengthPct, rep.Profile.Slack.AreaPct, rep.Profile.Slack.ExtraVias, autoReason(rep.Profile))
	fmt.Fprintf(w, "%-4s %-38s %10s %6s  %s\n", "id", "metric", "value", "score", "detail")
	for _, m := range rep.Metrics {
		if m.Skipped {
			fmt.Fprintf(w, "%-4s %-38s %10s %6s  skipped: %s\n", m.ID, m.Name, "–", "–", m.Reason)
			continue
		}
		fmt.Fprintf(w, "%-4s %-38s %10.3f %6.1f  %s\n", m.ID, m.Name, m.Value, m.Score, m.Detail)
		n := 3
		if all {
			n = len(m.Worst)
		}
		for i, o := range m.Worst {
			if i >= n {
				break
			}
			var who []string
			for _, s := range []string{o.Ref, o.Net, o.ID} {
				if s != "" {
					who = append(who, s)
				}
			}
			fmt.Fprintf(w, "       · %s  %s\n", strings.Join(who, " "), o.Note)
		}
	}
	if len(rep.Symmetry) > 0 {
		fmt.Fprintf(w, "\nsymmetry groups (P4):\n")
		for _, g := range rep.Symmetry {
			var inst []string
			for _, x := range g.Instances {
				inst = append(inst, strings.Join(x, "+"))
			}
			fmt.Fprintf(w, "  %-10s %-28s %-9s err %.3f score %5.1f  %s\n", g.Kind, trunc(g.Signature, 28), g.Axis.Type, g.Error, g.Score, strings.Join(inst, " | "))
		}
	}
	for _, e := range rep.Exemptions {
		fmt.Fprintf(w, "\nexempt %s (%s): %d items — %s", e.Kind, e.Metrics, len(e.Items), e.Why)
	}
	if len(rep.Exemptions) > 0 {
		fmt.Fprintln(w)
	}
	for _, n := range rep.Notes {
		fmt.Fprintf(w, "note: %s\n", n)
	}
}

func trunc(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// resolveAesStyle turns --style / --style-file into a profile (nil = the
// default balanced profile; Name "auto" is resolved against the board).
func resolveAesStyle(style, styleFile string) (*pcbauto.AesProfile, error) {
	if style != "" && styleFile != "" {
		return nil, fmt.Errorf("use either --style or --style-file (a style file names its own profile)")
	}
	if styleFile != "" {
		raw, err := os.ReadFile(styleFile)
		if err != nil {
			return nil, err
		}
		p, err := pcbauto.ParseAesStyle(raw)
		if err != nil {
			return nil, err
		}
		return &p, nil
	}
	switch style {
	case "":
		return nil, nil
	case "auto":
		return &pcbauto.AesProfile{Name: "auto"}, nil
	case "custom":
		return nil, fmt.Errorf("--style custom needs --style-file with the custom weights/tolerances")
	}
	p, err := pcbauto.AesProfileByName(style)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func autoReason(p pcbauto.AesProfile) string {
	if p.Auto == nil {
		return ""
	}
	return " — auto: " + p.Auto.Reason
}
