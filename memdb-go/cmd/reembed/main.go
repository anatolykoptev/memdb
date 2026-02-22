//go:build cgo

// Re-embedding CLI tool — re-embeds all existing memories with a new model.
//
// Usage: memdb-reembed
//
// Environment variables:
//   MEMDB_POSTGRES_URL  — PostgreSQL connection string
//   MEMDB_QDRANT_ADDR   — Qdrant host:port
//   MEMDB_ONNX_MODEL_DIR — path to ONNX model files
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/qdrant/go-client/qdrant"

	"github.com/MemDBai/MemDB/memdb-go/internal/db"
	"github.com/MemDBai/MemDB/memdb-go/internal/embedder"
)

const (
	graphName  = "memos_graph"
	batchSize  = 10
	maxRetries = 2
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	pgURL := os.Getenv("MEMDB_POSTGRES_URL")
	qdrantAddr := os.Getenv("MEMDB_QDRANT_ADDR")
	modelDir := os.Getenv("MEMDB_ONNX_MODEL_DIR")
	if modelDir == "" {
		modelDir = "/models"
	}

	if pgURL == "" {
		logger.Error("MEMDB_POSTGRES_URL is required")
		os.Exit(1)
	}

	ctx := context.Background()
	start := time.Now()

	// Initialize ONNX embedder
	logger.Info("initializing ONNX embedder", slog.String("model_dir", modelDir))
	emb, err := embedder.NewONNXEmbedder(modelDir, logger)
	if err != nil {
		logger.Error("embedder init failed", slog.Any("error", err))
		os.Exit(1)
	}
	defer emb.Close()
	logger.Info("embedder ready", slog.Int("dimension", emb.Dimension()))

	// Connect to PostgreSQL
	logger.Info("connecting to postgres")
	pgCfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		logger.Error("invalid postgres URL", slog.Any("error", err))
		_ = emb.Close()
		os.Exit(1) //nolint:gocritic // emb.Close() already called explicitly above
	}
	pgCfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "LOAD 'age'")
		if err != nil {
			return err
		}
		_, err = conn.Exec(ctx, fmt.Sprintf("SET search_path = ag_catalog, %q, public", graphName))
		return err
	}
	pool, err := pgxpool.NewWithConfig(ctx, pgCfg)
	if err != nil {
		logger.Error("postgres connect failed", slog.Any("error", err))
		os.Exit(1)
	}
	defer pool.Close()

	// Re-embed PolarDB Memory nodes
	pgCount, err := reembedPolarDB(ctx, pool, emb, logger)
	if err != nil {
		logger.Error("polardb re-embed failed", slog.Any("error", err))
		os.Exit(1)
	}

	// Re-embed Qdrant preference points
	var qdCount int
	if qdrantAddr != "" {
		qdDB, err := db.NewQdrant(ctx, qdrantAddr, logger)
		if err != nil {
			logger.Error("qdrant connect failed", slog.Any("error", err))
			os.Exit(1)
		}
		defer qdDB.Close()

		for _, coll := range []string{"explicit_preference", "implicit_preference"} {
			n, err := reembedQdrant(ctx, qdDB.Client(), coll, emb, logger)
			if err != nil {
				logger.Error("qdrant re-embed failed",
					slog.String("collection", coll),
					slog.Any("error", err))
				continue
			}
			qdCount += n
		}
	}

	elapsed := time.Since(start)
	logger.Info("re-embedding complete",
		slog.Int("polardb_entries", pgCount),
		slog.Int("qdrant_points", qdCount),
		slog.Duration("elapsed", elapsed),
	)
}

// reembedPolarDB re-embeds all activated Memory nodes in PolarDB.
func reembedPolarDB(ctx context.Context, pool *pgxpool.Pool, emb *embedder.ONNXEmbedder, logger *slog.Logger) (int, error) {
	// Load all activated memories with embeddings
	query := fmt.Sprintf(`
		SELECT m.id, m.properties->>'memory' AS memory
		FROM "%s"."Memory" m
		WHERE m.properties->>'status' = 'activated'
		  AND m.embedding IS NOT NULL
		  AND m.properties->>'memory' IS NOT NULL
		  AND m.properties->>'memory' != ''
		ORDER BY m.id
	`, graphName)

	rows, err := pool.Query(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("query memories: %w", err)
	}
	defer rows.Close()

	type memEntry struct {
		ID     string
		Memory string
	}

	var entries []memEntry
	for rows.Next() {
		var e memEntry
		if err := rows.Scan(&e.ID, &e.Memory); err != nil {
			return 0, fmt.Errorf("scan row: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("rows iteration: %w", err)
	}

	logger.Info("loaded PolarDB entries for re-embedding", slog.Int("count", len(entries)))

	// Process in batches
	updated := 0
	for i := 0; i < len(entries); i += batchSize {
		end := i + batchSize
		if end > len(entries) {
			end = len(entries)
		}
		batch := entries[i:end]

		// Prepare texts with "passage: " prefix
		texts := make([]string, len(batch))
		for j, e := range batch {
			texts[j] = "passage: " + e.Memory
		}

		// Embed
		embeddings, err := emb.Embed(ctx, texts)
		if err != nil {
			logger.Error("embed batch failed", slog.Int("batch_start", i), slog.Any("error", err))
			continue
		}

		// Update each entry
		for j, e := range batch {
			vecStr := db.FormatVector(embeddings[j])
			updateSQL := fmt.Sprintf(
				`UPDATE "%s"."Memory" SET embedding = $1::vector WHERE id = $2`,
				graphName,
			)
			if _, err := pool.Exec(ctx, updateSQL, vecStr, e.ID); err != nil {
				logger.Error("update embedding failed",
					slog.String("id", e.ID),
					slog.Any("error", err))
				continue
			}
			updated++
		}

		logger.Info("batch complete",
			slog.Int("progress", end),
			slog.Int("total", len(entries)),
		)
	}

	return updated, nil
}

// reembedQdrant re-embeds all points in a Qdrant collection.
func reembedQdrant(ctx context.Context, client *qdrant.Client, collection string, emb *embedder.ONNXEmbedder, logger *slog.Logger) (int, error) {
	if found, err := qdrantCollectionExists(ctx, client, collection); err != nil {
		return 0, err
	} else if !found {
		logger.Info("collection not found, skipping", slog.String("collection", collection))
		return 0, nil
	}

	allPoints, err := scrollAllQdrantPoints(ctx, client, collection)
	if err != nil {
		return 0, err
	}
	logger.Info("loaded Qdrant points for re-embedding",
		slog.String("collection", collection),
		slog.Int("count", len(allPoints)),
	)

	return reembedPointBatches(ctx, client, collection, allPoints, emb, logger)
}

// qdrantCollectionExists returns true if the named collection exists in Qdrant.
func qdrantCollectionExists(ctx context.Context, client *qdrant.Client, collection string) (bool, error) {
	collections, err := client.ListCollections(ctx)
	if err != nil {
		return false, fmt.Errorf("list collections: %w", err)
	}
	for _, c := range collections {
		if c == collection {
			return true, nil
		}
	}
	return false, nil
}

// scrollAllQdrantPoints fetches all points from a Qdrant collection with cursor pagination.
func scrollAllQdrantPoints(ctx context.Context, client *qdrant.Client, collection string) ([]*qdrant.RetrievedPoint, error) {
	var scrollOffset *qdrant.PointId
	var allPoints []*qdrant.RetrievedPoint
	limit := uint32(100)

	for {
		points, nextOffset, err := client.ScrollAndOffset(ctx, &qdrant.ScrollPoints{
			CollectionName: collection,
			Limit:          &limit,
			Offset:         scrollOffset,
			WithPayload:    qdrant.NewWithPayload(true),
			WithVectors:    qdrant.NewWithVectors(true),
		})
		if err != nil {
			return nil, fmt.Errorf("scroll %s: %w", collection, err)
		}
		allPoints = append(allPoints, points...)
		if nextOffset == nil {
			break
		}
		scrollOffset = nextOffset
	}
	return allPoints, nil
}

// reembedPointBatches processes all points in batches: embed + upsert back to Qdrant.
func reembedPointBatches(ctx context.Context, client *qdrant.Client, collection string, allPoints []*qdrant.RetrievedPoint, emb *embedder.ONNXEmbedder, logger *slog.Logger) (int, error) {
	updated := 0
	for i := 0; i < len(allPoints); i += batchSize {
		end := i + batchSize
		if end > len(allPoints) {
			end = len(allPoints)
		}
		n, err := reembedSingleBatch(ctx, client, collection, allPoints[i:end], emb)
		if err != nil {
			logger.Error("batch failed", slog.Int("batch_start", i), slog.Any("error", err))
			continue
		}
		updated += n
	}
	return updated, nil
}

// reembedSingleBatch embeds one batch of points and upserts them back to Qdrant.
func reembedSingleBatch(ctx context.Context, client *qdrant.Client, collection string, batch []*qdrant.RetrievedPoint, emb *embedder.ONNXEmbedder) (int, error) {
	texts := make([]string, len(batch))
	for j, pt := range batch {
		memory := extractPayloadString(pt.Payload, "memory")
		if memory == "" {
			memory = extractPayloadString(pt.Payload, "memory_content")
		}
		texts[j] = "passage: " + memory
	}

	embeddings, err := emb.Embed(ctx, texts)
	if err != nil {
		return 0, fmt.Errorf("embed: %w", err)
	}

	points := make([]*qdrant.PointStruct, len(batch))
	for j, pt := range batch {
		points[j] = &qdrant.PointStruct{
			Id:      pt.Id,
			Payload: pt.Payload,
			Vectors: qdrant.NewVectorsDense(embeddings[j]),
		}
	}

	wait := true
	if _, err = client.Upsert(ctx, &qdrant.UpsertPoints{
		CollectionName: collection,
		Points:         points,
		Wait:           &wait,
	}); err != nil {
		return 0, fmt.Errorf("upsert: %w", err)
	}
	return len(batch), nil
}

// extractPayloadString extracts a string value from Qdrant payload.
func extractPayloadString(payload map[string]*qdrant.Value, key string) string {
	v, ok := payload[key]
	if !ok || v == nil {
		return ""
	}
	if sv, ok := v.GetKind().(*qdrant.Value_StringValue); ok {
		return sv.StringValue
	}
	// Try JSON marshal for complex types
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return strings.Trim(string(b), `"`)
}
