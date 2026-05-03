package middleware

import "testing"

// shouldBypassSearchCache must return true for internet_search=true requests:
// external API content changes between calls, caching would freeze stale fetched docs.
func TestShouldBypassSearchCache_InternetSearch(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"internet_search true", `{"user_id":"u","query":"q","internet_search":true}`, true},
		{"internet_search false", `{"user_id":"u","query":"q","internet_search":false}`, false},
		{"internet_search omitted", `{"user_id":"u","query":"q"}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldBypassSearchCache([]byte(tc.body))
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// shouldBypassSearchCache must return true when len(speakers)>=2: the handler
// takes the dual-speaker fan-out branch which produces request-shaped responses
// the v3 cache key only partially covers.
func TestShouldBypassSearchCache_Speakers(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"empty speakers", `{"user_id":"u","query":"q"}`, false},
		{"single speaker", `{"user_id":"u","query":"q","speakers":["alice"]}`, false},
		{"two speakers", `{"user_id":"u","query":"q","speakers":["alice","bob"]}`, true},
		{"three speakers", `{"user_id":"u","query":"q","speakers":["a","b","c"]}`, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldBypassSearchCache([]byte(tc.body))
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

// Malformed JSON: do not panic, do not bypass — let downstream parser handle it.
func TestShouldBypassSearchCache_MalformedJSON(t *testing.T) {
	if shouldBypassSearchCache([]byte("not json at all")) {
		t.Error("malformed body should fall through, not bypass")
	}
}
