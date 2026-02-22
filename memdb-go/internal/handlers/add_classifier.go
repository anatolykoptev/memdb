package handlers

// add_classifier.go — content router that produces quality signals for downstream LLM calls.
//
// Phase 7 shipped a binary skip/proceed gate. This evolution returns a ContentSignal
// struct with hints that get injected into the extraction prompt, improving LLM output.
// Also detects session types for episodic summary focus.

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

// Classifier thresholds.
const (
	// nearDuplicateThreshold is the cosine similarity above which a conversation
	// is considered a near-duplicate of an existing memory and skipped.
	nearDuplicateThreshold = 0.97

	// mergeSuggestionThreshold is the cosine similarity above which the pipeline
	// suggests UPDATE-over-ADD to the LLM (but doesn't hard skip).
	mergeSuggestionThreshold = 0.92

	// trivialMaxChars is the rune limit below which a single-message
	// conversation may be classified as trivial.
	trivialMaxChars = 20

	// codeOnlyRatio is the minimum code-block character ratio to classify as code-only.
	codeOnlyRatio = 0.9

	// codeBiasRatio is the threshold above which code presence biases toward technical.
	codeBiasRatio = 0.2

	// episodicCodeRatio is the threshold above which episodic summary is skipped.
	episodicCodeRatio = 0.8
)

// Content type constants.
const (
	contentOpinion   = "opinion"
	contentTechnical = "technical"
	contentFactual   = "factual"
	contentMultiTurn = "multi-turn"
	contentMixed     = "mixed"
)

// Skip reason constants.
const (
	skipTrivial  = "trivial"
	skipCodeOnly = "code-only"
	skipNoUser   = "no-user-content"
)

// Session type constants.
const (
	sessionGeneral  = "general"
	sessionDecision = "decision"
	sessionLearning = "learning"
	sessionDebug    = "debug"
	sessionPlanning = "planning"
)

// casualPatterns matches short, trivial messages that carry no memorable content.
var casualPatterns = regexp.MustCompile(
	`(?i)\A(?:ok|okay|thanks|thank you|got it|yes|no|sure|done|lgtm|thx|ty|ack|k|yep|yup|nope|cool|nice|great|perfect|right|understood|good|fine|alright|np|nw|mhm|hmm|hm|ah|oh|wow|lol|haha|ha|btw)\z`,
)

// codeBlockRe matches fenced code blocks (``` with optional language tag).
var codeBlockRe = regexp.MustCompile("(?s)```[a-zA-Z]*\\n.*?```")

// Content type detection regexes.
var (
	// opinionRe detects first-person preference/opinion statements.
	opinionRe = regexp.MustCompile(`(?i)\b(I\s+(prefer|like|think|believe|feel|want|love|hate|dislike|enjoy|wish|hope|need|choose|favor|recommend))\b`)

	// technicalTermsRe detects technical terms, CLI commands, and config references.
	technicalTermsRe = regexp.MustCompile(`(?i)\b(docker|kubernetes|postgres|redis|nginx|api|http[s]?|tcp|udp|ssh|git|npm|pip|curl|sudo|systemctl|deploy|server|database|endpoint|middleware|container|microservice|grpc|graphql|webhook|dns|ssl|tls|cdn|vpc|yaml|json|toml|env|dockerfile|makefile|cicd|ci/cd)\b`)

	// factualDateRe detects date/time patterns suggesting factual content.
	factualDateRe = regexp.MustCompile(`\b(\d{4}-\d{2}-\d{2}|\d{1,2}(st|nd|rd|th)\s+(of\s+)?(January|February|March|April|May|June|July|August|September|October|November|December)|\d{1,2}:\d{2}\s*(am|pm|AM|PM)?)\b`)

	// factualNameRe detects capitalized proper nouns (names, places).
	factualNameRe = regexp.MustCompile(`\b[A-Z][a-z]+(?:\s+[A-Z][a-z]+)+\b`)
)

// Session type detection regexes.
var (
	decisionRe = regexp.MustCompile(`(?i)\b(decided|chose|going with|picked|settled on|agreed|concluded|chosen|opting for|decision)\b`)
	learningRe = regexp.MustCompile(`(?i)\b(TIL|learned|turns out|discovered|realized|found out|never knew|understanding now|interesting)\b`)
	debugRe    = regexp.MustCompile(`(?i)\b(error|bug|fix|fixed|solved|crash|exception|traceback|stack trace|segfault|panic|debug|debugging|broken)\b`)
	planningRe = regexp.MustCompile(`(?i)\b(plan|roadmap|next steps|TODO|sprint|milestone|timeline|deadline|priorit|backlog|schedule)\b`)
)

// ContentSignal carries classification results and quality hints for downstream stages.
type ContentSignal struct {
	Skip        bool     // hard skip — only for trivial messages
	SkipReason  string   // "trivial" | "code-only"
	Hints       []string // quality hints injected into LLM extraction prompt
	ContentType string   // "opinion" | "technical" | "factual" | "multi-turn" | "mixed"
}

// classifyContent analyzes messages and conversation text to produce a ContentSignal.
// Primary entry point for the fine-mode add pipeline (has raw messages).
func classifyContent(messages []chatMessage, conversation string) ContentSignal {
	var sig ContentSignal

	// Rule 0: no user content — skip (system-only, empty, whitespace).
	if !hasUserContent(messages) {
		sig.Skip = true
		sig.SkipReason = skipNoUser
		return sig
	}

	// Rule 1: trivial single-message — hard skip.
	if len(messages) == 1 {
		content := strings.TrimSpace(messages[0].Content)
		if content == "" || (utf8.RuneCountInString(content) <= trivialMaxChars && casualPatterns.MatchString(content)) {
			sig.Skip = true
			sig.SkipReason = skipTrivial
			return sig
		}
	}

	// Rule 2: pure code (>90%) — hard skip.
	// Use raw message content to avoid conversation metadata diluting the ratio.
	rawContent := joinMessageContent(messages)
	if ratio := codeBlockRatio(rawContent); ratio > codeOnlyRatio {
		sig.Skip = true
		sig.SkipReason = skipCodeOnly
		return sig
	}

	// Rule 3: multi-turn detection (>2 messages with back-and-forth).
	if len(messages) > 2 {
		sig.ContentType = contentMultiTurn
		sig.Hints = append(sig.Hints, "Multi-turn conversation — look for decisions, conclusions, and preference changes across turns")
		return sig
	}

	// Rule 4: content type detection from text.
	sig.ContentType = detectContentType(conversation)
	sig.Hints = hintsForContentType(sig.ContentType, conversation)
	return sig
}

// hasUserContent returns true if at least one message has role "user" with non-empty content.
func hasUserContent(messages []chatMessage) bool {
	for _, m := range messages {
		if m.Role == roleUser && strings.TrimSpace(m.Content) != "" {
			return true
		}
	}
	return false
}

// classifyContentFromText is a text-only variant for the buffer zone pipeline
// (receives pre-formatted conversation, no raw chatMessage structs).
func classifyContentFromText(conversation string) ContentSignal {
	var sig ContentSignal

	// Skip if no meaningful content.
	if strings.TrimSpace(conversation) == "" {
		sig.Skip = true
		sig.SkipReason = skipTrivial
		return sig
	}

	// Code-only check.
	if ratio := codeBlockRatio(conversation); ratio > codeOnlyRatio {
		sig.Skip = true
		sig.SkipReason = skipCodeOnly
		return sig
	}

	// Count role-prefixed lines to approximate message count.
	msgCount := 0
	for _, line := range strings.Split(conversation, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "user:") || strings.HasPrefix(trimmed, "assistant:") || strings.HasPrefix(trimmed, "system:") {
			msgCount++
		}
	}

	if msgCount > 2 {
		sig.ContentType = contentMultiTurn
		sig.Hints = append(sig.Hints, "Multi-turn conversation — look for decisions, conclusions, and preference changes across turns")
		return sig
	}

	sig.ContentType = detectContentType(conversation)
	sig.Hints = hintsForContentType(sig.ContentType, conversation)
	return sig
}

// convMetaRe matches the "role: [timestamp]: " prefix in formatted conversations.
var convMetaRe = regexp.MustCompile(`(?m)^(?:user|assistant|system):\s*\[[^\]]*\]:\s*`)

// stripConversationMeta removes conversation format metadata (role, timestamp)
// so content detection regexes don't match timestamps as factual content.
func stripConversationMeta(text string) string {
	return convMetaRe.ReplaceAllString(text, "")
}

// detectContentType returns the dominant content type based on regex signals.
func detectContentType(text string) string {
	// Strip conversation metadata to avoid timestamp false positives.
	stripped := stripConversationMeta(text)
	opinionScore := len(opinionRe.FindAllStringIndex(stripped, -1))
	techScore := len(technicalTermsRe.FindAllStringIndex(stripped, -1))
	factualScore := len(factualDateRe.FindAllStringIndex(stripped, -1)) + len(factualNameRe.FindAllStringIndex(stripped, -1))

	// Code blocks present → technical bias.
	if codeBlockRatio(stripped) > codeBiasRatio {
		techScore += 3
	}

	if opinionScore >= 2 || (opinionScore >= 1 && techScore == 0 && factualScore == 0) {
		return contentOpinion
	}
	if techScore >= 3 || (techScore >= 2 && opinionScore == 0) {
		return contentTechnical
	}
	if factualScore >= 2 || (factualScore >= 1 && opinionScore == 0 && techScore == 0) {
		return contentFactual
	}
	return contentMixed
}

// hintsForContentType returns extraction hints for the given content type.
func hintsForContentType(contentType, text string) []string {
	switch contentType {
	case contentOpinion:
		return []string{"Content contains opinions and preferences — extract user preferences with high fidelity, preserve the specific preference and reasoning"}
	case contentTechnical:
		if codeBlockRatio(text) > codeBiasRatio {
			return []string{"Technical content with code — extract architectural decisions, tool preferences, and configuration choices mentioned alongside code. Ignore the code syntax itself."}
		}
		return []string{"Technical content — extract tool choices, configuration decisions, and technical preferences"}
	case contentFactual:
		return []string{"Factual content with specific names, dates, or numbers — preserve all specifics exactly as stated"}
	default:
		return nil
	}
}

// detectSessionType returns the dominant session type based on keyword frequency.
// Used by episodic summary generation to customize the summary prompt.
func detectSessionType(conversation string) string {
	scores := map[string]int{
		sessionDecision: len(decisionRe.FindAllStringIndex(conversation, -1)),
		sessionLearning: len(learningRe.FindAllStringIndex(conversation, -1)),
		sessionDebug:    len(debugRe.FindAllStringIndex(conversation, -1)),
		sessionPlanning: len(planningRe.FindAllStringIndex(conversation, -1)),
	}

	bestType := sessionGeneral
	bestScore := 0
	for typ, score := range scores {
		if score > bestScore {
			bestScore = score
			bestType = typ
		}
	}
	if bestScore == 0 {
		return sessionGeneral
	}
	return bestType
}

// sessionPromptFocus returns a focus instruction for the episodic summary prompt
// based on the detected session type.
func sessionPromptFocus(sessionType string) string {
	switch sessionType {
	case "decision":
		return "Focus on what was decided and why. Capture the alternatives considered and the rationale for the chosen option."
	case "learning":
		return "Focus on key takeaways and new understanding. Capture what was learned and any misconceptions corrected."
	case "debug":
		return "Focus on the problem, root cause, and solution. Capture the error symptoms, investigation steps, and the fix applied."
	case "planning":
		return "Focus on goals, timeline, and dependencies. Capture action items, ownership, and deadlines mentioned."
	default:
		return ""
	}
}

// classifySkipExtraction is the Phase 7 backward-compatible wrapper.
// Callers that only need skip/reason can use this instead of classifyContent.
func classifySkipExtraction(messages []chatMessage, conversation string) (bool, string) {
	sig := classifyContent(messages, conversation)
	return sig.Skip, sig.SkipReason
}

// joinMessageContent concatenates raw message content without conversation metadata.
// Used for code-block ratio check to avoid metadata diluting the ratio.
func joinMessageContent(messages []chatMessage) string {
	var sb strings.Builder
	for i, m := range messages {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(m.Content)
	}
	return sb.String()
}

// codeBlockRatio returns the fraction of text inside fenced code blocks.
func codeBlockRatio(conversation string) float64 {
	total := len(conversation)
	if total == 0 {
		return 0
	}
	matches := codeBlockRe.FindAllStringIndex(conversation, -1)
	codeChars := 0
	for _, m := range matches {
		codeChars += m[1] - m[0]
	}
	return float64(codeChars) / float64(total)
}
