package app

import (
	"context"
	"math"

	"github.com/zhuangzard/pcbpilot/pkg/pcbrouting"
)

// Lexicographic visibility optimization: first fewest real direction changes,
// then shortest measured centerline. Every replacement edge is straight/45deg
// and checked through the same live-geometry clearance predicate.
func crystalStringPull45(path [][2]float64, detour float64, clear func([2]float64, [2]float64) bool) [][2]float64 {
	if len(path) < 2 {
		return path
	}
	request := pcbrouting.Request{From: path[0], To: path[len(path)-1], Step: 1, MaxDetour: detour, MaxStates: pcbrouting.MaxSearchStates}
	optimized, err := pcbrouting.Optimize45(context.Background(), request, path, clear)
	if err != nil {
		return path
	}
	return optimized
}
func crystal45Heading(a, z [2]float64) (int, bool) {
	dx, dy := z[0]-a[0], z[1]-a[1]
	if math.Hypot(dx, dy) < 1e-6 {
		return 0, false
	}
	if math.Abs(dx) > 1e-6 && math.Abs(dy) > 1e-6 && math.Abs(math.Abs(dx)-math.Abs(dy)) > 1e-5 {
		return 0, false
	}
	h := int(math.Round(math.Atan2(dy, dx) / (math.Pi / 4)))
	if h < 0 {
		h += 8
	}
	return h % 8, true
}
func crystalHeadingDelta(a, b int) int {
	d := int(math.Abs(float64(a - b)))
	if d > 4 {
		return 8 - d
	}
	return d
}
