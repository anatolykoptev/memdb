//go:build cgo

package embedder

import (
	"errors"
	"fmt"

	ort "github.com/yalue/onnxruntime_go"
)

// runInference creates tensors, runs the ONNX session, and extracts the hidden
// state. Caller must hold e.mu.
func (e *ONNXEmbedder) runInference(
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
		ttTensor, err := ort.NewTensor(shape, tokenTypeIDs)
		if err != nil {
			return nil, fmt.Errorf("onnx: create token_type_ids tensor: %w", err)
		}
		defer func() { _ = ttTensor.Destroy() }()
		inputs = append(inputs, ttTensor)
	}

	// Auto-allocate output: pass nil and let ONNX Runtime determine the shape.
	outputs := []ort.Value{nil}

	err = e.session.Run(inputs, outputs)
	if err != nil {
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
func (e *ONNXEmbedder) Close() error {
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
