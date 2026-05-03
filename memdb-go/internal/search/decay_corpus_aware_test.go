package search

import (
	"testing"
	"time"
)

// itemWithTS builds a minimal item carrying a created_at timestamp. Mirrors
// what postProcessResults sees from the formatted result map.
func itemWithTS(ts time.Time) map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"created_at": ts.UTC().Format(time.RFC3339),
		},
	}
}

func TestCorpusReferenceNow_OldCorpusAnchorsToMax(t *testing.T) {
	// conv-26-style timestamps: all 3 years stale.
	t1 := time.Date(2023, 5, 1, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2023, 5, 8, 12, 0, 0, 0, time.UTC) // newest
	t3 := time.Date(2023, 4, 20, 12, 0, 0, 0, time.UTC)
	pool := []map[string]any{itemWithTS(t1), itemWithTS(t2), itemWithTS(t3)}

	ref := corpusReferenceNow(pool)
	if !ref.Equal(t2) {
		t.Fatalf("expected ref = max(chat_time) = %s, got %s", t2, ref)
	}
}

func TestCorpusReferenceNow_FreshCorpusUsesWallClock(t *testing.T) {
	// All items within last week — production live chat.
	wallNow := time.Now()
	t1 := wallNow.Add(-2 * 24 * time.Hour)
	t2 := wallNow.Add(-1 * 24 * time.Hour)
	pool := []map[string]any{itemWithTS(t1), itemWithTS(t2)}

	ref := corpusReferenceNow(pool)
	// Allow a small clock-tick delta — ref must be wall-clock, NOT t2.
	delta := ref.Sub(wallNow)
	if delta < -2*time.Second || delta > 2*time.Second {
		t.Fatalf("expected ref ~= wallNow on fresh corpus, got delta=%s (ref=%s wallNow=%s)", delta, ref, wallNow)
	}
}

func TestCorpusReferenceNow_EmptyPoolUsesWallClock(t *testing.T) {
	wallNow := time.Now()
	ref := corpusReferenceNow(nil, []map[string]any{}, nil)
	delta := ref.Sub(wallNow)
	if delta < -2*time.Second || delta > 2*time.Second {
		t.Fatalf("expected ref ~= wallNow on empty pool, got delta=%s", delta)
	}
}

func TestCorpusReferenceNow_NoTimestampUsesWallClock(t *testing.T) {
	// Items present but metadata has no parseable timestamp.
	pool := []map[string]any{
		{"metadata": map[string]any{"foo": "bar"}},
		{"not_metadata": "x"},
	}
	wallNow := time.Now()
	ref := corpusReferenceNow(pool)
	delta := ref.Sub(wallNow)
	if delta < -2*time.Second || delta > 2*time.Second {
		t.Fatalf("expected ref ~= wallNow when no timestamps parseable, got delta=%s", delta)
	}
}

func TestCorpusReferenceNow_MixedPoolPicksMax(t *testing.T) {
	// Multiple pools with mixed timestamps; newest must win.
	old := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	mid := time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC)
	newest := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)

	textPool := []map[string]any{itemWithTS(old)}
	skillPool := []map[string]any{itemWithTS(newest), itemWithTS(mid)}
	toolPool := []map[string]any{itemWithTS(mid)}

	ref := corpusReferenceNow(textPool, skillPool, toolPool)
	if !ref.Equal(newest) {
		t.Fatalf("expected ref = newest across pools = %s, got %s", newest, ref)
	}
}

func TestExtractItemTimestamp_PrefersLastAccessed(t *testing.T) {
	created := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	accessed := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	item := map[string]any{
		"metadata": map[string]any{
			"created_at":       created.Format(time.RFC3339),
			"last_accessed_at": accessed.Format(time.RFC3339),
		},
	}
	got := extractItemTimestamp(item)
	if !got.Equal(accessed) {
		t.Fatalf("expected last_accessed_at preferred, got %s", got)
	}
}

func TestExtractItemTimestamp_NoMetadata(t *testing.T) {
	if !extractItemTimestamp(map[string]any{}).IsZero() {
		t.Fatal("expected zero time when metadata missing")
	}
	if !extractItemTimestamp(map[string]any{"metadata": "not-a-map"}).IsZero() {
		t.Fatal("expected zero time when metadata wrong type")
	}
}
