package rules

import "math"

// Intent is a net's electrical intent (04 §4.6). The resolver turns it into
// floors: current → width per layer, voltage → clearance. Floors raise an
// explicit rule and never lower it.
type Intent struct {
	CurrentA  float64
	TempRiseC float64 // 0 = DefaultTempRiseC
	// VoltageV is the peak working voltage; VoltageKnown says it was given
	// (0 V of a ground net is known, 0 of an unlabelled net is not).
	VoltageV     float64
	VoltageKnown bool
	Coated       bool // outer layers use B4 instead of B2
	// Mains marks a net a human must review (mains or hazardous domain);
	// Build lists it in the warnings. The floors are not creepage rules.
	Mains bool
	// NoNeckDown forbids a neck-down at pins (04 §3.5). It is the v2 form
	// of an intent widthMil.min at or above widthMil.outer (decision Q6).
	NoNeckDown bool
	// ControlledImpedance: the stack-up sets the width, so it never necks
	// down either (04 §3.5).
	ControlledImpedance bool
}

func (in *Intent) tempRise() float64 {
	if in.TempRiseC > 0 {
		return in.TempRiseC
	}
	return DefaultTempRiseC
}

// widthFloor is the current → width floor of a net on layer l.
func (in *Intent) widthFloor(l *Layer) int64 {
	return IntentWidth(in.CurrentA, in.tempRise(), l.CopperUm, l.Outer)
}

// neckFloor is the narrowest neck that still carries the current with at
// most twice the target rise (04 §3.5 hard constraint).
func (in *Intent) neckFloor(l *Layer) int64 {
	return IntentWidth(in.CurrentA, 2*in.tempRise(), l.CopperUm, l.Outer)
}

// voltageFloor is the clearance floor between nets a and b (nil = no
// intent) on layer l: V = |Va − Vb| when both voltages are known, otherwise
// max(|Va|, |Vb|). Without a known voltage on either side there is no floor.
func voltageFloor(a, b *Intent, l *Layer) int64 {
	ka, kb := a != nil && a.VoltageKnown, b != nil && b.VoltageKnown
	if !ka && !kb {
		return 0
	}
	var va, vb float64
	coated := false
	if a != nil {
		va, coated = a.VoltageV, a.Coated
	}
	if b != nil {
		vb, coated = b.VoltageV, coated || b.Coated
	}
	v := max(math.Abs(va), math.Abs(vb))
	if ka && kb {
		v = math.Abs(va - vb)
	}
	return VoltageClearance(v, l.Outer, coated)
}
