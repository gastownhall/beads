package domain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// stubPrefixConfigRepo answers only the read resolveTopLevelPrefix makes.
// The embedded interface is nil on purpose: a call this test did not intend
// panics rather than passing quietly.
type stubPrefixConfigRepo struct {
	ConfigSQLRepository
	prefix string
}

func (s stubPrefixConfigRepo) GetConfig(_ context.Context, _ string) (string, error) {
	return s.prefix, nil
}

// TestResolveTopLevelPrefixWithoutPrefixWrapsErrNotInitialized pins that the
// domain mint path fails the way issueops' does when the database carries no
// issue_prefix row: the shared storage.NewErrIssuePrefixMissing text, wrapped
// around storage.ErrNotInitialized, so every create path names bd bootstrap
// as the repair and no caller can mint past an unseeded prefix (GH#3494).
// At the branch base this path returned a bare "issue_prefix config is
// missing" — no sentinel, no repair.
func TestResolveTopLevelPrefixWithoutPrefixWrapsErrNotInitialized(t *testing.T) {
	u := &issueUseCaseImpl{cfgRepo: stubPrefixConfigRepo{prefix: ""}}

	_, err := u.resolveTopLevelPrefix(context.Background(), &types.Issue{}, false)
	if err == nil {
		t.Fatal("resolveTopLevelPrefix() returned nil error, want error")
	}
	if !errors.Is(err, storage.ErrNotInitialized) {
		t.Fatalf("resolveTopLevelPrefix error = %v, want storage.ErrNotInitialized", err)
	}
	msg := err.Error()
	for _, want := range []string{
		"issue_prefix config is missing",
		"bd bootstrap",
		"bd init --prefix",
		"to finish setting up an existing database",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("resolveTopLevelPrefix error missing %q: %s", want, msg)
		}
	}
	if strings.Contains(msg, "use key 'issue-prefix'") {
		t.Errorf("resolveTopLevelPrefix recommends a config.yaml key that does not satisfy it: %s", msg)
	}
}
