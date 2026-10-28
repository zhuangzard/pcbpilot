package app

// cmd_kicad_schcheck.go — `pcbpilot kicad sch-check`: the schematic hard gate
// on KiCad: kicad-cli ERC (JSON) of the hierarchy plus pcbpilot's own
// quality checks of every sheet (kicad.CheckSchematic). --fix-pwr-flag wires
// a PWR_FLAG to every power net ERC reports as not driven (one per net),
// each placement gated and netlist-verified like every other KiCad edit.

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

type kicadSheetFindings struct {
	File     string          `json:"file"`
	Findings []kicad.Finding `json:"findings"`
}

type kicadPwrFlagFix struct {
	Net  string   `json:"net"`
	File string   `json:"file"`
	At   kicad.Pt `json:"at"`
	Ref  string   `json:"ref,omitempty"`
	Err  string   `json:"error,omitempty"`
	Code string   `json:"code,omitempty"` // why no flag was placed
	Fix  string   `json:"fix,omitempty"`
}

type kicadSchCheckReport struct {
	OK       bool                 `json:"ok"`
	Root     string               `json:"root"`
	ERC      *kicad.ERCReport     `json:"erc"`
	Quality  []kicadSheetFindings `json:"quality"`
	Findings int                  `json:"qualityFindings"`
	Fixed    []kicadPwrFlagFix    `json:"pwrFlags,omitempty"`
	// Review is the design review (--review); Status is then the overall
	// verdict of ERC + quality + review.
	Review *kicad.SchReview `json:"review,omitempty"`
	Status gate.Status      `json:"status,omitempty"`
}

func newKicadSchCheckCmd(stdout, stderr io.Writer) *cobra.Command {
	var sch string
	var fixPwr, review bool
	var ignore []string
	var intentPath, simPath, waiverPath string
	c := &cobra.Command{
		Use:   "sch-check",
		Short: "Schematic hard gate on KiCad: kicad-cli ERC + pcbpilot quality checks, --review design audit (JSON, non-zero on fail)",
		Long: `Runs kicad-cli's ERC (JSON, all severities) on the hierarchy of --sch and
pcbpilot's own quality checks on every sheet: overlapping symbols, wires through
symbol bodies, diagonal / off-grid / overlapping wires, a wire end or pin on the
middle of a wire (KiCad does not connect it), labels and field texts on top of
symbols, labels or each other, a pin under a label, anything in the title block
or outside the drawing area. Prints one JSON report; exits non-zero when ERC has
an error or any quality finding remains (ERC warnings are listed, not failing).

--fix-pwr-flag: for every net ERC reports as "power input pin not driven" (the
supply comes from a connector or a part modelled with passive pins), a PWR_FLAG
is wired to that net once — a short stub off the reported pin or power symbol,
the first direction/length whose result passes the quality gate — and each sheet
write is netlist-verified (unchanged connectivity) or left untouched. ERC runs
again afterwards.

--ignore KIND (repeatable) drops a quality finding kind (e.g. off-grid on an
imported sheet whose parts sit off the 1.27 mm grid).

--review adds the design review (read-only; JSON under "review"): schematic
design mistakes proven from the KiCad netlist, the library pin electrical
types, ERC and the sheet files, plus --intent intent.json (pcbpilot intent
derive: net roles, rail voltages, regulator blocks, the optional review{}
roles) and --sim sim.json (pcbpilot sim power: rail voltages) when given.
  connections   i2c-pullup, open-drain-pullup, pullup-voltage, led-limiter,
                floating-input, output-conflict
  power         ic-power-ground, decoupling (exact cap set per power pin;
                shared coverage = warning), regulator-output-cap,
                regulator-input-cap, bulk-cap (>= 1 uF floor)
  net integrity merged-nets (ERC multiple_net_names), single-pin-net,
                hier-mismatch (sheet pin <-> hierarchical label)
Each check is pass | fail | cannot-check | not-applicable and names the
evidence it needed and used; each finding has severity error|warning|info,
certainty proven|likely|heuristic (only proven findings are errors), refs,
pins, nets, sheet, a fix and its evidence. A finding that misses optional
evidence (an internal pull not declared either way, a GPIO's direction, a
rail voltage) is a likely warning that names the missing evidence
(missingEvidence); cannot-check is kept for failed required evidence or
unresolved topology (no netlist / sheet files / ERC or its rule ignored, an
open jumper, a switch, a branching LED node, a diode of unknown polarity,
unparseable ERC items) and is never a pass. Verdict: pass, pass-with-
warnings (coverage incomplete — never "verified"), pass-with-waivers,
fail, cannot-check. Exemptions need an explicit role (intent review{}, a ReviewRole /
InternalPullup component field, the part kind) and name their source; a low
current alone exempts nothing. Connector pins are the off-board boundary
(info). Not covered: crystal load caps, strap pins, feedback values, ESR,
sequencing, transient protection.
--waivers takes signed {gate:"sch-review", match:<finding fingerprint>,
reason, by} entries: a waiver accepts that one finding (reported as waived,
verdict pass-with-waivers), never a cannot-check.
Exit with --review: 0 pass (also with warnings / waivers), 6 an unwaived
error (or ERC error / quality finding), 5 cannot-check; a tool or input
error (unreadable intent, no kicad-cli) is an error exit of its own.`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot kicad sch-check --sch board.kicad_sch
  pcbpilot kicad sch-check --sch board.kicad_sch --fix-pwr-flag
  pcbpilot kicad sch-check --sch board.kicad_sch --review --intent intent.json --waivers waivers.json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sch == "" {
				return fmt.Errorf("--sch is required")
			}
			if review && fixPwr {
				return fmt.Errorf("--review is read-only: run --fix-pwr-flag in its own sch-check first")
			}
			if !review && (intentPath != "" || simPath != "" || waiverPath != "") {
				return fmt.Errorf("--intent, --sim and --waivers are --review flags")
			}
			waivers, err := loadWaivers(waiverPath)
			if err != nil {
				return err
			}
			root, err := kicad.RootSheetFor(sch)
			if err != nil {
				return err
			}
			rep := kicadSchCheckReport{Root: root}
			erc, err := kicad.RunERC(root)
			if err != nil {
				return err
			}
			if fixPwr {
				rep.Fixed = kicadFixPwrFlags(root, erc)
				if erc, err = kicad.RunERC(root); err != nil {
					return err
				}
			}
			rep.ERC = erc
			files, err := kicad.SheetFiles(root)
			if err != nil {
				return err
			}
			for _, f := range files {
				b, err := os.ReadFile(f)
				if err != nil {
					return err
				}
				fs := kicad.CheckSchematic(string(b), kicad.CheckOptions{Ignore: ignore})
				if fs == nil {
					fs = []kicad.Finding{}
				}
				rep.Quality = append(rep.Quality, kicadSheetFindings{File: f, Findings: fs})
				rep.Findings += len(fs)
			}
			rep.OK = erc.Errors == 0 && rep.Findings == 0
			if review {
				rv, err := runSchReview(schReviewOpts{root: root, erc: erc, intentPath: intentPath, simPath: simPath, waivers: waivers})
				if err != nil {
					return err
				}
				rep.Review = rv
				rep.Status = gate.Aggregate(gate.Result{Name: "erc+quality", Status: gate.FromBool(rep.OK)}, gate.Result{Name: schReviewGate, Status: rv.Status})
				rep.OK = rep.Status == gate.Pass
				if err := writeJSON(stdout, rep); err != nil {
					return err
				}
				fmt.Fprintf(stderr, "sch-check: %d ERC error(s), %d warning(s), %d quality finding(s); %s\n", erc.Errors, erc.Warnings, rep.Findings, rv.Summary())
				if code := rep.Status.ExitCode(); code != 0 {
					return exitCodeError{code}
				}
				return nil
			}
			if err := writeJSON(stdout, rep); err != nil {
				return err
			}
			if !rep.OK {
				return fmt.Errorf("sch-check: %d ERC error(s), %d warning(s), %d quality finding(s)", erc.Errors, erc.Warnings, rep.Findings)
			}
			return nil
		},
	}
	c.Flags().StringVar(&sch, "sch", "", "a .kicad_sch of the design (its root sheet is found next to it)")
	c.Flags().BoolVar(&fixPwr, "fix-pwr-flag", false, "wire a PWR_FLAG to every power net ERC reports as not driven")
	c.Flags().StringArrayVar(&ignore, "ignore", nil, "quality finding kind to skip (repeatable)")
	c.Flags().BoolVar(&review, "review", false, "also run the design review (read-only; exit 6 on an unwaived error, 5 on cannot-check)")
	c.Flags().StringVar(&intentPath, "intent", "", "--review: intent.json from `pcbpilot intent derive` (net roles, rail voltages, review roles)")
	c.Flags().StringVar(&simPath, "sim", "", "--review: `pcbpilot sim power` document (rail voltages)")
	c.Flags().StringVar(&waiverPath, "waivers", "", "--review: signed waiver file (JSON list of {gate:\"sch-review\", match:<fingerprint>, reason, by})")
	return c
}

// kicadFixPwrFlags places one PWR_FLAG per undriven power net, at the
// exact pin ERC names (ercPinSnap); an item whose pin cannot be resolved
// and checked against the report is skipped, never placed at a guess.
func kicadFixPwrFlags(root string, erc *kicad.ERCReport) []kicadPwrFlagFix {
	nets, _ := kicadBeforeNets(root)
	type spot struct {
		file string
		at   kicad.Pt
		code string // why the pin is not resolved (err)
		err  error
	}
	byNet := map[string][]spot{}
	var order []string
	pins := ercPinSnap{}
	for _, v := range erc.Violations {
		if v.Type != "power_pin_not_driven" || v.File == "" {
			continue
		}
		for _, it := range v.Items {
			ref, pin, name, ok := kicad.ERCPin(it.Description)
			if !ok {
				continue
			}
			net := name // a power symbol's pin is named after its net
			if !strings.HasPrefix(ref, "#") {
				if n := nets[ref+"."+pin]; n != "" {
					net = stripSheetPath(n)
				}
			}
			if _, seen := byNet[net]; !seen {
				order = append(order, net)
			}
			at, code, err := pins.at(v.File, v.UUIDPath, it, ref, pin)
			byNet[net] = append(byNet[net], spot{v.File, at, code, err})
		}
	}
	if erc.PosScale == 0 {
		// the report's units are unknown: its positions cannot be checked
		// against the pins they name
		var out []kicadPwrFlagFix
		for _, net := range order {
			out = append(out, kicadPwrFlagFix{Net: net, Code: "ERC_UNITS_UNKNOWN", Err: "cannot check: " + erc.PosNote,
				Fix: "no PWR_FLAG placed; run with a released KiCad 10 kicad-cli (its version tells the units), or wire a PWR_FLAG to the net by hand"})
		}
		return out
	}
	floor := 1
	if files, err := kicad.SheetFiles(root); err == nil {
		for _, f := range files {
			if b, err := os.ReadFile(f); err == nil {
				floor = max(floor, kicad.MaxFlagRef(string(b))+1)
			}
		}
	}
	var out []kicadPwrFlagFix
	for _, net := range order {
		fix := kicadPwrFlagFix{Net: net}
		var errs []string
		done := false
		code, tried := "", false
		for _, s := range byNet[net] {
			fix.File, fix.At = s.file, s.at
			if s.err != nil {
				errs = append(errs, s.err.Error())
				code = s.code
				continue
			}
			tried = true
			ref, err := kicadPlacePwrFlag(s.file, s.at, floor, nets)
			if err == nil {
				fix.Ref, done = ref, true
				floor++
				break
			}
			errs = append(errs, err.Error())
		}
		if !done {
			sort.Strings(errs)
			fix.Err = strings.Join(errs, "; ")
			if !tried { // no pin of the net could be resolved: nothing was placed
				fix.Code = code
				fix.Fix = "no PWR_FLAG placed; wire a PWR_FLAG to this net by hand (or check that kicad-cli and the sheet files are of the same design)"
			}
		}
		out = append(out, fix)
	}
	for _, f := range out {
		if f.Ref != "" {
			if err := kicad.EnsurePwrFlagInLib(filepath.Dir(root)); err != nil {
				out = append(out, kicadPwrFlagFix{Err: err.Error()})
			}
			break
		}
	}
	return out
}

// ercPinSnap resolves an ERC pin item to the pin's exact sheet point
// (kicad.ERCPinPoint: the pin uuid, else the reference of that sheet
// instance — a repeated sheet's file stores one instance's reference in
// its Reference field). The report's position is only good to 0.01 mm on
// KiCad <= 10.0.6 (1/100 mm units, rounded), so a wire drawn there could
// miss an off-grid pin. The pin must lie within 1.27 mm of the report's
// position; farther off (a patched build, a mis-scaled report) the item is
// refused.
type ercPinSnap map[string]*kicad.SchEditor

func (c ercPinSnap) at(file, sheetPath string, it kicad.ERCItem, ref, pin string) (kicad.Pt, string, error) {
	e, ok := c[file]
	if !ok {
		e, _ = kicad.OpenSchematicFile(file)
		c[file] = e
	}
	if e == nil {
		return it.Pos, "ERC_PIN_UNRESOLVED", fmt.Errorf("%s pin %s: cannot read %s", ref, pin, file)
	}
	rep := it.Pos
	p, err := e.ERCPinPoint(it.UUID, sheetPath, ref, pin, &rep)
	if err != nil {
		return it.Pos, "ERC_PIN_UNRESOLVED", fmt.Errorf("%s pin %s in %s: %v", ref, pin, file, err)
	}
	if d := math.Hypot(p.At.X-rep.X, p.At.Y-rep.Y); d > 1.27 {
		return it.Pos, "ERC_POS_MISMATCH", fmt.Errorf("%s pin %s is at (%s, %s) in %s but ERC reports (%s, %s)", ref, pin,
			kicad.F(p.At.X), kicad.F(p.At.Y), file, kicad.F(rep.X), kicad.F(rep.Y))
	}
	return p.At, "", nil
}

// kicadPlacePwrFlag tries stubs of 2.54/5.08/7.62 mm right, left, down, up
// from at; the first whose page passes the strict gate is committed (netlist
// unchanged).
func kicadPlacePwrFlag(file string, at kicad.Pt, floor int, before map[string]string) (string, error) {
	orig, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if before == nil {
		return "", fmt.Errorf("no KiCad netlist of the design to verify the PWR_FLAG against")
	}
	var last string
	for _, stub := range []float64{2.54, 5.08, 7.62} {
		for _, dir := range []int{0, 2, 3, 1} {
			e, err := kicad.OpenSchematicFile(file)
			if err != nil {
				return "", err
			}
			e.SetFlagFloor(floor)
			ref, err := e.AddPwrFlag(at, dir, stub)
			if err != nil {
				return "", err
			}
			text, err := e.Render()
			if err != nil {
				return "", err
			}
			if g := kicad.GateSchematic(string(orig), text, kicad.CheckOptions{}); !g.OK {
				last = kicad.Summary(g.New, 2)
				continue
			}
			_, err = kicadSchCommitVerified(file, text, false, func(after map[string]string) error {
				if cmp := kicad.ComparePinNets(before, after, stripSheetPath); !cmp.Equal {
					return fmt.Errorf("the PWR_FLAG changes the netlist (%v)", cmp.Mismatched)
				}
				return nil
			})
			if err != nil {
				return "", err
			}
			return ref, nil
		}
	}
	return "", fmt.Errorf("no clean spot for a PWR_FLAG at (%s, %s) in %s: %s", kicad.F(at.X), kicad.F(at.Y), file, last)
}
