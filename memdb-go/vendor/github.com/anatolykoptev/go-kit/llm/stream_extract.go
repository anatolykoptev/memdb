package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// ExtractStream accumulates streaming chunks and progressively parses JSON.
type ExtractStream struct {
	stream *StreamResponse
	buf    strings.Builder
	target any // pointer to user's struct
	err    error
	done   bool
	cfg    extractConfig
}

// Next reads the next chunk and attempts to parse the accumulated text.
// Returns true while the stream is active.
// After each successful call, the target struct may have been partially filled.
func (s *ExtractStream) Next() bool {
	if s.done || s.err != nil {
		return false
	}

	chunk, ok := s.stream.Next()
	if !ok {
		s.done = true
		if err := s.stream.Err(); err != nil {
			s.err = err
			return false
		}
		s.finalParse()
		return false
	}

	s.buf.WriteString(chunk.Delta)

	// Try partial parse (best-effort, ignore errors).
	completed := partialJSON(s.buf.String())
	if completed != "" {
		_ = json.Unmarshal([]byte(completed), s.target)
	}

	return true
}

// finalParse does the definitive unmarshal + optional validation.
func (s *ExtractStream) finalParse() {
	text := strings.TrimSpace(s.buf.String())
	if text == "" {
		s.err = fmt.Errorf("streamextract: empty response")
		return
	}

	// Reset target before final unmarshal.
	reflect.ValueOf(s.target).Elem().SetZero()

	if err := json.Unmarshal([]byte(text), s.target); err != nil {
		s.err = fmt.Errorf("streamextract: unmarshal: %w", err)
		return
	}

	if s.cfg.validator != nil {
		if err := s.cfg.validator(s.target); err != nil {
			s.err = fmt.Errorf("streamextract: validation: %w", err)
		}
	}
}

// Text returns the accumulated raw text so far.
func (s *ExtractStream) Text() string { return s.buf.String() }

// Err returns any error from streaming or parsing.
func (s *ExtractStream) Err() error { return s.err }

// Close closes the underlying stream.
func (s *ExtractStream) Close() error { return s.stream.Close() }

// Usage returns token usage (available after stream completes).
func (s *ExtractStream) Usage() *Usage { return s.stream.Usage() }

// StreamExtract starts a streaming structured output request.
// The target must be a pointer to a struct. As chunks arrive, the target
// is progressively filled via partial JSON parsing.
func (c *Client) StreamExtract(ctx context.Context, messages []Message,
	target any, opts ...ExtractOption,
) (*ExtractStream, error) {
	cfg := extractConfig{maxRetries: 1} // no retries for streaming
	for _, opt := range opts {
		opt(&cfg)
	}

	schema := SchemaOf(target)

	t := reflect.TypeOf(target)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	name := strings.ToLower(t.Name())
	if name == "" {
		name = "response"
	}

	stream, err := c.Stream(ctx, messages, WithJSONSchema(name, schema))
	if err != nil {
		return nil, err
	}

	return &ExtractStream{
		stream: stream,
		target: target,
		cfg:    cfg,
	}, nil
}
