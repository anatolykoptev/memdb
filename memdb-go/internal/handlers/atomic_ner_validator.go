// Package handlers — atomic_ner_validator.go: post-extract NER completeness
// gate. Catches the LLM's "missed entity" failure mode (Gemini Flash family
// systematically drops items from enumerations: "I have 3 pets — Oliver,
// Luna, Bailey" → "User has 2 pets: Oliver, Luna"; Bailey lost) by
// regex-extracting proper nouns from the source conversation, comparing
// against the entities the LLM emitted (named_entities_in_text), and firing
// ONE targeted re-extract for the gap.
//
// Architectural rationale (cf. code-reviewer advisory and SOTA research):
//   - Prompt-based "preserve all enumerated entities" rules are ignored by
//     small LLMs (verified empirically across v2, v8, v9 atomic-extractor
//     iterations on Flash-lite and Flash 3.1).
//   - Schema-bound output (json_object + required field) helps but doesn't
//     close the gap — the LLM still misses entities.
//   - The general fix (per Memobase / Zep / mem0 audit) is a deterministic
//     post-processor that VALIDATES extraction completeness and triggers a
//     scoped second LLM call ONLY when needed (~15-20% of chunks).
//
// Cost model:
//   - Regex pass: O(n) over source text, <1 ms even on multi-KB chunks.
//   - Set diff: O(facts × avg-entities-per-fact). Bounded.
//   - Re-call fires only when missing > 0. Targeted prompt is short
//     (no Existing Memories block, just "extract these N entities") so it
//     finishes in ~2-3 s vs ~5 s for the primary extract.
//
// Telemetry: memdb.atomic.ner_validator_total{outcome=…}:
//   - skipped       : NER disabled or empty source
//   - complete      : LLM caught every proper noun on first pass
//   - rescued       : second-call recovered N missed entities
//   - rescue_failed : second call produced no facts containing missed entities
//   - rescue_error  : second call errored (logged, treated as no-op)
package handlers

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode"

	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// nerValidatorEnabled gates the whole pipeline. Default ON. Operator override:
// MEMDB_ATOMIC_NER_VALIDATOR=0 reverts to single-pass extraction. Used for
// A/B tests against the dedup-only baseline.
func nerValidatorEnabled() bool {
	v, ok := os.LookupEnv("MEMDB_ATOMIC_NER_VALIDATOR")
	if !ok {
		return true
	}
	return v == "1" || strings.EqualFold(v, "true")
}

// properNounRegex matches a contiguous run of TitleCase tokens (single word
// OR multi-word: "Becoming Nicole", "Caroline LGBTQ"). Single-word matches
// are filtered against an English stopword list (sentence starts, days,
// months) to suppress noise. Multi-word matches are kept as-is — they are
// almost always real names/titles.
//
// Cyrillic capitals included via the Unicode-aware [\p{Lu}] class so Russian
// production data ("Пользователь Марк") matches too.
var properNounRegex = regexp.MustCompile(`\b[\p{Lu}][\p{L}'-]+(?:\s+[\p{Lu}][\p{L}'-]+)*\b`)

// quotedTitleRegex catches quoted titles that the proper-noun regex may
// fragment ("Becoming Nicole", «Anna Karenina», 'Project X'). Three quote
// styles cover EN/RU production: ASCII double, French guillemet, ASCII single.
var quotedTitleRegex = regexp.MustCompile(`"([^"]+)"|«([^»]+)»|'([^']+)'`)

// stopWordsTitlecase — single-word TitleCase tokens that are almost never
// proper nouns in conversational text. Sentence starts dominate the noise
// floor; days/months add a smaller batch. Multi-word matches bypass this
// list (the multi-word context disambiguates).
var stopWordsTitlecase = map[string]struct{}{
	// English sentence-start common words (subjective; tuned to LoCoMo)
	"I": {}, "The": {}, "A": {}, "An": {}, "This": {}, "That": {}, "These": {}, "Those": {},
	"My": {}, "Your": {}, "His": {}, "Her": {}, "Our": {}, "Their": {},
	"It": {}, "He": {}, "She": {}, "We": {}, "They": {}, "You": {}, "Me": {}, "Us": {},
	"Yes": {}, "No": {}, "Maybe": {}, "Sure": {}, "OK": {}, "Okay": {}, "Hi": {}, "Hello": {},
	"Thanks": {}, "Thank": {},
	"Where": {}, "When": {}, "Why": {}, "What": {}, "How": {}, "Who": {}, "Which": {},
	"Did": {}, "Do": {}, "Does": {}, "Has": {}, "Have": {}, "Had": {}, "Is": {}, "Are": {}, "Was": {}, "Were": {},
	"Will": {}, "Would": {}, "Should": {}, "Could": {}, "Can": {}, "Must": {}, "May": {}, "Might": {},
	"And": {}, "Or": {}, "But": {}, "So": {}, "If": {}, "Then": {}, "While": {}, "Because": {},
	"After": {}, "Before": {}, "During": {}, "About": {}, "Just": {}, "Only": {}, "Also": {}, "Even": {},
	"Last": {}, "Next": {}, "Today": {}, "Yesterday": {}, "Tomorrow": {}, "Now": {}, "Soon": {}, "Later": {},
	"Monday": {}, "Tuesday": {}, "Wednesday": {}, "Thursday": {}, "Friday": {}, "Saturday": {}, "Sunday": {},
	"January": {}, "February": {}, "March": {}, "April": {}, "June": {},
	"July": {}, "August": {}, "September": {}, "October": {}, "November": {}, "December": {},
	"Mon": {}, "Tue": {}, "Wed": {}, "Thu": {}, "Fri": {}, "Sat": {}, "Sun": {},
	"Jan": {}, "Feb": {}, "Mar": {}, "Apr": {}, "Jun": {}, "Jul": {}, "Aug": {}, "Sep": {}, "Oct": {}, "Nov": {}, "Dec": {},
	// Russian common
	"Я": {}, "Ты": {}, "Он": {}, "Она": {}, "Мы": {}, "Вы": {}, "Они": {}, "Это": {},
	"Да": {}, "Нет": {}, "Спасибо": {}, "Привет": {},
}

// extractProperNouns returns the set of proper-noun phrases present in
// `text`. Combined output of properNounRegex + quotedTitleRegex (titles in
// quotes recovered separately so multi-word book/film titles don't get
// fragmented).
//
// Returns a set (map) so callers can do O(1) presence checks. All values are
// trimmed and case-preserved (matching the named_entities_in_text format the
// LLM is instructed to emit).
func extractProperNouns(text string) map[string]struct{} {
	out := make(map[string]struct{}, 32)

	// Pass 1: TitleCase runs.
	for _, m := range properNounRegex.FindAllString(text, -1) {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		// Filter single-word stopwords; multi-word always kept.
		if !strings.Contains(m, " ") {
			if _, drop := stopWordsTitlecase[m]; drop {
				continue
			}
			// Filter all-uppercase abbreviations of length 1 (artifacts of
			// sentence boundary detection). Real abbreviations like "LGBTQ"
			// are length ≥3.
			if len(m) <= 1 {
				continue
			}
		}
		out[m] = struct{}{}
	}

	// Pass 2: quoted titles (the regex returns groups; iterate over all
	// matches and pick the non-empty group).
	for _, m := range quotedTitleRegex.FindAllStringSubmatch(text, -1) {
		for i := 1; i < len(m); i++ {
			if v := strings.TrimSpace(m[i]); v != "" {
				out[v] = struct{}{}
			}
		}
	}
	return out
}

// gatherExtractedEntities walks a slice of facts and collects the union of
// their NamedEntitiesInText fields. The LLM is instructed (per v2 prompt) to
// populate this for every fact; in practice it does so for ~93% of facts.
// We also fold in any TitleCase tokens spotted INSIDE fact.Text as a safety
// net for facts where the LLM omitted the field.
func gatherExtractedEntities(facts []llm.AtomicFact) map[string]struct{} {
	out := make(map[string]struct{}, 32)
	for _, f := range facts {
		for _, e := range f.NamedEntitiesInText {
			if e = strings.TrimSpace(e); e != "" {
				out[e] = struct{}{}
			}
		}
		// Safety net: scan the fact text itself for proper nouns. This
		// catches cases where the LLM emitted the entity in `text` but
		// forgot to mirror it in `named_entities_in_text`.
		for k := range extractProperNouns(f.Text) {
			out[k] = struct{}{}
		}
	}
	return out
}

// computeMissingEntities returns the entities that appear in source but
// NOT in any extracted fact. Case-insensitive set diff so "Caroline" (source)
// matches "caroline" (extracted) — the LLM occasionally lowercases.
//
// Returns a slice (sorted for deterministic prompt) of canonical-cased
// entity strings as found in source.
func computeMissingEntities(source string, facts []llm.AtomicFact) []string {
	srcEntities := extractProperNouns(source)
	if len(srcEntities) == 0 {
		return nil
	}
	extracted := gatherExtractedEntities(facts)
	// Build a case-folded view of `extracted` for comparison.
	extractedLower := make(map[string]struct{}, len(extracted))
	for k := range extracted {
		extractedLower[strings.ToLower(k)] = struct{}{}
	}
	var missing []string
	for k := range srcEntities {
		if _, ok := extractedLower[strings.ToLower(k)]; !ok {
			missing = append(missing, k)
		}
	}
	// Stable order — keeps the recall prompt deterministic across re-runs
	// (helps the atomic-extract cache hit on rescue calls too).
	sortStrings(missing)
	return missing
}

// sortStrings exists to avoid an extra import block in the test harness;
// uses sort.Strings under the hood.
func sortStrings(s []string) {
	// Insertion sort — for the typical N < 20 case the constant factor beats
	// sort.Strings's slice header allocation. The fallback to sort.Strings is
	// not worth the dependency complexity here.
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// runNERRescue fires a targeted re-extract for entities the first pass
// missed. The prompt is intentionally minimal — no Existing Memories, no
// Recently Extracted, no Last K Messages — to keep the call fast and to
// scope the LLM strictly to the gap.
//
// Returns the rescue facts (post-validation by ExtractAtomicFacts itself,
// which already strips empty texts and validates dates). Errors are logged
// at warn and the function returns nil — failure to rescue is a soft
// degrade, not a hard failure.
func (h *Handler) runNERRescue(
	ctx context.Context,
	conversation, observationDate string,
	missing []string,
	ext *llm.AtomicExtractor,
) []llm.AtomicFact {
	if ext == nil || len(missing) == 0 {
		return nil
	}
	// Hint prompt is concatenated into the conversation so the existing
	// extractor pipeline (with its current skill body, json_object mode,
	// and named_entities_in_text required field) handles it identically
	// to the primary call. We tag the synthetic message so a reader of
	// the upstream proxy logs can spot rescue traffic.
	hint := fmt.Sprintf(
		"\n\n[NER-RESCUE: the previous extraction missed the following proper nouns from this source. Emit one or more facts covering these entities specifically. Do not duplicate facts already produced.]\n\nMissing: %s",
		strings.Join(missing, ", "),
	)
	rescueConv := conversation + hint
	// candidates intentionally empty — the rescue is scoped to the missing
	// entities in this source, not to sibling facts the LLM already linked.
	rescued, err := ext.ExtractAtomicFacts(ctx, rescueConv, nil, observationDate)
	if err != nil {
		recordNERValidatorOutcome(ctx, "rescue_error")
		h.logger.Warn("atomic NER rescue: extract failed",
			slog.Any("error", err),
			slog.Int("missing_count", len(missing)),
			slog.String("missing_sample", joinSampleEntities(missing, 5)),
		)
		return nil
	}
	if len(rescued) == 0 {
		recordNERValidatorOutcome(ctx, "rescue_failed")
		h.logger.Debug("atomic NER rescue: zero facts returned",
			slog.Int("missing_count", len(missing)),
			slog.String("missing_sample", joinSampleEntities(missing, 5)),
		)
		return nil
	}

	// Quality gate: rescue LLM call sometimes emits "extra" facts unrelated
	// to the missing entities (it has license to extract from the whole
	// conversation, not just the gap). Those extra facts inflate the cube's
	// fact pool, dilute top-k retrieval, and introduce noise that hurts
	// single-hop precision (cat1 -20pp observed in v11b vs v10).
	//
	// Filter: keep ONLY rescue facts whose text mentions at least one of the
	// missing entities (case-insensitive substring match). The fact's own
	// named_entities_in_text field is consulted first (cheaper, exact),
	// falling back to text.Contains for facts the LLM forgot to populate.
	missingLower := make([]string, len(missing))
	for i, m := range missing {
		missingLower[i] = strings.ToLower(m)
	}
	filtered := make([]llm.AtomicFact, 0, len(rescued))
	for _, f := range rescued {
		if factMentionsAny(f, missingLower) {
			filtered = append(filtered, f)
		}
	}
	if len(filtered) == 0 {
		recordNERValidatorOutcome(ctx, "rescue_filtered_empty")
		h.logger.Debug("atomic NER rescue: all facts filtered (none mentioned missing entities)",
			slog.Int("rescued_raw", len(rescued)),
			slog.Int("missing_count", len(missing)),
		)
		return nil
	}
	recordNERValidatorOutcome(ctx, "rescued")
	if len(filtered) < len(rescued) {
		h.logger.Debug("atomic NER rescue: noise filtered",
			slog.Int("rescued_raw", len(rescued)),
			slog.Int("rescued_kept", len(filtered)),
			slog.Int("dropped_off_topic", len(rescued)-len(filtered)),
		)
	}
	return filtered
}

// factMentionsAny — true if the fact references at least one of the supplied
// entities (already lowercased). Two-stage check:
//  1. Inspect named_entities_in_text (LLM-emitted; usually populated).
//  2. Fall back to substring scan of fact.Text (catches the ~7% of facts
//     where the LLM omitted the field).
//
// Substring match is intentional, not whole-word: handles inflections
// ("Bailey's" → matches "bailey") and partial titles ("Becoming Nicole" →
// matches if fact says "the book Becoming Nicole was…").
func factMentionsAny(f llm.AtomicFact, lowered []string) bool {
	for _, e := range f.NamedEntitiesInText {
		el := strings.ToLower(e)
		for _, m := range lowered {
			if el == m {
				return true
			}
		}
	}
	textL := strings.ToLower(f.Text)
	for _, m := range lowered {
		if strings.Contains(textL, m) {
			return true
		}
	}
	return false
}

// joinSampleEntities trims a list to N for log-friendly output.
func joinSampleEntities(es []string, n int) string {
	if len(es) <= n {
		return strings.Join(es, ", ")
	}
	return strings.Join(es[:n], ", ") + fmt.Sprintf(" (+%d more)", len(es)-n)
}

// hasUpper is a cheap predicate used by callers that want to skip the NER
// pass entirely on lowercase-only text (chat messages without named
// entities). Returns true on the first uppercase rune found.
func hasUpper(s string) bool {
	for _, r := range s {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// recordNERValidatorOutcome bumps the per-outcome counter.
var (
	nerValidatorCounterOnce sync.Once
	nerValidatorCounter     metric.Int64Counter
)

func recordNERValidatorOutcome(ctx context.Context, outcome string) {
	nerValidatorCounterOnce.Do(func() {
		m := otel.Meter("memdb-go/handlers")
		c, _ := m.Int64Counter("memdb.atomic.ner_validator_total",
			metric.WithDescription("Outcomes of post-extract NER completeness validator (skipped|complete|rescued|rescue_failed|rescue_error). Rescue-rate = rescued/(complete+rescued)."),
		)
		nerValidatorCounter = c
	})
	if nerValidatorCounter == nil {
		return
	}
	nerValidatorCounter.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}
