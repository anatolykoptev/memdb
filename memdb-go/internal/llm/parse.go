// Package llm — parse.go: generic structured chat helper.
//
// ChatStructured collapses the marshal+POST+status+limitReader+unmarshal+fence
// boilerplate that 10 ad-hoc callers inside MemDB had each open-coded into a
// single typed call. It is the typed companion of ChatText (plain text).
//
// Behaviour preserved across the original callsites:
//   - POST {"model": ..., "temperature": ..., "max_tokens": ..., "messages": [...]}
//     to {baseURL}/v1/chat/completions
//   - Authorization: Bearer {apiKey} when apiKey is non-empty
//   - Per-call HTTP timeout (option WithTimeout — default 15s, the most common
//     value across the original callsites)
//   - Response body capped via io.LimitReader to avoid runaway responses
//   - StripJSONFence on the assistant content before json.Unmarshal
//
// New behaviour added by this helper (per M11 R0 spec):
//   - On parse failure the helper retries once with a short reminder appended
//     to the user message ("Respond with strict JSON only, no prose.").
//     Behaviour delta vs legacy callers: all 10 migrated callsites previously
//     failed immediately on bad JSON; ChatStructured retries and self-corrects
//     — a strict improvement. To opt out, pass WithMaxRetries(0).
//   - On HTTP 429 (Too Many Requests) the helper retries once after a short
//     fixed delay. 5xx and other transport errors are NOT retried — the
//     original callsites treated them as terminal and graceful-degraded, and
//     several existing tests assert exactly one HTTP round trip on a 500.
//   - Per-prompt metrics:
//       memdb.llm.structured_call_total{prompt_id, outcome}
//       memdb.llm.structured_duration_ms{prompt_id}

package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Default tunables for ChatStructured. Picked to match the median of the
// 10 migrated callsites — individual callsites override via opts.
const (
	defaultStructuredTimeout      = 15 * time.Second
	defaultStructuredMaxTokens    = 1024
	defaultStructuredMaxRetries   = 1
	defaultStructuredTemperature  = 0.0
	defaultStructuredRespBodyMax  = int64(64 * 1024) // 64 KB — covers the largest existing limit (32K) with headroom.
	defaultStructured429BackoffMS = 250              // brief backoff before a single 429 retry.
)

// Message is one entry in an OpenAI-compatible chat completion request.
// Mirrors the ad-hoc map[string]string{"role":..., "content":...} the
// callsites used to construct.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatStructuredOpts holds resolved options. All fields are private; callers
// configure via the ChatOpt constructors below.
type chatStructuredOpts struct {
	maxRetries           int
	maxTokens            int
	temperature          float64
	timeout              time.Duration
	respBodyMax          int64
	useJSONResponseMode  bool   // sets response_format={type:"json_object"}
	parseRetryReminder   string // appended to the last user message on parse-error retry
}

// ChatOpt configures ChatStructured / ChatText. All options are optional;
// defaults are listed beside each constructor.
type ChatOpt func(*chatStructuredOpts)

// WithMaxRetries overrides the parse-error retry budget. Default 1.
// Note: the 429-retry budget is independent and fixed at 1.
func WithMaxRetries(n int) ChatOpt {
	return func(o *chatStructuredOpts) {
		if n < 0 {
			n = 0
		}
		o.maxRetries = n
	}
}

// WithMaxTokens overrides max_tokens in the request payload. Default 1024.
func WithMaxTokens(n int) ChatOpt {
	return func(o *chatStructuredOpts) { o.maxTokens = n }
}

// WithTemperature overrides temperature in the request payload. Default 0.0
// (deterministic — appropriate for structured output).
func WithTemperature(t float64) ChatOpt {
	return func(o *chatStructuredOpts) { o.temperature = t }
}

// WithTimeout overrides the per-call HTTP timeout. Default 15s.
func WithTimeout(d time.Duration) ChatOpt {
	return func(o *chatStructuredOpts) {
		if d > 0 {
			o.timeout = d
		}
	}
}

// WithRespBodyLimit overrides the io.LimitReader cap on the upstream response.
// Default 64 KB.
func WithRespBodyLimit(n int64) ChatOpt {
	return func(o *chatStructuredOpts) {
		if n > 0 {
			o.respBodyMax = n
		}
	}
}

// WithJSONResponseMode flips on response_format={type:"json_object"} on the
// upstream request. Some providers honour this; others ignore it. Equivalent
// to what fine.go used to set inline.
func WithJSONResponseMode() ChatOpt {
	return func(o *chatStructuredOpts) { o.useJSONResponseMode = true }
}

// WithParseRetryReminder customises the reminder appended to the final user
// message when the helper retries after a json.Unmarshal failure.
// Default: "Your previous response was not valid JSON. Respond with strict JSON only, no prose."
func WithParseRetryReminder(reminder string) ChatOpt {
	return func(o *chatStructuredOpts) {
		if reminder != "" {
			o.parseRetryReminder = reminder
		}
	}
}

func resolveStructuredOpts(opts []ChatOpt) chatStructuredOpts {
	o := chatStructuredOpts{
		maxRetries:         defaultStructuredMaxRetries,
		maxTokens:          defaultStructuredMaxTokens,
		temperature:        defaultStructuredTemperature,
		timeout:            defaultStructuredTimeout,
		respBodyMax:        defaultStructuredRespBodyMax,
		parseRetryReminder: "Your previous response was not valid JSON. Respond with strict JSON only, no prose.",
	}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// outcome label values for memdb.llm.structured_call_total.
const (
	outcomeSuccess    = "success"
	outcomeParseError = "parse_error"
	outcomeHTTPError  = "http_error"
	outcomeTimeout    = "timeout"
	outcomeRetried    = "retried"
)

// ChatStructured calls the LLM, strips a JSON fence, and unmarshals the assistant
// message content into target. promptID is a free-form string (e.g. "d5_staged",
// "d4_rewrite") used to tag per-prompt metrics. msgs is the OpenAI-compat
// messages array (system + user is the typical shape).
//
// Returns nil on success. On failure, returns an error and target is left in
// whatever state json.Unmarshal happened to leave it (per stdlib semantics).
//
// Retry policy:
//   - HTTP 429: one retry after defaultStructured429BackoffMS, increment
//     {outcome=retried}.
//   - parse error (json.Unmarshal): up to maxRetries retries, each with the
//     parseRetryReminder appended to the last user message.
//   - HTTP non-2xx (other than 429), transport error, ctx-deadline,
//     io.ReadAll error, missing choices: NO retry. Returns immediately.
//
// The Client supplies baseURL, apiKey, model, and the underlying http.Client.
func ChatStructured[T any](
	ctx context.Context,
	c *Client,
	promptID string,
	msgs []Message,
	target *T,
	opts ...ChatOpt,
) error {
	if c == nil {
		return errors.New("llm.ChatStructured: nil client")
	}
	if target == nil {
		return errors.New("llm.ChatStructured: nil target")
	}
	o := resolveStructuredOpts(opts)
	mx := llmStructuredMetrics()
	start := time.Now()

	defer func() {
		mx.Duration.Record(ctx, float64(time.Since(start).Milliseconds()),
			metric.WithAttributes(attribute.String("prompt_id", promptID)))
	}()

	current := append([]Message(nil), msgs...)
	var lastErr error
	for attempt := 0; attempt <= o.maxRetries; attempt++ {
		content, retried429, httpErr := fetchChatContent(ctx, c, current, o)
		if retried429 {
			mx.Calls.Add(ctx, 1, metric.WithAttributes(
				attribute.String("prompt_id", promptID),
				attribute.String("outcome", outcomeRetried),
			))
		}
		if httpErr != nil {
			outcome := outcomeHTTPError
			if errors.Is(httpErr, context.DeadlineExceeded) {
				outcome = outcomeTimeout
			}
			mx.Calls.Add(ctx, 1, metric.WithAttributes(
				attribute.String("prompt_id", promptID),
				attribute.String("outcome", outcome),
			))
			return httpErr
		}

		stripped := StripJSONFence([]byte(content))
		if err := json.Unmarshal(stripped, target); err != nil {
			lastErr = fmt.Errorf("llm.ChatStructured[%s]: parse: %w", promptID, err)
			if attempt < o.maxRetries {
				// retried label fires whether the second attempt succeeded or not — it is independent of the terminal outcome.
				mx.Calls.Add(ctx, 1, metric.WithAttributes(
					attribute.String("prompt_id", promptID),
					attribute.String("outcome", outcomeRetried),
				))
				current = appendReminder(current, o.parseRetryReminder)
				continue
			}
			mx.Calls.Add(ctx, 1, metric.WithAttributes(
				attribute.String("prompt_id", promptID),
				attribute.String("outcome", outcomeParseError),
			))
			return lastErr
		}
		mx.Calls.Add(ctx, 1, metric.WithAttributes(
			attribute.String("prompt_id", promptID),
			attribute.String("outcome", outcomeSuccess),
		))
		return nil
	}
	// Loop should always return; defensive fallthrough.
	if lastErr == nil {
		lastErr = errors.New("llm.ChatStructured: exhausted retries")
	}
	return lastErr
}

// ChatText is the plain-text sibling of ChatStructured: same metrics +
// retry-on-429, but no fence stripping and no Unmarshal. Used by callsites
// that consume the raw assistant content (e.g. profiler).
//
// maxRetries from opts is ignored — there is nothing to "parse-retry" for
// plain text.
func ChatText(
	ctx context.Context,
	c *Client,
	promptID string,
	msgs []Message,
	opts ...ChatOpt,
) (string, error) {
	if c == nil {
		return "", errors.New("llm.ChatText: nil client")
	}
	o := resolveStructuredOpts(opts)
	mx := llmStructuredMetrics()
	start := time.Now()
	defer func() {
		mx.Duration.Record(ctx, float64(time.Since(start).Milliseconds()),
			metric.WithAttributes(attribute.String("prompt_id", promptID)))
	}()

	content, retried429, httpErr := fetchChatContent(ctx, c, msgs, o)
	if retried429 {
		mx.Calls.Add(ctx, 1, metric.WithAttributes(
			attribute.String("prompt_id", promptID),
			attribute.String("outcome", outcomeRetried),
		))
	}
	if httpErr != nil {
		outcome := outcomeHTTPError
		if errors.Is(httpErr, context.DeadlineExceeded) {
			outcome = outcomeTimeout
		}
		mx.Calls.Add(ctx, 1, metric.WithAttributes(
			attribute.String("prompt_id", promptID),
			attribute.String("outcome", outcome),
		))
		return "", httpErr
	}
	mx.Calls.Add(ctx, 1, metric.WithAttributes(
		attribute.String("prompt_id", promptID),
		attribute.String("outcome", outcomeSuccess),
	))
	return content, nil
}

// fetchChatContent does up to two HTTP round trips: one normal call, plus a
// single retry if the first attempt returns 429. Returns (content, retried429,
// err). On err, content is "".
func fetchChatContent(
	ctx context.Context,
	c *Client,
	msgs []Message,
	o chatStructuredOpts,
) (string, bool, error) {
	content, status, err := chatRoundTrip(ctx, c, msgs, o)
	if err == nil && status == http.StatusOK {
		return content, false, nil
	}
	if status == http.StatusTooManyRequests {
		// brief backoff then a single retry
		select {
		case <-ctx.Done():
			return "", false, ctx.Err()
		case <-time.After(time.Duration(defaultStructured429BackoffMS) * time.Millisecond):
		}
		content2, status2, err2 := chatRoundTrip(ctx, c, msgs, o)
		if err2 == nil && status2 == http.StatusOK {
			return content2, true, nil
		}
		if err2 != nil {
			return "", true, err2
		}
		return "", true, fmt.Errorf("llm: status %d after 429 retry", status2)
	}
	if err != nil {
		return "", false, err
	}
	return "", false, fmt.Errorf("llm: status %d", status)
}

// chatRoundTrip performs exactly one HTTP POST. Returns content (when status==200)
// or status+err for the caller to decide on retry. On a non-200 status with no
// transport error, err is nil and the caller inspects status.
func chatRoundTrip(
	ctx context.Context,
	c *Client,
	msgs []Message,
	o chatStructuredOpts,
) (string, int, error) {
	payload := map[string]any{
		"model":       c.model,
		"temperature": o.temperature,
		"max_tokens":  o.maxTokens,
		"messages":    msgs,
	}
	if o.useJSONResponseMode {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", 0, fmt.Errorf("marshal: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost,
		c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Surface ctx cancellation cleanly so callers / metrics can label it
		// as timeout. errors.Is(reqCtx.Err(), context.DeadlineExceeded) is true
		// when the per-call timeout fires; the wrapped error chain includes it.
		if reqCtx.Err() == context.DeadlineExceeded {
			return "", 0, fmt.Errorf("llm request: %w", context.DeadlineExceeded)
		}
		return "", 0, fmt.Errorf("llm request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode, nil
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, o.respBodyMax))
	if err != nil {
		return "", resp.StatusCode, fmt.Errorf("read body: %w", err)
	}

	var chatResp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &chatResp); err != nil || len(chatResp.Choices) == 0 {
		return "", resp.StatusCode, errors.New("llm: bad response envelope")
	}
	return chatResp.Choices[0].Message.Content, resp.StatusCode, nil
}

// appendReminder returns a copy of msgs with the parse-retry reminder
// appended to the last user message's content (or as a new user message if
// the slice is empty / has no user messages).
func appendReminder(msgs []Message, reminder string) []Message {
	out := append([]Message(nil), msgs...)
	for i := len(out) - 1; i >= 0; i-- {
		if out[i].Role == "user" {
			out[i].Content = out[i].Content + "\n\n" + reminder
			return out
		}
	}
	out = append(out, Message{Role: "user", Content: reminder})
	return out
}

// NewSimpleClient returns a Client suitable for use with ChatStructured /
// ChatText: a stdlib http.Client with the supplied per-call timeout, the
// given baseURL/apiKey/model, and no fallback models. Logger is set to a
// no-op discard sink so callers do not have to thread one through.
//
// Use this from packages that have ad-hoc {URL,Key,Model} config structs
// (search.LLMRerankConfig, search.FineConfig, ...) and just want a typed
// LLM call:
//
//	client := llm.NewSimpleClient(cfg.APIURL, cfg.APIKey, cfg.Model)
//	err := llm.ChatStructured(ctx, client, "d5_staged", msgs, &out, llm.WithTimeout(15*time.Second))
func NewSimpleClient(baseURL, apiKey, model string) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: defaultStructuredTimeout},
		baseURL:    trimRightSlash(baseURL),
		apiKey:     apiKey,
		model:      model,
	}
}

// trimRightSlash mirrors strings.TrimRight(baseURL, "/") without pulling the
// strings import into this file. Kept private — same semantics as NewClient.
func trimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
