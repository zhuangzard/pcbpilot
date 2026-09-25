package app

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// sch groups: schematic module frames → PCB placement groups.
//
// A designer draws a (dashed) rectangle around each functional module — a core
// with its decoupling, crystal, regulator parts. That drawing is the ownership
// the PCB placer needs: `pcb auto run --groups` freezes each core with its
// module's critical auxiliaries (two-stage placement) and resolves shared-rail
// ambiguity (a buck's output cap is not the MCU's decoupler). This command
// reads the frames from ANY live schematic — not only pages pcbpilot composed —
// and writes the groups file.

type schGroupRect struct{ MinX, MinY, MaxX, MaxY float64 }

func (r schGroupRect) contains(x, y float64) bool {
	return x >= r.MinX && x <= r.MaxX && y >= r.MinY && y <= r.MaxY
}
func (r schGroupRect) area() float64 { return (r.MaxX - r.MinX) * (r.MaxY - r.MinY) }

type schGroupComp struct {
	Designator string
	Box        schGroupRect
}

type schGroupText struct {
	Content string
	X, Y    float64
}

type schGroupPage struct {
	Page  string
	Rects []schGroupRect
	Texts []schGroupText
	Comps []schGroupComp
}

type schGroupOut struct {
	ID      string       `json:"id"`
	Page    string       `json:"page"`
	Members []string     `json:"members"`
	Rect    schGroupRect `json:"rect"`
}

type schGroupsFile struct {
	SchemaVersion int           `json:"schemaVersion"`
	Source        string        `json:"source"`
	Groups        []schGroupOut `json:"groups"`
	Unframed      []string      `json:"unframed,omitempty"`
}

// buildSchGroups assigns every component to the SMALLEST frame containing its
// bbox centre (nested frames: the inner module wins), names each frame by the
// text nearest its top-left corner (inside, or up to titleReach above the top
// edge), and drops frames with fewer than minMembers parts (sheet borders,
// title blocks and decorative boxes contain either everything or nothing).
func buildSchGroups(pages []schGroupPage, minMembers int) schGroupsFile {
	const titleReach = 60.0
	out := schGroupsFile{SchemaVersion: 1, Source: "schematic frames"}
	used := map[string]int{}
	for pi, pg := range pages {
		total := len(pg.Comps)
		members := make([][]string, len(pg.Rects))
		for _, c := range pg.Comps {
			cx, cy := (c.Box.MinX+c.Box.MaxX)/2, (c.Box.MinY+c.Box.MaxY)/2
			best, bestArea := -1, math.Inf(1)
			for i, r := range pg.Rects {
				if r.contains(cx, cy) && r.area() < bestArea {
					best, bestArea = i, r.area()
				}
			}
			if best < 0 {
				out.Unframed = append(out.Unframed, c.Designator)
				continue
			}
			members[best] = append(members[best], c.Designator)
		}
		for i, r := range pg.Rects {
			m := members[i]
			// A frame holding (nearly) the whole page is a border, not a module.
			if len(m) < minMembers || (total >= 6 && len(m) >= total*9/10) {
				out.Unframed = append(out.Unframed, m...)
				continue
			}
			sort.Strings(m)
			id, bd := "", math.Inf(1)
			for _, t := range pg.Texts {
				if strings.TrimSpace(t.Content) == "" || t.X < r.MinX-1 || t.X > r.MaxX || t.Y < r.MinY || t.Y > r.MaxY+titleReach {
					continue
				}
				if d := math.Hypot(t.X-r.MinX, t.Y-r.MaxY); d < bd {
					id, bd = strings.TrimSpace(t.Content), d
				}
			}
			if id == "" {
				id = fmt.Sprintf("P%d-F%d", pi+1, i+1)
			}
			if used[id] > 0 {
				id = fmt.Sprintf("%s#%d", id, used[id]+1)
			}
			used[strings.SplitN(id, "#", 2)[0]]++
			out.Groups = append(out.Groups, schGroupOut{ID: id, Page: pg.Page, Members: m, Rect: r})
		}
	}
	sort.Strings(out.Unframed)
	return out
}

func newSchGroupsCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var pages []string
	var outPath string
	var minMembers int
	c := &cobra.Command{
		Use:   "groups",
		Short: "Read module frames from live schematic pages → PCB placement groups (read-only)",
		Long: `Read every rectangle frame, text and component bbox on the given schematic
pages and assign each component to the smallest frame around its centre; the
frame's title (text nearest its top-left corner) names the group. The output
is the groups file for 'pcb auto run --groups', which keeps each module's
critical parts (decaps, crystal + loads, regulator stage, pin filters) frozen
to their core during placement. Works on any schematic, not only pages
pcbpilot composed. Frames with fewer than --min-members parts, or holding
~the whole page (borders), are ignored; unframed parts are listed.`,
		Example: `  pcbpilot sch groups --project ceshi --pages <page1>,<page2> --out groups.json
  pcbpilot pcb auto run --board board.json --groups groups.json --place --out-dir out/`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(pages) == 0 {
				return fmt.Errorf("--pages is required (schematic page uuids or names; see 'pcbpilot sch pages')")
			}
			var in []schGroupPage
			for _, page := range pages {
				scope, err := switchToPage(cfg, *window, page)
				if err != nil {
					return err
				}
				pg, err := readSchGroupPage(cfg, scope.window, page)
				_ = scope.restore(cfg)
				if err != nil {
					return fmt.Errorf("page %s: %w", page, err)
				}
				in = append(in, pg)
			}
			res := buildSchGroups(in, minMembers)
			raw, _ := json.MarshalIndent(res, "", "  ")
			if outPath != "" {
				if err := os.WriteFile(outPath, append(raw, '\n'), 0o644); err != nil {
					return err
				}
				fmt.Fprintf(stderr, "sch groups: %d group(s), %d unframed part(s) → %s\n", len(res.Groups), len(res.Unframed), outPath)
				return nil
			}
			_, err := stdout.Write(append(raw, '\n'))
			return err
		},
	}
	c.Flags().StringSliceVar(&pages, "pages", nil, "schematic pages to read (uuid or name, comma-separated)")
	c.Flags().StringVar(&outPath, "out", "", "write the groups JSON here (default stdout)")
	c.Flags().IntVar(&minMembers, "min-members", 2, "ignore frames holding fewer parts than this")
	return c
}

func readSchGroupPage(cfg *appConfig, window, page string) (schGroupPage, error) {
	pg := schGroupPage{Page: page}
	cres, err := requestAction(cfg, "schematic.components.list", window, map[string]any{"includeBBox": true})
	if err != nil {
		return pg, err
	}
	comps, _ := cres.Result["components"].([]any)
	for _, raw := range comps {
		m, _ := raw.(map[string]any)
		d := asString(m["designator"])
		bb, ok := m["bbox"].(map[string]any)
		if d == "" || !ok {
			continue
		}
		minX, _ := asFloatOK(bb["minX"])
		minY, _ := asFloatOK(bb["minY"])
		maxX, _ := asFloatOK(bb["maxX"])
		maxY, _ := asFloatOK(bb["maxY"])
		pg.Comps = append(pg.Comps, schGroupComp{Designator: d, Box: schGroupRect{minX, minY, maxX, maxY}})
	}
	rres, err := requestAction(cfg, "schematic.rectangles.list", window, nil)
	if err != nil {
		return pg, fmt.Errorf("%w (needs connector ≥ 0.2.9: schematic.rectangles.list)", err)
	}
	rects, _ := rres.Result["rectangles"].([]any)
	for _, raw := range rects {
		m, _ := raw.(map[string]any)
		x, _ := asFloatOK(m["x"])
		y, _ := asFloatOK(m["y"])
		w, _ := asFloatOK(m["width"])
		h, _ := asFloatOK(m["height"])
		if w <= 0 || h <= 0 {
			continue
		}
		top := schFrameRectTopY(y)
		pg.Rects = append(pg.Rects, schGroupRect{x, top - h, x + w, top})
	}
	tres, err := requestAction(cfg, "schematic.text.list", window, nil)
	if err != nil {
		return pg, err
	}
	texts, _ := tres.Result["texts"].([]any)
	for _, raw := range texts {
		m, _ := raw.(map[string]any)
		x, _ := asFloatOK(m["x"])
		y, _ := asFloatOK(m["y"])
		pg.Texts = append(pg.Texts, schGroupText{Content: asString(m["content"]), X: x, Y: y})
	}
	return pg, nil
}
