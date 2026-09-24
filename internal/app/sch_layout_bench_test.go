package app

import (
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Realistic dense zones from measured symbol shapes (2-pin passive: body
// 21×17 raw, pins ±20, designator box above; TSOT23-5 core 51×41, pins at
// ±35 with official outward rotations). Used to reproduce the budget
// exhaustion seen in the v1.6.0 live acceptance and to regress the solver.

func twoPin(id, des string, pin1Net, pin2Net string) SchematicLayoutComponent {
	x, y := 0.0, 0.0
	left, right := directionNumber(180), directionNumber(0)
	return SchematicLayoutComponent{ID: id, Measurement: SchematicPlacement{Designator: des, X: x, Y: y,
		BBox:       SchematicBox{x - 10.5, y - 8.5, x + 10.5, y + 8.5},
		TextBBoxes: []SchematicBox{{x - 10, y + 10, x - 1.0517578125, y + 18}},
		Pins: []SchematicPin{{Number: "1", Net: pin1Net, X: x - 20, Y: y, Rotation: left},
			{Number: "2", Net: pin2Net, X: x + 20, Y: y, Rotation: right}}}}
}

func buckZoneFixture() SchematicLayoutInput {
	left, right, down := directionNumber(180), directionNumber(0), directionNumber(270)
	core := SchematicLayoutComponent{ID: "U", Measurement: SchematicPlacement{Designator: "U3",
		BBox:       SchematicBox{-25.5, -20.5, 25.5, 20.5},
		TextBBoxes: []SchematicBox{{-25, 20, -16.0517578125, 28}},
		Pins: []SchematicPin{
			{Number: "4", Net: "VIN", X: -35, Y: 10, Rotation: left},
			{Number: "1", Net: "EN", X: -35, Y: -10, Rotation: left},
			{Number: "3", Net: "LX", X: 35, Y: 10, Rotation: right},
			{Number: "5", Net: "FB", X: 35, Y: -10, Rotation: right},
			{Number: "2", Net: "GND", X: 0, Y: -30, Rotation: down},
		}}}
	comps := []SchematicLayoutComponent{core,
		twoPin("CIN1", "C1", "VIN", "GND"), twoPin("CIN2", "C2", "VIN", "GND"),
		twoPin("REN", "R1", "VIN", "EN"),
		twoPin("L", "L1", "LX", "+3V3"),
		twoPin("COUT1", "C3", "+3V3", "GND"), twoPin("COUT2", "C4", "+3V3", "GND"),
		twoPin("RFB1", "R2", "+3V3", "FB"), twoPin("RFB2", "R3", "FB", "GND"),
		twoPin("CFF", "C5", "+3V3", "FB"),
	}
	att := func(c, pin, host, hpin string) SchematicLayoutPeripheral {
		return SchematicLayoutPeripheral{ComponentID: c, PinNumber: pin, AttachTo: &SchematicLayoutAttach{ComponentID: host, PinNumber: hpin}}
	}
	return SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: "U", Components: comps,
		NetPolicies: map[string]string{"VIN": "module_port", "+3V3": "module_port", "GND": "local_ground", "EN": "direct", "LX": "direct", "FB": "direct"},
		Attachments: []SchematicLayoutPeripheral{
			att("CIN1", "1", "U", "4"), att("CIN2", "1", "U", "4"), att("REN", "2", "U", "1"),
			att("L", "1", "U", "3"), att("COUT1", "1", "L", "2"), att("COUT2", "1", "L", "2"),
			att("RFB1", "2", "U", "5"), att("RFB2", "1", "U", "5"), att("CFF", "2", "U", "5"),
		}}
}

// ESP32-S3-WROOM-1-like core: 41 pins, 10 raw pitch, 14 left / 14 right /
// 12 bottom + EP; most pins NC, a handful wired to the usual peripherals.
func mcuZoneFixture() SchematicLayoutInput {
	left, right, down := directionNumber(180), directionNumber(0), directionNumber(270)
	nets := map[string]string{"1": "GND", "2": "+3V3", "3": "EN", "27": "IO0", "36": "U0RXD", "37": "U0TXD", "40": "GND", "41": "GND", "4": "LED_CTRL"}
	var pins []SchematicPin
	states := map[string]string{}
	add := func(n int, x, y float64, rot *float64) {
		num := itoaPin(n)
		pins = append(pins, SchematicPin{Number: num, Net: nets[num], X: x, Y: y, Rotation: rot})
		if nets[num] == "" {
			states[num] = "nc"
		}
	}
	for i := 0; i < 14; i++ {
		add(1+i, -80, 70-float64(i)*10, left)
		add(40-i, 80, 70-float64(i)*10, right)
	}
	for i := 0; i < 12; i++ {
		add(15+i, -55+float64(i)*10, -90, down)
	}
	add(41, 65, -90, down)
	core := SchematicLayoutComponent{ID: "U", PinStates: states, Measurement: SchematicPlacement{Designator: "U1",
		BBox: SchematicBox{-70.5, -80.5, 70.5, 80.5}, TextBBoxes: []SchematicBox{{-70, 81, -61, 89}}, Pins: pins}}
	comps := []SchematicLayoutComponent{core,
		twoPin("C3V3A", "C1", "+3V3", "GND"), twoPin("C3V3B", "C2", "+3V3", "GND"), twoPin("C3V3C", "C3", "+3V3", "GND"),
		twoPin("REN", "R1", "+3V3", "EN"), twoPin("CEN", "C4", "EN", "GND"),
		twoPin("RIO0", "R2", "+3V3", "IO0"),
		twoPin("SWBOOT", "SW1", "IO0", "GND"), twoPin("SWRST", "SW2", "EN", "GND"),
		twoPin("RLED", "R3", "LED_CTRL", "LED_A"), twoPin("LED", "D1", "LED_A", "GND"),
	}
	att := func(c, pin, host, hpin string) SchematicLayoutPeripheral {
		return SchematicLayoutPeripheral{ComponentID: c, PinNumber: pin, AttachTo: &SchematicLayoutAttach{ComponentID: host, PinNumber: hpin}}
	}
	return SchematicLayoutInput{SchemaVersion: 1, CoreComponentID: "U", Components: comps,
		NetPolicies: map[string]string{"+3V3": "module_port", "GND": "local_ground", "EN": "direct", "IO0": "direct", "U0RXD": "module_port", "U0TXD": "module_port", "LED_CTRL": "direct", "LED_A": "direct"},
		Attachments: []SchematicLayoutPeripheral{
			att("C3V3A", "1", "U", "2"), att("C3V3B", "1", "U", "2"), att("C3V3C", "1", "U", "2"),
			att("REN", "2", "U", "3"), att("CEN", "1", "U", "3"), att("SWRST", "1", "U", "3"),
			att("RIO0", "2", "U", "27"), att("SWBOOT", "1", "U", "27"),
			att("RLED", "1", "U", "4"), att("LED", "1", "RLED", "2"),
		}}
}

func itoaPin(n int) string { return strconv.Itoa(n) }

func TestDebugSchematicZoneBench(t *testing.T) {
	if os.Getenv("PCBPILOT_SCHBENCH") == "" {
		t.Skip()
	}
	zones := map[string]func() SchematicLayoutInput{"buck": buckZoneFixture, "mcu": mcuZoneFixture}
	for _, name := range []string{"buck", "mcu"} {
		for _, budget := range []int{20000, 200000} {
			in := zones[name]()
			annealAttemptHook = func(round, spent int, e error) {
				t.Logf("      round %d attempt spent %d: %.250v", round, spent, e)
			}
			annealHook = func(e error) {
				if e != nil {
					t.Logf("   anneal failed: %.300v", e)
				} else {
					t.Logf("   anneal ok")
				}
			}
			in.MaxCandidates = budget
			start := time.Now()
			out, err := PlanSchematicLayout(in)
			var sf *SchematicLayoutSearchFailure
			detail := ""
			if errors.As(err, &sf) {
				detail = sf.Error()
			}
			if err != nil {
				t.Logf("%s budget %d: FAILED in %.2fs: %v %s", name, budget, time.Since(start).Seconds(), err, detail)
				_ = name
				continue
			}
			t.Logf("%s budget %d: ok in %.2fs, candidates %d, %d wires, %d flags, score %v", name, budget, time.Since(start).Seconds(), out.CandidatesUsed, len(out.Wires), len(out.Flags), out.Score)
		}
	}
}

// Dense zones the candidate search could not solve at the default budget
// (MCU: not even at 200 000 candidates / 66 s) are solved by the annealing
// placer, deterministically, through the unchanged terminal gates.
func TestAnnealSolvesDenseZonesAtDefaultBudget(t *testing.T) {
	for name, build := range map[string]func() SchematicLayoutInput{"buck": buckZoneFixture, "mcu": mcuZoneFixture} {
		t.Run(name, func(t *testing.T) {
			a, err := PlanSchematicLayout(build())
			if err != nil {
				t.Fatalf("dense %s zone not solved at the default budget: %v", name, err)
			}
			if a.Search == nil || !strings.HasPrefix(a.Search.Strategy, "anneal-v1") {
				t.Fatalf("expected the annealing placer, got %+v", a.Search)
			}
			b, err := PlanSchematicLayout(build())
			if err != nil || !reflect.DeepEqual(a.Placements, b.Placements) || !reflect.DeepEqual(a.Wires, b.Wires) || !reflect.DeepEqual(a.Flags, b.Flags) {
				t.Fatalf("not deterministic: %v", err)
			}
			if len(a.Placements) != len(build().Components) {
				t.Fatalf("placed %d of %d parts", len(a.Placements), len(build().Components))
			}
		})
	}
}

// Small zones keep the candidate search (and its pinned exact results).
func TestAnnealLeavesSmallZonesToTheSearch(t *testing.T) {
	out, err := PlanSchematicLayout(standaloneLayoutFixture())
	if err != nil {
		t.Fatal(err)
	}
	if out.Search != nil && strings.HasPrefix(out.Search.Strategy, "anneal") {
		t.Fatal("a two-part zone should not use the annealing placer")
	}
}
