// Package strutil provides Unicode-aware string helpers.
// Zero external dependencies.
package strutil

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Truncate caps s at maxRunes runes, appending "..." if truncated.
// Safe for UTF-8 (Cyrillic, CJK, emoji).
func Truncate(s string, maxRunes int) string {
	return TruncateWith(s, maxRunes, "...")
}

// TruncateWith is like Truncate but uses a custom placeholder.
func TruncateWith(s string, maxRunes int, placeholder string) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + placeholder
}

// TruncateAtWord truncates s to maxRunes at a word boundary.
// If the last space is too far back (< half of maxRunes), truncates at maxRunes.
// Appends "..." if truncated.
func TruncateAtWord(s string, maxRunes int) string {
	return TruncateAtWordWith(s, maxRunes, "...")
}

// TruncateAtWordWith is like TruncateAtWord but uses a custom placeholder.
func TruncateAtWordWith(s string, maxRunes int, placeholder string) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	truncated := string(runes[:maxRunes])
	cut := strings.LastIndex(truncated, " ")
	if cut < len(truncated)/2 {
		return truncated + placeholder
	}
	return truncated[:cut] + placeholder
}

// TruncateMiddle keeps the start and end of s, cutting the middle.
// Appends "..." as the middle placeholder.
// Example: TruncateMiddle("path/to/very/long/file.go", 15) → "path/to/...file.go"
func TruncateMiddle(s string, maxRunes int) string {
	return TruncateMiddleWith(s, maxRunes, "...")
}

// TruncateMiddleWith is like TruncateMiddle but uses a custom placeholder.
func TruncateMiddleWith(s string, maxRunes int, placeholder string) string {
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	if maxRunes <= 0 {
		return placeholder
	}
	head := (maxRunes + 1) / 2
	tail := maxRunes - head
	if tail <= 0 {
		return string(runes[:head]) + placeholder
	}
	return string(runes[:head]) + placeholder + string(runes[len(runes)-tail:])
}

// Contains reports whether items contains s.
func Contains(items []string, s string) bool {
	for _, item := range items {
		if item == s {
			return true
		}
	}
	return false
}

// ContainsAny reports whether s contains any of the given substrings.
func ContainsAny(s string, substrs []string) bool {
	for _, sub := range substrs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// splitWords breaks s into words by detecting case transitions and delimiters.
// Handles camelCase, PascalCase, snake_case, kebab-case, spaces, and acronyms.
func splitWords(s string) []string {
	var words []string
	runes := []rune(s)
	start := 0

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if r == '_' || r == '-' || r == ' ' {
			if i > start {
				words = append(words, string(runes[start:i]))
			}
			start = i + 1
			continue
		}

		if i > start && unicode.IsUpper(r) {
			prev := runes[i-1]
			if unicode.IsLower(prev) || unicode.IsDigit(prev) {
				words = append(words, string(runes[start:i]))
				start = i
			} else if unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
				words = append(words, string(runes[start:i]))
				start = i
			}
		}
	}
	if start < len(runes) {
		words = append(words, string(runes[start:]))
	}
	return words
}

// ToSnakeCase converts s to snake_case.
// "myVariableName" → "my_variable_name", "HTTPServer" → "http_server"
func ToSnakeCase(s string) string {
	words := splitWords(s)
	for i, w := range words {
		words[i] = strings.ToLower(w)
	}
	return strings.Join(words, "_")
}

// ToKebabCase converts s to kebab-case.
// "myVariableName" → "my-variable-name", "HTTPServer" → "http-server"
func ToKebabCase(s string) string {
	words := splitWords(s)
	for i, w := range words {
		words[i] = strings.ToLower(w)
	}
	return strings.Join(words, "-")
}

// ToCamelCase converts s to camelCase.
// "my_variable_name" → "myVariableName", "HTTPServer" → "httpServer"
func ToCamelCase(s string) string {
	words := splitWords(s)
	if len(words) == 0 {
		return ""
	}
	words[0] = strings.ToLower(words[0])
	for i := 1; i < len(words); i++ {
		words[i] = titleWord(words[i])
	}
	return strings.Join(words, "")
}

// ToPascalCase converts s to PascalCase.
// "my_variable_name" → "MyVariableName", "http_server" → "HttpServer"
func ToPascalCase(s string) string {
	words := splitWords(s)
	for i := range words {
		words[i] = titleWord(words[i])
	}
	return strings.Join(words, "")
}

// titleWord returns w with the first rune upper-cased and the rest lower-cased.
func titleWord(w string) string {
	runes := []rune(w)
	if len(runes) == 0 {
		return ""
	}
	runes[0] = unicode.ToUpper(runes[0])
	for j := 1; j < len(runes); j++ {
		runes[j] = unicode.ToLower(runes[j])
	}
	return string(runes)
}

// ContainsAll reports whether s contains all of the given substrings.
func ContainsAll(s string, substrs []string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

// Scrub returns s with invalid UTF-8 bytes replaced by the Unicode
// replacement character (U+FFFD). Returns s unchanged if already valid.
func Scrub(s string) string {
	if utf8.ValidString(s) {
		return s
	}
	return strings.ToValidUTF8(s, "\uFFFD")
}

// WordWrap wraps s at word boundaries to fit within width runes per line.
// Preserves existing newlines. Long words that exceed width are not broken.
func WordWrap(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	var sb strings.Builder
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		wrapLine(&sb, line, width)
	}
	return sb.String()
}

func wrapLine(sb *strings.Builder, line string, width int) {
	words := strings.Fields(line)
	if len(words) == 0 {
		return
	}
	lineLen := 0
	for _, word := range words {
		wordLen := len([]rune(word))
		if lineLen > 0 && lineLen+1+wordLen > width {
			sb.WriteByte('\n')
			lineLen = 0
		}
		if lineLen > 0 {
			sb.WriteByte(' ')
			lineLen++
		}
		sb.WriteString(word)
		lineLen += wordLen
	}
}

// Lang represents a detected language code.
type Lang string

const (
	LangEN Lang = "en"
	LangRU Lang = "ru"
	LangZH Lang = "zh"
	LangES Lang = "es"
)

// DetectLang detects the dominant language of text.
// Returns "ru" for Russian, "zh" for Chinese, "es" for Spanish, "en" otherwise.
// Uses character ratio analysis with a 0.3 threshold. Ported from MemDB llm.DetectLang.
func DetectLang(text string) Lang {
	var cyrillic, chinese, ascii, spanish int
	for _, r := range text {
		switch {
		case unicode.Is(unicode.Han, r):
			chinese++
		case unicode.Is(unicode.Cyrillic, r):
			cyrillic++
		case r == 'ñ' || r == 'Ñ' || r == '¿' || r == '¡' ||
			r == 'á' || r == 'é' || r == 'í' || r == 'ó' || r == 'ú' ||
			r == 'Á' || r == 'É' || r == 'Í' || r == 'Ó' || r == 'Ú' || r == 'ü' || r == 'Ü':
			spanish++
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			ascii++
		}
	}

	if ascii == 0 {
		if chinese > cyrillic && chinese > 0 {
			return LangZH
		}
		if cyrillic > 0 {
			return LangRU
		}
		return LangEN
	}

	ratio := func(count int) float64 { return float64(count) / float64(ascii) }
	if ratio(chinese) > 0.3 {
		return LangZH
	}
	if ratio(cyrillic) > 0.3 {
		return LangRU
	}
	if ratio(spanish) > 0.05 {
		return LangES
	}
	return LangEN
}
