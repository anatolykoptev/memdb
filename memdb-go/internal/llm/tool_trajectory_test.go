package llm

import (
	"context"
	"net/http"
	"testing"
)

// When every model answers empty, the client now returns ErrEmptyContent; the
// extractor must keep its "empty = no trajectories" contract instead of
// surfacing an error to the add pipeline.
func TestExtractToolTrajectory_AllModelsEmpty_NoTrajectories(t *testing.T) {
	_, srv := newModelServer(t, map[string]func(http.ResponseWriter, *http.Request){
		"a": replyContent(""),
	})
	c := NewClient(srv.URL, "k", "a", nil, quietLogger())

	items, err := ExtractToolTrajectory(context.Background(), c, "user: hi\nassistant: hello")
	if err != nil {
		t.Fatalf("want no error, got %v", err)
	}
	if items != nil {
		t.Fatalf("want nil trajectories, got %+v", items)
	}
}
