package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/pkg/designreport"
	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

func newIntentCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	var window string
	c := &cobra.Command{
		Use:   "intent",
		Short: "Electrical design intent derived from the finished schematic (intent.json)",
		Long: `Design intent = what every circuit is for and the electrical plan that the
PCB must honour: per-block function and summary, per-net voltage / current /
width / vias / clearance / impedance / net class with the reasons, voltage
domains, insulation pairs between domains, net classes and designer findings.

  intent derive   schematic (+ DC power simulation, + product spec) → intent.json

intent.json (schemaVersion 1) is the fixed contract consumed by the EasyEDA
rule push, pcb auto, the safety checker and the feedback loop.`,
	}
	c.PersistentFlags().StringVar(&window, "window", "", "EasyEDA window ID (live mode)")
	c.AddCommand(newIntentDeriveCmd(cfg, &window, stdout, stderr))
	return c
}

func newIntentDeriveCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var connPaths, valuePaths, modelPaths, pages, scenarios, switches []string
	var simPath, specPath, modelsLib, outPath, reportPath, simOut, boardPath, reportDir, reportName string
	var strict bool
	c := &cobra.Command{
		Use:   "derive",
		Short: "Derive intent.json (+ intent.md) from schematic connectivity, part values, power sim and spec",
		Args:  cobra.NoArgs,
		Long: `Turn a finished, accepted schematic into its electrical DESIGN INTENT: the
explained plan every later step (EasyEDA rule push, pcb auto, safety, feedback)
consumes, so the PCB no longer only knows positions.

WHAT IT DERIVES
  blocks[]      what each circuit is for: power-input (OR-ing diodes, TVS),
                buck/boost/ldo (Vout from the real divider, η, loss), charger,
                usb-uart, mcu, rf-module, esd, led, connector, isolation, mains,
                sensor, motor-driver, other (keys, auto-download, …); core,
                parts, owned nets and a one-line summary with the simulated
                voltage/current/power. Built on pcbauto.Understand (core →
                auxiliary members) with recognised sub-functions carved out.
  nets{}        per net: role (power|ground|signal|switch|hs|diff|rf|analog|
                clock), domain, block, voltage {nom (typical), min, max, peak —
                a switch node peaks at Vin, AC at √2·Vrms}, currentA with
                currentSource (simulated|declared|heuristic), per-pin currents
                with dir, widthMil {outer, inner, min} from IPC-2221/2152
                (1 oz outer / 0.5 oz inner, ΔT 10 °C unless the spec says
                otherwise), viasPerTransition, clearanceMil (IPC-2221B at the
                peak voltage, never below the fab clearance), impedanceOhm /
                diffPair / lengthGroup (USB 90 Ω, Ethernet/HDMI/MIPI 100 Ω,
                PCIe 85 Ω … solved on the JLC 4-layer stackup), netClass, why[].
  domains[]     reference domains (one per ground; MAINS; floating) with kind
                SELV|hazardous|mains|patient|floating|isolated-secondary and the
                working Vrms / Vpeak (> 60 V DC = hazardous).
  pairs[]       insulation between domains an isolation part bridges
                (optocoupler, isolator, isolated DC/DC, transformer, relay):
                working voltage, insulation grade, clearance/creepage mm and
                milled-slot need, all from SafetyDistances(pair, standard).
  netClasses[]  GND, POWER, POWER_HI (> 1 A), SWITCH, HS_DIFF, HS, RF, HV_<domain>,
                SIGNAL with track / clearance / via — ready for a rule push.
  findings[]    designer hints: inductor Ipk/Irms vs rating, regulator
                headroom/dropout/duty/Vin max/current, diode Vf loss, pin and
                connector current ratings, resistor power, capacitor voltage,
                missing bulk capacitance, USB 500 mA budget, missing USB ESD,
                uncontrollable impedance, insulation slots, unknown power models.

INPUTS
  Offline: --connectivity (repeat per page; pcbpilot sch connectivity JSON) +
  --values (pcbpilot sch list JSON or {"parts":{ref:{value,mpn,lcsc}}}); the DC
  power simulation then runs in-process with the skill's power-models.json
  (--models / --models-lib as in 'sim power'). --sim sim.json uses an existing
  'pcbpilot sim power' document instead of simulating; --sim alone (no
  connectivity) rebuilds the netlist from the sim's pin lists (no part values:
  weaker classification and no description-based ratings).
  --board board.json (no schematic available, e.g. a reference PCB): the
  netlist is rebuilt from the PCB pads; pin names and part values are unknown,
  so currents/ratings are heuristic (finding netlist-from-board) while pairs,
  interfaces, impedance widths and length groups come from the net names and
  the board's layer count.
  Live (no --connectivity/--sim): reads every schematic page (or --pages) of the
  connected --project with sch connectivity + sch list, restores the original
  page and simulates in-process. Read-only: nothing is written to the editor.
  --spec spec.json (all optional): {"standard":{"name":"IEC62368-1",
  "insulation":"reinforced","mop":"","pollutionDegree":2,"materialGroup":"IIIa",
  "altitudeM":2000,"overvoltageCategory":"II","coated":false},"layers":4,
  "outerOz":1,"innerOz":0.5,"tempRiseC":10,"rails":[{"net":"+3V3","voltage":3.3,
  "currentA":0.8,"rippleMvpp":30,"peakV":0}],"hsInterfaces":[{"name":"ETH",
  "pairs":[["TXP","TXN"]],"diffOhm":100,"lengthGroup":"ETH_TX"}],"mains":{"vrms":230,
  "nets":["L","N"]},"domains":[{"kind":"patient","nets":["ECG_IN"]}],
  "usbBudgetA":0.5,"rules":{"clearanceMil":6,"trackMil":6,"viaDrillMil":12,
  "viaDiaMil":24}}. Declared rail currents override the simulation (reported
  as "declared"; a declaration below the simulated value is a finding).

OUTPUT
  --out intent.json (default stdout), --report intent.md (human reading),
  --sim-out sim.json (the in-process simulation, reusable by pcb auto --sim).
  A one-line summary goes to stderr. --strict exits non-zero when a finding
  has severity error.`,
		Example: `  # offline: exported pages + values (simulates in-process)
  pcbpilot intent derive --connectivity sch-p1.json --connectivity sch-p2.json \
      --values sch-list.json --out intent.json --report intent.md

  # reference PCB without its schematic: netlist from the pads
  pcbpilot intent derive --board board.json --out intent.json --report intent.md

  # reuse an existing simulation and add the product spec
  pcbpilot intent derive --connectivity c.json --values v.json --sim sim.json \
      --spec spec.json --out intent.json

  # live project (read-only), keep the simulation for pcb auto --sim
  pcbpilot --project ceshi intent derive --pages P1,P2 --out intent.json \
      --report intent.md --sim-out sim.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(connPaths) > 0 && len(pages) > 0 {
				return fmt.Errorf("--pages is for live mode; do not combine it with --connectivity")
			}
			if len(connPaths) == 0 && len(valuePaths) > 0 {
				return fmt.Errorf("--values needs --connectivity (live mode reads values itself)")
			}
			if boardPath != "" && (len(connPaths) > 0 || len(pages) > 0 || simPath != "") {
				return fmt.Errorf("--board rebuilds the netlist from PCB pads; do not combine it with --connectivity, --pages or --sim")
			}
			sw, err := parseSimSwitches(switches)
			if err != nil {
				return err
			}
			in := intent.Input{SimOptions: powersim.Options{Scenarios: scenarios, Switches: sw}}
			libs, libNames, err := loadSimLibraries(modelsLib, modelPaths, stderr)
			if err != nil {
				return err
			}
			in.Libs, in.Sources.Models = libs, libNames
			if specPath != "" {
				b, err := os.ReadFile(specPath)
				if err != nil {
					return err
				}
				if in.Spec, err = intent.ParseSpec(b); err != nil {
					return fmt.Errorf("%s: %w", specPath, err)
				}
				in.Sources.Spec = specPath
			}
			if simPath != "" {
				b, err := os.ReadFile(simPath)
				if err != nil {
					return err
				}
				if in.Sim, err = intent.ParseSimOutput(b); err != nil {
					return fmt.Errorf("%s: %w", simPath, err)
				}
				in.Sources.Sim = simPath
			}
			var connDocs [][]byte
			values := map[string]powersim.PartValues{}
			switch {
			case boardPath != "":
				raw, err := os.ReadFile(boardPath)
				if err != nil {
					return err
				}
				b, err := pcbauto.FromSnapshot(raw)
				if err != nil {
					return fmt.Errorf("%s: %w", boardPath, err)
				}
				in.Design, in.BoardLayers = intent.DesignFromBoard(b), b.CopperLayers
				in.Sources.Schematic = []string{"(netlist from PCB pads: " + boardPath + "; part values unknown → heuristic currents/ratings)"}
				fmt.Fprintf(stderr, "board: %d parts, %d copper layers — netlist from pads, values unknown (heuristics, marked netlist-from-board)\n", len(in.Design.Parts), b.CopperLayers)
			case len(connPaths) > 0:
				for _, p := range connPaths {
					b, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					connDocs = append(connDocs, b)
				}
				for _, p := range valuePaths {
					b, err := os.ReadFile(p)
					if err != nil {
						return err
					}
					v, err := powersim.ParseValues(b)
					if err != nil {
						return fmt.Errorf("%s: %w", p, err)
					}
					for ref, pv := range v {
						values[ref] = pv
					}
				}
				in.Sources.Schematic = append(append([]string{}, connPaths...), valuePaths...)
			case in.Sim != nil && len(pages) == 0:
				in.Sources.Schematic = []string{"(rebuilt from " + simPath + " pin lists)"}
			default:
				docs, vals, desc, err := collectSimLive(cfg, *window, pages)
				if err != nil {
					return fmt.Errorf("live read failed (use --connectivity/--values or --sim for offline input): %w", err)
				}
				connDocs, values = docs, vals
				in.Sources.Schematic = []string{fmt.Sprintf("live project %s: %s", cfg.project, desc)}
			}
			if len(connDocs) > 0 {
				var parsed []*powersim.ConnDoc
				for i, b := range connDocs {
					d, err := powersim.ParseConnectivity(b)
					if err != nil {
						name := fmt.Sprintf("page %d", i+1)
						if i < len(connPaths) {
							name = connPaths[i]
						}
						return fmt.Errorf("%s: %w", name, err)
					}
					parsed = append(parsed, d)
				}
				design, dwarn, err := powersim.BuildDesign(parsed, values)
				if err != nil {
					return err
				}
				for _, w := range dwarn {
					fmt.Fprintln(stderr, "warning:", w)
				}
				in.Design = design
			}
			if in.Sim == nil {
				out, _, err := powersim.Simulate(in.Design, libs, in.SimOptions)
				if err != nil {
					return fmt.Errorf("power simulation: %w", err)
				}
				out.Inputs = &powersim.Inputs{Connectivity: connPaths, Values: valuePaths, Libraries: libNames}
				in.Sim = out
				in.Sources.Sim = "in-process (" + powersim.Generator + ")"
				if simOut != "" {
					b, _ := json.MarshalIndent(out, "", "  ")
					if err := os.WriteFile(simOut, append(b, '\n'), 0o644); err != nil {
						return err
					}
					in.Sources.Sim = simOut + " (in-process " + powersim.Generator + ")"
				}
			} else if simOut != "" {
				return fmt.Errorf("--sim-out writes the in-process simulation; it has no meaning with --sim")
			}
			doc, err := intent.Derive(in)
			if err != nil {
				return err
			}
			b, _ := json.MarshalIndent(doc, "", "  ")
			if outPath != "" {
				if err := os.WriteFile(outPath, append(b, '\n'), 0o644); err != nil {
					return err
				}
			} else if _, err := fmt.Fprintln(stdout, string(b)); err != nil {
				return err
			}
			if reportPath != "" {
				f, err := os.Create(reportPath)
				if err != nil {
					return err
				}
				werr := intent.WriteReport(f, doc)
				if cerr := f.Close(); werr == nil {
					werr = cerr
				}
				if werr != nil {
					return werr
				}
			}
			counts := map[string]int{}
			for _, f := range doc.Findings {
				counts[f.Severity]++
			}
			var classes []string
			for _, nc := range doc.NetClasses {
				classes = append(classes, fmt.Sprintf("%s(%d)", nc.Name, len(nc.Nets)))
			}
			fmt.Fprintf(stderr, "intent: %d blocks, %d nets, %d domains, %d pairs; classes %s; findings %d error / %d warn / %d info\n",
				len(doc.Blocks), len(doc.Nets), len(doc.Domains), len(doc.Pairs), strings.Join(classes, " "), counts["error"], counts["warn"], counts["info"])
			if reportDir != "" {
				// P11 pre-layout version: intent + simulation (+ board/values).
				if outPath == "" {
					return fmt.Errorf("--report-dir needs --out (the report reads intent.json from disk)")
				}
				simFile := simPath
				if simFile == "" {
					simFile = simOut
				}
				if simFile == "" {
					return fmt.Errorf("--report-dir with an in-process simulation needs --sim-out (the report reads sim.json from disk)")
				}
				models := ""
				if len(libNames) > 0 {
					models = libNames[len(libNames)-1]
				}
				values := ""
				if len(valuePaths) == 1 {
					values = valuePaths[0]
				}
				dir, dr, err := runDesignReport(designReportOpts{outDir: reportDir, version: "auto", project: reportName, intent: outPath, sim: simFile,
					models: models, values: values, maxImageBytes: designreport.DefaultMaxImageBytes}, stderr)
				if err != nil {
					return fmt.Errorf("design report: %w", err)
				}
				fmt.Fprintf(stdout, "wrote %s/{report.html,report.md,report.json} — %s %s\n", dir, dr.VersionLabel, dr.Verdict.Status)
			}
			if strict && counts["error"] > 0 {
				return fmt.Errorf("%d error finding(s) (--strict)", counts["error"])
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringArrayVar(&connPaths, "connectivity", nil, "offline: 'pcbpilot sch connectivity' JSON `file` (repeat per page; merged by net name)")
	f.StringArrayVar(&valuePaths, "values", nil, "offline: 'pcbpilot sch list' JSON or {\"parts\":{ref:{value,mpn,lcsc}}} values `file` (repeatable)")
	f.StringVar(&boardPath, "board", "", "offline without a schematic: 'pcb dump' JSON `file` — netlist from the PCB pads (footprint name as device, values unknown → heuristic currents, marked netlist-from-board); layers default to the board's")
	f.StringVar(&simPath, "sim", "", "existing 'pcbpilot sim power' JSON (skips the in-process simulation; alone = netlist from its pin lists)")
	f.StringVar(&specPath, "spec", "", "product spec JSON: standard, insulation, MOP, pollution degree, altitude, layers/copper, declared rails (+ripple), HS interfaces, mains, domains, USB budget, fab rules")
	f.StringSliceVar(&pages, "pages", nil, "live: schematic pages (name or uuid, comma-separated); default all pages")
	f.StringArrayVar(&modelPaths, "models", nil, "extra power-models JSON consulted before the skill library (repeatable)")
	f.StringVar(&modelsLib, "models-lib", "", "base power-models.json (default: the installed skill's references/power-models.json; \"none\" = no base library)")
	f.StringSliceVar(&scenarios, "scenario", nil, "in-process sim: only these scenarios (typical, peak, buttons-pressed, <source>-only, worst)")
	f.StringArrayVar(&switches, "switch", nil, "in-process sim: force a switch in every scenario: REF=closed|open (repeatable)")
	f.StringVar(&outPath, "out", "", "write intent.json here (default stdout)")
	f.StringVar(&reportPath, "report", "", "write the Markdown reading (blocks, domains, pairs, nets, classes, findings)")
	f.StringVar(&simOut, "sim-out", "", "also write the in-process simulation JSON (for pcb auto --sim)")
	f.BoolVar(&strict, "strict", false, "exit non-zero when any finding has severity error")
	f.StringVar(&reportDir, "report-dir", "", "also publish the next (pre-layout) design-report version here (reports/<name>/; needs --out and --sim or --sim-out; see 'pcbpilot report design')")
	f.StringVar(&reportName, "report-name", "", "project name on the report cover (default: the report dir name)")
	return c
}
