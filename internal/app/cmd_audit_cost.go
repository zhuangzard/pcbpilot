package app

// cmd_audit_cost.go — `pcbpilot audit cost`:一次设计跑了多久、花了多少次调用、
// 其中多少是白花的。用户立项(2026-08-16):「耗时和 token 以后都要记录,用以改善」。
//
// 为什么值得单独做一条命令:一整场跑的形状,在单条命令的视角下完全看不见。
//
// **报告首版按次数排序,当场把作者引到了错误的靶子上** —— 它把「探测占 65% 的调用」
// 顶到榜首,读起来像一笔巨大的浪费;按耗时一算,那 3527 次只花 22 秒(机器时间的
// 1.4%),优化掉毫无意义。真正吃掉 86% 机器时间的是 components.list(41%)、
// connect_pin(34%)、document.open(11%,单次 4.24s 最贵),它们在次数榜上并不显眼。
// 所以动作榜**按耗时排**,探测那一行的次数占比旁边永远站着时间占比:次数多 ≠ 贵。
// 次数真正的诊断价值是别的 —— 它反映**跑了多少条 CLI 命令**(每条固定 2~3 发探测,
// 实测 `doc ls` 3 发、`sch clusters` 2 发),而命令条数 = agent 的决策轮数。
//
// 三个指标是分开的,因为**改法不同**:
//   - 墙钟          —— 用户实际等了多久;
//   - daemon 侧耗时  —— 机器真在算的时间(优化调用次数/批量化);
//   - 两者之差      —— agent 在想/在改代码(优化流程与判据,不是优化 API)。
//
// token 不在审计日志里(那是 agent 侧的账),所以由调用方 `--tokens` 自报,和上面
// 三个机器指标一起落进台账。缺了就记 0,**绝不估算冒充实测**。

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/protocol"
)

// auditProbeActions 是上下文探测 —— CLI 每次启动 resolve 窗口/工程时打的那几发。
// 它们既不读设计数据也不改画布。单独计一栏**不是因为它贵**(实测只占机器时间的
// 1.4%),而是因为它是「跑了多少条 CLI 命令」的可靠代理:每条命令固定 2~3 发。
var auditProbeActions = map[string]bool{
	"document.current":         true,
	"document.list":            true,
	"documents.list":           true,
	"schematic.pages.list":     true,
	"pcb.documents.list":       true,
	"schematic.documents.list": true,
}

// auditActionStat 是一个动作的聚合。
type auditActionStat struct {
	Action   string  `json:"action"`
	Calls    int     `json:"calls"`
	Failures int     `json:"failures"`
	Seconds  float64 `json:"seconds"`
}

// auditCostReport 是一场跑的成本画像。
type auditCostReport struct {
	Label   string `json:"label,omitempty"`
	Day     string `json:"day"`
	From    string `json:"from"`
	To      string `json:"to"`
	Project string `json:"project,omitempty"`
	// Window 回显**解析后的查询区间**(本地与 UTC 两种写法)。Day/From/To 仍是
	// 命中记录的 UTC 首末时刻(台账历史口径不变)。
	Window *auditCostWindow `json:"window,omitempty"`

	WallMinutes   float64 `json:"wallMinutes"`
	DaemonMinutes float64 `json:"daemonMinutes"`
	// ThinkMinutes 是墙钟减去 daemon 侧 —— agent 思考 + 编译 + 人工介入的时间。
	ThinkMinutes float64 `json:"thinkMinutes"`

	Calls    int     `json:"calls"`
	Failures int     `json:"failures"`
	FailRate float64 `json:"failRate"`
	// Probes / ProbeShare 是上下文探测的次数与占比(见 auditProbeActions)。
	Probes     int     `json:"probes"`
	ProbeShare float64 `json:"probeShare"`
	// ProbeSeconds 是探测的**耗时** —— 与次数占比并列显示。次数多≠贵。
	ProbeSeconds float64 `json:"probeSeconds"`
	// Mutations 是写动作次数 —— 「产出」的粗略度量,用来跟成本对比。
	Mutations int `json:"mutations"`

	// Tokens 由调用方自报(审计日志里没有);0 = 未记录,不是「零消耗」。
	Tokens int `json:"tokens"`

	Top      []auditActionStat `json:"topActions"`
	TopFails []auditActionStat `json:"topFailures"`
	Note     string            `json:"note,omitempty"`
}

// auditCostWindow 是解析后的查询区间。F10(2026-09-25 E2E):旧版把 --day/--since/
// --until 一律当 UTC,用户按本地 EDT 填 12:22–12:57 取到了别场数据并记进台账。
// 现在默认按本机时区解释,--utc 恢复旧口径,且区间总是双写回显,错了一眼能看出。
type auditCostWindow struct {
	Zone    string `json:"zone"` // 解释 HH:MM 所用的时区(Local 名或 UTC)
	From    string `json:"from"` // RFC3339,带解释时区的偏移
	To      string `json:"to"`
	FromUTC string `json:"fromUtc"`
	ToUTC   string `json:"toUtc"`
}

func newAuditCostWindow(loc *time.Location, from, to time.Time) *auditCostWindow {
	return &auditCostWindow{
		Zone:    loc.String(),
		From:    from.In(loc).Format(time.RFC3339),
		To:      to.In(loc).Format(time.RFC3339),
		FromUTC: from.UTC().Format(time.RFC3339),
		ToUTC:   to.UTC().Format(time.RFC3339),
	}
}

// resolveAuditCostRange 把 --day/--since/--until 解析成绝对区间。
//
//   - HH:MM / HH:MM:SS 按 loc(默认本机时区,--utc 时为 UTC)落在 --day 那天;
//   - 可带显式偏移:HH:MM-04:00 / HH:MMZ,或完整 RFC3339(此时忽略 loc);
//   - 缺省 --day = loc 下的今天;缺省 since/until = 该日 00:00 与次日 00:00。
func resolveAuditCostRange(day, since, until string, loc *time.Location, now time.Time) (time.Time, time.Time, error) {
	if loc == nil {
		loc = time.Local
	}
	if strings.TrimSpace(day) == "" {
		day = now.In(loc).Format("2006-01-02")
	}
	d, err := time.ParseInLocation("2006-01-02", day, loc)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("invalid --day %q (want YYYY-MM-DD)", day)
	}
	parse := func(v string, def time.Time) (time.Time, error) {
		v = strings.TrimSpace(v)
		if v == "" {
			return def, nil
		}
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t, nil
		}
		for _, layout := range []string{"15:04:05Z07:00", "15:04Z07:00"} {
			if t, err := time.Parse(layout, v); err == nil {
				return time.Date(d.Year(), d.Month(), d.Day(), t.Hour(), t.Minute(), t.Second(), 0, t.Location()), nil
			}
		}
		for _, layout := range []string{"15:04:05", "15:04"} {
			if t, err := time.Parse(layout, v); err == nil {
				return time.Date(d.Year(), d.Month(), d.Day(), t.Hour(), t.Minute(), t.Second(), 0, loc), nil
			}
		}
		return time.Time{}, fmt.Errorf("invalid time %q (want HH:MM[:SS], HH:MM[:SS]±hh:mm or RFC3339)", v)
	}
	from, err := parse(since, d)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	to, err := parse(until, d.AddDate(0, 0, 1))
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, fmt.Errorf("--until %s is before --since %s", to.Format(time.RFC3339), from.Format(time.RFC3339))
	}
	return from, to, nil
}

// auditDayFiles 列出区间覆盖的每一个 UTC 日志文件名(daemon 按 UTC 日切文件,
// 本地区间可能跨两个 UTC 日)。上限 31 天,防止误写的 RFC3339 扫全盘。
func auditDayFiles(from, to time.Time) ([]string, error) {
	start := time.Date(from.UTC().Year(), from.UTC().Month(), from.UTC().Day(), 0, 0, 0, 0, time.UTC)
	var out []string
	for d := start; !d.After(to.UTC()); d = d.AddDate(0, 0, 1) {
		if len(out) >= 31 {
			return nil, fmt.Errorf("区间跨度超过 31 天(%s → %s)", from.Format(time.RFC3339), to.Format(time.RFC3339))
		}
		out = append(out, d.Format("2006-01-02"))
	}
	return out, nil
}

// summarizeAuditCost 是纯核:把一段区间内的审计行折成成本画像。无 I/O,可单测。
func summarizeAuditCost(rows []auditRow, mutating map[string]bool) auditCostReport {
	rep := auditCostReport{}
	if len(rows) == 0 {
		return rep
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Ts.Before(rows[j].Ts) })
	first, last := rows[0].Ts, rows[len(rows)-1].Ts
	rep.Day = first.UTC().Format("2006-01-02")
	rep.From = first.UTC().Format("15:04:05")
	rep.To = last.UTC().Format("15:04:05")
	rep.WallMinutes = last.Sub(first).Minutes()

	type agg struct {
		calls, fails int
		ms           float64
	}
	byAction := map[string]*agg{}
	var totalMs float64
	for _, r := range rows {
		rep.Calls++
		if !r.OK {
			rep.Failures++
		}
		if auditProbeActions[r.Action] {
			rep.Probes++
		}
		if mutating[r.Action] {
			rep.Mutations++
		}
		totalMs += r.DurationMs
		a := byAction[r.Action]
		if a == nil {
			a = &agg{}
			byAction[r.Action] = a
		}
		a.calls++
		a.ms += r.DurationMs
		if !r.OK {
			a.fails++
		}
	}
	rep.DaemonMinutes = totalMs / 1000 / 60
	rep.ThinkMinutes = rep.WallMinutes - rep.DaemonMinutes
	if rep.ThinkMinutes < 0 {
		rep.ThinkMinutes = 0 // 并发调用可能让 daemon 侧总和超过墙钟
	}
	if rep.Calls > 0 {
		rep.FailRate = float64(rep.Failures) / float64(rep.Calls)
		rep.ProbeShare = float64(rep.Probes) / float64(rep.Calls)
	}

	stats := make([]auditActionStat, 0, len(byAction))
	for name, a := range byAction {
		stats = append(stats, auditActionStat{Action: name, Calls: a.calls, Failures: a.fails, Seconds: a.ms / 1000})
	}
	// **按耗时排,不按次数** —— 首版按次数排,把我自己引到了错误的靶子上:
	// 探测占 65% 的次数却只占 1.4% 的时间(22 秒),而真正吃掉 86% 机器时间的
	// components.list / connect_pin / document.open 在次数榜上排在后面。
	// 要优化的是「时间去哪了」,次数只是它的一个弱代理。
	sort.Slice(stats, func(i, j int) bool {
		if stats[i].Seconds != stats[j].Seconds {
			return stats[i].Seconds > stats[j].Seconds
		}
		if stats[i].Calls != stats[j].Calls {
			return stats[i].Calls > stats[j].Calls
		}
		return stats[i].Action < stats[j].Action
	})
	rep.Top = stats[:minInt(len(stats), 12)]
	// ProbeSeconds 让「次数占比」旁边永远站着「时间占比」,免得再有人(比如我)
	// 拿次数当浪费的证据。
	for _, s := range stats {
		if auditProbeActions[s.Action] {
			rep.ProbeSeconds += s.Seconds
		}
	}

	fails := make([]auditActionStat, 0, len(stats))
	for _, s := range stats {
		if s.Failures > 0 {
			fails = append(fails, s)
		}
	}
	sort.Slice(fails, func(i, j int) bool { return fails[i].Failures > fails[j].Failures })
	rep.TopFails = fails[:minInt(len(fails), 8)]
	return rep
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// auditLedgerPath 是累积台账:每次 --record 追加一行,用来跨批次对比「有没有变好」。
func auditLedgerPath() string {
	return filepath.Join(filepath.Dir(defaultAuditDir()), "cost-ledger.jsonl")
}

func appendCostLedger(rep auditCostReport) error {
	path := auditLedgerPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	line := map[string]any{
		"recordedAt": time.Now().UTC().Format(time.RFC3339),
		"label":      rep.Label, "day": rep.Day, "from": rep.From, "to": rep.To,
		"window":        rep.Window,
		"project":       rep.Project,
		"wallMinutes":   costRound1(rep.WallMinutes),
		"daemonMinutes": costRound1(rep.DaemonMinutes),
		"thinkMinutes":  costRound1(rep.ThinkMinutes),
		"calls":         rep.Calls, "failures": rep.Failures,
		"failRate": costRound3(rep.FailRate), "probes": rep.Probes, "probeShare": costRound3(rep.ProbeShare),
		"mutations": rep.Mutations, "tokens": rep.Tokens, "note": rep.Note,
	}
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

func costRound1(f float64) float64 { return float64(int(f*10+0.5)) / 10 }
func costRound3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

// mutatingActionSet 从 **动作目录本身**取 Mutates 标志 —— 不在这里另抄一份写动作
// 清单。目录已经是 daemon 防抖 autosave 的判据来源,再抄一份就会有两套「什么算写」。
func mutatingActionSet() map[string]bool {
	out := map[string]bool{}
	for _, a := range protocol.AllActions() {
		if a.Mutates {
			out[a.Name] = true
		}
	}
	return out
}

// readCostLedger 读回台账(缺文件 = 空,不是错)。
func readCostLedger() ([]map[string]any, error) {
	b, err := os.ReadFile(auditLedgerPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []map[string]any
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(line), &m) == nil {
			out = append(out, m)
		}
	}
	return out, nil
}

func renderCostReport(rep auditCostReport, stdout io.Writer) {
	title := rep.Label
	if title == "" {
		title = "(未命名)"
	}
	fmt.Fprintf(stdout, "audit cost — %s · %s %s→%s UTC\n", title, rep.Day, rep.From, rep.To)
	if w := rep.Window; w != nil {
		fmt.Fprintf(stdout, "  查询区间 %s → %s(%s)= %s → %s UTC\n", w.From, w.To, w.Zone, w.FromUTC, w.ToUTC)
	}
	fmt.Fprintln(stdout)
	fmt.Fprintf(stdout, "  墙钟            %.1f 分钟\n", rep.WallMinutes)
	fmt.Fprintf(stdout, "  ├ daemon 侧     %.1f 分钟(%.0f%%)—— 机器真在算\n",
		rep.DaemonMinutes, pct(rep.DaemonMinutes, rep.WallMinutes))
	fmt.Fprintf(stdout, "  └ 其余          %.1f 分钟(%.0f%%)—— agent 思考/编译/人工介入\n\n",
		rep.ThinkMinutes, pct(rep.ThinkMinutes, rep.WallMinutes))
	fmt.Fprintf(stdout, "  调用            %d 次,失败 %d(%.1f%%)\n", rep.Calls, rep.Failures, rep.FailRate*100)
	fmt.Fprintf(stdout, "  ├ 上下文探测     %d 次(%.0f%%)但只花 %.0fs(机器时间的 %.1f%%)—— 次数多≠贵,\n",
		rep.Probes, rep.ProbeShare*100, rep.ProbeSeconds, pct(rep.ProbeSeconds/60, rep.DaemonMinutes))
	fmt.Fprintf(stdout, "  │                它真正的诊断价值是反映**跑了多少条 CLI 命令**(每条固定 2~3 发)\n")
	fmt.Fprintf(stdout, "  └ 写动作        %d 次 —— 产出\n", rep.Mutations)
	if rep.Tokens > 0 {
		fmt.Fprintf(stdout, "  token           %d(自报)\n", rep.Tokens)
	} else {
		fmt.Fprintf(stdout, "  token           未记录 —— 审计日志里没有,用 --tokens N 自报\n")
	}
	fmt.Fprintf(stdout, "\n  动作 top(**按耗时**排 —— 时间去哪了才是靶子,次数只是弱代理):\n")
	for _, s := range rep.Top {
		mark := ""
		if auditProbeActions[s.Action] {
			mark = "  ← 探测"
		}
		per := 0.0
		if s.Calls > 0 {
			per = s.Seconds / float64(s.Calls)
		}
		fmt.Fprintf(stdout, "    %-34s %7.1fs(%4.1f%%)  %5d 次  均 %.2fs  失败 %3d%s\n",
			s.Action, s.Seconds, pct(s.Seconds/60, rep.DaemonMinutes), s.Calls, per, s.Failures, mark)
	}
	if len(rep.TopFails) > 0 {
		fmt.Fprintf(stdout, "\n  失败 top(失败率高 = 那条路可能根本没在工作):\n")
		for _, s := range rep.TopFails {
			fmt.Fprintf(stdout, "    %-34s %3d/%d(%.0f%%)\n",
				s.Action, s.Failures, s.Calls, float64(s.Failures)/float64(s.Calls)*100)
		}
	}
}

func pct(part, whole float64) float64 {
	if whole <= 0 {
		return 0
	}
	return part / whole * 100
}

// newAuditCostCmd 注册 `audit cost`。
func newAuditCostCmd(stdout, stderr io.Writer) *cobra.Command {
	var (
		dir, day, since, until string
		label, project, note   string
		tokens                 int
		asJSON, record, ledger bool
		useUTC                 bool
	)
	c := &cobra.Command{
		Use:   "cost",
		Short: "一场设计跑了多久、多少次调用、多少是白花的(可 --record 落台账)",
		Long: `把一段时间的审计日志聚合成**成本画像**,并可追加进累积台账做跨批次对比。

三个耗时指标是分开的,因为改法不同:
  • 墙钟        —— 用户实际等了多久
  • daemon 侧   —— 机器真在算(优化调用次数 / 批量化)
  • 两者之差    —— agent 思考 / 编译 / 人工介入(优化流程与判据,不是优化 API)

**上下文探测**单独计一栏(document.current / pages.list / documents.list):它们不读
设计数据也不改画布,只是每个 CLI 进程启动都要重新 resolve 一遍窗口和工程。首测
esp32Mini 原理图 E2E:5466 次调用里 3527 次(65%)是它们 —— 这种浪费在单条命令的
视角下完全看不见。

token 不在审计日志里(那是 agent 侧的账),用 --tokens 自报;不给就记「未记录」,
**不估算冒充实测**。`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot audit cost --day 2026-08-15 --since 14:12 --until 15:50          # 本机时区
  pcbpilot audit cost --day 2026-08-15 --since 18:12 --until 19:50 --utc    # 旧 UTC 口径
  pcbpilot audit cost --since 2026-09-25T12:22:00-04:00 --until 2026-09-25T12:57:00-04:00
  pcbpilot audit cost --since 14:12 --until 15:50 --label "esp32Mini 原理图 E2E" --tokens 1200000 --record
  pcbpilot audit cost --ledger`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if ledger {
				entries, err := readCostLedger()
				if err != nil {
					return err
				}
				if len(entries) == 0 {
					fmt.Fprintf(stdout, "台账还是空的(%s)—— 跑完一场用 `audit cost … --record` 记第一笔\n", auditLedgerPath())
					return nil
				}
				if asJSON {
					enc := json.NewEncoder(stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(entries)
				}
				fmt.Fprintf(stdout, "cost ledger — %d 笔(%s)\n\n", len(entries), auditLedgerPath())
				fmt.Fprintf(stdout, "  %-28s %-11s %7s %7s %7s %7s %8s %8s\n",
					"label", "day", "墙钟m", "机器m", "调用", "探测%", "失败%", "token")
				for _, e := range entries {
					fmt.Fprintf(stdout, "  %-28s %-11s %7.1f %7.1f %7.0f %6.0f%% %7.1f%% %8.0f\n",
						truncPageName(asString(e["label"])), asString(e["day"]),
						asFloat(e["wallMinutes"]), asFloat(e["daemonMinutes"]), asFloat(e["calls"]),
						asFloat(e["probeShare"])*100, asFloat(e["failRate"])*100, asFloat(e["tokens"]))
				}
				return nil
			}

			if dir == "" {
				dir = defaultAuditDir()
			}
			loc := time.Local
			if useUTC {
				loc = time.UTC
			}
			fromTs, toTs, err := resolveAuditCostRange(day, since, until, loc, time.Now())
			if err != nil {
				return err
			}
			window := newAuditCostWindow(loc, fromTs, toTs)
			fmt.Fprintf(stderr, "区间: %s → %s(%s)= %s → %s UTC\n", window.From, window.To, window.Zone, window.FromUTC, window.ToUTC)
			days, err := auditDayFiles(fromTs, toTs)
			if err != nil {
				return err
			}
			var rows []auditRow
			found := 0
			for _, d := range days {
				path := filepath.Join(dir, d+".jsonl")
				if _, serr := os.Stat(path); serr != nil {
					continue
				}
				part, rerr := readAuditRows(path)
				if rerr != nil {
					return rerr
				}
				found++
				rows = append(rows, part...)
			}
			if found == 0 {
				return fmt.Errorf("没有审计日志:%s 下缺 %s.jsonl(按 UTC 日切文件)", dir, strings.Join(days, ".jsonl / "))
			}
			var in []auditRow
			for _, r := range rows {
				if r.Ts.Before(fromTs) || r.Ts.After(toTs) {
					continue
				}
				in = append(in, r)
			}
			if len(in) == 0 {
				return fmt.Errorf("%s → %s(%s)区间内没有审计记录", window.From, window.To, window.Zone)
			}
			rep := summarizeAuditCost(in, mutatingActionSet())
			rep.Window = window
			rep.Label, rep.Project, rep.Tokens, rep.Note = label, project, tokens, note

			if asJSON {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(rep); err != nil {
					return err
				}
			} else {
				renderCostReport(rep, stdout)
			}
			if record {
				if err := appendCostLedger(rep); err != nil {
					return fmt.Errorf("写台账失败: %w", err)
				}
				fmt.Fprintf(stderr, "✓ 已记入台账 %s\n", auditLedgerPath())
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "audit log directory (default ~/.pcbpilot/audit)")
	c.Flags().StringVar(&day, "day", "", "day, YYYY-MM-DD (default today in the local time zone; UTC with --utc)")
	c.Flags().StringVar(&since, "since", "", "start: HH:MM[:SS] in local time (UTC with --utc), HH:MM[:SS]±hh:mm, or RFC3339")
	c.Flags().StringVar(&until, "until", "", "end: HH:MM[:SS] in local time (UTC with --utc), HH:MM[:SS]±hh:mm, or RFC3339")
	c.Flags().BoolVar(&useUTC, "utc", false, "interpret --day/--since/--until as UTC (pre-2026-09-25 behaviour)")
	c.Flags().StringVar(&label, "label", "", "这一场叫什么(台账里的名字)")
	c.Flags().StringVar(&project, "project-name", "", "工程名(仅记录用)")
	c.Flags().StringVar(&note, "note", "", "备注(踩了什么坑、跑到哪一步)")
	c.Flags().IntVar(&tokens, "tokens", 0, "agent 侧 token 消耗(自报;审计日志里没有)")
	c.Flags().BoolVar(&asJSON, "json", false, "以 JSON 输出")
	c.Flags().BoolVar(&record, "record", false, "把这一场追加进累积台账")
	c.Flags().BoolVar(&ledger, "ledger", false, "只列台账历史(不分析日志)")
	return c
}
