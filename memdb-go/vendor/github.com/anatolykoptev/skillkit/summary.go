package skillkit

import (
	"encoding/json"
	"encoding/xml"
	"log/slog"
	"strings"
)

// SummaryFormat selects the output format for BuildSummary.
type SummaryFormat int

const (
	// SummaryXML renders skills as an XML document with optional plugin grouping.
	SummaryXML SummaryFormat = iota
	// SummaryMarkdown renders skills as Markdown headings with truncated descriptions.
	SummaryMarkdown
	// SummaryJSON renders skills as a JSON array of SkillInfo values (full description).
	SummaryJSON
)

const descTruncLen = 180 // characters for XML/Markdown truncation

// BuildSummary returns a formatted summary of all skills in the catalog.
//
// SummaryXML: escapes <, >, & in all string fields. Skills from plugin
// tiers (Source contains ":") are grouped under <skill-group plugin="...">.
// Descriptions are truncated to 180 chars.
//
// SummaryMarkdown: "### <name>\n<description truncated 180>\n" per skill.
//
// SummaryJSON: full JSON array of SkillInfo (no truncation).
//
// Empty Catalog returns "" for all formats.
func (c *Catalog) BuildSummary(format SummaryFormat) string {
	skills := c.List()
	if len(skills) == 0 {
		return ""
	}
	switch format {
	case SummaryMarkdown:
		return buildMarkdown(skills)
	case SummaryJSON:
		return buildJSON(skills)
	default: // SummaryXML
		return buildXML(skills)
	}
}

// buildXML renders the XML summary, grouping plugin skills.
func buildXML(skills []SkillInfo) string {
	var b strings.Builder
	b.WriteString("<skills>")

	// Separate plugin skills from plain skills.
	// Plugin skills: Source contains ":"
	// Plain skills: everything else.
	// Process plain skills first (in sorted order), then plugin groups.

	// Use insertion-ordered map via slice.
	var plain []SkillInfo
	pluginOrder := []string{}
	pluginMap := make(map[string][]SkillInfo)

	for i := range skills {
		si := skills[i]
		if isPluginSource(si.Source) {
			// Source format: "<tier>:<plugin>"
			plugin := si.Source[strings.Index(si.Source, ":")+1:]
			if _, exists := pluginMap[plugin]; !exists {
				pluginOrder = append(pluginOrder, plugin)
			}
			pluginMap[plugin] = append(pluginMap[plugin], si)
		} else {
			plain = append(plain, si)
		}
	}

	for _, si := range plain {
		writeXMLSkill(&b, si)
	}

	for _, plugin := range pluginOrder {
		b.WriteString(`<skill-group plugin="`)
		b.WriteString(xmlEscape(plugin))
		b.WriteString(`">`)
		for _, si := range pluginMap[plugin] {
			writeXMLSkill(&b, si)
		}
		b.WriteString("</skill-group>")
	}

	b.WriteString("</skills>")
	return b.String()
}

// writeXMLSkill appends a single <skill ...> element to b.
func writeXMLSkill(b *strings.Builder, si SkillInfo) {
	desc := truncate(si.Description, descTruncLen)
	b.WriteString(`<skill name="`)
	b.WriteString(xmlEscape(si.Name))
	if si.Path != "" {
		b.WriteString(`" path="`)
		b.WriteString(xmlEscape(si.Path))
	}
	if si.Source != "" {
		b.WriteString(`" source="`)
		b.WriteString(xmlEscape(si.Source))
	}
	b.WriteString(`">`)
	b.WriteString("<description>")
	b.WriteString(xmlEscape(desc))
	b.WriteString("</description>")
	b.WriteString("</skill>")
}

// buildMarkdown renders the Markdown summary.
func buildMarkdown(skills []SkillInfo) string {
	var b strings.Builder
	for i, si := range skills {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("### ")
		b.WriteString(si.Name)
		b.WriteByte('\n')
		desc := truncate(si.Description, descTruncLen)
		b.WriteString(desc)
		b.WriteByte('\n')
	}
	return b.String()
}

// buildJSON renders the JSON summary (full description, no truncation).
func buildJSON(skills []SkillInfo) string {
	data, err := json.Marshal(skills)
	if err != nil {
		slog.Warn("skillkit.BuildSummary: JSON marshal failed", "err", err, "count", len(skills))
		return "[]"
	}
	return string(data)
}

// xmlEscape escapes a string for safe inclusion in XML text or attribute
// content. Uses encoding/xml.EscapeText for full coverage (& < > " ' plus
// whitespace control characters).
func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// truncate returns s truncated to n characters (rune-counted).
// Returns s unchanged if len(s) <= n.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}
