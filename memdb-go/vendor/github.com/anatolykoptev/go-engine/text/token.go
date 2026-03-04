package text

import (
	"math"
	"unicode/utf8"
)

// DefaultCharsPerToken is the average bytes-per-token ratio for English text.
// Estimation and truncation operate on byte length (not rune count).
// Multilingual text (e.g., Cyrillic, CJK) may need ~2.5 bytes/token.
const DefaultCharsPerToken = 3.5

// EstimateTokens estimates the number of tokens in text using a character-based ratio.
func EstimateTokens(text string, charsPerToken float64) int {
	if text == "" || charsPerToken <= 0 {
		return 0
	}
	return int(math.Ceil(float64(len(text)) / charsPerToken))
}

// TruncateToTokenBudget truncates text to fit within maxTokens.
// Uses charsPerToken to estimate the character limit.
// UTF-8 safe: truncates at rune boundaries, not byte boundaries.
func TruncateToTokenBudget(text string, maxTokens int, charsPerToken float64) string {
	if maxTokens <= 0 || text == "" {
		return ""
	}

	maxBytes := int(float64(maxTokens) * charsPerToken)
	if len(text) <= maxBytes {
		return text
	}

	// Walk backward from maxBytes to find valid rune boundary.
	for maxBytes > 0 && !utf8.RuneStart(text[maxBytes]) {
		maxBytes--
	}
	return text[:maxBytes]
}
