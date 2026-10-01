package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/internal/version"
	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
	"github.com/zhuangzard/pcbpilot/pkg/designreport"
	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
	"github.com/zhuangzard/pcbpilot/pkg/projectconfig"
	"github.com/zhuangzard/pcbpilot/pkg/schaes"
)

// designReportOpts are the inputs of one report version.
type designReportOpts struct {
	outDir, version             string
	project, customer           string
	host, connector             string
	intent, sim, planDir, board string
	reloadBoard, drc, check     string
	rulesCheck, netDiff, models string
	values                      string
	post, spice                 string
	noZip                       bool
	analog                      string
	images                      []string
	schSnapshots                []string
	force                       bool
	maxImageBytes               int
	date                        string
	// projectConfig: "" = auto (./pcbpilot.project.json when present), "none",
	// or a path to the file / its work dir.
	projectConfig string
}

func newReportCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	g := &cobra.Command{
		Use:   "report",
		Short: "Customer-facing, versioned design reports built from a run's artifacts (offline)",
		Long: `Reports assembled from the artifacts a pcbpilot run leaves behind.

  report design   intent / sim / pcb auto / board dump / DRC / check / images →
                  reports/<name>/vN/{report.html,report.md,report.json} + index.json + CHANGELOG.md`,
	}
	g.AddCommand(newReportDesignCmd(stdout, stderr))
	return g
}

func newReportDesignCmd(stdout, stderr io.Writer) *cobra.Command {
	var o designReportOpts
	c := &cobra.Command{
		Use:   "design",
		Short: "Generate the next version of the customer design report (HTML + Markdown + JSON, changelog vs the previous version)",
		Args:  cobra.NoArgs,
		Long: `Builds a COMPLETE, customer-presentable DESIGN REPORT from a fixed template
(.agents/skills/pcbpilot/templates/design-report/) and publishes it as a new
version under --out-dir. Offline: never contacts the editor or daemon.

SECTIONS (each computed from the named inputs; a section whose inputs are
missing is printed as "不可用" with the reason — nothing is invented)
   0 封面        project, customer, version, date, tool/host versions, input
                 provenance (path + sha256), overall verdict PASS / PASS with
                 warnings / FAIL with the reasons
   1 执行摘要    key numbers + top risks (findings, margins below guideline,
                 failed checks), changes vs the previous version
   2 需求与意图  intent blocks, domains, standard/insulation, findings   [--intent]
   3 电源仿真    per-scenario rails (grouped-bar chart), power budget per part
                 and rail, power tree, regulator operating points, ripple,
                 model confidence and assumptions                       [--sim]
   4 器件可行性  stress vs rating with margin % for every part: inductor Ipk/
                 Irms, regulator Iout / Vin / Tj, diode If/VR, resistor P vs
                 package (0402 1/16 W … 1206 1/4 W), MLCC V vs rated (≥1.5×,
                 DC-bias note) and ripple, LED, BJT, connector/USB budget,
                 ESD VRWM, load supply range, GPIO drive; unknown rating =
                 "需数据手册"; margin chart sorted ascending  [--sim|--intent,
                 ratings from --models and part MPNs (board dump / --values)]
   5 工程计算    IPC-2221/2152 widths, via count, IPC-2221B clearance,
                 microstrip/diff impedance, creepage/clearance/slot and hi-pot
                 voltage per insulation pair, with formulas         [--intent]
   6 布局布线    images, stackup, routing stats, IR drop per net / load pad
                 (chart) / worst path / top segments, SI, isolation, feedback
                                                     [--plan-dir, --image]
  6C 原理图美观 schematic aesthetics per page (wiring / layout / labels / bus
                 lanes), report-only, never the verdict  [--sch-snapshot]
   7 验证状态    native DRC, pcb check, rules sync, pad-net diff, save/reload
                 hash, routing completion, IR budget — PASS/WARN/FAIL/N/A with
                 the evidence file                 [--drc --check --rules-check
                                            --net-diff --board --reload-board]
   8 测试点计划  rail voltages (expected ± tolerance from Vref accuracy and
                 divider resistor tolerance / source spec / sim envelope),
                 bench current limit 1.5× typical, switch-node and output
                 ripple (ΔI/(8·fsw·C)), reset/boot straps, LEDs, USB
                 enumeration, TDR coupons, hi-pot; test points to add
   9 制造装配    fab parameters, DFM warnings grouped by type, polarity / pin-1
                 / EP / fine-pitch parts, HV slots, coating, ESD handling
  10 调试上电    step-by-step first power-up, rail-to-GND resistance table,
                 failure signatures
  11 附录        net table, part list with models and decoded MPNs, raw
                 provenance, missing sections, glossary

OUTPUT (--out-dir reports/<name>/)
  vN/report.html   single self-contained file (inline CSS + SVG charts, images
                   as base64); light/dark/print
  vN/report.md     same content; charts in vN/charts/*.svg, images in assets/
  vN/report.json   every computed table (schemaVersion 1)
  assets/          content-addressed images shared across versions
  index.json       every version with its metrics / findings / changes
  CHANGELOG.md     vN vs vN-1: rail currents, IR drops, margins, routed %, DRC,
                   findings added / resolved
--version auto (default) = next integer; an existing version is refused unless
--force. Output is deterministic for identical inputs except the generatedAt
header (--date or SOURCE_DATE_EPOCH pin it).`,
		Example: `  # the sample report in docs/examples/esp32-mini-design-report/
  pcbpilot report design --project-name "ESP32-S3 mini" --customer Demo \
      --intent intent.json --sim sim.json --plan-dir final/ --board board.json \
      --drc drc.json --check check.txt --rules-check rules-check.json \
      --image sch:P1=sch-p1.png --image sch:P2=sch-p2.png --image layout=snapshot.png \
      --host "EasyEDA Pro desktop V3 3.2.149" --connector 0.4.1 \
      --out-dir reports/esp32-mini

  # pre-layout version straight after intent derive
  pcbpilot intent derive ... --out intent.json --sim-out sim.json --report-dir reports/esp32-mini`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.outDir == "" {
				return fmt.Errorf("--out-dir is required (e.g. reports/<name>/)")
			}
			dir, rep, err := runDesignReport(o, stderr)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "wrote %s/{report.html,report.md,report.json} — %s %s\n", dir, rep.VersionLabel, rep.Verdict.Status)
			return nil
		},
	}
	addDesignReportFlags(c, &o)
	return c
}

func addDesignReportFlags(c *cobra.Command, o *designReportOpts) {
	f := c.Flags()
	f.StringVar(&o.outDir, "out-dir", "", "report root reports/<name>/ (versions vN/, assets/, index.json, CHANGELOG.md)")
	f.StringVar(&o.version, "version", "auto", "auto (next integer) | vN | N")
	f.StringVar(&o.project, "project-name", "", "project name on the cover (default: the out-dir base name)")
	f.StringVar(&o.customer, "customer", "", "customer name on the cover")
	f.StringVar(&o.host, "host", "", "EDA host and exact version, e.g. \"EasyEDA Pro desktop V3 3.2.149\"")
	f.StringVar(&o.connector, "connector", "", "connector version reported by pcbpilot health")
	f.StringVar(&o.intent, "intent", "", "intent.json (pcbpilot intent derive)")
	f.StringVar(&o.sim, "sim", "", "sim.json (pcbpilot sim power / intent derive --sim-out)")
	f.StringVar(&o.planDir, "plan-dir", "", "pcb auto run --out-dir (plan.json, feedback.json, preview.svg)")
	f.StringVar(&o.board, "board", "", "board dump JSON (pcb dump --include-copper): MPNs, pads, rules, hashes")
	f.StringVar(&o.reloadBoard, "reload-board", "", "second board dump after save → reload: semanticSha256 must match --board")
	f.StringVar(&o.drc, "drc", "", "native DRC JSON (pcb drc)")
	f.StringVar(&o.check, "check", "", "pcb check output (text or --json)")
	f.StringVar(&o.rulesCheck, "rules-check", "", "pcb rules check JSON (intent ↔ EasyEDA rule sync)")
	f.StringVar(&o.netDiff, "net-diff", "", "pad-net diff JSON ({passed|ok, diffs[]…})")
	f.StringVar(&o.models, "models", "", "power-models.json with ratings (default: the installed skill's references/power-models.json)")
	f.StringVar(&o.values, "values", "", "part values/MPNs: sch list JSON or {\"parts\":{ref:{value,mpn}}}")
	f.StringVar(&o.post, "post", "", "post.json (pcbpilot sim post-layout): chapter 6A 设计后仿真验证 + verdict; its heat maps (--svg-dir) and Elmer deck are packaged")
	f.StringVar(&o.spice, "spice", "", "SPICE netlist of sim power --spice, packaged under data/")
	f.BoolVar(&o.noZip, "no-zip", false, "do not write reports/<name>/pcbpilot-report-<name>-vN.zip")
	f.StringArrayVar(&o.schSnapshots, "sch-snapshot", nil, "schematic page snapshot [LABEL=]PATH (repeatable): sch list --include-pins --include-bbox --include-wires, layout-plan, lib-layout or canonical JSON → §6C 原理图美观度 (report-only, never changes the verdict)")
	f.StringArrayVar(&o.images, "image", nil, "image KIND[:LABEL]=PATH, KIND sch|layout|heat|other (repeatable), e.g. sch:P1=p1.png, layout=snapshot.png, heat:TOP=heatmaps/temp-TOP.svg")
	f.StringVar(&o.analog, "analog", "", "analog.json (pcbpilot sim analog): §3A analog SPICE section; its ngspice netlists/outputs are packaged under data/analog/")
	f.BoolVar(&o.force, "force", false, "overwrite an existing version")
	f.IntVar(&o.maxImageBytes, "max-image-bytes", designreport.DefaultMaxImageBytes, "raster images larger than this are halved until they fit")
	f.StringVar(&o.date, "date", "", "generatedAt override (RFC3339); default SOURCE_DATE_EPOCH or now")
	f.StringVar(&o.projectConfig, "project-config", "", "pcbpilot.project.json (file or work dir) whose skipped steps/sections the report lists; default ./pcbpilot.project.json when present, \"none\" to ignore")
}

// reportInput reads one optional input file and records its provenance.
type reportInputs struct {
	refs  []designreport.InputRef
	files []pkgFile // non-image inputs, packaged under data/
}

func (ri *reportInputs) read(kind, label, path string) ([]byte, error) {
	ref := designreport.InputRef{Kind: kind, Label: label, Path: path}
	if path == "" {
		ri.refs = append(ri.refs, ref)
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s (%s): %w", label, kind, err)
	}
	h := sha256.Sum256(b)
	ref.Present, ref.SHA256, ref.Bytes = true, hex.EncodeToString(h[:]), len(b)
	ri.refs = append(ri.refs, ref)
	if kind != "image" {
		prod := producers[kind]
		if prod == "" && strings.HasPrefix(kind, "sch-snapshot") {
			prod = producers["sch-snapshot"]
		}
		ri.files = append(ri.files, newPkgFile(dataFileName(kind, path), "data:"+kind, prod, path, b))
	}
	return b, nil
}

func (ri *reportInputs) note(kind, note string) {
	for i := len(ri.refs) - 1; i >= 0; i-- {
		if ri.refs[i].Kind == kind {
			ri.refs[i].Note = note
			return
		}
	}
}

func reportTime(date string) (time.Time, error) {
	if date != "" {
		return time.Parse(time.RFC3339, date)
	}
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH: %w", err)
		}
		return time.Unix(n, 0).UTC(), nil
	}
	return time.Now().UTC(), nil
}

// loadDesignReportInputs parses every given input; a present but unreadable
// input is an error (a typo must not silently produce a thinner report).
func loadDesignReportInputs(o designReportOpts, stderr io.Writer) (*designreport.Inputs, []pkgFile, error) {
	in := &designreport.Inputs{Project: o.project, Customer: o.customer,
		Tools: designreport.ToolInfo{Pcbpilot: version.Version, Host: o.host, Connector: o.connector}}
	if in.Project == "" {
		in.Project = filepath.Base(filepath.Clean(o.outDir))
	}
	var err error
	if in.GeneratedAt, err = reportTime(o.date); err != nil {
		return nil, nil, err
	}
	ri := &reportInputs{}
	if b, err := ri.read("intent", "设计意图 intent.json", o.intent); err != nil {
		return nil, nil, err
	} else if b != nil {
		var it intent.Intent
		if err := json.Unmarshal(b, &it); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", o.intent, err)
		}
		in.Intent = &it
	}
	if b, err := ri.read("sim", "电源仿真 sim.json", o.sim); err != nil {
		return nil, nil, err
	} else if b != nil {
		if in.Sim, err = intent.ParseSimOutput(b); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", o.sim, err)
		}
	}
	planPath, fbPath, prevPath := "", "", ""
	if o.planDir != "" {
		planPath = filepath.Join(o.planDir, "plan.json")
		if _, err := os.Stat(filepath.Join(o.planDir, "feedback.json")); err == nil {
			fbPath = filepath.Join(o.planDir, "feedback.json")
		}
		if _, err := os.Stat(filepath.Join(o.planDir, "preview.svg")); err == nil {
			prevPath = filepath.Join(o.planDir, "preview.svg")
		}
	}
	if b, err := ri.read("plan", "pcb auto plan.json", planPath); err != nil {
		return nil, nil, err
	} else if b != nil {
		var rep pcbauto.Report
		if err := json.Unmarshal(b, &rep); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", planPath, err)
		}
		in.Plan = &rep
	}
	if b, err := ri.read("feedback", "pcb auto feedback.json", fbPath); err != nil {
		return nil, nil, err
	} else if b != nil {
		var fb pcbauto.Feedback
		if err := json.Unmarshal(b, &fb); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", fbPath, err)
		}
		in.Feedback = &fb
	}
	parse := func(kind, label, path string, fn func([]byte) error) error {
		b, err := ri.read(kind, label, path)
		if err != nil || b == nil {
			return err
		}
		if err := fn(b); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		return nil
	}
	if err := parse("board", "板级回读 board dump", o.board, func(b []byte) (err error) { in.Board, err = designreport.ParseBoard(b); return }); err != nil {
		return nil, nil, err
	}
	if err := parse("reload-board", "保存重载后回读", o.reloadBoard, func(b []byte) (err error) { in.ReloadBoard, err = designreport.ParseBoard(b); return }); err != nil {
		return nil, nil, err
	}
	if err := parse("drc", "原生 DRC", o.drc, func(b []byte) (err error) { in.DRC, err = designreport.ParseDRC(b); return }); err != nil {
		return nil, nil, err
	}
	if err := parse("check", "pcb check", o.check, func(b []byte) (err error) { in.Check, err = designreport.ParseCheck(b); return }); err != nil {
		return nil, nil, err
	}
	if err := parse("rules-check", "规则同步 rules check", o.rulesCheck, func(b []byte) (err error) { in.RulesCheck, err = designreport.ParseRulesCheck(b); return }); err != nil {
		return nil, nil, err
	}
	if err := parse("net-diff", "焊盘网络对账", o.netDiff, func(b []byte) (err error) { in.NetDiff, err = designreport.ParseNetDiff(b); return }); err != nil {
		return nil, nil, err
	}
	if err := parse("analog", "模拟仿真 analog.json", o.analog, func(b []byte) error {
		var out analogsim.Output
		if err := json.Unmarshal(b, &out); err != nil {
			return err
		}
		if out.SchemaVersion != analogsim.SchemaVersion {
			return fmt.Errorf("analog.json schemaVersion %d unsupported", out.SchemaVersion)
		}
		in.Analog = &out
		return nil
	}); err != nil {
		return nil, nil, err
	}
	if err := parse("values", "器件值/型号", o.values, func(b []byte) (err error) { in.Values, err = powersim.ParseValues(b); return }); err != nil {
		return nil, nil, err
	}
	modelsPath := o.models
	if modelsPath == "" {
		if p, err := powerModelsAsset.resolve(""); err == nil {
			modelsPath = p
		} else {
			fmt.Fprintln(stderr, "warning: power-models.json not found; ratings come from part MPNs only")
		}
	}
	if err := parse("models", "功率模型/额定 power-models.json", modelsPath, func(b []byte) (err error) {
		in.Models, err = designreport.ParseModels(b, modelsPath)
		return
	}); err != nil {
		return nil, nil, err
	}
	if err := parse("post", "设计后仿真 post.json", o.post, func(b []byte) (err error) { in.Post, err = designreport.ParsePost(b); return }); err != nil {
		return nil, nil, err
	}
	if err := parse("spice", "SPICE 网表", o.spice, func([]byte) error { return nil }); err != nil {
		return nil, nil, err
	}
	for i, spec := range o.schSnapshots {
		label, path, ok := strings.Cut(spec, "=")
		if !ok {
			label, path = fmt.Sprintf("P%d", i+1), spec
		}
		kind := "sch-snapshot"
		if len(o.schSnapshots) > 1 {
			kind = "sch-snapshot-" + strings.Map(func(r rune) rune {
				if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
					return r
				}
				return '_'
			}, label)
		}
		if err := parse(kind, "原理图快照 "+label, path, func(b []byte) error {
			snap, err := schaes.Parse(b)
			if err != nil {
				return err
			}
			in.SchPages = append(in.SchPages, designreport.SchPage{Label: label, Snapshot: snap})
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}
	// Images: explicit ones, then the pcb auto preview, then the post-layout
	// heat maps (unless given explicitly).
	type imgSpec struct{ kind, label, path string }
	var specs []imgSpec
	for _, s := range o.images {
		k, p, ok := strings.Cut(s, "=")
		if !ok || p == "" {
			return nil, nil, fmt.Errorf("--image %q: want KIND[:LABEL]=PATH", s)
		}
		kind, label, _ := strings.Cut(k, ":")
		switch kind {
		case "sch":
			label = strings.TrimSpace("原理图 " + label)
		case "layout":
			if label == "" {
				label = "PCB 布局（编辑器快照）"
			}
		case "heat":
			label = strings.TrimSpace(label + " 热图")
		default:
			if label == "" {
				label = kind
			}
		}
		specs = append(specs, imgSpec{kind, label, p})
	}
	if prevPath != "" {
		specs = append(specs, imgSpec{"preview", "pcb auto 离线布线预览（preview.svg）", prevPath})
	}
	explicitHeat := false
	for _, sp := range specs {
		explicitHeat = explicitHeat || sp.kind == "heat"
	}
	if !explicitHeat {
		if _, hs := postHeatImages(in.Post, o.post); len(hs) > 0 {
			for _, h := range hs {
				specs = append(specs, imgSpec{h[0], h[1], h[2]})
			}
		} else if in.Post != nil && len(in.Post.Maps) > 0 {
			fmt.Fprintf(stderr, "warning: post.json lists %d heat map(s) in %q but the directory was not found; pass --image heat:LAYER=… \n", len(in.Post.Maps), in.Post.MapsDir)
		}
	}
	for _, s := range specs {
		b, err := ri.read("image", s.label, s.path)
		if err != nil {
			return nil, nil, err
		}
		img, err := designreport.PrepareImage(s.kind, s.label, s.path, b, o.maxImageBytes)
		if err != nil {
			return nil, nil, err
		}
		if img.Resized {
			ri.note("image", fmt.Sprintf("缩小到 %d×%d（%d 字节）", img.Width, img.Height, img.Bytes))
		}
		in.Images = append(in.Images, img)
	}
	in.Refs = ri.refs
	in.Data = map[string]string{}
	for _, f := range ri.files {
		in.Data[strings.TrimPrefix(f.Role, "data:")] = f.Rel
	}
	skips, err := reportProcessSkips(o.projectConfig)
	if err != nil {
		return nil, nil, err
	}
	in.ProcessSkips = skips
	return in, ri.files, nil
}

// reportProcessSkips reads the project process template so the report says
// which steps and sections were skipped on purpose (and why) instead of
// listing them as missing evidence.
func reportProcessSkips(spec string) (*designreport.ProcessSkips, error) {
	if spec == "none" {
		return nil, nil
	}
	dir := spec
	if spec == "" {
		dir = "."
	} else if st, err := os.Stat(spec); err == nil && !st.IsDir() {
		dir = filepath.Dir(spec)
	}
	c, err := projectconfig.Load(dir)
	if errors.Is(err, projectconfig.ErrNotFound) && spec == "" {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("--project-config: %w", err)
	}
	ps := &designreport.ProcessSkips{Template: c.Template, Source: projectconfig.Path(dir)}
	for _, s := range c.SkippedSteps() {
		ps.Steps = append(ps.Steps, designreport.Missing{Section: s.ID + " " + s.Title, Reason: s.Reason})
	}
	for _, s := range c.SkippedSections() {
		ps.Sections = append(ps.Sections, designreport.Missing{Section: s.ID + " " + s.Title, Reason: s.Reason})
	}
	return ps, nil
}

// runDesignReport builds and publishes one report version.
func runDesignReport(o designReportOpts, stderr io.Writer) (string, *designreport.Report, error) {
	in, packaged, err := loadDesignReportInputs(o, stderr)
	if err != nil {
		return "", nil, err
	}
	idxPath := filepath.Join(o.outDir, "index.json")
	raw, err := os.ReadFile(idxPath)
	if err != nil && !os.IsNotExist(err) {
		return "", nil, err
	}
	idx, err := designreport.ParseIndex(raw)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", idxPath, err)
	}
	v, err := idx.ResolveVersion(o.version)
	if err != nil {
		return "", nil, err
	}
	label := fmt.Sprintf("v%d", v)
	dir := filepath.Join(o.outDir, label)
	if !o.force {
		if idx.Has(v) {
			return "", nil, fmt.Errorf("%s already exists in %s (use --version auto or --force)", label, idxPath)
		}
		if _, err := os.Stat(dir); err == nil {
			return "", nil, fmt.Errorf("%s already exists (use --force to overwrite)", dir)
		}
	}
	rep := designreport.Build(in)
	rep.Version, rep.VersionLabel = v, label
	zipFile := zipName(rep.Project, label)
	if !o.noZip {
		rep.Package = zipFile
	}
	cur := designreport.EntryOf(rep)
	rep.Changes = designreport.Compare(idx.Latest(v), cur)
	cur.Changes = rep.Changes
	charts := designreport.Charts(rep)
	html, err := designreport.RenderHTML(rep, charts)
	if err != nil {
		return "", nil, fmt.Errorf("render html: %w", err)
	}
	md, err := designreport.RenderMarkdown(rep)
	if err != nil {
		return "", nil, fmt.Errorf("render markdown: %w", err)
	}
	js, _ := json.MarshalIndent(rep, "", "  ")
	if o.force {
		_ = os.RemoveAll(dir)
	}
	// The version package: report files, assets/ (images, charts, heat
	// maps), data/ (every input and evidence file), manifest.json, zip.
	files := []pkgFile{
		newPkgFile("report.html", "report-html", producers["report"], "", html),
		newPkgFile("report.md", "report-md", producers["report"], "", md),
		newPkgFile("report.json", "report-json", producers["report"], "", append(js, '\n')),
	}
	for _, img := range in.Images {
		prod := producers["image"]
		if p, ok := producers[img.Kind]; ok {
			prod = p
		}
		files = append(files, newPkgFile("assets/"+img.Asset, "image:"+img.Kind, prod, img.Path, img.Data))
	}
	names := make([]string, 0, len(charts))
	for n := range charts {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		files = append(files, newPkgFile("assets/charts/"+n+".svg", "chart", producers["report"], "", []byte(charts[n]+"\n")))
	}
	files = append(files, packaged...)
	for _, af := range designreport.AnalogPackageFiles(in.Analog, o.analog) {
		b, err := os.ReadFile(af.Src)
		if err != nil {
			continue
		}
		files = append(files, newPkgFile(af.Dest, "data", "pcbpilot sim analog", af.Src, b))
	}
	var notes []string
	if ef, note := elmerFiles(in.Post, o.post); note != "" {
		notes = append(notes, note)
	} else {
		files = append(files, ef...)
	}
	man := &reportManifest{SchemaVersion: 1, Generator: designreport.Generator, Project: rep.Project, Version: label,
		GeneratedAt: rep.GeneratedAt, Verdict: rep.Verdict.Status, Notes: notes}
	zipPath := filepath.Join(o.outDir, zipFile)
	if !o.noZip {
		man.Zip = zipFile
	}
	when, _ := time.Parse(time.RFC3339, rep.GeneratedAt)
	if o.noZip {
		zipPath = filepath.Join(os.TempDir(), "pcbpilot-report-discard.zip")
	}
	if err := writeReportPackage(dir, zipPath, man, files, when); err != nil {
		return "", nil, err
	}
	if o.noZip {
		_ = os.Remove(zipPath)
	}
	if idx.Project == "" {
		idx.Project = rep.Project
	}
	if rep.Customer != "" {
		idx.Customer = rep.Customer
	}
	idx.Put(cur)
	ib, _ := json.MarshalIndent(idx, "", "  ")
	if err := os.WriteFile(idxPath, append(ib, '\n'), 0o644); err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(filepath.Join(o.outDir, "CHANGELOG.md"), []byte(designreport.RenderChangelog(idx)), 0o644); err != nil {
		return "", nil, err
	}
	fmt.Fprintf(stderr, "design report %s: %s (%d warning reason(s), %d fail reason(s)); %d section(s) not available\n",
		label, rep.Verdict.Status, len(rep.Verdict.Warnings), len(rep.Verdict.Reasons), len(rep.Missing))
	return dir, rep, nil
}
