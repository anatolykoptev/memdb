// Package langdetect provides natural language detection from text using
// trigram-based n-gram models (backed by whatlanggo). Supports 84 languages
// with confidence and reliability scoring.
//
// The package returns ISO 639-1 language codes (e.g. "en", "ru", "zh", "es")
// for easy integration with i18n systems, search backends, and LLM routing.
// For languages without an ISO 639-1 code (e.g. Cebuano, Ilocano, Saraiki),
// the ISO 639-3 code is returned instead (e.g. "ceb", "ilo", "skr").
//
// # Quick start
//
//	lang, conf := langdetect.Detect("Привет, мир!")
//	// lang == "ru", conf == 1.0
//
//	lang, conf := langdetect.Detect("你好世界")
//	// lang == "zh", conf == 1.0
//
//	info := langdetect.DetectInfo("Hello, world!")
//	// info.Lang == "en", info.Confidence == 1.0, info.Reliable == true
//
// # Confidence and reliability
//
// Confidence is a float in [0, 1] indicating how sure the detector is.
// Reliability is a boolean: true when confidence > 0.8 (whatlanggo's
// ReliableConfidenceThreshold). Use reliability to gate language-specific
// processing — if the detector is unsure, fall back to a neutral default.
//
//	info := langdetect.DetectInfo(text)
//	if info.Reliable {
//	    applyLanguageSpecificLogic(info.Lang)
//	} else {
//	    applyNeutralFallback()
//	}
//
// # Whitelist / blacklist
//
// For use cases that only care about a subset of languages (e.g. a search
// backend that only has stopword lists for en/ru/zh), use a whitelist to
// restrict detection and improve accuracy:
//
//	opts := langdetect.Options{
//	    Whitelist: []string{"en", "ru", "zh"},
//	}
//	lang, _ := langdetect.DetectWith("some text", opts)
package langdetect

import (
	"github.com/RadhiFadlillah/whatlanggo"
)

// Lang is an ISO 639-1 (or ISO 639-3 fallback) language code.
type Lang string

const (
	// LangUnknown is returned when detection fails or text is empty.
	LangUnknown Lang = ""
	// LangEN is English.
	LangEN Lang = "en"
	// LangRU is Russian.
	LangRU Lang = "ru"
	// LangZH is Chinese (Mandarin).
	LangZH Lang = "zh"
	// LangES is Spanish.
	LangES Lang = "es"
	// LangDE is German.
	LangDE Lang = "de"
	// LangFR is French.
	LangFR Lang = "fr"
	// LangJA is Japanese.
	LangJA Lang = "ja"
	// LangKO is Korean.
	LangKO Lang = "ko"
	// LangPT is Portuguese.
	LangPT Lang = "pt"
	// LangIT is Italian.
	LangIT Lang = "it"
	// LangTR is Turkish.
	LangTR Lang = "tr"
	// LangUK is Ukrainian.
	LangUK Lang = "uk"
	// LangPL is Polish.
	LangPL Lang = "pl"
	// LangNL is Dutch.
	LangNL Lang = "nl"
	// LangAR is Arabic.
	LangAR Lang = "ar"
	// LangHI is Hindi.
	LangHI Lang = "hi"
	// LangFA is Persian.
	LangFA Lang = "fa"
	// LangVI is Vietnamese.
	LangVI Lang = "vi"
	// LangTH is Thai.
	LangTH Lang = "th"
	// LangID is Indonesian.
	LangID Lang = "id"
)

// Info is the full outcome of language detection.
type Info struct {
	// Lang is the detected language as an ISO 639-1 code (or ISO 639-3
	// fallback for languages without a 639-1 code). LangUnknown if
	// detection failed.
	Lang Lang
	// Confidence is a float in [0, 1] indicating detection certainty.
	Confidence float64
	// Reliable is true when Confidence > 0.8 (whatlanggo's threshold).
	// Use this to gate language-specific processing.
	Reliable bool
	// Name is the human-readable language name (e.g. "Russian", "Mandarin").
	Name string
}

// Options configures language detection. Zero value = detect all 84 languages.
type Options struct {
	// Whitelist restricts detection to the listed ISO 639-1/639-3 codes.
	// If non-empty, only these languages are considered. Improves accuracy
	// when the use case only involves a known subset of languages.
	Whitelist []string
	// Blacklist excludes the listed ISO 639-1/639-3 codes from detection.
	// Ignored if Whitelist is non-empty.
	Blacklist []string
}

// Detect detects the language of text and returns the ISO 639-1 code
// (or ISO 639-3 fallback) and confidence. Returns (LangUnknown, 0) for
// empty or undetectable text.
func Detect(text string) (Lang, float64) {
	info := DetectInfo(text)
	return info.Lang, info.Confidence
}

// DetectInfo detects the language of text and returns full detection info
// including confidence, reliability, and human-readable name.
// Returns Info{Lang: LangUnknown} for empty or undetectable text.
func DetectInfo(text string) Info {
	return DetectWith(text, Options{})
}

// DetectWith detects the language of text using the provided options
// and returns full detection info.
func DetectWith(text string, opts Options) Info {
	if text == "" {
		return Info{}
	}

	wlOpts := whatlanggo.Options{}
	if len(opts.Whitelist) > 0 {
		wlOpts.Whitelist = codesToLangs(opts.Whitelist)
	} else if len(opts.Blacklist) > 0 {
		wlOpts.Blacklist = codesToLangs(opts.Blacklist)
	}

	raw := whatlanggo.DetectWithOptions(text, wlOpts)
	if raw.Lang < 0 {
		return Info{}
	}

	code := raw.Lang.Iso6391()
	if code == "" {
		code = raw.Lang.Iso6393()
	}

	return Info{
		Lang:       Lang(code),
		Confidence: raw.Confidence,
		Reliable:   raw.IsReliable(),
		Name:       raw.Lang.String(),
	}
}

// IsReliable is a convenience function that returns true if the detected
// language has confidence > 0.8. Use this for quick gating without
// inspecting the full Info struct.
func IsReliable(text string) bool {
	return DetectInfo(text).Reliable
}

// codesToLangs converts a list of ISO 639-1 or ISO 639-3 codes to
// whatlanggo Lang enums. Unknown codes are silently skipped.
func codesToLangs(codes []string) map[whatlanggo.Lang]bool {
	out := make(map[whatlanggo.Lang]bool, len(codes))
	for _, code := range codes {
		// Try ISO 639-3 first (whatlanggo's native format).
		lang := whatlanggo.CodeToLang(code)
		if lang >= 0 {
			out[lang] = true
			continue
		}
		// Try ISO 639-1 → ISO 639-3 mapping.
		iso3 := iso6391To6393(code)
		if iso3 != "" {
			lang = whatlanggo.CodeToLang(iso3)
			if lang >= 0 {
				out[lang] = true
			}
		}
	}
	return out
}

// iso6391To6393 maps ISO 639-1 codes to ISO 639-3 codes for whatlanggo's
// CodeToLang function. Only covers the 84 languages whatlanggo supports.
func iso6391To6393(code string) string {
	m := map[string]string{
		"af": "afr", "ak": "aka", "am": "amh", "ar": "arb", "az": "azj",
		"be": "bel", "bn": "ben", "bh": "bho", "bg": "bul", "cs": "ces",
		"zh": "cmn", "da": "dan", "de": "deu", "el": "ell", "en": "eng",
		"eo": "epo", "et": "est", "fi": "fin", "fr": "fra", "gu": "guj",
		"ht": "hat", "ha": "hau", "he": "heb", "hi": "hin", "hr": "hrv",
		"hu": "hun", "ig": "ibo", "id": "ind", "it": "ita", "jv": "jav",
		"ja": "jpn", "kn": "kan", "ka": "kat", "km": "khm", "rw": "kin",
		"ko": "kor", "ku": "kur", "lv": "lav", "lt": "lit", "ml": "mal",
		"mr": "mar", "mk": "mkd", "mg": "mlg", "my": "mya", "ne": "nep",
		"nl": "nld", "nn": "nno", "nb": "nob", "ny": "nya", "or": "ori",
		"om": "orm", "pa": "pan", "fa": "pes", "pl": "pol", "pt": "por",
		"ro": "ron", "rn": "run", "ru": "rus", "si": "sin", "sl": "slv",
		"sn": "sna", "so": "som", "es": "spa", "sr": "srp", "sv": "swe",
		"ta": "tam", "te": "tel", "tl": "tgl", "th": "tha", "ti": "tir",
		"tk": "tuk", "tr": "tur", "ug": "uig", "uk": "ukr", "ur": "urd",
		"uz": "uzb", "vi": "vie", "yo": "yor", "zu": "zul",
	}
	return m[code]
}
