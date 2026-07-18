package embedder

import "testing"

func TestApplyQueryPrefix(t *testing.T) {
	tests := []struct {
		name  string
		text  string
		model string
		want  string
	}{
		{"e5_known", "hello", "multilingual-e5-large", "query: hello"},
		{"e5_contains", "world", "some-e5-model", "query: world"},
		{"no_prefix", "hello", "text-embedding-ada-002", "hello"},
		{"code_model", "func main()", "code-rank-embed", "func main()"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := applyQueryPrefix(tt.text, tt.model); got != tt.want {
				t.Errorf("applyQueryPrefix(%q, %q) = %q, want %q", tt.text, tt.model, got, tt.want)
			}
		})
	}
}
