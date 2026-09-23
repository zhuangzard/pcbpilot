package app

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/workflow"
)

// Keep every SDK field in the survey. Missing fields must fail comparison,
// rather than decoding to zero (which would accidentally verify rotation=0).
type schFrameSurvey struct {
	Rectangles map[string]map[string]any
	Texts      map[string]map[string]any
}

// Frame ownership is keyed by the live project UUID, irrespective of whether
// --project was supplied as a name or UUID. Legacy workflow readers can still
// see that receipt without duplicating it into their name-keyed state file.
func recordedModuleFrameCount(st *pcbStageState, doc string) int {
	if st == nil {
		return 0
	}
	if st.ProjectUUID != "" && st.ProjectUUID != st.Project {
		canonical, err := loadPcbStageState(st.ProjectUUID)
		if err == nil {
			return canonical.SchModuleFrameCount(doc)
		}
	}
	return st.SchModuleFrameCount(doc)
}

func buildSchFrameSurveyJS() string {
	return `const rectangles = await eda.sch_PrimitiveRectangle.getAll();
const texts = await eda.sch_PrimitiveText.getAll();
if (!Array.isArray(rectangles) || !Array.isArray(texts)) throw new Error('graphics inventory unavailable');
const rs = rectangles.map(r => ({id:r.getState_PrimitiveId(),x:r.getState_TopLeftX(),y:r.getState_TopLeftY(),width:r.getState_Width(),height:r.getState_Height(),rotation:r.getState_Rotation(),cornerRadius:r.getState_CornerRadius(),color:r.getState_Color(),lineWidth:r.getState_LineWidth(),lineType:r.getState_LineType(),fillStyle:r.getState_FillStyle(),fillColor:r.getState_FillColor()}));
const ts = [];
for (const t of texts) {
  const id=t.getState_PrimitiveId();
  ts.push({id,content:t.getState_Content(),x:t.getState_X(),y:t.getState_Y(),rotation:t.getState_Rotation(),color:t.getState_TextColor(),fontSize:t.getState_FontSize(),alignMode:t.getState_AlignMode(),bold:t.getState_Bold(),italic:t.getState_Italic(),underLine:t.getState_UnderLine(),bbox:await eda.sch_Primitive.getPrimitivesBBox([id])});
}
return {rectangles:rs,texts:ts};`
}

// schFrameRectTopY normalises the y that sch_PrimitiveRectangle reports back.
//
// Measured on EasyEDA Pro 3.2.149: create() takes the visual TOP-LEFT corner on
// the y-UP canvas - the same (MinX, MaxY) writeZoneRectangleCreateJS passes -
// but getState_TopLeftY() mirrors it about y=0, so a frame drawn at MaxY=813
// reads back as -813. x/width/height round-trip unchanged, and both
// sch_PrimitiveText and sch_PrimitiveComponent report y unmirrored, so the
// asymmetry is confined to this one getter.
//
// A schematic sheet occupies y>=0, so a rectangle whose top edge sits at y<0 is
// not a position a frame can actually hold: mirroring exactly then repairs the
// affected builds and leaves a build that round-trips y byte-for-byte alone.
func schFrameRectTopY(y float64) float64 {
	if y < 0 {
		return -y
	}
	return y
}

func parseSchFrameSurvey(v map[string]any) (schFrameSurvey, error) {
	s := schFrameSurvey{Rectangles: map[string]map[string]any{}, Texts: map[string]map[string]any{}}
	for key, target := range map[string]map[string]map[string]any{"rectangles": s.Rectangles, "texts": s.Texts} {
		list, ok := v[key].([]any)
		if !ok {
			return s, fmt.Errorf("%s inventory missing", key)
		}
		for _, item := range list {
			m, ok := item.(map[string]any)
			id, _ := m["id"].(string)
			if !ok || id == "" || target[id] != nil {
				return s, fmt.Errorf("invalid/duplicate %s identity", key)
			}
			// Survey rectangles reach every consumer in y-UP sheet coordinates,
			// so the frame bbox a check builds and the bbox a component reports
			// are comparable without a per-call-site flip.
			if y, isNum := m["y"].(float64); key == "rectangles" && isNum && plFinite(y) {
				m["y"] = schFrameRectTopY(y)
			}
			target[id] = m
		}
	}
	return s, nil
}

func schFrameNumbers(m map[string]any, expected map[string]float64) error {
	for _, k := range sortedStateKeys(expected) {
		v, ok := m[k].(float64)
		if !ok || !plFinite(v) || math.Abs(v-expected[k]) > 1e-6 {
			return fmt.Errorf("%s: got %v, expected %g", k, m[k], expected[k])
		}
	}
	return nil
}

func matchSchFrameRectangle(f schFrameSpec, m map[string]any) error {
	if m == nil {
		return fmt.Errorf("rectangle missing")
	}
	if err := schFrameNumbers(m, map[string]float64{"x": f.Rect.MinX, "y": f.Rect.MaxY, "width": f.Rect.MaxX - f.Rect.MinX, "height": f.Rect.MaxY - f.Rect.MinY, "rotation": 0, "cornerRadius": 0, "lineWidth": 1, "lineType": float64(f.LineType)}); err != nil {
		return err
	}
	color, ok := m["color"].(string)
	// 3.2.186 returns null fillStyle even when create received "None".
	// An explicit "none" fill color proves transparency on that build.
	noFill := m["fillStyle"] == "None" || (m["fillStyle"] == nil && m["fillColor"] == "none")
	if !ok || !strings.EqualFold(color, f.Color) || !noFill {
		return fmt.Errorf("rectangle color/fill mismatch: %v/%v", m["color"], m["fillStyle"])
	}
	return nil
}

func matchSchFrameText(f schFrameSpec, m map[string]any) error {
	if m == nil {
		return fmt.Errorf("title missing")
	}
	if err := schFrameNumbers(m, map[string]float64{"x": f.TitleX, "y": f.TitleY, "rotation": 0, "fontSize": f.FontSize}); err != nil {
		return err
	}
	color, ok := m["color"].(string)
	if !ok || !strings.EqualFold(color, f.Color) || m["content"] != f.Title {
		return fmt.Errorf("title content/color mismatch")
	}
	for _, k := range []string{"bold", "italic", "underLine"} {
		if v, ok := m[k].(bool); !ok || v {
			return fmt.Errorf("title %s mismatch/unavailable", k)
		}
	}
	bb, ok := m["bbox"].(map[string]any)
	if !ok {
		return fmt.Errorf("title rendered bbox unavailable")
	}
	coords := []float64{}
	for _, k := range []string{"minX", "minY", "maxX", "maxY"} {
		n, ok := bb[k].(float64)
		if !ok || !plFinite(n) {
			return fmt.Errorf("title bbox %s unavailable", k)
		}
		coords = append(coords, n)
	}
	b := layoutBBox{MinX: coords[0], MinY: coords[1], MaxX: coords[2], MaxY: coords[3]}
	if !plBoxValid(b) || !boxInside(b, f.Rect) {
		return fmt.Errorf("rendered title outside module frame: %+v", b)
	}
	if err := checkSchFrameTitleOccupancy(f, b); err != nil {
		return err
	}
	// EasyEDA 3.2.186 stores alignMode=2 after requesting LEFT_TOP=1, yet
	// renders with exactly the requested top-left boundary. Verify the visual
	// coordinate contract, not that unreliable enum echo.
	if math.Abs(b.MinX-f.TitleX) > 1e-6 || math.Abs(b.MaxY-f.TitleY) > 1e-6 {
		return fmt.Errorf("rendered title is not at the planned top-left anchor: %+v", b)
	}
	return nil
}

func matchSchFrame(f schFrameSpec, r *workflow.SchModuleFrame, s schFrameSurvey) error {
	if r == nil || r.RectangleID == "" || r.TextID == "" {
		return fmt.Errorf("frame has no complete ownership receipt")
	}
	if err := matchSchFrameRectangle(f, s.Rectangles[r.RectangleID]); err != nil {
		return fmt.Errorf("rectangle %s: %w", r.RectangleID, err)
	}
	if err := matchSchFrameText(f, s.Texts[r.TextID]); err != nil {
		return fmt.Errorf("text %s: %w", r.TextID, err)
	}
	return nil
}

func schFrameInventoryIDs(m map[string]map[string]any) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Recover a lost response only when both complete target primitives are unique
// and were absent before this specific write. Partial/ambiguous writes stop.
func recoverSchFrame(f schFrameSpec, r *workflow.SchModuleFrame, s schFrameSurvey) (*workflow.SchModuleFrame, error) {
	if r.PlanHash != f.hash() {
		return nil, fmt.Errorf("pending write belongs to another plan; recover the original data first")
	}
	before := map[string]bool{}
	for _, id := range r.BeforeRectangles {
		before[id] = true
	}
	for _, id := range r.BeforeTexts {
		before[id] = true
	}
	var rects, texts []string
	for id, m := range s.Rectangles {
		if !before[id] && matchSchFrameRectangle(f, m) == nil {
			rects = append(rects, id)
		}
	}
	for id, m := range s.Texts {
		if !before[id] && matchSchFrameText(f, m) == nil {
			texts = append(texts, id)
		}
	}
	if len(rects) != 1 || len(texts) != 1 {
		return nil, fmt.Errorf("unresolved write: %d matching new rectangles/%d titles; no write retried", len(rects), len(texts))
	}
	return &workflow.SchModuleFrame{RectangleID: rects[0], TextID: texts[0], PlanHash: f.hash()}, nil
}

func buildSchFrameCreateJS(f schFrameSpec) string {
	raw, _ := json.Marshal(f)
	return `const f=` + string(raw) + `;
let rectangleId='',textId='';
try {
 const r=await eda.sch_PrimitiveRectangle.create(f.rect.minX,f.rect.maxY,f.rect.maxX-f.rect.minX,f.rect.maxY-f.rect.minY,0,0,f.color,'none',1,f.lineType,'None');
 if(!r) throw new Error('rectangle create returned undefined');
 rectangleId=r.getState_PrimitiveId(); if(!rectangleId) throw new Error('rectangle id unavailable');
 const t=await eda.sch_PrimitiveText.create(f.titleX,f.titleY,f.title,0,f.color,null,f.fontSize,false,false,false,1);
 if(!t) throw new Error('title create returned undefined');
 textId=t.getState_PrimitiveId(); if(!textId) throw new Error('title id unavailable');
 return {rectangleId,textId};
} catch(e) {return {rectangleId,textId,error:String(e)};}`
}

type schFrameApplyResult struct {
	Verified   bool             `json:"verified"`
	Applied    int              `json:"applied"`
	Unchanged  int              `json:"unchanged"`
	DocumentID string           `json:"documentId"`
	Frames     []map[string]any `json:"frames"`
}

func runSchFrameDocument(cfg *appConfig, window string, p schFrameDocument, apply bool) (schFrameApplyResult, error) {
	out := schFrameApplyResult{DocumentID: p.DocumentID, Frames: []map[string]any{}}
	if err := p.validate(); err != nil {
		return out, err
	}
	if cfg.doc != "" && cfg.doc != p.DocumentID {
		return out, fmt.Errorf("frame document conflicts with --doc")
	}
	local := *cfg
	local.doc = p.DocumentID
	pinned, win, doc, err := pinZonePage(&local, window)
	if err != nil {
		return out, err
	}
	if doc != p.DocumentID {
		return out, fmt.Errorf("active document does not match frame data")
	}
	_, project, err := resolveStageIdentityLive(pinned, win)
	if err != nil {
		return out, err
	}
	if project == "" {
		return out, fmt.Errorf("frame ownership requires the live project UUID")
	}
	st, err := loadPcbStageState(project)
	if err != nil {
		return out, err
	}
	st.Bind(project)
	exec := func(phase, code string) (map[string]any, error) {
		return execAutolayoutZoneJS(pinned, win, doc, phase, code)
	}
	survey := func() (schFrameSurvey, error) {
		v, e := exec("read module frame geometry/styles", buildSchFrameSurveyJS())
		if e != nil {
			return schFrameSurvey{}, e
		}
		return parseSchFrameSurvey(v)
	}
	s, err := survey()
	if err != nil {
		return out, err
	}
	for _, f := range p.Frames {
		r := st.SchModuleFramesByPage[doc][f.ID]
		if r != nil && r.Pending {
			r, err = recoverSchFrame(f, r, s)
			if err != nil {
				return out, fmt.Errorf("frame %s: %w", f.ID, err)
			}
			if apply {
				st.SetSchModuleFrame(doc, f.ID, r)
				if err = savePcbStageState(st); err != nil {
					return out, err
				}
			}
		}
		if matchErr := matchSchFrame(f, r, s); matchErr == nil {
			out.Unchanged++
		} else {
			if !apply {
				if r != nil {
					out.Frames = append(out.Frames, map[string]any{"id": f.ID, "expected": f, "rectangle": s.Rectangles[r.RectangleID], "title": s.Texts[r.TextID]})
				}
				return out, fmt.Errorf("frame %s: %w", f.ID, matchErr)
			}
			// Only the recorded rectangle/text classes can be removed. A wrong
			// class or stale inventory is never permission to delete a part.
			if r != nil {
				var ids []string
				for _, pair := range []struct {
					id         string
					own, other map[string]map[string]any
				}{{r.RectangleID, s.Rectangles, s.Texts}, {r.TextID, s.Texts, s.Rectangles}} {
					if pair.id == "" {
						continue
					}
					if pair.other[pair.id] != nil {
						return out, fmt.Errorf("owned primitive %s changed type", pair.id)
					}
					if pair.own[pair.id] != nil {
						ids = append(ids, pair.id)
					}
				}
				if len(ids) > 0 {
					data, _ := json.Marshal(ids)
					_, deleteErr := exec("replace owned module frame", `for(const id of `+string(data)+`) await eda.sch_PrimitiveObject.delete([id]); return {done:true};`)
					s, err = survey()
					if err != nil {
						return out, err
					}
					for _, id := range ids {
						if s.Rectangles[id] != nil || s.Texts[id] != nil {
							return out, fmt.Errorf("owned primitive %s survived deletion (%v)", id, deleteErr)
						}
					}
				}
			}
			// Save the write intent BEFORE dispatch. Even a transport timeout
			// now leaves an inventory against which a full rerun can reconcile.
			r = &workflow.SchModuleFrame{PlanHash: f.hash(), Pending: true, BeforeRectangles: schFrameInventoryIDs(s.Rectangles), BeforeTexts: schFrameInventoryIDs(s.Texts)}
			st.SetSchModuleFrame(doc, f.ID, r)
			if err = savePcbStageState(st); err != nil {
				return out, err
			}
			v, writeErr := exec("create module frame/title", buildSchFrameCreateJS(f))
			if writeErr == nil {
				r.RectangleID, _ = v["rectangleId"].(string)
				r.TextID, _ = v["textId"].(string)
				// Known partial results retain their IDs for the next full run.
				r.Pending = false
				if err = savePcbStageState(st); err != nil {
					return out, err
				}
				if failure, _ := v["error"].(string); failure != "" {
					return out, fmt.Errorf("frame %s partially created: %s (receipt saved)", f.ID, failure)
				}
			}
			s, err = survey()
			if err != nil {
				return out, err
			}
			if writeErr != nil {
				r, err = recoverSchFrame(f, r, s)
				if err != nil {
					return out, fmt.Errorf("%v; %w", writeErr, err)
				}
			}
			if err = matchSchFrame(f, r, s); err != nil {
				out.Frames = append(out.Frames, map[string]any{"id": f.ID, "expected": f, "rectangle": s.Rectangles[r.RectangleID], "title": s.Texts[r.TextID]})
				return out, fmt.Errorf("frame %s readback failed: %w", f.ID, err)
			}
			st.SetSchModuleFrame(doc, f.ID, r)
			if err = savePcbStageState(st); err != nil {
				return out, err
			}
			out.Applied++
		}
		out.Frames = append(out.Frames, map[string]any{"id": f.ID, "rectangle": s.Rectangles[r.RectangleID], "title": s.Texts[r.TextID]})
	}
	if apply {
		if err = saveZoneDocument(pinned, win, doc, "save verified module frames"); err != nil {
			return out, err
		}
	}
	out.Verified = true
	return out, nil
}
