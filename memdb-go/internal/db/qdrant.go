package db

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/qdrant/go-client/qdrant"
)

// Qdrant wraps the Qdrant gRPC client.
type Qdrant struct {
	client *qdrant.Client
	logger *slog.Logger
}

// NewQdrant creates a new Qdrant gRPC client.
// addr should be "host:port" (default Qdrant gRPC port is 6334).
func NewQdrant(ctx context.Context, addr string, logger *slog.Logger) (*Qdrant, error) {
	if addr == "" {
		return nil, fmt.Errorf("qdrant address is empty")
	}

	client, err := qdrant.NewClient(&qdrant.Config{
		Host: addr,
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant connect failed: %w", err)
	}

	// Health check
	health, err := client.HealthCheck(ctx)
	if err != nil {
		return nil, fmt.Errorf("qdrant health check failed: %w", err)
	}

	logger.Info("qdrant connected",
		slog.String("version", health.GetVersion()),
	)
	return &Qdrant{client: client, logger: logger}, nil
}

// Client returns the underlying Qdrant client for direct access.
func (q *Qdrant) Client() *qdrant.Client {
	return q.client
}

// Ping checks the Qdrant connection.
func (q *Qdrant) Ping(ctx context.Context) error {
	_, err := q.client.HealthCheck(ctx)
	return err
}

// Close closes the Qdrant client connection.
func (q *Qdrant) Close() error {
	return q.client.Close()
}

// DeleteByIDs deletes points with the given string IDs from a collection.
func (q *Qdrant) DeleteByIDs(ctx context.Context, collection string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	pointIDs := make([]*qdrant.PointId, len(ids))
	for i, id := range ids {
		pointIDs[i] = qdrant.NewID(id)
	}

	wait := true
	_, err := q.client.Delete(ctx, &qdrant.DeletePoints{
		CollectionName: collection,
		Points:         qdrant.NewPointsSelectorIDs(pointIDs),
		Wait:           &wait,
	})
	return err
}

// ListCollections returns the names of all Qdrant collections.
func (q *Qdrant) ListCollections(ctx context.Context) ([]string, error) {
	return q.client.ListCollections(ctx)
}
