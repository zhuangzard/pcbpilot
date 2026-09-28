package analogsim

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// IEC 60063 preferred-number series (one decade, mantissa ×100).
var (
	e6  = []float64{100, 150, 220, 330, 470, 680}
	e12 = []float64{100, 120, 150, 180, 220, 270, 330, 390, 470, 560, 680, 820}
	e24 = []float64{100, 110, 120, 130, 150, 160, 180, 200, 220, 240, 270, 300, 330, 360, 390, 430, 470, 510, 560, 620, 680, 750, 820, 910}
	e96 = []float64{100, 102, 105, 107, 110, 113, 115, 118, 121, 124, 127, 130, 133, 137, 140, 143, 147, 150, 154, 158, 162, 165, 169, 174,
		178, 182, 187, 191, 196, 200, 205, 210, 215, 221, 226, 232, 237, 243, 249, 255, 261, 267, 274, 280, 287, 294, 301, 309, 316, 324,
		332, 340, 348, 357, 365, 374, 383, 392, 402, 412, 422, 432, 442, 453, 464, 475, 487, 499, 511, 523, 536, 549, 562, 576, 590, 604,
		619, 634, 649, 665, 681, 698, 715, 732, 750, 768, 787, 806, 825, 845, 866, 887, 909, 931, 953, 976}
)

// SeriesMantissas returns the mantissas of an E-series name (E6, E12, E24, E96).
func SeriesMantissas(name string) []float64 {
	switch strings.ToUpper(name) {
	case "E6":
		return e6
	case "E12":
		return e12
	case "E24":
		return e24
	case "E96":
		return e96
	}
	return nil
}

// SeriesValues lists every value of the series within [lo, hi].
func SeriesValues(name string, lo, hi float64) []float64 {
	man := SeriesMantissas(name)
	if man == nil || lo <= 0 || hi < lo {
		return nil
	}
	var out []float64
	for dec := math.Floor(math.Log10(lo)) - 2; dec <= math.Ceil(math.Log10(hi)); dec++ {
		for _, m := range man {
			v := cleanFloat(m / 100 * math.Pow(10, dec))
			if v >= lo*(1-1e-9) && v <= hi*(1+1e-9) {
				out = append(out, v)
			}
		}
	}
	sort.Float64s(out)
	return dedupe(out)
}

// Snap returns the series value closest to v on a log scale.
func Snap(v float64, name string) float64 {
	if v <= 0 {
		return v
	}
	cands := SeriesValues(name, v/1.2, v*1.2)
	best, bestErr := v, math.Inf(1)
	for _, c := range cands {
		if e := math.Abs(math.Log(c / v)); e < bestErr {
			best, bestErr = c, e
		}
	}
	return best
}

// InSeries reports whether v is (within 0.5 %) a member of the series.
func InSeries(v float64, name string) bool {
	if v <= 0 {
		return false
	}
	s := Snap(v, name)
	return math.Abs(s-v)/v < 0.005
}

// Neighbours returns up to k series values either side of v (v's snap included).
func Neighbours(v float64, name string, k int) []float64 {
	if v <= 0 {
		return nil
	}
	all := SeriesValues(name, v/math.Pow(10, 0.5+float64(k)/10), v*math.Pow(10, 0.5+float64(k)/10))
	s := Snap(v, name)
	idx := 0
	for i, x := range all {
		if math.Abs(x-s) < 1e-12*math.Max(1, s) || x == s {
			idx = i
		}
	}
	lo, hi := idx-k, idx+k
	if lo < 0 {
		lo = 0
	}
	if hi >= len(all) {
		hi = len(all) - 1
	}
	return append([]float64(nil), all[lo:hi+1]...)
}

// SeriesOf names the coarsest series v belongs to ("" = none).
func SeriesOf(v float64) string {
	for _, s := range []string{"E6", "E12", "E24", "E96"} {
		if InSeries(v, s) {
			return s
		}
	}
	return ""
}

func dedupe(xs []float64) []float64 {
	var out []float64
	for _, x := range xs {
		if len(out) == 0 || math.Abs(out[len(out)-1]-x) > 1e-12*math.Abs(x) {
			out = append(out, x)
		}
	}
	return out
}

// cleanFloat rounds away binary noise (4.7000000001e-9 → 4.7e-9).
func cleanFloat(v float64) float64 {
	if v == 0 {
		return 0
	}
	f, _ := strconv.ParseFloat(strconv.FormatFloat(v, 'g', 10, 64), 64)
	return f
}

// FormatValue renders a component value the EasyEDA way: 11.3kΩ, 10nF, 1µF, 2.2µH.
func FormatValue(v float64, kind string) string {
	unit := map[string]string{"R": "Ω", "C": "F", "L": "H"}[kind]
	return FormatSI(v) + unit
}

// FormatSI renders v with an SI prefix and up to 4 significant digits.
func FormatSI(v float64) string {
	if v == 0 {
		return "0"
	}
	prefixes := []struct {
		p string
		m float64
	}{{"G", 1e9}, {"M", 1e6}, {"k", 1e3}, {"", 1}, {"m", 1e-3}, {"µ", 1e-6}, {"n", 1e-9}, {"p", 1e-12}, {"f", 1e-15}}
	a := math.Abs(v)
	for _, p := range prefixes {
		if a >= p.m*0.9995 {
			s := strconv.FormatFloat(roundSig(v/p.m, 4), 'f', -1, 64)
			return s + p.p
		}
	}
	return strconv.FormatFloat(roundSig(v/1e-15, 3), 'f', -1, 64) + "f"
}

func roundSig(v float64, n int) float64 {
	if v == 0 {
		return 0
	}
	p := math.Pow(10, float64(n)-math.Ceil(math.Log10(math.Abs(v))))
	return math.Round(v*p) / p
}

// fmtHz renders a frequency.
func fmtHz(v float64) string { return FormatSI(v) + "Hz" }

func fmtPct(v float64) string { return fmt.Sprintf("%.1f %%", v) }
