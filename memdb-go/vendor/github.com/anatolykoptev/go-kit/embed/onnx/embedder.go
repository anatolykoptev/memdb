//go:build cgo

package onnx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/daulet/tokenizers"
	ort "github.com/yalue/onnxruntime_go"

	parentembed "github.com/anatolykoptev/go-kit/embed"
)

const intraOpThreads = 4 // intra-op parallelism: 4 threads within a single ONNX op

// Embedder runs a quantized ONNX embedding model.
// It is safe for concurrent use; inference calls are serialized by a mutex.
type Embedder struct {
	session        *ort.DynamicAdvancedSession
	tokenizer      *tokenizers.Tokenizer
	dim            int
	maxLen         int
	padID          int64
	hasTokenTypeID bool
	logger         *slog.Logger
	mu             sync.Mutex
}

// New loads the ONNX model and HuggingFace tokenizer from cfg.ModelDir.
// Expects model_quantized.onnx and tokenizer.json inside the directory.
// The ONNX Runtime shared library must be at /usr/lib/libonnxruntime.so.
//
// logger=nil falls back to slog.Default().
func New(cfg Config, logger *slog.Logger) (*Embedder, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Point to the shared library. This is a no-op if already set.
	ort.SetSharedLibraryPath("/usr/lib/libonnxruntime.so")

	// InitializeEnvironment must be called once. Ignore "already initialized"
	// errors when loading multiple models in the same process.
	if err := ort.InitializeEnvironment(); err != nil {
		if !strings.Contains(err.Error(), "already been initialized") {
			return nil, fmt.Errorf("onnx: initialize environment: %w", err)
		}
	}

	// Load the HuggingFace tokenizer.
	tokPath := filepath.Join(cfg.ModelDir, "tokenizer.json")
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
	_ = opts.SetIntraOpNumThreads(intraOpThreads)
	_ = opts.SetInterOpNumThreads(1)

	modelPath := filepath.Join(cfg.ModelDir, "model_quantized.onnx")
	inputNames := []string{"input_ids", "attention_mask"}
	if cfg.Model.HasTokenTypeID {
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
		slog.Int("dim", cfg.Model.Dim),
		slog.Int("max_len", cfg.Model.MaxLen),
		slog.Int("pad_id", cfg.Model.PadID),
	)

	return &Embedder{
		session:        session,
		tokenizer:      tk,
		dim:            cfg.Model.Dim,
		maxLen:         cfg.Model.MaxLen,
		padID:          int64(cfg.Model.PadID),
		hasTokenTypeID: cfg.Model.HasTokenTypeID,
		logger:         logger,
	}, nil
}

// Embed produces L2-normalized mean-pooled embeddings for the given texts.
// It returns one vector per input text with dimension matching cfg.Model.Dim.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	start := time.Now()
	outcome := "success"
	defer func() {
		recordEmbed("onnx", outcome, len(texts), time.Since(start))
	}()

	// Check for cancellation before starting work.
	select {
	case <-ctx.Done():
		outcome = "error"
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
		outcome = "error"
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
func (e *Embedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return parentembed.EmbedQueryViaEmbed(ctx, e, text)
}

// Dimension returns the embedding vector dimension.
func (e *Embedder) Dimension() int { return e.dim }

// runInference creates tensors, runs the ONNX session, and extracts the hidden
// state. Caller must hold e.mu.
func (e *Embedder) runInference(
	batchSize, seqLen int,
	inputIDs, attentionMask []int64,
) ([]float32, error) {

	shape := ort.NewShape(int64(batchSize), int64(seqLen))

	idsTensor, err := ort.NewTensor(shape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("onnx: create input_ids tensor: %w", err)
	}
	defer func() { _ = idsTensor.Destroy() }()

	maskTensor, err := ort.NewTensor(shape, attentionMask)
	if err != nil {
		return nil, fmt.Errorf("onnx: create attention_mask tensor: %w", err)
	}
	defer func() { _ = maskTensor.Destroy() }()

	inputs := []ort.Value{idsTensor, maskTensor}

	// BERT-family models need token_type_ids (all zeros for single-segment).
	if e.hasTokenTypeID {
		tokenTypeIDs := make([]int64, len(inputIDs)) // all zeros
		ttTensor, ttErr := ort.NewTensor(shape, tokenTypeIDs)
		if ttErr != nil {
			return nil, fmt.Errorf("onnx: create token_type_ids tensor: %w", ttErr)
		}
		defer func() { _ = ttTensor.Destroy() }()
		inputs = append(inputs, ttTensor)
	}

	// Auto-allocate output: pass nil and let ONNX Runtime determine the shape.
	outputs := []ort.Value{nil}

	if err = e.session.Run(inputs, outputs); err != nil {
		return nil, fmt.Errorf("onnx: session run: %w", err)
	}

	// The output is auto-allocated; we must destroy it when done.
	if outputs[0] == nil {
		return nil, errors.New("onnx: session produced nil output")
	}
	defer func() { _ = outputs[0].Destroy() }()

	// Extract the flat float32 data from the output tensor.
	// The output shape is [batchSize, seqLen, dim].
	outputTensor, ok := outputs[0].(*ort.Tensor[float32])
	if !ok {
		return nil, errors.New("onnx: unexpected output tensor type, expected *Tensor[float32]")
	}

	data := outputTensor.GetData()
	expected := batchSize * seqLen * e.dim
	if len(data) != expected {
		return nil, fmt.Errorf("onnx: output size mismatch: got %d, expected %d (%dx%dx%d)",
			len(data), expected, batchSize, seqLen, e.dim)
	}

	// Copy the data so we can safely destroy the tensor.
	result := make([]float32, len(data))
	copy(result, data)

	return result, nil
}

// Close releases the ONNX session, tokenizer, and runtime environment.
func (e *Embedder) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.session != nil {
		_ = e.session.Destroy()
		e.session = nil
	}
	if e.tokenizer != nil {
		e.tokenizer.Close()
		e.tokenizer = nil
	}

	// DestroyEnvironment cleans up the global ONNX Runtime state.
	// Safe to call even if other sessions have already been destroyed.
	_ = ort.DestroyEnvironment()

	e.logger.Info("onnx embedder closed")
	return nil
}

// Compile-time interface check: Embedder satisfies parent embed.Embedder.
var _ parentembed.Embedder = (*Embedder)(nil)
