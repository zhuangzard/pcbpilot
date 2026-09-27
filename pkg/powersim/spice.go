package powersim

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SpiceCheck compares our node voltages with ngspice on the linearised netlist.
type SpiceCheck struct {
	Scenario string             `json:"scenario"`
	Ran      bool               `json:"ran"`
	Skipped  string             `json:"skipped,omitempty"`
	MaxDiffV float64            `json:"maxDiffV"`
	WorstNet string             `json:"worstNet,omitempty"`
	Nodes    int                `json:"nodes"`
	Pass     bool               `json:"pass"`
	TolV     float64            `json:"tolV"`
	Diffs    map[string]float64 `json:"diffs,omitempty"` // nets above tolerance
}

var reSpiceName = regexp.MustCompile(`[^A-Za-z0-9_]`)

type spiceNames struct {
	byNode map[int]string
	net    map[string]string // spice name → net name
}

func (c *Circuit) spiceNames() spiceNames {
	sn := spiceNames{byNode: map[int]string{}, net: map[string]string{}}
	used := map[string]bool{}
	for i, name := range c.names {
		base := strings.ToLower("n_" + reSpiceName.ReplaceAllString(strings.ReplaceAll(name, "+", "p"), "_"))
		s := base
		for k := 2; used[s]; k++ {
			s = fmt.Sprintf("%s_%d", base, k)
		}
		used[s] = true
		sn.byNode[i] = s
		sn.net[s] = name
	}
	return sn
}

func (sn spiceNames) n(i int) string {
	if i < 0 {
		return "0"
	}
	return sn.byNode[i]
}

// WriteSPICE writes an equivalent DC netlist of a solved scenario. Regulators
// are linearised at the operating point (output = V source at the solved
// voltage, input = I source at the solved current); loads are current sources
// (or their knee resistance below the knee).
func (e *Engine) WriteSPICE(w io.Writer, scenarioName string) error {
	r, ok := e.runs[scenarioName]
	if !ok || r.sol == nil {
		return fmt.Errorf("scenario %q was not solved", scenarioName)
	}
	x := r.sol.X
	sn := r.c.spiceNames()
	bw := bufio.NewWriter(w)
	fmt.Fprintf(bw, "* %s — linearised DC netlist, scenario %s\n", Generator, scenarioName)
	fmt.Fprintf(bw, "* nodes are schematic nets (n_<net>); ground nets are node 0\n")
	fmt.Fprintf(bw, ".options temp=26.85 tnom=26.85 rshunt=%g\n", 1/gminNode)
	for k, el := range r.c.elems {
		label := r.extra[el]
		switch t := el.(type) {
		case *resistor:
			fmt.Fprintf(bw, "R%d %s %s %.9g ; %s\n", k, sn.n(t.t[0].Node), sn.n(t.t[1].Node), t.r, label)
		case *vsource:
			if t.rs > 0 {
				fmt.Fprintf(bw, "V%d %s m%d DC %.9g ; %s\n", k, sn.n(t.t[0].Node), k, t.e, label)
				fmt.Fprintf(bw, "R%ds m%d %s %.9g\n", k, k, sn.n(t.t[1].Node), t.rs)
			} else {
				fmt.Fprintf(bw, "V%d %s %s DC %.9g ; %s\n", k, sn.n(t.t[0].Node), sn.n(t.t[1].Node), t.e, label)
			}
		case *isource:
			fmt.Fprintf(bw, "I%d %s %s DC %.9g ; %s\n", k, sn.n(t.t[0].Node), sn.n(t.t[1].Node), t.i, label)
		case *diode:
			fmt.Fprintf(bw, "D%d %s %s DM%d ; %s\n.model DM%d D(IS=%.6g N=%.6g)\n", k, sn.n(t.t[0].Node), sn.n(t.t[1].Node), k, label, k, t.is, t.n)
		case *bjt:
			pol := "NPN"
			if t.pnp {
				pol = "PNP"
			}
			fmt.Fprintf(bw, "Q%d %s %s %s QM%d ; %s\n.model QM%d %s(IS=%.6g BF=%.6g BR=%.6g)\n", k, sn.n(t.t[0].Node), sn.n(t.t[1].Node), sn.n(t.t[2].Node), k, label, k, pol, t.is, t.bf, t.br)
		case *load:
			v := volt(x, t.t[0].Node) - volt(x, t.t[1].Node)
			if t.inom == 0 {
				continue
			}
			if v >= t.knee {
				fmt.Fprintf(bw, "I%d %s %s DC %.9g ; %s\n", k, sn.n(t.t[0].Node), sn.n(t.t[1].Node), t.inom, label)
			} else {
				fmt.Fprintf(bw, "R%d %s %s %.9g ; %s (below knee)\n", k, sn.n(t.t[0].Node), sn.n(t.t[1].Node), t.knee/t.inom, label)
			}
		case *regulator:
			if t.mode == modeOff {
				fmt.Fprintf(bw, "* %s off\n", label)
				continue
			}
			vo := volt(x, t.t[1].Node) - volt(x, t.t[2].Node)
			fmt.Fprintf(bw, "V%d %s %s DC %.9g ; %s output (%s)\n", k, sn.n(t.t[1].Node), sn.n(t.t[2].Node), vo, label, t.mode)
			fmt.Fprintf(bw, "I%din %s %s DC %.9g ; %s input\n", k, sn.n(t.t[0].Node), sn.n(t.t[2].Node), t.inCurrent(x), label)
		}
	}
	fmt.Fprintf(bw, ".control\nop\nprint all\n.endc\n.end\n")
	return bw.Flush()
}

var reSpiceOut = regexp.MustCompile(`^\s*(n_[a-z0-9_]+)\s*=\s*([-+0-9.eE]+)\s*$`)

// CheckSPICE runs ngspice (when on PATH) on the netlist of scenarioName and
// compares node voltages.
func (e *Engine) CheckSPICE(scenarioName string, tolV float64) *SpiceCheck {
	ck := &SpiceCheck{Scenario: scenarioName, TolV: tolV}
	bin, err := exec.LookPath("ngspice")
	if err != nil {
		ck.Skipped = "ngspice not on PATH"
		return ck
	}
	var buf bytes.Buffer
	if err := e.WriteSPICE(&buf, scenarioName); err != nil {
		ck.Skipped = err.Error()
		return ck
	}
	f, err := os.CreateTemp("", "pcbpilot-sim-*.cir")
	if err != nil {
		ck.Skipped = err.Error()
		return ck
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(buf.Bytes()); err != nil {
		ck.Skipped = err.Error()
		return ck
	}
	f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "-b", f.Name()).CombinedOutput()
	if err != nil && len(out) == 0 {
		ck.Skipped = "ngspice failed: " + err.Error()
		return ck
	}
	r := e.runs[scenarioName]
	sn := r.c.spiceNames()
	got := map[string]float64{}
	for _, line := range strings.Split(string(out), "\n") {
		if m := reSpiceOut.FindStringSubmatch(line); m != nil {
			if v, err := strconv.ParseFloat(m[2], 64); err == nil {
				got[m[1]] = v
			}
		}
	}
	if len(got) == 0 {
		ck.Skipped = "ngspice produced no node voltages"
		return ck
	}
	ck.Ran = true
	names := make([]string, 0, len(sn.net))
	for s := range sn.net {
		names = append(names, s)
	}
	sort.Strings(names)
	for _, s := range names {
		net := sn.net[s]
		if strings.Contains(net, "#") {
			continue // internal node
		}
		v, ok := got[s]
		if !ok {
			continue
		}
		ours := r.sol.X[r.c.index[net]]
		d := math.Abs(v - ours)
		ck.Nodes++
		if d > ck.MaxDiffV {
			ck.MaxDiffV, ck.WorstNet = d, net
		}
		if d > tolV {
			if ck.Diffs == nil {
				ck.Diffs = map[string]float64{}
			}
			ck.Diffs[net] = round(d, 6)
		}
	}
	ck.MaxDiffV = round(ck.MaxDiffV, 9)
	ck.Pass = ck.Nodes > 0 && len(ck.Diffs) == 0
	return ck
}
