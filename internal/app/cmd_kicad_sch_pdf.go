package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"github.com/zhuangzard/pcbpilot/internal/kicad"
)

// schPdfResult is what kicad sch-pdf prints.
type schPdfResult struct {
	OK    bool   `json:"ok"`
	Root  string `json:"root"`
	Out   string `json:"out"`
	Pages int    `json:"pages"` // pages in the PDF (0 = not counted)
	Bytes int64  `json:"bytes"`
	// Sheets is the hierarchy in print order (the root first): a kicad
	// sch-build project prints every schematic page followed by its BOM sheet.
	Sheets []string `json:"sheets"`
}

var schPdfPageRE = regexp.MustCompile(`/Type\s*/Page[^s]`)

// schPdfRoot finds the root sheet: --sch, else the project directory's
// sch-build state, else its only .kicad_pro.
func schPdfRoot(project, sch string) (string, error) {
	if sch != "" {
		return kicad.RootSheetFor(sch)
	}
	if project == "" {
		return "", fmt.Errorf("give --project DIR or --sch ROOT.kicad_sch")
	}
	dir, err := filepath.Abs(project)
	if err != nil {
		return "", err
	}
	if st, _ := loadSbState(dir); st != nil && st.Name != "" && fileExists(filepath.Join(dir, st.Name+".kicad_sch")) {
		return filepath.Join(dir, st.Name+".kicad_sch"), nil
	}
	pros, _ := filepath.Glob(filepath.Join(dir, "*.kicad_pro"))
	if len(pros) != 1 {
		return "", fmt.Errorf("%s: want exactly one .kicad_pro (or give --sch)", dir)
	}
	root := strings.TrimSuffix(pros[0], ".kicad_pro") + ".kicad_sch"
	if !fileExists(root) {
		return "", fmt.Errorf("%s: no root schematic %s", dir, filepath.Base(root))
	}
	return root, nil
}

// runSchPdf plots the whole hierarchy of root into one PDF with kicad-cli.
func runSchPdf(root, out string) (*schPdfResult, error) {
	// the output replaces whatever has its name: never a project file
	if !strings.EqualFold(filepath.Ext(out), ".pdf") {
		return nil, fmt.Errorf("--out must be a .pdf file, got %s", out)
	}
	cli, err := kicad.KicadCLI()
	if err != nil {
		return nil, err
	}
	out, err = filepath.Abs(out)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return nil, err
	}
	// plotted aside: a failed plot leaves the previous PDF alone
	tmp, err := os.MkdirTemp(filepath.Dir(out), ".sch-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	part := filepath.Join(tmp, "out.pdf")
	var buf bytes.Buffer
	cmd, cancel := kicad.SchCLICommand(cli, "sch", "export", "pdf", "-o", part, root)
	defer cancel()
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("kicad-cli sch export pdf: %s", kicad.SchCLIFail(cmd, err, buf.String()))
	}
	data, err := os.ReadFile(part)
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("kicad-cli wrote no PDF: %s", strings.TrimSpace(buf.String()))
	}
	if err := os.Remove(out); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.Rename(part, out); err != nil {
		return nil, err
	}
	res := &schPdfResult{OK: true, Root: root, Out: out, Bytes: int64(len(data)), Pages: len(schPdfPageRE.FindAll(data, -1)), Sheets: []string{}}
	if files, err := kicad.SheetFiles(root); err == nil {
		for _, f := range files {
			res.Sheets = append(res.Sheets, filepath.Base(f))
		}
	}
	return res, nil
}

func newKicadSchPdfCmd(stdout io.Writer) *cobra.Command {
	var project, sch, out string
	c := &cobra.Command{
		Use:   "sch-pdf",
		Short: "One PDF of the whole schematic (kicad-cli sch export pdf on the root sheet): every page, each followed by its BOM sheet",
		Long: `Plots the root sheet and every hierarchical sheet under it into one PDF, in page
order. A kicad sch-build project carries a BOM sheet after every schematic page
(<page>-bom.kicad_sch), so the PDF reads: page, its BOM, next page, its BOM, …

The schematic is not changed. --project DIR takes the root from the sch-build state
(else the directory's only .kicad_pro); --sch names a sheet of the design directly.`,
		Example: `  pcbpilot kicad sch-pdf --project build/ --out build/schematic.pdf
  pcbpilot kicad sch-pdf --sch board.kicad_sch --out board.pdf`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := schPdfRoot(project, sch)
			if err != nil {
				return err
			}
			if out == "" {
				out = strings.TrimSuffix(root, ".kicad_sch") + ".pdf"
			}
			res, err := runSchPdf(root, out)
			if err != nil {
				return err
			}
			return writeJSON(stdout, res)
		},
	}
	c.Flags().StringVar(&project, "project", "", "project directory (a kicad sch-build --out, or any directory with one .kicad_pro)")
	c.Flags().StringVar(&sch, "sch", "", "a .kicad_sch of the design (its root sheet is found next to it) instead of --project")
	c.Flags().StringVar(&out, "out", "", "PDF to write (default: <root>.pdf next to the root sheet)")
	return c
}
