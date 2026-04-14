// Package llm — language detection for memory extraction.
package llm

import (
	"regexp"
	"unicode"
)

// Lang represents a detected language.
type Lang string

const (
	LangEN Lang = "en"
	LangZH Lang = "zh"
	LangRU Lang = "ru"
)

var (
	rolePrefixRe = regexp.MustCompile(`(?m)^(user|assistant|system|tool)\s*:\s*`)
	timestampRe  = regexp.MustCompile(`\[\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2})?\]`)
	urlRe        = regexp.MustCompile(`https?://\S+`)
)

// DetectLang detects the dominant language of a conversation text.
// Returns "zh" for Chinese, "ru" for Russian, "en" otherwise.
//
// Algorithm (port of Python detect_lang):
//  1. Strip role prefixes, timestamps, URLs
//  2. Count CJK chars, Cyrillic chars, ASCII letters
//  3. Ratio-based detection with 0.3 threshold
func DetectLang(text string) Lang {
	cleaned := rolePrefixRe.ReplaceAllString(text, "")
	cleaned = timestampRe.ReplaceAllString(cleaned, "")
	cleaned = urlRe.ReplaceAllString(cleaned, "")

	var chineseCount, asciiCount, cyrillicCount int
	for _, r := range cleaned {
		switch {
		case unicode.Is(unicode.Han, r):
			chineseCount++
		case unicode.Is(unicode.Cyrillic, r):
			cyrillicCount++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || r == '_':
			asciiCount++
		}
	}

	if asciiCount == 0 {
		if chineseCount > cyrillicCount && chineseCount > 0 {
			return LangZH
		}
		if cyrillicCount > 0 {
			return LangRU
		}
		return LangEN
	}

	ratio := func(count int) float64 {
		return float64(count) / float64(asciiCount)
	}
	if ratio(chineseCount) > 0.3 {
		return LangZH
	}
	if ratio(cyrillicCount) > 0.3 {
		return LangRU
	}
	return LangEN
}
