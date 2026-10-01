package eprj3

// ConventionRow is one piece of format knowledge with its source. The report
// carries the table so a reader never has to guess units or axis direction.
type ConventionRow struct {
	Topic  string `json:"topic"`
	V4     string `json:"v4"`
	V3     string `json:"v3,omitempty"`
	Source string `json:"source"`
}

const (
	srcCLI     = "easyeda-client-cli docs, Coordinate Systems and Units (699e686)"
	srcFormat  = "easyeda-format-skill SKILL.md (9e42727)"
	srcLog     = "easyeda-format-skill FORMATLOG.md V3→V4 (9e42727)"
	srcSample  = "observed in the vendored kicad-to-easyeda-eprj3 sample (editVersion 4.1.36)"
	srcSchemas = "easyeda-format-skill schemas (9e42727)"
)

// Conventions returns the documented format knowledge, V3→V4 differences
// included. Entries marked "observed" come from the real sample only.
func Conventions() []ConventionRow {
	return []ConventionRow{
		{"line format", "`{type,id,ticket}||{payload}|` + LF per record; DOCHEAD has no id; the editor omits the final `|` on the last line (observed)", "same frame; V3 example DOCHEAD carries no ticket", srcFormat},
		{"eventual consistency", "append-only log: key = document + id (type excluded); larger ticket wins; tie → lexicographically larger DOCHEAD client", "V3 spec says the smaller client wins (contradicts the implementation)", srcLog},
		{"deletion", "atom: empty payload `||`; document: DELETE_DOC `{\"isDelete\":true}`; META is never deleted with an empty payload", "", srcFormat},
		{"docType", "20 values incl. CONFIG, EDIT_HEAD, PANEL_LIB, FONT, VARIANT, COMPONENT_GROUP(_DATA), SIMULATION, SIMULATION_SCH", "11 values; CONFIG was PROJECT_CONFIG", srcLog},
		{"DOCHEAD", "docType, uuid, client (16 lowercase hex), updateTime (ms), version; editor also writes editVersion (e.g. 4.1.36) and user", "docType/uuid/client only", srcLog + "; editVersion " + srcSample},
		{"schematic unit", "10 mil (0.01 inch) per unit; origin bottom-left of the sheet, Y up; A4 = 1170 × 825", "0.01 inch globally", srcCLI},
		{"PCB unit", "mil; grid/snap sizes are stored ×10 on write and ÷10 on read; CANVAS.unit (lowercase mm|mil) is the display unit only", "0.01 inch globally (same as schematic)", srcLog},
		{"rotation", "API angles are counter-clockwise positive; schematic COMPONENT.rotation = (360 − API angle) % 360; PCB COMPONENT.angle = API angle", "", srcCLI},
		{"yAxisDirection", "eprj3 local files may carry yAxisDirection up|down on schematic records (absent ≡ down per the schema); the observed 4.1.36 sample carries none", "no such marker", srcSchemas},
		{"booleans / colors", "JSON true/false; schematic colors may be null (theme default); PCB colors also rgb()/cmyk()/data:/blob:", "1/0 booleans; #RRGGBB or \"\"", srcLog},
		{"encoding", "UTF-8 text; non-ASCII titles/ids occur (e.g. PART id 电阻.1 in the sample)", "", srcSample},
		{"arcs", "single ARC type with arcType DOT|CENT", "ARC (two-point) and CARC (center)", srcLog},
		{"folder layout", "X.eprj3 index + sch/<schematic>/<sheet>.esch2 (+ <schematic>.ecfg/.evar) + pcb/<pcb>.epcb2 + panel/<panel>.epan2; names come from file names; library SYMBOL/FOOTPRINT/DEVICE documents are embedded before the main document", "single-file .eprj/.eprj2 (SQLite)", srcCLI},
		{"PCB nets / pads", "NET outer id is an array literal [\"NET\",name]; PAD_NET id is [\"PAD_NET\",componentId,padNumber,padLocalId]; board outline is POLY on layerId 11", "", srcCLI},
	}
}

// coordinatesFor returns the documented unit/axis convention for a docType.
func coordinatesFor(docType string) *Coordinates {
	switch docType {
	case "SCH_PAGE", "SYMBOL", "SIMULATION":
		return &Coordinates{
			Unit:             "10mil (0.01 inch)",
			Axis:             "origin bottom-left of the sheet, Y up",
			Rotation:         "file rotation = (360 - API angle) % 360",
			ConventionSource: srcCLI,
		}
	case "PCB", "FOOTPRINT":
		return &Coordinates{
			Unit:             "mil",
			Axis:             "layerId 1 = top copper, 11 = board outline",
			Rotation:         "file angle = API angle (counter-clockwise positive)",
			ConventionSource: srcCLI,
		}
	}
	return nil
}
