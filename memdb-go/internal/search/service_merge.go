// Package search — service_merge.go: merge parallel results into per-type slices
// and apply CONTRADICTS penalty.
package search

import (
	"context"
	"slices"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// mergeSearchResults merges all parallel results into per-type slices.
func (s *SearchService) mergeSearchResults(ctx context.Context, psr *parallelSearchResults, bfsResults []db.GraphRecallResult, internetMerged []MergedResult, p SearchParams) (textMerged, skillMerged, toolMerged []MergedResult) {
	// AttributedTo filter: when set, drop atomic facts whose
	// properties.attributed_to disagrees. Applied to vector + fulltext per-type
	// pools BEFORE merge so merged scores stay correct. Graph + internet results
	// are passed through unchanged (they don't carry per-fact attribution).
	if p.AttributedTo != "" {
		psr.textVec = filterByAttribution(ctx, psr.textVec, p.AttributedTo)
		psr.textFT = filterByAttribution(ctx, psr.textFT, p.AttributedTo)
		psr.skillVec = filterByAttribution(ctx, psr.skillVec, p.AttributedTo)
		psr.skillFT = filterByAttribution(ctx, psr.skillFT, p.AttributedTo)
		psr.toolVec = filterByAttribution(ctx, psr.toolVec, p.AttributedTo)
		psr.toolFT = filterByAttribution(ctx, psr.toolFT, p.AttributedTo)
	} else {
		recordAttributionOutcome(ctx, "disabled", 1)
	}

	textMerged = mergeVectorAndFulltextDispatch(psr.textVec, psr.textFT)
	skillMerged = mergeVectorAndFulltextDispatch(psr.skillVec, psr.skillFT)
	toolMerged = mergeVectorAndFulltextDispatch(psr.toolVec, psr.toolFT)

	graphAll := slices.Concat(psr.graphKeyResults, psr.graphTagResults, bfsResults, psr.entityGraphResults)
	if len(graphAll) == 0 {
		return textMerged, skillMerged, toolMerged
	}

	var graphText, graphSkill []db.GraphRecallResult
	for _, g := range graphAll {
		props := ParseProperties(g.Properties)
		if props == nil {
			continue
		}
		mtype, _ := props["memory_type"].(string)
		if mtype == "SkillMemory" {
			graphSkill = append(graphSkill, g)
		} else {
			graphText = append(graphText, g)
		}
	}
	textMerged = MergeGraphIntoResults(textMerged, graphText)
	if p.IncludeSkill && p.SkillTopK > 0 {
		skillMerged = MergeGraphIntoResults(skillMerged, graphSkill)
	}
	textMerged = append(textMerged, internetMerged...)
	return textMerged, skillMerged, toolMerged
}

// filterByAttribution drops vector-search results whose
// properties.attributed_to differs from want. Rows missing the field are
// retained — legacy non-atomic memories don't carry attribution and must
// not be silently dropped. Empty want is a no-op (caller should gate).
//
// Each row contributes one outcome to the attribution_filter_total counter:
// "kept" — attribution matched want; "dropped" — attribution differed;
// "missing" — row had no attributed_to field (legacy memory, kept anyway).
func filterByAttribution(ctx context.Context, in []db.VectorSearchResult, want string) []db.VectorSearchResult {
	if want == "" || len(in) == 0 {
		return in
	}
	out := make([]db.VectorSearchResult, 0, len(in))
	var kept, dropped, missing int
	for _, r := range in {
		props := ParseProperties(r.Properties)
		// Missing attribution → keep (legacy memory without speaker tag).
		if props == nil {
			out = append(out, r)
			missing++
			continue
		}
		got, ok := props["attributed_to"].(string)
		if !ok || got == "" {
			out = append(out, r)
			missing++
			continue
		}
		if got == want {
			out = append(out, r)
			kept++
		} else {
			dropped++
		}
	}
	recordAttributionOutcome(ctx, "kept", kept)
	recordAttributionOutcome(ctx, "dropped", dropped)
	recordAttributionOutcome(ctx, "missing", missing)
	return out
}

// applyContradictsPenalty lowers scores of memories contradicted by higher-ranked results.
func (s *SearchService) applyContradictsPenalty(ctx context.Context, textMerged []MergedResult, p SearchParams) []MergedResult {
	if len(textMerged) == 0 {
		return textMerged
	}
	seedN := 10
	if len(textMerged) < seedN {
		seedN = len(textMerged)
	}
	seedIDs := make([]string, 0, seedN)
	for _, r := range textMerged[:seedN] {
		seedIDs = append(seedIDs, r.ID)
	}
	contradicted, err := s.postgres.GraphRecallByEdge(ctx, seedIDs, db.EdgeContradicts, p.CubeID, p.UserName, contradictsEdgeSeedN)
	if err != nil || len(contradicted) == 0 {
		return textMerged
	}
	contradictedSet := make(map[string]bool, len(contradicted))
	for _, c := range contradicted {
		contradictedSet[c.ID] = true
	}
	return PenalizeContradicts(textMerged, contradictedSet)
}
