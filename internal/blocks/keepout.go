package blocks

import "encoding/json"

// AntennaKeepout is a block's declarative RF/antenna keep-out spec (the top-level
// `keepout` object with role="antenna"). The circuit-block library is the single
// source-of-truth for the per-part keep-out DEPTH; the generator
// (`pcbpilot pcb antenna-keepout`) matches a placed part to this by device name and
// sizes the all-layer no-copper region at the module's pad-free (antenna) end.
type AntennaKeepout struct {
	Role    string  `json:"role"`     // "antenna"
	Match   string  `json:"match"`    // device-name substring (e.g. "wroom") this applies to
	EndFrac float64 `json:"end_frac"` // keep-out depth as a fraction of the module's long axis
	Layers  string  `json:"layers"`   // "all" (only value honored today)
	Reason  string  `json:"reason"`
}

// LoadAntennaKeepouts collects every block's declared antenna keep-out. A block
// without one, or a malformed block, is skipped (best-effort, never fatal).
func LoadAntennaKeepouts() ([]AntennaKeepout, error) {
	all, err := Load()
	if err != nil {
		return nil, err
	}
	var out []AntennaKeepout
	for _, b := range all {
		var raw struct {
			Keepout *AntennaKeepout `json:"keepout"`
		}
		if json.Unmarshal(b.Raw, &raw) != nil || raw.Keepout == nil {
			continue
		}
		if raw.Keepout.Match == "" {
			continue
		}
		out = append(out, *raw.Keepout)
	}
	return out, nil
}
