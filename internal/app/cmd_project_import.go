package app

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// kicadProjectZip packs a KiCad project for EasyEDA's importer: the
// .kicad_pro with every .kicad_sch / .kicad_pcb / .kicad_dru / sym- and
// fp-lib-table next to it (no backups, no lock files). path is the
// project folder or its .kicad_pro.
func kicadProjectZip(path string) ([]byte, string, error) {
	dir := path
	if st, err := os.Stat(path); err != nil {
		return nil, "", err
	} else if !st.IsDir() {
		dir = filepath.Dir(path)
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, "", err
	}
	var names []string
	pro := ""
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, "~") || strings.HasPrefix(n, "_autosave") || strings.HasSuffix(n, ".lck") {
			continue
		}
		switch ext := strings.ToLower(filepath.Ext(n)); {
		case ext == ".kicad_pro":
			if pro != "" && !strings.HasSuffix(path, ".kicad_pro") {
				return nil, "", fmt.Errorf("%s has several .kicad_pro files; pass the one to import", dir)
			}
			if strings.HasSuffix(path, ".kicad_pro") && n != filepath.Base(path) {
				continue
			}
			pro = n
			names = append(names, n)
		case ext == ".kicad_sch", ext == ".kicad_pcb", ext == ".kicad_dru", n == "sym-lib-table", n == "fp-lib-table":
			names = append(names, n)
		}
	}
	if pro == "" {
		return nil, "", fmt.Errorf("no .kicad_pro in %s", dir)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		data, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, "", err
		}
		w, err := zw.Create(n)
		if err != nil {
			return nil, "", err
		}
		if _, err := w.Write(data); err != nil {
			return nil, "", err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), strings.TrimSuffix(pro, ".kicad_pro") + ".zip", nil
}

func newProjectImportCmd(cfg *appConfig, window *string, stdout, stderr io.Writer) *cobra.Command {
	var file, typ, name, team string
	c := &cobra.Command{
		Use:   "import",
		Short: "Import a KiCad (or other) project into EasyEDA as a NEW project",
		Long: `Bring a finished design into EasyEDA once: the design is done in KiCad
(pcbpilot kicad …), then imported here as a new project in the current team
through the official importer (footprints and 3D models associated). An
existing project is never modified.

--file is a KiCad project folder or its .kicad_pro (the .kicad_pro, .kicad_sch,
.kicad_pcb, .kicad_dru and library tables are zipped), or any file the
importer accepts for --type.`,
		Args: cobra.NoArgs,
		Example: `  pcbpilot project import --file kicad/GasV5_A.kicad_pro --name GasV5_A_kicad
  pcbpilot project import --file design.zip --type KiCad --name Demo`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if file == "" || name == "" {
				return fmt.Errorf("--file and --name are required")
			}
			var data []byte
			fileName := filepath.Base(file)
			st, err := os.Stat(file)
			if err != nil {
				return err
			}
			if typ == "KiCad" && (st.IsDir() || strings.EqualFold(filepath.Ext(file), ".kicad_pro")) {
				if data, fileName, err = kicadProjectZip(file); err != nil {
					return err
				}
			} else if data, err = os.ReadFile(file); err != nil {
				return err
			}
			if len(data) > 16<<20 {
				return fmt.Errorf("%s is %d bytes; the import limit is 16 MiB", fileName, len(data))
			}
			payload := map[string]any{"fileBase64": base64.StdEncoding.EncodeToString(data), "fileName": fileName, "fileType": typ, "name": name}
			if team != "" {
				payload["teamUuid"] = team
			}
			fmt.Fprintf(stderr, "importing %s (%d bytes, %s) as new project %q\n", fileName, len(data), typ, name)
			res, err := requestActionTimed(cfg, "project.import_file", *window, payload, 10*time.Minute)
			if err != nil {
				return err
			}
			return encodeResultEnvelope(res, res.Result, stdout)
		},
	}
	c.Flags().StringVar(&file, "file", "", "KiCad project folder / .kicad_pro, or a project file for --type (required)")
	c.Flags().StringVar(&typ, "type", "KiCad", "importer: KiCad, EasyEDA Pro, EasyEDA, JLCEDA Pro, JLCEDA, Allegro, OrCAD, EAGLE, PADS, LTspice")
	c.Flags().StringVar(&name, "name", "", "name of the new EasyEDA project (required)")
	c.Flags().StringVar(&team, "team", "", "owner team UUID (default: the current team)")
	return c
}
