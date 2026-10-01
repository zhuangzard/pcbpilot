package schaes

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// BusCandidate is a group of nets that reads better as one bus / one aligned
// label lane ("virtual bus"). Detection is name-based and advisory: it never
// renames, merges or reconnects anything.
type BusCandidate struct {
	Kind      string   `json:"kind"` // indexed | uart | spi | qspi | i2c | sdio | usb | mipi
	Key       string   `json:"key"`  // shared prefix ("" when none)
	Members   []string `json:"members"`
	Suggested string   `json:"suggested"` // suggested bus / lane name
	Why       string   `json:"why"`
}

var (
	reIndexed = regexp.MustCompile(`^(.*?[A-Z_])(\d+)$`)
	reDiff    = regexp.MustCompile(`^(.*?)(?:_?([PN])|([+-]))$`)
)

// protocol roles, longest token first so "TXD" wins over "TX".
var protoRoles = []struct {
	kind  string
	role  string
	token []string
}{
	{"uart", "TX", []string{"TXD", "TX"}},
	{"uart", "RX", []string{"RXD", "RX"}},
	{"uart", "RTS", []string{"RTS"}},
	{"uart", "CTS", []string{"CTS"}},
	{"uart", "DTR", []string{"DTR"}},
	{"uart", "DSR", []string{"DSR"}},
	{"spi", "SCK", []string{"SCLK", "SCK"}},
	{"spi", "MOSI", []string{"MOSI", "COPI", "SDI"}},
	{"spi", "MISO", []string{"MISO", "CIPO", "SDO"}},
	{"spi", "CS", []string{"NSS", "CSN", "CS"}},
	{"i2c", "SDA", []string{"SDA"}},
	{"i2c", "SCL", []string{"SCL"}},
	{"sdio", "CMD", []string{"CMD"}},
}

// DetectBusCandidates groups the page's nets. Power / ground nets never join
// a group. Each net joins at most one group (protocol groups first).
func DetectBusCandidates(nets []string) []BusCandidate {
	uniq := map[string]bool{}
	for _, n := range nets {
		n = strings.TrimSpace(n)
		if n == "" || IsPowerNet(n) || IsGroundNet(n) {
			continue
		}
		uniq[n] = true
	}
	names := sortedKeys(uniq)
	used := map[string]bool{}
	var out []BusCandidate

	// 1. protocol roles (prefix = text before the role token, "_"-trimmed)
	type hit struct{ net, role string }
	proto := map[string]map[string][]hit{} // kind → prefix → hits
	for _, n := range names {
		u := strings.ToUpper(n)
		for _, r := range protoRoles {
			matched := false
			for _, tok := range r.token {
				if strings.HasSuffix(u, tok) {
					pre := strings.TrimRight(u[:len(u)-len(tok)], "_-")
					if proto[r.kind] == nil {
						proto[r.kind] = map[string][]hit{}
					}
					proto[r.kind][pre] = append(proto[r.kind][pre], hit{n, r.role})
					matched = true
					break
				}
			}
			if matched {
				break
			}
		}
	}
	for _, kind := range []string{"spi", "uart", "i2c", "sdio"} {
		for _, pre := range sortedKeys(proto[kind]) {
			hits := proto[kind][pre]
			roles := map[string]bool{}
			for _, h := range hits {
				roles[h.role] = true
			}
			ok := false
			switch kind {
			case "uart":
				ok = (roles["TX"] && roles["RX"]) || len(roles) >= 3
			case "spi":
				ok = roles["SCK"] && (roles["MOSI"] || roles["MISO"]) && len(roles) >= 3
			case "i2c":
				ok = roles["SDA"] && roles["SCL"]
			case "sdio":
				ok = roles["CMD"]
			}
			if !ok {
				continue
			}
			var mem []string
			for _, h := range hits {
				mem = append(mem, h.net)
			}
			if kind == "sdio" || kind == "spi" {
				// pull the matching clock / data lines of the same prefix
				for _, n := range names {
					u := strings.ToUpper(n)
					if used[n] || contains(mem, n) {
						continue
					}
					rest := strings.TrimLeft(strings.TrimPrefix(u, pre), "_-")
					if strings.HasPrefix(u, pre) && (rest == "CLK" || regexp.MustCompile(`^(D|DAT|IO)\d$`).MatchString(rest)) {
						mem = append(mem, n)
					}
				}
				if kind == "spi" && len(mem) >= 5 {
					kind = "qspi"
				}
			}
			if kind == "sdio" && len(mem) < 3 {
				continue
			}
			sort.Strings(mem)
			for _, m := range mem {
				used[m] = true
			}
			label := pre
			if label == "" {
				label = strings.ToUpper(kind)
			}
			out = append(out, BusCandidate{Kind: kind, Key: pre, Members: mem, Suggested: label + "_" + strings.ToUpper(kind),
				Why: fmt.Sprintf("%s role set %s", strings.ToUpper(kind), strings.Join(sortedKeys(roles), "/"))})
		}
	}

	// 2. differential lanes (USB / MIPI / LVDS): X_P + X_N pairs
	pairs := map[string][]string{}
	for _, n := range names {
		if used[n] {
			continue
		}
		u := strings.ToUpper(n)
		if m := reDiff.FindStringSubmatch(u); m != nil && m[1] != "" {
			pairs[m[1]] = append(pairs[m[1]], n)
		}
	}
	mipi := map[string][]string{}
	for _, base := range sortedKeys(pairs) {
		mem := pairs[base]
		if len(mem) != 2 {
			continue
		}
		switch {
		case strings.Contains(base, "MIPI") || strings.Contains(base, "CSI") || strings.Contains(base, "DSI") || strings.Contains(base, "LVDS"):
			pre := base
			if m := regexp.MustCompile(`^(.*?)_?(CLK|D\d+|DATA\d+|LANE\d+)$`).FindStringSubmatch(base); m != nil {
				pre = m[1]
			}
			mipi[pre] = append(mipi[pre], mem...)
		case strings.Contains(base, "USB") || base == "D" || strings.HasSuffix(base, "_D"):
			sort.Strings(mem)
			for _, m := range mem {
				used[m] = true
			}
			out = append(out, BusCandidate{Kind: "usb", Key: base, Members: mem, Suggested: base, Why: "USB differential pair (P/N): keep as a parallel pair, never a lettered bus"})
		}
	}
	for _, pre := range sortedKeys(mipi) {
		mem := mipi[pre]
		sort.Strings(mem)
		for _, m := range mem {
			used[m] = true
		}
		out = append(out, BusCandidate{Kind: "mipi", Key: pre, Members: mem, Suggested: pre + "_LANES", Why: fmt.Sprintf("%d MIPI/LVDS P/N lines: lay out as pairs in lane order", len(mem))})
	}
	// USB D+/D- spelled DP/DM
	for _, pre := range []string{"USB_", ""} {
		dp, dm := pre+"DP", pre+"DM"
		var mem []string
		for _, n := range names {
			u := strings.ToUpper(n)
			if !used[n] && (u == dp || u == dm) {
				mem = append(mem, n)
			}
		}
		if len(mem) == 2 {
			for _, m := range mem {
				used[m] = true
			}
			key := strings.TrimSuffix(pre, "_")
			sug := key
			if sug == "" {
				sug = "USB"
			}
			out = append(out, BusCandidate{Kind: "usb", Key: key, Members: mem, Suggested: sug, Why: "USB differential pair (DP/DM): keep as a parallel pair"})
		}
	}

	// 3. indexed groups: PREFIX0…PREFIXn, ≥3 members, near-contiguous indices
	idx := map[string]map[int]string{}
	for _, n := range names {
		if used[n] {
			continue
		}
		u := strings.ToUpper(n)
		if m := reIndexed.FindStringSubmatch(u); m != nil {
			k, _ := strconv.Atoi(m[2])
			if idx[m[1]] == nil {
				idx[m[1]] = map[int]string{}
			}
			idx[m[1]][k] = n
		}
	}
	for _, pre := range sortedKeys(idx) {
		g := idx[pre]
		if len(g) < 3 {
			continue
		}
		var ks []int
		for k := range g {
			ks = append(ks, k)
		}
		sort.Ints(ks)
		span := ks[len(ks)-1] - ks[0] + 1
		if span > 2*len(ks) {
			continue // scattered GPIO numbers (IO0, IO21, IO45) are not a bus
		}
		var mem []string
		for _, k := range ks {
			mem = append(mem, g[k])
			used[g[k]] = true
		}
		name := strings.TrimRight(pre, "_")
		out = append(out, BusCandidate{Kind: "indexed", Key: pre, Members: mem,
			Suggested: fmt.Sprintf("%s[%d:%d]", name, ks[0], ks[len(ks)-1]),
			Why:       fmt.Sprintf("%d indexed nets %s%d…%s%d", len(mem), pre, ks[0], pre, ks[len(ks)-1])})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// SnapshotNets lists every net name visible on the page (pins, wires,
// markers, buses).
func SnapshotNets(s *Snapshot) []string {
	m := map[string]bool{}
	for _, p := range s.Parts {
		for _, q := range p.Pins {
			if q.Net != "" {
				m[q.Net] = true
			}
		}
	}
	for _, w := range s.Wires {
		if w.Net != "" {
			m[w.Net] = true
		}
	}
	for _, k := range s.Markers {
		if k.Net != "" {
			m[k.Net] = true
		}
	}
	return sortedKeys(m)
}
