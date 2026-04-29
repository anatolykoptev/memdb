package onnx

// ModelConfig holds model-specific parameters for the ONNX backend.
//
// Field semantics:
//
//   - Dim            — output embedding dimension (e.g. 1024 for e5, 768 for jina)
//   - MaxLen         — max token sequence length (truncation cap)
//   - PadID          — tokenizer pad token ID
//   - HasTokenTypeID — model expects token_type_ids input (BERT family)
type ModelConfig struct {
	Dim            int
	MaxLen         int
	PadID          int
	HasTokenTypeID bool
}

// knownModels maps model names to their canonical configurations. Callers
// typically look up by name from KnownModels() and pass the result to New.
var knownModels = map[string]ModelConfig{
	"multilingual-e5-large": {Dim: 1024, MaxLen: 512, PadID: 1},
	"jina-code-v2":          {Dim: 768, MaxLen: 512, PadID: 0, HasTokenTypeID: true},
}

// DefaultModelConfig returns the multilingual-e5-large config — the legacy
// default used by memdb-go before multi-model support landed.
func DefaultModelConfig() ModelConfig {
	return knownModels["multilingual-e5-large"]
}

// KnownModels returns a copy of the model registry. Callers receive a snapshot
// — modifying the returned map does not affect the package-level registry.
func KnownModels() map[string]ModelConfig {
	out := make(map[string]ModelConfig, len(knownModels))
	for k, v := range knownModels {
		out[k] = v
	}
	return out
}

// Config is the public constructor input for New.
type Config struct {
	ModelDir string      // filesystem path containing model_quantized.onnx + tokenizer.json
	Model    ModelConfig // tokenizer / dimension parameters for the model in ModelDir
}
