package app

// cmd_sch_zone_draw.go — `sch zone-draw`: make the claimed functional zones
// VISIBLE on the schematic sheet (行业规范的「先看区、再看线」分区框).
//
// `sch zones set` persists the S0 partitioning and layout-lint verifies it
// mechanically; this command draws it for humans: one dashed rectangle + a
// "module (zone)" label per claim, resolved from the LIVE sheet bbox with the
// same zoneRect() geometry the violation rule uses — what you see IS what the
// gate checks.
//
// Implementation note: the schematic graphics API (eda.sch_PrimitiveRectangle /
// sch_PrimitiveText — full CRUD, probed live 2026-07-19 on ceshi) has no typed
// action yet, so DRAWING goes through the debug.exec_js hatch. Clearing uses the
// typed schematic.primitives.delete + includePagePrimitives readback
// (typedZoneFrameClear). Created primitive ids are recorded in the project
// workflow state so redraw/--clear removes exactly what this tool created and
// never touches user graphics.

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

// schZoneFrameInset shrinks each zone rectangle so adjacent frames don't sit
// on the exact same boundary line (schematic units).
const schZoneFrameInset = 4

// schZoneMinFrameSpan is the smallest frame worth drawing after the margin and
// title-block adjustments: below this a "frame" is a sliver that reads as a
// stray line, so the zone is skipped instead.
const schZoneMinFrameSpan = 40

// schZoneOpts are the fixed-grid frame tunables. The defaults mirror the
// partition mode's page margin so the two modes look like the same tool.
type schZoneOpts struct {
	Margin   float64 // inset from the sheet border, so frames never sit on it
	Inset    float64 // gap between adjacent frames
	LabelPad float64 // label inset from the frame's own lines
	Gap      float64 // clearance kept from the title-block keep-out
}

func defaultSchZoneOpts() schZoneOpts {
	return schZoneOpts{Margin: 20, Inset: schZoneFrameInset, LabelPad: 6, Gap: 8}
}

// schZoneFrameRect computes the rectangle actually drawn for one zone: the grid
// cell of the margin-inset sheet, shrunk by Inset, then lifted clear of the
// title-block keep-out. The bool is false when nothing usable survives.
//
// Issue #163: the frames used to be laid out on the RAW sheet bbox with only a
// 4-unit inset, so they sat on the sheet border and the bottom row ran straight
// through the title block — `sch check`'s own titleblock-overlap rule fired on
// frames zone-draw had just drawn, even though the keep-out geometry was
// already available from deriveSheetGeometry.
func schZoneFrameRect(zone string, sheet layoutBBox, tb *layoutBBox, o schZoneOpts) (layoutBBox, bool) {
	usable := layoutBBox{
		MinX: sheet.MinX + o.Margin, MaxX: sheet.MaxX - o.Margin,
		MinY: sheet.MinY + o.Margin, MaxY: sheet.MaxY - o.Margin,
	}
	if usable.MaxX-usable.MinX <= 0 || usable.MaxY-usable.MinY <= 0 {
		return layoutBBox{}, false
	}
	cell := zoneRect(zone, usable)
	frame := layoutBBox{
		MinX: cell.MinX + o.Inset, MaxX: cell.MaxX - o.Inset,
		MinY: cell.MinY + o.Inset, MaxY: cell.MaxY - o.Inset,
	}
	frame, ok := liftClearOfTitleBlock(frame, tb, o.Gap)
	if !ok {
		return layoutBBox{}, false
	}
	if frame.MaxX-frame.MinX < schZoneMinFrameSpan || frame.MaxY-frame.MinY < schZoneMinFrameSpan {
		return layoutBBox{}, false
	}
	return frame, true
}

// liftClearOfTitleBlock pulls a frame off the title-block keep-out. The title
// block sits at the sheet's bottom-right, so the natural move is to raise the
// frame's bottom edge above it; lowering the top edge is the fallback for the
// (unusual) keep-out that sits above the frame's middle.
func liftClearOfTitleBlock(frame layoutBBox, tb *layoutBBox, gap float64) (layoutBBox, bool) {
	if tb == nil || !boxesOverlap(frame, *tb) {
		return frame, true
	}
	// Keep-out covers the frame's lower part → raise the floor.
	if lifted := tb.MaxY + gap; lifted < frame.MaxY {
		frame.MinY = lifted
		return frame, true
	}
	// Keep-out covers the frame's upper part → drop the ceiling.
	if dropped := tb.MinY - gap; dropped > frame.MinY {
		frame.MaxY = dropped
		return frame, true
	}
	return layoutBBox{}, false // fully swallowed by the keep-out
}

// writeZoneRectangleCreateJS emits the one rectangle-create call shared by the
// fixed-grid and data-driven partition draw paths. EasyEDA anchors schematic
// rectangles at the visual TOP-LEFT corner: (MinX, MaxY) on the y-UP canvas,
// then extends toward -y by height. Keeping that conversion here prevents one
// mode from accidentally passing MinY and dropping the rendered frame one full
// height below its planned bbox.
func writeZoneRectangleCreateJS(b *strings.Builder, r layoutBBox, colorJS []byte) bool {
	w := r.MaxX - r.MinX
	h := r.MaxY - r.MinY
	if w <= 0 || h <= 0 {
		return false
	}
	fmt.Fprintf(b, "{ const rc = await eda.sch_PrimitiveRectangle.create(%g, %g, %g, %g, 0, 0, %s, null, 1, 1);\n",
		r.MinX, r.MaxY, w, h, colorJS)
	return true
}

// writeZoneDrawPrelude/Epilogue make a graphics draw self-cleaning. A thrown SDK
// error after rectangle N but before text N used to strand the already-created
// primitives because exec_js returned no ids. The catch path now deletes every id
// accumulated so far and reports any survivor.
func writeZoneDrawPrelude(b *strings.Builder) {
	b.WriteString(`const rects=[], texts=[];
const genericPrim = eda.sch_PrimitiveObject && typeof eda.sch_PrimitiveObject.delete === 'function'
  ? eda.sch_PrimitiveObject : null;
const cleanupCreated = async () => {
  const cleanupErrors = [];
  // Generic class first: the per-class text delete does not persist (issue #164),
  // so a rollback through it leaves the labels behind on the next reload.
  // 逐个删 + 回读 + 幸存者重试一次(缺陷 3:批量 delete 静默 no-op 仍返 true)。
  const delOne = async (id, cls) => {
    try { await (genericPrim ?? cls).delete([id]); }
    catch (err) { cleanupErrors.push(id + ': ' + String(err)); }
  };
  for (const id of rects) await delOne(id, eda.sch_PrimitiveRectangle);
  for (const id of texts) await delOne(id, eda.sch_PrimitiveText);
  try {
    let aliveRects = new Set(await eda.sch_PrimitiveRectangle.getAllPrimitiveId());
    let aliveTexts = new Set(await eda.sch_PrimitiveText.getAllPrimitiveId());
    const needRetry = rects.some(id => aliveRects.has(id)) || texts.some(id => aliveTexts.has(id));
    if (needRetry) {
      for (const id of rects) if (aliveRects.has(id)) await delOne(id, eda.sch_PrimitiveRectangle);
      for (const id of texts) if (aliveTexts.has(id)) await delOne(id, eda.sch_PrimitiveText);
      aliveRects = new Set(await eda.sch_PrimitiveRectangle.getAllPrimitiveId());
      aliveTexts = new Set(await eda.sch_PrimitiveText.getAllPrimitiveId());
    }
    return {cleanupSurvived:[...rects.filter(id => aliveRects.has(id)), ...texts.filter(id => aliveTexts.has(id))], cleanupErrors};
  } catch (err) {
    cleanupErrors.push(String(err));
    return {cleanupSurvived:[...rects, ...texts], cleanupErrors};
  }
};
try {
`)
}

func writeZoneDrawEpilogue(b *strings.Builder) {
	b.WriteString(`  return {ok:true, rects, texts};
} catch (err) {
  const cleanup = await cleanupCreated();
  return {ok:false, error: err instanceof Error ? err.message : String(err), rects, texts, ...cleanup};
}`)
}

func asStringSlice(v any) []string {
	if raw, ok := v.([]string); ok {
		return append([]string(nil), raw...)
	}
	raw, _ := v.([]any)
	out := make([]string, 0, len(raw))
	for _, it := range raw {
		if s := asString(it); s != "" {
			out = append(out, s)
		}
	}
	return out
}

const (
	defaultFixedZoneFontSize     = 14
	defaultPartitionZoneFontSize = 22
)

// pinZonePage resolves and pins one active schematic document before any zone
// read/write. It also honors the global --doc selector.
func pinZonePage(cfg *appConfig, window string) (*appConfig, string, string, error) {
	win, err := resolveTargetWindow(cfg, window)
	if err != nil {
		return nil, "", "", fmt.Errorf("schematic page: resolve target window: %w", err)
	}
	local := *cfg
	if err := ensureActiveDoc(&local, win); err != nil {
		return nil, "", "", fmt.Errorf("schematic page: activate target page: %w", err)
	}
	docUUID, err := currentSchematicDocumentUUID(&local, win)
	if err != nil {
		return nil, "", "", fmt.Errorf("schematic page: %w", err)
	}
	local.doc = docUUID
	return &local, win, docUUID, nil
}

func zoneFramesEmpty(f *workflow.SchZoneFrames) bool {
	return f == nil || (len(f.Rects) == 0 && len(f.Texts) == 0)
}

// recordedZoneFrames returns this page's record. A legacy unscoped record is a
// candidate only until its ids are inspected on the current page; if none are
// present we leave it untouched so visiting the original page can still recover
// and clear it.
func recordedZoneFrames(st *pcbStageState, docUUID string) (*workflow.SchZoneFrames, string) {
	if st == nil {
		return nil, ""
	}
	if f := st.SchZoneFrameIdsByPage[docUUID]; !zoneFramesEmpty(f) {
		return f, "page"
	}
	if f := st.SchZoneFrameIds; !zoneFramesEmpty(f) &&
		(f.DocumentUUID == "" || f.DocumentUUID == docUUID) {
		return f, "legacy"
	}
	return nil, ""
}

func removeRecordedZoneFrames(st *pcbStageState, docUUID, source string) {
	switch source {
	case "page":
		delete(st.SchZoneFrameIdsByPage, docUUID)
	case "legacy":
		st.SchZoneFrameIds = nil
	}
}

func setRecordedZoneFrames(st *pcbStageState, docUUID, mode string, f *workflow.SchZoneFrames) {
	if st.SchZoneFrameIdsByPage == nil {
		st.SchZoneFrameIdsByPage = map[string]*workflow.SchZoneFrames{}
	}
	f.DocumentUUID = docUUID
	f.Mode = mode
	st.SchZoneFrameIdsByPage[docUUID] = f
}

func validateZoneDrawResult(v map[string]any, expected int) (*workflow.SchZoneFrames, error) {
	f := &workflow.SchZoneFrames{
		Rects: asStringSlice(v["rects"]),
		Texts: asStringSlice(v["texts"]),
		At:    nowRFC3339(),
	}
	if !asBool(v["ok"]) {
		return f, fmt.Errorf("zone draw failed: %s (cleanup survivors: %v, cleanup errors: %v)",
			asString(v["error"]), asStringSlice(v["cleanupSurvived"]), v["cleanupErrors"])
	}
	seen := map[string]bool{}
	for _, id := range append(append([]string(nil), f.Rects...), f.Texts...) {
		if id == "" || seen[id] {
			return f, fmt.Errorf("zone draw returned an empty or duplicate primitive id %q", id)
		}
		seen[id] = true
	}
	if len(f.Rects) != expected || len(f.Texts) != expected {
		return f, fmt.Errorf("zone draw returned %d rectangle id(s) and %d text id(s), want %d each",
			len(f.Rects), len(f.Texts), expected)
	}
	return f, nil
}

type zoneJSExecutor func(phase, code string) (map[string]any, error)

// zoneFrameClearFn deletes a recorded frame set and returns how many of its ids
// were present on the page. A survivor is an error (fail closed).
type zoneFrameClearFn func(f *workflow.SchZoneFrames, phase string) (int, error)

// typedZoneFrameClear clears frames through typed actions pinned to docUUID:
// schematic.primitives.delete one id at a time (it routes page graphics through
// the persisting sch_PrimitiveObject delete, #164) and an includePagePrimitives
// read as the alive set. The former exec_js clear re-read immediately after the
// deletes, so an unsettled snapshot reported fresh frames as survivors and
// zone move failed its redraw intermittently (v1.13.0 acceptance, 2026-10-16).
func typedZoneFrameClear(cfg *appConfig, window, docUUID string) zoneFrameClearFn {
	return func(f *workflow.SchZoneFrames, phase string) (int, error) {
		deleteOne := func(id string) error {
			_, err := requestAutolayoutActionTimed(cfg, "schematic.primitives.delete", window,
				map[string]any{"primitiveIds": []string{id}}, 30*time.Second, docUUID, phase)
			return err
		}
		alive := func() (map[string]bool, error) {
			res, err := requestAutolayoutActionTimed(cfg, "schematic.components.list", window,
				map[string]any{"includePagePrimitives": true}, 30*time.Second, docUUID, phase+" (verify)")
			if err != nil {
				return nil, err
			}
			return zoneFrameAliveSet(res.Result)
		}
		return clearZoneFramesWith(f, deleteOne, alive)
	}
}

// zoneFrameAliveSet collects live rectangle and text ids from a components.list
// pagePrimitives read; a missing inventory or a malformed entry is an error,
// never "all deleted".
func zoneFrameAliveSet(result map[string]any) (map[string]bool, error) {
	pp, _ := result["pagePrimitives"].(map[string]any)
	if pp == nil {
		return nil, fmt.Errorf("page primitive inventory unavailable")
	}
	alive := map[string]bool{}
	for _, kind := range []string{"rectangles", "texts"} {
		items, ok := pp[kind].([]any)
		if !ok {
			return nil, fmt.Errorf("page primitive inventory has no %s list", kind)
		}
		for i, it := range items {
			// An entry that cannot name its primitive is a primitive this
			// read failed to identify, possibly the very frame being cleared.
			// Skipping it would turn "unknown" into "deleted".
			m, ok := it.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("page primitive inventory %s[%d] is not an object", kind, i)
			}
			id, ok := m["primitiveId"].(string)
			if !ok || strings.TrimSpace(id) == "" {
				return nil, fmt.Errorf("page primitive inventory %s[%d] has no primitiveId", kind, i)
			}
			alive[id] = true
		}
	}
	return alive, nil
}

// clearZoneFramesWith deletes the recorded ids that are present on the page with
// the repository's single verified-delete routine (one by one, settled readback,
// one retry). Ids already absent are not deleted and not counted.
//
// "Already absent" is itself a claim about the page, and one inventory read is
// not proof of it: a read taken right after a draw can predate the frames, and
// would then report a clean page with every frame still on it (the record is
// dropped, the frames become untracked). An id missing from the first read is
// therefore re-read on the shared settle budget; it counts as absent only when
// every read agrees, and a read that fails leaves absence unconfirmed, which
// is an error so that callers keep the recovery record.
func clearZoneFramesWith(f *workflow.SchZoneFrames, deleteOne func(string) error, alive func() (map[string]bool, error)) (int, error) {
	recorded := append(append([]string(nil), f.Rects...), f.Texts...)
	seen := map[string]bool{}
	_, _, err := settleRead(func() (struct{}, bool, error) {
		live, rerr := alive()
		if rerr != nil {
			return struct{}{}, false, rerr
		}
		complete := true
		for _, id := range recorded {
			if live[id] {
				seen[id] = true
			} else if !seen[id] {
				complete = false
			}
		}
		return struct{}{}, complete, nil
	})
	if err != nil {
		return 0, fmt.Errorf("read zone frames before delete: %w", err)
	}
	var present []string
	for _, id := range recorded {
		if seen[id] {
			present = append(present, id)
		}
	}
	if len(present) == 0 {
		return 0, nil
	}
	res, err := deleteVerifiedOneByOne(present, deleteOne, alive)
	if err != nil {
		return len(present), err
	}
	if len(res.Survived) > 0 {
		return len(present), fmt.Errorf("zone frame delete was not verified; survived=%v errors=%v", res.Survived, res.Errors)
	}
	return len(present), nil
}

// cleanupNewZoneFrames is the compensation path for count/state failures after a
// draw. It never changes workflow state; callers preserve the old record so a
// retry remains recoverable.
func cleanupNewZoneFrames(clear zoneFrameClearFn, f *workflow.SchZoneFrames) error {
	if zoneFramesEmpty(f) {
		return nil
	}
	_, err := clear(f, "clean up incomplete zone draw")
	return err
}

// clearPriorZoneFrames removes this page's prior record in memory only after a
// verified delete. Callers persist state at the same checkpoint as the new draw
// (or after schematic.save for --clear).
func clearPriorZoneFrames(st *pcbStageState, docUUID string, clear zoneFrameClearFn, stderr io.Writer) (bool, error) {
	prev, source := recordedZoneFrames(st, docUUID)
	if prev == nil {
		return false, nil
	}
	found, err := clear(prev, "clear previous zone frames")
	if err != nil {
		return false, fmt.Errorf("clear previous zone frames: %w", err)
	}
	// An old unscoped record may belong to another page. No matching live id means
	// this page must not consume it; retain it for recovery on its actual page.
	if source == "legacy" && prev.DocumentUUID == "" && found == 0 {
		fmt.Fprintln(stderr, "note: legacy unscoped zone-frame ids are not present on this page; record retained for its original page")
		return false, nil
	}
	removeRecordedZoneFrames(st, docUUID, source)
	fmt.Fprintf(stderr, "cleared %d previous zone-frame primitive(s) on page %s\n", found, docUUID)
	return true, nil
}

func saveZoneDocument(cfg *appConfig, window, docUUID, phase string) error {
	return saveAutolayoutDocument(cfg, window, docUUID, phase)
}

// compensateZoneDraw removes a just-created, not-yet-committed frame set and
// explicitly saves the cleanup. If deletion or save cannot be proven, the full
// candidate id set is persisted as this page's recovery record: future --clear
// re-reads live ids, so retaining already-deleted ids is safe while losing a
// survivor id would create an unrecoverable orphan.
func compensateZoneDraw(
	cfg *appConfig,
	window, docUUID string,
	st *pcbStageState,
	mode string,
	clear zoneFrameClearFn,
	f *workflow.SchZoneFrames,
	cause error,
) error {
	if cerr := cleanupNewZoneFrames(clear, f); cerr != nil {
		setRecordedZoneFrames(st, docUUID, mode, f)
		if perr := savePcbStageState(st); perr != nil {
			return fmt.Errorf("%v; cleanup also failed: %v; recovery ids=%v/%v; persist recovery record failed: %w",
				cause, cerr, f.Rects, f.Texts, perr)
		}
		return fmt.Errorf("%v; cleanup also failed: %w; recovery ids retained for page %s",
			cause, cerr, docUUID)
	}
	if serr := saveZoneDocument(cfg, window, docUUID, "save zone-draw cleanup"); serr != nil {
		setRecordedZoneFrames(st, docUUID, mode, f)
		if perr := savePcbStageState(st); perr != nil {
			return fmt.Errorf("%v; cleanup succeeded in memory but save failed: %v; recovery ids=%v/%v; persist recovery record failed: %w",
				cause, serr, f.Rects, f.Texts, perr)
		}
		return fmt.Errorf("%v; cleanup succeeded in memory but save failed: %w; recovery ids retained for page %s",
			cause, serr, docUUID)
	}
	// The old frame record was already removed in memory before drawing. Persist
	// that checkpoint as well, otherwise a validation failure leaves stale ids on
	// disk even though both old and new graphics were verified absent.
	removeRecordedZoneFrames(st, docUUID, "page")
	if perr := savePcbStageState(st); perr != nil {
		return fmt.Errorf("%v; cleanup was verified and saved, but clearing stale recovery state failed: %w",
			cause, perr)
	}
	return cause
}

// newSchZoneDrawCmd builds `sch zone-draw`.
func newSchZoneDrawCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var color string
	var clear bool
	var mode string
	var fontSize, margin, gutter, titleBand float64
	c := &cobra.Command{
		Use:   "zone-draw",
		Short: "Draw the claimed functional zones as dashed frames + labels on the sheet (--clear removes them)",
		Long: `Draw functional module frames from live group geometry and the sheet bbox.
Use sch frame for explicit frame geometry, or sch compose for a complete
data-driven Lib composition. These annotations do not establish connectivity.

Frames are annotation graphics, not electrical objects. Their primitive ids are
recorded by document UUID in the project workflow state; re-running redraws
(old frames verified removed first) and --clear deletes them without touching
another page or user graphics. Draw/clear explicitly saves the schematic.
Use the global --doc <page> selector for multi-page projects.`,
		Example: `  pcbpilot sch zones set --spec s0.json --project ceshi
  pcbpilot sch zone-draw --doc P1_MCU --font-size 14 --project ceshi
  pcbpilot sch zone-draw --doc P1_MCU --clear --project ceshi`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Partition mode (issue #149): whole-sheet data-driven functional partitions
			// via the planner + per-page frame persistence, instead of the fixed
			// zones-grid rectangles below.
			// 固定九宫格模式已废弃(用户拍板 2026-08-15):框的几何一律由**活体模块
			// bbox** 反推。九宫格的框与电路的实际位置无关 —— 单模块页铺满整纸时,
			// 框套不住电路、框里大半是空的,而且它还让 zone-violation 对着一张画得好
			// 好的图报违规。分区是**版面**,版面就该跟着电路走。
			if mode != "" && mode != "partition" {
				return fmt.Errorf("--mode %q 已废弃:分区框一律数据驱动(从活体模块 bbox 反推);去掉 --mode 即可", mode)
			}
			if fontSize <= 0 {
				fontSize = defaultPartitionZoneFontSize
			}
			// 韧性路径(REPORT esp32mini-round2 新 3):逐区单次 exec_js +
			// 复核后重试 + partial 语义。见 cmd_sch_zone_draw_resilient.go。
			return runPartitionDrawResilient(cfg, *window,
				partitionOptsFrom(margin, gutter, titleBand), fontSize, color, clear, stdout, stderr)
		},
	}
	c.Flags().StringVar(&color, "color", "#AA00AA", "frame + label color")
	c.Flags().BoolVar(&clear, "clear", false, "remove the frames drawn by the last zone-draw on this page")
	c.Flags().StringVar(&mode, "mode", "", "已废弃:分区框一律数据驱动;传 zones 会报错")
	c.Flags().Float64Var(&fontSize, "font-size", 0, "分区标题字号(默认 22)")
	// Defaults come from defaultPartitionOpts so the flag layer can never drift
	// from the planner's own defaults (margin 20 lingered here after the planner
	// moved to 28 — live 2026-08-11).
	def := defaultPartitionOpts()
	c.Flags().Float64Var(&margin, "margin", def.Margin, "--mode partition: page margin inset from the sheet edge")
	c.Flags().Float64Var(&gutter, "gutter", def.Gutter, "为说明扩边时与邻框/障碍保持的间距(分区怎么分与它无关——一区一框)")
	c.Flags().Float64Var(&titleBand, "title-band", def.TitleBand, "--mode partition: height of each partition's title band")
	return c
}
