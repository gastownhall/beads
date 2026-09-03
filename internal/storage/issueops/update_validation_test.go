package issueops

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	publicops "github.com/steveyegge/beads/issueops"
)

func TestValidateUpdateRequestRejectsInvalidCanonicalFields(t *testing.T) {
	negative := -1
	for _, tc := range []struct {
		name  string
		patch publicops.IssuePatch
	}{
		{"priority above range", publicops.IssuePatch{Priority: publicops.Field[int]{Set: true, Value: 9}}},
		{"empty title", publicops.IssuePatch{Title: publicops.Field[string]{Set: true}}},
		{"title over byte limit", publicops.IssuePatch{Title: publicops.Field[string]{Set: true, Value: strings.Repeat("t", 501)}}},
		{"negative estimate", publicops.IssuePatch{EstimatedMinutes: publicops.Field[*int]{Set: true, Value: &negative}}},
		{"unknown persistence mode", publicops.IssuePatch{Persistence: publicops.Field[publicops.PersistenceMode]{Set: true, Value: "archived"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUpdateRequest(publicops.UpdateRequest{Actor: "actor", IssueID: "bd-validate", Patch: tc.patch})
			if !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidateUpdateRequest() error = %v, want ErrValidation", err)
			}
		})
	}
}

func TestApplyMetadataPatchRejectsUnsafeKeysAndNullMerge(t *testing.T) {
	for _, tc := range []struct {
		name  string
		patch publicops.MetadataPatch
	}{
		{"set key with unsafe characters", publicops.MetadataPatch{Set: map[string]json.RawMessage{"bad-key": json.RawMessage(`"value"`)}}},
		{"unset key with unsafe characters", publicops.MetadataPatch{Unset: []string{"bad-key"}}},
		{"null merge payload", publicops.MetadataPatch{Merge: publicops.Field[json.RawMessage]{Set: true, Value: json.RawMessage(`null`)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, changed, err := ApplyMetadataPatch(json.RawMessage(`{"keep":"yes"}`), tc.patch)
			if !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ApplyMetadataPatch() = %s, %t, %v; want ErrValidation", next, changed, err)
			}
		})
	}
}
