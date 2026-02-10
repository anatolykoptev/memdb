package db

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"

	"github.com/qdrant/go-client/qdrant"
)

// Qdrant wraps the Qdrant gRPC client.
type Qdrant struct {
	client *qdrant.Client
	logger *slog.Logger
}

// NewQdrant creates a new Qdrant gRPC client.
// addr should be "host:port" or just "host" (default gRPC port is 6334).
func NewQdrant(ctx context.Context, addr string, logger *slog.Logger) (*Qdrant, error) {
	if addr == "" {
		return nil, fmt.Errorf("qdrant address is empty")
	}

	cfg := &qdrant.Config{}
	if host, portStr, err := net.SplitHostPort(addr); err == nil {
		cfg.Host = host
		if p, err := strconv.Atoi(portStr); err == nil {
			cfg.Port = p
		}
	} else {
		cfg.Host = addr // no port, use default 6334
	}

	client, err := qdrant.NewClient(cfg)
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

// QdrantSearchResult holds a single search result from Qdrant.
type QdrantSearchResult struct {
	ID      string
	Score   float32
	Payload map[string]any
}

// SearchByVector searches a collection by vector similarity.
func (q *Qdrant) SearchByVector(ctx context.Context, collection string, vector []float32, limit uint64) ([]QdrantSearchResult, error) {
	points, err := q.client.Query(ctx, &qdrant.QueryPoints{
		CollectionName: collection,
		Query:          qdrant.NewQueryDense(vector),
		Limit:          qdrant.PtrOf(limit),
		WithPayload:    qdrant.NewWithPayload(true),
	})
	if err != nil {
		return nil, fmt.Errorf("qdrant search %s: %w", collection, err)
	}

	results := make([]QdrantSearchResult, 0, len(points))
	for _, pt := range points {
		r := QdrantSearchResult{
			Score:   pt.GetScore(),
			Payload: make(map[string]any),
		}
		// Extract ID (UUID string or num)
		if pid := pt.GetId(); pid != nil {
			if uuid := pid.GetUuid(); uuid != "" {
				r.ID = uuid
			} else {
				r.ID = fmt.Sprintf("%d", pid.GetNum())
			}
		}
		// Extract payload values
		for k, v := range pt.GetPayload() {
			r.Payload[k] = extractQdrantValue(v)
		}
		results = append(results, r)
	}
	return results, nil
}

// extractQdrantValue converts a qdrant.Value to a Go native type.
func extractQdrantValue(v *qdrant.Value) any {
	if v == nil {
		return nil
	}
	switch val := v.GetKind().(type) {
	case *qdrant.Value_StringValue:
		return val.StringValue
	case *qdrant.Value_IntegerValue:
		return val.IntegerValue
	case *qdrant.Value_DoubleValue:
		return val.DoubleValue
	case *qdrant.Value_BoolValue:
		return val.BoolValue
	case *qdrant.Value_ListValue:
		items := val.ListValue.GetValues()
		result := make([]any, len(items))
		for i, item := range items {
			result[i] = extractQdrantValue(item)
		}
		return result
	case *qdrant.Value_StructValue:
		fields := val.StructValue.GetFields()
		result := make(map[string]any, len(fields))
		for k, fv := range fields {
			result[k] = extractQdrantValue(fv)
		}
		return result
	default:
		return nil
	}
}
