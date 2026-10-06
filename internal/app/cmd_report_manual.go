package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/internal/version"
	"github.com/zhuangzard/pcbpilot/pkg/boardmanual"
	"github.com/zhuangzard/pcbpilot/pkg/intent"
)

// boardManualOpts are the inputs of `report manual`.
type boardManualOpts struct {
	board, intent, sim, notes, pinMap string
	out, lang, date                   string
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
	f.StringVar(&o.pinMap, "pin-map", "", "FPGA/CPLD pin assignments: Quartus .tcl/.qsf or Vivado .xdc")
	f.StringVar(&o.out, "out", "", "output HTML file (required)")
	f.StringVar(&o.lang, "lang", "zh", "label language: zh | en")
	f.StringVar(&o.date, "date", "", "generation date override (RFC3339); default SOURCE_DATE_EPOCH or now")
	return c
}

func runBoardManual(o boardManualOpts) (*boardmanual.Manual, error) {
	if o.board == "" || o.out == "" {
		return nil, fmt.Errorf("--board and --out are required")
	}
	if o.lang != "zh" && o.lang != "en" {
		return nil, fmt.Errorf("--lang %q: want zh or en", o.lang)
	}
	in := boardmanual.Inputs{Lang: o.lang, Tool: version.Version}
	var err error
	if in.GeneratedAt, err = reportTime(o.date); err != nil {
		return nil, err
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
		return nil, err
	}
	if in.Board, err = boardmanual.ParseBoard(b); err != nil {
		return nil, fmt.Errorf("%s: %w", o.board, err)
	}
	if b, err := read("intent", o.intent); err != nil {
		return nil, err
	} else if b != nil {
		var it intent.Intent
		if err := json.Unmarshal(b, &it); err != nil {
			return nil, fmt.Errorf("%s: %w", o.intent, err)
		}
		in.Intent = &it
	}
	if b, err := read("sim", o.sim); err != nil {
		return nil, err
	} else if b != nil {
		if in.Sim, err = intent.ParseSimOutput(b); err != nil {
			return nil, fmt.Errorf("%s: %w", o.sim, err)
		}
	}
	if b, err := read("notes", o.notes); err != nil {
		return nil, err
	} else if b != nil {
		if in.Notes, err = boardmanual.ParseNotes(b); err != nil {
			return nil, fmt.Errorf("%s: %w", o.notes, err)
		}
	}
	if b, err := read("pin-map", o.pinMap); err != nil {
		return nil, err
	} else if b != nil {
		if in.PinMap, err = boardmanual.ParsePinMap(b); err != nil {
			return nil, fmt.Errorf("%s: %w", o.pinMap, err)
		}
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
