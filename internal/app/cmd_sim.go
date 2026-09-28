package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/internal/connectivity"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// powerModelsAsset is the skill's part power-model library.
var powerModelsAsset = skillAsset{
	name:     "power-models.json",
	rels:     []string{"pcbpilot/references/power-models.json"},
	flagHint: "--models-lib",
}

func newSimCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	var window string
	sim := &cobra.Command{
		Use:   "sim",
		Short: "Circuit simulations computed from the schematic (DC power tree, analog SPICE)",
		Long: `Circuit simulations computed from schematic connectivity.

  sim power   DC operating point of the power tree: voltage of every net and the
              current through every component pin, per scenario. Feeds trace
              width sizing with computed (not guessed) currents.
  sim analog  ngspice simulation of the analog circuits (amplifiers, active and
              passive filters, ADC inputs, references, comparators, crystals,
              reset RC, transistor switches, regulator feedback): targets vs
              simulated, tolerance Monte-Carlo, value-change plan.
  sim post-layout
              设计后仿真: the finished board's real copper (pcb dump
              --include-copper) with sim power's currents — IR drop per load
              pad, via currents, current density, thermal maps, part
              temperatures, width/corner/via feedback.
  sim tools   check / install the external open-source cross-check simulators
              (ngspice required, Elmer FEM optional). pcbpilot's own simulators
              are built into this binary and need no install.`,
	}
	sim.PersistentFlags().StringVar(&window, "window", "", "EasyEDA window ID (live mode)")
	sim.AddCommand(newSimPowerCmd(cfg, &window, stdout, stderr))
	sim.AddCommand(newSimAnalogCmd(cfg, &window, stdout, stderr))
	sim.AddCommand(newSimPostLayoutCmd(stdout, stderr))
	sim.AddCommand(newSimToolsCmd(stdout, stderr))
	return sim
}

func newSimPowerCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var connPaths, valuePaths, modelPaths, switches []string
	var pages, scenarios []string
	var modelsLib, outPath, reportPath, spicePath, spiceScenario string
	var spiceCheck bool
	var spiceTol float64
	c := &cobra.Command{
		Use:   "power",
		Short: "DC power simulation: per-net voltage and per-pin current for every scenario",
		Args:  cobra.NoArgs,
		Long: `Compute the DC operating point of the schematic's power tree and report the
current through every component pin, so trace widths are computed from the
circuit instead of guessed from net names.

WHAT IT IS. Modified Nodal Analysis with Newton-Raphson (damped, junction
limiting) over the whole schematic: resistors by value, inductors = DCR,
capacitors open, diodes/LEDs Shockley (Is/n from the model or fitted from
Vf@If), BJTs Ebers-Moll, switches open/closed per scenario, input connectors
as voltage sources with series R, IC loads as constant-current sinks on their
supply pins (split over same-rail pins; return split over ground pins), LDOs
(Vout source, Iin = Iout + Iq, dropout) and bucks (V(FB) = Vref through the
real divider, Iin = V(LX)·Iout/(η·Vin) + Iq solved iteratively, ripple
ΔI = (Vin−Vout)·D/(L·fsw), Ipk, Irms, input-cap Irms = Iout·√(D(1−D))). It is an
averaged DC model, not a transient SPICE run.

MODELS. Parts are matched in .agents/skills/pcbpilot/references/power-models.json
(auto-located; --models-lib overrides) by LCSC C-number, exact MPN, then name
regex; --models files are consulted first. Passives come from their value.
An IC with no model gets an assumed 50 mA per supply pin and a warning — never
silently zero. Every result lists its warnings and assumptions; every part
lists model id and confidence (datasheet | approx | assumed | value).

SCENARIOS. typical (typ loads), peak (peak loads), buttons-pressed (momentary
switches closed, when any exist), <source>-only for each input source when
more than one feeds the board (e.g. usb-only, terminal-only through OR diodes),
and the synthetic worst = per-pin maximum over all scenarios (pins[].scenario
says where it occurred). --scenario restricts the list.

OUTPUT (schemaVersion 1, fixed contract): {schemaVersion, generator,
scenarios[], results[{scenario, nets{<net>:{voltage, currentA, role
power|ground|signal|switch, pins[{ref, pin, name, currentA, dir}]}},
parts{<ref>:{model, powerW, notes}}, ripple{<net|ref>:{iPeakA, iRmsA}},
warnings[], assumptions[]}]}. Pin currentA is the magnitude through the pad;
dir source = leaves the part into the net, sink = enters the part, pass = no DC
current. Net currentA = Σ source pin currents = Σ sink currents (KCL). Ground
pins are included (return currents). Refs/pins are schematic designators and
pin numbers (= PCB designators/pad numbers).

INPUT. Offline: one or more --connectivity files (pcbpilot sch connectivity
output; pages are merged by net name) plus --values (a pcbpilot sch list
response, or {"parts":{"R1":{"value":"10k","mpn":"…","lcsc":"C…"}}}); the
connectivity device name is often an unresolved "={Value}" template, so
values are needed for passives. Live (no --connectivity): reads every
schematic page (or --pages) of the connected project with sch connectivity +
sch list, then restores the original page. Live reads only; nothing is written.

CROSS-CHECK. --spice writes the linearised netlist (.op) of --spice-scenario;
--spice-check runs ngspice when on PATH and compares node voltages
(--spice-tol, default 1 mV), otherwise records "skipped".`,
		Example: `  # offline replay of exported pages
  pcbpilot sim power --connectivity sch-p1.json --connectivity sch-p2.json \
      --values sch-list.json --out sim.json --report sim.md

  # live project, two pages, peak + worst only
  pcbpilot --project ceshi sim power --pages P1,P2 --scenario peak,worst --out sim.json

  # extra/override models, a forced switch, ngspice cross-check
  pcbpilot sim power --connectivity c.json --values v.json --models my-models.json \
      --switch SW3=closed --spice sim.cir --spice-check`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(connPaths) > 0 && len(pages) > 0 {
				return fmt.Errorf("--pages is for live mode; do not combine it with --connectivity")
			}
			if len(connPaths) == 0 && len(valuePaths) > 0 {
				return fmt.Errorf("--values needs --connectivity (live mode reads values itself)")
			}
			sw, err := parseSimSwitches(switches)
			if err != nil {
				return err
			}
			libs, libNames, err := loadSimLibraries(modelsLib, modelPaths, stderr)
			if err != nil {
				return err
			}
			var connDocs [][]byte
			values := map[string]powersim.PartValues{}
			inputs := &powersim.Inputs{Libraries: libNames}
			if len(connPaths) > 0 {
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
				inputs.Connectivity, inputs.Values = connPaths, valuePaths
			} else {
				docs, vals, desc, err := collectSimLive(cfg, *window, pages)
				if err != nil {
					return fmt.Errorf("live read failed (use --connectivity/--values for offline input): %w", err)
				}
				connDocs, values, inputs.Live = docs, vals, desc
			}
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
			out, eng, err := powersim.Simulate(design, libs, powersim.Options{Scenarios: scenarios, Switches: sw})
			if err != nil {
				return err
			}
			out.Inputs = inputs
			if spicePath != "" || spiceCheck {
				found := false
				for _, s := range out.Scenarios {
					found = found || s == spiceScenario
				}
				if !found || spiceScenario == "worst" {
					return fmt.Errorf("--spice-scenario %q is not a solved scenario (have %s)", spiceScenario, strings.Join(out.Scenarios, ", "))
				}
			}
			if spicePath != "" {
				f, err := os.Create(spicePath)
				if err != nil {
					return err
				}
				werr := eng.WriteSPICE(f, spiceScenario)
				if cerr := f.Close(); werr == nil {
					werr = cerr
				}
				if werr != nil {
					return werr
				}
			}
			if spiceCheck {
				out.SpiceCheck = eng.CheckSPICE(spiceScenario, spiceTol)
			}
			b, _ := json.MarshalIndent(out, "", "  ")
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
				werr := powersim.WriteReport(f, out)
				if cerr := f.Close(); werr == nil {
					werr = cerr
				}
				if werr != nil {
					return werr
				}
			}
			printSimSummary(stderr, out)
			if out.SpiceCheck != nil && out.SpiceCheck.Ran && !out.SpiceCheck.Pass {
				return fmt.Errorf("ngspice cross-check failed: max |ΔV| %.3g V at %s", out.SpiceCheck.MaxDiffV, out.SpiceCheck.WorstNet)
			}
			for _, r := range out.Results {
				if !r.Converged {
					return fmt.Errorf("scenario %s did not converge — see warnings", r.Scenario)
				}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringArrayVar(&connPaths, "connectivity", nil, "offline: 'pcbpilot sch connectivity' JSON `file` (repeat per page; merged by net name)")
	f.StringArrayVar(&valuePaths, "values", nil, "offline: 'pcbpilot sch list' JSON or {\"parts\":{ref:{value,mpn,lcsc}}} values `file` (repeatable)")
	f.StringSliceVar(&pages, "pages", nil, "live: schematic pages (name or uuid, comma-separated); default all pages")
	f.StringArrayVar(&modelPaths, "models", nil, "extra power-models JSON consulted before the skill library (repeatable)")
	f.StringVar(&modelsLib, "models-lib", "", "base power-models.json (default: the installed skill's references/power-models.json; \"none\" = no base library)")
	f.StringSliceVar(&scenarios, "scenario", nil, "only these scenarios (typical, peak, buttons-pressed, <source>-only, worst)")
	f.StringArrayVar(&switches, "switch", nil, "force a switch in every scenario: REF=closed|open (repeatable)")
	f.StringVar(&outPath, "out", "", "write the simulation JSON here (default stdout)")
	f.StringVar(&reportPath, "report", "", "write a Markdown report (rails, regulators, part power, pin currents, ripple, warnings, assumptions)")
	f.StringVar(&spicePath, "spice", "", "write the linearised SPICE DC netlist (.op) of --spice-scenario")
	f.StringVar(&spiceScenario, "spice-scenario", "peak", "scenario exported by --spice / checked by --spice-check")
	f.BoolVar(&spiceCheck, "spice-check", false, "run ngspice (if on PATH) on the linearised netlist and compare node voltages")
	f.Float64Var(&spiceTol, "spice-tol", 1e-3, "--spice-check voltage tolerance (V)")
	return c
}

func parseSimSwitches(in []string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, s := range in {
		ref, state, ok := strings.Cut(s, "=")
		if !ok || strings.TrimSpace(ref) == "" {
			return nil, fmt.Errorf("--switch %q: want REF=closed|open", s)
		}
		switch strings.ToLower(strings.TrimSpace(state)) {
		case "closed", "close", "on", "1", "pressed":
			out[strings.TrimSpace(ref)] = true
		case "open", "off", "0", "released":
			out[strings.TrimSpace(ref)] = false
		default:
			return nil, fmt.Errorf("--switch %q: state must be closed or open", s)
		}
	}
	return out, nil
}

func loadSimLibraries(base string, extra []string, stderr io.Writer) (powersim.Libraries, []string, error) {
	var libs powersim.Libraries
	var names []string
	for _, p := range extra {
		lib, err := powersim.LoadLibrary(p)
		if err != nil {
			return nil, nil, err
		}
		libs = append(libs, lib)
		names = append(names, p)
	}
	if strings.EqualFold(strings.TrimSpace(base), "none") {
		return libs, names, nil
	}
	path, err := powerModelsAsset.resolve(base)
	if err != nil {
		if base != "" {
			return nil, nil, err
		}
		fmt.Fprintf(stderr, "warning: skill power-models.json not found; only --models and generic parts are used\n%v\n", err)
		return libs, names, nil
	}
	lib, err := powersim.LoadLibrary(path)
	if err != nil {
		return nil, nil, err
	}
	return append(libs, lib), append(names, path), nil
}

func printSimSummary(w io.Writer, out *powersim.Output) {
	for _, r := range out.Results {
		var rails []string
		for _, net := range sortedNetNames(r.Nets) {
			nr := r.Nets[net]
			if nr.Role == "power" && !nr.Floating {
				rails = append(rails, fmt.Sprintf("%s %.3fV %.3fA", net, nr.Voltage, nr.CurrentA))
			}
		}
		fmt.Fprintf(w, "sim power %-16s %s; %d warning(s)\n", r.Scenario+":", strings.Join(rails, ", "), len(r.Warnings))
	}
}

func sortedNetNames(m map[string]*powersim.NetResult) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// collectSimLive reads each selected schematic page (connectivity + component
// attributes) and restores the originally active page. Read-only.
func collectSimLive(cfg *appConfig, window string, pages []string) (docs [][]byte, values map[string]powersim.PartValues, desc string, err error) {
	readCfg := *cfg
	readCfg.doc = ""
	all, active, win, err := discoverDocs(&readCfg, window)
	if err != nil {
		return nil, nil, "", err
	}
	want := map[string]bool{}
	for _, p := range pages {
		if p = strings.TrimSpace(p); p != "" {
			want[p] = true
		}
	}
	var selected []openableDoc
	for _, d := range all {
		if d.Type != "schematic" {
			continue
		}
		if len(want) == 0 || want[d.UUID] || want[d.Name] {
			selected = append(selected, d)
			delete(want, d.UUID)
			delete(want, d.Name)
		}
	}
	if len(want) > 0 {
		var missing []string
		for p := range want {
			missing = append(missing, p)
		}
		sort.Strings(missing)
		return nil, nil, "", fmt.Errorf("schematic page(s) not found: %s", strings.Join(missing, ", "))
	}
	if len(selected) == 0 {
		return nil, nil, "", fmt.Errorf("no schematic pages in the connected project")
	}
	changed := false
	defer func() {
		if changed {
			scope := pageScope{window: win, prevActive: active, switched: true}
			if rerr := scope.restore(&readCfg); rerr != nil {
				err = errors.Join(err, fmt.Errorf("restore original document %s: %w", active, rerr))
			}
		}
	}()
	values = map[string]powersim.PartValues{}
	var names []string
	for _, d := range selected {
		changed = changed || d.UUID != active
		scope, serr := switchToPage(&readCfg, win, d.UUID)
		if serr != nil {
			return nil, nil, "", serr
		}
		if !scope.settled {
			return nil, nil, "", fmt.Errorf("page %s did not settle", d.Name)
		}
		raw, rerr := requestAction(&readCfg, "schematic.read", win, map[string]any{"includeCheck": false})
		if rerr != nil {
			return nil, nil, "", rerr
		}
		list, lerr := requestAction(&readCfg, "schematic.components.list", win, map[string]any{"includePins": true})
		if lerr != nil {
			return nil, nil, "", lerr
		}
		if raw.Result == nil || list.Result == nil {
			return nil, nil, "", fmt.Errorf("page %s: empty read", d.Name)
		}
		parts, ok := list.Result["components"].([]any)
		if !ok {
			return nil, nil, "", fmt.Errorf("page %s: missing component inventory", d.Name)
		}
		raw.Result["components"] = parts
		doc, cerr := connectivity.FromRead(raw.Result)
		if cerr != nil {
			return nil, nil, "", fmt.Errorf("page %s: %w", d.Name, cerr)
		}
		doc.DocumentID = d.UUID
		b, _ := json.Marshal(doc)
		docs = append(docs, b)
		var recs []map[string]any
		for _, p := range parts {
			if m, ok := p.(map[string]any); ok {
				recs = append(recs, m)
			}
		}
		for ref, v := range powersim.ValuesFromSchList(recs) {
			values[ref] = v
		}
		names = append(names, d.Name)
	}
	return docs, values, "pages " + strings.Join(names, ", "), nil
}
