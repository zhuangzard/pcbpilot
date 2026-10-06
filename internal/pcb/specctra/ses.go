package specctra

import (
	"fmt"
	"strconv"
	"strings"
)

// Segment is one straight routed piece, coordinates and width in mil.
type Segment struct {
	Net      string     `json:"net"`
	Layer    string     `json:"layer"`
	WidthMil float64    `json:"widthMil"`
	A        [2]float64 `json:"a"`
	B        [2]float64 `json:"b"`
}

// Via is one routed via, coordinates in mil.
type Via struct {
	Net      string     `json:"net"`
	Padstack string     `json:"padstack"`
	At       [2]float64 `json:"at"`
}

// Wiring is the copper a session (or a DSN wiring section) carries.
type Wiring struct {
	Segments []Segment `json:"segments"`
	Vias     []Via     `json:"vias"`
}

// ParseSES reads the routed wiring of a Specctra session file. Coordinates
// are converted from the session resolution to mil.
func ParseSES(src string) (*Wiring, error) {
	root, err := parseSexpr(src)
	if err != nil {
		return nil, fmt.Errorf("parse SES: %w", err)
	}
	if root.head() != "session" {
		return nil, fmt.Errorf("not a Specctra session: top-level is (%s ...)", root.head())
	}
	routes := root.child("routes")
	if routes == nil {
		return nil, fmt.Errorf("SES has no (routes) section")
	}
	scale, err := resolutionScale(routes.child("resolution"))
	if err != nil {
		return nil, err
	}
	w := &Wiring{}
	netOut := routes.child("network_out")
	if netOut == nil {
		return w, nil
	}
	for _, n := range netOut.children("net") {
		a := n.atoms()
		if len(a) == 0 {
			continue
		}
		name := a[0]
		for _, wire := range n.children("wire") {
			if err := addPath(w, name, wire.child("path"), scale); err != nil {
				return nil, err
			}
		}
		for _, v := range n.children("via") {
			va := v.atoms()
			if len(va) < 3 {
				return nil, fmt.Errorf("net %s: malformed via", name)
			}
			x, err1 := strconv.ParseFloat(va[1], 64)
			y, err2 := strconv.ParseFloat(va[2], 64)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("net %s: bad via coordinate", name)
			}
			w.Vias = append(w.Vias, Via{Net: name, Padstack: va[0], At: [2]float64{scale(x), scale(y)}})
		}
	}
	return w, nil
}

// ParseFixedWiring reads the (type fix) wires and vias of a DSN wiring
// section. Routers keep them but do not write them back into the session,
// so the importer never creates them.
func ParseFixedWiring(dsn string) (*Wiring, error) {
	root, err := parseSexpr(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse DSN: %w", err)
	}
	if root.head() != "PCB" && root.head() != "pcb" {
		return nil, fmt.Errorf("not a Specctra DSN: top-level is (%s ...)", root.head())
	}
	unit := 1.0
	if r := root.child("resolution"); r != nil {
		// DSN coordinates are in the resolution's unit; only the unit matters.
		if a := r.atoms(); len(a) > 0 {
			if unit, err = unitToMil(a[0]); err != nil {
				return nil, err
			}
		}
	}
	scale := toMil(func(v float64) float64 { return v * unit })
	w := &Wiring{}
	wiring := root.child("wiring")
	if wiring == nil {
		return w, nil
	}
	for _, c := range wiring.List[1:] {
		if !c.isList || !isFixed(c) {
			continue
		}
		net := ""
		if n := c.child("net"); n != nil && len(n.atoms()) > 0 {
			net = n.atoms()[0]
		}
		switch c.head() {
		case "wire":
			if err := addPath(w, net, c.child("path"), scale); err != nil {
				return nil, err
			}
		case "via":
			a := c.atoms()
			if len(a) < 3 {
				return nil, fmt.Errorf("malformed fixed via")
			}
			x, err1 := strconv.ParseFloat(a[1], 64)
			y, err2 := strconv.ParseFloat(a[2], 64)
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("bad fixed via coordinate")
			}
			w.Vias = append(w.Vias, Via{Net: net, Padstack: a[0], At: [2]float64{scale(x), scale(y)}})
		}
	}
	return w, nil
}

// ViaPadstack returns the via padstack the DSN structure declares ("" if none).
func ViaPadstack(dsn string) string {
	if i := strings.Index(dsn, "(structure"); i >= 0 {
		if m := reViaPadstack.FindStringSubmatch(dsn[i:]); m != nil {
			return m[1]
		}
	}
	return ""
}

// ViaDiameterMil returns the TopLayer circle diameter of a DSN padstack (0 if
// the padstack or a circle shape is absent).
func ViaDiameterMil(dsn, padstack string) float64 {
	root, err := parseSexpr(dsn)
	if err != nil {
		return 0
	}
	lib := root.child("library")
	if lib == nil {
		return 0
	}
	for _, ps := range lib.children("padstack") {
		if a := ps.atoms(); len(a) == 0 || a[0] != padstack {
			continue
		}
		for _, sh := range ps.children("shape") {
			if c := sh.child("circle"); c != nil {
				if a := c.atoms(); len(a) >= 2 && a[0] == "TopLayer" {
					d, _ := strconv.ParseFloat(a[1], 64)
					return d
				}
			}
		}
	}
	return 0
}

func isFixed(c *node) bool {
	t := c.child("type")
	return t != nil && len(t.atoms()) > 0 && t.atoms()[0] == "fix"
}

func addPath(w *Wiring, net string, path *node, scale toMil) error {
	if path == nil {
		return fmt.Errorf("net %s: wire without (path)", net)
	}
	a := path.atoms()
	if len(a) < 6 || len(a)%2 != 0 {
		return fmt.Errorf("net %s: malformed path", net)
	}
	width, err := strconv.ParseFloat(a[1], 64)
	if err != nil {
		return fmt.Errorf("net %s: bad path width %q", net, a[1])
	}
	var pts [][2]float64
	for i := 2; i+1 < len(a); i += 2 {
		x, err1 := strconv.ParseFloat(a[i], 64)
		y, err2 := strconv.ParseFloat(a[i+1], 64)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("net %s: bad path coordinate", net)
		}
		pts = append(pts, [2]float64{scale(x), scale(y)})
	}
	for i := 0; i+1 < len(pts); i++ {
		if pts[i] == pts[i+1] {
			continue
		}
		w.Segments = append(w.Segments, Segment{Net: net, Layer: a[0], WidthMil: scale(width), A: pts[i], B: pts[i+1]})
	}
	return nil
}

// toMil converts a coordinate to mil.
type toMil func(float64) float64

// resolutionScale converts session integer units to mil:
// (resolution mil 1000) → v/1000. Dividing (not multiplying by 1/1000) keeps
// 21650 → 21.65 exact.
func resolutionScale(r *node) (toMil, error) {
	if r == nil {
		return nil, fmt.Errorf("SES routes have no (resolution)")
	}
	a := r.atoms()
	if len(a) < 2 {
		return nil, fmt.Errorf("malformed (resolution)")
	}
	unit, err := unitToMil(a[0])
	if err != nil {
		return nil, err
	}
	div, err := strconv.ParseFloat(a[1], 64)
	if err != nil || div <= 0 {
		return nil, fmt.Errorf("bad resolution divisor %q", a[1])
	}
	return func(v float64) float64 { return v / div * unit }, nil
}

func unitToMil(u string) (float64, error) {
	switch strings.ToLower(u) {
	case "mil":
		return 1, nil
	case "inch":
		return 1000, nil
	case "mm":
		return 1000 / 25.4, nil
	case "um":
		return 1 / 25.4, nil
	case "cm":
		return 10000 / 25.4, nil
	}
	return 0, fmt.Errorf("unsupported Specctra unit %q", u)
}
