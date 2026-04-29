package rerank

// ServerNormalize names the embed-server normalization mode sent in the
// `normalize` field of the /v1/rerank request body.
//
// The server applies normalization once for all callers; this is preferred
// over client-side post-processing for sigmoid because a single server
// implementation avoids divergence across callers.
//
// Requires embed-server >= 2026-04-29 (commit 3713ceb on main of embed-server).
// Providers that do not recognise the field tolerate it silently via omitempty
// when the value is empty.
type ServerNormalize string

const (
	// ServerNormalizeNone is the default — the field is omitted from the
	// request body (omitempty). The server returns raw logits, which is the
	// Cohere-compatible default behaviour.
	ServerNormalizeNone ServerNormalize = ""

	// ServerNormalizeSigmoid requests sigmoid normalisation: 1/(1+exp(-x)),
	// clamped at ±50 to avoid overflow. Returns scores in [0, 1].
	// Prefer this over client-side sigmoid to keep the implementation in one
	// place.
	ServerNormalizeSigmoid ServerNormalize = "sigmoid"
)

// WithServerNormalize sets the `normalize` field in /v1/rerank requests sent
// to embed-server. The default (ServerNormalizeNone / "") omits the field so
// that Cohere-shape providers that do not understand it are unaffected.
func WithServerNormalize(mode ServerNormalize) Opt {
	return func(c *cfgInternal) { c.serverNormalize = string(mode) }
}
