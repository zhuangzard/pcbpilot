package app

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Freerouting as a `kicad route` backend (--router freerouting): the same
// DSN in, a Specctra session out. It knows none of fastroute's extras
// (no-neck-down classes, pairs, tuning, --diagnose, a time limit with a
// checkpoint), so every pcbpilot gate judges its board as it is.

const freeroutingInstallHint = "freerouting not found: set --freerouting-bin or $FREEROUTING_BIN, unpack the official package (the one that bundles its Java runtime) to ~/.pcbpilot/freerouting/current, or put it on PATH"

// resolveFreerouting finds the freerouting launcher: explicit flag, then
// $FREEROUTING_BIN, ~/.pcbpilot/freerouting/current and PATH.
func resolveFreerouting(explicit string) (string, error) {
	return resolveRouterBinary("freerouting", explicit, "FREEROUTING_BIN", "--freerouting-bin", exec.LookPath, freeroutingInstallHint)
}

type freeroutingOpts struct {
	bin string
	// passes is the autorouter pass limit; 0 = 100.
	passes  int
	threads int
	// timeout kills the run: Freerouting writes its session only at the
	// end, so a run stopped here has no result. 0 = 30 min.
	timeout time.Duration
}

func (o freeroutingOpts) maxPasses() int {
	if o.passes <= 0 {
		return 100
	}
	return o.passes
}

func (o freeroutingOpts) limit() time.Duration {
	if o.timeout <= 0 {
		return 30 * time.Minute
	}
	return o.timeout
}

// freeroutingArgs: headless, no usage reporting (-da: a board never leaves
// this machine), the pass limit under its current name.
func freeroutingArgs(o freeroutingOpts, dsn, ses string) []string {
	args := []string{"-de", dsn, "-do", ses, "--gui.enabled=false", "-da",
		"--router.autorouter.max_passes=" + strconv.Itoa(o.maxPasses())}
	if o.threads > 0 {
		args = append(args, "-mt", strconv.Itoa(o.threads))
	}
	return args
}

var (
	// "… stage completed: …, final score: 497.89 (9 unrouted and 7 violations), …"
	// "Optimization was completed in 0.04 seconds with the score of 500.00 (9 unrouted and 0 violations)."
	freeroutingFinalRe   = regexp.MustCompile(`(?:Auto-routing stage completed|Optimization was completed).*\((\d+) unrouted and (\d+) violations?\)`)
	freeroutingVersionRe = regexp.MustCompile(`Freerouting v(\S+)`)
)

// parseFreeroutingLog reads the final counts from Freerouting's log: the
// last stage summary wins (the optimizer's, when it ran). ok is false when
// the log has none — the counts are then unknown.
func parseFreeroutingLog(log string) (unrouted, violations int, version string, ok bool) {
	if m := freeroutingVersionRe.FindStringSubmatch(log); m != nil {
		version = m[1]
	}
	all := freeroutingFinalRe.FindAllStringSubmatch(log, -1)
	if len(all) == 0 {
		return 0, 0, version, false
	}
	last := all[len(all)-1]
	unrouted, _ = strconv.Atoi(last[1])
	violations, _ = strconv.Atoi(last[2])
	return unrouted, violations, version, true
}

// runFreerouting routes dsn into ses. The whole log goes to logPath; stderr
// gets it too. A run without a session is an error, never an empty success;
// a session whose counts the log does not state is a Partial result (the
// board's own connectivity then counts it).
func runFreerouting(ctx context.Context, o freeroutingOpts, dsn, ses, logPath string, stderr io.Writer) (*routeResult, error) {
	// a session that cannot be removed could pass for this run's result
	if err := os.Remove(ses); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("freerouting: cannot remove the previous session %s: %w", ses, err)
	}
	args := freeroutingArgs(o, dsn, ses)
	fmt.Fprintf(stderr, "freerouting: %s %s\n", o.bin, strings.Join(args, " "))
	lf, err := os.Create(logPath)
	if err != nil {
		return nil, err
	}
	defer lf.Close()
	rctx, cancel := context.WithTimeout(ctx, o.limit())
	defer cancel()
	cmd := exec.CommandContext(rctx, o.bin, args...)
	w := io.MultiWriter(lf, stderr)
	cmd.Stdout, cmd.Stderr = w, w
	killTreeOnCancel(cmd)
	cmd.WaitDelay = 10 * time.Second
	start := time.Now()
	err = cmd.Run()
	if rctx.Err() != nil {
		// nothing a stopped run left behind is a result
		_ = os.Remove(ses)
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if rctx.Err() != nil {
		return nil, fmt.Errorf("freerouting did not finish within %s (--freerouting-time) and writes no session before the end; raise the limit or lower --freerouting-passes", o.limit())
	}
	if err != nil {
		return nil, fmt.Errorf("freerouting: %w (log: %s)", err, logPath)
	}
	if st, serr := os.Stat(ses); serr != nil || st.Size() == 0 {
		return nil, fmt.Errorf("freerouting wrote no session %s (log: %s)", ses, logPath)
	}
	_ = lf.Sync()
	logText, _ := os.ReadFile(logPath)
	unrouted, violations, version, ok := parseFreeroutingLog(string(logText))
	res := &routeResult{Router: routerFreerouting, Version: version, PatchSHA: executableSHA256(o.bin), Args: args,
		Session: ses, UnroutedCount: unrouted, Violations: violations, Report: logPath, Seconds: time.Since(start).Seconds()}
	if !ok {
		res.UnroutedCount, res.Partial = -1, true
	}
	return res, nil
}
