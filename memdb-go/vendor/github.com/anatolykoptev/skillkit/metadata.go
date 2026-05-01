package skillkit

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

const (
	descriptionMaxLen  = 1024
	nameMaxLen         = 64
	metadataIndent     = 2  // minimum spaces for `metadata:` sub-keys
	maxTagsComma       = 64 // sanity cap for comma-split
)

// nameRE enforces agentskills.io name rules: lowercase alphanumeric and
// hyphens, no leading/trailing/consecutive hyphens, 1–64 chars.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// Metadata holds parsed frontmatter fields covering the agentskills.io
// standard plus Claude Code extensions.
type Metadata struct {
	// Required by spec
	Name        string
	Description string

	// agentskills.io optional standard fields
	License       string
	Compatibility string

	// Claude Code extensions (load-bearing for Skill auto-trigger)
	WhenToUse              string   // appended to Description for discovery
	AllowedTools           []string
	DisableModelInvocation bool
	UserInvocable          *bool // nil = default (true)

	// skillkit custom (widely useful, not in spec)
	Version string
	Locale  string
	Tags    []string

	// Top-level non-standard YAML keys + agentskills.io `metadata:` map
	Extra map[string]string
}

// ParseMetadata parses YAML or JSON frontmatter into Metadata. JSON
// when frontmatter starts with "{". The YAML parser is intentionally
// minimal: top-level scalars, quoted strings, flow lists for tags
// and allowed-tools, comments and blanks ignored. Nested maps NOT
// supported except `metadata:` whose top-level keys are flattened
// into Extra. allowed-tools also accepts space-separated string per
// spec. Unknown formats yield an empty Metadata (no error).
func ParseMetadata(content string) Metadata {
	fm, _ := ParseFrontmatter(content)
	if fm == "" {
		return Metadata{}
	}
	fm = strings.TrimSpace(fm)
	if fm == "" {
		return Metadata{}
	}
	if strings.HasPrefix(fm, "{") {
		return parseJSONMetadata(fm)
	}
	return parseYAMLMetadata(fm)
}

// ValidateName enforces agentskills.io spec rules:
//   - 1–64 characters
//   - lowercase ASCII alphanumeric and hyphens only
//   - no leading or trailing hyphen
//   - no consecutive hyphens
//
// dirName is the on-disk directory name; spec mandates it matches
// name. Pass "" to skip the directory-name check (used by Embedded).
func ValidateName(name, dirName string) error {
	if name == "" {
		return errors.New("skill name must not be empty")
	}
	if len(name) > nameMaxLen {
		return fmt.Errorf("skill name %q exceeds %d character limit", name, nameMaxLen)
	}
	if !nameRE.MatchString(name) {
		return fmt.Errorf("skill name %q is invalid: must be lowercase alphanumeric and hyphens, no leading/trailing/consecutive hyphens", name)
	}
	// LOAD-BEARING: nameRE allows "foo--bar" because the middle group
	// `[a-z0-9-]*` does not forbid consecutive hyphens — only the
	// trailing `[a-z0-9]` anchor blocks "--" at the very end. This
	// explicit Contains check is the primary guard against consecutive
	// hyphens; do NOT remove it as "redundant".
	if strings.Contains(name, "--") {
		return fmt.Errorf("skill name %q contains consecutive hyphens", name)
	}
	if dirName != "" && dirName != name {
		return fmt.Errorf("skill name %q does not match directory name %q", name, dirName)
	}
	return nil
}

// ---- JSON path ----

// jsonMetadata is the typed helper for encoding/json Unmarshal.
type jsonMetadata struct {
	Name                   string            `json:"name"`
	Description            string            `json:"description"`
	License                string            `json:"license"`
	Compatibility          string            `json:"compatibility"`
	WhenToUse              string            `json:"when_to_use"`
	AllowedTools           []string          `json:"allowed-tools"`
	DisableModelInvocation bool              `json:"disable-model-invocation"`
	UserInvocable          *bool             `json:"user-invocable"`
	Version                string            `json:"version"`
	Locale                 string            `json:"locale"`
	Tags                   []string          `json:"tags"`
	Extra                  map[string]string `json:"metadata"`
}

func parseJSONMetadata(fm string) Metadata {
	var jm jsonMetadata
	if err := json.Unmarshal([]byte(fm), &jm); err != nil {
		return Metadata{}
	}
	m := jsonToMetadata(jm)
	warnLongDescription(m.Description)
	return m
}

// jsonToMetadata converts the JSON helper struct to Metadata.
// Go allows direct conversion between structs with identical field
// sets regardless of struct tags.
func jsonToMetadata(jm jsonMetadata) Metadata {
	return Metadata(jm)
}

// ---- YAML path ----

func parseYAMLMetadata(fm string) Metadata {
	m := Metadata{}
	scanner := bufio.NewScanner(strings.NewReader(fm))
	scanner.Buffer(make([]byte, scannerBufSize), scannerBufSize)

	inMetadataBlock := false

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		inMetadataBlock = processYAMLLine(&m, line, trimmed, inMetadataBlock)
	}

	warnLongDescription(m.Description)
	return m
}

// processYAMLLine handles a single frontmatter line and returns the updated
// inMetadataBlock flag.
func processYAMLLine(m *Metadata, line, trimmed string, inMetadataBlock bool) bool {
	// Blank line ends the metadata block; comments are always skipped.
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "#") {
		return inMetadataBlock
	}

	// `metadata:` block header.
	if trimmed == "metadata:" {
		return true
	}

	// Sub-key under `metadata:` block (indented ≥ metadataIndent spaces).
	if inMetadataBlock && strings.HasPrefix(line, strings.Repeat(" ", metadataIndent)) {
		addExtraKV(m, trimmed)
		return true
	}

	// Top-level key: value (exits metadata block if active).
	key, val, ok := splitYAMLKV(trimmed)
	if ok {
		applyYAMLField(m, key, val)
	}
	return false
}

// addExtraKV parses a trimmed "key: value" line and adds it to m.Extra.
func addExtraKV(m *Metadata, trimmed string) {
	subKey, subVal, ok := splitYAMLKV(trimmed)
	if !ok {
		return
	}
	if m.Extra == nil {
		m.Extra = make(map[string]string)
	}
	m.Extra[subKey] = subVal
}

// applyYAMLField maps a YAML key/value to the Metadata struct.
func applyYAMLField(m *Metadata, key, val string) {
	switch key {
	case "name":
		m.Name = val
	case "description":
		m.Description = val
	case "license":
		m.License = val
	case "compatibility":
		m.Compatibility = val
	case "when_to_use":
		m.WhenToUse = val
	case "version":
		m.Version = val
	case "locale":
		m.Locale = val
	case "allowed-tools":
		m.AllowedTools = parseToolList(val)
	case "disable-model-invocation":
		m.DisableModelInvocation = parseBoolYAML(val)
	case "user-invocable":
		b := parseBoolYAML(val)
		m.UserInvocable = &b
	case "tags":
		m.Tags = parseTagList(val)
	default:
		// Unknown top-level key → Extra map.
		if m.Extra == nil {
			m.Extra = make(map[string]string)
		}
		m.Extra[key] = val
	}
}

// splitYAMLKV splits "key: value" into (key, value, true). Returns
// (_, _, false) when the line is not a valid key-value pair.
func splitYAMLKV(line string) (key, val string, ok bool) {
	idx := strings.Index(line, ":")
	if idx < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:idx])
	val = strings.TrimSpace(line[idx+1:])
	val = unquoteYAML(val)
	return key, val, key != ""
}

// unquoteYAML strips surrounding double or single quotes from a YAML
// scalar value. Escaped quotes inside are not further processed
// (sufficient for agentskills.io spec values).
func unquoteYAML(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') ||
			(s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// parseBoolYAML returns true for "true"/"yes"/"on"/"1".
func parseBoolYAML(s string) bool {
	switch strings.ToLower(s) {
	case "true", "yes", "on", "1":
		return true
	}
	return false
}

// parseToolList parses allowed-tools: either a YAML flow list [A, B]
// or a space-separated string. Note: quoted multi-word tool names
// (e.g. "Bash with space") are not supported in space-separated form;
// use the flow list syntax ([Read, "Bash with space"]) instead.
func parseToolList(val string) []string {
	if strings.HasPrefix(val, "[") {
		return parseFlowList(val)
	}
	// Space-separated.
	parts := strings.Fields(val)
	if len(parts) == 0 {
		return nil
	}
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		result = append(result, unquoteYAML(p))
	}
	return result
}

// parseTagList parses tags: either a YAML flow list [a, b] or a
// comma-separated string.
func parseTagList(val string) []string {
	if strings.HasPrefix(val, "[") {
		return parseFlowList(val)
	}
	// Comma-separated.
	raw := strings.Split(val, ",")
	result := make([]string, 0, len(raw))
	for i, part := range raw {
		if i >= maxTagsComma {
			break
		}
		t := unquoteYAML(strings.TrimSpace(part))
		if t != "" {
			result = append(result, t)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// parseFlowList parses a YAML flow sequence: [item1, item2, ...].
// Quotes around individual elements are stripped via unquoteYAML.
func parseFlowList(val string) []string {
	val = strings.TrimSpace(val)
	if !strings.HasPrefix(val, "[") || !strings.HasSuffix(val, "]") {
		return nil
	}
	inner := val[1 : len(val)-1]
	if inner == "" {
		return nil
	}
	parts := strings.Split(inner, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		t := unquoteYAML(strings.TrimSpace(p))
		if t != "" {
			result = append(result, t)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func warnLongDescription(desc string) {
	if len(desc) > descriptionMaxLen {
		slog.Debug("skill description exceeds 1024 chars",
			"len", len(desc))
	}
}
