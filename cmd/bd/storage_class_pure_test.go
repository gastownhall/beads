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

	// Explicit versioned is returned verbatim; callers normalize it to the unset
	// marker (C2.4 omitted-when-versioned) only after plane-conflict validation,
	// so the durable request survives long enough to be honored or rejected.
	got, err = resolveStorageClass("versioned", types.TypeTask)
	if err != nil || got != types.StorageClassVersioned {
		t.Errorf("explicit versioned should be preserved: got %q, %v", got, err)
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

// reconcileStorageClassPlane is the flag-over-config decision shared by
// single-issue create and graph-apply (Protocol v0.1 §C1.3). A durable class on
// an effective wisp plane is a contradiction: an explicit class is rejected so
// the durable intent is not silently erased, while a config-derived class yields
// to the explicit plane. versioned normalizes to the unset marker only after the
// check, so on conflict the class is returned verbatim for the caller's message.
func TestReconcileStorageClassPlane(t *testing.T) {
	tests := []struct {
		name      string
		class     types.StorageClass
		explicit  bool
		wispPlane bool
		wantClass types.StorageClass
		wantConf  bool
	}{
		{"explicit unversioned + wisp plane conflicts", types.StorageClassUnversioned, true, true, types.StorageClassUnversioned, true},
		{"explicit versioned + wisp plane conflicts (verbatim)", types.StorageClassVersioned, true, true, types.StorageClassVersioned, true},
		{"config unversioned yields to wisp plane", types.StorageClassUnversioned, false, true, "", false},
		{"config versioned yields to wisp plane", types.StorageClassVersioned, false, true, "", false},
		{"explicit unversioned stays on a durable row", types.StorageClassUnversioned, true, false, types.StorageClassUnversioned, false},
		{"config unversioned stays on a durable row", types.StorageClassUnversioned, false, false, types.StorageClassUnversioned, false},
		{"versioned normalizes to unset on a durable row", types.StorageClassVersioned, true, false, "", false},
		{"unset class with a wisp plane is a no-op", "", true, true, "", false},
		{"unset class without a wisp plane is a no-op", "", false, false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotClass, gotConf := reconcileStorageClassPlane(tt.class, tt.explicit, tt.wispPlane)
			if gotClass != tt.wantClass || gotConf != tt.wantConf {
				t.Errorf("reconcileStorageClassPlane(%q, explicit=%v, wisp=%v) = (%q, %v), want (%q, %v)",
					tt.class, tt.explicit, tt.wispPlane, gotClass, gotConf, tt.wantClass, tt.wantConf)
			}
		})
	}
}
