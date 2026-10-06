package boardmanual

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Notes is the project-specific human text merged on top of the automatic
// data (notes.json). Every field is optional; unknown fields are an error so
// a typo cannot silently drop text. Write "TODO" for an unknown value — never
// a guess. Schema: .agents/skills/pcbpilot/references/board-manual.md.
type Notes struct {
	Comment  string `json:"_comment,omitempty"`
	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Revision string `json:"revision,omitempty"`
	Overview string `json:"overview,omitempty"`
	// Sequence is the operating sequence (使用顺序), one step per entry.
	Sequence []string `json:"sequence,omitempty"`
	// Sources lists the documents the notes were written from (shown on the cover).
	Sources []string `json:"sources,omitempty"`

	// PartLabels labels other parts on the board picture (ref → text, e.g. "U8": "CPLD").
	PartLabels map[string]string   `json:"partLabels,omitempty"`
	Power      PowerNotes          `json:"power,omitempty"`
	Connectors map[string]ConnNote `json:"connectors,omitempty"`
	Cautions   []string            `json:"cautions,omitempty"`
	IO         []IONote            `json:"io,omitempty"`
	Jumpers    []JumperNote        `json:"jumpers,omitempty"`
	LEDs       []LEDNote           `json:"leds,omitempty"`
	// Firmwares are the firmware images whose LED behaviour leds[].modes
	// describes (column order of the LED table).
	Firmwares       []string       `json:"firmwares,omitempty"`
	Firmware        *FirmwareNotes `json:"firmware,omitempty"`
	Measurements    *Measurements  `json:"measurements,omitempty"`
	Equipment       []Equipment    `json:"equipment,omitempty"`
	Bringup         []BringupStep  `json:"bringup,omitempty"`
	Software        *Software      `json:"software,omitempty"`
	Troubleshooting []Trouble      `json:"troubleshooting,omitempty"`
	TODO            []string       `json:"todo,omitempty"`
}

// PowerNotes is the power-input description and per-rail measurement notes.
type PowerNotes struct {
	Input PowerInput          `json:"input,omitempty"`
	Notes []string            `json:"notes,omitempty"`
	Rails map[string]RailNote `json:"rails,omitempty"`
	// Margin multiplies the peak input current for the recommended supply
	// rating (default 1.5).
	Margin float64 `json:"margin,omitempty"`
}

// PowerInput describes the board's supply input.
type PowerInput struct {
	Connector         string   `json:"connector,omitempty"`
	VoltageV          float64  `json:"voltageV,omitempty"`
	VoltageRange      string   `json:"voltageRange,omitempty"`
	CurrentATyp       *float64 `json:"currentA_typ,omitempty"`
	CurrentAMax       *float64 `json:"currentA_max,omitempty"`
	IdleCurrent       string   `json:"idleCurrent,omitempty"`
	RecommendedSupply string   `json:"recommendedSupply,omitempty"`
	Plug              string   `json:"plug,omitempty"`
	Polarity          string   `json:"polarity,omitempty"`
	Protection        string   `json:"protection,omitempty"`
}

// RailNote is the measurement note of one rail (net name key).
type RailNote struct {
	// Probe is where to measure: "REF.PAD" (C9.1, J6.4) or "REF".
	Probe     string `json:"probe,omitempty"`
	Tolerance string `json:"tolerance,omitempty"` // e.g. "±2 %", "4.75–5.25 V"
	Expected  string `json:"expected,omitempty"`  // overrides the sim value text
	Note      string `json:"note,omitempty"`
}

// ConnNote is the human text of one connector (designator key).
type ConnNote struct {
	Name string `json:"name,omitempty"`
	// Role is the short role tag on the board picture: POWER, JTAG, CAN,
	// GAS, JUMPER, RS232, EXP, ANALOG … (default: from the pad nets).
	Role string `json:"role,omitempty"`
	// Subtitle is the one-line hint under the label on the board picture.
	Subtitle string `json:"subtitle,omitempty"`
	Purpose  string `json:"purpose,omitempty"`
	// ConnectsTo is what is plugged in (接到什么设备); SystemRole its role in
	// the system; Usage when and how to use it (steps).
	ConnectsTo string   `json:"connectsTo,omitempty"`
	SystemRole string   `json:"systemRole,omitempty"`
	Usage      []string `json:"usage,omitempty"`
	// Highlight is a boxed explanation on the connector page (e.g. "port or jumper?").
	Highlight string            `json:"highlight,omitempty"`
	Mating    string            `json:"mating,omitempty"`
	Part      string            `json:"part,omitempty"`
	PinNotes  map[string]string `json:"pinNotes,omitempty"`
	Cautions  []string          `json:"cautions,omitempty"`
	// DocPins is the pinout of the design document (pin → {name, net});
	// every pin is compared with the board netlist.
	DocPins map[string]DocPin `json:"docPins,omitempty"`
	// ExpectedPins is the pin count the BOM/document gives (0 = not checked).
	ExpectedPins int    `json:"expectedPins,omitempty"`
	Source       string `json:"source,omitempty"`
}

// DocPin is one pin of a document pinout.
type DocPin struct {
	Name string `json:"name,omitempty"`
	Net  string `json:"net,omitempty"`
}

// IONote is one external signal.
type IONote struct {
	Signal     string `json:"signal"`
	Connector  string `json:"connector,omitempty"`
	Pin        string `json:"pin,omitempty"`
	Direction  string `json:"direction,omitempty"`
	Level      string `json:"level,omitempty"`
	MaxCurrent string `json:"maxCurrent,omitempty"`
	Notes      string `json:"notes,omitempty"`
}

// JumperNote is one jumper / configuration header.
type JumperNote struct {
	Ref      string   `json:"ref"`
	Name     string   `json:"name,omitempty"`
	Default  string   `json:"default,omitempty"`
	Settings []string `json:"settings,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// LEDNote is one indicator LED.
type LEDNote struct {
	Ref   string `json:"ref"`
	Name  string `json:"name,omitempty"`
	Color string `json:"color,omitempty"` // green|yellow|red|blue|white or text
	// Net is the anode net the documents give; it is checked against the board.
	Net      string `json:"net,omitempty"`
	Hardware string `json:"hardware,omitempty"` // a hardware LED: what lights it (firmware-independent)
	// Modes is the behaviour per firmware (firmwares[] keys).
	Modes map[string]string `json:"modes,omitempty"`
}

// FirmwareNotes describes firmware images and the FPGA/CPLD pin map.
type FirmwareNotes struct {
	Images []FirmwareImage `json:"images,omitempty"`
	// FPGARef is the designator of the programmable part the --pin-map file belongs to.
	FPGARef string   `json:"fpgaRef,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// FirmwareImage is one programming file.
type FirmwareImage struct {
	Name    string `json:"name"`
	File    string `json:"file,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Purpose string `json:"purpose,omitempty"`
}

// Measurements are the key-signal probes (rails come from sim + power.rails).
type Measurements struct {
	Signals []SignalProbe `json:"signals,omitempty"`
	Notes   []string      `json:"notes,omitempty"`
}

// SignalProbe is one signal to look at with a scope or meter.
type SignalProbe struct {
	Signal     string `json:"signal"`
	Net        string `json:"net,omitempty"`
	Probe      string `json:"probe,omitempty"` // "REF.PAD" or "REF"; empty = first connector pad on Net
	Expected   string `json:"expected,omitempty"`
	Instrument string `json:"instrument,omitempty"`
	Note       string `json:"note,omitempty"`
}

// Equipment is one item of required test equipment.
type Equipment struct {
	Item    string `json:"item"`
	Spec    string `json:"spec,omitempty"`
	Purpose string `json:"purpose,omitempty"`
}

// BringupStep is one step of the bring-up procedure.
type BringupStep struct {
	Title    string         `json:"title"`
	Firmware string         `json:"firmware,omitempty"`
	Setup    []string       `json:"setup,omitempty"`
	Checks   []BringupCheck `json:"checks,omitempty"`
	Note     string         `json:"note,omitempty"`
}

// BringupCheck is one check of a bring-up step with its pass/fail criteria.
type BringupCheck struct {
	Item   string `json:"item"`
	Probe  string `json:"probe,omitempty"`
	Expect string `json:"expect,omitempty"`
	See    string `json:"see,omitempty"` // what you should see (LEDs, terminal)
	Pass   string `json:"pass,omitempty"`
	Fail   string `json:"fail,omitempty"`
}

// Software is the interface description for firmware / host engineers.
type Software struct {
	Interfaces []Interface `json:"interfaces,omitempty"`
	Commands   []Command   `json:"commands,omitempty"`
	Ack        []string    `json:"ack,omitempty"`
	Telemetry  *Telemetry  `json:"telemetry,omitempty"`
	Notes      []string    `json:"notes,omitempty"`
}

// Interface is one communication interface.
type Interface struct {
	Name      string `json:"name"`
	Connector string `json:"connector,omitempty"`
	Settings  string `json:"settings,omitempty"`
	Status    string `json:"status,omitempty"`
	Notes     string `json:"notes,omitempty"`
}

// Command is one host command.
type Command struct {
	Key        string `json:"key"`
	Name       string `json:"name,omitempty"`
	AcceptedIn string `json:"acceptedIn,omitempty"`
	Effect     string `json:"effect,omitempty"`
	Ack        string `json:"ack,omitempty"`
}

// Telemetry is the periodic status line.
type Telemetry struct {
	Period  string  `json:"period,omitempty"`
	Example string  `json:"example,omitempty"`
	Fields  []Field `json:"fields,omitempty"`
	Notes   string  `json:"notes,omitempty"`
}

// Field is one telemetry field.
type Field struct {
	Field   string `json:"field"`
	Format  string `json:"format,omitempty"`
	Meaning string `json:"meaning,omitempty"`
}

// Trouble is one symptom → cause → check row.
type Trouble struct {
	Symptom string `json:"symptom"`
	Cause   string `json:"cause,omitempty"`
	Check   string `json:"check,omitempty"`
}

// ParseNotes reads notes.json strictly (unknown fields are an error).
func ParseNotes(raw []byte) (*Notes, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var n Notes
	if err := dec.Decode(&n); err != nil {
		return nil, fmt.Errorf("notes.json: %w", err)
	}
	return &n, nil
}
