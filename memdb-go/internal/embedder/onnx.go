//go:build cgo

// Package embedder provides text embedding backends.
package embedder

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"
)

const onnxIntraOpThreads = 4 // intra-op parallelism: 4 threads within a single ONNX op

// ONNXEmbedder runs a quantized ONNX embedding model.
// It is safe for concurrent use; inference calls are serialized by a mutex.
type ONNXEmbedder struct {
	session   *ort.DynamicAdvancedSession
	tokenizer *tokenizers.Tokenizer
	dim            int
	maxLen         int
	padID          int64
	hasTokenTypeID bool
	logger         *slog.Logger
	mu             sync.Mutex
}

// NewONNXEmbedder loads the ONNX model and HuggingFace tokenizer from modelDir.
// Expects model_quantized.onnx and tokenizer.json inside modelDir.
// The ONNX Runtime shared library must be at /usr/lib/libonnxruntime.so.
func NewONNXEmbedder(modelDir string, cfg ONNXModelConfig, logger *slog.Logger) (*ONNXEmbedder, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Point to the shared library. This is a no-op if already set.
	ort.SetSharedLibraryPath("/usr/lib/libonnxruntime.so")

	// InitializeEnvironment is idempotent — returns nil on subsequent calls.
	if err := ort.InitializeEnvironment(); err != nil {
		return nil, fmt.Errorf("onnx: initialize environment: %w", err)
	}

	// Load the HuggingFace tokenizer.
	tokPath := filepath.Join(modelDir, "tokenizer.json")
	tk, err := tokenizers.FromFile(tokPath)
	if err != nil {
		return nil, fmt.Errorf("onnx: load tokenizer %s: %w", tokPath, err)
	}

	// Configure session: use 4 intra-op threads (within a single op) and
	// 1 inter-op thread (we serialize at the Go level anyway).
	opts, err := ort.NewSessionOptions()
	if err != nil {
		tk.Close()
		return nil, fmt.Errorf("onnx: create session options: %w", err)
	}
	_ = opts.SetIntraOpNumThreads(onnxIntraOpThreads)
	_ = opts.SetInterOpNumThreads(1)

	modelPath := filepath.Join(modelDir, "model_quantized.onnx")
	inputNames := []string{"input_ids", "attention_mask"}
	if cfg.HasTokenTypeID {
		inputNames = append(inputNames, "token_type_ids")
	}
	outputNames := []string{"last_hidden_state"}

	session, err := ort.NewDynamicAdvancedSession(
		modelPath,
		inputNames,
		outputNames,
		opts,
	)
	if err != nil {
		_ = opts.Destroy()
		tk.Close()
		return nil, fmt.Errorf("onnx: create session from %s: %w", modelPath, err)
	}
	// Session owns the options after creation; we do not destroy opts separately.

	logger.Info("onnx embedder loaded",
		slog.String("model", modelPath),
		slog.Int("dim", cfg.Dim),
		slog.Int("max_len", cfg.MaxLen),
		slog.Int("pad_id", cfg.PadID),
	)

	return &ONNXEmbedder{
		session:        session,
		tokenizer:      tk,
		dim:            cfg.Dim,
		maxLen:         cfg.MaxLen,
		padID:          int64(cfg.PadID),
		hasTokenTypeID: cfg.HasTokenTypeID,
		logger:         logger,
	}, nil
}

// Embed produces L2-normalized mean-pooled embeddings for the given texts.
// It returns one vector per input text with dimension matching the model config.
func (e *ONNXEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	// Check for cancellation before starting work.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	batchSize := len(texts)

	// --- Tokenize all texts ---------------------------------------------------
	allIDs := make([][]uint32, batchSize)
	allMasks := make([][]uint32, batchSize)
	maxSeqLen := 0

	encOpts := []tokenizers.EncodeOption{
		tokenizers.WithReturnAttentionMask(),
	}

	for i, text := range texts {
		enc := e.tokenizer.EncodeWithOptions(text, true, encOpts...)
		ids := enc.IDs
		mask := enc.AttentionMask

		// Truncate to maxLen if needed.
		if len(ids) > e.maxLen {
			ids = ids[:e.maxLen]
			mask = mask[:e.maxLen]
		}

		allIDs[i] = ids
		allMasks[i] = mask

		if len(ids) > maxSeqLen {
			maxSeqLen = len(ids)
		}
	}

	if maxSeqLen == 0 {
		// All texts tokenized to zero length — return zero vectors.
		result := make([][]float32, batchSize)
		for i := range result {
			result[i] = make([]float32, e.dim)
		}
		return result, nil
	}

	// --- Pad to maxSeqLen and build flat int64 slices -------------------------
	totalTokens := batchSize * maxSeqLen
	inputIDs := make([]int64, totalTokens)
	attentionMask := make([]int64, totalTokens)

	for b := 0; b < batchSize; b++ {
		offset := b * maxSeqLen
		seqLen := len(allIDs[b])

		for s := 0; s < seqLen; s++ {
			inputIDs[offset+s] = int64(allIDs[b][s])
			attentionMask[offset+s] = int64(allMasks[b][s])
		}
		// Pad the remainder with the model's pad token ID and attention mask=0.
		for s := seqLen; s < maxSeqLen; s++ {
			inputIDs[offset+s] = e.padID
			attentionMask[offset+s] = 0
		}
	}

	// --- Run ONNX inference (serialized) --------------------------------------
	e.mu.Lock()
	hidden, err := e.runInference(batchSize, maxSeqLen, inputIDs, attentionMask)
	e.mu.Unlock()

	if err != nil {
		return nil, err
	}

	// --- Mean pool with attention mask and L2 normalize -----------------------
	embeddings := meanPool(hidden, attentionMask, batchSize, maxSeqLen, e.dim)

	e.logger.Debug("onnx embed complete",
		slog.Int("texts", batchSize),
		slog.Int("seq_len", maxSeqLen),
	)

	return embeddings, nil
}

// EmbedQuery embeds a single query string (search/retrieval use case).
// Delegates to Embed — ONNX model handles query vs document identically.
func (e *ONNXEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return EmbedQueryViaEmbed(ctx, e, text)
}

// Dimension returns the embedding vector dimension.
func (e *ONNXEmbedder) Dimension() int { return e.dim }
