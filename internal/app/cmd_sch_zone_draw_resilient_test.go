package app

// Tests for the resilient zone-draw path (REPORT esp32mini-round2 新 3).
// All offline: exec/survey/sleep are injected fakes.

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

func resilientTarget() zonePartitionTarget {
	return zonePartitionTarget{
		Title:    "D_ESD / U",
		TX:       104, // MinX 100 + 4
		TY:       478, // MaxY 500 - fontSize 22
		Rect:     layoutBBox{MinX: 100, MinY: 200, MaxX: 400, MaxY: 500},
		FontSize: 22,
	}
}

// ── JS generation: frame + title merged into ONE self-cleaning exec_js ────

func TestBuildPartitionZoneDrawJSSingleScriptWithSelfCleanup(t *testing.T) {
	js := buildPartitionZoneDrawJS(resilientTarget(), []byte(`"#AA00AA"`))
	if js == "" {
		t.Fatal("expected a script for a non-degenerate bbox")
	}
	// 框线与区名在同一段 JS 里 —— 单次 exec,消除「框成名败」的两次写中间态。
	if got := strings.Count(js, "sch_PrimitiveRectangle.create("); got != 1 {
		t.Fatalf("rectangle creates = %d, want exactly 1", got)
	}
	if got := strings.Count(js, "sch_PrimitiveText.create("); got != 1 {
		t.Fatalf("text creates = %d, want exactly 1", got)
	}
	// 失败时 JS 内自清理:prelude 定义 cleanupCreated,epilogue 的 catch 调用它。
	for _, want := range []string{"cleanupCreated", "catch (err)", "rects.push(rid)", "texts.push(tid)"} {
		if !strings.Contains(js, want) {
			t.Fatalf("generated JS is missing %q:\n%s", want, js)
		}
	}
	// 标题锚点与 buildPartitionDrawJS 同一几何(MinX+4 / MaxY-fontSize)。
	if !strings.Contains(js, "eda.sch_PrimitiveText.create(104, 478,") {
		t.Fatalf("title anchor drifted from the legacy geometry:\n%s", js)
	}
	// 标题内容经 JSON 转义进 JS 字面量。
	if !strings.Contains(js, `"D_ESD / U"`) {
		t.Fatalf("title literal missing:\n%s", js)
	}
}

func TestBuildPartitionZoneDrawJSDegenerateBBox(t *testing.T) {
	tg := resilientTarget()
	tg.Rect = layoutBBox{MinX: 10, MinY: 10, MaxX: 10, MaxY: 40} // zero width
	if js := buildPartitionZoneDrawJS(tg, []byte(`"#AA00AA"`)); js != "" {
		t.Fatalf("degenerate bbox must produce no script, got:\n%s", js)
	}
}

func TestPartitionTargetsMatchLegacyAnchors(t *testing.T) {
	plan := partitionPlan{Partitions: []partitionRect{{
		Modules:   []string{"WROOM", "LED"},
		BBox:      layoutBBox{MinX: 50, MinY: 60, MaxX: 350, MaxY: 460},
		TitleBBox: layoutBBox{MinX: 50, MinY: 430, MaxX: 350, MaxY: 460},
	}}}
	got := partitionTargets(plan, 22)
	if len(got) != 1 {
		t.Fatalf("targets = %d, want 1", len(got))
	}
	if got[0].Title != "WROOM / LED" {
		t.Fatalf("title = %q", got[0].Title)
	}
	if got[0].TX != 54 || got[0].TY != 438 {
		t.Fatalf("anchor = (%g,%g), want (54,438) — must stay identical to buildPartitionDrawJS", got[0].TX, got[0].TY)
	}
}

// ── fake exec/survey harness ──────────────────────────────────────────────

type fakeZoneExec struct {
	results []func() (map[string]any, error)
	calls   int
	phases  []string
}

func (f *fakeZoneExec) exec(phase, code string) (map[string]any, error) {
	f.phases = append(f.phases, phase)
	if f.calls >= len(f.results) {
		return nil, fmt.Errorf("unexpected exec call #%d (phase %q)", f.calls+1, phase)
	}
	r := f.results[f.calls]
	f.calls++
	return r()
}

func okDraw(rectID, textID string) func() (map[string]any, error) {
	return func() (map[string]any, error) {
		return map[string]any{"ok": true, "rects": []any{rectID}, "texts": []any{textID}}, nil
	}
}

func failDrawClean(msg string) func() (map[string]any, error) {
	return func() (map[string]any, error) {
		return map[string]any{"ok": false, "error": msg, "rects": []any{}, "texts": []any{},
			"cleanupSurvived": []any{}, "cleanupErrors": []any{}}, nil
	}
}

func transportFail() func() (map[string]any, error) {
	return func() (map[string]any, error) { return nil, errors.New("connector did not respond") }
}

type fakeSurvey struct {
	results []func() (zoneFrameSurvey, error)
	calls   int
}

func (f *fakeSurvey) survey() (zoneFrameSurvey, error) {
	if f.calls >= len(f.results) {
		return zoneFrameSurvey{}, fmt.Errorf("unexpected survey call #%d", f.calls+1)
	}
	r := f.results[f.calls]
	f.calls++
	return r()
}

func surveyWith(rects []string, texts ...zoneSurveyText) func() (zoneFrameSurvey, error) {
	return func() (zoneFrameSurvey, error) {
		s := zoneFrameSurvey{Rects: map[string]bool{}, Texts: texts}
		for _, id := range rects {
			s.Rects[id] = true
		}
		return s, nil
	}
}

func surveyFail() func() (zoneFrameSurvey, error) {
	return func() (zoneFrameSurvey, error) { return zoneFrameSurvey{}, errors.New("read failed too") }
}

func emptyKnown() zoneKnownIDs {
	return zoneKnownIDs{Rects: map[string]bool{}, Texts: map[string]bool{}}
}

func plannedTitleText(tg zonePartitionTarget, id string) zoneSurveyText {
	return zoneSurveyText{ID: id, Content: tg.Title, X: tg.TX, Y: tg.TY}
}

// plannedZoneGeom is the frame the plan asks for, as the page would report it.
func plannedZoneGeom(tg zonePartitionTarget, id string) map[string]layoutBBox {
	return map[string]layoutBBox{id: tg.Rect}
}

// ── 假失败定律:报失败但已落地 → 收编 id,绝不重发 ─────────────────────────

func TestDrawOneZoneFakeFailureLandedIsAdoptedNotResent(t *testing.T) {
	tg := resilientTarget()
	ex := &fakeZoneExec{results: []func() (map[string]any, error){transportFail()}}
	sv := &fakeSurvey{results: []func() (zoneFrameSurvey, error){
		surveyWith([]string{"r_new"}, plannedTitleText(tg, "t_new")),
	}}
	slept := 0
	out := drawOneZoneResilient(zoneDrawDeps{exec: ex.exec, survey: sv.survey, sleep: func() { slept++ }},
		tg, buildPartitionZoneDrawJS(tg, []byte(`"#AA00AA"`)), emptyKnown())
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if !out.Adopted || out.RectID != "r_new" || out.TextID != "t_new" {
		t.Fatalf("outcome = %+v, want adopted r_new/t_new", out)
	}
	if ex.calls != 1 {
		t.Fatalf("exec called %d times, want 1 — a landed write must NOT be resent", ex.calls)
	}
	if slept != 0 {
		t.Fatalf("slept %d times, want 0 (no retry happened)", slept)
	}
}

// ── 真失败(复核确认没落地)→ settle 后重发一次 ──────────────────────────

func TestDrawOneZoneTrueTransportFailureRetriesOnce(t *testing.T) {
	tg := resilientTarget()
	ex := &fakeZoneExec{results: []func() (map[string]any, error){
		transportFail(),
		okDraw("r1", "t1"),
	}}
	sv := &fakeSurvey{results: []func() (zoneFrameSurvey, error){
		surveyWith(nil), // 复核:什么都没落地 → 重发安全
	}}
	slept := 0
	out := drawOneZoneResilient(zoneDrawDeps{exec: ex.exec, survey: sv.survey, sleep: func() { slept++ }},
		tg, buildPartitionZoneDrawJS(tg, []byte(`"#AA00AA"`)), emptyKnown())
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if out.RectID != "r1" || out.TextID != "t1" || !out.Retried || out.Adopted {
		t.Fatalf("outcome = %+v, want retried r1/t1", out)
	}
	if ex.calls != 2 || slept != 1 {
		t.Fatalf("exec=%d slept=%d, want exec=2 slept=1", ex.calls, slept)
	}
}

// ── JS 自报失败且自清理干净 → 轻读 settle 后重发一次(P3 六连败的病)──────

func TestDrawOneZoneJSFailureCleanCanvasRetriesAfterSettleRead(t *testing.T) {
	tg := resilientTarget()
	ex := &fakeZoneExec{results: []func() (map[string]any, error){
		failDrawClean("text create returned undefined for D_ESD / U"),
		okDraw("r2", "t2"),
	}}
	sv := &fakeSurvey{results: []func() (zoneFrameSurvey, error){surveyWith(nil)}} // settle read
	slept := 0
	out := drawOneZoneResilient(zoneDrawDeps{exec: ex.exec, survey: sv.survey, sleep: func() { slept++ }},
		tg, buildPartitionZoneDrawJS(tg, []byte(`"#AA00AA"`)), emptyKnown())
	if out.Err != nil {
		t.Fatalf("unexpected error: %v", out.Err)
	}
	if out.RectID != "r2" || out.TextID != "t2" || !out.Retried {
		t.Fatalf("outcome = %+v, want retried r2/t2", out)
	}
	if sv.calls != 1 {
		t.Fatalf("settle read calls = %d, want 1 (the light read before the retry)", sv.calls)
	}
	if slept != 1 {
		t.Fatalf("slept = %d, want 1", slept)
	}
}

// ── 清理有幸存者 = 画布脏 → 绝不重发,幸存 id 交回登记 ────────────────────

func TestDrawOneZoneCleanupSurvivorsNoRetry(t *testing.T) {
	tg := resilientTarget()
	ex := &fakeZoneExec{results: []func() (map[string]any, error){
		func() (map[string]any, error) {
			return map[string]any{"ok": false, "error": "boom",
				"rects": []any{"r_half"}, "texts": []any{},
				"cleanupSurvived": []any{"r_half"}, "cleanupErrors": []any{}}, nil
		},
	}}
	out := drawOneZoneResilient(zoneDrawDeps{exec: ex.exec,
		survey: (&fakeSurvey{}).survey, sleep: func() { t.Fatal("must not sleep") }},
		tg, buildPartitionZoneDrawJS(tg, []byte(`"#AA00AA"`)), emptyKnown())
	if out.Err == nil {
		t.Fatal("expected an error")
	}
	if ex.calls != 1 {
		t.Fatalf("exec called %d times, want 1 — dirty canvas must never be retried into", ex.calls)
	}
	if len(out.StrandedRects) != 1 || out.StrandedRects[0] != "r_half" {
		t.Fatalf("stranded rects = %v, want [r_half] (recorded for --clear recovery)", out.StrandedRects)
	}
}

// ── 复核读也失败 → 不重发,错误里说明白 ─────────────────────────────────

func TestDrawOneZoneUnverifiableFailureIsNotResent(t *testing.T) {
	tg := resilientTarget()
	ex := &fakeZoneExec{results: []func() (map[string]any, error){transportFail()}}
	sv := &fakeSurvey{results: []func() (zoneFrameSurvey, error){surveyFail()}}
	out := drawOneZoneResilient(zoneDrawDeps{exec: ex.exec, survey: sv.survey,
		sleep: func() { t.Fatal("must not sleep") }},
		tg, buildPartitionZoneDrawJS(tg, []byte(`"#AA00AA"`)), emptyKnown())
	if out.Err == nil {
		t.Fatal("expected an error")
	}
	if ex.calls != 1 {
		t.Fatalf("exec called %d times, want 1 — unverifiable state must NOT be resent", ex.calls)
	}
	if !strings.Contains(out.Err.Error(), "NOT retrying") {
		t.Fatalf("error must state that no retry happened: %v", out.Err)
	}
}

// ── 歧义落地(标题在、rect 数对不上)→ 不重发,可证 id 交回登记 ───────────

func TestDrawOneZoneAmbiguousLandingRecordsStrandedIDs(t *testing.T) {
	tg := resilientTarget()
	ex := &fakeZoneExec{results: []func() (map[string]any, error){transportFail()}}
	sv := &fakeSurvey{results: []func() (zoneFrameSurvey, error){
		// 标题落了,但页面上多出两个不认识的 rect —— 无法配对,不能重发。
		surveyWith([]string{"r_a", "r_b"}, plannedTitleText(tg, "t_l")),
	}}
	out := drawOneZoneResilient(zoneDrawDeps{exec: ex.exec, survey: sv.survey,
		sleep: func() { t.Fatal("must not sleep") }},
		tg, buildPartitionZoneDrawJS(tg, []byte(`"#AA00AA"`)), emptyKnown())
	if out.Err == nil {
		t.Fatal("expected an error")
	}
	if ex.calls != 1 {
		t.Fatalf("exec called %d times, want 1", ex.calls)
	}
	if len(out.StrandedRects) != 2 || len(out.StrandedTexts) != 1 || out.StrandedTexts[0] != "t_l" {
		t.Fatalf("stranded = rects %v texts %v, want both new rects + the landed title", out.StrandedRects, out.StrandedTexts)
	}
}

// ── 幂等匹配:已画好的框(记录配对 + 画布证实)直接保留 ────────────────────

func TestMatchExistingZoneFrame(t *testing.T) {
	tg := resilientTarget()
	prev := &workflow.SchZoneFrames{Rects: []string{"rA", "rB"}, Texts: []string{"tA", "tB"}}

	s := zoneFrameSurvey{
		Rects:    map[string]bool{"rB": true},
		RectGeom: plannedZoneGeom(tg, "rB"),
		Texts:    []zoneSurveyText{plannedTitleText(tg, "tB")},
	}
	rid, tid, ok := matchExistingZoneFrame(tg, prev, s)
	if !ok || rid != "rB" || tid != "tB" {
		t.Fatalf("match = (%q,%q,%v), want (rB,tB,true)", rid, tid, ok)
	}

	// 配对 rect 已不在画布 → 不算已画(宁可重画,不可漏)。
	s.Rects = map[string]bool{}
	if _, _, ok := matchExistingZoneFrame(tg, prev, s); ok {
		t.Fatal("dead paired rect must not count as drawn")
	}

	// 标题内容对但锚点漂了(plan 变了)→ 不算已画。
	moved := plannedTitleText(tg, "tB")
	moved.X += 10
	s = zoneFrameSurvey{Rects: map[string]bool{"rB": true}, RectGeom: plannedZoneGeom(tg, "rB"), Texts: []zoneSurveyText{moved}}
	if _, _, ok := matchExistingZoneFrame(tg, prev, s); ok {
		t.Fatal("a title at a stale anchor must not count as drawn")
	}

	// 标题在画布上但不在记录里(用户自己的文字)→ 不算。
	s = zoneFrameSurvey{Rects: map[string]bool{"rB": true}, RectGeom: plannedZoneGeom(tg, "rB"), Texts: []zoneSurveyText{plannedTitleText(tg, "t_user")}}
	if _, _, ok := matchExistingZoneFrame(tg, prev, s); ok {
		t.Fatal("an unrecorded title text must not count as drawn")
	}

	if _, _, ok := matchExistingZoneFrame(tg, nil, s); ok {
		t.Fatal("nil record must not match")
	}
}

// #234: 搬件后框的左上角常常不动 —— 标题锚点逐字不变,只有宽/高变了。
// 判据只要不看矩形几何,就会报「已经正确」并零写入,旧框留在画布上。
func TestMatchExistingZoneFrameRejectsAStaleRectUnderAnUnchangedTitle(t *testing.T) {
	tg := resilientTarget() // Rect 100,200 → 400,500;标题锚点在左上角
	prev := &workflow.SchZoneFrames{Rects: []string{"rB"}, Texts: []string{"tB"}}
	title := []zoneSurveyText{plannedTitleText(tg, "tB")}

	// 成员往右/往下挪 → 计划的框变大,左上角(MinX/MaxY)一点没动。
	stale := tg.Rect
	stale.MaxX -= 60
	stale.MinY += 40
	if stale.MinX != tg.Rect.MinX || stale.MaxY != tg.Rect.MaxY {
		t.Fatal("this case is only meaningful while the title corner is unchanged")
	}
	s := zoneFrameSurvey{Rects: map[string]bool{"rB": true},
		RectGeom: map[string]layoutBBox{"rB": stale}, Texts: title}
	if _, _, ok := matchExistingZoneFrame(tg, prev, s); ok {
		t.Fatal("a frame whose bbox no longer matches the plan must be redrawn, not kept")
	}

	// 几何读不到(旧连接器、getAll 抛错、矩形被旋转)= 未知,不是「没变」。
	s.RectGeom = map[string]layoutBBox{}
	if _, _, ok := matchExistingZoneFrame(tg, prev, s); ok {
		t.Fatal("unreadable geometry must be treated as not drawn")
	}

	// 浮点噪声仍然算同一个框,否则每次都白重画一遍。
	near := tg.Rect
	near.MaxX += zoneAnchorEps / 2
	s.RectGeom = map[string]layoutBBox{"rB": near}
	if _, _, ok := matchExistingZoneFrame(tg, prev, s); !ok {
		t.Fatal("a difference below the anchor tolerance must still count as drawn")
	}
}

func TestParseZoneFrameSurvey(t *testing.T) {
	s := parseZoneFrameSurvey(map[string]any{
		"ok":    true,
		"rects": []any{"r1", "r2"},
		"rectGeom": []any{
			// create(MinX, MaxY, w, h) ⇒ 读回来是 {x:MinX, y:MaxY, width, height}。
			map[string]any{"id": "r1", "x": 100.0, "y": 500.0, "width": 300.0, "height": 300.0, "rotation": 0.0},
			// EasyEDA Pro 3.2.149 会把 TopLeftY 镜像成负数，几何仍应与 r1 相同。
			map[string]any{"id": "r4", "x": 100.0, "y": -500.0, "width": 300.0, "height": 300.0, "rotation": 0.0},
			map[string]any{"id": "r2", "x": 0.0, "y": 10.0, "width": 10.0, "height": 10.0, "rotation": 90.0}, // 旋转过 → 不是我们画的形态
			map[string]any{"id": "r3", "x": 0.0, "y": 10.0, "width": 0.0, "height": 10.0, "rotation": 0.0},   // 退化
			map[string]any{"id": "", "x": 1.0, "y": 2.0, "width": 3.0, "height": 4.0, "rotation": 0.0},
			"garbage",
		},
		"texts": []any{
			map[string]any{"id": "t1", "content": "POWER", "x": 104.0, "y": 478.0},
			"garbage",
		},
	})
	if !s.Rects["r1"] || !s.Rects["r2"] || len(s.Texts) != 1 {
		t.Fatalf("parsed survey = %+v", s)
	}
	// 几何只收可信的那些;收不到的 id 仍在 Rects 里,只是几何「未知」。
	if got := s.RectGeom["r1"]; got != (layoutBBox{MinX: 100, MinY: 200, MaxX: 400, MaxY: 500}) {
		t.Fatalf("r1 geometry = %+v", got)
	}
	if got := s.RectGeom["r4"]; got != (layoutBBox{MinX: 100, MinY: 200, MaxX: 400, MaxY: 500}) {
		t.Fatalf("r4 mirrored-y geometry = %+v", got)
	}
	if len(s.RectGeom) != 2 {
		t.Fatalf("only r1 and r4 are usable geometry, got %+v", s.RectGeom)
	}
	if s.Texts[0] != (zoneSurveyText{ID: "t1", Content: "POWER", X: 104, Y: 478}) {
		t.Fatalf("text = %+v", s.Texts[0])
	}
	if !s.hasText("t1") || s.hasText("t2") {
		t.Fatal("hasText misbehaves")
	}
}

func TestAnyZoneFrameIDLive(t *testing.T) {
	f := &workflow.SchZoneFrames{Rects: []string{"r1"}, Texts: []string{"t1"}}
	if anyZoneFrameIDLive(f, zoneFrameSurvey{Rects: map[string]bool{}}) {
		t.Fatal("nothing live → false")
	}
	if !anyZoneFrameIDLive(f, zoneFrameSurvey{Rects: map[string]bool{"r1": true}}) {
		t.Fatal("live rect → true")
	}
	if !anyZoneFrameIDLive(f, zoneFrameSurvey{Rects: map[string]bool{}, Texts: []zoneSurveyText{{ID: "t1"}}}) {
		t.Fatal("live text → true")
	}
}
