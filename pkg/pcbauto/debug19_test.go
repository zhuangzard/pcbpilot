package pcbauto

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Deterministic router speed benchmark: a fixed iteration count and no
// effective time limit, so an optimisation must reproduce the exact output
// (hash of every track and via) and only the time may change.
func TestDebugRouteSpeed(t *testing.T) {
	f := os.Getenv("PCBAUTO_SPEED")
	if f == "" {
		t.Skip()
	}
	iters := 2
	if v := os.Getenv("PCBAUTO_SPEED_ITERS"); v != "" {
		iters, _ = strconv.Atoi(v)
	}
	noOwnerCache = os.Getenv("PCBAUTO_NOOWN") != ""
	noUnreachableProof = os.Getenv("PCBAUTO_NOPROOF") != ""
	defer func() { noOwnerCache, noUnreachableProof = false, false }()
	b := loadFixture(t, f)
	var ru0, ru1 syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru0)
	start := time.Now()
	res, err := Run(context.Background(), b, Options{Stack: StackOptions{Force: b.CopperLayers}, NoEscalate: true,
		Route: RouteOptions{Timeout: 2 * time.Hour, MaxIters: iters}})
	if err != nil {
		t.Fatal(err)
	}
	el := time.Since(start)
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru1)
	cpu := time.Duration(ru1.Utime.Nano()-ru0.Utime.Nano()) + time.Duration(ru1.Stime.Nano()-ru0.Stime.Nano())
	var lines []string
	for _, tr := range res.Route.Tracks {
		lines = append(lines, fmt.Sprintf("T %s %d %.4f %.4f %.4f %.4f %.4f", tr.Net, tr.Layer, tr.A.X, tr.A.Y, tr.B.X, tr.B.Y, tr.Width))
	}
	for _, v := range res.Route.Vias {
		lines = append(lines, fmt.Sprintf("V %s %.4f %.4f %.4f", v.Net, v.C.X, v.C.Y, v.Dia))
	}
	for _, u := range res.Route.Unrouted {
		lines = append(lines, fmt.Sprintf("U %s %v %s", u.Net, u.Pads, u.Reason))
	}
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l + "\n"))
	}
	for _, nt := range res.Route.Notes {
		if len(nt) > 6 && nt[:6] == "radii:" {
			t.Log(nt)
		}
	}
	t.Logf("%s iters=%d: cpu %.1f s (wall %.1f s), completion %.1f%%, %d tracks %d vias, hash %x", f, iters, cpu.Seconds(), el.Seconds(), res.Route.Stats.Completion, len(res.Route.Tracks), len(res.Route.Vias), h.Sum(nil)[:8])
}
