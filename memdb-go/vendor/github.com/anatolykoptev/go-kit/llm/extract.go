package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// ExtractOption configures Extract behavior.
type ExtractOption func(*extractConfig)

type extractConfig struct {
	maxRetries int
	validator  func(any) error
}

// WithValidator sets a validation function called after unmarshalling.
// If it returns an error, Extract retries with the error fed back to the LLM.
func WithValidator(fn func(any) error) ExtractOption {
	return func(c *extractConfig) { c.validator = fn }
}

// WithExtractRetries sets the maximum number of extraction retries (default 3).
func WithExtractRetries(n int) ExtractOption {
	return func(c *extractConfig) { c.maxRetries = n }
}

// Extract sends a structured output request, unmarshals into target,
// and validates with the optional validator. On validation failure, the error
// is fed back to the LLM and the request is retried.
//
// This is the Go equivalent of Python's Instructor pattern:
// JSON Schema -> structured output -> validate -> retry with error feedback.
func (c *Client) Extract(ctx context.Context, messages []Message, target any, opts ...ExtractOption) error {
	cfg := extractConfig{maxRetries: 3}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.maxRetries < 1 {
		cfg.maxRetries = 1
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

	msgs := make([]Message, len(messages))
	copy(msgs, messages)

	for attempt := range cfg.maxRetries {
		resp, err := c.Chat(ctx, msgs, WithJSONSchema(name, schema))
		if err != nil {
			return err
		}

		// Reset target before unmarshal to avoid stale fields from previous attempt.
		reflect.ValueOf(target).Elem().SetZero()

		if err := json.Unmarshal([]byte(resp.Content), target); err != nil {
			if attempt == cfg.maxRetries-1 {
				return fmt.Errorf("extract: unmarshal failed after %d attempts: %w", cfg.maxRetries, err)
			}
			msgs = append(msgs,
				Message{Role: "assistant", Content: resp.Content},
				Message{Role: "user", Content: "JSON parsing failed: " + err.Error() + ". Please fix the JSON and try again."},
			)
			continue
		}

		if cfg.validator == nil {
			return nil
		}

		if err := cfg.validator(target); err != nil {
			if attempt == cfg.maxRetries-1 {
				return fmt.Errorf("extract: validation failed after %d attempts: %w", cfg.maxRetries, err)
			}
			msgs = append(msgs,
				Message{Role: "assistant", Content: resp.Content},
				Message{Role: "user", Content: "Validation error: " + err.Error() + ". Please fix and try again."},
			)
			continue
		}

		return nil
	}

	return fmt.Errorf("extract: exhausted %d retries", cfg.maxRetries)
}
