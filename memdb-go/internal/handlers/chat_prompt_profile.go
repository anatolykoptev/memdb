package handlers

// chat_prompt_profile.go — user-profile section assembly for chat system prompts.
//
// M10 Stream 3 (PROFILE-RETRIEVE): Memobase-style profile injection.
// The "## User Profile" section is always emitted (Memobase pattern: absence is
// signal) and is placed BEFORE the existing memory section in the rendered
// system prompt. The existing memory templates are not modified — this is a
// strictly additive prepend.
//
// Env gate: MEMDB_PROFILE_INJECT (default true). Set to false for ablation
// studies / M9 baseline reproduction.

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

const (
	profileSectionHeader  = "## User Profile"
	profileSectionEmpty   = "(none)"
	profileMaxApproxToken = 1000 // soft cap; over this we truncate by lowest confidence first
	profileTokenPerChar   = 4    // crude tokens-per-char heuristic; TODO: replace with shared tokenizer if/when one is wired
)

// profileInjectEnabled returns whether the profile section should be emitted.
// Default: true. Disabled when MEMDB_PROFILE_INJECT is set to a falsey value
// ("0", "false", "no", "off" — case-insensitive).
func profileInjectEnabled() bool {
	v, ok := os.LookupEnv("MEMDB_PROFILE_INJECT")
	if !ok {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "false", "no", "off", "":
		return false
	default:
		return true
	}
}

// formatProfileSection renders the "## User Profile" block.
//
// Empty / nil input → header + "(none)". With entries → one bullet per row:
//
//	- {topic} / {sub_topic}: {memo}
//	- {topic} / {sub_topic}: {memo} [mention YYYY-MM-DD]
//
// Order of bullets follows the input slice (callers rely on
// db.GetProfilesByUser's stable ORDER BY topic, sub_topic, updated_at DESC).
//
// If the rendered block exceeds profileMaxApproxToken tokens (heuristic:
// len/profileTokenPerChar) it is truncated by dropping rows with the lowest
// confidence first; a metric counter is bumped and a warning is logged via the
// caller's metric pipeline (no logger plumbed here).
func formatProfileSection(ctx context.Context, entries []db.ProfileEntry) string {
	if len(entries) == 0 {
		return profileSectionHeader + "\n" + profileSectionEmpty + "\n"
	}

	rendered := renderProfileBullets(entries)
	body := strings.Join(rendered, "\n")

	if approxTokens(body) <= profileMaxApproxToken {
		return profileSectionHeader + "\n" + body + "\n"
	}

	// Truncate by lowest confidence first. Build an index and sort a copy.
	idx := make([]int, len(entries))
	for i := range entries {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		return entries[idx[a]].Confidence < entries[idx[b]].Confidence
	})

	keep := make(map[int]bool, len(entries))
	for i := range entries {
		keep[i] = true
	}
	for _, dropIdx := range idx {
		if approxTokens(strings.Join(filterRendered(rendered, keep), "\n")) <= profileMaxApproxToken {
			break
		}
		delete(keep, dropIdx)
	}

	chatProfileMx().Truncated.Add(ctx, 1)

	final := filterRendered(rendered, keep)
	if len(final) == 0 {
		return profileSectionHeader + "\n" + profileSectionEmpty + "\n"
	}
	return profileSectionHeader + "\n" + strings.Join(final, "\n") + "\n"
}

// renderProfileBullets returns one formatted bullet per entry, in input order.
func renderProfileBullets(entries []db.ProfileEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		line := fmt.Sprintf("- %s / %s: %s", e.Topic, e.SubTopic, e.Memo)
		if e.ValidAt != nil {
			line += fmt.Sprintf(" [mention %s]", e.ValidAt.Format("2006-01-02"))
		}
		out = append(out, line)
	}
	return out
}

// filterRendered keeps bullets whose original index is present in keep,
// preserving original ordering.
func filterRendered(rendered []string, keep map[int]bool) []string {
	out := make([]string, 0, len(keep))
	for i, line := range rendered {
		if keep[i] {
			out = append(out, line)
		}
	}
	return out
}

// approxTokens is a deliberately simple heuristic (len/4) used to enforce the
// soft token cap on the profile section. Replace with a real tokenizer once
// the codebase exposes one outside vendored dependencies.
func approxTokens(s string) int {
	return len(s) / profileTokenPerChar
}

// ── Metrics ──────────────────────────────────────────────────────────────────

var (
	chatProfileMxOnce sync.Once
	chatProfileMxInst *chatProfileMetricsInstruments
)

type chatProfileMetricsInstruments struct {
	// Truncated counts chat requests where the user profile section exceeded
	// profileMaxApproxToken and was truncated. Exported as
	// memdb.chat.profile_truncated_total.
	Truncated metric.Int64Counter
}

func chatProfileMx() *chatProfileMetricsInstruments {
	chatProfileMxOnce.Do(func() {
		meter := otel.Meter("memdb-go/chat")
		trunc, _ := meter.Int64Counter("memdb.chat.profile_truncated_total",
			metric.WithDescription("Count of chat requests where the User Profile section exceeded the soft token cap and was truncated."),
		)
		chatProfileMxInst = &chatProfileMetricsInstruments{Truncated: trunc}
	})
	return chatProfileMxInst
}
