package search

// query_entities.go — F14: extract entity_node IDs that match query terms.
//
// ExtractQueryEntities implements a cheap regex-based NER pass over the raw query:
// it extracts capitalized tokens (≥2 chars), normalizes them via db.NormalizeEntityID,
// and looks them up in the entity_nodes table for the given cube. The returned IDs
// are string (text primary key) suitable for use as ComputePersonalizedPR seeds.
//
// The postgresClient interface in service_types.go already exposes
// FindEntitiesByNormalizedID; no new DB methods are needed.
//
// Note: an experimental LLM-NER path was previously gated behind
// MEMDB_PPR_LLM_ENTITIES. Both branches resolved to the regex implementation
// (the LLM path was never wired) and the env var was never set in any deploy,
// so the gate was removed in M11. If a real LLM path lands in the future it
// should reuse this entry point and re-introduce the gate at that time.

import (
	"context"
	"regexp"
	"strings"
	"unicode"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// reCapitalizedToken matches tokens that start with a Unicode uppercase letter
// and contain at least one more letter — filters out sentence-start words like
// single "I" while catching proper names: "Alice", "MemDB", "OpenAI", "GPT-4".
var reCapitalizedToken = regexp.MustCompile(`\b[A-Z][A-Za-z0-9][A-Za-z0-9_-]*\b`)

// ExtractQueryEntities returns entity_node IDs (string PKs from entity_nodes) that
// match tokens extracted from query for the given cube.
//
// Strategy: capitalized token extraction → db.NormalizeEntityID → lookup.
// Returns nil (not error) when no entities match — callers treat nil as "no seed".
func ExtractQueryEntities(ctx context.Context, pg postgresClient, cubeID, query string) ([]string, error) {
	if query == "" || cubeID == "" {
		return nil, nil
	}
	return extractQueryEntitiesRegex(ctx, pg, cubeID, query)
}

// extractQueryEntitiesRegex is the cheap NER path:
// 1. Extract capitalized tokens from the query.
// 2. Normalize each via db.NormalizeEntityID.
// 3. Look them up in entity_nodes for the cube.
func extractQueryEntitiesRegex(ctx context.Context, pg postgresClient, cubeID, query string) ([]string, error) {
	tokens := extractCapitalizedTokens(query)
	if len(tokens) == 0 {
		return nil, nil
	}

	normalized := make([]string, 0, len(tokens))
	seen := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		n := db.NormalizeEntityID(t)
		if n == "" {
			continue
		}
		if _, dup := seen[n]; dup {
			continue
		}
		seen[n] = struct{}{}
		normalized = append(normalized, n)
	}
	if len(normalized) == 0 {
		return nil, nil
	}

	// FindEntitiesByNormalizedID accepts personID as the second scope filter.
	// For PPR we pass cubeID as both cube and person scopes — entity_nodes are
	// cube-scoped (user_name == cubeID) and personID filtering is not meaningful
	// for entity extraction. Passing cubeID twice matches the callers in
	// service_parallel.go (they pass p.CubeID, p.UserName which are identical
	// in single-cube mode).
	ids, err := pg.FindEntitiesByNormalizedID(ctx, normalized, cubeID, cubeID)
	if err != nil {
		return nil, err
	}
	return ids, nil
}

// extractCapitalizedTokens returns unique capitalized tokens from text using
// reCapitalizedToken. Also includes all-caps acronyms (≥2 chars, no digits-only).
// Handles mixed scripts conservatively: only ASCII capitalization is checked to
// avoid false positives in Russian/Chinese text where uppercase is not a reliable
// proper-noun signal.
func extractCapitalizedTokens(text string) []string {
	// Primary: regex on ASCII capitalized tokens.
	matches := reCapitalizedToken.FindAllString(text, -1)

	// Secondary: ALL_CAPS acronyms (e.g. "GPT", "API", "MemDB").
	for _, word := range strings.Fields(text) {
		word = strings.TrimFunc(word, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if len(word) >= 2 && isAllUpper(word) {
			matches = append(matches, word)
		}
	}

	// Deduplicate while preserving order.
	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		if _, ok := seen[m]; !ok {
			seen[m] = struct{}{}
			out = append(out, m)
		}
	}
	return out
}

// isAllUpper reports whether every rune in s is uppercase ASCII.
func isAllUpper(s string) bool {
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			return false
		}
	}
	return true
}
