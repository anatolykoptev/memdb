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

// linkEntities fires a background goroutine that upserts entity_nodes and creates
// MENTIONS_ENTITY edges for every ADD/UPDATE fact that carries LLM-extracted entities.
// Non-blocking, non-fatal — entity graph enriches search but is not required for correctness.
func (r *Reorganizer) linkEntities(embedded []embeddedMemReadFact, cubeID, now string) {
	if r.postgres == nil {
		return
	}

	type pair struct {
		ltmID     string
		entities  []llm.EntityMention
		relations []llm.EntityRelation
		validAt   string
	}
	var pairs []pair
	for _, ef := range embedded {
		if ef.fact.Action != llm.MemAdd && ef.fact.Action != llm.MemUpdate {
			continue
		}
		if len(ef.fact.Entities) == 0 {
			continue
		}
		if ef.ltmID == "" {
			continue
		}
		pairs = append(pairs, pair{
			ltmID:     ef.ltmID,
			entities:  ef.fact.Entities,
			relations: ef.fact.Relations,
			validAt:   ef.fact.ValidAt,
		})
	}
	if len(pairs) == 0 {
		return
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// Batch-embed all unique entity names for identity resolution.
		entityEmbByName := make(map[string]string)
		if r.embedder != nil {
			var allNames []string
			seen := make(map[string]bool)
			for _, p := range pairs {
				for _, ent := range p.entities {
					if ent.Name != "" && !seen[ent.Name] {
						allNames = append(allNames, ent.Name)
						seen[ent.Name] = true
					}
				}
			}
			if len(allNames) > 0 {
				vecs, err := r.embedder.Embed(ctx, allNames)
				if err == nil && len(vecs) == len(allNames) {
					for i, name := range allNames {
						entityEmbByName[name] = db.FormatVector(vecs[i])
					}
				}
			}
		}

		for _, p := range pairs {
			entityIDByName := make(map[string]string, len(p.entities))
			for _, ent := range p.entities {
				if ent.Name == "" {
					continue
				}
				embVec := entityEmbByName[ent.Name]
				entityID, err := r.postgres.UpsertEntityNodeWithEmbedding(ctx, ent.Name, ent.Type, cubeID, now, embVec)
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
			// Create entity-to-entity triplet edges from LLM-extracted relations.
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
		r.logger.Debug("mem_read entity link: complete",
			slog.String("cube_id", cubeID), slog.Int("pairs", len(pairs)))
	}()
}
