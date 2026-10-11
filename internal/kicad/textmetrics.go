package kicad

// strokeAdv is the advance width of one glyph of KiCad's stroke font at
// 1.27 mm text size, fitted to the textLength values kicad-cli writes into
// its SVG export (620 texts of a rendered sheet; worst fit error 0.26 mm
// per text). Glyphs the export never showed use strokeAdvDefault.
var strokeAdv = map[rune]float64{
	' ': 0.78, '#': 1.35, '(': 1.2, ')': 1.2, '+': 1.54, ',': 0.84, '-': 1.43, '.': 0.59, '/': 1.4,
	'0': 1.23, '1': 1.32, '2': 1.32, '3': 1.3, '4': 1.32, '5': 1.35, '6': 1.34, '7': 1.34, '8': 1.3, '9': 1.29, ':': 0.5,
	'A': 1.11, 'B': 1.33, 'C': 1.32, 'D': 1.36, 'E': 1.26, 'F': 1.19, 'G': 1.28, 'H': 1.37, 'I': 0.66, 'J': 1.06,
	'K': 1.18, 'L': 1.02, 'M': 1.38, 'N': 1.43, 'O': 1.31, 'P': 1.33, 'Q': 1.38, 'R': 1.32, 'S': 1.19, 'T': 1.01,
	'U': 1.4, 'V': 1.17, 'W': 1.6, 'X': 1.33, 'Y': 1.01, 'Z': 1.2, '_': 0.9, 'µ': 1.28, 'Ω': 1.5, '—': 1.75,
	'm': 1.76, 'w': 1.7, 'i': 0.7, 'l': 0.7,
}

const strokeAdvDefault = 1.2

// TextWidth is the drawn length (mm) of s in KiCad's stroke font at the
// given text height (1.27 mm is the schematic default).
func TextWidth(s string, size float64) float64 {
	w := 0.0
	for _, r := range s {
		a, ok := strokeAdv[r]
		if !ok {
			a = strokeAdvDefault
		}
		w += a
	}
	return w * size / 1.27
}
