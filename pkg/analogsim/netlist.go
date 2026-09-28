package analogsim

import (
	"fmt"
	"math"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Analysis modes of one ngspice run.
const (
	modeDC     = "dc"     // DC sweep of the stimulus
	modeMain   = "main"   // .op + .ac + .tran step
	modeLoop   = "loop"   // loop gain by voltage injection at the ideal output
	modeMC     = "mc"     // Monte-Carlo (alter + ac / op per run)
	modeSample = "sample" // ADC sample-and-hold settling
	modeRamp   = "ramp"   // comparator triangle
	modeSwitch = "switch" // transistor on/off transient
	modeRStep  = "rstep"  // reset RC after a supply step
	modeXtal   = "xtal"   // crystal load resonance
	modeOp     = "op"     // operating point only
)

var reSan = regexp.MustCompile(`[^a-z0-9_]`)

// nodeNamer maps schematic nets to SPICE nodes.
type nodeNamer struct {
	c    *circuit
	name map[string]string
	used map[string]string
}

func newNamer(c *circuit) *nodeNamer {
	return &nodeNamer{c: c, name: map[string]string{}, used: map[string]string{}}
}

func (nn *nodeNamer) node(net string) string {
	if net == "" {
		return "0"
	}
	if nn.c.ground[net] {
		return "0"
	}
	if s, ok := nn.name[net]; ok {
		return s
	}
	s := "n_" + reSan.ReplaceAllString(strings.ToLower(net), "_")
	for i := 2; ; i++ {
		if prev, ok := nn.used[s]; !ok || prev == net {
			break
		}
		s = fmt.Sprintf("n_%s_%d", reSan.ReplaceAllString(strings.ToLower(net), "_"), i)
	}
	nn.name[net], nn.used[s] = s, net
	return s
}

func elemName(prefix, ref string) string {
	return prefix + "_" + reSan.ReplaceAllString(strings.ToLower(ref), "_")
}

func sp(v float64) string { return strings.TrimSuffix(fmt.Sprintf("%.6g", v), ".") }

// spt formats PWL/PULSE times with enough digits to keep 10 ns edges apart.
func spt(v float64) string { return strings.TrimSuffix(fmt.Sprintf("%.12g", v), ".") }

// spiceSetup is the per-run knobs.
type spiceSetup struct {
	mode    string
	vals    map[string]float64   // passive overrides by ref
	mcSets  []map[string]float64 // MC runs (mode mc)
	mcAC    bool                 // MC measures AC (else .op)
	f1, f2  float64
	ptsDec  int
	tstop   float64
	tstep   float64
	t0      float64
	stepAmp float64
	bias    float64
	sweepLo float64
	sweepHi float64
	sweepSt float64
	libPath string
	period  float64
	rampT   float64
	xtalCL  float64
}

// netlistResult tells the parser what was written.
type netlistResult struct {
	text   string
	out    string // output node
	stim   string // stimulus node
	loopA  string // oa node (loop mode)
	loopB  string // oi node
	link   string // V element whose current = op-amp output current
	probes []string
	files  map[string]string // data file role → file name
}

func (r *runner) netlist(blk *Block, s spiceSetup) netlistResult {
	b := blk.c
	c := r.c
	nn := newNamer(c)
	var w strings.Builder
	res := netlistResult{files: map[string]string{}}
	fmt.Fprintf(&w, "* pcbpilot sim analog — block %s %s (%s) mode %s\n", blk.ID, blk.Class, blk.Core, s.mode)
	fmt.Fprintf(&w, "* generated from schematic connectivity; rails from %s\n", r.railsFrom)
	fmt.Fprintf(&w, ".include \"%s\"\n", s.libPath)
	includes := map[string]bool{}
	val := func(ref string) float64 {
		if v, ok := s.vals[ref]; ok {
			return v
		}
		return c.passive[ref].Value
	}
	// Rails.
	driven := map[string]bool{}
	for _, net := range sortedKeys(b.rails) {
		if c.ground[net] || net == b.stim || net == b.stim2 {
			continue
		}
		if blk.Class == ClassLCFilter && net == b.out {
			continue
		}
		v := b.rails[net]
		n := nn.node(net)
		if n == "0" || driven[n] {
			continue
		}
		driven[n] = true
		if s.mode == modeRStep && net == b.supplyStep {
			ramp := r.lib.Defaults.RampS
			fmt.Fprintf(&w, "V_%s %s 0 PWL(0 0 %s %s)\n", n, n, sp(ramp), sp(v))
			continue
		}
		fmt.Fprintf(&w, "V_%s %s 0 DC %s\n", n, n, sp(v))
	}
	// Regulator error amplifier: drives Vout until V(FB) = Vref.
	if b.regFB != "" {
		fmt.Fprintf(&w, "V_VREF vref_reg 0 DC %s\n", sp(b.regVref))
		fmt.Fprintf(&w, "E_REG %s 0 vref_reg %s 1e5\n", nn.node(b.out), nn.node(b.regFB))
		driven[nn.node(b.out)] = true
	}
	// Passives.
	for _, ref := range b.passives {
		if s.mode == modeXtal {
			break
		}
		pv := c.passive[ref]
		if pv == nil {
			continue
		}
		a, bb := nn.node(pv.Nets[0]), nn.node(pv.Nets[1])
		if a == bb {
			continue
		}
		v := val(ref)
		switch pv.Kind {
		case "R":
			fmt.Fprintf(&w, "%s %s %s %s\n", elemName("R", ref), a, bb, sp(v))
		case "C":
			fmt.Fprintf(&w, "%s %s %s %s\n", elemName("C", ref), a, bb, sp(v))
		case "L":
			mid := "m_" + reSan.ReplaceAllString(strings.ToLower(ref), "_")
			fmt.Fprintf(&w, "%s %s %s %s\n", elemName("L", ref), a, mid, sp(v))
			fmt.Fprintf(&w, "%s %s %s 0.01\n", elemName("RDCR", ref), mid, bb)
		}
	}
	// Op-amps.
	for _, o := range b.opamps {
		m := o.Model
		id := reSan.ReplaceAllString(strings.ToLower(o.id()), "_")
		vp, vn := nn.node(o.Vp), nn.node(o.Vn)
		inp, inn, out := nn.node(o.Inp), nn.node(o.Inn), nn.node(o.Out)
		if o.Vendor != nil {
			path := o.Vendor.File
			if !filepath.IsAbs(path) {
				path = filepath.Join(r.lib.Dir, path)
			}
			if !includes[path] {
				fmt.Fprintf(&w, ".include \"%s\"\n", path)
				includes[path] = true
			}
			roleNode := map[string]string{"inp": inp, "inn": inn, "vp": vp, "vn": vn, "out": out}
			var pins []string
			for i, role := range o.Vendor.PinOrder {
				n, ok := roleNode[strings.ToLower(role)]
				if !ok {
					n = fmt.Sprintf("nc_%s_%d", id, i)
				}
				pins = append(pins, n)
			}
			fmt.Fprintf(&w, "X_%s %s %s\n", id, strings.Join(pins, " "), o.Vendor.Subckt)
			continue
		}
		if o.Comparator && o.OpenDrain {
			fmt.Fprintf(&w, "X_%s %s %s %s %s %s PCBPILOT_COMP_OD params: vos=%s tdel=%s\n", id, inp, inn, vp, vn, out, sp(m.VosV), sp(math.Max(m.TpdS, 1e-9)))
			continue
		}
		gbw, sr, aol := m.GBWHz, m.SlewVPerUs*1e6, math.Pow(10, m.AolDB/20)
		p2 := m.P2Hz
		if o.Comparator {
			gbw, sr, aol, p2 = 1e8, 1e9, 1e6, 1e9
			if m.TpdS > 0 {
				gbw = math.Min(1e8, 1/m.TpdS*10)
			}
		}
		oa, oi := "x_"+id+"_oa", "x_"+id+"_oi"
		fmt.Fprintf(&w, "X_%s %s %s %s %s %s %s %s PCBPILOT_OPAMP params: gbw=%s aol=%s sr=%s vos=%s p2=%s hp=%s hn=%s zout=%s\n",
			id, inp, inn, vp, vn, out, oa, oi, sp(gbw), sp(aol), sp(sr), sp(m.VosV), sp(p2), sp(m.HeadroomHighV), sp(m.HeadroomLowV), sp(m.ZoutOhm))
		if s.mode == modeLoop {
			fmt.Fprintf(&w, "V_LINK_%s %s %s DC 0 AC 1\n", id, oa, oi)
			res.loopA, res.loopB = oa, oi
		} else {
			fmt.Fprintf(&w, "V_LINK_%s %s %s DC 0\n", id, oa, oi)
		}
		res.link = "v_link_" + id
	}
	// Fixed-gain amplifiers.
	for _, a := range b.amps {
		ref := a.RefN
		if ref == "" {
			ref = a.Vn
		}
		bw := a.Model.BWHz
		if a.Model.GBWHz > 0 && a.Gain > 0 && a.Model.GBWHz/a.Gain < bw {
			bw = a.Model.GBWHz / a.Gain
		}
		g := a.Gain
		if a.RgRef != "" {
			g = a.Model.GainOffset + a.Model.GainK/val(a.RgRef)
		}
		hp, hn := a.Model.HeadroomHighV, a.Model.HeadroomLowV
		fmt.Fprintf(&w, "X_%s %s %s %s %s %s %s PCBPILOT_VAMP params: gain=%s bw=%s hp=%s hn=%s\n", strings.ToLower(a.Ref),
			nn.node(a.Inp), nn.node(a.Inn), nn.node(ref), nn.node(a.Vp), nn.node(a.Vn), nn.node(a.Out), sp(g), sp(bw), sp(hp), sp(hn))
	}
	// References.
	for _, ri := range b.refs {
		gm := 50.0
		if ri.Model.ZOhm > 0 {
			gm = 1 / ri.Model.ZOhm
		}
		kx := "ik_" + strings.ToLower(ri.Ref)
		fmt.Fprintf(&w, "V_IK_%s %s %s DC 0\n", strings.ToLower(ri.Ref), nn.node(ri.K), kx)
		fmt.Fprintf(&w, "X_%s %s %s PCBPILOT_SHUNTREF params: vz=%s gm=%s\n", strings.ToLower(ri.Ref), kx, nn.node(ri.A), sp(ri.Model.VzV), sp(gm))
		res.probes = append(res.probes, "i(v_ik_"+strings.ToLower(ri.Ref)+")")
	}
	// BJTs / MOSFETs.
	for _, q := range b.bjts {
		cn := "ic_" + strings.ToLower(q.Ref)
		fmt.Fprintf(&w, "V_IC_%s %s %s DC 0\n", strings.ToLower(q.Ref), nn.node(q.C), cn)
		pol := "NPN"
		if !q.NPN {
			pol = "PNP"
		}
		fmt.Fprintf(&w, "Q_%s %s %s %s QM_%s\n", strings.ToLower(q.Ref), cn, nn.node(q.B), nn.node(q.E), strings.ToLower(q.Ref))
		fmt.Fprintf(&w, ".model QM_%s %s(IS=%s BF=%s BR=%s VAF=100 CJE=4.5p CJC=3.6p TF=0.3n TR=50n)\n", strings.ToLower(q.Ref), pol, sp(q.IS), sp(q.BF), sp(q.BR))
		res.probes = append(res.probes, "i(v_ic_"+strings.ToLower(q.Ref)+")")
	}
	for _, f := range b.fets {
		cn := "id_" + strings.ToLower(f.Ref)
		fmt.Fprintf(&w, "V_ID_%s %s %s DC 0\n", strings.ToLower(f.Ref), nn.node(f.D), cn)
		vt := f.Model.VthV
		kp := 0.1
		if f.Model.RdsonOhm > 0 && f.Model.RdsonVgsV > vt {
			kp = 1 / (f.Model.RdsonOhm * (f.Model.RdsonVgsV - vt))
		}
		pol := "NMOS"
		if !f.N {
			pol, vt = "PMOS", -vt
		}
		fmt.Fprintf(&w, "M_%s %s %s %s %s MM_%s\n", strings.ToLower(f.Ref), cn, nn.node(f.G), nn.node(f.S), nn.node(f.S), strings.ToLower(f.Ref))
		fmt.Fprintf(&w, ".model MM_%s %s(LEVEL=1 VTO=%s KP=%s LAMBDA=0.01 CGSO=2e-11 CGDO=5e-12)\n", strings.ToLower(f.Ref), pol, sp(vt), sp(kp))
		// Body diode.
		fmt.Fprintf(&w, "D_%s %s %s PCBPILOT_D\n", strings.ToLower(f.Ref), nn.node(f.S), cn)
		res.probes = append(res.probes, "i(v_id_"+strings.ToLower(f.Ref)+")")
	}
	// Resistive DC loads.
	for _, net := range sortedKeys(b.loadR) {
		fmt.Fprintf(&w, "R_LOAD_%s %s 0 %s\n", nn.node(net), nn.node(net), sp(b.loadR[net]))
	}
	for _, net := range sortedKeys(b.loadI) {
		if strings.HasPrefix(net, "_") || b.loadI[net] <= 0 {
			continue
		}
		fmt.Fprintf(&w, "I_LOAD_%s %s 0 DC %s\n", nn.node(net), nn.node(net), sp(b.loadI[net]))
	}
	// ADC input.
	if a := b.adc; a != nil {
		n := nn.node(a.Net)
		if a.Model.CpinF > 0 {
			fmt.Fprintf(&w, "C_ADCPIN %s 0 %s\n", n, sp(a.Model.CpinF))
		}
		if s.mode == modeSample {
			per := s.period
			ts := a.Model.TsampleS
			fmt.Fprintf(&w, "V_SMP smp 0 PULSE(0 1 %s 1n 1n %s %s)\n", sp(per-ts), sp(ts), sp(per))
			fmt.Fprintf(&w, "V_RST rst 0 PULSE(0 1 %s 1n 1n %s %s)\n", sp(per-ts-per/4), sp(per/8), sp(per))
			fmt.Fprintf(&w, "S_SMP %s adc_s1 smp 0 SW_ADC\n", n)
			fmt.Fprintf(&w, "R_ADC adc_s1 adc_h %s\n", sp(math.Max(a.Model.RadcOhm, 1)))
			fmt.Fprintf(&w, "C_SH adc_h 0 %s\n", sp(a.Model.CshF))
			fmt.Fprintf(&w, "S_RST adc_h 0 rst 0 SW_ADC\n")
			fmt.Fprintf(&w, ".model SW_ADC SW(VT=0.5 VH=0 RON=1 ROFF=1e12)\n")
		}
	}
	// Crystal (load-resonance mode only).
	if x := b.xtal; x != nil && s.mode == modeXtal {
		cm, c0 := x.CmFF*1e-15, x.C0PF*1e-12
		cls := x.CLPF * 1e-12
		if cls <= 0 {
			cls = s.xtalCL
		}
		lm := (cm + c0 + cls) / (math.Pow(twoPi()*x.FHz, 2) * cm * (c0 + cls))
		fmt.Fprintf(&w, "V_XS xs 0 DC 0 AC 1\n")
		fmt.Fprintf(&w, "L_XM xs xm1 %s\nC_XM xm1 xm2 %s\nR_XM xm2 xb %s\nC_X0 xs xb %s\n", sp(lm), sp(cm), sp(x.ESR), sp(c0))
		fmt.Fprintf(&w, "C_XLOAD xb 0 %s\n", sp(s.xtalCL))
	}
	// Floating ports: 1 GΩ to ground keeps the DC matrix regular.
	for _, p := range b.openPorts {
		if n := nn.node(p); n != "0" && !driven[n] {
			fmt.Fprintf(&w, "R_OPEN_%s %s 0 1e9\n", n, n)
		}
	}
	// Stimulus.
	stimNode := ""
	if b.stim != "" {
		stimNode = nn.node(b.stim)
		res.stim = stimNode
	}
	switch {
	case s.mode == modeSwitch && b.driveNet != "":
		dn := nn.node(b.driveNet)
		hi := b.driveHigh
		t1, t2 := s.t0, s.t0+s.tstop/2
		if c.isRail(b.driveNet) {
			// Level shifter: the gate sits on a rail; drive the source side low instead.
			sn := nn.node(b.emitNet)
			fmt.Fprintf(&w, "V_DRV %s 0 PWL(0 %s %s %s %s 0 %s 0 %s %s)\n", sn, sp(hi), spt(t1), sp(hi), spt(t1+1e-8), spt(t2), spt(t2+1e-8), sp(hi))
			stimNode = sn
		} else {
			if !driven[dn] {
				fmt.Fprintf(&w, "V_DRV %s 0 PWL(0 0 %s 0 %s %s %s %s %s 0)\n", dn, spt(t1), spt(t1+1e-8), sp(hi), spt(t2), sp(hi), spt(t2+1e-8))
			}
			if b.emitNet != "" && !c.isRail(b.emitNet) {
				fmt.Fprintf(&w, "V_EMIT %s 0 DC 0\n", nn.node(b.emitNet))
			}
			stimNode = dn
		}
		res.stim = stimNode
	case s.mode == modeOp && b.driveNet != "":
		dn := nn.node(b.driveNet)
		if !driven[dn] {
			fmt.Fprintf(&w, "V_DRV %s 0 DC %s\n", dn, sp(b.driveHigh))
		}
		if b.emitNet != "" && !c.isRail(b.emitNet) {
			fmt.Fprintf(&w, "V_EMIT %s 0 DC 0\n", nn.node(b.emitNet))
		}
	case stimNode != "":
		src := "V_STIM"
		dc := s.bias
		switch s.mode {
		case modeRamp:
			T := s.rampT
			fmt.Fprintf(&w, "%s %s 0 PWL(0 %s %s %s %s %s)\n", src, stimNode, sp(s.sweepLo), sp(T/2), sp(s.sweepHi), sp(T), sp(s.sweepLo))
		case modeSample:
			fmt.Fprintf(&w, "%s %s 0 DC %s\n", src, stimNode, sp(dc))
		default:
			acMag, acPh := "1", ""
			if b.stim2 != "" {
				acMag = "0.5"
			}
			if s.mode == modeLoop {
				acMag = "0"
			}
			pulse := ""
			if s.mode == modeMain && s.tstop > 0 {
				amp := s.stepAmp
				if b.stim2 != "" {
					amp /= 2
				}
				pulse = fmt.Sprintf(" PULSE(%s %s %s %s %s 1 2)", sp(dc), sp(dc+amp), sp(s.t0), sp(s.tstop/1e5), sp(s.tstop/1e5))
			}
			fmt.Fprintf(&w, "%s %s 0 DC %s AC %s%s%s\n", src, stimNode, sp(dc), acMag, acPh, pulse)
			if b.stim2 != "" {
				n2 := nn.node(b.stim2)
				pulse2 := ""
				if s.mode == modeMain && s.tstop > 0 {
					pulse2 = fmt.Sprintf(" PULSE(%s %s %s %s %s 1 2)", sp(dc), sp(dc-s.stepAmp/2), sp(s.t0), sp(s.tstop/1e5), sp(s.tstop/1e5))
				}
				ac2 := "0.5 180"
				if s.mode == modeLoop {
					ac2 = "0"
				}
				fmt.Fprintf(&w, "V_STIM2 %s 0 DC %s AC %s%s\n", n2, sp(dc), ac2, pulse2)
			}
		}
	}
	res.out = nn.node(b.out)
	if b.out == "" {
		res.out = stimNode
	}
	// Control.
	w.WriteString(".options noacct rshunt=1e12\n.control\nset filetype=ascii\nset wr_singlescale\nset wr_vecnames\noption numdgt=10\n")
	file := func(role string) string {
		name := fmt.Sprintf("%s-%s-%s.txt", blk.ID, s.mode, role)
		res.files[role] = name
		return name
	}
	outV := "v(" + res.out + ")"
	switch s.mode {
	case modeDC:
		fmt.Fprintf(&w, "dc V_STIM %s %s %s\n", sp(s.sweepLo), sp(s.sweepHi), sp(s.sweepSt))
		probes := []string{outV}
		for _, o := range b.opamps {
			probes = append(probes, "v("+nn.node(o.Inp)+")", "v("+nn.node(o.Inn)+")")
		}
		fmt.Fprintf(&w, "wrdata %s %s\n", file("dc"), strings.Join(probes, " "))
	case modeMain:
		w.WriteString("op\n")
		fmt.Fprintf(&w, "print %s\n", r.opProbes(b, nn, res))
		if s.f2 > 0 && stimNode != "" {
			fmt.Fprintf(&w, "ac dec %d %s %s\n", s.ptsDec, sp(s.f1), sp(s.f2))
			fmt.Fprintf(&w, "wrdata %s %s\n", file("ac"), outV)
		}
		if s.tstop > 0 && stimNode != "" {
			fmt.Fprintf(&w, "tran %s %s 0 %s\n", sp(s.tstep), sp(s.tstop), sp(s.tstep))
			pr := []string{outV, "v(" + stimNode + ")"}
			if res.link != "" {
				pr = append(pr, "i("+res.link+")")
			}
			fmt.Fprintf(&w, "wrdata %s %s\n", file("tran"), strings.Join(pr, " "))
		}
	case modeOp:
		w.WriteString("op\n")
		fmt.Fprintf(&w, "print %s\n", r.opProbes(b, nn, res))
	case modeLoop:
		fmt.Fprintf(&w, "ac dec %d %s %s\n", s.ptsDec, sp(s.f1), sp(s.f2))
		fmt.Fprintf(&w, "wrdata %s v(%s) v(%s)\n", file("loop"), res.loopA, res.loopB)
	case modeMC:
		for i, set := range s.mcSets {
			for _, ref := range sortedKeys(set) {
				pv := c.passive[ref]
				if pv == nil {
					continue
				}
				prefix := map[string]string{"R": "R", "C": "C", "L": "L"}[pv.Kind]
				fmt.Fprintf(&w, "alter %s = %s\n", strings.ToLower(elemName(prefix, ref)), sp(set[ref]))
			}
			if s.mcAC {
				fmt.Fprintf(&w, "ac dec %d %s %s\n", s.ptsDec, sp(s.f1), sp(s.f2))
				fmt.Fprintf(&w, "wrdata %s %s\n", file(fmt.Sprintf("mc%03d", i)), outV)
			} else {
				w.WriteString("op\n")
				fmt.Fprintf(&w, "print %s\n", r.opProbes(b, nn, res))
			}
		}
	case modeSample:
		fmt.Fprintf(&w, "tran %s %s 0 %s\n", sp(s.tstep), sp(s.tstop), sp(s.tstep))
		fmt.Fprintf(&w, "wrdata %s %s v(adc_h) v(smp)\n", file("sample"), outV)
	case modeRamp:
		fmt.Fprintf(&w, "tran %s %s 0 %s\n", sp(s.tstep), sp(s.tstop), sp(s.tstep))
		fmt.Fprintf(&w, "wrdata %s v(%s) %s\n", file("ramp"), stimNode, outV)
	case modeSwitch:
		fmt.Fprintf(&w, "tran %s %s 0 %s\n", sp(s.tstep), sp(s.tstop), sp(s.tstep))
		pr := []string{outV, "v(" + stimNode + ")"}
		pr = append(pr, res.probes...)
		fmt.Fprintf(&w, "wrdata %s %s\n", file("switch"), strings.Join(pr, " "))
	case modeRStep:
		fmt.Fprintf(&w, "tran %s %s 0 %s\n", sp(s.tstep), sp(s.tstop), sp(s.tstep))
		fmt.Fprintf(&w, "wrdata %s %s v(%s)\n", file("rstep"), outV, nn.node(b.supplyStep))
	case modeXtal:
		x := b.xtal
		fmt.Fprintf(&w, "ac lin 4001 %s %s\n", sp(x.FHz*(1-600e-6)), sp(x.FHz*(1+600e-6)))
		fmt.Fprintf(&w, "wrdata %s i(v_xs)\n", file("xtal"))
	}
	w.WriteString("quit\n.endc\n.end\n")
	res.text = w.String()
	return res
}

// opProbes lists the .op vectors printed (block nodes + source currents).
func (r *runner) opProbes(b *blockCtx, nn *nodeNamer, res netlistResult) string {
	seen := map[string]bool{}
	var pr []string
	add := func(s string) {
		if s != "" && !seen[s] && s != "v(0)" {
			seen[s] = true
			pr = append(pr, s)
		}
	}
	add("v(" + nn.node(b.out) + ")")
	for _, o := range b.opamps {
		add("v(" + nn.node(o.Inp) + ")")
		add("v(" + nn.node(o.Inn) + ")")
	}
	for _, q := range b.bjts {
		add("v(" + nn.node(q.B) + ")")
		add("v(" + nn.node(q.E) + ")")
		add("v(ic_" + strings.ToLower(q.Ref) + ")")
	}
	for _, f := range b.fets {
		add("v(" + nn.node(f.G) + ")")
		add("v(" + nn.node(f.S) + ")")
		add("v(id_" + strings.ToLower(f.Ref) + ")")
	}
	for _, p := range res.probes {
		add(p)
	}
	if res.link != "" {
		add("i(" + res.link + ")")
	}
	for _, ri := range b.refs {
		add("v(" + nn.node(ri.K) + ")")
	}
	sort.SliceStable(pr, func(i, j int) bool { return false })
	return strings.Join(pr, " ")
}
