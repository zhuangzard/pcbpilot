package app

import (
	"fmt"
	"os"

	"github.com/zhuangzard/pcbpilot/pkg/pcbauto"
)

// addIsolationFindings runs the intent isolation rule (clearance on every
// shared layer, creepage along the outer surface with milled slots credited)
// over a board dump and appends its findings as ERRORs.
func addIsolationFindings(rep *pcbCheckReport, dump []byte, intentPath string) error {
	raw, err := os.ReadFile(intentPath)
	if err != nil {
		return err
	}
	in, err := pcbauto.ParseIntent(raw)
	if err != nil {
		return err
	}
	chk, err := pcbauto.CheckIsolationSnapshot(dump, in)
	if err != nil {
		return err
	}
	if len(chk.Pairs) == 0 {
		rep.Limitations = append(rep.Limitations, "isolation: the intent declares no insulation pair — nothing to check")
	}
	for _, p := range chk.Pairs {
		rep.Limitations = append(rep.Limitations, fmt.Sprintf("isolation %s|%s (%s, %s): clearance %.2f mm, creepage %.2f mm — %s",
			p.A, p.B, chk.Standard, p.Insulation, p.ClearanceMil*0.0254, p.CreepageMil*0.0254, "engineering reference; confirm with the certification lab"))
	}
	rep.Limitations = append(rep.Limitations, chk.Notes...)
	for _, f := range chk.Findings {
		rep.Findings = append(rep.Findings, pcbCheckFinding{Type: f.Kind, Level: "ERROR", Nets: []string{f.NetA, f.NetB}, Layer: f.Layer,
			Primitives: []string{f.ItemA, f.ItemB}, Message: f.Message, At: &pcbXY{X: f.At.X, Y: f.At.Y}})
		rep.Summary.Isolation++
		rep.Summary.Errors++
		rep.Summary.Warnings++
		rep.Summary.Total++
	}
	rep.Passed = rep.Summary.Total == 0
	return nil
}
