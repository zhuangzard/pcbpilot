package pcbauto

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

// The Skill ships the canonical capability table; the package embeds a
// mirror. They must be byte-identical.
func TestPinCapsMirrorInSync(t *testing.T) {
	skill, err := os.ReadFile("../../.agents/skills/pcbpilot/references/pin-capabilities.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(skill, pinCapsRaw) {
		t.Fatal("pkg/pcbauto/data/pin-capabilities.json differs from .agents/skills/pcbpilot/references/pin-capabilities.json — copy the skill file over the mirror")
	}
}

func TestPinCapsESP32S3Exclusions(t *testing.T) {
	caps := DefaultPinCaps()
	pc := caps.For("ESP32-S3-WROOM-1-N16R8")
	if pc == nil || pc.Remap != "gpio-matrix" {
		t.Fatalf("ESP32-S3-WROOM-1 not matched: %+v", pc)
	}
	if len(pc.Pins) != 41 {
		t.Fatalf("want 41 module pins, got %d", len(pc.Pins))
	}
	fixed := map[string]string{
		"27": "IO0", "15": "IO3", "26": "IO45", "16": "IO46", // strapping
		"13": "IO19", "14": "IO20", // USB
		"37": "TXD0", "36": "RXD0", // ROM UART0
		"28": "IO35", "29": "IO36", "30": "IO37", // octal PSRAM
		"1": "GND", "2": "3V3", "3": "EN", "40": "GND", "41": "GND",
	}
	for pin, name := range fixed {
		p := pc.Pin(pin)
		if p == nil || p.Name != name {
			t.Fatalf("pin %s: want %s, got %+v", pin, name, p)
		}
		if p.Slot() {
			t.Fatalf("pin %s (%s) must not be a swap slot", pin, name)
		}
	}
	for _, pin := range []string{"4", "5", "38", "39", "23", "12"} { // IO4 IO5 IO2 IO1 IO21 IO8
		if p := pc.Pin(pin); p == nil || !p.Slot() {
			t.Fatalf("pin %s should be a free GPIO slot: %+v", pin, p)
		}
	}
	if p := pc.Pin("24"); p.Caution == "" { // IO47: 1.8 V on octal-flash variants
		t.Fatal("IO47 must carry a caution")
	}
	// Net needs: ADC nets only onto ADC pins; USB nets nowhere (no free usb pin).
	if n := caps.Need("VBAT_SENSE"); n != "analog-in" {
		t.Fatalf("VBAT_SENSE need %q", n)
	}
	if !caps.Satisfies(pc.Pin("4"), "analog-in") { // IO4 = ADC1_CH3
		t.Fatal("IO4 carries adc1")
	}
	if caps.Satisfies(pc.Pin("23"), "analog-in") { // IO21 has no ADC
		t.Fatal("IO21 has no ADC")
	}
	if caps.Need("LED_CTRL") != "" {
		t.Fatal("LED_CTRL is plain digital")
	}
	if caps.For("CH340C") != nil {
		t.Fatal("CH340C is not remappable")
	}
}

func TestPinCapsMergeAndValidate(t *testing.T) {
	if _, err := ParsePinCaps([]byte(`{"schemaVersion":1,"parts":[{"id":"x","match":["X"],"remap":"magic","pins":[]}]}`)); err == nil {
		t.Fatal("bad remap accepted")
	}
	o, err := ParsePinCaps([]byte(`{"schemaVersion":1,"parts":[{"id":"stm32f103c8","match":["STM32F103C8"],"remap":"af-table","pins":[
		{"pin":"10","name":"PA0","caps":["gpio","adc1","usart2_cts"]},{"pin":"11","name":"PA1","caps":["gpio","adc1","usart2_rts"]},
		{"pin":"30","name":"PA9","caps":["gpio","usart1_tx"]},{"pin":"31","name":"PA10","caps":["gpio","usart1_rx"]}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	caps := DefaultPinCaps()
	caps.Merge(o)
	if caps.For("STM32F103C8T6") == nil || caps.For("ESP32-S3-WROOM-1") == nil {
		t.Fatal("merge lost a part")
	}
}

func TestHungarian(t *testing.T) {
	inf := 1e18
	cost := [][]float64{{4, 1, 3}, {2, 0, 5}, {3, 2, 2}}
	a := hungarian(cost)
	sum := 0.0
	for i, j := range a {
		sum += cost[i][j]
	}
	if sum != 5 { // 1 + 2 + 2
		t.Fatalf("assignment %v cost %v, want 5", a, sum)
	}
	_ = inf
	// rectangular: 2 rows, 3 cols
	a = hungarian([][]float64{{9, 1, 9}, {9, 9, 1}})
	if a[0] != 1 || a[1] != 2 {
		t.Fatalf("rectangular %v", a)
	}
}

// crossBoard: an ESP32-S3 module whose two GPIO nets cross on the way to
// two resistors — IO4 (pin 4, upper) goes to the lower resistor and IO5
// (pin 5, lower) to the upper one. IO0 (strapping) also carries a net to a
// far button and must never move.
func crossBoard() *Board {
	pad := func(n, net string, x, y float64) *Pad {
		return &Pad{Number: n, Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{x, y}, W: 30, H: 30}}
	}
	u := &Part{Ref: "U1", Device: "ESP32-S3-WROOM-1-N8", Pos: Point{500, 500}}
	u.Pads = []*Pad{
		pad("1", "GND", 400, 300), pad("2", "+3V3", 400, 350),
		pad("4", "SIG_A", 600, 600), pad("5", "SIG_B", 600, 500), pad("6", "", 600, 400),
		pad("27", "BOOT", 600, 300),
	}
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 1600, 1000}.Corners(), CopperLayers: 2}
	b.Parts = []*Part{u,
		{Ref: "R1", Device: "0402 1k", Pos: Point{1000, 500}, Pads: []*Pad{pad("1", "SIG_A", 1000, 500), pad("2", "LED1", 1080, 500)}},
		{Ref: "R2", Device: "0402 1k", Pos: Point{1000, 600}, Pads: []*Pad{pad("1", "SIG_B", 1000, 600), pad("2", "LED2", 1080, 600)}},
		{Ref: "D1", Device: "LED", Pos: Point{1200, 500}, Pads: []*Pad{pad("1", "LED1", 1200, 500), pad("2", "GND", 1300, 500)}},
		{Ref: "D2", Device: "LED", Pos: Point{1200, 600}, Pads: []*Pad{pad("1", "LED2", 1200, 600), pad("2", "GND", 1300, 600)}},
		{Ref: "SW1", Device: "TS-1088", Pos: Point{1000, 150}, Pads: []*Pad{pad("1", "BOOT", 1000, 150), pad("2", "GND", 1100, 150)}},
	}
	_ = b.Index()
	return b
}

func TestPinSwapRemovesCrossing(t *testing.T) {
	b := crossBoard()
	an := Analyze(b, PowerSpec{}, nil)
	before := Ratsnest(b, an)
	if before.Crossings == 0 {
		t.Fatalf("fixture must start crossed: %+v", before)
	}
	cands := SearchPinSwaps(b, an, DefaultPinCaps())
	if len(cands) != 1 || cands[0].ref != "U1" {
		t.Fatalf("want one U1 candidate, got %d", len(cands))
	}
	c := cands[0]
	for _, s := range c.swaps {
		if s.FromPin == "27" || s.ToPin == "27" || s.Net == "BOOT" {
			t.Fatalf("strapping pin IO0 moved: %+v", s)
		}
	}
	if c.boardTo.Crossings >= before.Crossings {
		t.Fatalf("crossings %d → %d", before.Crossings, c.boardTo.Crossings)
	}
	after := Ratsnest(ApplyPinSwaps(b, c.swaps), an)
	if after != c.boardTo {
		t.Fatalf("ApplyPinSwaps disagrees with the estimate: %+v vs %+v", after, c.boardTo)
	}
	// the original board is untouched
	if Ratsnest(b, an) != before {
		t.Fatal("search mutated the input board")
	}
}

func TestPinSwapKeepsAlignedBoard(t *testing.T) {
	b := crossBoard()
	// uncross: IO4 → R2 (lower-right? no: pin 4 is y=600 → R2 at y=600)
	for _, pd := range b.Part("U1").Pads {
		switch pd.Number {
		case "4":
			pd.Net = "SIG_B"
		case "5":
			pd.Net = "SIG_A"
		}
	}
	an := Analyze(b, PowerSpec{}, nil)
	if cands := SearchPinSwaps(b, an, DefaultPinCaps()); len(cands) != 0 {
		t.Fatalf("aligned board must yield no swap, got %+v", cands[0].swaps)
	}
}

func TestFeedbackVerifyAndLoopOnSyntheticBoard(t *testing.T) {
	b := crossBoard()
	opts := Options{Stack: StackOptions{Force: 2}, Route: RouteOptions{Timeout: 20 * time.Second}}
	ctx := context.Background()
	res, err := Run(ctx, b, opts)
	if err != nil {
		t.Fatal(err)
	}
	c := Understand(b, res.Analysis)
	js := Joint(b, res.Analysis, c, res.Stackup, res.Route, res.DRC, JointOptions{PlacementScore: -1})
	fb, err := BuildFeedback(ctx, b, res, js, FeedbackOptions{Circuit: c, Route: opts, Verify: 1, Loop: 2})
	if err != nil {
		t.Fatal(err)
	}
	var swap *FeedbackItem
	for _, it := range append(append([]*FeedbackItem{}, fb.Items...), fb.Rejected...) {
		if it.Kind == FBPinSwap && swap == nil {
			swap = it
		}
		if it.Status != "live-unverified" {
			t.Fatalf("status %q", it.Status)
		}
	}
	if swap == nil || swap.ExpectedGain == nil || swap.ExpectedGain.Method != "reroute" {
		t.Fatalf("pin swap not verified by re-route: %+v", swap)
	}
	if swap.ExpectedGain.After["ratsCrossings"] >= swap.ExpectedGain.Before["ratsCrossings"] {
		t.Fatalf("verified swap did not remove the crossing: %v → %v", swap.ExpectedGain.Before, swap.ExpectedGain.After)
	}
	if fb.Loop == nil || len(fb.Loop.Passes) == 0 {
		t.Fatal("loop did not run")
	}
	for _, s := range fb.Loop.Accepted {
		if s.Net == "BOOT" {
			t.Fatal("loop moved the strapping net")
		}
	}
	// the base board still carries the original nets
	if b.Part("U1").Pads[2].Net != "SIG_A" {
		t.Fatal("feedback mutated the routed board")
	}
	raw, _ := json.Marshal(fb)
	if !strings.Contains(string(raw), `"schemaVersion":1`) {
		t.Fatal("feedback JSON lacks schemaVersion")
	}
}

func TestDecapOwnershipShared(t *testing.T) {
	pad := func(n, net string, x, y float64) *Pad {
		return &Pad{Number: n, Net: net, Layer: LayerTop, Box: OrientedBox{C: Point{x, y}, W: 20, H: 20}}
	}
	ic := func(ref string, x float64) *Part {
		p := &Part{Ref: ref, Device: "MCU", Pos: Point{x, 500}}
		for i := 0; i < 8; i++ {
			net := ""
			switch i {
			case 0:
				net = "+3V3"
			case 1:
				net = "GND"
			}
			p.Pads = append(p.Pads, pad(string(rune('1'+i)), net, x, 400+float64(i)*25))
		}
		return p
	}
	b := &Board{Rules: DefaultRules(), Outline: Rect{0, 0, 3000, 1000}.Corners()}
	b.Parts = []*Part{ic("U1", 500), ic("U2", 1500),
		{Ref: "C1", Device: "100nF", Pads: []*Pad{pad("1", "+3V3", 560, 400), pad("2", "GND", 560, 440)}}}
	_ = b.Index()
	an := Analyze(b, PowerSpec{}, nil)
	items := decapFeedback(b, an)
	if len(items) != 1 || items[0].Evidence.Refs[0] != "U2" || items[0].Applyable {
		t.Fatalf("want one non-applyable decap item for U2, got %+v", items)
	}
	if items[0].Evidence.Metrics["nearestDecapMil"] < 900 {
		t.Fatalf("distance evidence %v", items[0].Evidence.Metrics)
	}
}

// ESP32 mini replay (live dump of the 2026-09-25 E2E, placement confirmed by
// the user): the only remappable signal on U1 that is not pinned by function
// is LED_CTRL (ESP_TXD/RXD sit on the ROM UART0 pins, the keys on IO0/EN).
func TestFeedbackESP32MiniReplay(t *testing.T) {
	raw, err := os.ReadFile("testdata/esp32-mini-v4-base.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	var power PowerSpec
	praw, _ := os.ReadFile("testdata/esp32-mini-power.json")
	if err := json.Unmarshal(praw, &power); err != nil {
		t.Fatal(err)
	}
	an := Analyze(b, power, nil)
	for _, c := range SearchPinSwaps(b, an, DefaultPinCaps()) {
		for _, s := range c.swaps {
			switch s.Net {
			case "ESP_TXD", "ESP_RXD", "IO0", "EN", "USB_DP", "USB_DM":
				t.Fatalf("function-pinned net %s proposed for a swap: %+v", s.Net, s)
			}
		}
	}
	if testing.Short() {
		t.Skip("re-route verification skipped in -short")
	}
	opts := Options{Power: power, Stack: StackOptions{Force: 4}, Route: RouteOptions{Timeout: time.Minute}}
	ctx := context.Background()
	res, err := Run(ctx, b, opts)
	if err != nil {
		t.Fatal(err)
	}
	c := Understand(b, res.Analysis)
	js := Joint(b, res.Analysis, c, res.Stackup, res.Route, res.DRC, JointOptions{PlacementScore: -1})
	fb, err := BuildFeedback(ctx, b, res, js, FeedbackOptions{Circuit: c, Route: opts, Verify: 3})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range fb.Items {
		if it.Kind == FBPinSwap {
			t.Fatalf("confirmed layout: no pin swap should survive re-route verification, got %s (%s)", it.Title, it.ExpectedGain.Summary)
		}
	}
	for _, it := range fb.Rejected {
		t.Logf("rejected: %s — %s", it.Title, it.ExpectedGain.Summary)
	}
}

// Negative control on the real board: move LED_CTRL to IO5 (pin 5, the far
// left column of the module, away from R9) — the search must bring it back
// to the right-hand column and the re-route must confirm the gain.
func TestFeedbackESP32MiniMisassignedLED(t *testing.T) {
	if testing.Short() {
		t.Skip("re-routes the ESP32 mini twice")
	}
	raw, err := os.ReadFile("testdata/esp32-mini-v4-base.json")
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, pd := range b.Part("U1").Pads {
		switch pd.Number {
		case "38":
			pd.Net = ""
		case "5":
			pd.Net = "LED_CTRL"
		}
	}
	var power PowerSpec
	praw, _ := os.ReadFile("testdata/esp32-mini-power.json")
	_ = json.Unmarshal(praw, &power)
	opts := Options{Power: power, Stack: StackOptions{Force: 4}, Route: RouteOptions{Timeout: time.Minute}}
	ctx := context.Background()
	res, err := Run(ctx, b, opts)
	if err != nil {
		t.Fatal(err)
	}
	c := Understand(b, res.Analysis)
	js := Joint(b, res.Analysis, c, res.Stackup, res.Route, res.DRC, JointOptions{PlacementScore: -1})
	fb, err := BuildFeedback(ctx, b, res, js, FeedbackOptions{Circuit: c, Route: opts, Verify: 1})
	if err != nil {
		t.Fatal(err)
	}
	var got *FeedbackItem
	for _, it := range fb.Items {
		if it.Kind == FBPinSwap {
			got = it
		}
	}
	if got == nil {
		for _, it := range fb.Rejected {
			t.Logf("rejected: %s — %s", it.Title, it.ExpectedGain.Summary)
		}
		t.Fatal("misassigned LED_CTRL: no verified pin swap")
	}
	s := got.Proposal.Swaps[0]
	if s.Net != "LED_CTRL" || s.FromPin != "5" {
		t.Fatalf("unexpected swap %+v", s)
	}
	g := got.ExpectedGain
	t.Logf("%s: %s", got.Title, g.Summary)
	if g.After["wireLengthIn"] >= g.Before["wireLengthIn"] && g.After["vias"] >= g.Before["vias"] {
		t.Fatalf("no routed gain: %v → %v", g.Before, g.After)
	}
}
