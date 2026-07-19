package handlers

import "github.com/anatolykoptev/go-kit/langdetect"

// chatLangOpts restricts detection to the languages we have skill prompts
// for. This improves accuracy on short texts (e.g. "Привет мир" → "ru"
// not "uk") and eliminates false matches against the other 81 languages.
// Expand the whitelist when new locale-specific skill prompts are added.
var chatLangOpts = langdetect.Options{
	Whitelist: []string{"en", "ru", "zh"},
}

// detectLang classifies text into a language code ("en", "ru", or "zh")
// using trigram-based language detection. Used by chat prompt selection
// to pick the locale-appropriate system prompt.
//
// Returns "en" for empty or undetectable text. To add support for more
// languages, add skill prompts to d10SkillsByLocale and expand the
// whitelist in chatLangOpts.
func detectLang(text string) string {
	info := langdetect.DetectWith(text, chatLangOpts)
	if info.Lang == langdetect.LangUnknown {
		return "en"
	}
	return string(info.Lang)
}
