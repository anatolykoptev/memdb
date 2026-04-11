package db_test

// postgres_cubes_test.go — integration tests for UpsertCube (insert + update behavior).

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func dbURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("MEMDB_TEST_POSTGRES_URL")
	if url == "" {
		t.Skip("MEMDB_TEST_POSTGRES_URL not set; skipping postgres integration test")
	}
	return url
}

func setupCubesTest(t *testing.T) (*db.Postgres, func()) {
	t.Helper()
	ctx := context.Background()
	pg, err := db.NewPostgres(ctx, dbURL(t), slog.Default())
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	if err := pg.EnsureCubesTable(ctx); err != nil {
		t.Fatalf("ensure cubes table: %v", err)
	}
	if err := pg.DeleteCubesByPrefix(ctx, "test-cubes-"); err != nil {
		t.Fatalf("cleanup prior test rows: %v", err)
	}
	cleanup := func() {
		_ = pg.DeleteCubesByPrefix(context.Background(), "test-cubes-")
		pg.Close()
	}
	return pg, cleanup
}

func TestUpsertCube_Insert(t *testing.T) {
	pg, cleanup := setupCubesTest(t)
	defer cleanup()
	ctx := context.Background()

	desc := "initial description"
	cube, created, err := pg.UpsertCube(ctx, db.UpsertCubeParams{
		CubeID:      "test-cubes-insert",
		OwnerID:     "test-user",
		Description: &desc,
	})
	if err != nil {
		t.Fatalf("UpsertCube: %v", err)
	}
	if !created {
		t.Errorf("expected created=true on first insert, got false")
	}
	if cube.CubeID != "test-cubes-insert" {
		t.Errorf("cube_id: got %q want test-cubes-insert", cube.CubeID)
	}
	if cube.CubeName != "test-cubes-insert" {
		t.Errorf("cube_name default: got %q want test-cubes-insert", cube.CubeName)
	}
	if cube.OwnerID != "test-user" {
		t.Errorf("owner_id: got %q want test-user", cube.OwnerID)
	}
	if cube.Description == nil || *cube.Description != "initial description" {
		t.Errorf("description: got %v want initial description", cube.Description)
	}
	if !cube.IsActive {
		t.Errorf("is_active: got false want true")
	}
}

func TestUpsertCube_Idempotent(t *testing.T) {
	pg, cleanup := setupCubesTest(t)
	defer cleanup()
	ctx := context.Background()

	_, created1, err := pg.UpsertCube(ctx, db.UpsertCubeParams{
		CubeID:  "test-cubes-idempotent",
		OwnerID: "test-user",
	})
	if err != nil || !created1 {
		t.Fatalf("first upsert: created=%v err=%v", created1, err)
	}

	time.Sleep(10 * time.Millisecond)

	cube2, created2, err := pg.UpsertCube(ctx, db.UpsertCubeParams{
		CubeID:  "test-cubes-idempotent",
		OwnerID: "test-user",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if created2 {
		t.Errorf("expected created=false on second upsert, got true")
	}
	if !cube2.UpdatedAt.After(cube2.CreatedAt) {
		t.Errorf("updated_at should advance past created_at on re-upsert")
	}
}

func TestUpsertCube_UpdateMetadata(t *testing.T) {
	pg, cleanup := setupCubesTest(t)
	defer cleanup()
	ctx := context.Background()

	_, _, err := pg.UpsertCube(ctx, db.UpsertCubeParams{
		CubeID:  "test-cubes-metadata",
		OwnerID: "test-user",
	})
	if err != nil {
		t.Fatalf("initial upsert: %v", err)
	}

	newName := "Renamed Cube"
	newDesc := "Updated description"
	cube, _, err := pg.UpsertCube(ctx, db.UpsertCubeParams{
		CubeID:      "test-cubes-metadata",
		CubeName:    &newName,
		OwnerID:     "other-user",
		Description: &newDesc,
	})
	if err != nil {
		t.Fatalf("update upsert: %v", err)
	}
	if cube.CubeName != "Renamed Cube" {
		t.Errorf("cube_name: got %q want Renamed Cube", cube.CubeName)
	}
	if cube.OwnerID != "test-user" {
		t.Errorf("owner_id MUST NOT change via upsert: got %q want test-user", cube.OwnerID)
	}
	if cube.Description == nil || *cube.Description != "Updated description" {
		t.Errorf("description update: got %v", cube.Description)
	}
}
