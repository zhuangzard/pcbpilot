package app

// cmd_kicad_lcsc.go — `pcbpilot kicad lcsc`: check / write / search LCSC part
// numbers on a KiCad design. --search reuses the skill's parts-select.py (the
// one JLC SMT catalog client pcbpilot has) instead of a second HTTP client.

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

var partsSelectAsset = skillAsset{
	name:       "parts-select.py",
	rels:       []string{"pcbpilot/scripts/parts-select.py"},
	searchPath: true,
	flagHint:   "--parts-select",
}

// partsSelectHit is one row of `parts-select.py --online --json`.
type partsSelectHit struct {
	LCSC      string  `json:"lcsc"`
	MPN       string  `json:"mpn"`
	Brand     string  `json:"brand"`
	Desc      string  `json:"desc"`
	Package   string  `json:"package"`
	Base      bool    `json:"base"`
	Preferred bool    `json:"preferred"`
	Stock     int64   `json:"stock"`
	Unit      float64 `json:"unit"`
	Key       string  `json:"key"` // offline (standard-parts.json) rows
}

func newKiCadLcscCmd(stdout, stderr io.Writer) *cobra.Command {
	var pcb, sch, search, field, scriptPath string
	var sets []string
	var check, offline, asJSON bool
	var qty, limit int
	c := &cobra.Command{
		Use:   "lcsc",
		Short: "Check, write or search LCSC (JLC) part numbers on a KiCad design",
		Args:  cobra.NoArgs,
		Long: `Exactly one mode:

  --check            list placed parts that need but lack an LCSC number (exit 1 if any)
  --set REF=Cxxxx    write LCSC numbers (repeat or comma-separate). Updates an existing
                     "LCSC Part #"/"LCSC"/"LCSC Part"/"JLCPCB Part #" field, else creates
                     --field (default "LCSC Part #", hidden). The board is re-saved by
                     KiCad's pcbnew; the schematic (and its sub-sheets) gets a minimal
                     text edit. Close the files in KiCad first.
  --search "query"   search the JLCPCB SMT catalog (live, via the skill's parts-select.py):
                     LCSC number, basic/extended, stock, package, MPN, description.`,
		Example: `  pcbpilot kicad lcsc --pcb b.kicad_pcb --sch b.kicad_sch --check
  pcbpilot kicad lcsc --pcb b.kicad_pcb --sch b.kicad_sch --set R1=C25744 --set C1=C1525,C2=C1525
  pcbpilot kicad lcsc --search "10k 0402"
  pcbpilot kicad lcsc --search "AMS1117-3.3" --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			modes := 0
			for _, on := range []bool{check, len(sets) > 0, search != ""} {
				if on {
					modes++
				}
			}
			if modes != 1 {
				return fmt.Errorf("choose exactly one of --check, --set, --search")
			}
			switch {
			case search != "":
				return runLcscSearch(stdout, scriptPath, search, offline, qty, limit, asJSON)
			case check:
				return runLcscCheck(stdout, stderr, pcb, sch, asJSON)
			default:
				return runLcscSet(stdout, pcb, sch, sets, field)
			}
		},
	}
	f := c.Flags()
	f.StringVar(&pcb, "pcb", "", "board file (.kicad_pcb)")
	f.StringVar(&sch, "sch", "", "root schematic (.kicad_sch)")
	f.BoolVar(&check, "check", false, "list placed parts without an LCSC number")
	f.StringArrayVar(&sets, "set", nil, "REF=Cxxxx assignment(s)")
	f.StringVar(&field, "field", kicad.DefaultLCSCField, "field name to create when a part has none")
	f.StringVar(&search, "search", "", "JLCPCB catalog query (value+package, MPN or C-number)")
	f.BoolVar(&offline, "offline", false, "--search: only the curated standard-parts.json (no network)")
	f.IntVar(&qty, "qty", 100, "--search: build quantity for stock ranking")
	f.IntVar(&limit, "limit", 10, "--search: rows to print")
	f.BoolVar(&asJSON, "json", false, "machine-readable output (--check, --search)")
	f.StringVar(&scriptPath, "parts-select", "", "path to parts-select.py (default: the installed skill)")
	return c
}

func runLcscCheck(stdout, stderr io.Writer, pcb, sch string, asJSON bool) error {
	if pcb == "" {
		return fmt.Errorf("--check needs --pcb (placed parts come from the board)")
	}
	bd, err := kicad.ReadBoard(pcb)
	if err != nil {
		return err
	}
	var rows []kicad.SchPart
	if sch != "" {
		cli, err := kicad.ResolveFabCLI()
		if err != nil {
			return err
		}
		if rows, err = kicad.ReadSchematicParts(cli, sch); err != nil {
			return err
		}
	}
	parts, warns := kicad.MergeParts(bd, rows, sch != "")
	missing, invalid := kicad.CheckLCSC(parts)
	if asJSON {
		b, _ := json.MarshalIndent(map[string]any{"parts": parts, "missing": missing, "invalid": invalid, "warnings": warns}, "", "  ")
		fmt.Fprintln(stdout, string(b))
	} else {
		for _, w := range warns {
			fmt.Fprintln(stderr, "warning:", w)
		}
		assembled := 0
		for _, p := range parts {
			if p.Assembled() {
				assembled++
			}
		}
		fmt.Fprintf(stdout, "%d footprints, %d assembled, %d missing LCSC, %d invalid\n", len(parts), assembled, len(missing), len(invalid))
		printLCSCProblems(stdout, missing, invalid)
	}
	if len(missing)+len(invalid) > 0 {
		return errActionFailed
	}
	return nil
}

func runLcscSet(stdout io.Writer, pcb, sch string, sets []string, field string) error {
	if pcb == "" && sch == "" {
		return fmt.Errorf("--set needs --pcb and/or --sch")
	}
	assign, err := kicad.ParseAssignments(sets)
	if err != nil {
		return err
	}
	tools := kicad.FabTools{}
	if py, err := kicad.ResolveFabPython(); err == nil {
		tools.Python = py
	} else if pcb != "" {
		return err
	}
	res, err := kicad.SetLCSC(tools, pcb, sch, assign, field)
	for _, r := range res {
		fmt.Fprintf(stdout, "%s: %d changed\n", r.File, len(r.Changed))
		for ref, ch := range r.Changed {
			fmt.Fprintf(stdout, "  %s %s\n", ref, ch)
		}
		if len(r.NotFound) > 0 {
			fmt.Fprintf(stdout, "  not found: %s\n", strings.Join(r.NotFound, ", "))
		}
	}
	return err
}

func runLcscSearch(stdout io.Writer, scriptPath, query string, offline bool, qty, limit int, asJSON bool) error {
	script, err := partsSelectAsset.resolve(scriptPath)
	if err != nil {
		return err
	}
	args := []string{query, "--json", "--qty", strconv.Itoa(qty)}
	if !offline {
		args = append(args, "--online")
	}
	cmd, err := pythonCommand(script, args...)
	if err != nil {
		return err
	}
	out, err := cmd.Output()
	// parts-select exits 1 with "[]" for an explicit-resistance query without a
	// verified match; that is a result, not a crash.
	var hits []partsSelectHit
	if jerr := json.Unmarshal(out, &hits); jerr != nil {
		if err != nil {
			return fmt.Errorf("parts-select.py: %w", err)
		}
		return fmt.Errorf("parts-select.py: bad JSON: %w", jerr)
	}
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	if asJSON {
		b, _ := json.MarshalIndent(hits, "", "  ")
		fmt.Fprintln(stdout, string(b))
		return nil
	}
	src := "jlcpcb.com (live)"
	if offline {
		src = "standard-parts.json (offline; no stock data)"
	}
	fmt.Fprintf(stdout, "query=%q source=%s qty=%d results=%d\n", query, src, qty, len(hits))
	fmt.Fprintf(stdout, "%-10s %-6s %10s  %-12s %-22s %s\n", "LCSC", "type", "stock", "package", "MPN", "description")
	for _, h := range hits {
		typ := "ext"
		if h.Base {
			typ = "BASIC"
		}
		stock := strconv.FormatInt(h.Stock, 10)
		if offline {
			stock = "-"
		}
		fmt.Fprintf(stdout, "%-10s %-6s %10s  %-12s %-22s %s\n", h.LCSC, typ, stock, lcscTrunc(h.Package, 12), lcscTrunc(h.MPN, 22), lcscTrunc(h.Desc, 70))
	}
	if len(hits) > 0 {
		fmt.Fprintf(stdout, "assign: pcbpilot kicad lcsc --pcb <board> --sch <sch> --set <REF>=%s\n", hits[0].LCSC)
	}
	return nil
}

func lcscTrunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
