package skills

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// StripFrontmatter removes a leading YAML or JSON frontmatter block
// and returns the body trimmed of leading whitespace (spaces, tabs,
// and newlines). Input without a recognized fence is returned
// unchanged. Supports YAML ("---" fences) and JSON (top-level object
// on first line, blank line, body).
//
// CRLF input is normalized to LF before parsing. The "unchanged"
// guarantee covers byte-identical input only for content that does NOT
// start with '-' or '{'; once a fence is detected and parsing fails
// (e.g. "---\r\n" with no closing fence), the returned string is the
// CRLF-normalized form. NUL bytes inside the body are silently
// stripped.
func StripFrontmatter(content string) string {
	_, body := ParseFrontmatter(content)
	return body
}

// ParseFrontmatter splits content into (frontmatter, body). Returns
// ("", content) when no fence is found.
func ParseFrontmatter(content string) (frontmatter, body string) {
	// Fast path: no allocations if no fence detected.
	if len(content) == 0 {
		return "", content
	}
	first := content[0]
	if first != '-' && first != '{' {
		return "", content
	}

	// Normalize CRLF once before scanning.
	normalized := strings.ReplaceAll(content, "\r\n", "\n")

	if normalized[0] == '{' {
		return parseJSONFrontmatter(normalized)
	}
	return parseYAMLFrontmatter(normalized)
}

// nextLine splits the first line off s. The line excludes its "\n" and one
// trailing "\r" (the same lines bufio.ScanLines yields); a final line without
// a newline counts, an empty remainder does not (ok=false).
//
// Lines are split by hand instead of with bufio.Scanner on purpose: a
// Scanner caps the line length and, once a line exceeds the cap, stops without
// any signal the caller here could see, silently dropping the rest of the
// skill. A string already in memory has no use for such a limit.
func nextLine(s string) (line, rest string, ok bool) {
	if s == "" {
		return "", "", false
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		line, rest = s[:i], s[i+1:]
	} else {
		line = s
	}
	return strings.TrimSuffix(line, "\r"), rest, true
}

// parseYAMLFrontmatter handles "---\n...\n---\nbody" format.
func parseYAMLFrontmatter(content string) (frontmatter, body string) {
	// First line must be exactly "---".
	line, rest, ok := nextLine(content)
	if !ok || line != "---" {
		return "", content
	}

	var fm strings.Builder
	for {
		line, rest, ok = nextLine(rest)
		if !ok {
			break
		}
		if line == "---" {
			return fm.String(), collectBody(rest)
		}
		if fm.Len() > 0 {
			fm.WriteByte('\n')
		}
		fm.WriteString(line)
	}

	// No closing fence found — return content unchanged.
	return "", content
}

// parseJSONFrontmatter handles "{...}\n\nbody" format.
//
// Uses json.Decoder + InputOffset so that literal { or } inside JSON
// string values do not confuse the detector. Trailing non-whitespace
// on the same line as the closing brace (e.g. `{"k":"v"}garbage`)
// causes ("", original) to be returned.
func parseJSONFrontmatter(content string) (frontmatter, body string) {
	dec := json.NewDecoder(strings.NewReader(content))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return "", content
	}
	end := int(dec.InputOffset())
	fm := strings.TrimSpace(string(raw))

	rest := content[end:]
	// Anything between end and the first newline must be only whitespace.
	nl := strings.IndexByte(rest, '\n')
	if nl < 0 {
		return "", content
	}
	if strings.TrimSpace(rest[:nl]) != "" {
		return "", content
	}
	rest = rest[nl+1:]

	// Next line must be blank (the separator).
	nl2 := strings.IndexByte(rest, '\n')
	if nl2 < 0 {
		// No trailing newline: rest is the separator line itself.
		// It must be blank; if so, body is empty. If not, fail.
		if strings.TrimSpace(rest) != "" {
			return "", content
		}
		return fm, ""
	}
	if strings.TrimSpace(rest[:nl2]) != "" {
		return "", content
	}
	body = stripNUL(strings.TrimLeft(rest[nl2+1:], " \t\n"))
	return fm, body
}

// collectBody turns the text after the closing fence into a NUL-free body
// string with leading whitespace (spaces, tabs, newlines) stripped per the
// spec. Lines are re-joined with "\n" and a single trailing newline is dropped.
func collectBody(rest string) string {
	var bodyLines []string
	for {
		line, next, ok := nextLine(rest)
		if !ok {
			break
		}
		bodyLines = append(bodyLines, line)
		rest = next
	}
	return stripNUL(strings.TrimLeft(strings.Join(bodyLines, "\n"), " \t\n"))
}

// stripNUL removes NUL bytes from s so skill bodies with embedded NULs are
// sanitized before they reach a prompt.
func stripNUL(s string) string {
	if !strings.ContainsRune(s, 0) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r != 0 {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}
