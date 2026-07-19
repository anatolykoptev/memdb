// Package llm — modelhealth.go: health-aware model-chain filtering.
//
// Problem this solves. BuildModelChainEndpoints materializes an env-declared
// CSV chain (LLM_MODEL + LLM_MODEL_FALLBACK) into a static []Endpoint with
// ZERO validation against what the proxy actually serves. When an upstream
// provider silently removes a model (e.g. cliproxyapi drops cerebras-qwen-3-235b
// from /v1/models), every request that hits that model burns a fast-503
// round-trip before the chain advances — log noise + wasted requests + the
// human has to hand-edit env to recover. Failure class: static config drift
// against a dynamic provider surface.
//
// How this differs from circuit.go. The CircuitBreaker is REACTIVE: it trips
// only after N consecutive failures observed during live traffic, then re-probes
// after a cooldown — so it still pays the first failed round-trip on every fresh
// request and keeps re-probing a permanently-dead model forever. This filter is
// PROACTIVE: a model absent from the live /v1/models set is dropped from the
// chain BEFORE any request is built, so the dead round-trip never happens.
// The two are complementary, not redundant.
//
// Graceful degradation is the load-bearing invariant. /v1/models being
// unreachable, slow, non-200, empty, or malformed must NEVER turn into a new
// failure mode. In every such case the filter returns the FULL unfiltered
// chain — exactly today's behaviour. The filter is an optimization, never a gate.
//
// Zero external deps (net/http + encoding/json + sync + time), per the package
// header contract.
package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"
)

// DefaultModelRegistryTTL is the cache lifetime for a baseURL's /v1/models set.
// Models do not churn on a sub-minute cadence, so one fetch amortized across
// many requests is the right trade-off; a stale entry at worst leaves a freshly
// dead model in the chain for up to one TTL, which only reverts to today's
// fast-503-then-advance behaviour.
const DefaultModelRegistryTTL = 5 * time.Minute

// defaultModelsFetchTimeout bounds a single /v1/models GET so a hung proxy
// cannot stall chain construction. Construction must stay fast; on timeout we
// degrade to the unfiltered chain.
const defaultModelsFetchTimeout = 3 * time.Second

// ModelFilterEvent describes one BuildModelChainEndpointsFiltered call's outcome.
// It is the observability surface for "a model in my env chain died": Dropped
// lists the chain entries absent from the live /v1/models set, and Degraded
// reports that filtering was skipped (graceful fallback to the full chain).
//
// Mirrors the EndpointAttemptObserver opt-in pattern: the kit emits the event,
// the consumer bumps a Prometheus counter / structured log. The kit adds no
// metric dependency of its own.
type ModelFilterEvent struct {
	// BaseURL is the proxy whose /v1/models was consulted (cache key).
	BaseURL string
	// Requested is the number of endpoints in the unfiltered chain.
	Requested int
	// Kept is the number of endpoints surviving the filter (== Requested when Degraded).
	Kept int
	// Dropped is the model ids removed because they were absent from /v1/models,
	// in chain order. Empty when Degraded with no comparison, or when nothing was dropped.
	Dropped []string
	// Available is the number of model ids the proxy reported (0 when the set
	// could not be obtained).
	Available int
	// Degraded is true when filtering was SKIPPED and the full unfiltered chain
	// was returned. This is the signal "I could not validate the chain" — either
	// /v1/models was unreachable/garbage, or filtering would have emptied the
	// chain. A Degraded=false event with a non-empty Dropped is the signal
	// "an env model died".
	Degraded bool
	// Reason is a short machine-stable token for Degraded (or "" when not degraded):
	// "no_registry", "fetch_failed", "empty_set", "all_filtered". Used for a
	// metric label; not free-form.
	Reason string
}

// ModelFilterObserver is the opt-in callback fired exactly once per
// BuildModelChainEndpointsFiltered call. Must not block (incr a counter / async
// log only) and must not panic — wrap your own recover() if needed; the kit
// does not recover for you, matching EndpointAttemptObserver semantics.
type ModelFilterObserver func(ev ModelFilterEvent)

// Degrade reason tokens (machine-stable; safe as a Prometheus label value).
const (
	filterReasonNoRegistry  = "no_registry"
	filterReasonFetchFail   = "fetch_failed"
	filterReasonEmptySet    = "empty_set"
	filterReasonAllFiltered = "all_filtered"
)

// ModelRegistry caches the set of model ids a proxy serves at {baseURL}/v1/models,
// keyed by baseURL, with a per-key TTL. Safe for concurrent use.
//
// Construction is cheap; the registry holds no goroutines and no background
// refresh — entries are fetched lazily on first use and re-fetched on TTL
// expiry under a per-call request. Share one *ModelRegistry across a service's
// clients so the cache is hit across requests.
type ModelRegistry struct {
	ttl    time.Duration
	client *http.Client

	mu    sync.Mutex
	cache map[string]modelSetEntry
}

type modelSetEntry struct {
	ids       map[string]struct{}
	expiresAt time.Time
}

// ModelRegistryOption configures a ModelRegistry.
type ModelRegistryOption func(*ModelRegistry)

// WithModelRegistryTTL overrides the cache TTL (default DefaultModelRegistryTTL).
// A non-positive value resets to the default.
func WithModelRegistryTTL(ttl time.Duration) ModelRegistryOption {
	return func(r *ModelRegistry) {
		if ttl <= 0 {
			ttl = DefaultModelRegistryTTL
		}
		r.ttl = ttl
	}
}

// WithModelRegistryHTTPClient overrides the HTTP client used to fetch
// /v1/models. Useful for injecting a proxy-pool transport or a test client.
// A nil client is ignored (keeps the default bounded-timeout client).
func WithModelRegistryHTTPClient(hc *http.Client) ModelRegistryOption {
	return func(r *ModelRegistry) {
		if hc != nil {
			r.client = hc
		}
	}
}

// NewModelRegistry constructs a registry with a default 5-minute TTL and a
// bounded-timeout HTTP client.
func NewModelRegistry(opts ...ModelRegistryOption) *ModelRegistry {
	r := &ModelRegistry{
		ttl:    DefaultModelRegistryTTL,
		client: &http.Client{Timeout: defaultModelsFetchTimeout},
		cache:  make(map[string]modelSetEntry),
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// modelsResponse is the OpenAI-compatible /v1/models shape: {"data":[{"id":...}]}.
type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// available returns the set of model ids the proxy at baseURL currently serves,
// using the cache when fresh.
//
//   - fetched=false  → the GET failed at the transport level / non-200 /
//     unparseable body. The set is unknown; caller degrades (fetch_failed).
//   - fetched=true, len 0 → the proxy answered 200 with a parseable body that
//     carried no usable ids (empty {"data":[]} or a non-data shape). The proxy
//     is up but reports nothing; caller degrades (empty_set) — a distinct,
//     more actionable signal than a dead proxy.
//   - fetched=true, len>0 → the live set; caller filters against it.
//
// Neither a failed fetch nor an empty-but-valid result is cached (both are
// suspicious/transient), so the next call re-attempts. Only a non-empty result
// is cached for the registry TTL. The fetch happens under the registry lock to
// collapse a herd of concurrent cold callers into a single GET per baseURL per
// TTL window.
func (r *ModelRegistry) available(ctx context.Context, baseURL string, apiKey string) (ids map[string]struct{}, fetched bool) {
	if r == nil {
		return nil, false
	}
	key := normalizeBaseURL(baseURL)

	r.mu.Lock()
	defer r.mu.Unlock()

	if e, ok := r.cache[key]; ok && time.Now().Before(e.expiresAt) {
		return e.ids, true
	}

	got, ok := r.fetch(ctx, key, apiKey)
	if !ok {
		return nil, false // transport/non-200/unparseable — unknown set.
	}
	if len(got) == 0 {
		return got, true // up but empty — do not cache; surface as empty_set.
	}
	r.cache[key] = modelSetEntry{ids: got, expiresAt: time.Now().Add(r.ttl)}
	return got, true
}

// fetch GETs {baseURL}/v1/models and parses the id set. Returns ok=false on any
// transport error, non-200 status, or unparseable body. Caller holds r.mu.
func (r *ModelRegistry) fetch(ctx context.Context, baseURL string, apiKey string) (map[string]struct{}, bool) {
	// Bound the fetch even if the injected client has no timeout, and even if
	// the caller's ctx has none — construction must not hang on a dead proxy.
	fctx, cancel := context.WithTimeout(ctx, defaultModelsFetchTimeout)
	defer cancel()

	url := strings.TrimRight(baseURL, "/") + "/v1/models"
	req, err := http.NewRequestWithContext(fctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var mr modelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, false
	}
	ids := make(map[string]struct{}, len(mr.Data))
	for _, m := range mr.Data {
		if m.ID != "" {
			ids[m.ID] = struct{}{}
		}
	}
	return ids, true
}

// normalizeBaseURL strips a trailing /v1 or /v1/ and trailing slashes so that
// callers passing either "http://host:8317" or "http://host:8317/v1" share one
// cache entry and resolve the same /v1/models URL. The fetch always appends
// /v1/models to the normalized root.
func normalizeBaseURL(u string) string {
	u = strings.TrimRight(u, "/")
	u = strings.TrimSuffix(u, "/v1")
	return strings.TrimRight(u, "/")
}

// BuildModelChainEndpointsFiltered builds the same chain as
// BuildModelChainEndpoints, then drops every endpoint whose Model is absent
// from the proxy's live /v1/models set — preserving order. It is the
// health-aware, opt-in variant; the static BuildModelChainEndpoints is
// unchanged and existing callers are unaffected.
//
// Graceful degradation (mandatory): if registry is nil, /v1/models cannot be
// obtained (unreachable / non-200 / malformed / empty), OR filtering would
// empty the chain (every model absent — almost always a registry hiccup, not
// a real config wipe), the FULL unfiltered chain is returned unchanged. The
// filter never produces an empty chain and never introduces a new failure mode.
//
// obs (may be nil) fires exactly once with the outcome — Dropped models on the
// happy path, or Degraded+Reason on a fallback. Use it to bump a counter so the
// operator sees "N models skipped as absent from /v1/models".
//
// The fetch is cached on the registry for its TTL, so threading the same
// *ModelRegistry through every BuildModelChainEndpointsFiltered call costs one
// GET per baseURL per TTL window, not one per request.
func BuildModelChainEndpointsFiltered(
	ctx context.Context,
	registry *ModelRegistry,
	baseURL, apiKey, primary string,
	chain []string,
	obs ModelFilterObserver,
) []Endpoint {
	full := BuildModelChainEndpoints(baseURL, apiKey, primary, chain)

	emit := func(ev ModelFilterEvent) {
		if obs != nil {
			obs(ev)
		}
	}

	if registry == nil {
		emit(ModelFilterEvent{
			BaseURL: baseURL, Requested: len(full), Kept: len(full),
			Degraded: true, Reason: filterReasonNoRegistry,
		})
		return full
	}

	// Cache key is baseURL only: cliproxyapi returns identical model sets for all valid keys.
	// If a future proxy returns key-scoped model sets, include a key hash here.
	ids, ok := registry.available(ctx, baseURL, apiKey)
	if !ok {
		// Could not obtain a usable live set — degrade to the full chain.
		emit(ModelFilterEvent{
			BaseURL: baseURL, Requested: len(full), Kept: len(full),
			Degraded: true, Reason: filterReasonFetchFail,
		})
		return full
	}
	if len(ids) == 0 {
		emit(ModelFilterEvent{
			BaseURL: baseURL, Requested: len(full), Kept: len(full),
			Available: 0, Degraded: true, Reason: filterReasonEmptySet,
		})
		return full
	}

	kept := make([]Endpoint, 0, len(full))
	var dropped []string
	for _, ep := range full {
		if _, present := ids[ep.Model]; present {
			kept = append(kept, ep)
		} else {
			dropped = append(dropped, ep.Model)
		}
	}

	// All-filtered guard: never hand back an empty chain. An empty result almost
	// always means the live set is bogus (e.g. a proxy that 200s /v1/models with
	// a different id scheme than the chat route), not that every configured model
	// truly vanished. Degrade to the full chain and flag it loudly.
	if len(kept) == 0 {
		emit(ModelFilterEvent{
			BaseURL: baseURL, Requested: len(full), Kept: len(full),
			Available: len(ids), Dropped: dropped,
			Degraded: true, Reason: filterReasonAllFiltered,
		})
		return full
	}

	emit(ModelFilterEvent{
		BaseURL: baseURL, Requested: len(full), Kept: len(kept),
		Available: len(ids), Dropped: dropped, Degraded: false,
	})
	return kept
}

// BuildMultiProxyEndpointsFiltered builds the same cross-product as
// BuildMultiProxyEndpoints, then for each proxy drops endpoints whose Model
// is absent from that proxy's live /v1/models set. Each proxy is filtered
// independently (they may serve different model sets). Order is preserved:
// proxy1:primary, proxy1:fallbacks..., proxy2:primary, proxy2:fallbacks...
//
// Graceful degradation: if registry is nil, a proxy's /v1/models fetch fails,
// or filtering would empty that proxy's segment, the FULL unfiltered segment
// for that proxy is kept. The filter never produces an empty chain.
//
// obs (may be nil) fires once per proxy with that proxy's outcome.
func BuildMultiProxyEndpointsFiltered(
	ctx context.Context,
	registry *ModelRegistry,
	proxies []ProxySpec,
	primary string,
	chain []string,
	obs ModelFilterObserver,
) []Endpoint {
	full := BuildMultiProxyEndpoints(proxies, primary, chain)

	if len(proxies) <= 1 {
		// Single proxy: delegate to the single-proxy filtered builder for
		// the standard observer event semantics.
		if len(proxies) == 0 {
			return full
		}
		return BuildModelChainEndpointsFiltered(ctx, registry,
			proxies[0].URL, proxies[0].Key, primary, chain, obs)
	}

	emit := func(ev ModelFilterEvent) {
		if obs != nil {
			obs(ev)
		}
	}

	if registry == nil {
		for _, p := range proxies {
			emit(ModelFilterEvent{
				BaseURL: p.URL, Requested: countModels(primary, chain),
				Kept:     countModels(primary, chain),
				Degraded: true, Reason: filterReasonNoRegistry,
			})
		}
		return full
	}

	out := make([]Endpoint, 0, len(full))
	for _, p := range proxies {
		segment := BuildModelChainEndpoints(p.URL, p.Key, primary, chain)

		ids, ok := registry.available(ctx, p.URL, p.Key)
		if !ok {
			emit(ModelFilterEvent{
				BaseURL: p.URL, Requested: len(segment), Kept: len(segment),
				Degraded: true, Reason: filterReasonFetchFail,
			})
			out = append(out, segment...)
			continue
		}
		if len(ids) == 0 {
			emit(ModelFilterEvent{
				BaseURL: p.URL, Requested: len(segment), Kept: len(segment),
				Available: 0, Degraded: true, Reason: filterReasonEmptySet,
			})
			out = append(out, segment...)
			continue
		}

		kept := make([]Endpoint, 0, len(segment))
		var dropped []string
		for _, ep := range segment {
			if _, present := ids[ep.Model]; present {
				kept = append(kept, ep)
			} else {
				dropped = append(dropped, ep.Model)
			}
		}
		if len(kept) == 0 {
			emit(ModelFilterEvent{
				BaseURL: p.URL, Requested: len(segment), Kept: len(segment),
				Available: len(ids), Dropped: dropped,
				Degraded: true, Reason: filterReasonAllFiltered,
			})
			out = append(out, segment...)
			continue
		}

		emit(ModelFilterEvent{
			BaseURL: p.URL, Requested: len(segment), Kept: len(kept),
			Available: len(ids), Dropped: dropped, Degraded: false,
		})
		out = append(out, kept...)
	}
	return out
}

// countModels returns the number of unique models in primary + chain
// (matching BuildModelChainEndpoints deduplication).
func countModels(primary string, chain []string) int {
	seen := make(map[string]struct{}, 1+len(chain))
	count := 0
	if primary != "" {
		seen[primary] = struct{}{}
		count++
	}
	for _, m := range chain {
		if m == "" {
			continue
		}
		if _, dup := seen[m]; dup {
			continue
		}
		seen[m] = struct{}{}
		count++
	}
	return count
}
