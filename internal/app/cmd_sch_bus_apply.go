package app

// cmd_sch_bus_apply.go — `pcbpilot sch bus apply` and `sch bus check`.
//
// apply creates the native buses of a layout plan (result/module/compose
// "buses", official encoding) AFTER every member label exists, reads them
// back as undirected segment sets (the host returns several branches as one
// out-and-back flat path), and journals the primitive IDs. A re-run replaces
// exactly the buses it created earlier — matched by journal ID AND name +
// geometry — and never duplicates them; a bus that is not in the journal is
// the user's and is never touched (an identical user bus is not duplicated
// either). --rollback deletes the journalled buses by exact ID.
//
// Host gate: system.api.probe for sch_PrimitiveBus.create/getAll/get/delete.
// Absent → fallback: no writes, the plan's member labels already form the
// virtual bus lane. V3 3.2.x → live-verified; V4 / other → created and read
// back but reported host-unverified.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

// schBusJournal is the per-page record of buses pcbpilot created.
type schBusJournal struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Project       string               `json:"project,omitempty"`
	Doc           string               `json:"doc,omitempty"`
	UpdatedAt     string               `json:"updatedAt,omitempty"`
	Buses         []SchematicNativeBus `json:"buses"`
}

var schBusAPIPaths = []string{"sch_PrimitiveBus.create", "sch_PrimitiveBus.getAll", "sch_PrimitiveBus.get", "sch_PrimitiveBus.delete"}

// schBusHost is the editor side of bus apply (live: daemon actions; tests:
// a fake host).
type schBusHost interface {
	Probe(paths []string) (members map[string]string, hostVersion string, err error)
	// Page returns the active page (pins, markers, wires, buses) and its
	// project/document ids.
	Page() (snap *schaes.Snapshot, project, doc string, err error)
	Create(payload map[string]any) (map[string]any, error)
	Delete(ids []string) (map[string]any, error)
}

type schBusApplyReport struct {
	Status          string               `json:"status"` // applied | rolled-back | fallback-virtual-bus | dry-run
	HostVersion     string               `json:"hostVersion,omitempty"`
	Verification    string               `json:"verification,omitempty"`
	Journal         string               `json:"journal,omitempty"`
	Deleted         []string             `json:"deleted"`
	Created         []SchematicNativeBus `json:"created"`
	NotDuplicated   []string             `json:"notDuplicated,omitempty"`
	UserBusesKept   []string             `json:"userBusesKept"`
	PrunedJournal   []string             `json:"prunedJournal,omitempty"`
	MemberChecks    []schaes.BusCheck    `json:"memberChecks,omitempty"`
	Creates         []map[string]any     `json:"creates,omitempty"` // dry-run payloads
	Warnings        []string             `json:"warnings,omitempty"`
	ConnectivityRef string               `json:"connectivity"`
}

const schBusConnectivityNote = "a native bus is drawing only: member connectivity comes from each member's own wire + label/port (per-pin netlist, sch check); the bus is never evidence"

func busFromLive(b schaes.Bus) SchematicNativeBus {
	out := SchematicNativeBus{PrimitiveID: b.ID, BusName: b.Name}
	for _, l := range b.Pts {
		var flat []float64
		for _, q := range l {
			flat = append(flat, q.X, q.Y)
		}
		out.Line = append(out.Line, flat)
	}
	return out
}

func busToSchaes(b SchematicNativeBus) schaes.Bus {
	out := schaes.Bus{ID: b.PrimitiveID, Name: b.BusName}
	for _, l := range b.Line {
		var pts []schaes.Pt
		for j := 0; j+1 < len(l); j += 2 {
			pts = append(pts, schaes.Pt{X: l[j], Y: l[j+1]})
		}
		out.Pts = append(out.Pts, pts)
	}
	return out
}

func schBusCreatePayload(b SchematicNativeBus) map[string]any {
	payload := map[string]any{"busName": b.BusName}
	if len(b.Line) == 1 {
		payload["line"] = b.Line[0]
	} else {
		payload["line"] = b.Line
	}
	return payload
}

func defaultSchBusJournal(project, doc string) string {
	dir, ok := artifactOutputDir()
	if !ok {
		dir = "."
	}
	key := journalNameSanitizer.ReplaceAllString(project+"_"+doc, "_")
	return filepath.Join(dir, ".pcbpilot", "bus-journal", key+".json")
}

func readSchBusJournal(path string) (*schBusJournal, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &schBusJournal{SchemaVersion: 1}, nil
	}
	if err != nil {
		return nil, err
	}
	var j schBusJournal
	if err := json.Unmarshal(raw, &j); err != nil {
		return nil, fmt.Errorf("bus journal %s: %w", path, err)
	}
	if j.SchemaVersion != 1 {
		return nil, fmt.Errorf("bus journal %s: unsupported schemaVersion %d", path, j.SchemaVersion)
	}
	return &j, nil
}

func writeSchBusJournal(path string, j *schBusJournal) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	j.SchemaVersion = 1
	j.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	if j.Buses == nil {
		j.Buses = []SchematicNativeBus{}
	}
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// schBusClassification sorts the live buses against the journal and plan.
type schBusClassification struct {
	Replace   []string             // journalled + unchanged on the page: ours, delete by ID
	Pruned    []string             // journalled but gone from the page
	User      []SchematicNativeBus // not journalled: never touched
	Identical map[int]string       // plan index → identical user bus id (not duplicated)
}

func classifySchBuses(live []SchematicNativeBus, j *schBusJournal, plan []SchematicNativeBus) (*schBusClassification, error) {
	c := &schBusClassification{Identical: map[int]string{}}
	byID := map[string]SchematicNativeBus{}
	for _, b := range live {
		byID[b.PrimitiveID] = b
	}
	journalled := map[string]bool{}
	for _, e := range j.Buses {
		journalled[e.PrimitiveID] = true
		b, ok := byID[e.PrimitiveID]
		if !ok {
			c.Pruned = append(c.Pruned, e.PrimitiveID)
			continue
		}
		if b.BusName != e.BusName || !schSameBusLine(b.Line, e.Line) {
			return nil, fmt.Errorf("journalled bus %s (%s) was changed on the page (name/geometry differ from the journal): left untouched — inspect it, delete it yourself or remove it from the journal, then re-run", e.PrimitiveID, e.BusName)
		}
		c.Replace = append(c.Replace, e.PrimitiveID)
	}
	for _, b := range live {
		if !journalled[b.PrimitiveID] {
			c.User = append(c.User, b)
		}
	}
	for i, p := range plan {
		for _, u := range c.User {
			if strings.EqualFold(u.BusName, p.BusName) && schSameBusLine(u.Line, p.Line) {
				c.Identical[i] = u.PrimitiveID
			}
		}
	}
	return c, nil
}

func schBusVerification(hostVersion string) string {
	switch {
	case strings.HasPrefix(hostVersion, "3.2."):
		return "live-verified host line (V3 3.2.149 desktop: create → save/reload readback → delete, 2026-10-01)"
	case strings.HasPrefix(hostVersion, "4."):
		return "host-unverified: the V4 bus API is not live-verified; buses are created and read back but reported unverified"
	}
	return "host-unverified: bus API not live-verified on host " + hostVersion
}

// runSchBusApply executes apply / rollback against a host.
func runSchBusApply(host schBusHost, plan []SchematicNativeBus, journalPath string, rollback bool) (*schBusApplyReport, error) {
	rep := &schBusApplyReport{Status: "applied", Deleted: []string{}, Created: []SchematicNativeBus{}, UserBusesKept: []string{}, ConnectivityRef: schBusConnectivityNote}
	if !rollback {
		names := map[string]bool{}
		for _, b := range plan {
			if err := validateSchNativeBusShape(b); err != nil {
				return nil, err
			}
			if names[strings.ToUpper(b.BusName)] {
				return nil, fmt.Errorf("duplicate bus name %s in the plan", b.BusName)
			}
			names[strings.ToUpper(b.BusName)] = true
		}
	}
	members, version, err := host.Probe(schBusAPIPaths)
	if err != nil {
		return nil, fmt.Errorf("bus API probe: %w", err)
	}
	rep.HostVersion = version
	for _, path := range schBusAPIPaths {
		if members[path] != "function" {
			if rollback {
				return nil, fmt.Errorf("host lacks eda.%s: cannot roll back buses", path)
			}
			rep.Status = "fallback-virtual-bus"
			rep.Warnings = append(rep.Warnings, fmt.Sprintf("host lacks eda.%s (%s): no native bus drawn; the members' aligned labels remain the virtual bus lane", path, members[path]))
			return rep, nil
		}
	}
	rep.Verification = schBusVerification(version)
	snap, project, doc, err := host.Page()
	if err != nil {
		return nil, fmt.Errorf("read page: %w", err)
	}
	if journalPath == "" {
		if project == "" || doc == "" {
			return nil, fmt.Errorf("page context lacks project/document ids; pass --journal")
		}
		journalPath = defaultSchBusJournal(project, doc)
	}
	rep.Journal = journalPath
	j, err := readSchBusJournal(journalPath)
	if err != nil {
		return nil, err
	}
	if (j.Project != "" && project != "" && j.Project != project) || (j.Doc != "" && doc != "" && j.Doc != doc) {
		return nil, fmt.Errorf("bus journal %s belongs to %s/%s, not the active page %s/%s", journalPath, j.Project, j.Doc, project, doc)
	}
	var live []SchematicNativeBus
	for _, b := range snap.Buses {
		live = append(live, busFromLive(b))
	}
	if rollback {
		plan = nil
	}
	cls, err := classifySchBuses(live, j, plan)
	if err != nil {
		return nil, err
	}
	rep.PrunedJournal = cls.Pruned
	for _, u := range cls.User {
		rep.UserBusesKept = append(rep.UserBusesKept, u.PrimitiveID)
	}
	var todo []SchematicNativeBus
	for i, b := range plan {
		if id, dup := cls.Identical[i]; dup {
			rep.NotDuplicated = append(rep.NotDuplicated, fmt.Sprintf("%s: identical user bus %s already on the page (not journalled, kept, not duplicated)", b.BusName, id))
			continue
		}
		for _, u := range cls.User {
			if strings.EqualFold(u.BusName, b.BusName) {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("user bus %s already uses the name %s (different geometry): both are kept", u.PrimitiveID, b.BusName))
			}
		}
		todo = append(todo, b)
	}
	// Members first: every member must already exist on a pin and carry its
	// own label/port. Checked on the page before any write.
	if len(todo) > 0 {
		check := *snap
		check.Buses = nil
		for _, b := range todo {
			check.Buses = append(check.Buses, busToSchaes(b))
		}
		check.HasBuses = true
		rep.MemberChecks = schaes.CheckBuses(&check)
		for _, c := range rep.MemberChecks {
			for _, f := range c.Findings {
				if f.Rule == "bus-touches-wire" || f.Rule == "bus-touches-pin" {
					return rep, fmt.Errorf("bus %s would touch the page (%s %s): a planned bus touches nothing — replan from a fresh snapshot; nothing was written", c.Name, f.Rule, f.Net)
				}
			}
			if !c.OK {
				var bad []string
				for _, f := range c.Findings {
					if f.Severity == "error" {
						bad = append(bad, f.Rule+" "+f.Net)
					}
				}
				return rep, fmt.Errorf("bus %s: members not ready (%s); create the member labels first — nothing was written", c.Name, strings.Join(bad, ", "))
			}
		}
	}
	// From here on every write is journalled, even on failure.
	keep := []SchematicNativeBus{}
	for _, e := range j.Buses {
		if slices.Contains(cls.Replace, e.PrimitiveID) {
			keep = append(keep, e)
		}
	}
	j.Project, j.Doc, j.Buses = project, doc, keep
	save := func() error { return writeSchBusJournal(journalPath, j) }
	if err := save(); err != nil {
		return nil, err
	}
	if len(cls.Replace) > 0 {
		res, err := host.Delete(cls.Replace)
		if err != nil {
			return rep, fmt.Errorf("delete journalled buses %v: %w", cls.Replace, err)
		}
		survived := stringList(res["survivedIds"])
		deleted := map[string]bool{}
		for _, id := range cls.Replace {
			if !slices.Contains(survived, id) {
				deleted[id] = true
				rep.Deleted = append(rep.Deleted, id)
			}
		}
		var left []SchematicNativeBus
		for _, e := range j.Buses {
			if !deleted[e.PrimitiveID] {
				left = append(left, e)
			}
		}
		j.Buses = left
		if err := save(); err != nil {
			return rep, err
		}
		if v, _ := res["verified"].(bool); !v || len(survived) > 0 {
			return rep, fmt.Errorf("bus delete not confirmed by readback (survivors %v); journal kept them", survived)
		}
	}
	if rollback {
		rep.Status = "rolled-back"
	}
	var createErr error
	for _, b := range todo {
		res, err := host.Create(schBusCreatePayload(b))
		if err != nil {
			createErr = fmt.Errorf("create bus %s: %w", b.BusName, err)
			break
		}
		id, _ := res["primitiveId"].(string)
		if id == "" {
			createErr = fmt.Errorf("create bus %s returned no primitive id; inspect the page before retrying", b.BusName)
			break
		}
		made := b
		made.PrimitiveID = id
		made.Status = ""
		j.Buses = append(j.Buses, made)
		rep.Created = append(rep.Created, made)
		if err := save(); err != nil {
			return rep, err
		}
		if v, _ := res["verified"].(bool); !v {
			createErr = fmt.Errorf("bus %s (%s) created but its readback did not confirm name/path; journalled for rollback", id, b.BusName)
			break
		}
	}
	if createErr != nil {
		return rep, createErr
	}
	// Final readback: our buses exactly as planned, user buses untouched.
	after, _, _, err := host.Page()
	if err != nil {
		return rep, fmt.Errorf("final readback: %w", err)
	}
	got := map[string]SchematicNativeBus{}
	for _, b := range after.Buses {
		lb := busFromLive(b)
		got[lb.PrimitiveID] = lb
	}
	for _, b := range rep.Created {
		g, ok := got[b.PrimitiveID]
		if !ok || g.BusName != b.BusName || !schSameBusLine(g.Line, b.Line) {
			return rep, fmt.Errorf("final readback: bus %s (%s) missing or differs from the plan (segment-set comparison)", b.PrimitiveID, b.BusName)
		}
	}
	for _, u := range cls.User {
		g, ok := got[u.PrimitiveID]
		if !ok || g.BusName != u.BusName || !schSameBusLine(g.Line, u.Line) {
			return rep, fmt.Errorf("final readback: user bus %s (%s) changed or vanished", u.PrimitiveID, u.BusName)
		}
	}
	for _, id := range rep.Deleted {
		if _, still := got[id]; still {
			return rep, fmt.Errorf("final readback: deleted bus %s is still on the page", id)
		}
	}
	if want := len(cls.User) + len(rep.Created); len(got) != want {
		return rep, fmt.Errorf("final readback: %d buses on the page, expected %d (user %d + created %d)", len(got), want, len(cls.User), len(rep.Created))
	}
	return rep, nil
}

func stringList(v any) []string {
	var out []string
	if xs, ok := v.([]any); ok {
		for _, x := range xs {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	if xs, ok := v.([]string); ok {
		out = append(out, xs...)
	}
	return out
}

// decodeSchBusPlan reads plan buses from a bus array, {"buses":[…]} (layout
// result), {"layout":{"buses":[…]}} (compose plan) or {"modules":[{"buses"}]}
// (lib-layout source).
func decodeSchBusPlan(raw []byte) ([]SchematicNativeBus, error) {
	var arr []SchematicNativeBus
	if json.Unmarshal(raw, &arr) == nil {
		return arr, nil
	}
	var top struct {
		Buses  []SchematicNativeBus `json:"buses"`
		Layout *struct {
			Buses []SchematicNativeBus `json:"buses"`
		} `json:"layout"`
		Modules []struct {
			Buses []SchematicNativeBus `json:"buses"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, fmt.Errorf("bus plan: %w", err)
	}
	out := append([]SchematicNativeBus(nil), top.Buses...)
	if top.Layout != nil {
		out = append(out, top.Layout.Buses...)
	}
	if len(top.Modules) > 1 && len(top.Modules[0].Buses) > 0 {
		return nil, fmt.Errorf("lib-layout modules are module-local: apply the composed plan (sch compose --out plan.json) instead")
	}
	for _, m := range top.Modules {
		out = append(out, m.Buses...)
	}
	return out, nil
}

// decodeLiveBusList reads `sch bus list` output (envelope or bare result).
func decodeLiveBusList(raw []byte) ([]SchematicNativeBus, error) {
	var env struct {
		Result *struct {
			Buses *[]SchematicNativeBus `json:"buses"`
		} `json:"result"`
		Buses *[]SchematicNativeBus `json:"buses"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("--bus-list: %w", err)
	}
	switch {
	case env.Result != nil && env.Result.Buses != nil:
		return *env.Result.Buses, nil
	case env.Buses != nil:
		return *env.Buses, nil
	}
	return nil, fmt.Errorf("--bus-list must be sch bus list output (result.buses)")
}

// liveSchBusHost drives the daemon.
type liveSchBusHost struct {
	cfg    *appConfig
	window string
}

func (h liveSchBusHost) Probe(paths []string) (map[string]string, string, error) {
	res, err := requestAction(h.cfg, "system.api.probe", h.window, map[string]any{"paths": paths})
	if err != nil {
		return nil, "", err
	}
	out := map[string]string{}
	if m, ok := res.Result["members"].(map[string]any); ok {
		for k, v := range m {
			out[k], _ = v.(string)
		}
	}
	version, _ := res.Result["hostVersion"].(string)
	return out, version, nil
}

func (h liveSchBusHost) Page() (*schaes.Snapshot, string, string, error) {
	res, err := requestAction(h.cfg, "schematic.components.list", h.window, map[string]any{"includeBBox": true, "includePins": true, "includeWires": true})
	if err != nil {
		return nil, "", "", err
	}
	buses, err := requestAction(h.cfg, "schematic.bus.list", h.window, map[string]any{})
	if err != nil {
		return nil, "", "", err
	}
	merged := map[string]any{}
	for k, v := range res.Result {
		merged[k] = v
	}
	list, ok := buses.Result["buses"]
	if !ok {
		return nil, "", "", fmt.Errorf("schematic.bus.list returned no buses array: bus inventory unknown")
	}
	merged["buses"] = list
	raw, err := json.Marshal(merged)
	if err != nil {
		return nil, "", "", err
	}
	snap, err := schaes.Parse(raw)
	if err != nil {
		return nil, "", "", err
	}
	project, doc := "", ""
	if res.Context != nil {
		project, doc = res.Context.ProjectUUID, res.Context.DocumentUUID
	}
	return snap, project, doc, nil
}

func (h liveSchBusHost) Create(payload map[string]any) (map[string]any, error) {
	res, err := requestAction(h.cfg, "schematic.bus.create", h.window, payload)
	if err != nil {
		return nil, err
	}
	return res.Result, nil
}

func (h liveSchBusHost) Delete(ids []string) (map[string]any, error) {
	res, err := requestAction(h.cfg, "schematic.bus.delete", h.window, map[string]any{"primitiveIds": ids})
	if err != nil {
		return nil, err
	}
	return res.Result, nil
}

func newSchBusApplyCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var planPath, planB64, journal, busList string
	var dry, rollback bool
	c := &cobra.Command{
		Use:   "apply",
		Short: "Create a layout plan's native buses after their member labels exist (journalled, readback-verified, replace-safe)",
		Long: "按布局计划创建原生总线（成员标签必须已存在；总线只是绘图，不是连接证据）。\n\n" +
			"  --plan       计划 JSON：总线数组、layout-plan 结果（buses）、compose 计划（layout.buses）\n" +
			"  --buses-b64  同上（base64，供 compose 队列内联）\n" +
			"  --journal    本页总线日志（默认 <项目根>/.pcbpilot/bus-journal/<project>_<doc>.json）\n" +
			"  --rollback   按日志精确 ID 删除本工具建的总线（名字与几何须与日志一致）\n" +
			"  --dry-run    离线：校验并打印 create 载荷；给 --bus-list（sch bus list 输出）与 --journal 时\n" +
			"               同时打印替换/保留分类，不联系 daemon\n\n" +
			"流程：api probe（缺 sch_PrimitiveBus → 退回虚拟总线，不写）→ 读页（引脚/标签/导线/总线）→\n" +
			"日志分类（日志 ID 且名字+几何一致 = 本工具建的 → 替换；日志 ID 已不在 → 剪除；不在日志 = 用户的 →\n" +
			"永不删除，完全相同的不重复建）→ 成员检查（每个成员在引脚上且有自己的标签/端口）→ 按 ID 删旧 →\n" +
			"逐条 create（连接器回读无向线段集合）→ 每次写后落日志 → 终态回读核对。V3 3.2.x 记 live-verified，\n" +
			"V4 照建但记 host-unverified。",
		Example: "  pcbpilot sch bus apply --plan plan.json --dry-run\n" +
			"  pcbpilot sch bus apply --plan plan.json --project P --doc <page>\n" +
			"  pcbpilot sch bus apply --rollback --project P --doc <page>",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var plan []SchematicNativeBus
			if !rollback {
				var raw []byte
				var err error
				switch {
				case planPath != "" && planB64 != "":
					return fmt.Errorf("use either --plan or --buses-b64")
				case planPath != "":
					raw, err = os.ReadFile(planPath)
				case planB64 != "":
					raw, err = base64.StdEncoding.DecodeString(planB64)
				default:
					return fmt.Errorf("--plan or --buses-b64 is required (or --rollback)")
				}
				if err != nil {
					return err
				}
				if plan, err = decodeSchBusPlan(raw); err != nil {
					return err
				}
				if len(plan) == 0 {
					return fmt.Errorf("the plan carries no buses")
				}
			}
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if dry {
				rep := &schBusApplyReport{Status: "dry-run", Journal: journal, Deleted: []string{}, Created: []SchematicNativeBus{}, UserBusesKept: []string{}, ConnectivityRef: schBusConnectivityNote}
				for _, b := range plan {
					if err := validateSchNativeBusShape(b); err != nil {
						return err
					}
					rep.Creates = append(rep.Creates, schBusCreatePayload(b))
				}
				if busList != "" {
					raw, err := os.ReadFile(busList)
					if err != nil {
						return err
					}
					live, err := decodeLiveBusList(raw)
					if err != nil {
						return err
					}
					j := &schBusJournal{SchemaVersion: 1}
					if journal != "" {
						if j, err = readSchBusJournal(journal); err != nil {
							return err
						}
					}
					cls, err := classifySchBuses(live, j, plan)
					if err != nil {
						return err
					}
					rep.Deleted, rep.PrunedJournal = append(rep.Deleted, cls.Replace...), cls.Pruned
					for _, u := range cls.User {
						rep.UserBusesKept = append(rep.UserBusesKept, u.PrimitiveID)
					}
					idx := make([]int, 0, len(cls.Identical))
					for i := range cls.Identical {
						idx = append(idx, i)
					}
					sort.Ints(idx)
					for _, i := range idx {
						rep.NotDuplicated = append(rep.NotDuplicated, plan[i].BusName+": identical user bus "+cls.Identical[i])
					}
				}
				rep.Warnings = append(rep.Warnings, "dry-run: validated offline; nothing was sent to the daemon")
				return enc.Encode(rep)
			}
			if busList != "" {
				return fmt.Errorf("--bus-list is for --dry-run only (live apply reads the page itself)")
			}
			rep, err := runSchBusApply(liveSchBusHost{cfg: cfg, window: *window}, plan, journal, rollback)
			if rep != nil && err != nil && (rep.Status == "applied" || rep.Status == "rolled-back") {
				// Live 2026-10-04: a failed create still printed "applied".
				rep.Status = "failed"
			}
			if rep != nil {
				_ = enc.Encode(rep)
				for _, w := range rep.Warnings {
					fmt.Fprintln(stderr, "warning: "+w)
				}
			}
			return err
		},
	}
	c.Flags().StringVar(&planPath, "plan", "", "plan JSON carrying buses (bus array, layout result, compose plan)")
	c.Flags().StringVar(&planB64, "buses-b64", "", "base64 of a bus array (used by compose playbooks)")
	c.Flags().StringVar(&journal, "journal", "", "per-page bus journal (default <project root>/.pcbpilot/bus-journal/<project>_<doc>.json)")
	c.Flags().StringVar(&busList, "bus-list", "", "with --dry-run: sch bus list output to classify against the journal")
	c.Flags().BoolVar(&dry, "dry-run", false, "validate and print the create payloads; do not contact the daemon")
	c.Flags().BoolVar(&rollback, "rollback", false, "delete the journalled buses by exact id (name + geometry must still match)")
	return c
}

func newSchBusCheckCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var snapshot string
	c := &cobra.Command{
		Use:   "check",
		Short: "Check every native bus: members exist on pins and carry their own label/port (non-zero exit on errors)",
		Long: "总线成员连通检查：每条原生总线的名字（NAME[a:b] 或协议组名）对应的成员网必须在引脚上存在，且有自己的\n" +
			"网络标签/端口——总线不是连接证据，任何成员不得只靠总线「连接」。另报总线碰导线/引脚（warn）。\n" +
			"--snapshot 离线（sch list + buses / layout-plan / compose 计划）；不给则只读现场当前页。有 error 时退出码非零。",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var snap *schaes.Snapshot
			var err error
			if snapshot != "" {
				raw, e := os.ReadFile(snapshot)
				if e != nil {
					return e
				}
				if snap, err = schaes.Parse(raw); err != nil {
					// compose plan: the page drawing lives under "layout"
					var top struct {
						Layout json.RawMessage `json:"layout"`
					}
					if json.Unmarshal(raw, &top) != nil || len(top.Layout) == 0 {
						return err
					}
					if snap, err = schaes.Parse(top.Layout); err != nil {
						return err
					}
				}
			} else if snap, _, _, err = (liveSchBusHost{cfg: cfg, window: *window}).Page(); err != nil {
				return err
			}
			checks := schaes.CheckBuses(snap)
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(map[string]any{"buses": len(snap.Buses), "checks": checks, "connectivity": schBusConnectivityNote}); err != nil {
				return err
			}
			for _, c := range checks {
				if !c.OK {
					return fmt.Errorf("bus %s: member connectivity check failed", c.Name)
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&snapshot, "snapshot", "", "offline snapshot JSON; omit to read the live active page")
	return c
}
