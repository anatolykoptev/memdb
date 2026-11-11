package embed

import "context"

// Prefixed wraps an Embedder so every text passed to Embed / EmbedQuery is
// prepended with prefix before reaching the underlying backend. An empty
// prefix returns the embedder unchanged.
//
// This is the composition-over-configuration alternative to per-backend
// prefix options (WithTextPrefix / WithQueryPrefix on OllamaClient). It works
// with ANY Embedder — Ollama, Voyage, HTTP, ONNX, or a *Client — and lets a
// caller share one backend instance across two prefix conventions without
// creating two HTTP connection pools:
//
//	base, _ := embed.NewClient(url, embed.WithOllama(model))
//	docs := embed.Prefixed(base, embed.E5PassagePrefix) // storage side
//	queries := embed.Prefixed(base, embed.E5QueryPrefix) // retrieval side
//
// The decorator does NOT combine with per-backend prefix options — if the
// underlying embedder already applies its own prefix (e.g. OllamaClient with
// WithTextPrefix set), wrapping it with Prefixed will double-prefix. Use one
// or the other, not both.
//
// See issue #259 for why the prefix convention must be explicit and checkable.
func Prefixed(e Embedder, prefix string) Embedder {
	if prefix == "" {
		return e
	}
	return &prefixed{inner: e, prefix: prefix}
}

// prefixed is the decorator returned by Prefixed. It implements Embedder and
// the optional modelGetter interface so modelFromEmbedder resolves through it
// transparently.
type prefixed struct {
	inner  Embedder
	prefix string
}

// Embed prepends prefix to every text and delegates to the inner Embedder.
func (p *prefixed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return p.inner.Embed(ctx, texts)
	}
	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = p.prefix + t
	}
	return p.inner.Embed(ctx, prefixed)
}

// EmbedQuery prepends prefix to the query text and delegates to the inner
// Embedder's EmbedQuery.
func (p *prefixed) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return p.inner.EmbedQuery(ctx, p.prefix+text)
}

// Dimension delegates to the inner Embedder.
func (p *prefixed) Dimension() int { return p.inner.Dimension() }

// Close delegates to the inner Embedder.
func (p *prefixed) Close() error { return p.inner.Close() }

// Model delegates to the inner Embedder via modelFromEmbedder so the
// decorator is transparent to modelFromEmbedder's resolution chain.
func (p *prefixed) Model() string { return modelFromEmbedder(p.inner) }

// MaxBatchSize delegates to the inner Embedder when it implements BatchSizer,
// so the decorator is transparent to the chunk-size resolution in
// newClientFromInternal. Returns 0 when the inner embedder doesn't implement
// BatchSizer — the caller falls back to defaultChunkSize.
func (p *prefixed) MaxBatchSize() int {
	if bs, ok := p.inner.(BatchSizer); ok {
		return bs.MaxBatchSize()
	}
	return 0
}

// Compile-time interface checks.
var (
	_ Embedder   = (*prefixed)(nil)
	_ BatchSizer = (*prefixed)(nil)
)
