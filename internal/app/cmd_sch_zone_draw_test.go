package app

import (
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

// TestBuildZoneDrawJS pins the generated script: deterministic module order,
// inset dashed rects, y-UP rectangle anchor, and label inside the top-left.

var zoneRectangleCreateRE = regexp.MustCompile(
	`sch_PrimitiveRectangle\.create\(([-+0-9.eE]+), ([-+0-9.eE]+), ([-+0-9.eE]+), ([-+0-9.eE]+),`,
)

// renderedZoneRectangleBBoxes reconstructs SDK readback geometry from generated
// create(topLeftX, topLeftY, width, height) calls. On the y-UP canvas the rendered
// bbox is [x, topY-height → x+width, topY].
func renderedZoneRectangleBBoxes(t *testing.T, js string) []layoutBBox {
	t.Helper()
	matches := zoneRectangleCreateRE.FindAllStringSubmatch(js, -1)
	out := make([]layoutBBox, 0, len(matches))
	for _, m := range matches {
		var values [4]float64
		for i := range values {
			v, err := strconv.ParseFloat(m[i+1], 64)
			if err != nil {
				t.Fatalf("parse rectangle arg %q: %v\njs:\n%s", m[i+1], err, js)
			}
			values[i] = v
		}
		x, topY, w, h := values[0], values[1], values[2], values[3]
		out = append(out, layoutBBox{MinX: x, MinY: topY - h, MaxX: x + w, MaxY: topY})
	}
	return out
}

func requireZoneBBoxEqual(t *testing.T, got, want layoutBBox) {
	t.Helper()
	const eps = 1e-9
	if math.Abs(got.MinX-want.MinX) > eps ||
		math.Abs(got.MinY-want.MinY) > eps ||
		math.Abs(got.MaxX-want.MaxX) > eps ||
		math.Abs(got.MaxY-want.MaxY) > eps {
		t.Fatalf("rendered bbox = %+v, want %+v", got, want)
	}
}

// Both fixed-grid and partition modes must go through the same SDK rectangle
// semantics. This catches the old fixed-mode bug where MinY was passed as the
// top-left y and the rendered frame dropped one full height below its target.

// A bottom-right partition is lifted above the title-block keep-out by the
// planner. The generated SDK call must render that exact bbox; using MinY as the
// top-left y would drop it across the title-block and below the sheet.
func TestPartitionDrawRenderedBBoxStaysClearOfTitleBlock(t *testing.T) {
	sheet := layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825}
	keepout := layoutBBox{MinX: 468, MinY: 0, MaxX: 1170, MaxY: 165}
	modules := []partitionModule{{
		Name: "IO",
		BBox: layoutBBox{MinX: 900, MinY: 200, MaxX: 1000, MaxY: 260},
	}}
	plan := planPartitions(sheet, &keepout, modules, defaultPartitionOpts())
	if len(plan.Partitions) != 1 {
		t.Fatalf("planner produced %d partitions, want 1: %+v", len(plan.Partitions), plan)
	}
	if !plan.Validation.clean() {
		t.Fatalf("planner validation not clean: %+v", plan.Validation)
	}

	js := buildPartitionDrawJS(plan, 22, "#AA00AA")
	rendered := renderedZoneRectangleBBoxes(t, js)
	if len(rendered) != 1 {
		t.Fatalf("draw emitted %d rectangles, want 1\n%s", len(rendered), js)
	}
	requireZoneBBoxEqual(t, rendered[0], plan.Partitions[0].BBox)
	if !bboxContains(sheet, rendered[0]) {
		t.Errorf("rendered partition escaped sheet: sheet=%+v rendered=%+v", sheet, rendered[0])
	}
	if boxesOverlap(rendered[0], keepout) {
		t.Errorf("rendered partition crossed title-block keep-out: partition=%+v keepout=%+v", rendered[0], keepout)
	}
}

// The verdict waits for a settled readback: a frame still listed right after its
// delete but gone on the next read is deleted, not a survivor (v1.13.0
// acceptance: the old exec_js clear re-read at once and failed zone move).
func TestClearZoneFramesWithSettlesBeforeJudging(t *testing.T) {
	f := &workflow.SchZoneFrames{Rects: []string{"r1"}, Texts: []string{"t1", "gone"}}
	reads, deleted := 0, map[string]int{}
	alive := func() (map[string]bool, error) {
		reads++
		if reads <= 2 { // before-read + the first post-delete read: not settled yet
			return map[string]bool{"r1": true, "t1": true, "user": true}, nil
		}
		return map[string]bool{"user": true}, nil
	}
	found, err := clearZoneFramesWith(f, func(id string) error { deleted[id]++; return nil }, alive)
	if err != nil || found != 2 {
		t.Fatalf("settled delete reported failure: found=%d err=%v", found, err)
	}
	if deleted["gone"] != 0 || deleted["user"] != 0 || deleted["r1"] != 1 || deleted["t1"] != 1 {
		t.Fatalf("deleted the wrong ids: %v", deleted)
	}
}

func TestClearZoneFramesWithFailsOnRealSurvivor(t *testing.T) {
	f := &workflow.SchZoneFrames{Rects: []string{"r1"}, Texts: []string{"t1"}}
	alive := func() (map[string]bool, error) { return map[string]bool{"t1": true}, nil }
	if _, err := clearZoneFramesWith(f, func(string) error { return nil }, alive); err == nil || !strings.Contains(err.Error(), "survived=[t1]") {
		t.Fatalf("persistent survivor not reported: %v", err)
	}
}

func TestZoneFrameAliveSetRequiresInventory(t *testing.T) {
	if _, err := zoneFrameAliveSet(map[string]any{}); err == nil {
		t.Fatal("missing pagePrimitives must not read as all-deleted")
	}
	got, err := zoneFrameAliveSet(map[string]any{"pagePrimitives": map[string]any{
		"rectangles": []any{map[string]any{"primitiveId": "r1"}},
		"texts":      []any{map[string]any{"primitiveId": "t1"}},
	}})
	if err != nil || !got["r1"] || !got["t1"] || len(got) != 2 {
		t.Fatalf("alive set wrong: %v %v", got, err)
	}
}

func TestValidateZoneDrawResultRequiresExactUniqueIds(t *testing.T) {
	ok := map[string]any{
		"ok":    true,
		"rects": []string{"r1", "r2"},
		"texts": []string{"t1", "t2"},
	}
	if _, err := validateZoneDrawResult(ok, 2); err != nil {
		t.Fatalf("exact ids rejected: %v", err)
	}
	if _, err := validateZoneDrawResult(map[string]any{
		"ok": true, "rects": []string{"r1", "r2"}, "texts": []string{"t1"},
	}, 2); err == nil {
		t.Fatal("missing text id accepted")
	}
	if _, err := validateZoneDrawResult(map[string]any{
		"ok": true, "rects": []string{"same"}, "texts": []string{"same"},
	}, 1); err == nil {
		t.Fatal("duplicate rectangle/text id accepted")
	}
}

func TestClearPriorZoneFramesFailsClosedAndKeepsState(t *testing.T) {
	st := &pcbStageState{
		SchZoneFrameIdsByPage: map[string]*workflow.SchZoneFrames{
			"page-a": {DocumentUUID: "page-a", Rects: []string{"r1"}, Texts: []string{"t1"}},
		},
	}
	clear := func(*workflow.SchZoneFrames, string) (int, error) {
		return 2, errors.New("zone frame delete was not verified; survived=[t1]")
	}
	if _, err := clearPriorZoneFrames(st, "page-a", clear, io.Discard); err == nil {
		t.Fatal("surviving text did not fail clear")
	}
	if st.SchZoneFrameIdsByPage["page-a"] == nil {
		t.Fatal("failed clear discarded the only recovery ids")
	}
}

func TestCompensateZoneDrawRetainsRecoveryIdsWhenCleanupSurvives(t *testing.T) {
	t.Setenv(workflow.EnvDir, t.TempDir())
	st := &workflow.State{Project: "zone-project"}
	frames := &workflow.SchZoneFrames{Rects: []string{"r1"}, Texts: []string{"t1"}}
	clear := func(*workflow.SchZoneFrames, string) (int, error) {
		return 2, errors.New("zone frame delete was not verified; survived=[t1]")
	}
	err := compensateZoneDraw(nil, "", "page-a", st, "partition", clear, frames, errors.New("draw count mismatch"))
	if err == nil || !strings.Contains(err.Error(), "recovery ids retained") {
		t.Fatalf("compensation did not report retained recovery ids: %v", err)
	}
	got, loadErr := workflow.Load("zone-project")
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	recovery := got.SchZoneFrameIdsByPage["page-a"]
	if recovery == nil || recovery.Mode != "partition" ||
		len(recovery.Rects) != 1 || recovery.Rects[0] != "r1" ||
		len(recovery.Texts) != 1 || recovery.Texts[0] != "t1" {
		t.Fatalf("survivor recovery record was lost: %+v", recovery)
	}
}

func TestRecordedZoneFramesArePageScoped(t *testing.T) {
	st := &pcbStageState{}
	setRecordedZoneFrames(st, "page-a", "zones", &workflow.SchZoneFrames{Rects: []string{"ra"}, Texts: []string{"ta"}})
	setRecordedZoneFrames(st, "page-b", "partition", &workflow.SchZoneFrames{Rects: []string{"rb"}, Texts: []string{"tb"}})
	a, source := recordedZoneFrames(st, "page-a")
	if source != "page" || a.Rects[0] != "ra" || a.Mode != "zones" {
		t.Fatalf("page-a record wrong: source=%q frame=%+v", source, a)
	}
	b, source := recordedZoneFrames(st, "page-b")
	if source != "page" || b.Rects[0] != "rb" || b.Mode != "partition" {
		t.Fatalf("page-b record wrong: source=%q frame=%+v", source, b)
	}
}

// ─── #163: frames must clear the sheet border and the title block ─────

// The A4-landscape numbers are the ones measured in issue #163: the frames sat
// 4 units off the sheet border and the bottom row ran through the title block.
func TestZoneDrawFramesClearSheetBorderAndTitleBlock(t *testing.T) {
	sheet := layoutBBox{MinX: 0, MinY: 0, MaxX: 1170, MaxY: 825}
	keepout := layoutBBox{MinX: 468, MinY: 0, MaxX: 1170, MaxY: 165}
	opts := defaultSchZoneOpts()

	// The full 3×2 grid zone-draw uses by default.
	zones := []string{
		"left-top", "center-top", "right-top",
		"left-bottom", "center-bottom", "right-bottom",
	}
	for _, zone := range zones {
		frame, ok := schZoneFrameRect(zone, sheet, &keepout, opts)
		if !ok {
			t.Errorf("%s: no drawable frame left", zone)
			continue
		}
		if !bboxContains(sheet, frame) {
			t.Errorf("%s: frame %+v escaped the sheet %+v", zone, frame, sheet)
		}
		if frame.MinX < sheet.MinX+opts.Margin || frame.MaxX > sheet.MaxX-opts.Margin ||
			frame.MinY < sheet.MinY+opts.Margin || frame.MaxY > sheet.MaxY-opts.Margin {
			t.Errorf("%s: frame %+v is not inset by the %g margin", zone, frame, opts.Margin)
		}
		if boxesOverlap(frame, keepout) {
			t.Errorf("%s: frame %+v still crosses the title block %+v", zone, frame, keepout)
		}
	}
}

// The label must sit INSIDE its frame, not on the frame line (which on the
// bottom/outer row is also the sheet border).

// A zone whose cell is entirely swallowed by the keep-out must be skipped, not
// drawn as a sliver or a negative-height rectangle.
func TestZoneDrawSkipsFrameFullySwallowedByTitleBlock(t *testing.T) {
	sheet := layoutBBox{MinX: 0, MinY: 0, MaxX: 400, MaxY: 300}
	// A keep-out covering everything below the mid line leaves the bottom row
	// with nothing usable.
	keepout := layoutBBox{MinX: 0, MinY: 0, MaxX: 400, MaxY: 300}
	if _, ok := schZoneFrameRect("right-bottom", sheet, &keepout, defaultSchZoneOpts()); ok {
		t.Error("a frame fully inside the keep-out must not be drawable")
	}
}

func TestZoneFrameAliveSetFailsClosedOnMalformedEntries(t *testing.T) {
	good := map[string]any{"primitiveId": "ok"}
	inv := func(rects, texts any) map[string]any {
		return map[string]any{"pagePrimitives": map[string]any{"rectangles": rects, "texts": texts}}
	}
	for name, result := range map[string]map[string]any{
		"rectangle empty object":     inv([]any{map[string]any{}}, []any{}),
		"rectangle not an object":    inv([]any{"r1"}, []any{}),
		"rectangle null entry":       inv([]any{nil}, []any{}),
		"rectangle id null":          inv([]any{map[string]any{"primitiveId": nil}}, []any{}),
		"rectangle id not a string":  inv([]any{map[string]any{"primitiveId": 7.0}}, []any{}),
		"rectangle id blank":         inv([]any{map[string]any{"primitiveId": "  "}}, []any{}),
		"bad rectangle after a good": inv([]any{good, map[string]any{}}, []any{}),
		"text empty object":          inv([]any{good}, []any{map[string]any{}}),
		"text not an object":         inv([]any{}, []any{42.0}),
		"text id blank":              inv([]any{}, []any{map[string]any{"primitiveId": ""}}),
		"rectangles null":            inv(nil, []any{}),
		"texts not a list":           inv([]any{}, map[string]any{}),
	} {
		if got, err := zoneFrameAliveSet(result); err == nil {
			t.Errorf("%s: accepted as alive set %v", name, got)
		}
	}
	if got, err := zoneFrameAliveSet(inv([]any{}, []any{})); err != nil || len(got) != 0 {
		t.Fatalf("well-formed empty inventory = %v, %v", got, err)
	}

	// The clear must not report success on an inventory it could not read.
	frames := &workflow.SchZoneFrames{Rects: []string{"r1"}, Texts: []string{"t1"}}
	deleted := 0
	alive := func() (map[string]bool, error) { return zoneFrameAliveSet(inv([]any{map[string]any{}}, []any{})) }
	n, err := clearZoneFramesWith(frames, func(string) error { deleted++; return nil }, alive)
	if err == nil || n != 0 || deleted != 0 {
		t.Fatalf("clear on malformed inventory = (%d, %v) with %d deletes, want an error and no deletes", n, err, deleted)
	}
}

// A first inventory that predates the frames must not pass for "already
// absent": the ids that show up on the settle re-read are deleted and counted.
func TestClearZoneFramesWithRereadsIdsMissingFromFirstInventory(t *testing.T) {
	f := &workflow.SchZoneFrames{Rects: []string{"r1"}, Texts: []string{"t1"}}
	reads, deleted := 0, map[string]int{}
	gone := map[string]bool{}
	alive := func() (map[string]bool, error) {
		reads++
		if reads == 1 { // unsettled: the frames are not in the inventory yet
			return map[string]bool{"user": true}, nil
		}
		out := map[string]bool{"user": true}
		for _, id := range []string{"r1", "t1"} {
			if !gone[id] {
				out[id] = true
			}
		}
		return out, nil
	}
	found, err := clearZoneFramesWith(f, func(id string) error { deleted[id]++; gone[id] = true; return nil }, alive)
	if err != nil || found != 2 || deleted["r1"] != 1 || deleted["t1"] != 1 || deleted["user"] != 0 {
		t.Fatalf("late-appearing frames: found=%d err=%v deleted=%v", found, err, deleted)
	}

	// Absent on every read: confirmed absent, nothing deleted, more than one read.
	reads = 0
	found, err = clearZoneFramesWith(f, func(string) error { t.Fatal("deleted an absent id"); return nil },
		func() (map[string]bool, error) { reads++; return map[string]bool{"user": true}, nil })
	if err != nil || found != 0 || reads != settleAttempts {
		t.Fatalf("confirmed-absent clear: found=%d err=%v reads=%d", found, err, reads)
	}

	// Every id present at once: no extra read before deleting.
	reads = 0
	gone = map[string]bool{}
	if _, err = clearZoneFramesWith(f, func(id string) error { gone[id] = true; return nil }, func() (map[string]bool, error) {
		reads++
		return map[string]bool{"r1": !gone["r1"], "t1": !gone["t1"]}, nil
	}); err != nil || reads != 2 {
		t.Fatalf("all-present clear: err=%v reads=%d, want one pre-read and one verify read", err, reads)
	}
}

// When the re-read that should confirm absence fails, absence is unproven: the
// clear errors, and the prior record stays in the workflow state.
func TestClearPriorZoneFramesKeepsRecordWhenAbsenceIsUnconfirmed(t *testing.T) {
	const doc = "doc-1"
	prev := &workflow.SchZoneFrames{Rects: []string{"r1"}, Texts: []string{"t1"}, DocumentUUID: doc}
	st := &pcbStageState{SchZoneFrameIdsByPage: map[string]*workflow.SchZoneFrames{doc: prev}}
	reads := 0
	clear := func(f *workflow.SchZoneFrames, _ string) (int, error) {
		return clearZoneFramesWith(f, func(string) error { t.Fatal("deleted without a confirmed inventory"); return nil },
			func() (map[string]bool, error) {
				reads++
				if reads == 1 {
					return map[string]bool{}, nil
				}
				return nil, errors.New("inventory unavailable")
			})
	}
	cleared, err := clearPriorZoneFrames(st, doc, clear, io.Discard)
	if err == nil || cleared {
		t.Fatalf("unconfirmed absence reported as cleared: cleared=%v err=%v", cleared, err)
	}
	if st.SchZoneFrameIdsByPage[doc] != prev {
		t.Fatalf("recovery record dropped: %+v", st.SchZoneFrameIdsByPage)
	}
}
