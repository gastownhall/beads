package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

type gcSizeStoreStub struct {
	storage.DoltStorage
	size int64
	err  error
}

func (s *gcSizeStoreStub) ActiveDatabaseSize(context.Context) (int64, error) {
	return s.size, s.err
}

type gcLocatorOnlyStoreStub struct {
	storage.DoltStorage
	path string
}

func (s *gcLocatorOnlyStoreStub) Path() string   { return s.path }
func (s *gcLocatorOnlyStoreStub) CLIDir() string { return s.path }

func TestStoreSizeBytesForStoreUsesOnlyActiveDatabaseSizer(t *testing.T) {
	t.Parallel()

	failure := errors.New("measurement failed")
	tests := []struct {
		name  string
		store storage.DoltStorage
		want  int64
	}{
		{
			name:  "active database available",
			store: &gcSizeStoreStub{size: 42},
			want:  42,
		},
		{
			name: "unsupported active database",
			store: &gcSizeStoreStub{err: &storage.ErrUnsupported{
				Op:      "ActiveDatabaseSize",
				Backend: "external",
			}},
			want: -1,
		},
		{
			name:  "measurement failure",
			store: &gcSizeStoreStub{err: failure},
			want:  -1,
		},
		{
			name:  "legacy locator is not a size fallback",
			store: &gcLocatorOnlyStoreStub{path: t.TempDir()},
			want:  -1,
		},
		{
			name: "nil store",
			want: -1,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := storeSizeBytesForStore(t.Context(), tc.store); got != tc.want {
				t.Fatalf("storeSizeBytesForStore() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestAddGCSizeJSONOmitsUnavailableMeasurements(t *testing.T) {
	t.Parallel()

	result := map[string]interface{}{}
	addGCSizeJSON(result, -1, -1)
	if len(result) != 0 {
		t.Fatalf("unavailable measurements produced JSON fields: %#v", result)
	}

	addGCSizeJSON(result, 42, -1)
	if got := result["size_before_bytes"]; got != int64(42) {
		t.Fatalf("size_before_bytes = %#v, want 42", got)
	}
	if _, ok := result["size_after_bytes"]; ok {
		t.Fatalf("unavailable size_after_bytes was emitted: %#v", result)
	}
	if _, ok := result["freed_bytes"]; ok {
		t.Fatalf("freed_bytes was emitted without both measurements: %#v", result)
	}
}
