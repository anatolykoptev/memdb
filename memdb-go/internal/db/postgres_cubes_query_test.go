package db_test

// postgres_cubes_query_test.go — integration tests for ListCubes, SoftDelete, EnsureCubeExists.

import (
	"context"
	"testing"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

func TestListCubes_AllActive(t *testing.T) {
	pg, cleanup := setupCubesTest(t)
	defer cleanup()
	ctx := context.Background()

	for _, id := range []string{"test-cubes-list-a", "test-cubes-list-b", "test-cubes-list-c"} {
		if _, _, err := pg.UpsertCube(ctx, db.UpsertCubeParams{CubeID: id, OwnerID: "test-user"}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}

	cubes, err := pg.ListCubes(ctx, nil)
	if err != nil {
		t.Fatalf("ListCubes: %v", err)
	}
	found := map[string]bool{}
	for _, c := range cubes {
		found[c.CubeID] = true
	}
	for _, id := range []string{"test-cubes-list-a", "test-cubes-list-b", "test-cubes-list-c"} {
		if !found[id] {
			t.Errorf("ListCubes missing %s", id)
		}
	}
}

func TestListCubes_OwnerFilter(t *testing.T) {
	pg, cleanup := setupCubesTest(t)
	defer cleanup()
	ctx := context.Background()

	_, _, _ = pg.UpsertCube(ctx, db.UpsertCubeParams{CubeID: "test-cubes-alice-1", OwnerID: "alice"})
	_, _, _ = pg.UpsertCube(ctx, db.UpsertCubeParams{CubeID: "test-cubes-bob-1", OwnerID: "bob"})

	alice := "alice"
	cubes, err := pg.ListCubes(ctx, &alice)
	if err != nil {
		t.Fatalf("ListCubes: %v", err)
	}
	foundAlice := false
	for _, c := range cubes {
		if c.OwnerID != "alice" {
			t.Errorf("owner filter leak: cube %q owned by %q", c.CubeID, c.OwnerID)
		}
		if c.CubeID == "test-cubes-alice-1" {
			foundAlice = true
		}
	}
	if !foundAlice {
		t.Errorf("test-cubes-alice-1 not returned")
	}
}

func TestSoftDeleteCube(t *testing.T) {
	pg, cleanup := setupCubesTest(t)
	defer cleanup()
	ctx := context.Background()

	_, _, _ = pg.UpsertCube(ctx, db.UpsertCubeParams{CubeID: "test-cubes-soft", OwnerID: "test-user"})

	if err := pg.SoftDeleteCube(ctx, "test-cubes-soft"); err != nil {
		t.Fatalf("SoftDeleteCube: %v", err)
	}

	cubes, _ := pg.ListCubes(ctx, nil)
	for _, c := range cubes {
		if c.CubeID == "test-cubes-soft" {
			t.Errorf("soft-deleted cube must not appear in ListCubes")
		}
	}
}

func TestSoftDeleteCube_NotFound(t *testing.T) {
	pg, cleanup := setupCubesTest(t)
	defer cleanup()
	ctx := context.Background()

	err := pg.SoftDeleteCube(ctx, "test-cubes-nonexistent-xyz")
	if err == nil {
		t.Errorf("expected ErrCubeNotFound, got nil")
	}
	if err != db.ErrCubeNotFound {
		t.Errorf("expected ErrCubeNotFound, got %v", err)
	}
}

func TestEnsureCubeExists_Create(t *testing.T) {
	pg, cleanup := setupCubesTest(t)
	defer cleanup()
	ctx := context.Background()

	created, err := pg.EnsureCubeExists(ctx, "test-cubes-ensure", "test-user")
	if err != nil {
		t.Fatalf("EnsureCubeExists: %v", err)
	}
	if !created {
		t.Errorf("expected created=true on first ensure")
	}

	created2, _ := pg.EnsureCubeExists(ctx, "test-cubes-ensure", "test-user")
	if created2 {
		t.Errorf("expected created=false on second ensure")
	}
}
