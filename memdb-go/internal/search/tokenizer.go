// Package search provides fulltext search utilities for PostgreSQL tsquery construction.
// This is a Go port of Python's FastTokenizer.tokenize_mixed() from retrieve_utils.py.
package search

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// tokenRegexp extracts word tokens: ASCII letters, Cyrillic letters, and digits.
// Matches the Python regex [a-zA-ZА-Яа-яёЁ0-9]+
var tokenRegexp = regexp.MustCompile(`[a-zA-ZА-Яа-яёЁ0-9]+`)

// numericRegexp matches tokens that are purely numeric.
var numericRegexp = regexp.MustCompile(`^[0-9]+$`)

const (
	// cyrillicLangThreshold is the minimum ratio of Cyrillic characters required
	// to classify text as Russian (ru).
	cyrillicLangThreshold = 0.20

	// cjkLangThreshold is the minimum ratio of CJK characters required
	// to classify text as Chinese (zh).
	cjkLangThreshold = 0.30
)

// TokenizeMixed is the main entry point. It detects the language of the input text,
// extracts tokens using a shared regex, lowercases them, and filters stopwords
// based on the detected language.
func TokenizeMixed(text string) []string {
	lang := detectLang(text)

	matches := tokenRegexp.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}

	var stopwords map[string]bool
	switch lang {
	case "ru":
		stopwords = russianStopwords
	case "zh":
		stopwords = chineseStopwords
	default:
		stopwords = englishStopwords
	}

	tokens := make([]string, 0, len(matches))
	for _, m := range matches {
		tok := strings.ToLower(m)

		// Filter single-character tokens.
		if utf8.RuneCountInString(tok) <= 1 {
			continue
		}

		// Filter pure numeric tokens.
		if numericRegexp.MatchString(tok) {
			continue
		}

		// Filter stopwords.
		if stopwords[tok] {
			continue
		}

		tokens = append(tokens, tok)
	}

	return tokens
}

// BuildTSQuery joins tokens with " | " for PostgreSQL tsquery (OR semantics).
// Returns an empty string if tokens is empty.
func BuildTSQuery(tokens []string) string {
	if len(tokens) == 0 {
		return ""
	}
	return strings.Join(tokens, " | ")
}

// isCyrillic returns true if the rune is in the Cyrillic Unicode block (U+0400-U+04FF).
func isCyrillic(r rune) bool {
	return r >= 0x0400 && r <= 0x04FF
}

// isCJK returns true if the rune is in the CJK Unified Ideographs block (U+4E00-U+9FFF).
func isCJK(r rune) bool {
	return r >= 0x4E00 && r <= 0x9FFF
}

// detectLang analyzes the text and returns a language code:
//   - "ru" if >20% of characters are Cyrillic
//   - "zh" if >30% of characters are CJK
//   - "en" otherwise
func detectLang(text string) string {
	var total, cyrillic, cjk int

	for _, r := range text {
		if unicode.IsSpace(r) || unicode.IsPunct(r) {
			continue
		}
		total++
		if isCyrillic(r) {
			cyrillic++
		}
		if isCJK(r) {
			cjk++
		}
	}

	if total == 0 {
		return "en"
	}

	cyrillicRatio := float64(cyrillic) / float64(total)
	cjkRatio := float64(cjk) / float64(total)

	// Check Cyrillic first (>cyrillicLangThreshold).
	if cyrillicRatio > cyrillicLangThreshold {
		return "ru"
	}

	// Check CJK (>cjkLangThreshold).
	if cjkRatio > cjkLangThreshold {
		return "zh"
	}

	return "en"
}

// englishStopwords contains ~100 common English stopwords.
var englishStopwords = map[string]bool{
	"the": true, "an": true, "is": true, "are": true,
	"was": true, "were": true, "be": true, "been": true, "being": true,
	"have": true, "has": true, "had": true, "do": true, "does": true,
	"did": true, "will": true, "would": true, "shall": true, "should": true,
	"can": true, "could": true, "may": true, "might": true, "must": true,
	"am": true, "me": true, "my": true, "we": true, "our": true,
	"you": true, "your": true, "he": true, "him": true, "his": true,
	"she": true, "her": true, "it": true, "its": true, "they": true,
	"them": true, "their": true, "this": true, "that": true, "these": true,
	"those": true, "what": true, "which": true, "who": true, "whom": true,
	"whose": true, "when": true, "where": true, "why": true, "how": true,
	"all": true, "each": true, "every": true, "both": true, "few": true,
	"more": true, "most": true, "other": true, "some": true, "such": true,
	"no": true, "not": true, "only": true, "same": true, "so": true,
	"than": true, "too": true, "very": true, "just": true, "but": true,
	"and": true, "or": true, "if": true, "then": true, "for": true,
	"of": true, "to": true, "in": true, "on": true, "at": true,
	"by": true, "with": true, "from": true, "up": true, "about": true,
	"into": true, "through": true, "during": true, "before": true, "after": true,
	"above": true, "below": true, "between": true, "out": true, "off": true,
	"over": true, "under": true, "again": true, "further": true, "once": true,
	"here": true, "there": true, "any": true, "as": true, "because": true,
	"until": true, "while": true, "also": true, "still": true, "already": true,
	"even": true, "now": true, "re": true, "ll": true, "ve": true,
}

// russianStopwords contains ~60 common Russian stopwords.
var russianStopwords = map[string]bool{
	"и": true, "в": true, "на": true, "с": true, "по": true,
	"не": true, "что": true, "это": true, "как": true, "а": true,
	"то": true, "все": true, "он": true, "она": true, "мы": true,
	"вы": true, "они": true, "но": true, "за": true, "от": true,
	"до": true, "из": true, "у": true, "при": true, "для": true,
	"о": true, "об": true, "так": true, "же": true, "ни": true,
	"ну": true, "бы": true, "да": true, "нет": true, "его": true,
	"её": true, "их": true, "мне": true, "мой": true, "мои": true,
	"моя": true, "моё": true, "наш": true, "ваш": true, "свой": true,
	"кто": true, "где": true, "когда": true, "уже": true, "ещё": true,
	"тоже": true, "также": true, "быть": true, "был": true, "была": true,
	"было": true, "были": true, "будет": true, "есть": true, "нас": true,
	"вас": true, "себя": true, "там": true, "тут": true, "сам": true,
	"вот": true, "этот": true, "тот": true, "который": true,
}

// chineseStopwords contains ~55 common Chinese stopwords.
var chineseStopwords = map[string]bool{
	"的": true, "了": true, "在": true, "是": true, "我": true,
	"有": true, "和": true, "就": true, "不": true, "人": true,
	"都": true, "一": true, "个": true, "上": true, "也": true,
	"很": true, "到": true, "说": true, "要": true, "去": true,
	"你": true, "会": true, "着": true, "没有": true, "看": true,
	"好": true, "来": true, "对": true, "他": true, "她": true,
	"它": true, "这": true, "那": true, "些": true, "们": true,
	"把": true, "被": true, "让": true, "给": true, "用": true,
	"与": true, "及": true, "等": true, "可以": true, "已经": true,
	"因为": true, "所以": true, "但是": true, "如果": true, "虽然": true,
	"或者": true, "而且": true, "以及": true, "还是": true, "就是": true,
}
