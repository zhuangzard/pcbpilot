package app

import (
	"testing"
	"time"
)

func costRow(ts string, action string, ok bool, ms float64) auditRow {
	t0, _ := time.Parse(time.RFC3339, ts)
	return auditRow{Ts: t0, Action: action, OK: ok, DurationMs: ms}
}

func TestSummarizeAuditCost_SplitsMachineTimeFromThinkTime(t *testing.T) {
	// 墙钟 10 分钟、机器 2 分钟 → 其余 8 分钟是 agent 在想。这两个数必须分开,
	// 因为改法完全不同(优化调用 vs 优化流程)。
	rows := []auditRow{
		costRow("2026-08-15T14:00:00Z", "schematic.component.place", true, 60000),
		costRow("2026-08-15T14:10:00Z", "schematic.save", true, 60000),
	}
	rep := summarizeAuditCost(rows, map[string]bool{"schematic.component.place": true, "schematic.save": true})
	if got := rep.WallMinutes; got != 10 {
		t.Errorf("wall = %v, want 10", got)
	}
	if got := rep.DaemonMinutes; got != 2 {
		t.Errorf("daemon = %v, want 2", got)
	}
	if got := rep.ThinkMinutes; got != 8 {
		t.Errorf("think = %v, want 8", got)
	}
	if rep.Mutations != 2 {
		t.Errorf("mutations = %d, want 2", rep.Mutations)
	}
}

func TestSummarizeAuditCost_ProbeShare(t *testing.T) {
	// 上下文探测单独计:它们是每个 CLI 进程启动的开销,混进总数就看不见了
	// (真机首测:5466 次调用里 3527 次是它们)。
	rows := []auditRow{
		costRow("2026-08-15T14:00:00Z", "document.current", true, 1),
		costRow("2026-08-15T14:00:01Z", "schematic.pages.list", true, 1),
		costRow("2026-08-15T14:00:02Z", "pcb.documents.list", true, 1),
		costRow("2026-08-15T14:00:03Z", "schematic.component.place", true, 1000),
	}
	rep := summarizeAuditCost(rows, map[string]bool{"schematic.component.place": true})
	if rep.Probes != 3 {
		t.Fatalf("probes = %d, want 3", rep.Probes)
	}
	if got := rep.ProbeShare; got < 0.74 || got > 0.76 {
		t.Errorf("probeShare = %v, want ≈0.75", got)
	}
}

func TestSummarizeAuditCost_FailuresRankedByCount(t *testing.T) {
	// 失败榜要能一眼看出「哪条路可能根本没在工作」——真机首测抓到
	// titleblock.modify 17/18 全挂。
	rows := []auditRow{
		costRow("2026-08-15T14:00:00Z", "schematic.titleblock.modify", false, 5),
		costRow("2026-08-15T14:00:01Z", "schematic.titleblock.modify", false, 5),
		costRow("2026-08-15T14:00:02Z", "schematic.power.connect_pin", false, 5),
		costRow("2026-08-15T14:00:03Z", "schematic.power.connect_pin", true, 5),
		costRow("2026-08-15T14:00:04Z", "schematic.power.connect_pin", true, 5),
	}
	rep := summarizeAuditCost(rows, nil)
	if len(rep.TopFails) == 0 {
		t.Fatal("TopFails 为空")
	}
	if rep.TopFails[0].Action != "schematic.titleblock.modify" || rep.TopFails[0].Failures != 2 {
		t.Errorf("失败榜首 = %+v, want titleblock.modify ×2", rep.TopFails[0])
	}
	if rep.Failures != 3 {
		t.Errorf("failures = %d, want 3", rep.Failures)
	}
}

func TestSummarizeAuditCost_EmptyIsZeroNotPanic(t *testing.T) {
	rep := summarizeAuditCost(nil, nil)
	if rep.Calls != 0 || rep.WallMinutes != 0 {
		t.Errorf("空输入应全零,得到 %+v", rep)
	}
}

func TestTruncPageName_CutsOnRunes(t *testing.T) {
	// 按字节截断会把汉字劈成半个(cost ledger 首跑渲染出 `原理�…`)。
	got := truncPageName("esp32Mini 原理图端到端回归测试标签超长")
	for _, r := range got {
		if r == '�' {
			t.Fatalf("截断产生了坏字符: %q", got)
		}
	}
	if len([]rune(got)) > 18 {
		t.Errorf("截断后 %d runes, want ≤18", len([]rune(got)))
	}
}

// F10 (2026-09-25 E2E): the user typed local EDT times and got a UTC window.
func TestResolveAuditCostRange_LocalByDefault(t *testing.T) {
	edt := time.FixedZone("EDT", -4*3600)
	now := time.Date(2026, 9, 25, 13, 0, 0, 0, edt)
	from, to, err := resolveAuditCostRange("2026-09-25", "12:22", "12:57", edt, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := from.UTC().Format("15:04"); got != "16:22" {
		t.Errorf("from UTC = %s, want 16:22", got)
	}
	if got := to.UTC().Format("15:04"); got != "16:57" {
		t.Errorf("to UTC = %s, want 16:57", got)
	}
	// --utc keeps the old meaning.
	from, _, err = resolveAuditCostRange("2026-09-25", "12:22", "", time.UTC, now)
	if err != nil || from.UTC().Format("15:04") != "12:22" {
		t.Errorf("--utc from = %v (%v), want 12:22Z", from, err)
	}
}

func TestResolveAuditCostRange_ExplicitOffsetsAndDefaults(t *testing.T) {
	edt := time.FixedZone("EDT", -4*3600)
	now := time.Date(2026, 9, 25, 22, 30, 0, 0, edt) // already 2026-09-26 in UTC
	// Explicit offset / Z ignore the interpretation zone.
	from, to, err := resolveAuditCostRange("2026-09-25", "12:22-04:00", "16:57Z", time.UTC, now)
	if err != nil {
		t.Fatal(err)
	}
	if from.UTC().Format("15:04") != "16:22" || to.UTC().Format("15:04") != "16:57" {
		t.Errorf("offsets: %v → %v", from.UTC(), to.UTC())
	}
	from, _, err = resolveAuditCostRange("", "2026-09-25T12:22:00-04:00", "", edt, now)
	if err != nil || !from.Equal(time.Date(2026, 9, 25, 16, 22, 0, 0, time.UTC)) {
		t.Errorf("RFC3339 since = %v (%v)", from, err)
	}
	// Default day is "today" in the interpretation zone, not in UTC.
	from, to, err = resolveAuditCostRange("", "", "", edt, now)
	if err != nil {
		t.Fatal(err)
	}
	if from.In(edt).Format("2006-01-02 15:04") != "2026-09-25 00:00" || to.Sub(from) != 24*time.Hour {
		t.Errorf("default window = %v → %v", from, to)
	}
	if _, _, err := resolveAuditCostRange("2026-09-25", "13:00", "12:00", edt, now); err == nil {
		t.Error("until before since must be rejected")
	}
	if _, _, err := resolveAuditCostRange("2026-09-25", "25:99", "", edt, now); err == nil {
		t.Error("bad time must be rejected")
	}
}

func TestAuditDayFiles_LocalEveningSpansTwoUTCDays(t *testing.T) {
	edt := time.FixedZone("EDT", -4*3600)
	from := time.Date(2026, 9, 25, 19, 0, 0, 0, edt) // 23:00Z
	to := time.Date(2026, 9, 25, 21, 0, 0, 0, edt)   // 01:00Z next day
	days, err := auditDayFiles(from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 2 || days[0] != "2026-09-25" || days[1] != "2026-09-26" {
		t.Errorf("days = %v", days)
	}
	w := newAuditCostWindow(edt, from, to)
	if w.From != "2026-09-25T19:00:00-04:00" || w.FromUTC != "2026-09-25T23:00:00Z" {
		t.Errorf("window echo = %+v", w)
	}
}
