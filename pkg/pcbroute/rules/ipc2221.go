package rules

import "math"

// IPC-2221 conductor sizing, I = k·ΔT^0.44·A^0.725 with A in mil² (04 §4.6).
const (
	ipcKOuter = 0.048
	ipcKInner = 0.024
	nmPerMil  = 25_400.0
	nmPerUm   = 1_000
)

// DefaultTempRiseC is the conductor temperature rise when the intent gives none.
const DefaultTempRiseC = 10.0

// DefaultViaPlatingNM is the via barrel plating thickness of 04 §3.7: 25 µm.
const DefaultViaPlatingNM int64 = 25_000

// IntentWidth is the IPC-2221 width (nm, rounded up to 1 µm) that carries
// currentA with a rise of tempRiseC on copper copperUm thick. Outer layers
// use k = 0.048, inner layers k = 0.024. It is 0 for no current or copper.
func IntentWidth(currentA, tempRiseC, copperUm float64, outer bool) int64 {
	if currentA <= 0 || copperUm <= 0 {
		return 0
	}
	if tempRiseC <= 0 {
		tempRiseC = DefaultTempRiseC
	}
	k := ipcKInner
	if outer {
		k = ipcKOuter
	}
	area := math.Pow(currentA/(k*math.Pow(tempRiseC, 0.44)), 1/0.725) // mil²
	w := area / (copperUm * nmPerUm / nmPerMil)                       // mil
	return int64(math.Ceil(w*nmPerMil/nmPerUm)) * nmPerUm
}

// ViaAmpacity is the current (A) one through via carries: its barrel is a
// conductor of cross-section π·(drill + t)·t under the inner-layer formula
// (04 §3.7). platingNM 0 means DefaultViaPlatingNM.
func ViaAmpacity(drillNM, platingNM int64, tempRiseC float64) float64 {
	if platingNM <= 0 {
		platingNM = DefaultViaPlatingNM
	}
	if tempRiseC <= 0 {
		tempRiseC = DefaultTempRiseC
	}
	d, t := float64(drillNM)/nmPerMil, float64(platingNM)/nmPerMil
	area := math.Pi * (d + t) * t
	return ipcKInner * math.Pow(tempRiseC, 0.44) * math.Pow(area, 0.725)
}

// spacingRow is one voltage band of IPC-2221 Table 6-1 (mm): B1 internal
// conductors, B2 external uncoated, B4 external with permanent coating, sea
// level to 3050 m. The values are the table pcbauto.ClearanceForVoltage
// already ships.
type spacingRow struct{ maxV, b1, b2, b4 float64 }

var spacingTable = []spacingRow{
	{15, 0.05, 0.1, 0.05}, {30, 0.05, 0.1, 0.05}, {50, 0.1, 0.6, 0.13},
	{100, 0.1, 0.6, 0.13}, {150, 0.2, 0.6, 0.4}, {170, 0.2, 1.25, 0.4},
	{250, 0.2, 1.25, 0.4}, {300, 0.2, 1.25, 0.4}, {500, 0.25, 2.5, 0.8},
}

// Per-volt increments above 500 V (mm/V) for B1, B2 and B4.
const (
	perVoltB1 = 0.0025
	perVoltB2 = 0.005
	perVoltB4 = 0.00305
)

// VoltageClearance is the Table 6-1 spacing (nm) for a voltage between two
// conductors: B1 on inner layers, B2 on outer layers, B4 when coated.
func VoltageClearance(volts float64, outer, coated bool) int64 {
	pick := func(r spacingRow) (float64, float64) {
		switch {
		case !outer:
			return r.b1, perVoltB1
		case coated:
			return r.b4, perVoltB4
		}
		return r.b2, perVoltB2
	}
	v := math.Abs(volts)
	for _, r := range spacingTable {
		if v <= r.maxV {
			mm, _ := pick(r)
			return int64(math.Round(mm * 1e6))
		}
	}
	mm, per := pick(spacingTable[len(spacingTable)-1])
	return int64(math.Round((mm + (v-500)*per) * 1e6))
}
