package handlers

import (
	"context"
	"io"
	"log/slog"

	"github.com/anatolykoptev/memdb/memdb-go/internal/db"
)

// fakeCubeStore is a minimal in-memory implementation of cubeStoreClient
// used to test handler request/response shapes without a live postgres.
type fakeCubeStore struct {
	cubes        map[string]db.Cube
	upsertCalls  int
	listCalls    int
	softDeleted  []string
	hardDeleted  []string
	hardDeletedN int64
	returnErrOn  string // method name to return error from
}

func (f *fakeCubeStore) UpsertCube(ctx context.Context, params db.UpsertCubeParams) (db.Cube, bool, error) {
	f.upsertCalls++
	if f.cubes == nil {
		f.cubes = map[string]db.Cube{}
	}
	existing, ok := f.cubes[params.CubeID]
	created := !ok
	if created {
		name := params.CubeID
		if params.CubeName != nil {
			name = *params.CubeName
		}
		existing = db.Cube{
			CubeID: params.CubeID, CubeName: name, OwnerID: params.OwnerID,
			Description: params.Description, CubePath: params.CubePath, IsActive: true,
		}
	} else {
		if params.CubeName != nil {
			existing.CubeName = *params.CubeName
		}
		if params.Description != nil {
			existing.Description = params.Description
		}
		if params.CubePath != nil {
			existing.CubePath = params.CubePath
		}
	}
	f.cubes[params.CubeID] = existing
	return existing, created, nil
}

func (f *fakeCubeStore) ListCubes(ctx context.Context, ownerID *string) ([]db.Cube, error) {
	f.listCalls++
	var out []db.Cube
	for _, c := range f.cubes {
		if !c.IsActive {
			continue
		}
		if ownerID != nil && c.OwnerID != *ownerID {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func (f *fakeCubeStore) GetCube(ctx context.Context, cubeID string) (*db.Cube, error) {
	if c, ok := f.cubes[cubeID]; ok {
		return &c, nil
	}
	return nil, db.ErrCubeNotFound
}

func (f *fakeCubeStore) SoftDeleteCube(ctx context.Context, cubeID string) error {
	f.softDeleted = append(f.softDeleted, cubeID)
	if c, ok := f.cubes[cubeID]; ok {
		c.IsActive = false
		f.cubes[cubeID] = c
		return nil
	}
	return db.ErrCubeNotFound
}

func (f *fakeCubeStore) HardDeleteCube(ctx context.Context, cubeID string) (int64, error) {
	f.hardDeleted = append(f.hardDeleted, cubeID)
	delete(f.cubes, cubeID)
	return f.hardDeletedN, nil
}

func (f *fakeCubeStore) EnsureCubeExists(ctx context.Context, cubeID, ownerID string) (bool, error) {
	if _, ok := f.cubes[cubeID]; ok {
		return false, nil
	}
	if f.cubes == nil {
		f.cubes = map[string]db.Cube{}
	}
	f.cubes[cubeID] = db.Cube{CubeID: cubeID, CubeName: cubeID, OwnerID: ownerID, IsActive: true}
	return true, nil
}

func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newCubeHandler(store *fakeCubeStore) *Handler {
	return &Handler{logger: silentLogger(), cubeStore: store}
}
