package main

import (
	"context"
	"errors"
	"strings"
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

func TestSuggestFullGC(t *testing.T) {
	t.Parallel()

	const mib = 1 << 20
	tests := []struct {
		name          string
		before, after int64
		want          bool
	}{
		{name: "unmeasurable before", before: -1, after: 100 * mib},
		{name: "unmeasurable after", before: 100 * mib, after: -1},
		{name: "both unmeasurable", before: -1, after: -1},
		{name: "small store freeing nothing is not worth a full pass", before: 8 * mib, after: 8 * mib},
		{name: "just below the store floor", before: 64*mib - 1, after: 64*mib - 1},
		{name: "at the store floor, freed nothing", before: 64 * mib, after: 64 * mib, want: true},
		{name: "large store freed nothing", before: 4096 * mib, after: 4096 * mib, want: true},
		{name: "freed 0.9% of the store", before: 1000 * mib, after: 991 * mib, want: true},
		{name: "freed exactly 1% is not ~nothing", before: 1000 * mib, after: 990 * mib},
		{name: "field report: 1276 MB -> 971 MB freed 24%", before: 1276 * mib, after: 971 * mib},
		{name: "store grew during the pass", before: 1000 * mib, after: 1001 * mib, want: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := suggestFullGC(tc.before, tc.after); got != tc.want {
				t.Fatalf("suggestFullGC(%d, %d) = %v, want %v", tc.before, tc.after, got, tc.want)
			}
		})
	}
}

// TestGCHelpDocumentsFullPass pins the help text explaining why a default Dolt
// GC can reclaim nothing. These Long strings are the source the CLI reference
// pages are generated from, so this wording is maintained only here.
func TestGCHelpDocumentsFullPass(t *testing.T) {
	t.Parallel()

	full := gcCmd.Flags().Lookup("full")
	if full == nil {
		t.Fatal("bd gc has no --full flag")
	}
	if !strings.Contains(full.Usage, "all generations") {
		t.Errorf("--full usage does not mention all generations: %q", full.Usage)
	}

	for _, want := range []string{"--full", "old generation", "generational"} {
		if !strings.Contains(gcCmd.Long, want) {
			t.Errorf("gcCmd.Long does not mention %q", want)
		}
	}

	rewrites := []struct {
		name string
		long string
	}{
		{"flatten", flattenCmd.Long},
		{"compact", compactDoltCmd.Long},
	}
	for _, cmd := range rewrites {
		if !strings.Contains(cmd.long, "full Dolt GC") {
			t.Errorf("%s help does not say it runs a full Dolt GC", cmd.name)
		}
		if !strings.Contains(cmd.long, "old generation") {
			t.Errorf("%s help does not explain the old generation", cmd.name)
		}
	}
}
