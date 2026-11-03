package storage

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// configSearchStore answers SearchIssues and GetConfig from fixed values and
// records every read, so a test can tell two separate reads from one
// SearchIssuesWithConfig.
type configSearchStore struct {
	DoltStorage
	issues    []*types.Issue
	config    map[string]string
	configErr error
	calls     []string
	filters   []types.IssueFilter
}

func (s *configSearchStore) SearchIssues(_ context.Context, _ string, filter types.IssueFilter) ([]*types.Issue, error) {
	s.calls = append(s.calls, "SearchIssues")
	s.filters = append(s.filters, filter)
	return s.issues, nil
}

func (s *configSearchStore) GetConfig(_ context.Context, key string) (string, error) {
	s.calls = append(s.calls, "GetConfig "+key)
	return s.config[key], s.configErr
}

// snapshotConfigSearchStore is configSearchStore with the
// IssuesWithConfigSearcher capability, answering from the same values.
type snapshotConfigSearchStore struct {
	*configSearchStore
}

func (s snapshotConfigSearchStore) SearchIssuesWithConfig(_ context.Context, _ string, filter types.IssueFilter, key string) ([]*types.Issue, string, error) {
	s.calls = append(s.calls, "SearchIssuesWithConfig "+key)
	s.filters = append(s.filters, filter)
	return s.issues, s.config[key], nil
}

// capabilityHidingDecorator hides the inner store's optional capabilities from
// a type assertion but hands the inner store to Unwrap, as the storage
// decorators do.
type capabilityHidingDecorator struct {
	DoltStorage
}

func (d capabilityHidingDecorator) Unwrap() DoltStorage { return d.DoltStorage }

// TestSearchIssuesWithConfigReadsOneSnapshotBehindDecorators pins that a
// retention door's selection and its read of types.retained are ONE call, on
// one read transaction, when the innermost store, behind any decorators,
// offers the capability; and that a store without it (the http client) gets
// the search and then the config read, a failed config read failing both.
func TestSearchIssuesWithConfigReadsOneSnapshotBehindDecorators(t *testing.T) {
	ctx := context.Background()
	closed := types.StatusClosed
	filter := types.IssueFilter{Status: &closed}
	issues := []*types.Issue{{ID: "bd-1"}}
	setting := map[string]string{"types.retained": "op"}

	for decorators := range 3 {
		t.Run(fmt.Sprintf("decorators=%d", decorators), func(t *testing.T) {
			inner := &configSearchStore{issues: issues, config: setting}
			var s DoltStorage = snapshotConfigSearchStore{inner}
			for range decorators {
				s = capabilityHidingDecorator{s}
			}
			got, value, err := SearchIssuesWithConfig(ctx, s, "", filter, "types.retained")
			if err != nil {
				t.Fatalf("SearchIssuesWithConfig: %v", err)
			}
			if !slices.Equal(got, issues) || value != "op" {
				t.Fatalf("SearchIssuesWithConfig = %v, %q; want %v, %q", got, value, issues, "op")
			}
			if want := []string{"SearchIssuesWithConfig types.retained"}; !slices.Equal(inner.calls, want) {
				t.Fatalf("reads = %v, want %v: one read transaction, not two reads", inner.calls, want)
			}
			if !reflect.DeepEqual(inner.filters, []types.IssueFilter{filter}) {
				t.Fatalf("filters = %+v, want the caller's %+v", inner.filters, filter)
			}
		})
	}

	t.Run("without the capability", func(t *testing.T) {
		inner := &configSearchStore{issues: issues, config: setting}
		got, value, err := SearchIssuesWithConfig(ctx, capabilityHidingDecorator{inner}, "", filter, "types.retained")
		if err != nil {
			t.Fatalf("SearchIssuesWithConfig: %v", err)
		}
		if !slices.Equal(got, issues) || value != "op" {
			t.Fatalf("SearchIssuesWithConfig = %v, %q; want %v, %q", got, value, issues, "op")
		}
		if want := []string{"SearchIssues", "GetConfig types.retained"}; !slices.Equal(inner.calls, want) {
			t.Fatalf("reads = %v, want %v", inner.calls, want)
		}
	})

	t.Run("a failed config read fails the search", func(t *testing.T) {
		readErr := errors.New("config table unreadable")
		inner := &configSearchStore{issues: issues, configErr: readErr}
		got, value, err := SearchIssuesWithConfig(ctx, inner, "", filter, "types.retained")
		if !errors.Is(err, readErr) || got != nil || value != "" {
			t.Fatalf("SearchIssuesWithConfig = %v, %q, %v; want no issues and the read error", got, value, err)
		}
	})
}
