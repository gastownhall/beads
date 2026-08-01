package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeIdentityReader struct {
	prefix       string
	prefixErr    error
	projectID    string
	projectIDErr error
}

func (f *fakeIdentityReader) GetConfig(_ context.Context, key string) (string, error) {
	if key != "issue_prefix" {
		return "", errors.New("unexpected config key: " + key)
	}
	return f.prefix, f.prefixErr
}

func (f *fakeIdentityReader) GetMetadata(_ context.Context, key string) (string, error) {
	if key != "_project_id" {
		return "", errors.New("unexpected metadata key: " + key)
	}
	return f.projectID, f.projectIDErr
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

	t.Run("prefix read error surfaces as transient, not unprovisioned", func(t *testing.T) {
		reader := &fakeIdentityReader{prefixErr: errors.New("connection reset")}
		_, _, err := adoptTeamServerIdentity(ctx, reader, "beads_gc", "gc", false, "local-id")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "connection reset")
		assert.NotContains(t, err.Error(), "bts init")
	})

	t.Run("project id read error surfaces as transient, not unprovisioned", func(t *testing.T) {
		reader := &fakeIdentityReader{prefix: "gc", projectIDErr: errors.New("connection reset")}
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
