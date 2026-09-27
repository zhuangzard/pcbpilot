package powersim

// Simulate binds models, solves every scenario and returns the document plus
// the engine (for SPICE export / cross-checks).
func Simulate(d *Design, libs Libraries, opt Options) (*Output, *Engine, error) {
	e := NewEngine(d, libs, opt)
	out, err := e.Run()
	if err != nil {
		return nil, e, err
	}
	out.Models = e.ModelUses()
	out.Defs = &Definitions{
		PinCurrentA: "magnitude (A) of the DC current through the pad",
		Dir:         "source = current leaves the part into the net; sink = enters the part from the net; pass = no DC current (|I| < 1 nA)",
		NetCurrentA: "sum of source pin currents on the net (= sum of sink currents, KCL)",
		Worst:       "result 'worst' holds, per pin, the maximum current over all scenarios (pins[].scenario names where it occurred)",
	}
	return out, e, nil
}
