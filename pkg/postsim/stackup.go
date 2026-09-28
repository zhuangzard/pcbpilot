package postsim

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
)

// Physical constants (SI).
const (
	RhoCu       = 1.72e-8 // Ω·m, annealed copper at 20 °C (same as pcb auto's IR solver)
	KCu         = 385.0   // W/(m·K)
	OzMm        = 0.035   // 1 oz/ft² copper ≈ 35 µm (1.378 mil)
	MilMm       = 0.0254
	sigmaSB     = 5.670e-8
	jlcPrepreg4 = 0.2104 // mm, JLC04161H-7628 L1→L2 / L3→L4 prepreg
)

// StackLayer is one copper layer of the modelled stackup.
type StackLayer struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	CuMm  float64 `json:"copperMm"`
	ZMm   float64 `json:"zMm"`  // centre depth below the top surface
	Kind  string  `json:"kind"` // signal | plane
	Plane string  `json:"planeNet,omitempty"`
}

// Stackup is the vertical model: copper layers top → bottom and the
// dielectric between consecutive layers.
type Stackup struct {
	Layers      []StackLayer `json:"layers"`
	DielMm      []float64    `json:"dielectricMm"` // len(Layers)-1
	ThicknessMm float64      `json:"thicknessMm"`
	Source      string       `json:"source"`
}

// StackOptions describe the stackup inputs; zero values take defaults.
type StackOptions struct {
	OuterOz, InnerOz float64
	ThicknessMm      float64
	PrepregMm        float64 // outer dielectric (L1→L2, Ln-1→Ln); 0 = JLC 7628 default for 4 layers, uniform otherwise
	Description      string  // e.g. intent copper.stackup "JLC04161H-7628 (4-layer 1.6 mm, L1→L2 prepreg 0.2104 mm)"
}

var (
	reThick   = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*mm`)
	rePrepreg = regexp.MustCompile(`prepreg\s+(\d+(?:\.\d+)?)\s*mm`)
)

// BuildStackup builds the layer z positions for the board's layer ids.
func BuildStackup(ids []int, o StackOptions) *Stackup {
	src := "defaults"
	if o.Description != "" {
		if o.ThicknessMm <= 0 {
			if m := reThick.FindStringSubmatch(o.Description); m != nil {
				o.ThicknessMm, _ = strconv.ParseFloat(m[1], 64)
			}
		}
		if o.PrepregMm <= 0 {
			if m := rePrepreg.FindStringSubmatch(o.Description); m != nil {
				o.PrepregMm, _ = strconv.ParseFloat(m[1], 64)
			}
		}
		src = o.Description
	}
	if o.ThicknessMm <= 0 {
		o.ThicknessMm = 1.6
	}
	if o.OuterOz <= 0 {
		o.OuterOz = 1
	}
	if o.InnerOz <= 0 {
		o.InnerOz = 0.5
		if len(ids) <= 2 {
			o.InnerOz = o.OuterOz
		}
	}
	n := len(ids)
	st := &Stackup{ThicknessMm: o.ThicknessMm, Source: src}
	cu := 0.0
	for k, id := range ids {
		oz := o.InnerOz
		if k == 0 || k == n-1 {
			oz = o.OuterOz
		}
		st.Layers = append(st.Layers, StackLayer{ID: id, Name: LayerName(id), CuMm: oz * OzMm, Kind: "signal"})
		cu += oz * OzMm
	}
	if n >= 2 {
		st.DielMm = make([]float64, n-1)
		rest := o.ThicknessMm - cu
		if rest < 0.05 {
			rest = 0.05
		}
		switch {
		case n == 2:
			st.DielMm[0] = rest
		default:
			pp := o.PrepregMm
			if pp <= 0 && n == 4 {
				pp = jlcPrepreg4
				if o.Description == "" {
					st.Source = fmt.Sprintf("defaults (4-layer %.2f mm, JLC 7628 prepreg %.4f mm)", o.ThicknessMm, pp)
				}
			}
			if pp <= 0 || 2*pp >= rest {
				for i := range st.DielMm {
					st.DielMm[i] = rest / float64(n-1)
				}
				break
			}
			st.DielMm[0], st.DielMm[n-2] = pp, pp
			for i := 1; i < n-2; i++ {
				st.DielMm[i] = (rest - 2*pp) / float64(n-3)
			}
		}
	}
	z := 0.0
	for k := range st.Layers {
		if k == 0 {
			z = st.Layers[0].CuMm / 2
		} else {
			z += st.Layers[k-1].CuMm/2 + st.DielMm[k-1] + st.Layers[k].CuMm/2
		}
		st.Layers[k].ZMm = z
	}
	return st
}

// index returns the stack position of layer id (-1 when absent).
func (s *Stackup) index(id int) int {
	for k, l := range s.Layers {
		if l.ID == id {
			return k
		}
	}
	return -1
}

// cuM is the copper thickness of stack layer k in metres.
func (s *Stackup) cuM(k int) float64 { return s.Layers[k].CuMm * 1e-3 }

// spanM is the via barrel length between stack layers a and b (m).
func (s *Stackup) spanM(a, b int) float64 { return math.Abs(s.Layers[a].ZMm-s.Layers[b].ZMm) * 1e-3 }
