package llm

import (
	"reflect"
	"strconv"
	"strings"
)

// SchemaOf generates a JSON Schema from a Go struct.
// Uses struct field types and json tags for field names.
// Pointer fields and omitempty fields are optional (not in "required").
func SchemaOf(v any) map[string]any {
	t := reflect.TypeOf(v)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return typeSchema(t)
}

func typeSchema(t reflect.Type) map[string]any {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Slice:
		return map[string]any{"type": "array", "items": typeSchema(t.Elem())}
	case reflect.Map:
		if t.Key().Kind() == reflect.String {
			return map[string]any{"type": "object", "additionalProperties": typeSchema(t.Elem())}
		}
		return map[string]any{"type": "object"}
	case reflect.Struct:
		return structSchema(t)
	default:
		return map[string]any{"type": "string"}
	}
}

func structSchema(t reflect.Type) map[string]any {
	props := make(map[string]any)
	var required []string

	for i := range t.NumField() {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name := f.Name
		omit := false
		if tag := f.Tag.Get("json"); tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] == "-" {
				continue
			}
			if parts[0] != "" {
				name = parts[0]
			}
			for _, opt := range parts[1:] {
				if opt == "omitempty" {
					omit = true
				}
			}
		}
		fieldSchema := typeSchema(f.Type)
		if jsTag := f.Tag.Get("jsonschema"); jsTag != "" {
			applyConstraints(fieldSchema, jsTag)
		}
		props[name] = fieldSchema
		if !omit && f.Type.Kind() != reflect.Ptr {
			required = append(required, name)
		}
	}

	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// applyConstraints merges jsonschema tag constraints into a field schema.
// Supported keys: description, pattern (string), minimum, maximum, minLength,
// maxLength, minItems, maxItems (numeric), enum (pipe-separated values).
func applyConstraints(schema map[string]any, tag string) {
	for _, part := range strings.Split(tag, ",") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		switch k {
		case "description", "pattern":
			schema[k] = v
		case "minimum", "maximum",
			"minLength", "maxLength",
			"minItems", "maxItems":
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			schema[k] = n
		case "enum":
			schema[k] = strings.Split(v, "|")
		}
	}
}

// injectField adds a property to an object schema and prepends it to required.
func injectField(schema map[string]any, name string, fieldSchema map[string]any) {
	props := schema["properties"].(map[string]any)
	props[name] = fieldSchema
	req, _ := schema["required"].([]string)
	schema["required"] = append([]string{name}, req...)
}
