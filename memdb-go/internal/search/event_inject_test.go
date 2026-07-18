package search

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
	"github.com/google/uuid"
)

// stubEventsPG is a minimal eventsPostgres impl for testing the selection
// priority + injection happy path. Records which method was called so we
// can assert the date→tag→cosine fallthrough.  Captures the (start, end,
// limit) tuple of the most recent SearchEventsByDate call so window-math
// tests can assert ±7d.
type stubEventsPG struct {
	dateCalled   bool
	tagCalled    bool
	cosineCalled bool
	dateRows     []db.EventEntry
	tagRows      []db.EventEntry
	cosineRows   []db.EventEntry

	lastDateStart time.Time
	lastDateEnd   time.Time
	lastDateLimit int
}

func (s *stubEventsPG) SearchEventsByDate(ctx context.Context, cubeID, userID string, start, end time.Time, limit int) ([]db.EventEntry, error) {
	s.dateCalled = true
	s.lastDateStart = start
	s.lastDateEnd = end
	s.lastDateLimit = limit
	return s.dateRows, nil
}
func (s *stubEventsPG) SearchEventsByTag(ctx context.Context, cubeID, userID string, tags []string, limit int) ([]db.EventEntry, error) {
	s.tagCalled = true
	return s.tagRows, nil
}
func (s *stubEventsPG) SearchEventsByCosine(ctx context.Context, cubeID, userID string, query []float32, k int) ([]db.EventEntry, error) {
	s.cosineCalled = true
	return s.cosineRows, nil
}

func makeEvent(text string) db.EventEntry {
	d := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
	return db.EventEntry{
		ID:        uuid.New(),
		CubeID:    "c1",
		UserID:    "u1",
		EventText: text,
		EventDate: &d,
		Tags:      []string{"location:Berlin"},
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
}

func TestLookupEvents_DateWindowWins(t *testing.T) {
	stub := &stubEventsPG{dateRows: []db.EventEntry{makeEvent("a")}}
	st := &pipelineState{
		Params:    SearchParams{CubeID: "c1", UserName: "u1", Tags: []string{"x"}},
		HasCutoff: true,
		CutoffISO: "2024-05-01T00:00:00+00:00",
		QueryVec:  []float32{0.1, 0.2},
	}
	svc := &SearchService{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	events, tp := svc.lookupEvents(context.Background(), stub, st)
	if tp != eventSearchTypeDate || len(events) != 1 {
		t.Errorf("expected date win, got type=%q n=%d", tp, len(events))
	}
	if stub.tagCalled || stub.cosineCalled {
		t.Error("date hit should short-circuit fallback paths")
	}
}

func TestLookupEvents_TagFallback(t *testing.T) {
	stub := &stubEventsPG{tagRows: []db.EventEntry{makeEvent("b")}}
	st := &pipelineState{
		Params:   SearchParams{CubeID: "c1", UserName: "u1", Tags: []string{"location:Berlin"}},
		QueryVec: []float32{0.1, 0.2},
	}
	svc := &SearchService{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	events, tp := svc.lookupEvents(context.Background(), stub, st)
	if tp != eventSearchTypeTag || len(events) != 1 {
		t.Errorf("expected tag win, got type=%q n=%d", tp, len(events))
	}
	if stub.cosineCalled {
		t.Error("tag hit should short-circuit cosine")
	}
}

func TestLookupEvents_CosineFallback(t *testing.T) {
	stub := &stubEventsPG{cosineRows: []db.EventEntry{makeEvent("c")}}
	st := &pipelineState{
		Params:   SearchParams{CubeID: "c1", UserName: "u1"},
		QueryVec: []float32{0.1, 0.2},
	}
	svc := &SearchService{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	events, tp := svc.lookupEvents(context.Background(), stub, st)
	if tp != eventSearchTypeCosine || len(events) != 1 {
		t.Errorf("expected cosine win, got type=%q n=%d", tp, len(events))
	}
}

func TestEventToMergedResult_PropertiesShape(t *testing.T) {
	ev := makeEvent("user moved")
	mr := eventToMergedResult(ev)
	if mr.ID != ev.ID.String() {
		t.Errorf("id mismatch: %s vs %s", mr.ID, ev.ID.String())
	}
	if mr.Score != eventInjectScore {
		t.Errorf("score = %f, want %f", mr.Score, eventInjectScore)
	}
	if !strings.Contains(mr.Properties, `"memory_type":"EventSummary"`) {
		t.Errorf("properties missing memory_type marker: %s", mr.Properties)
	}
	if !strings.Contains(mr.Properties, `"event_date":"2024-05-01"`) {
		t.Errorf("properties missing event_date: %s", mr.Properties)
	}
}

// TestLookupEvents_DateWindowMath pins the ±7d window math: for cutoff
// 2024-05-15 we expect SearchEventsByDate to be called with
// start=2024-05-08, end=2024-05-22, limit=eventInjectTopN.  Regression
// guard for a future off-by-one or unit drift.
func TestLookupEvents_DateWindowMath(t *testing.T) {
	stub := &stubEventsPG{dateRows: []db.EventEntry{makeEvent("a")}}
	cutoffISO := "2024-05-15T00:00:00+00:00"
	cutoff, err := time.Parse("2006-01-02T15:04:05+00:00", cutoffISO)
	if err != nil {
		t.Fatalf("setup: parse cutoff: %v", err)
	}
	st := &pipelineState{
		Params:    SearchParams{CubeID: "c1", UserName: "u1"},
		HasCutoff: true,
		CutoffISO: cutoffISO,
	}
	svc := &SearchService{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	_, tp := svc.lookupEvents(context.Background(), stub, st)
	if tp != eventSearchTypeDate {
		t.Fatalf("expected date win, got %q", tp)
	}
	wantStart := cutoff.AddDate(0, 0, -eventInjectDateWindowDays)
	wantEnd := cutoff.AddDate(0, 0, eventInjectDateWindowDays)
	if !stub.lastDateStart.Equal(wantStart) {
		t.Errorf("start: got %s, want %s (cutoff - 7d)", stub.lastDateStart, wantStart)
	}
	if !stub.lastDateEnd.Equal(wantEnd) {
		t.Errorf("end:   got %s, want %s (cutoff + 7d)", stub.lastDateEnd, wantEnd)
	}
	if stub.lastDateLimit != eventInjectTopN {
		t.Errorf("limit: got %d, want %d", stub.lastDateLimit, eventInjectTopN)
	}
	// Asymmetry check: window must span exactly 14 days.
	if d := stub.lastDateEnd.Sub(stub.lastDateStart); d != 14*24*time.Hour {
		t.Errorf("window width: got %s, want 14d (2 × 7d)", d)
	}
}

// TestLookupEvents_DateWindowMath_PreservesUTC pins that the window math
// honours whatever location the cutoff was parsed in (always UTC for our
// callers, but the math must not silently shift to local).
func TestLookupEvents_DateWindowMath_PreservesUTC(t *testing.T) {
	stub := &stubEventsPG{dateRows: []db.EventEntry{makeEvent("a")}}
	st := &pipelineState{
		Params:    SearchParams{CubeID: "c1", UserName: "u1"},
		HasCutoff: true,
		CutoffISO: "2024-12-31T12:00:00+00:00", // late-day cutoff close to year boundary
	}
	svc := &SearchService{logger: slog.New(slog.NewTextHandler(os.Stderr, nil))}
	_, _ = svc.lookupEvents(context.Background(), stub, st)
	if stub.lastDateStart.Location() != time.UTC {
		t.Errorf("start location drifted: %s", stub.lastDateStart.Location())
	}
	if stub.lastDateEnd.Location() != time.UTC {
		t.Errorf("end location drifted: %s", stub.lastDateEnd.Location())
	}
}

func TestEventInjectEnabled_DefaultOn(t *testing.T) {
	t.Setenv(eventInjectEnvVar, "")
	if !eventInjectEnabled() {
		t.Error("should default to true")
	}
	t.Setenv(eventInjectEnvVar, "false")
	if eventInjectEnabled() {
		t.Error("'false' should disable")
	}
	t.Setenv(eventInjectEnvVar, "0")
	if eventInjectEnabled() {
		t.Error("'0' should disable")
	}
	t.Setenv(eventInjectEnvVar, "true")
	if !eventInjectEnabled() {
		t.Error("'true' should enable")
	}
}
