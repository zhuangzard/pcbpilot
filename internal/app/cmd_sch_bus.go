package app

// cmd_sch_bus.go — `pcbpilot sch bus list|create|delete|candidates`.
//
// Native buses are sch_PrimitiveBus (@beta in @jlceda/pro-api-types 0.4.25:
// create/delete/modify/get/getAll/getAllPrimitiveId). The extension API has
// NO bus-entry primitive (ESCH_PrimitiveType has Bus and Wire, no BusEntry), so
// a member is tapped with an ordinary orthogonal wire plus a net label/port of
// the member name — the same typed connect paths as any other net.
// Status: live-verified on V3 3.2.149 desktop (2026-10-01); V4 unverified
// (procedure: docs/reviews/2026-10-schematic-aesthetics §5).

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

// busNameConvention is the conventional indexed bus spelling. Advisory only:
// the host's accepted bus-name grammar is not documented in the type package,
// so the CLI warns instead of refusing (live verification pending).
var busNameConvention = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*\[\d+(:|\.\.)\d+\]$`)

// parseBusPolylines reads repeated --points "x1,y1,x2,y2,…" values.
func parseBusPolylines(specs []string) ([][]float64, error) {
	var out [][]float64
	for i, s := range specs {
		var line []float64
		for _, f := range strings.Split(s, ",") {
			f = strings.TrimSpace(f)
			if f == "" {
				continue
			}
			v, err := strconv.ParseFloat(f, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				return nil, fmt.Errorf("--points #%d: %q is not a finite number", i+1, f)
			}
			line = append(line, v)
		}
		out = append(out, line)
	}
	return out, nil
}

// validateBusLine applies the documented sch_PrimitiveBus.create rules fail
// closed: every polyline is x,y pairs with ≥2 points; every segment is
// horizontal or vertical and non-zero (a diagonal segment is illegal, case 1.3);
// polylines must touch each other (disjoint polylines fail, case 1.4). A
// one-point polyline is silently ignored by the host (case 1.5) — rejected here
// so the readback can be compared exactly.
func validateBusLine(lines [][]float64) error {
	if len(lines) == 0 {
		return fmt.Errorf("bus line needs at least one polyline (--points x1,y1,x2,y2,…)")
	}
	pts := make([][]schaes.Pt, len(lines))
	for i, l := range lines {
		if len(l)%2 != 0 {
			return fmt.Errorf("polyline #%d has an odd number of coordinates", i+1)
		}
		if len(l) < 4 {
			return fmt.Errorf("polyline #%d needs ≥2 points (a one-point polyline is dropped by the host)", i+1)
		}
		for k := 0; k+1 < len(l); k += 2 {
			pts[i] = append(pts[i], schaes.Pt{X: l[k], Y: l[k+1]})
		}
		for k := 1; k < len(pts[i]); k++ {
			a, b := pts[i][k-1], pts[i][k]
			if a == b {
				return fmt.Errorf("polyline #%d segment %d has zero length", i+1, k)
			}
			if a.X != b.X && a.Y != b.Y {
				return fmt.Errorf("polyline #%d segment %d is diagonal (%g,%g)→(%g,%g); bus segments must be horizontal or vertical", i+1, k, a.X, a.Y, b.X, b.Y)
			}
		}
	}
	// connectivity between polylines (vertex of one on a segment of another)
	parent := make([]int, len(pts))
	for i := range parent {
		parent[i] = i
	}
	find := func(x int) int {
		for parent[x] != x {
			x = parent[x]
		}
		return x
	}
	touches := func(a, b []schaes.Pt) bool {
		for _, p := range a {
			for k := 1; k < len(b); k++ {
				s, e := b[k-1], b[k]
				if p.X >= math.Min(s.X, e.X) && p.X <= math.Max(s.X, e.X) && p.Y >= math.Min(s.Y, e.Y) && p.Y <= math.Max(s.Y, e.Y) &&
					(s.X == e.X && p.X == s.X || s.Y == e.Y && p.Y == s.Y) {
					return true
				}
			}
		}
		return false
	}
	for i := range pts {
		for j := i + 1; j < len(pts); j++ {
			if touches(pts[i], pts[j]) || touches(pts[j], pts[i]) {
				parent[find(i)] = find(j)
			}
		}
	}
	for i := range pts {
		if find(i) != find(0) {
			return fmt.Errorf("polyline #%d does not touch the rest of the bus (disjoint polylines are refused by the host)", i+1)
		}
	}
	return nil
}

func buildBusCreatePayload(name string, lines [][]float64, color string, width float64) (map[string]any, []string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, fmt.Errorf("--name is required")
	}
	if len([]rune(name)) > 64 {
		return nil, nil, fmt.Errorf("--name longer than 64 characters")
	}
	if err := validateBusLine(lines); err != nil {
		return nil, nil, err
	}
	var warnings []string
	if !busNameConvention.MatchString(name) {
		// Live V3 3.2.149 (2026-10-04): a name without [a:b] makes
		// sch_PrimitiveBus.create return an empty result; refuse before writing.
		return nil, nil, fmt.Errorf("bus name %q must be NAME[a:b] (e.g. D[0:7]): the host rejects other names", name)
	}
	payload := map[string]any{"busName": name}
	if len(lines) == 1 {
		payload["line"] = lines[0]
	} else {
		payload["line"] = lines
	}
	if color != "" {
		if !regexp.MustCompile(`^#[0-9a-fA-F]{6}$`).MatchString(color) {
			return nil, nil, fmt.Errorf("--color must be #RRGGBB")
		}
		payload["color"] = color
	}
	if width != 0 {
		if width < 1 || width > 10 {
			return nil, nil, fmt.Errorf("--line-width must be 1…10 (sch_PrimitiveBus.create range)")
		}
		payload["lineWidth"] = width
	}
	return payload, warnings, nil
}

func printDryRun(w io.Writer, action string, payload map[string]any, warnings []string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(map[string]any{"dryRun": true, "action": action, "payload": payload, "warnings": warnings,
		"note": "validated offline; nothing was sent to the daemon"})
}

func newSchBusCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	bus := &cobra.Command{
		Use:   "bus",
		Short: "Native schematic buses (sch_PrimitiveBus, @beta; live-verified on V3 3.2.149, V4 unverified) and offline bus-candidate detection",
		Long: "原理图总线（官方 sch_PrimitiveBus，@beta；V3 3.2.149 现场已验证，V4 未验证，见 docs/FEATURES.md）。\n\n" +
			"  list        只读：当前页全部总线（primitiveId/busName/line/color/lineWidth/lineType）\n" +
			"  create      建一条总线（正交多段线，互相连通），写后回读名字与路径；--dry-run 只做离线校验\n" +
			"  delete      按 ID 删总线并回读确认；--dry-run 只打印计划\n" +
			"  candidates  离线：从快照找索引网/SPI/I2C/UART/SDIO/MIPI/USB 组，并评估其标签泳道\n" +
			"  apply       按布局计划建总线（成员标签须已存在；日志记 ID、按 ID+几何替换自己建的、永不删用户总线）\n" +
			"  check       成员检查：名字对应的成员网在引脚上且有自己的标签/端口（error 时非零退出）\n\n" +
			"布局生成（sch layout-plan / lib-layout --aesthetics balanced|precision）默认为完整标签泳道画原生总线\n" +
			"（layout.buses）；sch compose --playbook 在 wire-tree 检查后追加 sch bus apply。\n\n" +
			"扩展 API 没有总线分支（bus entry）图元：成员接入用普通正交导线 + 成员名网络标签/端口\n" +
			"（connect_pin / autoconnect）。总线本身不建立电气连接的证据；成员网的连通仍以逐 pin\n" +
			"网表和 sch check 为准。宿主不可用时退回「虚拟总线」：同组标签同列、等距、同向。",
	}
	// list
	{
		c := &cobra.Command{
			Use:   "list",
			Short: "List native buses on the active page (read-only)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				return dispatch(cfg, "schematic.bus.list", *window, map[string]any{}, stdout, stderr)
			},
		}
		bus.AddCommand(c)
	}
	// create
	{
		var name, color string
		var points []string
		var width float64
		var dry bool
		c := &cobra.Command{
			Use:   "create",
			Short: "Create one native bus from orthogonal polylines (verified by readback; --dry-run validates offline)",
			Args:  cobra.NoArgs,
			Example: "  pcbpilot sch bus create --name 'D[0:7]' --points 400,600,400,300 --dry-run\n" +
				"  pcbpilot sch bus create --name 'D[0:7]' --points 400,600,400,300 --points 400,300,700,300 --project P --doc <page>",
			RunE: func(cmd *cobra.Command, _ []string) error {
				lines, err := parseBusPolylines(points)
				if err != nil {
					return err
				}
				payload, warnings, err := buildBusCreatePayload(name, lines, color, width)
				if err != nil {
					return err
				}
				if dry {
					return printDryRun(stdout, "schematic.bus.create", payload, warnings)
				}
				for _, w := range warnings {
					fmt.Fprintln(stderr, "warning: "+w)
				}
				return dispatch(cfg, "schematic.bus.create", *window, payload, stdout, stderr)
			},
		}
		c.Flags().StringVar(&name, "name", "", "bus name, conventionally NAME[a:b] (e.g. D[0:7])")
		c.Flags().StringArrayVar(&points, "points", nil, "one polyline as x1,y1,x2,y2,… (repeat for branches; branches must touch)")
		c.Flags().StringVar(&color, "color", "", "#RRGGBB (default: host default)")
		c.Flags().Float64Var(&width, "line-width", 0, "1…10 (default: host default)")
		c.Flags().BoolVar(&dry, "dry-run", false, "validate and print the payload; do not contact the daemon")
		bus.AddCommand(c)
	}
	// delete
	{
		var ids string
		var dry bool
		c := &cobra.Command{
			Use:   "delete",
			Short: "Delete native buses by primitive id (verified by readback)",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				list, err := parseIDList(ids)
				if err != nil {
					return err
				}
				if len(list) == 0 {
					return fmt.Errorf("--ids is required")
				}
				payload := map[string]any{"primitiveIds": list}
				if dry {
					return printDryRun(stdout, "schematic.bus.delete", payload, nil)
				}
				return dispatch(cfg, "schematic.bus.delete", *window, payload, stdout, stderr)
			},
		}
		c.Flags().StringVar(&ids, "ids", "", "bus primitive ids, CSV")
		c.Flags().BoolVar(&dry, "dry-run", false, "print the payload; do not contact the daemon")
		bus.AddCommand(c)
	}
	bus.AddCommand(newSchBusApplyCmd(cfg, window, stdout, stderr))
	bus.AddCommand(newSchBusCheckCmd(cfg, window, stdout, stderr))
	// candidates (offline)
	{
		var snapshot string
		var asJSON bool
		c := &cobra.Command{
			Use:   "candidates",
			Short: "Offline: find bus / virtual-bus candidate groups in a page snapshot and judge their label lanes",
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if snapshot == "" {
					return fmt.Errorf("--snapshot is required (sch list / layout-plan / lib-layout / canonical JSON)")
				}
				raw, err := os.ReadFile(snapshot)
				if err != nil {
					return err
				}
				snap, err := schaes.Parse(raw)
				if err != nil {
					return err
				}
				rep := schaes.Analyze(snap, nil)
				if asJSON {
					enc := json.NewEncoder(stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(map[string]any{"candidates": rep.BusCandidates, "lanes": rep.Lanes})
				}
				if len(rep.BusCandidates) == 0 {
					fmt.Fprintln(stdout, "no bus candidates on this page")
					return nil
				}
				for _, l := range rep.Lanes {
					fmt.Fprintf(stdout, "%-8s %-20s %s\n         members: %s\n         lane: %s\n", l.Candidate.Kind, l.Candidate.Suggested, l.Candidate.Why,
						strings.Join(l.Candidate.Members, ", "), l.Note)
				}
				return nil
			},
		}
		c.Flags().StringVar(&snapshot, "snapshot", "", "page snapshot JSON")
		c.Flags().BoolVar(&asJSON, "json", false, "JSON output")
		bus.AddCommand(c)
	}
	return bus
}
