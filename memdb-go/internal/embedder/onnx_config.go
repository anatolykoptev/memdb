package embedder

// ONNXModelConfig holds model-specific parameters.
type ONNXModelConfig struct {
	Dim            int  // output embedding dimension
	MaxLen         int  // max token sequence length
	PadID          int  // tokenizer pad token ID
	HasTokenTypeID bool // model expects token_type_ids input (BERT-family)
}

// knownONNXModels maps model names to their configurations.
var knownONNXModels = map[string]ONNXModelConfig{
	"multilingual-e5-large": {Dim: 1024, MaxLen: 512, PadID: 1},
	"jina-code-v2":          {Dim: 768, MaxLen: 512, PadID: 0, HasTokenTypeID: true},
}

// DefaultONNXConfig returns the e5-large config for backward compatibility.
func DefaultONNXConfig() ONNXModelConfig {
	return knownONNXModels["multilingual-e5-large"]
}

// KnownONNXModels returns the map of known model configurations.
func KnownONNXModels() map[string]ONNXModelConfig { return knownONNXModels }
