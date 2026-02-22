package scheduler

// reorganizer_entities.go — background entity linking for async mem_read facts.
//
// Replicates the logic from handlers/add_episodic.go:linkEntitiesAsync as a
// Reorganizer method. Runs in a background goroutine (15s timeout). Non-fatal.

import (
	"context"
	"log/slog"
	"time"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/llm"
)

const entityLinkTimeout = 15 * time.Second // timeout for background entity linking goroutine

// entityPair holds the LTM node and its associated entities/relations for linking.
type entityPair struct {
	ltmID     string
	entities  []llm.EntityMention
	relations []llm.EntityRelation
	validAt   string
}

// linkEntities fires a background goroutine that upserts entity_nodes and creates
// MENTIONS_ENTITY edges for every ADD/UPDATE fact that carries LLM-extracted entities.
// Non-blocking, non-fatal — entity graph enriches search but is not required for correctness.
func (r *Reorganizer) linkEntities(embedded []embeddedMemReadFact, cubeID, now string) {
	if r.postgres == nil {
		return
	}

	pairs := collectEntityPairs(embedded)
	if len(pairs) == 0 {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), entityLinkTimeout)
		defer cancel()

		embByName := r.batchEmbedEntityNames(ctx, pairs)
		for _, p := range pairs {
			r.linkOnePair(ctx, p, cubeID, now, embByName)
		}
		r.logger.Debug("mem_read entity link: complete",
			slog.String("cube_id", cubeID), slog.Int("pairs", len(pairs)))
	}()
}

// collectEntityPairs builds entityPair slice from embedded facts with entities.
func collectEntityPairs(embedded []embeddedMemReadFact) []entityPair {
	var pairs []entityPair
	for _, ef := range embedded {
		if ef.fact.Action != llm.MemAdd && ef.fact.Action != llm.MemUpdate {
			continue
		}
		if len(ef.fact.Entities) == 0 || ef.ltmID == "" {
			continue
		}
		pairs = append(pairs, entityPair{
			ltmID: ef.ltmID, entities: ef.fact.Entities,
			relations: ef.fact.Relations, validAt: ef.fact.ValidAt,
		})
	}
	return pairs
}

// batchEmbedEntityNames embeds all unique entity names and returns a name→vector map.
func (r *Reorganizer) batchEmbedEntityNames(ctx context.Context, pairs []entityPair) map[string]string {
	embByName := make(map[string]string)
	if r.embedder == nil {
		return embByName
	}
	seen := make(map[string]bool)
	var allNames []string
	for _, p := range pairs {
		for _, ent := range p.entities {
			if ent.Name != "" && !seen[ent.Name] {
				allNames = append(allNames, ent.Name)
				seen[ent.Name] = true
			}
		}
	}
	if len(allNames) == 0 {
		return embByName
	}
	vecs, err := r.embedder.Embed(ctx, allNames)
	if err == nil && len(vecs) == len(allNames) {
		for i, name := range allNames {
			embByName[name] = db.FormatVector(vecs[i])
		}
	}
	return embByName
}

// linkOnePair upserts entity nodes, creates MENTIONS_ENTITY edges, and entity-relation edges.
func (r *Reorganizer) linkOnePair(ctx context.Context, p entityPair, cubeID, now string, embByName map[string]string) {
	entityIDByName := make(map[string]string, len(p.entities))
	for _, ent := range p.entities {
		if ent.Name == "" {
			continue
		}
		entityID, err := r.postgres.UpsertEntityNodeWithEmbedding(ctx, ent.Name, ent.Type, cubeID, now, embByName[ent.Name])
		if err != nil {
			r.logger.Debug("mem_read entity link: upsert entity node failed",
				slog.String("name", ent.Name), slog.Any("error", err))
			continue
		}
		entityIDByName[ent.Name] = entityID
		if err := r.postgres.CreateMemoryEdge(ctx, p.ltmID, entityID, db.EdgeMentionsEntity, now, p.validAt); err != nil {
			r.logger.Debug("mem_read entity link: create edge failed",
				slog.String("ltm_id", p.ltmID), slog.String("entity_id", entityID), slog.Any("error", err))
		}
	}
	for _, rel := range p.relations {
		fromID, ok1 := entityIDByName[rel.Subject]
		toID, ok2 := entityIDByName[rel.Object]
		if !ok1 || !ok2 || rel.Predicate == "" {
			continue
		}
		if err := r.postgres.UpsertEntityEdge(ctx, fromID, rel.Predicate, toID, p.ltmID, cubeID, p.validAt, now); err != nil {
			r.logger.Debug("mem_read entity link: upsert entity edge failed",
				slog.String("from", fromID), slog.String("pred", rel.Predicate),
				slog.String("to", toID), slog.Any("error", err))
		}
	}
}
