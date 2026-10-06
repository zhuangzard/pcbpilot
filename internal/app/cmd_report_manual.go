package app

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/internal/version"
	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
	"github.com/zhuangzard/pcbpilot/pkg/boardmanual"
	"github.com/zhuangzard/pcbpilot/pkg/designreport"
	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/postsim"
	"github.com/zhuangzard/pcbpilot/pkg/projectconfig"
)

// boardManualOpts are the inputs of `report manual`.
type boardManualOpts struct {
	board, intent, sim, notes, pinMap string
	post, analog                      string
	out, lang, date                   string
	project, doc                      string
	notesOptional                     bool
	// versioned gate mode
	outDir, projectConfig string
	gate                  bool
}

func newReportManualCmd(stdout, stderr io.Writer) *cobra.Command {
	var o boardManualOpts
	c := &cobra.Command{
		Use:   "manual",
		Short: "Generate the board USER MANUAL (one self-contained HTML: board picture, connector pinouts, power, I/O, LEDs, tests, bring-up, software)",
		Args:  cobra.NoArgs,
		Long: `Builds a human-readable USER MANUAL for one board from files only (offline:
never contacts the editor or daemon) and writes ONE self-contained HTML file
(inline CSS and SVG, no external fetches, prints on A4).

INPUTS
  --board     (required) pcb dump --include-copper JSON: parts with bbox and
              pads (number, net, position, size, shape), outline, holes
  --intent    intent derive output: net roles (power/ground/signal), domains
  --sim       sim power output: rail voltages, per-pin currents per scenario
  --notes     project notes.json: the human text (names, purposes, mating
              connectors, cautions, I/O levels, LEDs, test steps, software
              interface …), merged on top of the automatic data; unknown
              fields are an error; write "TODO" for unknown values
  --pin-map   Quartus .tcl/.qsf (set_location_assignment) or Vivado .xdc
              (PACKAGE_PIN): the programmable-device pin table, each pin
              checked against the board net of that pad

SECTIONS (generated where the data exists, from --notes otherwise)
   1 概览           title, revision, size from the outline, layers, parts,
                    holes, generation date, source files
   2 板卡俯视图     outline, holes, every part as a light bbox, connectors
                    (J*/CN*/P<n>/JP*, or a connector-like footprint) with
                    numbered callouts and leader lines, pin 1, edges 左右上下,
                    10 mm scale bar
   3 接口详细说明   per connector: zoomed pad drawing at true position and
                    shape (power red, ground black, signal blue, NC grey),
                    pin table pin / net / role / voltage / max current /
                    direction / doc net / note; purpose, mating, cautions
   4 电源要求       input connector, voltage, typical / peak / worst current
                    (sim), recommended rating = peak × margin; rails table
   5 输入与输出     external signals per connector pin
   6 LED 指示灯     zoomed LED picture + per-firmware behaviour table; the
                    notes' anode net is checked against the board
   7 注意事项       notes cautions + automatic ones: max current per power
                    pin, non-SELV domains, mixed power+signal connectors,
                    input current > 2 A, document mismatches
   8 仪器设备  9 测量点（编号探针图 + 电源轨/关键信号表） 10 上电步骤
  11 软件接口（命令、遥测、引脚表） 12 故障排查
  13 文档与板数据对账: every docPins / expectedPins / LED net / probe /
                    pin-map entry that disagrees with the board netlist
  14 TODO  15 数据来源 (path + sha256)

Schema of notes.json and the review checklist:
.agents/skills/pcbpilot/references/board-manual.md`,
		Example: `  pcbpilot report manual --board board-final.json --intent intent.json \
      --sim sim.json --notes notes.json --pin-map board_pins.tcl \
      --out 05_Output/Board_使用说明.html

  # English labels
  pcbpilot report manual --board board.json --out manual-en.html --lang en`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.outDir != "" {
				if o.board == "" {
					return fmt.Errorf("--board is required")
				}
				g, run := runManualGate(manualGateOpts{board: o.board, intent: o.intent, sim: o.sim, post: o.post, analog: o.analog, projectConfig: o.projectConfig,
					notes: o.notes, pinMap: o.pinMap, outDir: o.outDir, project: o.project, doc: o.doc, date: o.date, lang: o.lang}, stderr)
				res := map[string]any{"gates": []gateResult{g}, "pass": g.Pass}
				if run != nil {
					res["manual"] = run
				}
				_ = writeJSON(stdout, res)
				if !g.Pass && o.gate {
					return fmt.Errorf("gate failed: %s", boardmanual.GateName)
				}
				return nil
			}
			if o.out == "" {
				return fmt.Errorf("--out (single file) or --out-dir (versioned + gate) is required")
			}
			m, err := runBoardManual(o)
			if err != nil {
				return err
			}
			fmt.Fprintf(stdout, "wrote %s — %d connector(s), %d rail probe(s), %d mismatch(es)\n", o.out, len(m.Connectors), len(m.RailProbes), len(m.Checks))
			for _, ch := range m.Checks {
				fmt.Fprintf(stderr, "mismatch %s: doc %q board %q (%s)\n", ch.Where, ch.Doc, ch.Board, ch.Note)
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&o.board, "board", "", "board dump JSON (pcb dump --include-copper) (required)")
	f.StringVar(&o.intent, "intent", "", "intent.json (pcbpilot intent derive)")
	f.StringVar(&o.sim, "sim", "", "sim.json (pcbpilot sim power)")
	f.StringVar(&o.notes, "notes", "", "notes.json with the project-specific human text")
	f.StringVar(&o.post, "post", "", "post.json (pcbpilot sim post-layout) of this board: thermal / IR drop / via current; its temp-TOP/BOTTOM heat maps are embedded (required by the board-manual gate, same board sha256)")
	f.StringVar(&o.analog, "analog", "", "analog.json (pcbpilot sim analog): filter fc / Q / gain vs target (default: pcbpilot.project.json manual.analog in --out-dir mode)")
	f.StringVar(&o.pinMap, "pin-map", "", "FPGA/CPLD pin assignments: Quartus .tcl/.qsf or Vivado .xdc")
	f.StringVar(&o.out, "out", "", "output HTML file (single-file mode)")
	f.StringVar(&o.outDir, "out-dir", "", "versioned mode, as pcb gate / pcb auto route run it: <out-dir>/manual/{vN/<Board>_使用说明.html, <Board>_使用说明.html, index.json} + the board-manual gate (notes / pin map / name / extra copy from pcbpilot.project.json \"manual\")")
	f.StringVar(&o.projectConfig, "project-config", "", "pcbpilot.project.json (file or work dir) for --out-dir; default ./pcbpilot.project.json when present, \"none\" to ignore")
	f.BoolVar(&o.gate, "gate", false, "with --out-dir: exit non-zero when the board-manual gate fails")
	f.StringVar(&o.project, "project-name", "", "project shown in the header")
	f.StringVar(&o.doc, "doc-name", "", "PCB document shown in the header")
	f.StringVar(&o.lang, "lang", "zh", "label language: zh | en")
	f.StringVar(&o.date, "date", "", "generation date override (RFC3339); default SOURCE_DATE_EPOCH or now")
	return c
}

// loadManualInputs reads the manual's input files. A missing notes file is
// not an error here (notesMissing=true): the board-manual gate reports it.
func loadManualInputs(o boardManualOpts) (in boardmanual.Inputs, notesMissing bool, err error) {
	in = boardmanual.Inputs{Lang: o.lang, Tool: version.Version, Project: o.project, Doc: o.doc}
	if in.Lang == "" {
		in.Lang = "zh"
	}
	if in.GeneratedAt, err = reportTime(o.date); err != nil {
		return in, false, err
	}
	read := func(kind, path string) ([]byte, error) {
		if path == "" {
			return nil, nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("--%s: %w", kind, err)
		}
		h := sha256.Sum256(b)
		in.Sources = append(in.Sources, boardmanual.Source{Kind: kind, Path: path, SHA256: hex.EncodeToString(h[:]), Bytes: len(b)})
		return b, nil
	}
	b, err := read("board", o.board)
	if err != nil {
		return in, false, err
	}
	if in.Board, err = boardmanual.ParseBoard(b); err != nil {
		return in, false, fmt.Errorf("%s: %w", o.board, err)
	}
	if in.BoardSHA = in.Board.SemanticSHA256; in.BoardSHA == "" {
		in.BoardSHA = in.Sources[0].SHA256
	}
	if b, err := read("intent", o.intent); err != nil {
		return in, false, err
	} else if b != nil {
		var it intent.Intent
		if err := json.Unmarshal(b, &it); err != nil {
			return in, false, fmt.Errorf("%s: %w", o.intent, err)
		}
		in.Intent = &it
	}
	if b, err := read("sim", o.sim); err != nil {
		return in, false, err
	} else if b != nil {
		if in.Sim, err = intent.ParseSimOutput(b); err != nil {
			return in, false, fmt.Errorf("%s: %w", o.sim, err)
		}
	}
	if o.notes != "" {
		if _, serr := os.Stat(o.notes); os.IsNotExist(serr) && o.notesOptional {
			notesMissing = true
		} else if b, err := read("notes", o.notes); err != nil {
			return in, false, err
		} else if in.Notes, err = boardmanual.ParseNotes(b); err != nil {
			return in, false, fmt.Errorf("%s: %w", o.notes, err)
		}
	} else {
		notesMissing = true
	}
	if b, err := read("post", o.post); err != nil {
		return in, false, err
	} else if b != nil {
		if in.Post, err = designreport.ParsePost(b); err != nil {
			return in, false, fmt.Errorf("%s: %w", o.post, err)
		}
		in.HeatMaps = manualHeatMaps(in.Post, o.post)
	}
	if b, err := read("analog", o.analog); err != nil {
		return in, false, err
	} else if b != nil {
		var a analogsim.Output
		if err := json.Unmarshal(b, &a); err != nil {
			return in, false, fmt.Errorf("%s: %w", o.analog, err)
		}
		in.Analog = &a
	}
	if b, err := read("pin-map", o.pinMap); err != nil {
		return in, false, err
	} else if b != nil {
		if in.PinMap, err = boardmanual.ParsePinMap(b); err != nil {
			return in, false, fmt.Errorf("%s: %w", o.pinMap, err)
		}
	}
	return in, notesMissing, nil
}

// manualHeatMaps embeds the TOP and BOTTOM temperature maps of post.json
// (mapsDir as written, else next to post.json).
func manualHeatMaps(p *postsim.Result, postPath string) []boardmanual.HeatMap {
	var out []boardmanual.HeatMap
	dirs := []string{p.MapsDir, filepath.Join(filepath.Dir(postPath), filepath.Base(p.MapsDir)), filepath.Join(filepath.Dir(postPath), "heatmaps")}
	for _, mf := range p.Maps {
		if mf.Kind != "temperature" || (mf.Layer != "TOP" && mf.Layer != "BOTTOM") {
			continue
		}
		for _, d := range dirs {
			if d == "" {
				continue
			}
			if b, err := os.ReadFile(filepath.Join(d, mf.File)); err == nil {
				out = append(out, boardmanual.HeatMap{Layer: mf.Layer, Kind: mf.Kind, DataURI: "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(b)})
				break
			}
		}
	}
	return out
}

func runBoardManual(o boardManualOpts) (*boardmanual.Manual, error) {
	if o.board == "" || o.out == "" {
		return nil, fmt.Errorf("--board and --out are required")
	}
	if o.lang != "zh" && o.lang != "en" {
		return nil, fmt.Errorf("--lang %q: want zh or en", o.lang)
	}
	in, _, err := loadManualInputs(o)
	if err != nil {
		return nil, err
	}
	m := boardmanual.Build(in)
	html, err := boardmanual.RenderHTML(m)
	if err != nil {
		return nil, fmt.Errorf("render: %w", err)
	}
	if dir := filepath.Dir(o.out); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if err := os.WriteFile(o.out, html, 0o644); err != nil {
		return nil, err
	}
	return m, nil
}

// manualGateOpts are the inputs of the board-manual gate.
type manualGateOpts struct {
	board, intent, sim string
	post, analog       string
	projectConfig      string // "" = ./pcbpilot.project.json when present, "none", or a path
	notes, pinMap      string // overrides of the project config
	outDir             string // the run's --out-dir; the manual goes to <outDir>/manual/
	project, doc, date string
	noManual           bool
	waivers            []gateWaiver
	lang               string
}

// manualRun is what the gate produced.
type manualRun struct {
	Dir, Current, VerFile, Extra, Notes string
	Version                             int
	Bumped                              bool
}

// noManualMatch is the waiver match that allows --no-manual.
const noManualMatch = "--no-manual"

// checkNoManual refuses --no-manual unless a signed waiver covers it.
func checkNoManual(noManual bool, ws []gateWaiver) error {
	if !noManual {
		return nil
	}
	for _, w := range ws {
		if w.Gate == boardmanual.GateName && strings.Contains(noManualMatch, w.Match) {
			return nil
		}
	}
	return fmt.Errorf("--no-manual skips the %s gate: it needs a signed waiver in --waivers {\"gate\":%q,\"match\":%q,\"reason\":…,\"by\":…}", boardmanual.GateName, boardmanual.GateName, noManualMatch)
}

// runManualGate builds, checks and publishes the board manual (versioned
// under <outDir>/manual/) and returns the board-manual gate result. It never
// returns an error: every failure is a gate item.
func runManualGate(o manualGateOpts, stderr io.Writer) (gateResult, *manualRun) {
	g := gateResult{Gate: boardmanual.GateName}
	fail := func(f string, a ...any) (gateResult, *manualRun) {
		g.Items = append(g.Items, fmt.Sprintf(f, a...))
		g.Detail = "board manual not generated"
		applyWaivers(&g, o.waivers)
		return g, nil
	}
	if o.noManual {
		g.Items = []string{"manual generation skipped (" + noManualMatch + ")"}
		g.Detail = "skipped by --no-manual"
		applyWaivers(&g, o.waivers)
		return g, nil
	}
	// project config → notes / pin map / name / extra copy
	projDir, mc := ".", &projectconfig.Manual{}
	name := ""
	if o.projectConfig != "none" {
		dir := o.projectConfig
		load := projectconfig.Load
		if dir == "" {
			dir = "."
		} else if st, err := os.Stat(dir); err == nil && !st.IsDir() {
			// A file names the config itself (pcbpilot.project.B.json):
			// loading <dir>/pcbpilot.project.json instead gave board B
			// board A's config and overwrote A's manual.
			file := dir
			dir = filepath.Dir(dir)
			load = func(string) (*projectconfig.Config, error) { return projectconfig.LoadFile(file) }
		}
		c, err := load(dir)
		switch {
		case err == nil:
			projDir, name = dir, c.Name
			if c.Manual != nil {
				mc = c.Manual
			}
		case errors.Is(err, projectconfig.ErrNotFound) && o.projectConfig == "":
		default:
			return fail("project config: %v", err)
		}
	}
	rel := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(projDir, p)
	}
	notes := o.notes
	if notes == "" {
		notes = rel(firstNonEmptyStr(mc.Notes, projectconfig.ManualNotesFile))
	}
	pinMap := o.pinMap
	if pinMap == "" {
		pinMap = rel(mc.PinMap)
	}
	lang := firstNonEmptyStr(o.lang, mc.Lang, "zh")
	analog := o.analog
	if analog == "" {
		analog = rel(mc.Analog)
	}
	in, notesMissing, err := loadManualInputs(boardManualOpts{board: o.board, intent: o.intent, sim: o.sim, post: o.post, analog: analog, notes: notes, notesOptional: true,
		pinMap: pinMap, lang: lang, date: o.date, project: o.project, doc: o.doc})
	if err != nil {
		return fail("generation failed: %v", err)
	}
	m := boardmanual.Build(in)
	var notesDoc *boardmanual.Notes
	if !notesMissing {
		notesDoc = in.Notes
	}
	items := boardmanual.GateCheck(m, notesDoc)
	if notesMissing {
		items[0] = "notes file missing: " + notes
	}
	if name = firstNonEmptyStr(mc.Name, name, m.Title); name == "PCB" {
		name = "board"
	}
	dir := filepath.Join(o.outDir, "manual")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fail("write failed: %v", err)
	}
	pub, err := boardmanual.Publish(dir, name, m, len(items) == 0, boardmanual.RenderHTML)
	if err != nil {
		return fail("generation/write failed: %v", err)
	}
	run := &manualRun{Dir: dir, Current: pub.Current, VerFile: pub.VerFile, Version: pub.Version, Bumped: pub.Bumped, Notes: notes}
	if mc.Out != "" {
		run.Extra = rel(mc.Out)
		// The extra copy is a published file of one board: write it only
		// when the config names this board's document.
		if why := manualOutRefusal(mc.Doc, o.doc); why != "" {
			items = append(items, fmt.Sprintf("manual.out %s not written: %s", run.Extra, why))
			run.Extra = ""
		} else if b, err := os.ReadFile(pub.Current); err != nil {
			items = append(items, "write failed: "+err.Error())
		} else if err := os.MkdirAll(filepath.Dir(run.Extra), 0o755); err != nil {
			items = append(items, "write failed: "+err.Error())
		} else if err := os.WriteFile(run.Extra, b, 0o644); err != nil {
			items = append(items, "write failed: "+err.Error())
		}
	}
	g.Items, g.Pass = items, len(items) == 0
	bump := "new"
	if !pub.Bumped {
		bump = "same board sha256, rebuilt"
	}
	g.Detail = fmt.Sprintf("v%d (%s): %s — %d connector(s), %d problem(s)", pub.Version, bump, pub.Current, len(m.Connectors), len(items))
	applyWaivers(&g, o.waivers)
	fmt.Fprintf(stderr, "board manual v%d → %s (%s)\n", pub.Version, pub.Current, map[bool]string{true: "PASS", false: "FAIL"}[g.Pass])
	return g, run
}

// addManualFlags registers the board-manual flags of pcb gate / pcb auto route.
func addManualFlags(c *cobra.Command, noManual *bool, projectConfig *string) {
	c.Flags().BoolVar(noManual, "no-manual", false, "skip the board user manual (board-manual gate); refused unless --waivers holds a signed {\"gate\":\"board-manual\",\"match\":\"--no-manual\"} entry")
	c.Flags().StringVar(projectConfig, "project-config", "", "pcbpilot.project.json (file or work dir) whose \"manual\" block gives the manual notes / pin map / name / extra copy; default ./pcbpilot.project.json when present, \"none\" to ignore")
}

// manualOutRefusal says why manual.out may not be written for the board doc
// ("" = it may): the config must name the board's document uuid.
func manualOutRefusal(cfgDoc, boardDoc string) string {
	switch {
	case cfgDoc == "":
		return "the project config's manual.doc is empty (set it to the board's PCB document uuid)"
	case boardDoc == "":
		return fmt.Sprintf("the board document is unknown (pass --doc %s)", cfgDoc)
	case !strings.EqualFold(cfgDoc, boardDoc):
		return fmt.Sprintf("the config is for document %s, this board is %s", cfgDoc, boardDoc)
	}
	return ""
}
