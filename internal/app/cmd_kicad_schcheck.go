package app

// cmd_kicad_schcheck.go — `pcbpilot kicad sch-check`: the schematic hard gate
// on KiCad: kicad-cli ERC (JSON) of the hierarchy plus pcbpilot's own
// quality checks of every sheet (kicad.CheckSchematic). --fix-pwr-flag wires
// a PWR_FLAG to every power net ERC reports as not driven (one per net),
// each placement gated and netlist-verified like every other KiCad edit.

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
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
}

type kicadSchCheckReport struct {
	OK       bool                 `json:"ok"`
	Root     string               `json:"root"`
	ERC      *kicad.ERCReport     `json:"erc"`
	Quality  []kicadSheetFindings `json:"quality"`
	Findings int                  `json:"qualityFindings"`
	Fixed    []kicadPwrFlagFix    `json:"pwrFlags,omitempty"`
}

func newKicadSchCheckCmd(stdout, stderr io.Writer) *cobra.Command {
	var sch string
	var fixPwr bool
	var ignore []string
	c := &cobra.Command{
		Use:   "sch-check",
		Short: "Schematic hard gate on KiCad: kicad-cli ERC + pcbpilot quality checks (JSON, non-zero on fail)",
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
imported sheet whose parts sit off the 1.27 mm grid).`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot kicad sch-check --sch board.kicad_sch
  pcbpilot kicad sch-check --sch board.kicad_sch --fix-pwr-flag`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sch == "" {
				return fmt.Errorf("--sch is required")
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
				rep.Quality = append(rep.Quality, kicadSheetFindings{File: f, Findings: fs})
				rep.Findings += len(fs)
			}
			rep.OK = erc.Errors == 0 && rep.Findings == 0
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
	return c
}

// kicadFixPwrFlags places one PWR_FLAG per undriven power net.
func kicadFixPwrFlags(root string, erc *kicad.ERCReport) []kicadPwrFlagFix {
	nets, _ := kicadBeforeNets(root)
	type spot struct {
		file string
		at   kicad.Pt
	}
	byNet := map[string][]spot{}
	var order []string
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
			byNet[net] = append(byNet[net], spot{v.File, it.Pos})
		}
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
		for _, s := range byNet[net] {
			fix.File, fix.At = s.file, s.at
			ref, err := kicadPlacePwrFlag(s.file, s.at, floor)
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
		}
		out = append(out, fix)
	}
	return out
}

// kicadPlacePwrFlag tries stubs of 2.54/5.08/7.62 mm right, left, down, up
// from at; the first whose page passes the strict gate is committed (netlist
// unchanged).
func kicadPlacePwrFlag(file string, at kicad.Pt, floor int) (string, error) {
	orig, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	before, err := kicadBeforeNets(file)
	if err != nil {
		return "", err
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
