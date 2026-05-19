package render

import "strings"

// sparkBlocks is the standard 8-level block set used by every spark
// implementation. Index 0 is "near zero", index 7 is "near max".
var sparkBlocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// Sparkline renders a slice of values into a fixed-width unicode spark. A
// value of zero renders as ' ' (single space) so empty days don't pollute the
// trend. Values are scaled against the slice maximum.
func Sparkline(values []float64) string {
	if len(values) == 0 {
		return ""
	}
	max := 0.0
	for _, v := range values {
		if v > max {
			max = v
		}
	}
	if max <= 0 {
		return strings.Repeat(" ", len(values))
	}
	var b strings.Builder
	b.Grow(len(values) * 3) // unicode block runes are 3 bytes
	for _, v := range values {
		if v <= 0 {
			b.WriteRune(' ')
			continue
		}
		idx := int(v / max * float64(len(sparkBlocks)-1))
		if idx < 0 {
			idx = 0
		}
		if idx >= len(sparkBlocks) {
			idx = len(sparkBlocks) - 1
		}
		b.WriteRune(sparkBlocks[idx])
	}
	return b.String()
}
