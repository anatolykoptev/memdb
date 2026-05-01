// Package lang provides locale detection from text. Used by D10 skill
// loader (search package) and chat prompt selector (handlers package);
// neutral location prevents circular import between them.
package lang

import "unicode"

// Detect classifies text into "en", "ru", or "zh" based on Unicode
// character ratios. Only letter+digit runes count toward the total;
// punctuation and spaces are ignored.
//
// Thresholds: >30% CJK → "zh", >30% Cyrillic → "ru", otherwise "en".
// Empty string returns "en".
//
// Verbatim port of the original handlers/chat_lang.detectLang
// implementation (same threshold constant, same Unicode range checks,
// same letter+digit counting).
func Detect(text string) string {
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

// isCJK returns true if the rune is in one of the common CJK blocks.
// Mirrors handlers/chat_lang.isCJK exactly (same four ranges).
func isCJK(r rune) bool {
	return (r >= 0x4E00 && r <= 0x9FFF) || // CJK Unified
		(r >= 0x3400 && r <= 0x4DBF) || // CJK Extension A
		(r >= 0x20000 && r <= 0x2A6DF) || // CJK Extension B
		(r >= 0xF900 && r <= 0xFAFF) // CJK Compatibility
}

// isCyrillic returns true if the rune is in the Cyrillic block (U+0400–U+04FF).
// Mirrors handlers/chat_lang.isCyrillic exactly.
func isCyrillic(r rune) bool {
	return r >= 0x0400 && r <= 0x04FF
}
