package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// VariantDef defines one variant of a union type for ExtractOneOf.
type VariantDef struct {
	Name string       // discriminator value (e.g. "search")
	typ  reflect.Type // concrete type for unmarshal
}

// Variant creates a VariantDef. zeroVal is a zero-value instance (e.g. SearchAction{}).
func Variant(name string, zeroVal any) VariantDef {
	if name == "" || zeroVal == nil {
		panic("llm.Variant: name and zeroVal must be non-empty")
	}
	t := reflect.TypeOf(zeroVal)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return VariantDef{Name: name, typ: t}
}

// unionSchema generates a JSON Schema wrapper with anyOf for OpenAI structured output.
// The discriminator field "action" is injected into each variant.
func unionSchema(variants []VariantDef) map[string]any {
	anyOf := make([]any, 0, len(variants))
	for _, v := range variants {
		s := structSchema(v.typ)
		injectField(s, "action", map[string]any{
			"type": "string",
			"enum": []string{v.Name},
		})
		anyOf = append(anyOf, s)
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"result": map[string]any{"anyOf": anyOf},
		},
		"required":            []string{"result"},
		"additionalProperties": false,
	}
}

// unmarshalUnion parses JSON wrapper and returns a typed pointer for the matched variant.
func unmarshalUnion(data []byte, variants []VariantDef) (any, error) {
	var wrapper struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		return nil, fmt.Errorf("union: unwrap failed: %w", err)
	}

	var disc struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(wrapper.Result, &disc); err != nil {
		return nil, fmt.Errorf("union: read discriminator: %w", err)
	}

	for _, v := range variants {
		if v.Name == disc.Action {
			ptr := reflect.New(v.typ)
			if err := json.Unmarshal(wrapper.Result, ptr.Interface()); err != nil {
				return nil, fmt.Errorf("union: unmarshal %q: %w", v.Name, err)
			}
			return ptr.Interface(), nil
		}
	}
	return nil, fmt.Errorf("union: unknown action %q", disc.Action)
}

// ExtractOneOf sends a structured output request where the LLM chooses between
// multiple response types. Returns a pointer to the matched variant.
func (c *Client) ExtractOneOf(ctx context.Context, messages []Message,
	variants []VariantDef, opts ...ExtractOption) (any, error) {

	cfg := extractConfig{maxRetries: 3}
	for _, opt := range opts {
		opt(&cfg)
	}
	if cfg.maxRetries < 1 {
		cfg.maxRetries = 1
	}

	schema := unionSchema(variants)
	msgs := make([]Message, len(messages))
	copy(msgs, messages)

	for attempt := range cfg.maxRetries {
		resp, err := c.Chat(ctx, msgs, WithJSONSchema("union", schema))
		if err != nil {
			return nil, err
		}

		result, err := unmarshalUnion([]byte(resp.Content), variants)
		if err != nil {
			if attempt == cfg.maxRetries-1 {
				return nil, fmt.Errorf("extractOneOf: unmarshal failed after %d attempts: %w", cfg.maxRetries, err)
			}
			msgs = append(msgs,
				Message{Role: "assistant", Content: resp.Content},
				Message{Role: "user", Content: "JSON parsing failed: " + err.Error() + ". Please fix the JSON and try again."},
			)
			continue
		}

		if cfg.validator != nil {
			if err := cfg.validator(result); err != nil {
				if attempt == cfg.maxRetries-1 {
					return nil, fmt.Errorf("extractOneOf: validation failed after %d attempts: %w", cfg.maxRetries, err)
				}
				msgs = append(msgs,
					Message{Role: "assistant", Content: resp.Content},
					Message{Role: "user", Content: "Validation error: " + err.Error() + ". Please fix and try again."},
				)
				continue
			}
		}

		return result, nil
	}

	return nil, fmt.Errorf("extractOneOf: exhausted %d retries", cfg.maxRetries)
}
