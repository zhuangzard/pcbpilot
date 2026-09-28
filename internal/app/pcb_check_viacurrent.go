package app

import (
	"os"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// addViaCurrentFindings runs the via-current rule (pcbauto.CheckViaCurrent)
// on a board snapshot with copper: every layer transition of a power net is
// rated — Σ via ampacity against the current it passes (intent pin / net
// current, bounded by the attached track) — ERROR below the current, WARN
// under the required margin (intent copper.viaMarginPct, default 20 %).
func addViaCurrentFindings(rep *pcbCheckReport, dump []byte, intentPath string) error {
	raw, err := os.ReadFile(intentPath)
	if err != nil {
		return err
	}
	in, err := pcbauto.ParseIntent(raw)
	if err != nil {
		return err
	}
	chk, err := pcbauto.CheckViaCurrentSnapshot(dump, in)
	if err != nil {
		return err
	}
	rep.Limitations = append(rep.Limitations, chk.Notes...)
	for _, f := range chk.Findings {
		rep.Findings = append(rep.Findings, pcbCheckFinding{Type: "via-current", Level: f.Level, Net: f.Net, Nets: []string{f.Net},
			Primitives: f.Primitives, At: &pcbXY{X: f.At.X, Y: f.At.Y}, Message: f.Message + docRule("2.4", "过孔载流")})
		rep.Summary.ViaCurr++
		if f.Level == "ERROR" {
			rep.Summary.Errors++
		}
		rep.Summary.Warnings++
		rep.Summary.Total++
	}
	rep.Passed = rep.Summary.Total == 0
	return nil
}
