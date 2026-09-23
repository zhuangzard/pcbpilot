package app

// pcb_netclass.go — net-role classification and the canonical net-class → track
// width ladder (规范线宽). This upgrades the old two-bucket power/signal split
// (isGlobalNet ? powerWidth : signalWidth) into role-aware widths so a 3V3 branch,
// a +5V trunk, a VBUS/VIN connector-input rail and a signal each get their own
// spec width, per the official-board benchmark §7.8 ladder
// (pcb-layout-conventions.md): signal / power-branch / power-trunk / high-current.
//
// Like defaultPcbRules(), the ladder lives inline here as the daemon's authoritative
// source of truth; .agents/skills/pcbpilot/references/fab-rules-jlcpcb.json carries a
// mirrored "netClasses" doc section for humans. The board's LIVE rules still seed
// the signal width and the legal-minimum floor — the ladder only adds the role
// steps ON TOP of what the live rule already gives.
//
// The classifier is a NAME/voltage heuristic. A circuit block CAN declare a per-net
// track_width_mil / net_class (internal/blocks/data/*.json signals map, the "sink to
// blocks" rule) but NOTHING consumes that declaration yet — wiring it in as the
// authoritative override is phase-2 work; until then this heuristic decides alone.

import (
	"math"
	"regexp"
	"strings"
)

// Net-class roles, ordered narrowest → widest. gnd is nominally the widest when
// routed, but a GND net normally belongs in a pour/plane, not a track (see
// power-not-poured / route-short --skip-power).
const (
	roleSignal      = "signal"       // ordinary logic/analog signal
	rolePowerBranch = "power-branch" // regulated downstream rail (3V3/1V8/VCC/VDD, < 5V)
	rolePowerTrunk  = "power-trunk"  // main rail (+5V-class, 5–9V)
	roleHighCurrent = "high-current" // connector-input / battery / bus rail (VBUS/VIN/VBAT/VSYS, ≥ 9V)
	roleGnd         = "gnd"          // ground — prefer pour/plane over a routed track
)

// netClassRolesByWidth is the display/iteration order (narrow → wide).
var netClassRolesByWidth = []string{roleSignal, rolePowerBranch, rolePowerTrunk, roleHighCurrent, roleGnd}

// railInputRe matches connector-input / battery / bus rails that carry the full
// board current regardless of their nominal voltage (a 5V VBUS from USB still feeds
// the whole board) → high-current.
var railInputRe = regexp.MustCompile(`(?i)^[+-]?(?:vbus|vin|vbat|vsys)\b`)

// railVoltageRe extracts a rail voltage encoded in the net name: "+5V"→5, "3V3"→3.3,
// "1V8"→1.8, "+12V"→12, "5V0"→5. The digits after the V are the fractional part.
var railVoltageRe = regexp.MustCompile(`(?i)(\d+)v(\d*)`)

// railVoltage parses a rail voltage (volts) from a net name; ok=false when the name
// carries no numeric voltage (e.g. VCC, VDD, VREF).
func railVoltage(net string) (float64, bool) {
	m := railVoltageRe.FindStringSubmatch(net)
	if m == nil {
		return 0, false
	}
	whole := m[1]
	v := 0.0
	for _, r := range whole {
		v = v*10 + float64(r-'0')
	}
	if frac := m[2]; frac != "" {
		// "3V3" → 3.3, "5V0" → 5.0 — treat the trailing digits as the decimal part.
		f := 0.0
		scale := 1.0
		for _, r := range frac {
			scale *= 10
			f = f*10 + float64(r-'0')
		}
		v += f / scale
	}
	return v, true
}

// netRole classifies a net into a spec-width role. It is the fallback heuristic used
// when no block declares an explicit width for the net; block data overrides it.
func netRole(net string) string {
	n := strings.TrimSpace(net)
	if n == "" {
		return roleSignal
	}
	if isGndNetName(n) {
		return roleGnd
	}
	if !isGlobalNet(n) {
		return roleSignal
	}
	// A power rail — split by input-rail name first, then by voltage.
	if railInputRe.MatchString(n) {
		return roleHighCurrent
	}
	if v, ok := railVoltage(n); ok {
		switch {
		case v >= 9:
			return roleHighCurrent
		case v >= 5:
			return rolePowerTrunk
		default:
			return rolePowerBranch // 3V3 / 1V8 / 1V2 …
		}
	}
	// Voltage-less power name (VCC/VDD/VREF/VOUT …) → treat as a regulated branch.
	return rolePowerBranch
}

// metricStepMM is the metric width grid of 规范手册 §1.2 (pcb-design-rules.md):
// track widths are METRIC-round values in 0.05mm steps (0.15/0.2/0.25/0.3/0.4/
// 0.5mm …), never mil fragments (10mil = 0.254mm is a fragment; 0.25mm is not).
const metricStepMM = 0.05

// roundMetricMil snaps a width (mil) to the nearest 0.05mm multiple, expressed in
// mil (规范 §1.2 metric rounding). Guard rail: nearest-rounding can shave up to
// half a step (0.025mm ≈ 0.98mil) off the input; if that loses more than 10% of
// the input width (only possible below ~9.8mil) the value steps UP to the next
// 0.05mm multiple instead, so a spec width never degrades materially. The fab
// clamp floor is re-checked by the caller AFTER rounding (netClassWidthTable) —
// a rounded value may not sit below the legal minimum.
func roundMetricMil(w float64) float64 {
	if w <= 0 {
		return w
	}
	mm := w / mmToMil
	steps := math.Round(mm / metricStepMM)
	r := steps * metricStepMM * mmToMil
	if r < w*0.9 { // nearest rounding lost >10% — take the next 0.05mm step up
		r = (steps + 1) * metricStepMM * mmToMil
	}
	return r
}

// netClassWidthTable returns the canonical role→width (mil) ladder, seeded from the
// board's live rules. Signal tracks the board's live default; each power role steps
// up per the §7.8 role split, with rung values on the metric grid of 规范手册 §1.2:
// branch 0.25mm (9.84mil) / trunk 0.4mm (15.75mil) / high-current 0.5mm (19.69mil)
// — replacing the old mil-fragment bases 10/15/20 (= 0.254/0.381/0.508mm, all
// off-grid). Every width is floored at the fab's legal minimum.
//
// Metric-rounding decisions (do not "fix" these without re-reading §1.2):
//   - branch stays 0.25mm (9.84mil) even though 9.84 < the old 10mil base.
//     Rounding UP to 0.3mm (11.81mil) would make width-under-spec retro-flag
//     every existing board routed at the old 10mil branch (10 < 11.81−1mil tol);
//     with 0.25mm the whole legacy 10/15/20 ladder stays ≥ spec−pcbWidthTolMil
//     (10 ≥ 9.84−1, 15 ≥ 15.75−1, 20 ≥ 19.69−1) — zero false WARNs.
//   - signal is NOT metric-rounded: it is the board's LIVE rule value (the
//     user's own setting), which we report, never rewrite.
//   - monotonicity: the power rungs (branch ≤ trunk ≤ high-current) are kept
//     STRICTLY monotonic. The signal→branch edge is monotonic up to metric
//     quantization: snapping a live width onto the 0.05mm grid can dip at most
//     half a step (≈0.98mil) below the raw live value — under pcbWidthTolMil,
//     so width-under-spec's tolerance absorbs it (e.g. live signal 10mil,
//     branch 9.84mil is a grid artifact, not an inversion).
func netClassWidthTable(r pcbRules) map[string]float64 {
	sig := r.clampWidth(r.trackWidthMil)
	// §1.2 recommended metric bases for the power rungs.
	const (
		branchBaseMM = 0.25 // 分支电源 — 9.84mil
		trunkBaseMM  = 0.40 // 主干电源 — 15.75mil
		highBaseMM   = 0.50 // 大电流 — 19.69mil
	)
	// rung folds one ladder step: at least the §1.2 metric base, at least the
	// previous rung (strict power-rung monotonicity), metric-rounded, and never
	// below the fab's legal minimum — nearest-rounding can dip under the clamp
	// floor by less than one step, so a single 0.05mm step up always clears it.
	rung := func(baseMM, prev float64) float64 {
		w := roundMetricMil(r.clampWidth(math.Max(baseMM*mmToMil, prev)))
		if w < r.trackWidthMinMil {
			w += metricStepMM * mmToMil
		}
		return w
	}
	branch := rung(branchBaseMM, sig)
	trunk := rung(trunkBaseMM, branch)
	// High-current also honors the board's power width (default 20mil → rounds
	// onto the grid at 0.5mm), never below trunk.
	high := rung(highBaseMM, math.Max(r.powerWidthMil, trunk))
	return map[string]float64{
		roleSignal:      sig,
		rolePowerBranch: branch,
		rolePowerTrunk:  trunk,
		roleHighCurrent: high,
		roleGnd:         high, // if ever routed; normally poured
	}
}
