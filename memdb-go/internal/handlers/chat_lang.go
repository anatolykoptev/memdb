package handlers

import "unicode"

// detectLang detects the primary language of text by character ratios.
// Returns "zh" for Chinese (>30% CJK), "ru" for Russian (>30% Cyrillic), "en" otherwise.
func detectLang(text string) string {
	if text == "" {
		return "en"
	}

	var total, cjk, cyrillic int
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			total++
			if isCJK(r) {
				cjk++
			} else if isCyrillic(r) {
				cyrillic++
			}
		}
	}

	if total == 0 {
		return "en"
	}

	const threshold = 0.3
	if float64(cjk)/float64(total) > threshold {
		return "zh"
	}
	if float64(cyrillic)/float64(total) > threshold {
		return "ru"
	}
	return "en"
}

// isCJK returns true if the rune is a CJK Unified Ideograph.
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Extension B
		(r >= 0xF900 && r <= 0xFAFF) // CJK Compatibility
}

// isCyrillic returns true if the rune is in the Cyrillic block.
func isCyrillic(r rune) bool {
	return r >= 0x0400 && r <= 0x04FF
}
