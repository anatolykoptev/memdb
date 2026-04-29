// Backward-compat shim: every public symbol in this file is a thin
// re-export of github.com/anatolykoptev/go-kit/embed. Deprecation cycle
// ends in M13 — new code MUST import go-kit/embed directly.

package embedder

import (
	"github.com/anatolykoptev/go-kit/embed"
)

// Embedder is an alias for [embed.Embedder].
//
// Deprecated: import github.com/anatolykoptev/go-kit/embed and use embed.Embedder.
type Embedder = embed.Embedder

// Config is an alias for [embed.Config].
//
// Deprecated: import github.com/anatolykoptev/go-kit/embed and use embed.Config.
type Config = embed.Config

// HTTPEmbedder is an alias for [embed.HTTPEmbedder].
//
// Deprecated: import github.com/anatolykoptev/go-kit/embed.
type HTTPEmbedder = embed.HTTPEmbedder

// OllamaClient is an alias for [embed.OllamaClient].
//
// Deprecated: import github.com/anatolykoptev/go-kit/embed.
type OllamaClient = embed.OllamaClient

// OllamaOption is an alias for [embed.OllamaOption].
//
// Deprecated: import github.com/anatolykoptev/go-kit/embed.
type OllamaOption = embed.OllamaOption

// VoyageClient is an alias for [embed.VoyageClient].
//
// Deprecated: import github.com/anatolykoptev/go-kit/embed.
type VoyageClient = embed.VoyageClient

// Registry is an alias for [embed.Registry].
//
// Deprecated: import github.com/anatolykoptev/go-kit/embed.
type Registry = embed.Registry

// New is a re-export of [embed.New].
//
// Deprecated: call embed.New directly.
var New = embed.New

// EmbedQueryViaEmbed is a re-export of [embed.EmbedQueryViaEmbed].
//
// Deprecated: call embed.EmbedQueryViaEmbed directly.
var EmbedQueryViaEmbed = embed.EmbedQueryViaEmbed

// NewHTTPEmbedder is a re-export of [embed.NewHTTPEmbedder].
//
// Deprecated: call embed.NewHTTPEmbedder directly.
var NewHTTPEmbedder = embed.NewHTTPEmbedder

// NewOllamaClient is a re-export of [embed.NewOllamaClient].
//
// Deprecated: call embed.NewOllamaClient directly.
var NewOllamaClient = embed.NewOllamaClient

// NewVoyageClient is a re-export of [embed.NewVoyageClient].
//
// Deprecated: call embed.NewVoyageClient directly.
var NewVoyageClient = embed.NewVoyageClient

// NewRegistry is a re-export of [embed.NewRegistry].
//
// Deprecated: call embed.NewRegistry directly.
var NewRegistry = embed.NewRegistry

// WithOllamaDimension is a re-export of [embed.WithOllamaDimension].
//
// Deprecated: call embed.WithOllamaDimension directly.
var WithOllamaDimension = embed.WithOllamaDimension

// WithOllamaTimeout is a re-export of [embed.WithOllamaTimeout].
//
// Deprecated: call embed.WithOllamaTimeout directly.
var WithOllamaTimeout = embed.WithOllamaTimeout

// WithTextPrefix is a re-export of [embed.WithTextPrefix].
//
// Deprecated: call embed.WithTextPrefix directly.
var WithTextPrefix = embed.WithTextPrefix

// WithQueryPrefix is a re-export of [embed.WithQueryPrefix].
//
// Deprecated: call embed.WithQueryPrefix directly.
var WithQueryPrefix = embed.WithQueryPrefix

// WithNormalizeL2 is a re-export of [embed.WithNormalizeL2].
//
// Deprecated: call embed.WithNormalizeL2 directly.
var WithNormalizeL2 = embed.WithNormalizeL2
