package main

import (
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// resolveStorageClass: the explicit flag path (config-default resolution
// rides config.GetString and is exercised by the embedded create tests).
func TestResolveStorageClassExplicit(t *testing.T) {
	got, err := resolveStorageClass("unversioned", types.TypeTask)
	if err != nil || got != types.StorageClassUnversioned {
		t.Errorf("explicit unversioned: got %q, %v", got, err)
	}

	// Explicit versioned normalizes to unset (C2.4 omitted-when-versioned)
	// while still overriding any per-type config default upstream.
	got, err = resolveStorageClass("versioned", types.TypeTask)
	if err != nil || got != "" {
		t.Errorf("explicit versioned should normalize to unset: got %q, %v", got, err)
	}

	got, err = resolveStorageClass("ephemeral", types.TypeTask)
	if err != nil || got != types.StorageClassEphemeral {
		t.Errorf("explicit ephemeral: got %q, %v", got, err)
	}

	if _, err := resolveStorageClass("bogus", types.TypeTask); err == nil {
		t.Error("invalid explicit value should error")
	}
}

func TestValidateStorageClassConfig(t *testing.T) {
	if err := validateStorageClassConfig("storage-class.event", "unversioned"); err != nil {
		t.Errorf("valid key+value rejected: %v", err)
	}
	if err := validateStorageClassConfig("storage-class.", "unversioned"); err == nil {
		t.Error("empty type suffix should be rejected")
	}
	if err := validateStorageClassConfig("storage-class.event.extra", "unversioned"); err == nil {
		t.Error("nested suffix should be rejected")
	}
	err := validateStorageClassConfig("storage-class.event", "permanent")
	if err == nil || !strings.Contains(err.Error(), "versioned, unversioned, or ephemeral") {
		t.Errorf("bad value should be rejected with the value list, got: %v", err)
	}
	// The suffix is validated too: create-time lookup keys on the Normalize()d
	// type, so an alias or typo would otherwise pass here and silently never
	// match (lion's #5149 should-fix).
	err = validateStorageClassConfig("storage-class.feat", "unversioned")
	if err == nil || !strings.Contains(err.Error(), "storage-class.feature") {
		t.Errorf("alias suffix should be rejected with the canonical key hint, got: %v", err)
	}
	if err := validateStorageClassConfig("storage-class.taks", "unversioned"); err == nil {
		t.Error("unknown (typo) suffix should be rejected")
	}
	if err := validateStorageClassConfig("storage-class.task", "unversioned"); err != nil {
		t.Errorf("canonical built-in suffix rejected: %v", err)
	}
}
