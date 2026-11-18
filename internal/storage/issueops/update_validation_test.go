package issueops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

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

// TestValidateUpdateRequestRejectsInvalidNotesRequestShapes covers the two
// notes rules ValidateUpdateRequest owns that are properties of the REQUEST
// rather than of the patch alone, so they cannot be expressed in the
// patch-only table above. Both are contract-level: the batch apply leg reaches
// them here and nowhere else, and the single-issue HTTP handler's own
// pre-check returns before storage is reached, so without these cases the
// branches backing the published ApplyUpdateItem promise are unexercised.
func TestValidateUpdateRequestRejectsInvalidNotesRequestShapes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		request publicops.UpdateRequest
	}{
		{
			name: "forced notes overwrite without a notes value",
			request: publicops.UpdateRequest{
				Actor: "actor", IssueID: "bd-validate",
				ForceNotesOverwrite: true,
			},
		},
		{
			name: "notes replacement combined with append",
			request: publicops.UpdateRequest{
				Actor: "actor", IssueID: "bd-validate",
				Patch: publicops.IssuePatch{
					Notes:       publicops.Field[string]{Set: true, Value: "replacement"},
					AppendNotes: publicops.Field[string]{Set: true, Value: "addition"},
				},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUpdateRequest(tc.request)
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

// reporterTitle is the title from GH#8150: 499 runes but 514 bytes (one em
// dash, two en dashes, five middle dots and two primes). It fits the
// VARCHAR(500) column, so only the canonical byte rule can refuse it.
func reporterTitle(t *testing.T) string {
	t.Helper()
	title := strings.Repeat("a", 489) + "—––·····′′"
	if runes, size := utf8.RuneCountInString(title), len(title); runes != 499 || size != 514 {
		t.Fatalf("reporter fixture is %d runes / %d bytes, want 499 / 514", runes, size)
	}
	return title
}

// TestValidateScalarUpdatesTitle pins the map-update title gate (GH#8150):
// the map path bd edit, bd batch and bd rename take must refuse exactly the
// titles create and import refuse, or the store holds a row its own export
// cannot import.
func TestValidateScalarUpdatesTitle(t *testing.T) {
	for _, tc := range []struct {
		name    string
		updates map[string]interface{}
		wantErr string // "" means the update is accepted
	}{
		{"absent title", map[string]interface{}{"description": "d"}, ""},
		{"short title", map[string]interface{}{"title": "short"}, ""},
		{"500 ASCII bytes", map[string]interface{}{"title": strings.Repeat("t", 500)}, ""},
		{"250 two-byte runes", map[string]interface{}{"title": strings.Repeat("é", 250)}, ""},
		{"empty title", map[string]interface{}{"title": ""}, "title is required"},
		{"501 ASCII bytes", map[string]interface{}{"title": strings.Repeat("t", 501)}, "(got 501)"},
		{"reporter title", map[string]interface{}{"title": reporterTitle(t)}, "(got 514)"},
		{"251 two-byte runes", map[string]interface{}{"title": strings.Repeat("é", 251)}, "(got 502)"},
		{"integer title", map[string]interface{}{"title": 7}, "int"},
		{"nil title", map[string]interface{}{"title": nil}, "nil"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateScalarUpdates(context.Background(), nil, tc.updates)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateScalarUpdates() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidateScalarUpdates() error = %v, want ErrValidation", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateScalarUpdates() error = %q, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}
