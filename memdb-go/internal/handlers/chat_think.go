package handlers

// chat_think.go — <think>...</think> tag parser for streaming LLM responses.
// Handles partial tags arriving across chunk boundaries.

import "strings"

const (
	thinkOpen  = "<think>"
	thinkClose = "</think>"
)

// chatSegment is a classified piece of streaming text.
type chatSegment struct {
	Text      string
	Reasoning bool // true if inside <think> block
}

// thinkParser tracks <think>...</think> boundaries in streaming text.
type thinkParser struct {
	inThink bool
	buf     strings.Builder
}

// Feed processes incoming text and returns classified segments.
// Retains partial tag text in the internal buffer for the next call.
func (p *thinkParser) Feed(text string) []chatSegment {
	p.buf.WriteString(text)
	s := p.buf.String()
	p.buf.Reset()

	var segments []chatSegment

	for len(s) > 0 {
		if p.inThink {
			idx := strings.Index(s, thinkClose)
			if idx < 0 {
				// Check for partial close tag at end.
				if partial := longestSuffix(s, thinkClose); partial > 0 {
					if idx := len(s) - partial; idx > 0 {
						segments = append(segments, chatSegment{Text: s[:idx], Reasoning: true})
					}
					p.buf.WriteString(s[len(s)-partial:])
				} else {
					segments = append(segments, chatSegment{Text: s, Reasoning: true})
				}
				return segments
			}
			if idx > 0 {
				segments = append(segments, chatSegment{Text: s[:idx], Reasoning: true})
			}
			p.inThink = false
			s = s[idx+len(thinkClose):]
		} else {
			idx := strings.Index(s, thinkOpen)
			if idx < 0 {
				// Check for partial open tag at end.
				if partial := longestSuffix(s, thinkOpen); partial > 0 {
					if idx := len(s) - partial; idx > 0 {
						segments = append(segments, chatSegment{Text: s[:idx], Reasoning: false})
					}
					p.buf.WriteString(s[len(s)-partial:])
				} else if s != "" {
					segments = append(segments, chatSegment{Text: s, Reasoning: false})
				}
				return segments
			}
			if idx > 0 {
				segments = append(segments, chatSegment{Text: s[:idx], Reasoning: false})
			}
			p.inThink = true
			s = s[idx+len(thinkOpen):]
		}
	}
	return segments
}

// Flush returns any buffered partial-tag text as a segment
// using the current state (reasoning or not).
func (p *thinkParser) Flush() []chatSegment {
	if p.buf.Len() == 0 {
		return nil
	}
	seg := chatSegment{Text: p.buf.String(), Reasoning: p.inThink}
	p.buf.Reset()
	return []chatSegment{seg}
}

// longestSuffix returns the length of the longest suffix of s
// that is a prefix of tag. Returns 0 if no overlap.
func longestSuffix(s, tag string) int {
	maxLen := len(tag) - 1
	if maxLen > len(s) {
		maxLen = len(s)
	}
	for n := maxLen; n > 0; n-- {
		if strings.HasSuffix(s, tag[:n]) {
			return n
		}
	}
	return 0
}

// parseThinkTags strips <think>...</think> from a complete string.
// Returns the cleaned response text and the reasoning text.
func parseThinkTags(text string) (response, reasoning string) {
	var respBuf, reasonBuf strings.Builder
	remaining := text
	for {
		openIdx := strings.Index(remaining, thinkOpen)
		if openIdx < 0 {
			respBuf.WriteString(remaining)
			break
		}
		respBuf.WriteString(remaining[:openIdx])
		remaining = remaining[openIdx+len(thinkOpen):]
		closeIdx := strings.Index(remaining, thinkClose)
		if closeIdx < 0 {
			// Unclosed think tag — treat rest as reasoning.
			reasonBuf.WriteString(remaining)
			break
		}
		reasonBuf.WriteString(remaining[:closeIdx])
		remaining = remaining[closeIdx+len(thinkClose):]
	}
	return strings.TrimSpace(respBuf.String()), strings.TrimSpace(reasonBuf.String())
}
