package flatfile

import (
	"context"
	"testing"
)

// Oracle: the SQL backends keep metadata in a separate 'metadata' table and
// GetAllConfigInTx selects only FROM config — so bd init's
// SetMetadata(_project_id) never shows up in 'bd config list', and no config
// operation can read, overwrite, or delete repo metadata.

func TestMetadataInvisibleToConfigAPI(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetMetadata(ctx, "repo_id", "fingerprint-1"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}
	if err := s.SetConfig(ctx, "issue_prefix", "bd"); err != nil {
		t.Fatalf("SetConfig: %v", err)
	}

	all, err := s.GetAllConfig(ctx)
	if err != nil {
		t.Fatalf("GetAllConfig: %v", err)
	}
	if _, ok := all["metadata:repo_id"]; ok {
		t.Error("GetAllConfig leaked metadata:repo_id (SQL selects only FROM config)")
	}
	if all["issue_prefix"] != "bd" {
		t.Errorf("GetAllConfig missing real config key, got %v", all)
	}

	// GetConfig must not read through into the metadata namespace.
	if v, err := s.GetConfig(ctx, "metadata:repo_id"); err != nil || v != "" {
		t.Errorf("GetConfig(metadata:repo_id) = (%q, %v), want empty (no config row on SQL)", v, err)
	}
}

func TestConfigWritesCannotClobberMetadata(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.SetMetadata(ctx, "repo_id", "fingerprint-1"); err != nil {
		t.Fatalf("SetMetadata: %v", err)
	}

	// SetConfig on the reserved prefix must not overwrite repo metadata.
	if err := s.SetConfig(ctx, "metadata:repo_id", "evil"); err == nil {
		t.Error("SetConfig(metadata:repo_id) succeeded; want reserved-prefix rejection")
	}
	if v, err := s.GetMetadata(ctx, "repo_id"); err != nil || v != "fingerprint-1" {
		t.Errorf("GetMetadata(repo_id) = (%q, %v), want fingerprint-1 untouched", v, err)
	}

	// DeleteConfig only touches the config namespace on SQL; the metadata
	// row must survive (and the call succeeds silently, like deleting any
	// missing config key).
	if err := s.DeleteConfig(ctx, "metadata:repo_id"); err != nil {
		t.Fatalf("DeleteConfig(metadata:repo_id): %v", err)
	}
	if v, err := s.GetMetadata(ctx, "repo_id"); err != nil || v != "fingerprint-1" {
		t.Errorf("GetMetadata(repo_id) after DeleteConfig = (%q, %v), want fingerprint-1 untouched", v, err)
	}
}
