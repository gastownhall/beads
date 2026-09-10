package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/steveyegge/beads/issueops"
)

// fakeIdentityReader stands in for issueops.InitVerifier, which is what
// adoptTeamServerIdentity now reads the bts-provisioned identity through.
//
// It answers with the PAIR and one error, because that is the shape of the role:
// the two markers are read in one snapshot, so there is no longer a state where
// the prefix read succeeded and the project-id read did not.
type fakeIdentityReader struct {
	prefix    string
	projectID string
	readErr   error
}

func (f *fakeIdentityReader) VerifyIdentity(_ context.Context, _ issueops.VerifyIdentityRequest) (issueops.VerifyIdentityResult, error) {
	if f.readErr != nil {
		return issueops.VerifyIdentityResult{}, f.readErr
	}
	return issueops.VerifyIdentityResult{Prefix: f.prefix, ProjectID: f.projectID}, nil
}

func TestAdoptTeamServerIdentity(t *testing.T) {
	ctx := context.Background()

	t.Run("adopts provisioned identity", func(t *testing.T) {
		reader := &fakeIdentityReader{prefix: "gc", projectID: "bts-id"}
		prefix, projectID, err := adoptTeamServerIdentity(ctx, reader, "beads_gc", "local_guess", false, "local-id")
		require.NoError(t, err)
		assert.Equal(t, "gc", prefix)
		assert.Equal(t, "bts-id", projectID)
	})

	t.Run("matching explicit prefix adopts", func(t *testing.T) {
		reader := &fakeIdentityReader{prefix: "gc", projectID: "bts-id"}
		prefix, _, err := adoptTeamServerIdentity(ctx, reader, "beads_gc", "gc", true, "local-id")
		require.NoError(t, err)
		assert.Equal(t, "gc", prefix)
	})

	t.Run("conflicting explicit prefix is a hard error naming both", func(t *testing.T) {
		reader := &fakeIdentityReader{prefix: "gc", projectID: "bts-id"}
		_, _, err := adoptTeamServerIdentity(ctx, reader, "beads_gc", "other", true, "local-id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), `"other"`)
		assert.Contains(t, err.Error(), `"gc"`)
	})

	t.Run("conflicting derived prefix adopts silently", func(t *testing.T) {
		reader := &fakeIdentityReader{prefix: "gc", projectID: "bts-id"}
		prefix, _, err := adoptTeamServerIdentity(ctx, reader, "beads_gc", "cwd_basename", false, "local-id")
		require.NoError(t, err)
		assert.Equal(t, "gc", prefix)
	})

	t.Run("missing prefix names bts init", func(t *testing.T) {
		reader := &fakeIdentityReader{prefix: "", projectID: "bts-id"}
		_, _, err := adoptTeamServerIdentity(ctx, reader, "beads_gc", "gc", false, "local-id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bts init")
		assert.Contains(t, err.Error(), "bts migrate")
	})

	t.Run("missing project id names bts init", func(t *testing.T) {
		reader := &fakeIdentityReader{prefix: "gc", projectID: ""}
		_, _, err := adoptTeamServerIdentity(ctx, reader, "beads_gc", "gc", false, "local-id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "bts init")
		assert.Contains(t, err.Error(), "_project_id")
	})

	// The distinction this pins is issueops.InitVerifier's whole reason for
	// existing: an ABSENT identity means "unprovisioned, run bts init" and an
	// UNREADABLE one means "the connection failed". Reporting the second as the
	// first is how a flaky link gets a shared database re-provisioned.
	t.Run("read error surfaces as transient, not unprovisioned", func(t *testing.T) {
		reader := &fakeIdentityReader{readErr: errors.New("connection reset")}
		_, _, err := adoptTeamServerIdentity(ctx, reader, "beads_gc", "gc", false, "local-id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection reset")
		assert.NotContains(t, err.Error(), "bts init")
	})

	t.Run("stale local project id is replaced by the provisioned one", func(t *testing.T) {
		reader := &fakeIdentityReader{prefix: "gc", projectID: "bts-id"}
		_, projectID, err := adoptTeamServerIdentity(ctx, reader, "beads_gc", "gc", false, "stale-local-id")
		require.NoError(t, err)
		assert.Equal(t, "bts-id", projectID)
	})
}
