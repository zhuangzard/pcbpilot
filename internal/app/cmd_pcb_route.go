package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// No appConfig is accepted. These commands cannot dispatch an editor action.
func newPcbRouteCmd(stdout, stderr io.Writer) *cobra.Command {
	group := &cobra.Command{Use: "route", Short: "Offline single-layer path solving and independent checking"}
	for _, mode := range []string{"solve", "check"} {
		var boardPath, fromPath, planPath, outPath string
		cmd := &cobra.Command{
			Use: mode, Args: cobra.NoArgs,
			Short: map[string]string{"solve": "Find a bounded straight/45-degree path between measured pads", "check": "Recheck a candidate against independent inputs without solving"}[mode],
			Long: `Use a complete pcb dump --include-copper snapshot and a schemaVersion 1
request (mil, y-UP): net, layer, widthMil, from/to as designator.pad,
stepMil, maxDetourMil, maxStates, units. TOP/BOTTOM single-layer, zero new vias.
The shared Go package performs bounded direction-aware search; exact measured
geometry supplies clearance. Missing geometry and exhausted searches remain
incomplete. This is an offline endpoint-pair proof, not whole-board autorouting
or native DRC. No daemon, connector, editor writes or additional CLI is needed.
Reports are written even for rejected candidates and incomplete searches; only
pass exits zero. A same-name SVG preview is always rendered beside the JSON.
Check verifies provenance and geometry without rerunning solve.`,
			Example: `  pcbpilot pcb route solve --board board.json --from request.json --out plan.json
  pcbpilot pcb route check --board board.json --from request.json --plan plan.json --out check.json`,
			RunE: func(cmd *cobra.Command, _ []string) error {
				if boardPath == "" || fromPath == "" || outPath == "" || mode == "check" && planPath == "" {
					return fmt.Errorf("--board, --from, --out and (for check) --plan are required")
				}
				inputs := []string{boardPath, fromPath}
				if mode == "check" {
					inputs = append(inputs, planPath)
				}
				previewPath := pcbRoutePreviewPath(outPath)
				if previewPath == outPath {
					return fmt.Errorf("--out must name a JSON report, not an SVG preview")
				}
				if err := pcbRouteOutputSafe(outPath, inputs); err != nil {
					return err
				}
				if err := pcbRouteOutputSafe(previewPath, append(inputs, outPath)); err != nil {
					return err
				}
				boardRaw, err := os.ReadFile(boardPath)
				if err != nil {
					return err
				}
				requestRaw, err := os.ReadFile(fromPath)
				if err != nil {
					return err
				}
				var in pcbRouteRequest
				if err := decodePCBRouteJSON(requestRaw, &in, true); err != nil {
					return fmt.Errorf("request: %w", err)
				}
				// Board snapshots are a shared evolving schema. Preserve compatibility
				// with extra fields, but reject trailing JSON and invalid framing.
				var snap boardSnapshot
				if err := decodePCBRouteJSON(boardRaw, &snap, false); err != nil {
					return fmt.Errorf("board: %w", err)
				}
				snap.sanitizeOutline()
				report := newPCBRouteReport(boardRaw, requestRaw)
				report.Preview = previewPath
				var resultErr error
				if mode == "solve" {
					resultErr = solvePCBRoute(cmd.Context(), in, &snap, &report)
				} else {
					planRaw, err := os.ReadFile(planPath)
					if err != nil {
						return err
					}
					var plan pcbRouteReport
					if err := decodePCBRouteJSON(planRaw, &plan, true); err != nil {
						return fmt.Errorf("plan: %w", err)
					}
					report.PlanSHA256 = sha256String(planRaw)
					resultErr = checkPCBRoute(cmd.Context(), in, &snap, plan, &report)
				}
				blob, err := json.MarshalIndent(report, "", "  ")
				if err != nil {
					return err
				}
				if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
					return err
				}
				var preview bytes.Buffer
				if err := renderPCBRouteSVG(&preview, &snap, in, report); err != nil {
					return fmt.Errorf("render route preview: %w", err)
				}
				if err := writeAtomic(previewPath, preview.Bytes()); err != nil {
					return err
				}
				if err := writeAtomic(outPath, append(blob, '\n')); err != nil {
					return err
				}
				fmt.Fprintf(stderr, "route %s: %s → %s + %s\n", mode, report.Status, outPath, previewPath)
				fmt.Fprintln(stdout, outPath)
				return resultErr
			},
		}
		cmd.Flags().StringVar(&boardPath, "board", "", "complete measured JSON from pcb dump --include-copper")
		cmd.Flags().StringVar(&fromPath, "from", "", "schemaVersion 1 route request JSON (mil, y-UP)")
		cmd.Flags().StringVar(&outPath, "out", "", "output JSON report; a same-name .svg preview is also written")
		if mode == "check" {
			cmd.Flags().StringVar(&planPath, "plan", "", "route solve report to verify against independent inputs")
		}
		group.AddCommand(cmd)
	}
	return group
}

func pcbRoutePreviewPath(out string) string {
	ext := filepath.Ext(out)
	if strings.EqualFold(ext, ".svg") {
		return out
	}
	if ext == "" {
		return out + ".svg"
	}
	return strings.TrimSuffix(out, ext) + ".svg"
}

func decodePCBRouteJSON(raw []byte, target any, strict bool) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	return nil
}

func pcbRouteOutputSafe(out string, inputs []string) error {
	dest, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	destInfo, destErr := os.Stat(out)
	for _, input := range inputs {
		source, err := filepath.Abs(input)
		if err != nil {
			return err
		}
		info, statErr := os.Stat(input)
		if dest == source || destErr == nil && statErr == nil && os.SameFile(destInfo, info) {
			return fmt.Errorf("output must not overwrite input: %s", input)
		}
	}
	return nil
}
