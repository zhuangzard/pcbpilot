package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/zhuangzard/pcbpilot/pkg/analogsim"
	"github.com/zhuangzard/pcbpilot/pkg/designreport"
	"github.com/zhuangzard/pcbpilot/pkg/intent"
	"github.com/zhuangzard/pcbpilot/pkg/powersim"
)

// analogModelsAsset is the skill's analog SPICE model library.
var analogModelsAsset = skillAsset{
	name:     "analog-models.json",
	rels:     []string{"pcbpilot/references/spice-models/analog-models.json"},
	flagHint: "--analog-models",
}

// loadAnalogLibrary resolves analog-models.json ("none" = built-in generic defaults).
func loadAnalogLibrary(explicit string, stderr io.Writer) (*analogsim.Library, string, error) {
	if strings.EqualFold(strings.TrimSpace(explicit), "none") {
		return analogsim.EmptyLibrary(), "", nil
	}
	path, err := analogModelsAsset.resolve(explicit)
	if err != nil {
		if explicit != "" {
			return nil, "", err
		}
		fmt.Fprintf(stderr, "warning: analog-models.json not found; generic (assumed) models only\n%v\n", err)
		return analogsim.EmptyLibrary(), "", nil
	}
	lib, err := analogsim.LoadLibrary(path)
	if err != nil {
		return nil, "", err
	}
	return lib, path, nil
}

func loadStockLib(explicit string) (*analogsim.StockLib, string) {
	if strings.EqualFold(strings.TrimSpace(explicit), "none") {
		return nil, ""
	}
	path, err := standardPartsAsset.resolve(explicit)
	if err != nil {
		return nil, ""
	}
	s, err := analogsim.LoadStock(path)
	if err != nil {
		return nil, ""
	}
	return s, path
}

// simDesignInputs reads offline connectivity/values or the live project.
func simDesignInputs(cfg *appConfig, window string, connPaths, valuePaths, pages []string, stderr io.Writer) (*powersim.Design, []string, error) {
	if len(connPaths) > 0 && len(pages) > 0 {
		return nil, nil, fmt.Errorf("--pages is for live mode; do not combine it with --connectivity")
	}
	if len(connPaths) == 0 && len(valuePaths) > 0 {
		return nil, nil, fmt.Errorf("--values needs --connectivity (live mode reads values itself)")
	}
	var connDocs [][]byte
	values := map[string]powersim.PartValues{}
	var sources []string
	if len(connPaths) > 0 {
		for _, p := range connPaths {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, nil, err
			}
			connDocs = append(connDocs, b)
		}
		for _, p := range valuePaths {
			b, err := os.ReadFile(p)
			if err != nil {
				return nil, nil, err
			}
			v, err := powersim.ParseValues(b)
			if err != nil {
				return nil, nil, fmt.Errorf("%s: %w", p, err)
			}
			for ref, pv := range v {
				values[ref] = pv
			}
		}
		sources = append(append(sources, connPaths...), valuePaths...)
	} else {
		docs, vals, desc, err := collectSimLive(cfg, window, pages)
		if err != nil {
			return nil, nil, fmt.Errorf("live read failed (use --connectivity/--values for offline input): %w", err)
		}
		connDocs, values = docs, vals
		sources = []string{fmt.Sprintf("live project %s: %s", cfg.project, desc)}
	}
	var parsed []*powersim.ConnDoc
	for i, b := range connDocs {
		d, err := powersim.ParseConnectivity(b)
		if err != nil {
			name := fmt.Sprintf("page %d", i+1)
			if i < len(connPaths) {
				name = connPaths[i]
			}
			return nil, nil, fmt.Errorf("%s: %w", name, err)
		}
		parsed = append(parsed, d)
	}
	design, dwarn, err := powersim.BuildDesign(parsed, values)
	if err != nil {
		return nil, nil, err
	}
	for _, w := range dwarn {
		fmt.Fprintln(stderr, "warning:", w)
	}
	return design, sources, nil
}

func newSimAnalogCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var connPaths, valuePaths, pages, modelPaths []string
	var simPath, specPath, analogModels, modelsLib, stockPath, outPath, reportPath, plotsDir, workDir, planPath, ngspice, whatIf string
	var mcRuns, seed int
	var noOptimise, strict bool
	c := &cobra.Command{
		Use:   "analog",
		Short: "SPICE (ngspice) simulation of the schematic's analog circuits + value optimisation plan",
		Args:  cobra.NoArgs,
		Long: `Find the ANALOG circuits of the schematic, extract each as a SPICE subcircuit
with its rails and loads, run ngspice, compare with the targets and propose
component-value changes. Nothing is written to the editor.

DETECTS (analog blocks): op-amp amplifiers (follower, non-inverting,
inverting, difference, integrator), active filters (Sallen-Key and MFB,
low/high-pass, with or without gain), comparators (hysteresis thresholds),
instrumentation / current-sense amplifier ICs (gain from RG / fixed gain,
full scale from the simulated shunt current), shunt voltage references (bias
current window), ADC inputs (divider / RC / op-amp output → SAR hold-capacitor
settling in LSB), regulator feedback dividers (Vout = Vref·(1+Rtop/Rbot)),
crystal load capacitors (CL vs spec, load-resonance ppm), reset/enable RC
(delay vs MCU timing), BJT/MOSFET switches and level shifters (saturation,
turn-on/off, peak current), LC supply filters (resonance peaking), and
standalone RC low-pass filters.

MODELS. .agents/skills/pcbpilot/references/spice-models/: analog-models.json
(op-amps by MPN/LCSC with GBW, slew, Aol, Vos, headroom, input CM range, output
current, pin-out; comparators, amplifiers, references, ADC input models, reset
timing, MOSFETs, BJT switching limits — each with its datasheet source) and
pcbpilot-generic.lib (behavioural PCBPILOT_OPAMP two-pole macro with slew and
rail clipping, open-drain comparator, fixed-gain amplifier, shunt reference,
generic D/NPN/PNP/NMOS/PMOS). An unknown op-amp gets the generic model and the
result is marked assumed. Vendor .subckt models: drop the file into
spice-models/vendor/ and map its pin order in vendorModels[] (see README.md
there). Regulator Vref and BJT Gummel-Poon numbers come from power-models.json.

ANALYSES (ngspice -b, wrdata): .dc sweep of the stimulus (bias point, output
swing, common mode), .op, .ac (gain, -3 dB fc, f0 and Q from the -90° phase
point), loop gain T(f) by voltage injection at the macro's ideal output
(crossover, phase margin, gain margin), .tran small-signal step (overshoot,
10–90 % rise, 2 % settling, output current), ADC sample-and-hold transient,
comparator triangle ramp, transistor on/off transient, reset RC after a supply
ramp, crystal BVD load resonance, and Monte-Carlo tolerance (alter per run;
normal ±tol = 3σ; tolerance from the part description / MPN code).

TARGETS. --spec (optional): {"blocks":[{"core":"U1:B","targets":{"fcHz":1000,
"q":0.707,"gain":10},"tolPct":{"fcHz":5},"min":{"phaseMarginDeg":60},
"inputRange":[0,24],"fixed":["R4"]}],"adc":{"sampleRateHz":100000,
"tsampleS":1e-6},"phaseMarginMinDeg":45,"mcRuns":200,"seed":1,"strayPF":3,
"crystals":{"Y1":{"clPF":12}},"rails":{"VBAT":3.7}}. Blocks match by id (A3),
core (U1:B), output net or any part ref. Without a spec the targets are
inferred: design nominal (±2 % gain, ±10 % fc/Q), phase margin ≥ 45°, ADC
settling ≤ ½ LSB and output reaching the ADC full scale, reference Ik window,
reset delay ≥ datasheet minimum, BJT overdrive ≥ 2, LC peaking ≤ 6 dB,
regulator Vout = rail-name voltage ±3 %.

OPTIMISATION (default on). Analytic first guess (Sallen-Key capacitor-ratio
design, crystal 2·(CL − Cs), …) → coordinate search over E96 resistors / E12
capacitors preferring stocked parts (standard-parts.json) and few changes →
ngspice verification; a change is kept only if the SIMULATED cost improves.
--apply-plan writes the typed value-change plan (kind
pcbpilot.schematic-value-plan): ref, old → new, series, stocked LCSC part or
needsPartSelection, reason, before/after metrics. The plan is a SCHEMATIC edit:
show it to the user, wait for an explicit yes, then 'sim analog compile-plan'
against a fresh sch list and 'pcbpilot apply'. Re-run sim analog, intent derive
and report design afterwards.

INPUT: as 'sim power' — offline --connectivity/--values or live --pages
(read-only). Rails come from --sim (a sim power document) or an in-process
power simulation. OUTPUT: --out analog.json (schemaVersion 1), --report
analog.md, --plots-dir (one SVG per curve), --work-dir (netlists, logs, wrdata;
default <out>-ngspice/ next to --out), --apply-plan plan.json. ngspice missing
→ analytic checks only and a note: run 'pcbpilot sim tools install'.`,
		Example: `  # offline fixture with targets, plots and a plan
  pcbpilot sim analog --connectivity sch.json --values values.json --spec spec.json \
      --out analog.json --report analog.md --plots-dir plots --apply-plan plan.json

  # live project, pages P1,P2 (read-only)
  pcbpilot --project demo sim analog --pages P1,P2 --out analog.json --report analog.md`,
		RunE: func(cmd *cobra.Command, args []string) error {
			design, sources, err := simDesignInputs(cfg, *window, connPaths, valuePaths, pages, stderr)
			if err != nil {
				return err
			}
			if whatIf != "" {
				b, err := os.ReadFile(whatIf)
				if err != nil {
					return err
				}
				plan, err := analogsim.ParsePlan(b)
				if err != nil {
					return fmt.Errorf("%s: %w", whatIf, err)
				}
				n, err := analogsim.ApplyPlanToDesign(design, plan)
				if err != nil {
					return err
				}
				sources = append(sources, fmt.Sprintf("what-if: %d value change(s) from %s applied in memory (schematic unchanged)", n, whatIf))
				fmt.Fprintf(stderr, "what-if: %d value change(s) from %s applied in memory — the schematic is NOT changed\n", n, whatIf)
			}
			libs, libNames, err := loadSimLibraries(modelsLib, modelPaths, stderr)
			if err != nil {
				return err
			}
			var psim *powersim.Output
			simSrc := ""
			if simPath != "" {
				b, err := os.ReadFile(simPath)
				if err != nil {
					return err
				}
				if psim, err = intent.ParseSimOutput(b); err != nil {
					return fmt.Errorf("%s: %w", simPath, err)
				}
				simSrc = simPath
			} else {
				out, _, err := powersim.Simulate(design, libs, powersim.Options{Scenarios: []string{"typical"}})
				if err == nil {
					psim, simSrc = out, "in-process "+powersim.Generator+" (typical)"
				} else {
					fmt.Fprintln(stderr, "warning: power simulation for the rails failed:", err)
				}
			}
			lib, libPath, err := loadAnalogLibrary(analogModels, stderr)
			if err != nil {
				return err
			}
			var spec *analogsim.Spec
			if specPath != "" {
				b, err := os.ReadFile(specPath)
				if err != nil {
					return err
				}
				if spec, err = analogsim.ParseSpec(b); err != nil {
					return fmt.Errorf("%s: %w", specPath, err)
				}
			}
			stock, _ := loadStockLib(stockPath)
			wd := workDir
			if wd == "" && outPath != "" {
				wd = strings.TrimSuffix(outPath, filepath.Ext(outPath)) + "-ngspice"
			}
			if strings.EqualFold(wd, "none") {
				wd = ""
			}
			models := []string{}
			if libPath != "" {
				models = append(models, libPath)
			}
			models = append(models, libNames...)
			out, err := analogsim.Run(design, lib, analogsim.Options{Ngspice: ngspice, WorkDir: wd, MCRuns: mcRuns, Seed: seed, Optimise: !noOptimise,
				Spec: spec, Stock: stock, PowerSim: psim, PowerLibs: libs,
				Inputs: &analogsim.Inputs{Schematic: sources, PowerSim: simSrc, Spec: specPath, Models: models}})
			if err != nil {
				return err
			}
			return writeAnalogOutputs(out, outPath, reportPath, plotsDir, planPath, stdout, stderr, strict)
		},
	}
	f := c.Flags()
	f.StringArrayVar(&connPaths, "connectivity", nil, "offline: 'pcbpilot sch connectivity' JSON `file` (repeat per page)")
	f.StringArrayVar(&valuePaths, "values", nil, "offline: 'pcbpilot sch list' JSON or {\"parts\":{ref:{value,mpn,lcsc,description}}} `file` (repeatable)")
	f.StringSliceVar(&pages, "pages", nil, "live: schematic pages (name or uuid, comma-separated); default all pages")
	f.StringVar(&simPath, "sim", "", "'pcbpilot sim power' JSON for the rail voltages (default: in-process typical scenario)")
	f.StringVar(&specPath, "spec", "", "analog targets JSON (blocks[].targets/min/max/tolPct/inputRange/fixed, adc, phaseMarginMinDeg, mcRuns, seed, crystals, rails)")
	f.StringVar(&analogModels, "analog-models", "", "analog-models.json (default: the installed skill's references/spice-models/analog-models.json; \"none\" = generic only)")
	f.StringArrayVar(&modelPaths, "models", nil, "extra power-models JSON (regulator Vref, BJT numbers), consulted first")
	f.StringVar(&modelsLib, "models-lib", "", "base power-models.json (default: the skill's; \"none\" = no base library)")
	f.StringVar(&stockPath, "parts", "", "standard-parts.json for stocked-value snapping (default: the skill's; \"none\" = E-series only)")
	f.StringVar(&outPath, "out", "", "write analog.json here (default stdout)")
	f.StringVar(&reportPath, "report", "", "write the Markdown reading (analog.md)")
	f.StringVar(&plotsDir, "plots-dir", "", "write one SVG per curve (Bode, loop gain, step, transfer, ADC sample, …)")
	f.StringVar(&workDir, "work-dir", "", "keep ngspice netlists/logs/data here (default <out>-ngspice/; \"none\" = temp dir)")
	f.StringVar(&planPath, "apply-plan", "", "write the schematic value-change plan (pcbpilot.schematic-value-plan) here")
	f.StringVar(&ngspice, "ngspice", "", "ngspice binary (default: $PCBPILOT_NGSPICE, PATH, Homebrew/usr paths; \"none\" = analytic only)")
	f.StringVar(&whatIf, "what-if", "", "apply a value-change plan IN MEMORY before simulating (preview the proposed values; the schematic is not touched)")
	f.IntVar(&mcRuns, "mc-runs", 0, "Monte-Carlo runs per block (default: spec mcRuns or the library default 100)")
	f.IntVar(&seed, "seed", 0, "Monte-Carlo seed (default: spec seed or 1)")
	f.BoolVar(&noOptimise, "no-optimise", false, "report only; do not search for better component values")
	f.BoolVar(&strict, "strict", false, "exit non-zero when a finding has severity error")
	c.AddCommand(newSimAnalogCompileCmd(cfg, stdout, stderr))
	return c
}

// writeAnalogOutputs writes analog.json / analog.md / plots / plan and a summary line.
func writeAnalogOutputs(out *analogsim.Output, outPath, reportPath, plotsDir, planPath string, stdout, stderr io.Writer, strict bool) error {
	relWorkDir(out, outPath)
	for _, p := range []string{outPath, reportPath, planPath} {
		if p != "" {
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return err
			}
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	if outPath != "" {
		if err := os.WriteFile(outPath, append(b, '\n'), 0o644); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintln(stdout, string(b)); err != nil {
		return err
	}
	plots := map[string]string{}
	if plotsDir != "" {
		if err := os.MkdirAll(plotsDir, 0o755); err != nil {
			return err
		}
		charts := designreport.AnalogCharts(out)
		names := make([]string, 0, len(charts))
		for n := range charts {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			p := filepath.Join(plotsDir, n+".svg")
			if err := os.WriteFile(p, []byte(charts[n]+"\n"), 0o644); err != nil {
				return err
			}
		}
		rel := plotsDir
		if reportPath != "" {
			if r, err := filepath.Rel(filepath.Dir(reportPath), plotsDir); err == nil {
				rel = r
			}
		}
		for _, blk := range out.Blocks {
			for _, cv := range blk.Curves {
				plots[blk.ID+"/"+cv.Name] = filepath.ToSlash(filepath.Join(rel, "analog-"+strings.ToLower(blk.ID)+"-"+cv.Name+".svg"))
			}
		}
	}
	if reportPath != "" {
		f, err := os.Create(reportPath)
		if err != nil {
			return err
		}
		werr := analogsim.WriteReport(f, out, plots)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	if planPath != "" {
		plan := out.Plan
		if plan == nil {
			plan = &analogsim.Plan{SchemaVersion: analogsim.SchemaVersion, Kind: analogsim.PlanKind, Generator: analogsim.Generator, RequiresUserConfirmation: true, Changes: []analogsim.Change{}}
		}
		pb, _ := json.MarshalIndent(plan, "", "  ")
		if err := os.WriteFile(planPath, append(pb, '\n'), 0o644); err != nil {
			return err
		}
	}
	printAnalogSummary(stderr, out)
	if strict {
		for _, f := range out.Findings {
			if f.Severity == "error" {
				return fmt.Errorf("analog: error finding(s) (--strict)")
			}
		}
	}
	return nil
}

func printAnalogSummary(w io.Writer, out *analogsim.Output) {
	s := out.Summary
	ng := "ngspice missing (analytic only) → pcbpilot sim tools install"
	if out.Ngspice.Available {
		ng = fmt.Sprintf("%s, %d runs", out.Ngspice.Version, out.Ngspice.Runs)
	}
	n := map[string]int{}
	for _, f := range out.Findings {
		n[f.Severity]++
	}
	var cls []string
	for _, k := range sortedStringKeys(s.ByClass) {
		cls = append(cls, fmt.Sprintf("%s×%d", k, s.ByClass[k]))
	}
	fmt.Fprintf(w, "sim analog: %d block(s) [%s]; %s; targets %d met / %d failing; %d value change(s); findings %d error / %d warn / %d info; %s\n",
		s.Blocks, strings.Join(cls, " "), ng, s.Met, s.Failing, s.Changes, n["error"], n["warn"], n["info"], s.Status)
}

func sortedStringKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func newSimAnalogCompileCmd(cfg *appConfig, stdout, stderr io.Writer) *cobra.Command {
	var planPath, outPath string
	var compPaths []string
	var allowValueOnly bool
	c := &cobra.Command{
		Use:   "compile-plan",
		Short: "Compile an analog value-change plan into a pcbpilot apply playbook (after the user confirmed it)",
		Args:  cobra.NoArgs,
		Long: `Turn the plan written by 'sim analog --apply-plan' into a 'pcbpilot apply'
playbook, resolving each designator to its primitiveId in a FRESH 'pcbpilot sch
list' export of the page(s) that hold the parts.

Only run this after the user explicitly confirmed the plan's changes: value
changes are schematic edits. A change with a stocked part (changes[].part.lcsc)
compiles to schematic.component.replace (keeps designator, uniqueId, pose and
wiring; part identity follows the new device). A change marked
needsPartSelection is refused unless --allow-value-only, which compiles to a
schematic.component.modify of the Value attribute only (the old LCSC/MPN stay —
fix the BOM before ordering). Every step carries confirm:true and the playbook
ends with schematic.save. Run 'pcbpilot apply <playbook> --dry-run' first.`,
		Example: `  pcbpilot --project demo sch list --page P1 > sch-list.json
  pcbpilot sim analog compile-plan --plan plan.json --components sch-list.json --out value-playbook.json
  pcbpilot --project demo apply value-playbook.json --dry-run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if planPath == "" || len(compPaths) == 0 {
				return fmt.Errorf("--plan and --components are required")
			}
			b, err := os.ReadFile(planPath)
			if err != nil {
				return err
			}
			plan, err := analogsim.ParsePlan(b)
			if err != nil {
				return fmt.Errorf("%s: %w", planPath, err)
			}
			if len(plan.Changes) == 0 {
				return fmt.Errorf("%s has no changes", planPath)
			}
			var comps []map[string]any
			for _, p := range compPaths {
				raw, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				list, err := componentRecords(raw)
				if err != nil {
					return fmt.Errorf("%s: %w", p, err)
				}
				comps = append(comps, list...)
			}
			pb, warns, err := analogsim.CompilePlaybook(plan, comps, analogsim.PlaybookOptions{AllowValueOnly: allowValueOnly, Project: cfg.project})
			if err != nil {
				return err
			}
			for _, w := range warns {
				fmt.Fprintln(stderr, "warning:", w)
			}
			js, _ := json.MarshalIndent(pb, "", "  ")
			if outPath == "" {
				_, err := fmt.Fprintln(stdout, string(js))
				return err
			}
			if err := os.WriteFile(outPath, append(js, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stderr, "compiled %d change(s) → %s; next: pcbpilot apply %s --dry-run\n", len(plan.Changes), outPath, outPath)
			return nil
		},
	}
	c.Flags().StringVar(&planPath, "plan", "", "value-change plan (sim analog --apply-plan)")
	c.Flags().StringArrayVar(&compPaths, "components", nil, "fresh 'pcbpilot sch list' JSON of the page(s) holding the refs (repeatable)")
	c.Flags().StringVar(&outPath, "out", "", "write the playbook here (default stdout)")
	c.Flags().BoolVar(&allowValueOnly, "allow-value-only", false, "compile changes without a stocked part as a Value-attribute edit (BOM keeps the old part)")
	return c
}

// componentRecords extracts components[] from a sch list response / envelope.
func componentRecords(raw []byte) ([]map[string]any, error) {
	var top map[string]any
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	for depth := 0; depth < 4 && top != nil; depth++ {
		if list, ok := top["components"].([]any); ok {
			var out []map[string]any
			for _, x := range list {
				if m, ok := x.(map[string]any); ok {
					out = append(out, m)
				}
			}
			return out, nil
		}
		next, _ := top["result"].(map[string]any)
		if next == nil {
			next, _ = top["data"].(map[string]any)
		}
		top = next
	}
	return nil, fmt.Errorf("no components[] (want a 'pcbpilot sch list' response)")
}

func ngspiceInstallHint() string {
	return "install: pcbpilot sim tools install --only ngspice   (sim analog also honours $PCBPILOT_NGSPICE)"
}

// relWorkDir records the work dir relative to analog.json, so the report
// package can find the netlists wherever the pair is moved together.
func relWorkDir(out *analogsim.Output, outPath string) {
	if out.WorkDir == "" || outPath == "" {
		return
	}
	absW, err1 := filepath.Abs(out.WorkDir)
	absO, err2 := filepath.Abs(filepath.Dir(outPath))
	if err1 != nil || err2 != nil {
		return
	}
	if rel, err := filepath.Rel(absO, absW); err == nil {
		out.WorkDir = filepath.ToSlash(rel)
	}
}
