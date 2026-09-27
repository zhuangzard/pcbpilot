package intent

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DDR memory interfaces are recognised from their net names (DDR3/DDR4
// "DDR_A_DQS0P", "DDR_DQ10_A", LPDDR4 "DDR_DQSA0_P", "DDR_CAA3", …): the
// strobe and clock pairs become differential pairs, the DQ/DM/DQS of each
// byte lane one length group, and address/command/control with the clock
// one fly-by group per channel. Nets a spec declares keep the spec's
// grouping.

// ddrAddrTolMil is the address/command/control-to-clock tolerance of a
// fly-by group: the common DDR3/DDR4 layout-guide ±100 mil, loose next to
// the byte lane (the DDR class GroupSkewMil, 25 mil).
const ddrAddrTolMil = 100

var (
	reDDRName  = regexp.MustCompile(`(?i)((^|[_\-])(LP)?DDR[0-9]*([_\-]|$)|DRAM|(^|[_\-])DQS)`)
	reDDRPol   = regexp.MustCompile(`[_\-]?(P|N|T|C)$`)
	reDDRData  = regexp.MustCompile(`(^|_)(DQS|DMI|DM|DQ)([A-Z]?)([0-9]+)`)
	reDDRAddr  = regexp.MustCompile(`(^|_)(BA|BG|CA|A)([A-Z]?)([0-9]+)(_|$)`)
	reDDRCmd   = regexp.MustCompile(`(^|_)(RAS|CAS|WE|CKE|CS|ODTCA|ODT|ACT|PAR|CLK|CK)([A-Z]?)([0-9]*)(_|$)`)
	reDDRStrip = regexp.MustCompile(`(^|_)(LP)?DDR[0-9]*(_|$)|DRAM`)
)

func isDDRNet(n string) bool { return reDDRName.MatchString(n) }

// ddrPairs returns the P/N (t/c) pairs among DDR-named nets whose base is a
// strobe or clock (DQS, CK/CLK, WCK).
func ddrPairs(nets []string) [][2]string {
	byUpper := map[string]string{}
	for _, n := range nets {
		byUpper[strings.ToUpper(strings.TrimSpace(n))] = n
	}
	var out [][2]string
	seen := map[string]bool{}
	for _, n := range nets {
		u := strings.ToUpper(strings.TrimSpace(n))
		if !isDDRNet(u) || seen[n] {
			continue
		}
		for _, sfx := range [][2]string{{"_P", "_N"}, {"P", "N"}, {"_T", "_C"}, {"_T", "_B"}} {
			if !strings.HasSuffix(u, sfx[0]) {
				continue
			}
			base := strings.TrimSuffix(u, sfx[0])
			if !strings.Contains(base, "DQS") && !strings.Contains(base, "CK") && !strings.Contains(base, "CLK") {
				continue
			}
			if m, ok := byUpper[base+sfx[1]]; ok && m != n {
				out = append(out, [2]string{n, m})
				seen[n], seen[m] = true, true
				break
			}
		}
	}
	return out
}

// ddrParse reads a DDR net name: kind data|addr, channel letter, index.
// pol strips a strobe/clock polarity suffix first.
func ddrParse(net string, pol bool) (kind, ch string, idx int, ok bool) {
	u := strings.ToUpper(strings.TrimSpace(net))
	if pol {
		u = reDDRPol.ReplaceAllString(u, "")
	}
	u = strings.ReplaceAll(u, "-", "_")
	rest := ""
	letter := ""
	switch {
	case reDDRData.MatchString(u):
		m := reDDRData.FindStringSubmatchIndex(u)
		tok := u[m[4]:m[5]]
		letter = u[m[6]:m[7]]
		idx, _ = strconv.Atoi(u[m[8]:m[9]])
		if tok == "DQ" {
			idx /= 8 // byte lane
		}
		kind = "data"
		rest = u[:m[0]] + "_" + u[m[1]:]
	case reDDRAddr.MatchString(u):
		m := reDDRAddr.FindStringSubmatchIndex(u)
		letter = u[m[6]:m[7]]
		kind = "addr"
		rest = u[:m[0]] + "_" + u[m[9]:]
	case reDDRCmd.MatchString(u):
		m := reDDRCmd.FindStringSubmatchIndex(u)
		letter = u[m[6]:m[7]]
		kind = "addr"
		rest = u[:m[0]] + "_" + u[m[9]:]
	default:
		return "", "", 0, false
	}
	// A separate single-letter token is the channel ("DDR_A_CS0",
	// "DDR_DQ10_A"); otherwise the letter glued to the token ("DDR_DQA10").
	rest = reDDRStrip.ReplaceAllString(rest, "_")
	for _, t := range strings.Split(rest, "_") {
		if len(t) == 1 && t[0] >= 'A' && t[0] <= 'Z' && ch == "" {
			ch = t
		}
	}
	if ch == "" {
		ch = letter
	}
	return kind, ch, idx, true
}

// ddrGroups assigns every parseable DDR signal net its length group:
// DDR[_<ch>]_BYTE<n> for DQ/DM/DQS, DDR[_<ch>]_ADDR for address, command,
// control and clock. isSignal filters out rails (DDR_VDDQ, VREF).
func ddrGroups(nets []string, pairs [][2]string, isSignal func(string) bool) map[string]string {
	inPair := map[string]bool{}
	for _, p := range pairs {
		inPair[p[0]], inPair[p[1]] = true, true
	}
	out := map[string]string{}
	count := map[string]int{}
	for _, n := range nets {
		if !isDDRNet(n) || !isSignal(n) {
			continue
		}
		kind, ch, idx, ok := ddrParse(n, inPair[n])
		if !ok {
			continue
		}
		g := "DDR"
		if ch != "" {
			g += "_" + ch
		}
		if kind == "data" {
			g += "_BYTE" + strconv.Itoa(idx)
		} else {
			g += "_ADDR"
		}
		out[n] = g
		count[g]++
	}
	for n, g := range out {
		if count[g] < 3 { // a group needs several members to be a bus
			delete(out, n)
		}
	}
	return out
}

func sortedPairs(ps [][2]string) [][2]string {
	sort.Slice(ps, func(i, j int) bool { return ps[i][0] < ps[j][0] })
	return ps
}
