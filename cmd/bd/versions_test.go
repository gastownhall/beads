package main

import (
	"context"
	"errors"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
)

type fakeVersionLister struct {
	storage.DoltStorage
	called   bool
	versions []storage.IssueVersion
}

func (f *fakeVersionLister) ListVersions(_ context.Context, _ string) ([]storage.IssueVersion, error) {
	f.called = true
	return f.versions, nil
}

type notAVersionLister struct{ storage.DoltStorage }

// TestVersionsRefusesWhenFlagOff pins the distinction #5898 exists to make:
// with the feature off the answer is a REFUSAL, never an empty list. An empty
// list is a claim about the bead ("it has no versions"); the truth is a claim
// about the store ("it never recorded any"). Returning the former for the
// latter is the wrong-answer-not-an-empty-one failure.
func TestVersionsRefusesWhenFlagOff(t *testing.T) {
	backend := &fakeVersionLister{versions: []storage.IssueVersion{{Revision: 1}}}

	got, err := runVersions(context.Background(), backend, "bd-1", false)

	if !errors.Is(err, errVersionedHistoryOff) {
		t.Fatalf("runVersions(enabled=false) error = %v, want errVersionedHistoryOff", err)
	}
	if got != nil {
		t.Errorf("runVersions(enabled=false) returned %d versions, want none", len(got))
	}
	if backend.called {
		t.Error("runVersions(enabled=false) queried the backend; it must refuse before reading")
	}
}

// TestVersionsRefusesUnsupportedBackend pins that "this backend cannot answer"
// is distinguishable from "there are no versions" -- the third shape of an
// answer, not a smaller success.
func TestVersionsRefusesUnsupportedBackend(t *testing.T) {
	_, err := runVersions(context.Background(), &notAVersionLister{}, "bd-1", true)

	if !errors.Is(err, errVersionsUnsupported) {
		t.Fatalf("runVersions(unsupported backend) error = %v, want errVersionsUnsupported", err)
	}
	if errors.Is(err, errVersionedHistoryOff) {
		t.Error("an unsupported backend must not be reported as the feature being off: different fixes")
	}
}

// TestVersionsEmptyIsNotAnError pins the other side: a supported, enabled
// store with nothing recorded returns an empty list and no error. That is a
// truthful answer, and the command renders the no-backfill explanation.
func TestVersionsEmptyIsNotAnError(t *testing.T) {
	got, err := runVersions(context.Background(), &fakeVersionLister{}, "bd-1", true)
	if err != nil {
		t.Fatalf("runVersions on an empty store = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d versions, want 0", len(got))
	}
}

// TestVersionListerPeelsDecorators is the regression guard for the bug this
// command shipped with first: cmd/bd holds the DECORATED store, VersionLister
// is an optional capability none of the decorators forwards, and asserting
// against the wrapper reported "unsupported" for a store that could serve
// versions. The capability must be found through the decorator chain.
func TestVersionListerPeelsDecorators(t *testing.T) {
	inner := &fakeVersionLister{versions: []storage.IssueVersion{{Revision: 7}}}
	decorated := storage.NewHookFiringStore(inner, nil)

	lister, ok := versionListerFor(decorated)
	if !ok {
		t.Fatal("versionListerFor could not see through the decorator chain")
	}
	got, err := lister.ListVersions(context.Background(), "bd-1")
	if err != nil {
		t.Fatalf("ListVersions through decorator = %v", err)
	}
	if len(got) != 1 || got[0].Revision != 7 {
		t.Errorf("got %+v, want the inner store's revision 7", got)
	}
}

// TestRestrictionLabelsAreDistinct pins that the four removal reasons render
// as four different sentences. Collapsing them into one "gone" would erase
// the distinction R10 requires, and in particular "unknown" (this store has
// no lineage knowledge) is NOT "gone" -- the conformance suite pins that a
// never-synced store answers Unknown rather than Gone.
func TestRestrictionLabelsAreDistinct(t *testing.T) {
	seen := map[string]string{}
	for _, r := range []string{"gone_retention", "gone_erasure", "gone_reorganization", "unknown"} {
		label := restrictionLabel(r)
		if label == "" {
			t.Errorf("restrictionLabel(%q) is empty", r)
		}
		if prev, dup := seen[label]; dup {
			t.Errorf("restrictionLabel(%q) and (%q) both render %q; they are different answers", r, prev, label)
		}
		seen[label] = r
	}
	if restrictionLabel("unknown") == restrictionLabel("gone_erasure") {
		t.Error("unknown must not read as gone: no lineage knowledge is not erasure")
	}
}
