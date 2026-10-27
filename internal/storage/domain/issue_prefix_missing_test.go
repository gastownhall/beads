package domain

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// emptyPrefixConfigRepo answers every issue_prefix read with an empty value,
// the state of a database that was never fully initialized.
type emptyPrefixConfigRepo struct {
	ConfigSQLRepository
}

func (emptyPrefixConfigRepo) GetConfig(context.Context, string) (string, error) {
	return "", nil
}

// TestResolveTopLevelPrefixMissingPrefixNamesBootstrap pins that the domain
// create path reports a missing issue_prefix the way the issueops path does:
// wrapped in storage.ErrNotInitialized, naming `bd bootstrap` for an existing
// database and `bd init --prefix` for a new project.
func TestResolveTopLevelPrefixMissingPrefixNamesBootstrap(t *testing.T) {
	u := &issueUseCaseImpl{cfgRepo: emptyPrefixConfigRepo{}}
	_, err := u.resolveTopLevelPrefix(context.Background(), &types.Issue{}, false)
	if err == nil {
		t.Fatal("resolveTopLevelPrefix with no issue_prefix returned nil error")
	}
	if !errors.Is(err, storage.ErrNotInitialized) {
		t.Errorf("error %v does not wrap storage.ErrNotInitialized", err)
	}
	for _, want := range []string{
		"issue_prefix config is missing",
		"bd bootstrap",
		"existing database",
		"bd init --prefix",
		"config.yaml alone does not satisfy it",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}
