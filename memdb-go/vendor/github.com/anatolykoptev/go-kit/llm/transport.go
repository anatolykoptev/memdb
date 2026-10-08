package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"
)

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content,omitempty"`
			ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage,omitempty"`
}

func (c *Client) doWithRetry(ctx context.Context, baseURL, apiKey string, req *ChatRequest) (*ChatResponse, error) {
	delay := retryDelay
	var lastErr error

	for attempt := range c.maxRetries {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(delay):
			}
			delay = min(delay*2, maxRetryDelay)
		}

		result, err := c.doRequest(ctx, baseURL, apiKey, req)
		if err == nil {
			return result, nil
		}
		lastErr = err

		// Only retry on retryable errors.
		if !asRetryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

func (c *Client) doRequest(ctx context.Context, baseURL, apiKey string, req *ChatRequest) (*ChatResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := c.httpClient.Do(httpReq) //nolint:gosec // G704: URL comes from caller config, not user input
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			slog.Warn("llm: response body close failed",
				slog.String("url", baseURL+"/chat/completions"),
				slog.Any("error", err),
			)
		}
	}()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, newAPIError(resp.StatusCode, string(respBody), isRetryableStatus(resp.StatusCode), parseRetryAfter(resp.Header.Get("Retry-After")))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return nil, errors.New("llm: empty choices in response")
	}

	msg := chatResp.Choices[0].Message
	clean, reasoning := splitReasoning(msg.Content, msg.ReasoningContent)
	// Empty-completion guard: a 200-OK whose assistant message carries no usable
	// content (no text AND no tool calls) is a semantic failure, not a success.
	// Observed in production when a reasoning model exhausts its max_tokens budget
	// on reasoning tokens and emits finish_reason=length with empty content. Pre-
	// guard this short-circuited the model-fallback chain on the first such model
	// and propagated an empty answer with no error (silent downgrade). Surface it
	// as the empty-completion sentinel so the chain advances to a model that can
	// answer (and a single endpoint reports a real error instead of "").
	// Tool-call responses legitimately have empty content, so they are exempt.
	if clean == "" && len(msg.ToolCalls) == 0 {
		return nil, newEmptyCompletionError(chatResp.Choices[0].FinishReason)
	}
	return &ChatResponse{
		Content:      clean,
		Reasoning:    reasoning,
		ToolCalls:    msg.ToolCalls,
		FinishReason: chatResp.Choices[0].FinishReason,
		Usage:        chatResp.Usage,
	}, nil
}

// hasNonCooledEndpoint reports whether at least one chain endpoint's model is
// NOT currently in cooldown. Used to gate skipping so the loop never fail-closed
// (when every model is cooled, no skip happens and the primary is still tried).
func (c *Client) hasNonCooledEndpoint() bool {
	for _, ep := range c.endpoints {
		if !c.cooldown.cooling(ep.Model) {
			return true
		}
	}
	return false
}

// cooldownCandidates returns the endpoints to iterate and whether cooled models
// should be skipped, applying quota-aware cooldown selection:
//   - cooldown disabled → the full chain, no skipping (unchanged behaviour).
//   - cooldown enabled, ≥1 healthy → the full chain, skip cooled models
//     (degraded > dead).
//   - cooldown enabled, ALL cooled → never fail-closed: only the PRIMARY (one
//     last-resort upstream probe) so its real error surfaces, instead of burning
//     the whole known-dead chain.
func (c *Client) cooldownCandidates() (endpoints []Endpoint, skipCooled bool) {
	if c.cooldown == nil {
		return c.endpoints, false
	}
	if c.hasNonCooledEndpoint() {
		return c.endpoints, true
	}
	return c.endpoints[:1], false
}

// recordCooldownOutcome feeds an attempt result to the cooldown bookkeeping: a
// success clears the model's cooldown; a quota-class failure drives it. No-op
// when cooldown is disabled.
func (c *Client) recordCooldownOutcome(ep Endpoint, err error) {
	if c.cooldown == nil {
		return
	}
	if err == nil {
		c.cooldown.recordSuccess(ep.Model)
		return
	}
	if isQuotaError(err) {
		var apiErr *APIError
		_ = errors.As(err, &apiErr)
		var retryAfter time.Duration
		if apiErr != nil {
			retryAfter = apiErr.RetryAfter
		}
		c.cooldown.recordFailure(ep.Model, retryAfter)
	}
}

// attemptEndpoint performs ONE chain-endpoint attempt: it applies the per-model
// override, the optional per-attempt timeout, fires the endpoint observer, and
// feeds the cooldown bookkeeping. The single authority for "try one endpoint" —
// shared by the chain loop and the never-fail-closed race guard so both paths
// stay observably identical (same observer + cooldown side effects). It does NOT
// classify the error for chain advancement (DeadlineExceeded / failover /
// retryable); that loop-control logic stays in executeInner.
// prepareEndpointRequest applies per-endpoint request transformations that
// must be identical on both the non-stream (attemptEndpoint) and stream
// (Stream) paths: the per-endpoint model override and the reasoning_effort
// allowlist gate. Returns a shallow copy of req with the overrides applied;
// the original req is never mutated.
func (c *Client) prepareEndpointRequest(ep Endpoint, req *ChatRequest) ChatRequest {
	epReq := *req
	if ep.Model != "" {
		epReq.Model = ep.Model
	}
	// Per-endpoint reasoning_effort gate: strip from endpoints NOT in the
	// allowlist. Empty allowlist = pass-through (existing behavior preserved).
	if epReq.ReasoningEffort != "" && len(c.reasoningEffortModels) > 0 {
		if !slices.Contains(c.reasoningEffortModels, epReq.Model) {
			epReq.ReasoningEffort = ""
		}
	}
	return epReq
}

func (c *Client) attemptEndpoint(ctx context.Context, ep Endpoint, req *ChatRequest) (*ChatResponse, error) {
	epReq := c.prepareEndpointRequest(ep, req)

	// Per-attempt timeout: derive a child ctx bounded by d, but only when
	// d > 0 and WithEndpoints is in use. The outer ctx remains the absolute
	// ceiling — context.WithTimeout takes min(d, time-left-on-outer).
	attemptCtx := ctx
	var cancelAttempt context.CancelFunc
	if c.perAttemptTimeout > 0 {
		attemptCtx, cancelAttempt = context.WithTimeout(ctx, c.perAttemptTimeout)
		// Deferred cancel guarantees the child context (and its timer goroutine)
		// is released even if doWithRetry panics — a non-deferred call would be
		// skipped during a panic unwind, leaking the context.WithTimeout timer.
		defer cancelAttempt()
	}

	result, err := c.doWithRetry(attemptCtx, ep.URL, ep.Key, &epReq)

	// Served-model attribution: the model that returned the 200 is this
	// endpoint's effective model. Set here (the single "try one endpoint"
	// authority) so BOTH the chain loop and the never-fail-closed race guard
	// attribute identically. epReq.Model carries the per-endpoint override when
	// set; fall back to req.Model otherwise.
	if err == nil && result != nil {
		result.ServedBy = epReq.Model
	}
	if c.endpointObserver != nil {
		c.endpointObserver(ep, err)
	}
	// Feed the cooldown bookkeeping: success clears, quota-fail drives.
	c.recordCooldownOutcome(ep, err)
	return result, err
}

// hasNonEmptyFallbackKey reports whether any of the fallback keys is non-empty.
func hasNonEmptyFallbackKey(keys []string) bool {
	for _, k := range keys {
		if k != "" {
			return true
		}
	}
	return false
}

func (c *Client) executeInner(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	// No-endpoint path guard: when no endpoint chain is configured (each
	// endpoint carries its own key), the only keys available are c.apiKey and
	// c.fallbackKeys. If ALL of them are empty, every doWithRetry attempt would
	// fire with an empty Bearer token → an opaque 401/403, and the fallback loop
	// silently skips empty keys via `if key == "" { continue }`, leaving the
	// caller with that opaque auth error. Fail fast with a clear sentinel
	// instead. The endpoint-chain path is unaffected: each endpoint has its own
	// ep.Key validated separately.
	if len(c.endpoints) == 0 && c.apiKey == "" && !hasNonEmptyFallbackKey(c.fallbackKeys) {
		return nil, ErrNoValidAPIKey
	}
	if len(c.endpoints) > 0 {
		var lastErr error
		attempted := false
		endpoints, skipCooled := c.cooldownCandidates()
		// tryOrder is the iteration slice for the loop. It may be a reordered or
		// filtered subset of endpoints (never a superset). The original endpoints
		// slice from cooldownCandidates is always preserved as the source of the
		// never-fail-closed race guard at the bottom (endpoints[0]), because tryOrder
		// can be empty (e.g. all models weight-0 under SelectionWeighted).
		tryOrder := endpoints
		switch c.selectionStrategy {
		case SelectionRandom:
			// When skipCooled=true (≥1 healthy candidate exists), build the
			// eligible (non-cooled) subset and shuffle it. When skipCooled=false
			// (cooldown disabled OR all-cooled last-resort path), endpoints is
			// either the full chain (no cooldown) or endpoints[:1] (all-cooled);
			// shuffle the full list in the no-cooldown case, preserve order in
			// the last-resort case.
			if skipCooled {
				// eligibleEndpoints is Guard A: filter to non-cooled subset before
				// shuffling so a cooled model is never placed in the try-order.
				// Guard B (the per-ep cooling() check in the loop below) is the
				// race-safety backstop for the concurrent-cooldown window where a
				// model may be cooled between this snapshot and the loop iteration.
				tryOrder = shuffleEndpoints(eligibleEndpoints(endpoints, c.cooldown), c.rander)
			} else if c.cooldown == nil {
				// No cooldown configured: all endpoints are eligible; shuffle all.
				tryOrder = shuffleEndpoints(endpoints, c.rander)
			}
			// else: all-cooled last-resort (endpoints[:1]): keep priority order.
		case SelectionWeighted:
			if skipCooled {
				// eligibleEndpoints is Guard A for weighted path: filter non-cooled
				// subset first, then apply weighted exclusion + ordering.
				tryOrder = weightedShuffleEndpoints(eligibleEndpoints(endpoints, c.cooldown), c.modelWeights, c.rander)
			} else if c.cooldown == nil {
				// No cooldown: all endpoints eligible for weighted shuffle.
				tryOrder = weightedShuffleEndpoints(endpoints, c.modelWeights, c.rander)
			}
			// else: all-cooled last-resort (endpoints[:1]): keep priority order.
		}
		for _, ep := range tryOrder {
			// Skip a model in quota cooldown, but only while a healthier
			// candidate remains — degraded > dead.
			if skipCooled && c.cooldown.cooling(ep.Model) {
				continue
			}
			attempted = true

			result, err := c.attemptEndpoint(ctx, ep, req)
			if err == nil {
				return result, nil
			}
			lastErr = err

			// A DeadlineExceeded where the outer ctx is still alive means this
			// endpoint was slow (not a genuine give-up by the caller). The
			// deadline could come from either the per-attempt timeout (when
			// configured) or the HTTP client's own timeout (default 90s). In
			// both cases the endpoint is too slow — treat it as retryable-
			// advance: continue to the next endpoint.
			// If the outer ctx is also done, fall through to the asRetryable
			// gate, which will return non-retryable → abort the chain (correct).
			if errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
				continue
			}

			// A "request too large for this model" failure (413 TPM/payload, or
			// 400 context_length_exceeded) is non-retryable on THIS endpoint — the
			// identical request recurs — but the next model in the chain may have a
			// larger context window or token budget. Advance to it instead of
			// aborting. The endpointObserver has already fired with this endpoint's
			// error, so the failover stays observable.
			if asFailover(err) {
				continue
			}

			if !asRetryable(err) {
				return nil, err
			}
		}
		// Race guard: cooldownCandidates() snapshotted skipCooled=true (≥1 model
		// healthy at that instant), but a concurrent goroutine cooled the last
		// healthy model between the snapshot and the per-iteration cooling()
		// re-check, so EVERY endpoint was skipped — `attempted` is false and
		// lastErr is nil. Returning here would yield (nil, nil) → a nil-deref in
		// CompleteRaw and every public caller. Never fail-closed: force one
		// last-resort attempt on the primary (degraded > dead) so a real response
		// or a real upstream error always surfaces. (nil, nil) is then
		// structurally impossible.
		if !attempted {
			return c.attemptEndpoint(ctx, endpoints[0], req)
		}
		return nil, lastErr
	}
	result, err := c.doWithRetry(ctx, c.baseURL, c.apiKey, req)
	if err == nil {
		return result, nil
	}
	for _, key := range c.fallbackKeys {
		if key == "" {
			continue
		}
		result, err = c.doWithRetry(ctx, c.baseURL, key, req)
		if err == nil {
			return result, nil
		}
	}
	return nil, err
}
