package app

// cmd_kicad_lib.go — CLI commands for KiCad library authoring:
//   pcbpilot kicad lib-sym    — create a symbol from a JSON spec
//   pcbpilot kicad lib-fp     — create a footprint from a JSON spec
//   pcbpilot kicad lib-fp-edit — edit pads of an existing footprint
//   pcbpilot kicad lib-register — register libraries in project tables
//   pcbpilot kicad pin-pad-check — check pin↔pad matching

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// ---- lib-sym ----

func newKicadLibSymCmd(stdout io.Writer) *cobra.Command {
	var specPath, outDir, libName string
	c := &cobra.Command{
		Use:   "lib-sym",
		Short: "Create a KiCad symbol (.kicad_sym) from a JSON pin-table spec",
		Long: `Reads a SymbolSpec JSON from --spec (or stdin) and writes a .kicad_sym file.
The spec describes pins (number, name, type, side, unit), properties
(Reference, Value, Footprint, Datasheet, LCSC), and layout options.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readSpecInput(specPath)
			if err != nil {
				return err
			}
			var spec kicad.SymbolSpec
			if err := json.Unmarshal(data, &spec); err != nil {
				return fmt.Errorf("parse spec: %w", err)
			}

			symText, err := kicad.AuthorSymbol(spec)
			if err != nil {
				return err
			}

			if outDir == "" {
				// Write to stdout.
				lib := kicad.AuthorSymbolLib(map[string]string{spec.Name: symText})
				fmt.Fprint(stdout, lib)
				return nil
			}

			// Write to file.
			name := libName
			if name == "" {
				name = "pcbpilot"
			}
			path := filepath.Join(outDir, name+".kicad_sym")
			if err := os.MkdirAll(outDir, 0o755); err != nil {
				return err
			}

			// If file exists, merge symbols (preserve existing, add/replace ours).
			symbols := map[string]string{spec.Name: symText}
			if _, rerr := os.Stat(path); rerr == nil {
				names, nerr := kicad.LibSymbolNames(path)
				if nerr != nil {
					return fmt.Errorf("read existing library %s: %w", path, nerr)
				}
				for _, n := range names {
					if n == spec.Name {
						continue // will be overwritten by the new symbol
					}
					t, terr := kicad.LibSymbolCached(path, n)
					if terr != nil {
						return fmt.Errorf("read existing symbol %q from %s: %w", n, path, terr)
					}
					symbols[n] = t
				}
			} else if !os.IsNotExist(rerr) {
				return fmt.Errorf("stat %s: %w", path, rerr)
			}

			lib := kicad.AuthorSymbolLib(symbols)
			if err := os.WriteFile(path, []byte(lib), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "wrote %s\n", path)
			return nil
		},
	}
	c.Flags().StringVar(&specPath, "spec", "", "path to JSON spec file (- or omit for stdin)")
	c.Flags().StringVar(&outDir, "out", "", "output directory (omit for stdout)")
	c.Flags().StringVar(&libName, "lib-name", "", "library file name without extension (default: pcbpilot)")
	return c
}

// ---- lib-fp ----

func newKicadLibFPCmd(stdout io.Writer) *cobra.Command {
	var specPath, outDir, libName string
	c := &cobra.Command{
		Use:   "lib-fp",
		Short: "Create a KiCad footprint (.kicad_mod) from a JSON pad spec",
		Long: `Reads a FootprintSpec JSON from --spec (or stdin) and writes a .kicad_mod file
inside a .pretty library directory. The spec describes pads (number, type, shape,
position, size, drill, layers), courtyard margin, and outline options.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := readSpecInput(specPath)
			if err != nil {
				return err
			}
			var spec kicad.FootprintSpec
			if err := json.Unmarshal(data, &spec); err != nil {
				return fmt.Errorf("parse spec: %w", err)
			}

			fpText, err := kicad.AuthorFootprint(spec)
			if err != nil {
				return err
			}

			if outDir == "" {
				fmt.Fprint(stdout, fpText)
				return nil
			}

			name := libName
			if name == "" {
				name = "pcbpilot"
			}
			prettyDir := filepath.Join(outDir, name+".pretty")
			if err := os.MkdirAll(prettyDir, 0o755); err != nil {
				return err
			}
			path := filepath.Join(prettyDir, spec.Name+".kicad_mod")
			if err := os.WriteFile(path, []byte(fpText), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "wrote %s\n", path)
			return nil
		},
	}
	c.Flags().StringVar(&specPath, "spec", "", "path to JSON spec file (- or omit for stdin)")
	c.Flags().StringVar(&outDir, "out", "", "output directory (omit for stdout)")
	c.Flags().StringVar(&libName, "lib-name", "", "library name (creates <name>.pretty/; default: pcbpilot)")
	return c
}

// ---- lib-fp-edit ----

func newKicadLibFPEditCmd(stdout io.Writer) *cobra.Command {
	var footprintPath, editsJSON string
	c := &cobra.Command{
		Use:   "lib-fp-edit",
		Short: "Edit pads of an existing .kicad_mod footprint",
		RunE: func(cmd *cobra.Command, args []string) error {
			if footprintPath == "" {
				return fmt.Errorf("--footprint is required")
			}
			data, err := readSpecInput(editsJSON)
			if err != nil {
				return err
			}
			var edits []kicad.PadEdit
			if err := json.Unmarshal(data, &edits); err != nil {
				return fmt.Errorf("parse edits: %w", err)
			}

			src, err := os.ReadFile(footprintPath)
			if err != nil {
				return err
			}

			result, err := kicad.EditFootprintPads(string(src), edits)
			if err != nil {
				return err
			}

			if err := os.WriteFile(footprintPath, []byte(result), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(stdout, "edited %s\n", footprintPath)
			return nil
		},
	}
	c.Flags().StringVar(&footprintPath, "footprint", "", "path to .kicad_mod file")
	c.Flags().StringVar(&editsJSON, "edits", "", "path to JSON edits file (- or omit for stdin)")
	return c
}

// ---- lib-register ----

func newKicadLibRegisterCmd(stdout io.Writer) *cobra.Command {
	var projectDir string
	var symEntries, fpEntries []string
	c := &cobra.Command{
		Use:   "lib-register",
		Short: "Register libraries in project sym-lib-table / fp-lib-table",
		Long: `Ensures library entries exist in the project's library tables with
${KIPRJMOD}-relative paths. Creates tables if they don't exist.

Examples:
  pcbpilot kicad lib-register --project ./myproj --sym mylib=lib/mylib.kicad_sym
  pcbpilot kicad lib-register --project ./myproj --fp mylib=lib/mylib.pretty`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if projectDir == "" {
				return fmt.Errorf("--project is required")
			}

			for _, entry := range symEntries {
				nick, relPath, ok := strings.Cut(entry, "=")
				if !ok || nick == "" || relPath == "" {
					return fmt.Errorf("--sym must be nickname=path, got %q", entry)
				}
				tablePath := filepath.Join(projectDir, "sym-lib-table")
				changed, err := kicad.EnsureLibTableEntry(tablePath, "sym_lib_table", nick, relPath)
				if err != nil {
					return fmt.Errorf("sym-lib-table: %w", err)
				}
				if changed {
					fmt.Fprintf(stdout, "sym-lib-table: added/updated %q → %s\n", nick, relPath)
				} else {
					fmt.Fprintf(stdout, "sym-lib-table: %q already up to date\n", nick)
				}
			}

			for _, entry := range fpEntries {
				nick, relPath, ok := strings.Cut(entry, "=")
				if !ok || nick == "" || relPath == "" {
					return fmt.Errorf("--fp must be nickname=path, got %q", entry)
				}
				tablePath := filepath.Join(projectDir, "fp-lib-table")
				changed, err := kicad.EnsureLibTableEntry(tablePath, "fp_lib_table", nick, relPath)
				if err != nil {
					return fmt.Errorf("fp-lib-table: %w", err)
				}
				if changed {
					fmt.Fprintf(stdout, "fp-lib-table: added/updated %q → %s\n", nick, relPath)
				} else {
					fmt.Fprintf(stdout, "fp-lib-table: %q already up to date\n", nick)
				}
			}

			return nil
		},
	}
	c.Flags().StringVar(&projectDir, "project", "", "KiCad project directory")
	c.Flags().StringArrayVar(&symEntries, "sym", nil, "symbol library: nickname=relative/path.kicad_sym (repeatable)")
	c.Flags().StringArrayVar(&fpEntries, "fp", nil, "footprint library: nickname=relative/path.pretty (repeatable)")
	return c
}

// ---- pin-pad-check ----

func newKicadPinPadCheckCmd(stdout io.Writer) *cobra.Command {
	var symbolPath, footprintPath string
	var excludePads []string
	var asJSON bool
	c := &cobra.Command{
		Use:   "pin-pad-check",
		Short: "Check pin↔pad consistency between a symbol and footprint",
		Long: `Verifies that every symbol pin number has a matching footprint pad and
vice versa. Reports unmatched pins and pads. Exit code 1 if mismatches found.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if symbolPath == "" || footprintPath == "" {
				return fmt.Errorf("--symbol and --footprint are required")
			}

			symData, err := os.ReadFile(symbolPath)
			if err != nil {
				return fmt.Errorf("read symbol: %w", err)
			}
			fpData, err := os.ReadFile(footprintPath)
			if err != nil {
				return fmt.Errorf("read footprint: %w", err)
			}

			// If the symbol file is a .kicad_sym library, extract the first symbol.
			symText := string(symData)
			if strings.Contains(symText, "kicad_symbol_lib") {
				names, err := kicad.LibSymbolNames(symbolPath)
				if err != nil {
					return fmt.Errorf("list symbols: %w", err)
				}
				if len(names) == 0 {
					return fmt.Errorf("no symbols in %s", symbolPath)
				}
				symText, err = kicad.LibSymbolCached(symbolPath, names[0])
				if err != nil {
					return fmt.Errorf("read symbol %q: %w", names[0], err)
				}
			}

			result, err := kicad.CheckPinPad(symText, string(fpData), excludePads)
			if err != nil {
				return err
			}

			if asJSON {
				enc := json.NewEncoder(stdout)
				enc.SetIndent("", "  ")
				if err := enc.Encode(result); err != nil {
					return err
				}
				if !result.OK {
					cmd.SilenceUsage = true
					return fmt.Errorf("pin-pad check failed")
				}
				return nil
			}

			if result.OK {
				fmt.Fprintf(stdout, "OK: %d pins matched\n", result.MatchedCount)
				return nil
			}

			for _, e := range result.Errors {
				fmt.Fprintf(stdout, "MISMATCH: %s\n", e)
			}
			// Signal mismatch via non-zero exit.
			cmd.SilenceUsage = true
			return fmt.Errorf("pin-pad check failed: %d unmatched pins, %d unmatched pads",
				len(result.UnmatchedPins), len(result.UnmatchedPads))
		},
	}
	c.Flags().StringVar(&symbolPath, "symbol", "", "path to .kicad_sym file")
	c.Flags().StringVar(&footprintPath, "footprint", "", "path to .kicad_mod file")
	c.Flags().StringSliceVar(&excludePads, "exclude-pad", nil, "pad numbers to exclude from check (e.g. mechanical NPTH)")
	c.Flags().BoolVar(&asJSON, "json", false, "output result as JSON")
	return c
}

// readSpecInput reads from a file path, or stdin if path is "" or "-".
func readSpecInput(path string) ([]byte, error) {
	if path == "" || path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}
