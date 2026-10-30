package app

import (
	"bytes"
	"io"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// fastrouteSavedLinger is how long a fastroute run may keep running after
// it logged "routing finished" and saved its final session: it should exit
// and write its report at once; one that does not (0.1.14 on a 1443-part
// board idled ~145 s until pcbpilot's deadline, never writing the report)
// is asked to stop then. Independent of the time-limit logic.
var fastrouteSavedLinger = 30 * time.Second

var reFastrouteCounts = regexp.MustCompile(`best board so far \((\d+) unrouted, (\d+) violations?\)`)

// fastrouteLogWatch passes fastroute's output through and notes, line by
// line, the last "best board so far (N unrouted, M violations)" and whether
// the session was saved after "routing finished" (saved closes then).
type fastrouteLogWatch struct {
	w     io.Writer
	ses   string
	saved chan struct{}

	mu                   sync.Mutex
	buf                  []byte
	finished, final      bool
	unrouted, violations int
	counts               bool
}

func newFastrouteLogWatch(w io.Writer, ses string) *fastrouteLogWatch {
	return &fastrouteLogWatch{w: w, ses: filepath.Base(ses), saved: make(chan struct{})}
}

func (l *fastrouteLogWatch) Write(p []byte) (int, error) {
	n, err := l.w.Write(p)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			break
		}
		l.line(string(l.buf[:i]))
		l.buf = l.buf[i+1:]
	}
	if len(l.buf) > 1<<16 { // a line that never ends: keep the tail only
		l.buf = l.buf[len(l.buf)-1<<12:]
	}
	return n, err
}

func (l *fastrouteLogWatch) line(s string) {
	if m := reFastrouteCounts.FindStringSubmatch(s); m != nil {
		l.unrouted, _ = strconv.Atoi(m[1])
		l.violations, _ = strconv.Atoi(m[2])
		l.counts = true
	}
	if strings.Contains(s, "routing finished") {
		l.finished = true
	}
	// "saved 'route\\route.r0.ses' (429254 bytes)": the path as fastroute
	// prints it; compare its base name.
	norm := strings.ReplaceAll(s, "\\", "/")
	if l.finished && !l.final && strings.Contains(norm, "saved '") &&
		(strings.Contains(norm, "/"+l.ses+"'") || strings.Contains(norm, "'"+l.ses+"'")) {
		l.final = true
		close(l.saved)
	}
}

// finalSaved: the session was saved after "routing finished".
func (l *fastrouteLogWatch) finalSaved() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.final
}

// lastCounts are the last logged unrouted / violation counts.
func (l *fastrouteLogWatch) lastCounts() (int, int, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.unrouted, l.violations, l.counts
}
