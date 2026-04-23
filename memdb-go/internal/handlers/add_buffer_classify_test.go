package handlers

import (
	"errors"
	"testing"
)

func TestClassifyFlushError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"lua", errors.New("buffer flush: lua script: ERR attempt to call nil"), "lua"},
		{"parse", errors.New("buffer flush: extract and dedup: invalid character"), "parse"},
		{"db", errors.New("buffer flush: insert nodes: pgx connect timeout"), "db"},
		{"unknown", errors.New("buffer flush: weird thing: boom"), "other"},
		{"no prefix", errors.New("just broken"), "other"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyFlushError(c.err); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
