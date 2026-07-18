//go:build cgo

// Package main — reembed-atomic: M11 F8 one-shot atomic-fact re-extraction.
//
// For a single cube, fetch every kind='paragraph_legacy' Memory row, run
// each through the mem0 ADDITIVE_EXTRACTION_PROMPT, and INSERT the
// resulting atomic facts as new rows with kind='atomic_fact'. Old rows
// are NEVER deleted — they remain a fallback for the env-off path. The
// new rows carry properties.info.superseded_legacy_id pointing back at
// their source row so audit can trace the lineage.
//
// Usage:
//
//	reembed-atomic --cube <cube_id> [--limit N] [--dry-run]
//
// Environment variables:
//   - MEMDB_POSTGRES_URL   — required
//   - MEMDB_LLM_BASE_URL   — required (CLIProxyAPI :8317 typically)
//   - MEMDB_LLM_API_KEY    — required
//   - MEMDB_LLM_MODEL      — defaults to gemini-2.5-flash
//   - MEMDB_ONNX_MODEL_DIR — required for embeddings (defaults /models)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/anatolykoptev/memdb/memdb-go/internal/embedder"
	"github.com/anatolykoptev/memdb/memdb-go/internal/llm"
)

const graphName = "memos_graph"

func main() {
	var (
		cube    = flag.String("cube", "", "cube id (REQUIRED)")
		limit   = flag.Int("limit", 0, "max paragraph_legacy rows to re-extract (0=all)")
		dryRun  = flag.Bool("dry-run", false, "do not write atomic_fact rows; just print extraction stats")
		timeout = flag.Duration("timeout", 60*time.Second, "per-extraction LLM timeout")
		force   = flag.Bool("force", false, "re-run even if cube already has atomic_fact rows (will create duplicates)")
	)
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if *cube == "" {
		logger.Error("--cube is required")
		os.Exit(2)
	}

	pgURL := os.Getenv("MEMDB_POSTGRES_URL")
	llmURL := os.Getenv("MEMDB_LLM_BASE_URL")
	llmKey := os.Getenv("MEMDB_LLM_API_KEY")
	llmModel := os.Getenv("MEMDB_LLM_MODEL")
	if llmModel == "" {
		llmModel = "gemini-2.5-flash"
	}
	modelDir := os.Getenv("MEMDB_ONNX_MODEL_DIR")
	if modelDir == "" {
		modelDir = "/models"
	}
	if pgURL == "" || llmURL == "" || llmKey == "" {
		logger.Error("MEMDB_POSTGRES_URL / MEMDB_LLM_BASE_URL / MEMDB_LLM_API_KEY required")
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()

	emb, err := embedder.NewONNXEmbedder(modelDir, embedder.DefaultONNXConfig(), logger)
	if err != nil {
		logger.Error("embedder init failed", slog.Any("error", err))
		os.Exit(1)
	}
	defer emb.Close()

	pgCfg, err := pgxpool.ParseConfig(pgURL)
	if err != nil {
		logger.Error("invalid postgres URL", slog.Any("error", err))
		os.Exit(1)
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

	llmClient := llm.NewClient(llmURL, llmKey, llmModel, nil, logger)
	atomic := llm.NewAtomicExtractor(llmClient)

	// Idempotency guard — refuse to re-run on a cube that already has atomic
	// rows unless --force is passed. Without this guard a second invocation
	// silently doubles the atomic-fact corpus and skews every M11 metric.
	{
		var existing int
		countQ := fmt.Sprintf(`
			SELECT COUNT(*) FROM "%s"."Memory" m
			WHERE m.properties->>'user_name' = $1
			  AND COALESCE(NULLIF(m.properties->>'kind',''), 'paragraph_legacy') = 'atomic_fact'
		`, graphName)
		if err := pool.QueryRow(ctx, countQ, *cube).Scan(&existing); err != nil {
			logger.Error("idempotency check failed", slog.Any("error", err))
			os.Exit(1)
		}
		if existing > 0 && !*force && !*dryRun {
			logger.Error("cube already has atomic facts — refusing to re-run",
				slog.Int("existing_atomic_facts", existing),
				slog.String("cube", *cube),
				slog.String("hint", "pass --force to re-process anyway (will create duplicates)"))
			os.Exit(2)
		}
		if existing > 0 {
			logger.Warn("cube already has atomic facts — proceeding due to --force/--dry-run",
				slog.Int("existing_atomic_facts", existing),
				slog.Bool("force", *force),
				slog.Bool("dry_run", *dryRun))
		}
	}

	type srcRow struct {
		ID     string
		Memory string
	}
	q := fmt.Sprintf(`
		SELECT m.properties->>'id', m.properties->>'memory'
		FROM "%s"."Memory" m
		WHERE COALESCE(NULLIF(m.properties->>'kind',''), 'paragraph_legacy') = 'paragraph_legacy'
		  AND m.properties->>'user_name' = $1
		  AND m.properties->>'memory_type' IN ('LongTermMemory','UserMemory')
		  AND m.properties->>'status' = 'activated'
		ORDER BY m.properties->>'created_at' ASC
	`, graphName)
	rows, err := pool.Query(ctx, q, *cube)
	if err != nil {
		logger.Error("query legacy rows failed", slog.Any("error", err))
		os.Exit(1)
	}
	var src []srcRow
	for rows.Next() {
		var r srcRow
		if err := rows.Scan(&r.ID, &r.Memory); err != nil {
			logger.Error("scan failed", slog.Any("error", err))
			os.Exit(1)
		}
		src = append(src, r)
		if *limit > 0 && len(src) >= *limit {
			break
		}
	}
	rows.Close()
	logger.Info("loaded legacy rows", slog.Int("count", len(src)), slog.String("cube", *cube))

	totalAtomic := 0
	insQ := fmt.Sprintf(`INSERT INTO "%s"."Memory"(properties, embedding) VALUES ($1::text::agtype, $2::vector(1024))`, graphName)
	for i, r := range src {
		ictx, icancel := context.WithTimeout(ctx, *timeout)
		facts, err := atomic.ExtractAtomicFacts(ictx, "user: "+r.Memory, nil, "")
		icancel()
		if err != nil {
			logger.Warn("extract failed",
				slog.String("legacy_id", r.ID),
				slog.Any("error", err))
			continue
		}
		if len(facts) == 0 {
			continue
		}
		// Embed all texts in one call.
		texts := make([]string, len(facts))
		for j, f := range facts {
			texts[j] = "passage: " + f.Text
		}
		embs, err := emb.Embed(ctx, texts)
		if err != nil {
			logger.Warn("embed failed", slog.Any("error", err))
			continue
		}

		newIDs := make([]string, 0, len(facts))
		for j, f := range facts {
			newID := uuid.New().String()
			newIDs = append(newIDs, newID)
			if *dryRun {
				continue
			}
			props := map[string]any{
				"id":          newID,
				"memory":      f.Text,
				"memory_type": "LongTermMemory",
				"status":      "activated",
				"user_name":   *cube,
				"created_at":  time.Now().UTC().Format(time.RFC3339),
				"updated_at":  time.Now().UTC().Format(time.RFC3339),
				// kind / linked_memory_ids / attributed_to MUST live at top level —
				// migration 0022's GENERATED column reads properties->>'kind',
				// idx_memory_linked_ids is a GIN index on (properties -> 'linked_memory_ids'),
				// and idx_memory_attributed_to is a partial index on
				// (properties->>'attributed_to'). Nesting them under `info`
				// makes every atomic row degrade to kind='paragraph_legacy'.
				"kind":       "atomic_fact",
				"tags":       []string{"mode:fine", "atomic_fact"},
				"sources":    []string{},
				"info":       map[string]any{"superseded_legacy_id": r.ID, "attributed_to": f.AttributedTo, "linked_memory_ids": f.LinkedMemoryIDs},
				"confidence": 0.9,
				"type":       "fact",
				"raw_text":   f.Text,
			}
			if f.AttributedTo != "" {
				props["attributed_to"] = f.AttributedTo
			}
			if len(f.LinkedMemoryIDs) > 0 {
				props["linked_memory_ids"] = f.LinkedMemoryIDs
			}
			pj, _ := json.Marshal(props)
			vec := db.FormatVector(embs[j])
			if _, err := pool.Exec(ctx, insQ, pj, vec); err != nil {
				logger.Warn("insert atomic fact failed",
					slog.String("new_id", newID),
					slog.Any("error", err))
				continue
			}
		}
		totalAtomic += len(newIDs)
		logger.Info("re-extracted",
			slog.Int("idx", i),
			slog.String("legacy_id", r.ID),
			slog.Int("facts", len(newIDs)),
		)
	}
	logger.Info("done",
		slog.Int("legacy_rows", len(src)),
		slog.Int("atomic_facts_written", totalAtomic),
		slog.Duration("elapsed", time.Since(start)),
		slog.Bool("dry_run", *dryRun),
	)
}
