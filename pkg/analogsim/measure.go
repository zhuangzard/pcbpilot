package analogsim

import (
	"math"
	"sort"
)

// bode is a complex frequency response.
type bode struct {
	f     []float64
	mag   []float64 // |H|
	db    []float64
	phase []float64 // degrees, unwrapped
}

// bodeFrom builds a response from an AC wrdata table (freq, re, im [, re2, im2]).
// With two vectors the ratio -v1/v2 is returned (loop gain).
func bodeFrom(t *table, ratio bool) *bode {
	b := &bode{}
	prev := math.NaN()
	offset := 0.0
	for _, r := range t.rows {
		if len(r) < 3 {
			continue
		}
		re, im := r[1], r[2]
		if ratio {
			if len(r) < 5 {
				continue
			}
			// T = -(a)/(b)
			ar, ai, br, bi := r[1], r[2], r[3], r[4]
			den := br*br + bi*bi
			if den == 0 {
				continue
			}
			re = -(ar*br + ai*bi) / den
			im = -(ai*br - ar*bi) / den
		}
		m := math.Hypot(re, im)
		ph := math.Atan2(im, re) * 180 / math.Pi
		if !math.IsNaN(prev) {
			for ph+offset-prev > 180 {
				offset -= 360
			}
			for ph+offset-prev < -180 {
				offset += 360
			}
		}
		ph += offset
		prev = ph
		b.f = append(b.f, r[0])
		b.mag = append(b.mag, m)
		b.db = append(b.db, 20*math.Log10(math.Max(m, 1e-30)))
		b.phase = append(b.phase, ph)
	}
	return b
}

// logInterp finds x where y crosses target between samples i-1 and i (log x).
func logInterp(x0, x1, y0, y1, target float64) float64 {
	if y1 == y0 {
		return x1
	}
	t := (target - y0) / (y1 - y0)
	return math.Exp(math.Log(x0) + t*(math.Log(x1)-math.Log(x0)))
}

func linInterp(x0, x1, y0, y1, target float64) float64 {
	if y1 == y0 {
		return x1
	}
	return x0 + (target-y0)/(y1-y0)*(x1-x0)
}

// acMetrics extracts gain, -3 dB frequency, f0 and Q from a response.
// resp: lp | hp | amp | bp.
func acMetrics(b *bode, resp string, second bool) map[string]float64 {
	out := map[string]float64{}
	n := len(b.f)
	if n < 3 {
		return out
	}
	ref, refPh := b.mag[0], b.phase[0]
	switch resp {
	case "hp":
		ref, refPh = 0, 0
		// passband: the maximum of the top decade, else the last point
		for i := n - 1; i >= 0 && b.f[i] >= b.f[n-1]/10; i-- {
			if b.mag[i] > ref {
				ref = b.mag[i]
			}
		}
		refPh = b.phase[n-1]
	}
	out["gain"] = ref
	out["gainDB"] = 20 * math.Log10(math.Max(ref, 1e-30))
	target := ref / math.Sqrt2
	switch resp {
	case "hp":
		for i := n - 1; i > 0; i-- {
			if b.mag[i] >= target && b.mag[i-1] < target {
				out["fcHz"] = logInterp(b.f[i-1], b.f[i], b.db[i-1], b.db[i], 20*math.Log10(target))
				break
			}
		}
	default:
		for i := 1; i < n; i++ {
			if b.mag[i-1] >= target && b.mag[i] < target {
				out["fcHz"] = logInterp(b.f[i-1], b.f[i], b.db[i-1], b.db[i], 20*math.Log10(target))
				break
			}
		}
	}
	peak := 0.0
	for i := range b.mag {
		if b.mag[i] > peak {
			peak = b.mag[i]
		}
	}
	if ref > 0 {
		out["peakingDB"] = math.Max(0, 20*math.Log10(peak/ref))
	}
	if second {
		// f0: phase 90° away from the passband phase; Q = |H(f0)| / |H(passband)|.
		want := refPh - 90
		if resp == "hp" {
			want = refPh + 90
		}
		for i := 1; i < n; i++ {
			a, c := b.phase[i-1], b.phase[i]
			if resp == "hp" {
				// scanning up: phase falls from +180 towards 0
				if (a-want)*(c-want) <= 0 {
					f0 := logInterp(b.f[i-1], b.f[i], a, c, want)
					m := math.Exp(math.Log(b.mag[i-1]) + (math.Log(f0)-math.Log(b.f[i-1]))/(math.Log(b.f[i])-math.Log(b.f[i-1]))*(math.Log(b.mag[i])-math.Log(b.mag[i-1])))
					out["f0Hz"], out["q"] = f0, m/ref
					break
				}
			} else if (a-want)*(c-want) <= 0 {
				f0 := logInterp(b.f[i-1], b.f[i], a, c, want)
				m := math.Exp(math.Log(b.mag[i-1]) + (math.Log(f0)-math.Log(b.f[i-1]))/(math.Log(b.f[i])-math.Log(b.f[i-1]))*(math.Log(b.mag[i])-math.Log(b.mag[i-1])))
				out["f0Hz"], out["q"] = f0, m/ref
				break
			}
		}
	}
	return out
}

// loopMetrics: crossover, phase margin and gain margin of a loop gain T(f).
func loopMetrics(t *bode) map[string]float64 {
	out := map[string]float64{}
	n := len(t.f)
	for i := 1; i < n; i++ {
		if t.mag[i-1] >= 1 && t.mag[i] < 1 {
			fc := logInterp(t.f[i-1], t.f[i], t.db[i-1], t.db[i], 0)
			ph := t.phase[i-1] + (math.Log(fc)-math.Log(t.f[i-1]))/(math.Log(t.f[i])-math.Log(t.f[i-1]))*(t.phase[i]-t.phase[i-1])
			// T(0) is positive real (negative feedback): margin to -180°.
			rel := ph - 360*math.Round(t.phase[0]/360)
			out["crossoverHz"], out["phaseMarginDeg"] = fc, 180+rel
			break
		}
	}
	for i := 1; i < n; i++ {
		off := 360 * math.Round(t.phase[0]/360)
		a, b := t.phase[i-1]-off, t.phase[i]-off
		if a > -180 && b <= -180 {
			f := logInterp(t.f[i-1], t.f[i], a, b, -180)
			db := t.db[i-1] + (math.Log(f)-math.Log(t.f[i-1]))/(math.Log(t.f[i])-math.Log(t.f[i-1]))*(t.db[i]-t.db[i-1])
			out["gainMarginDB"] = -db
			break
		}
	}
	if len(t.mag) > 0 {
		out["loopGainDcDB"] = t.db[0]
	}
	return out
}

// stepMetrics: overshoot, 10–90 % rise, 2 % settling of a step starting at t0.
func stepMetrics(t, y []float64, t0 float64) map[string]float64 {
	out := map[string]float64{}
	if len(t) < 10 {
		return out
	}
	i0 := sort.SearchFloat64s(t, t0)
	if i0 >= len(t)-2 {
		return out
	}
	y0 := y[0]
	if i0 > 0 {
		y0 = y[i0-1]
	}
	yf := y[len(y)-1]
	dy := yf - y0
	if math.Abs(dy) < 1e-9 {
		return out
	}
	ext := y0
	for i := i0; i < len(y); i++ {
		if (y[i]-ext)*dy > 0 {
			ext = y[i]
		}
	}
	out["overshootPct"] = math.Max(0, (ext-yf)/dy*100)
	out["finalV"] = yf
	var t10, t90 float64
	for i := i0 + 1; i < len(y); i++ {
		f0, f1 := (y[i-1]-y0)/dy, (y[i]-y0)/dy
		if t10 == 0 && f0 < 0.1 && f1 >= 0.1 {
			t10 = linInterp(t[i-1], t[i], f0, f1, 0.1)
		}
		if t90 == 0 && f0 < 0.9 && f1 >= 0.9 {
			t90 = linInterp(t[i-1], t[i], f0, f1, 0.9)
			break
		}
	}
	if t90 > t10 && t10 > 0 {
		out["riseTimeS"] = t90 - t10
	}
	band := 0.02 * math.Abs(dy)
	settle := t[len(t)-1] - t0
	for i := len(y) - 1; i >= i0; i-- {
		if math.Abs(y[i]-yf) > band {
			if i+1 < len(t) {
				settle = t[i+1] - t0
			}
			break
		}
		settle = t[i] - t0
	}
	out["settlingTimeS"] = settle
	return out
}

// crossings returns the x values where y crosses level (both directions).
func crossings(x, y []float64, level float64) (up, down []float64) {
	for i := 1; i < len(y); i++ {
		if y[i-1] < level && y[i] >= level {
			up = append(up, linInterp(x[i-1], x[i], y[i-1], y[i], level))
		}
		if y[i-1] >= level && y[i] < level {
			down = append(down, linInterp(x[i-1], x[i], y[i-1], y[i], level))
		}
	}
	return
}

// valueAt linearly interpolates y at x.
func valueAt(x, y []float64, at float64) float64 {
	if len(x) == 0 {
		return 0
	}
	i := sort.SearchFloat64s(x, at)
	if i <= 0 {
		return y[0]
	}
	if i >= len(x) {
		return y[len(y)-1]
	}
	return y[i-1] + (y[i]-y[i-1])*(at-x[i-1])/(x[i]-x[i-1])
}

// decimate keeps ≤ n points (log-spaced for Bode, uniform otherwise) for plots.
func decimate(x []float64, ys [][]float64, n int, logX bool) ([]float64, [][]float64) {
	if len(x) <= n {
		out := make([][]float64, len(ys))
		for i := range ys {
			out[i] = append([]float64(nil), ys[i]...)
		}
		return append([]float64(nil), x...), out
	}
	var idx []int
	if logX && x[0] > 0 {
		l0, l1 := math.Log(x[0]), math.Log(x[len(x)-1])
		j := 0
		for k := 0; k < n; k++ {
			target := l0 + (l1-l0)*float64(k)/float64(n-1)
			for j < len(x)-1 && math.Log(x[j]) < target {
				j++
			}
			if len(idx) == 0 || idx[len(idx)-1] != j {
				idx = append(idx, j)
			}
		}
	} else {
		// Uniform buckets; each keeps its first point plus the min and max
		// of every trace, so short peaks (overshoot, discharge pulses) survive.
		buckets := n / 2
		if buckets < 2 {
			buckets = 2
		}
		seen := map[int]bool{}
		for k := 0; k < buckets; k++ {
			a := k * len(x) / buckets
			bnd := (k + 1) * len(x) / buckets
			if bnd <= a {
				continue
			}
			pick := []int{a}
			for _, y := range ys {
				mi, ma := a, a
				for q := a; q < bnd; q++ {
					if y[q] < y[mi] {
						mi = q
					}
					if y[q] > y[ma] {
						ma = q
					}
				}
				pick = append(pick, mi, ma)
			}
			for _, q := range pick {
				if !seen[q] {
					seen[q] = true
					idx = append(idx, q)
				}
			}
		}
		if !seen[len(x)-1] {
			idx = append(idx, len(x)-1)
		}
		sort.Ints(idx)
	}
	// Keep extrema of each y between samples (overshoot peaks survive).
	xo := make([]float64, 0, len(idx))
	yo := make([][]float64, len(ys))
	for _, j := range idx {
		xo = append(xo, x[j])
		for s := range ys {
			yo[s] = append(yo[s], ys[s][j])
		}
	}
	return xo, yo
}

func round6(v float64) float64 {
	if v == 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	return roundSig(v, 6)
}
