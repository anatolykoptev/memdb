package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ErrNoValidAPIKey is returned when a call is attempted with no usable API
// key configured — the primary apiKey is empty, every fallback key is empty,
// and no endpoint chain (each endpoint carries its own key) is configured.
// Surfacing this explicitly avoids an opaque 401/403 from the upstream after
// the empty-key attempts are silently skipped by the fallback loop.
var ErrNoValidAPIKey = errors.New("llm: no valid API key configured")

// APIError is a structured error returned by LLM API calls.
// Use errors.As to extract status code, body, and error type from callers.
type APIError struct {
	StatusCode int
	Body       string
	Type       string // parsed from JSON response if available (e.g. "rate_limit_error")
	Code       string // parsed from JSON response if available (e.g. "context_length_exceeded")
	Param      string // parsed from error.param (OpenAI-family); "model" on model-not-found 400s
	Retryable  bool
	// RetryAfter is the server-suggested delay before retry, parsed from
	// the HTTP Retry-After response header (RFC 7231 §7.1.3). Zero when
	// the header is absent or unparseable. Callers retrying APIError
	// should honour this value when non-zero instead of their own
	// backoff schedule.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	if e.Type != "" {
		return fmt.Sprintf("llm: HTTP %d (%s): %s", e.StatusCode, e.Type, e.Body)
	}
	return fmt.Sprintf("llm: HTTP %d: %s", e.StatusCode, e.Body)
}

func newAPIError(statusCode int, body string, retryable bool, retryAfter time.Duration) *APIError {
	e := &APIError{
		StatusCode: statusCode,
		Body:       body,
		Retryable:  retryable,
		RetryAfter: retryAfter,
	}
	// Try to extract error type/code/param from JSON body (OpenAI/Anthropic format).
	var parsed struct {
		Error struct {
			Type  string `json:"type"`
			Code  string `json:"code"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(body), &parsed) == nil {
		e.Type = parsed.Error.Type
		e.Code = parsed.Error.Code
		e.Param = parsed.Error.Param
	}
	return e
}

func isRetryableStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout:
		return true
	default:
		return false
	}
}

func asRetryable(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Retryable
}

// isModelUnavailable reports whether apiErr represents a model-specific
// "model not found / not available" failure — the model alias lists in
// /v1/models but is dead on actual call (provider silently swapped backing
// model). Such errors are model-specific: the NEXT model in the chain is a
// different model that may exist. The chain must ADVANCE, not abort.
//
// The invariant: model-not-found is signalled by a MODEL MARKER, independent
// of the 4xx code (400 or 422). Status alone does NOT carry the "model gone"
// semantic — 422 is the standard REST validation status used by Mistral/Cohere
// for malformed requests that recur on every model; 422 without a model marker
// must NOT failover.
//
// Recognised signals (both 400 and 422 require a marker):
//   - error.param == "model" (OpenAI-family "Model X not found", parsed by
//     newAPIError into APIError.Param).
//   - Body contains a structured unavailability term ("not available"/
//     "not found"/"does not exist") AND a structured model-listing marker
//     ("available_models" or "available:"). The bare word "model" is
//     intentionally excluded from the model-context disjunct: capability-
//     mismatch messages such as "response_format json_schema is not available
//     for this model" contain "model" but param=response_format, not param=model
//     — and they recur identically on every model (should abort, not failover).
//
// A plain 400/422 with no matching marker does NOT advance the chain.
func isModelUnavailable(apiErr *APIError) bool {
	if apiErr.StatusCode != http.StatusBadRequest &&
		apiErr.StatusCode != http.StatusUnprocessableEntity {
		return false
	}
	if apiErr.Param == "model" {
		return true
	}
	// Body-marker fallback: structured unavailability term + structured
	// model-listing indicator. Deliberately excludes the bare word "model"
	// to avoid false-positives on capability-mismatch messages.
	body := strings.ToLower(apiErr.Body)
	hasUnavailSignal := strings.Contains(body, "not available") ||
		strings.Contains(body, "not found") ||
		strings.Contains(body, "does not exist")
	hasModelListing := strings.Contains(body, "available_models") ||
		strings.Contains(body, "available:")
	return hasUnavailSignal && hasModelListing
}

// asFailover reports whether err is a per-model failure that should advance
// the fallback chain to the next model rather than aborting.
//
// Such an error is NOT retryable on the same endpoint — the identical request
// recurs — but the NEXT model in a fallback chain may succeed. The endpoint
// loop should ADVANCE to it rather than abort the whole chain.
//
// Recognised signals (cross-provider, observed on the cliproxyapi fleet):
//   - HTTP 413 Payload Too Large — Groq emits this with type "tokens" when a
//     single request exceeds the model's per-minute token (TPM) budget.
//     Matched status-alone (any body) — see note below.
//   - HTTP 400 with error code "context_length_exceeded" — the OpenAI-family
//     context-window-overflow shape.
//   - HTTP 400/422 with a model-not-found marker — provider silently swapped
//     the backing model; alias still lists in /v1/models but call fails.
//     Marker required: param=model or body contains ("not available"/"not found"/
//     "does not exist") AND ("available_models"/"available:"). See isModelUnavailable.
//
// A plain 400 or 422 with NO model marker is deliberately NOT a failover: it
// recurs identically on every model, so the chain must abort, not burn every
// endpoint.
//
// Note: 413 is matched on status alone (any body) — treated as model-specific.
// A non-model 413 shared by every endpoint (e.g. a gateway payload-size limit)
// would still advance and burn the chain, surfacing the same error after N
// attempts. Acceptable for the same-proxy chains this targets.
func asFailover(err error) bool {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	if apiErr.StatusCode == http.StatusRequestEntityTooLarge { // 413
		return true
	}
	if apiErr.StatusCode == http.StatusBadRequest && apiErr.Code == "context_length_exceeded" {
		return true
	}
	if isModelUnavailable(apiErr) {
		return true
	}
	// An empty-completion response (HTTP 200, no usable content) is non-retryable
	// on THIS model — a deterministic reasoning model truncated by the output-token
	// budget recurs the same truncation — but the NEXT model in the chain may have
	// a different reasoning/output profile and answer. Advance, don't abort.
	if isEmptyCompletion(err) {
		return true
	}
	return false
}

// emptyCompletionCode is the APIError.Code sentinel for a 200-OK response whose
// assistant message carried no usable content (no text, no tool calls). Observed
// in production when a reasoning model exhausts its max_tokens budget on
// reasoning tokens before emitting any answer (finish_reason=length, content="").
// A non-empty body that merely fails to JSON-parse is NOT this case — that
// surfaces as decode/parse handling upstream; this sentinel is specifically the
// "model produced nothing" semantic failure.
const emptyCompletionCode = "empty_completion"

// newEmptyCompletionError builds the structured error for an empty completion.
// StatusCode is 0 (the HTTP call succeeded; the failure is semantic — 200 would look like success in logs), Retryable
// is false (re-issuing the identical request recurs the same empty output on a
// deterministic endpoint — so this is a chain-failover signal, not a
// same-endpoint retry). finishReason is carried in the body for observability.
func newEmptyCompletionError(finishReason string) *APIError {
	return &APIError{
		StatusCode: 0,
		Body:       "llm: empty completion (no content, no tool calls; finish_reason=" + finishReason + ")",
		Code:       emptyCompletionCode,
		Retryable:  false,
	}
}

// isEmptyCompletion reports whether err is the empty-completion sentinel.
func isEmptyCompletion(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == emptyCompletionCode
}

// errTypeUnknown is the error_type label value for errors that do not match
// any known class. Centralised to avoid the string literal appearing 3+ times
// (goconst min-occurrences: 3 in .golangci.yml).
const errTypeUnknown = "unknown"

// ClassifyErrorType returns a low-cardinality error_type label value for
// Prometheus / OTel instrumentation. The mapping derives from the existing
// classifiers in this package — no new detection logic is introduced here.
//
// Values (align with the fleet failure-class taxonomy shared names):
//   - auth_expiry        — 401; or 403 without a quota marker
//   - dependency_block   — 429; quota-class 503; or 403 with a quota marker
//   - context_overflow   — 413 (TPM/payload too large) or 400 context_length_exceeded
//   - empty_completion   — 200 with no usable content (reasoning truncated by max_tokens)
//   - model_unavailable  — 422 (any body); or 400 with a model-not-found marker
//     (param=model or body marker). Operator signal: a chain
//     model is listed in /v1/models but dead on actual call.
//   - transient          — retryable 5xx / network (not quota-class)
//   - client             — non-auth, non-overflow, non-model 4xx (bad request, etc.)
//   - unknown            — non-APIError errors or anything unclassified
//
// Returns "" when err is nil (success path label).
func ClassifyErrorType(err error) string {
	if err == nil {
		return ""
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		return errTypeUnknown
	}
	// auth_expiry: explicit auth rejection (401 always; 403 unless it carries a quota marker)
	if apiErr.StatusCode == http.StatusUnauthorized {
		return "auth_expiry"
	}
	// calls checkMarkers directly — 403 does NOT go through isQuotaError to avoid triggering cooldown
	if apiErr.StatusCode == http.StatusForbidden {
		if checkMarkers(apiErr) {
			return "dependency_block"
		}
		return "auth_expiry"
	}
	// dependency_block: provider-side quota / rate-limit denial
	// reuses isQuotaError logic (429 + quota-class 503)
	if isQuotaError(err) {
		return "dependency_block"
	}
	// empty_completion: model returned a 200 with no usable content (reasoning
	// model truncated by max_tokens before emitting any answer). Checked BEFORE
	// asFailover because asFailover also matches this class for chain-advance,
	// but the metric label must distinguish it from context_overflow.
	if isEmptyCompletion(err) {
		return "empty_completion"
	}
	// model_unavailable: model alias listed in /v1/models but dead on actual call
	// (provider silently swapped backing model). Checked BEFORE context_overflow
	// because isModelUnavailable matches some 400/422 that asFailover also advances,
	// but the metric must distinguish "model gone" from "request too large".
	if isModelUnavailable(apiErr) {
		return "model_unavailable"
	}
	// context_overflow: request too large for this model
	// reuses asFailover logic (413 + 400 context_length_exceeded)
	if asFailover(err) {
		return "context_overflow"
	}
	// transient: retryable 5xx / network errors (remaining ones — 500,502,504 etc.)
	if apiErr.Retryable {
		return "transient"
	}
	// client: 4xx that aren't auth/quota/overflow/model-unavailable (bad request, etc.)
	if apiErr.StatusCode >= http.StatusBadRequest && apiErr.StatusCode < http.StatusInternalServerError {
		return "client"
	}
	return errTypeUnknown
}

// parseRetryAfter parses the HTTP Retry-After header per RFC 7231. The
// value can be either a non-negative integer number of seconds or an
// HTTP-date. Returns 0 on empty or unparseable input.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	// Seconds form.
	if secs, err := strconv.Atoi(h); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	// HTTP-date form.
	if t, err := http.ParseTime(h); err == nil {
		d := time.Until(t)
		if d < 0 {
			return 0
		}
		return d
	}
	return 0
}
