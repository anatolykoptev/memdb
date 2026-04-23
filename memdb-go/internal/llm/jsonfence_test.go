package llm

import (
	"encoding/json"
	"testing"
)

func TestStripJSONFence(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		valid bool
		want  string // expected stripped output; empty means don't check
	}{
		{"no fence", `[{"a":1}]`, true, `[{"a":1}]`},
		{"json fence lf", "```json\n[{\"a\":1}]\n```", true, `[{"a":1}]`},
		{"bare fence", "```\n[{\"a\":1}]\n```", true, `[{"a":1}]`},
		{"fence with trailing ws", "```json\n[{\"a\":1}]\n```\n  ", true, `[{"a":1}]`},
		{"fence with crlf", "```json\r\n[{\"a\":1}]\r\n```", true, ""},
		{"leading ws only", "  \n[{\"a\":1}]\n", true, `[{"a":1}]`},
		{"no fence obj", `{"a":1}`, true, `{"a":1}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := StripJSONFence([]byte(c.in))
			var v any
			err := json.Unmarshal(got, &v)
			if (err == nil) != c.valid {
				t.Fatalf("unmarshal err=%v, want valid=%v. Stripped: %q", err, c.valid, got)
			}
			if c.want != "" && string(got) != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
