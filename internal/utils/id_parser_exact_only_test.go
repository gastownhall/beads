package utils_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/beadserrors"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/utils"
)

// exactOnlyStore is fakeResolverStore as an exact-lookup-only backend (the
// http client's shape, S6b): it counts every call, and its substring search
// fails the test outright, because the resolver must never reach it.
type exactOnlyStore struct {
	t        *testing.T
	inner    fakeResolverStore
	lookups  []string
	configs  int
	allReads int
}

func (s *exactOnlyStore) ExactIDLookupOnly() bool { return true }

func (s *exactOnlyStore) SearchIssues(ctx context.Context, q string, f types.IssueFilter) ([]*types.Issue, error) {
	s.lookups = append(s.lookups, strings.Join(f.IDs, ","))
	return s.inner.SearchIssues(ctx, q, f)
}

func (s *exactOnlyStore) SearchIssueIDs(context.Context, string, types.IssueFilter) ([]string, error) {
	s.t.Error("exact-only resolution reached the substring search")
	return nil, errors.New("no substring search")
}

func (s *exactOnlyStore) GetConfig(ctx context.Context, key string) (string, error) {
	s.configs++
	return s.inner.GetConfig(ctx, key)
}

func (s *exactOnlyStore) GetAllConfig(context.Context) (map[string]string, error) {
	s.allReads++
	return s.inner.config, nil
}

func TestResolvePartialID_ExactOnlyStore(t *testing.T) {
	newStore := func(t *testing.T) *exactOnlyStore {
		return &exactOnlyStore{t: t, inner: fakeResolverStore{
			issues: []fakeIssue{{id: "mc-abc123"}, {id: "mc-wisp-z9"}},
			config: map[string]string{"issue_prefix": "mc"},
		}}
	}

	t.Run("present prefixed id is one lookup", func(t *testing.T) {
		s := newStore(t)
		got, err := utils.ResolvePartialID(context.Background(), s, "mc-abc123")
		if err != nil || got != "mc-abc123" {
			t.Fatalf("got %q, %v", got, err)
		}
		if len(s.lookups) != 1 || s.configs+s.allReads != 0 {
			t.Errorf("lookups=%v configs=%d allReads=%d; want one lookup and no settings read", s.lookups, s.configs, s.allReads)
		}
	})

	t.Run("missing prefixed id is a not-found after one lookup", func(t *testing.T) {
		s := newStore(t)
		_, err := utils.ResolvePartialID(context.Background(), s, "mc-nope")
		if !errors.Is(err, beadserrors.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if !strings.HasPrefix(err.Error(), `no issue found matching "mc-nope"`) {
			t.Errorf("err text %q lost the not-found lead every matcher reads", err)
		}
		if len(s.lookups) != 1 || s.configs+s.allReads != 0 {
			t.Errorf("lookups=%v configs=%d allReads=%d; want exactly one lookup", s.lookups, s.configs, s.allReads)
		}
	})

	t.Run("bare hash tries its prefixed spelling with one settings read", func(t *testing.T) {
		s := newStore(t)
		got, err := utils.ResolvePartialID(context.Background(), s, "abc123")
		if err != nil || got != "mc-abc123" {
			t.Fatalf("got %q, %v", got, err)
		}
		if strings.Join(s.lookups, " ") != "abc123 mc-abc123" || s.allReads != 1 || s.configs != 0 {
			t.Errorf("lookups=%v configs=%d allReads=%d", s.lookups, s.configs, s.allReads)
		}
	})

	t.Run("an abbreviation is not resolved and reads as not found", func(t *testing.T) {
		s := newStore(t)
		_, err := utils.ResolvePartialID(context.Background(), s, "abc")
		if !errors.Is(err, beadserrors.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound (no substring search exists)", err)
		}
	})

	t.Run("the exact resolver takes the same path", func(t *testing.T) {
		s := newStore(t)
		if _, err := utils.ResolvePartialIDExact(context.Background(), s, "mc-nope"); !errors.Is(err, beadserrors.ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})
}

// TestResolvePartialID_SkipsTheRepeatedNormalizedProbe pins the general
// path's one-lookup saving: an input that normalizes to itself is not asked
// for a second time.
func TestResolvePartialID_SkipsTheRepeatedNormalizedProbe(t *testing.T) {
	s := &countingResolverStore{fakeResolverStore: fakeResolverStore{
		issues: []fakeIssue{{id: "mc-abc123"}},
		config: map[string]string{"issue_prefix": "mc"},
	}}
	if _, err := utils.ResolvePartialID(context.Background(), s, "mc-zzz"); err == nil {
		t.Fatal("resolved an id that names nothing")
	}
	if s.exactProbes != 1 {
		t.Errorf("exact probes = %d, want 1 (mc-zzz normalizes to itself)", s.exactProbes)
	}
}

type countingResolverStore struct {
	fakeResolverStore
	exactProbes int
}

func (s *countingResolverStore) SearchIssues(ctx context.Context, q string, f types.IssueFilter) ([]*types.Issue, error) {
	if len(f.IDs) > 0 {
		s.exactProbes++
	}
	return s.fakeResolverStore.SearchIssues(ctx, q, f)
}
