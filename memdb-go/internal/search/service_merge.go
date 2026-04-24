// Package search — service_merge.go: merge parallel results into per-type slices
// and apply CONTRADICTS penalty.
package search

import (
	"context"
	"slices"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// mergeSearchResults merges all parallel results into per-type slices.
func (s *SearchService) mergeSearchResults(psr *parallelSearchResults, bfsResults []db.GraphRecallResult, internetMerged []MergedResult, p SearchParams) (textMerged, skillMerged, toolMerged []MergedResult) {
	textMerged = MergeVectorAndFulltext(psr.textVec, psr.textFT)
	skillMerged = MergeVectorAndFulltext(psr.skillVec, psr.skillFT)
	toolMerged = MergeVectorAndFulltext(psr.toolVec, psr.toolFT)

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
