package handlers

import "github.com/anatolykoptev/memdb/memdb-go/internal/lang"

// detectLang is preserved as a package-private wrapper for existing
// callers in chat_prompt.go and similar. The actual implementation
// lives in internal/lang.Detect to allow internal/search to use it
// without a circular import on internal/handlers.
func detectLang(text string) string {
	return lang.Detect(text)
}
