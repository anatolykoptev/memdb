package rerank

import "unicode/utf8"

// approxTokens returns an approximate token count for text.
//
// Heuristic (per-script, summed for mixed input):
//   - Cyrillic runes   → count / 1.5  (BPE avg ~1.5 runes/token)
//   - Latin runes      → count / 4    (BPE avg ~4 chars/token for English)
//   - Other (CJK, punct, misc) → 1 token per rune (conservative)
//
// NOT a real tokenizer — a rough budget estimator. Use to cap input at ~256
// tokens before bge-m3 / gte-multi-rerank server-side truncation silently
// discards suffix (auto_truncate=true in embed-server defaults).
func approxTokens(text string) int {
	var cyrillic, latin, other float64
	for _, r := range text {
		switch {
		case r >= 0x0400 && r <= 0x04FF: // Cyrillic block
			cyrillic++
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') ||
			(r >= '0' && r <= '9') || r == ' ' || r == '\t' ||
			r == '\n' || r == '\r' || r == ',' || r == '.' ||
			r == '!' || r == '?' || r == ';' || r == ':' ||
			r == '\'' || r == '"' || r == '-':
			latin++
		default:
			other++
		}
	}
	return int(cyrillic/1.5 + latin/4.0 + other)
}

// truncateToTokens returns the longest prefix of text that fits within
// maxTokens (as estimated by approxTokens). UTF-8 safe; never splits a rune.
//
// Returns:
//   - truncated: the (possibly shortened) text
//   - beforeTok: token estimate of the original text
//   - afterTok:  token estimate of the returned text
//
// If the full text already fits, truncated == text and beforeTok == afterTok.
func truncateToTokens(text string, maxTokens int) (truncated string, beforeTok, afterTok int) {
	beforeTok = approxTokens(text)
	if beforeTok <= maxTokens {
		return text, beforeTok, beforeTok
	}

	// Walk rune-by-rune, accumulating characters, re-estimate after each rune.
	// Binary search would be faster but correctness matters more here.
	// For typical doc sizes (<2 KB) the linear walk is fast enough.
	end := 0
	for end < len(text) {
		_, size := utf8.DecodeRuneInString(text[end:])
		candidate := text[:end+size]
		if approxTokens(candidate) > maxTokens {
			break
		}
		end += size
	}
	truncated = text[:end]
	afterTok = approxTokens(truncated)
	return truncated, beforeTok, afterTok
}

// WithMaxTokensPerDoc enables token-aware truncation of each document text.
// 0 disables. Preferred over WithMaxCharsPerDoc for models with token budgets.
//
// If BOTH WithMaxTokensPerDoc and WithMaxCharsPerDoc are set, token truncation
// is applied first, then char truncation (rare; the token budget is tighter in
// practice).
func WithMaxTokensPerDoc(n int) Opt {
	return func(c *cfgInternal) { c.maxTokensPerDoc = n }
}
