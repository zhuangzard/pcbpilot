package app

// cmd_kicad_fab_preflight.go — `pcbpilot kicad fab --preflight`: everything
// the fab can be asked before any output exists, as one tri-state verdict.
// Read-only: the board and schematic are checked on temporary copies and
// nothing is written to --out (or anywhere in the project).

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/zhuangzard/pcbpilot/internal/gate"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

const kicadFabPreflightKind = "pcbpilot-kicad-fab-preflight"

type fabAssemblyReport struct {
	Parts      int                 `json:"parts"`
	Assembled  int                 `json:"assembled"`
	WithLCSC   int                 `json:"withLcsc"`
	Missing    []kicad.MissingLCSC `json:"missingLcsc,omitempty"`
	Invalid    []kicad.MissingLCSC `json:"invalidLcsc,omitempty"`
	Problems   []kicad.FabProblem  `json:"problems,omitempty"`
	CPLOmitted []string            `json:"cplOmitted,omitempty"`
	CPLSides   []string            `json:"cplSides,omitempty"` // top / bottom
	Warnings   []string            `json:"warnings,omitempty"`
}

type fabPreflightReport struct {
	Kind     string                `json:"kind"`
	Version  int                   `json:"version"`
	Verdict  gate.Status           `json:"verdict"`
	ExitCode int                   `json:"exitCode"`
	Checks   []drcCheck            `json:"checks"`
	PCB      string                `json:"pcb"`
	Sch      string                `json:"sch,omitempty"`
	Contract *kicad.FabContract    `json:"contract,omitempty"`
	Rules    *kicad.EffectiveRules `json:"rules,omitempty"`
	Assembly *fabAssemblyReport    `json:"assembly,omitempty"`
	DRC      *kicadDRCReport       `json:"drc,omitempty"`
	Notes    []string              `json:"notes,omitempty"`
}

type fabPreflightOptions struct {
	pcb, sch, contract, overrides, cli string
	noAssembly                         bool
	waivers                            []gateWaiver
}

func runFabPreflight(o fabPreflightOptions) *fabPreflightReport {
	rep := &fabPreflightReport{Kind: kicadFabPreflightKind, Version: 1, PCB: o.pcb, Sch: o.sch}
	add := func(c drcCheck) { rep.Checks = append(rep.Checks, c) }
	defer func() {
		rep.Verdict = gate.Aggregate(checkResults(rep.Checks)...)
		rep.ExitCode = rep.Verdict.ExitCode()
	}()
	data, err := os.ReadFile(o.pcb)
	var bd *kicad.Board
	if err == nil {
		bd, err = kicad.ParseBoard(data)
	}
	if err != nil {
		add(cannotCheck("board", "cannot read the board: "+err.Error()))
		return rep
	}

	// 1. DRC (+ parity when the project's root schematic is there).
	root := strings.TrimSuffix(o.pcb, ".kicad_pcb") + ".kicad_sch"
	parity := false
	if o.sch != "" {
		if _, err := os.Stat(root); err == nil {
			parity = true
		}
	}
	rep.DRC = runKicadDRC(kicadDRCOptions{pcb: o.pcb, cli: o.cli, parity: parity, waivers: o.waivers, tolMm: 0.5})
	rep.Checks = append(rep.Checks, rep.DRC.Checks...)
	if o.sch != "" && !parity {
		add(cannotCheck("schematic-parity", fmt.Sprintf("KiCad compares a board with its project's root schematic %s, which is not next to the board (fix: keep the board and %s in one KiCad project)", filepath.Base(root), filepath.Base(root))))
	}

	// 2. Board outline: KiCad's own outline test runs inside DRC.
	add(outlineCheck(data, rep.DRC))

	// 3. Contract and what it bounds.
	var ct *kicad.FabContract
	if o.contract == "" {
		add(cannotCheck("contract", "no --contract: the fab's limits, layer count, finish and assembly sides are unknown (fix: write the order contract, pcbpilot-fab skill fab-acceptance.md §1)"))
	} else if c, err := kicad.LoadFabContract(o.contract); err != nil {
		add(drcCheck{Result: gate.Result{Name: "contract", Status: gate.Fail, Detail: err.Error()}})
	} else {
		ct, rep.Contract = c, c
		var dates []string
		for _, s := range c.Sources {
			dates = append(dates, s.Retrieved)
		}
		sort.Strings(dates)
		add(drcCheck{Result: gate.Result{Name: "contract", Status: gate.Pass,
			Detail: fmt.Sprintf("%s, %d layer(s); sources retrieved %s (re-read the vendor page on order day)", c.Vendor, c.Layers, strings.Join(dedupeStrings(dates), ", "))}})
	}
	add(layerCountCheck(bd, ct))
	rules, err := kicad.ReadEffectiveRules(o.pcb)
	if err != nil {
		for _, n := range []string{"min-track", "min-space", "min-drill", "min-annular"} {
			add(cannotCheck(n, "cannot read the board's rules: "+err.Error()))
		}
	} else {
		rep.Rules = rules
		for _, e := range rules.Items() {
			add(ruleLimitCheck(e, ct, rules.Measured))
		}
	}
	add(finishCheck(data, ct))

	// 4. Assembly readiness: LCSC coverage and BOM / CPL designators.
	if o.noAssembly {
		for _, n := range []string{"assembly-sides", "lcsc", "bom-cpl"} {
			add(drcCheck{Result: gate.Result{Name: n, Status: gate.Skip, Advisory: true, Detail: "--no-assembly: bare-board order"}})
		}
		return rep
	}
	asm, cs := preflightAssembly(o, bd)
	rep.Assembly = asm
	add(sidesCheck(asm, ct))
	rep.Checks = append(rep.Checks, cs...)
	return rep
}

func outlineCheck(board []byte, drc *kicadDRCReport) drcCheck {
	c := drcCheck{Result: gate.Result{Name: "board-outline"}}
	switch {
	case !kicad.HasEdgeCuts(board):
		c.Status, c.Detail = gate.Fail, "nothing on Edge.Cuts: the board has no outline (fix: draw a closed board outline on Edge.Cuts)"
		return c
	case drc.Run == nil:
		return cannotCheck(c.Name, "KiCad's outline test runs inside DRC, which could not run")
	case drc.Run.Policy.Ignores("invalid_outline"):
		return cannotCheck(c.Name, ".kicad_pro sets invalid_outline to ignore, so KiCad did not test the outline (fix: set it back to error)")
	}
	var items []string
	for _, v := range append(append([]kicad.OwnedViolation{}, drc.Violations...), drc.Excluded...) {
		if v.Rule == "invalid_outline" {
			it := v.Item()
			if v.Excluded {
				it = "EXCLUDED " + it
			}
			items = append(items, it)
		}
	}
	c.Detail = "Edge.Cuts forms closed outline(s): KiCad's outline test (DRC invalid_outline) reported nothing"
	if len(items) > 0 {
		c.Detail = fmt.Sprintf("%d invalid_outline item(s): the outline is not closed (fix: close the Edge.Cuts gaps at the positions listed)", len(items))
	}
	c.Items, c.Status = items, gate.FromBool(len(items) == 0)
	return c
}

func layerCountCheck(bd *kicad.Board, ct *kicad.FabContract) drcCheck {
	n := 0
	for _, l := range bd.Layers {
		if l.IsCopper {
			n++
		}
	}
	if ct == nil {
		return cannotCheck("layer-count", fmt.Sprintf("board has %d copper layer(s); no valid contract to compare with", n))
	}
	return drcCheck{Result: gate.Result{Name: "layer-count", Status: gate.FromBool(n == ct.Layers),
		Detail: fmt.Sprintf("board %d copper layer(s), contract %d", n, ct.Layers)}}
}

var ruleCheckName = map[string]string{"track": "min-track", "space": "min-space", "drill": "min-drill", "annular": "min-annular"}
var ruleContractKey = map[string]string{"track": "limits.minTrackMm", "space": "limits.minSpaceMm", "drill": "limits.minDrillMm", "annular": "limits.minAnnularMm"}

// ruleLimitCheck: the smallest value any rule allows must be at or above
// the contract limit (1 nm slack for decimal file values).
func ruleLimitCheck(e *kicad.EffectiveMin, ct *kicad.FabContract, m kicad.Measured) drcCheck {
	name := ruleCheckName[e.Item]
	c := drcCheck{Result: gate.Result{Name: name}}
	for _, s := range e.Sources {
		c.Info = append(c.Info, fmt.Sprintf("%s %s = %g mm", s.Source, s.Where, s.Mm))
	}
	c.Info = append(c.Info, e.Unset...)
	switch {
	case e.Item == "track" && m.MinTrackMm != nil:
		c.Info = append(c.Info, fmt.Sprintf("narrowest track on the board: %g mm", *m.MinTrackMm))
	case e.Item == "drill" && (m.MinViaDrillMm != nil || m.MinPadDrillMm != nil):
		c.Info = append(c.Info, fmt.Sprintf("smallest drill on the board: via %s, pad %s", mmOrNone(m.MinViaDrillMm), mmOrNone(m.MinPadDrillMm)))
	}
	if ct == nil {
		c.Status, c.Detail = gate.CannotCheck, "no valid contract to compare with"
		return c
	}
	limit := ct.Limit(e.Item)
	switch {
	case limit <= 0:
		c.Status, c.Detail = gate.CannotCheck, "the contract gives no "+ruleContractKey[e.Item]
	case len(e.Unknown) > 0:
		c.Status, c.Detail = gate.CannotCheck, "rules pcbpilot cannot read bound it: "+strings.Join(e.Unknown, "; ")+" (fix: check them by hand against the contract)"
	case e.MinMm == nil:
		c.Status, c.Detail = gate.CannotCheck, fmt.Sprintf("no rule bounds the %s (fix: set the Board Setup constraint to the contract's %g mm)", e.Item, limit)
	default:
		var below []string
		for _, s := range e.Sources {
			if s.Mm < limit-1e-6 {
				below = append(below, fmt.Sprintf("%s %s = %g mm", s.Source, s.Where, s.Mm))
			}
		}
		c.Items = below
		c.Status = gate.FromBool(len(below) == 0)
		c.Detail = fmt.Sprintf("smallest allowed %s %g mm vs contract %g mm", e.Item, *e.MinMm, limit)
		if len(below) > 0 {
			c.Detail += fmt.Sprintf(": %d rule value(s) below the contract (fix: raise them in Board Setup / .kicad_dru and rerun DRC; never lower the contract)", len(below))
		}
	}
	return c
}

func mmOrNone(v *float64) string {
	if v == nil {
		return "none"
	}
	return fmt.Sprintf("%g mm", *v)
}

func finishCheck(board []byte, ct *kicad.FabContract) drcCheck {
	c := drcCheck{Result: gate.Result{Name: "finish", Advisory: true}}
	have := kicad.BoardFinish(board)
	switch {
	case ct == nil:
		return cannotCheck(c.Name, "no valid contract to compare with")
	case ct.Finish == "":
		c.Status, c.Detail = gate.Skip, "the contract names no finish"
	case have == "":
		c.Status, c.Detail = gate.Warn, "the board's stackup names no copper finish; the order form decides ("+ct.Finish+")"
	case strings.Contains(strings.ToLower(have), strings.ToLower(ct.Finish)) || strings.Contains(strings.ToLower(ct.Finish), strings.ToLower(have)):
		c.Status, c.Detail = gate.Pass, fmt.Sprintf("board stackup %q, contract %q", have, ct.Finish)
	default:
		c.Advisory = false
		c.Status, c.Detail = gate.Fail, fmt.Sprintf("board stackup %q, contract %q (fix: set Board Setup > Physical Stackup finish or the contract, with the user)", have, ct.Finish)
	}
	return c
}

func sidesCheck(asm *fabAssemblyReport, ct *kicad.FabContract) drcCheck {
	name := "assembly-sides"
	if asm == nil || asm.CPLSides == nil {
		return cannotCheck(name, "no CPL to read the placed sides from")
	}
	have := "none"
	switch {
	case len(asm.CPLSides) == 2:
		have = "both"
	case len(asm.CPLSides) == 1:
		have = asm.CPLSides[0]
	}
	if ct == nil || ct.Assembly == nil || ct.Assembly.Sides == "" {
		return cannotCheck(name, fmt.Sprintf("CPL places parts on %s; the contract gives no assembly.sides", have))
	}
	return drcCheck{Result: gate.Result{Name: name, Status: gate.FromBool(have == ct.Assembly.Sides),
		Detail: fmt.Sprintf("CPL places parts on %s, contract assembly.sides %s", have, ct.Assembly.Sides)}}
}

// preflightAssembly is RunFab's LCSC and BOM / CPL checks on temporary
// copies: the parts (board + schematic fields), kicad-cli's position file
// turned into the CPL, then CheckLCSC and CheckBOMCPL.
func preflightAssembly(o fabPreflightOptions, bd *kicad.Board) (*fabAssemblyReport, []drcCheck) {
	fail := func(why string) (*fabAssemblyReport, []drcCheck) {
		return nil, []drcCheck{cannotCheck("lcsc", why), cannotCheck("bom-cpl", why)}
	}
	tmp, err := os.MkdirTemp("", "pcbpilot-preflight-")
	if err != nil {
		return fail(err.Error())
	}
	defer os.RemoveAll(tmp)
	pcb, err := filepath.Abs(o.pcb)
	if err != nil {
		return fail(err.Error())
	}
	if _, err := kicad.StageProject(pcb, filepath.Join(tmp, "board"), false); err != nil {
		return fail(err.Error())
	}
	var rows []kicad.SchPart
	if o.sch != "" {
		staged, err := kicad.StageSchematic(o.sch, filepath.Join(tmp, "sch"))
		if err == nil {
			rows, err = kicad.ReadSchematicParts(o.cli, staged)
		}
		if err != nil {
			return fail("cannot read the schematic's BOM fields: " + err.Error())
		}
	}
	parts, warns := kicad.MergeParts(bd, rows, o.sch != "")
	asm := &fabAssemblyReport{Parts: len(parts), Warnings: warns}
	asm.Missing, asm.Invalid = kicad.CheckLCSC(parts)
	for _, p := range parts {
		if p.Assembled() {
			asm.Assembled++
			if kicad.ValidLCSC(p.LCSC) {
				asm.WithLCSC++
			}
		}
	}
	lc := drcCheck{Result: gate.Result{Name: "lcsc", Detail: fmt.Sprintf("%d of %d assembled part(s) have a valid LCSC number; %d missing, %d invalid", asm.WithLCSC, asm.Assembled, len(asm.Missing), len(asm.Invalid))}}
	for _, m := range asm.Missing {
		lc.Items = append(lc.Items, fmt.Sprintf("%s %s %s: no LCSC number", m.Ref, m.Value, m.Footprint))
	}
	for _, m := range asm.Invalid {
		lc.Items = append(lc.Items, fmt.Sprintf("%s %s %s: invalid LCSC %q (want C + digits)", m.Ref, m.Value, m.Footprint, m.LCSC))
	}
	lc.Status = gate.FromBool(len(lc.Items) == 0)
	if len(lc.Items) > 0 {
		lc.Detail += " (fix: pcbpilot kicad lcsc --set REF=Cxxxx, or mark the part DNP / exclude from BOM and position files, or --no-assembly)"
	}
	ov, err := kicad.LoadCPLOverrides(o.overrides)
	if err != nil {
		return asm, []drcCheck{lc, cannotCheck("bom-cpl", err.Error())}
	}
	stagedPCB := filepath.Join(tmp, "board", filepath.Base(pcb))
	cpl, _, omitted, err := kicad.BuildCPL(o.cli, stagedPCB, filepath.Join(tmp, "pos.csv"), bd, ov, parts, nil)
	if err != nil {
		return asm, []drcCheck{lc, cannotCheck("bom-cpl", "kicad-cli position export: "+err.Error())}
	}
	asm.CPLOmitted = omitted
	asm.CPLSides = cplSides(cpl)
	asm.Problems = kicad.CheckBOMCPL(parts, cpl)
	bc := drcCheck{Result: gate.Result{Name: "bom-cpl", Detail: fmt.Sprintf("%d CPL part(s), %d left out as not assembled; %d designator problem(s)", int(math.Max(0, float64(len(cpl)-1))), len(omitted), len(asm.Problems))}}
	for _, p := range asm.Problems {
		bc.Items = append(bc.Items, fmt.Sprintf("%s %s (fix: %s)", p.Code, p.Message, p.Fix))
	}
	bc.Status = gate.FromBool(len(bc.Items) == 0)
	return asm, []drcCheck{lc, bc}
}

// cplSides is the set of placed sides of CPL rows (header first): top,
// bottom; empty (not nil) without parts.
func cplSides(rows [][]string) []string {
	col := -1
	out := []string{}
	if len(rows) == 0 {
		return out
	}
	for i, h := range rows[0] {
		if h == "Layer" {
			col = i
		}
	}
	seen := map[string]bool{}
	for _, r := range rows[1:] {
		if col < 0 || col >= len(r) {
			continue
		}
		s := strings.ToLower(strings.TrimSpace(r[col]))
		if (s == "top" || s == "bottom") && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a] == "top" })
	return out
}
