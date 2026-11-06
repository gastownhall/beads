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

// TestValidateScalarUpdatesTitle covers the title branch of the map-update
// gate (GH#8150): bd edit, bd batch and bd rename submit raw update maps, and
// before this branch the map path stored titles that create and import refuse
// — a row the store's own export/import cannot take back. The guarded
// UpdateRequest path already applied the same types.ValidateIssueTitle rule,
// so this table mirrors its cases at the map layer. Message assertions use
// unit-agnostic fragments ("title must be 500", "(got 514)"); the
// characters-vs-bytes wording of the canonical error is pinned by the types
// package's own tests.
func TestValidateScalarUpdatesTitle(t *testing.T) {
	// The reporter's fixture: 499 characters, 514 bytes — one em dash, two en
	// dashes, five middle dots, two primes. Under 500 characters, so it fits
	// the issues.title VARCHAR(500) column; only bytes-based validation
	// refuses it.
	reporter := strings.Repeat("a", 489) + "—––·····′′"
	if runes := utf8.RuneCountInString(reporter); runes != 499 {
		t.Fatalf("reporter fixture is %d runes, want 499", runes)
	}
	if size := len(reporter); size != 514 {
		t.Fatalf("reporter fixture is %d bytes, want 514", size)
	}

	for _, tc := range []struct {
		name    string
		updates map[string]interface{}
		wantErr bool
		wantMsg string
	}{
		{"empty title", map[string]interface{}{"title": ""}, true, "title is required"},
		{"501 ASCII bytes", map[string]interface{}{"title": strings.Repeat("a", 501)}, true, "title must be 500"},
		{"reporter fixture, 499 characters 514 bytes", map[string]interface{}{"title": reporter}, true, "(got 514)"},
		{"251 × é, 502 bytes", map[string]interface{}{"title": strings.Repeat("é", 251)}, true, "(got 502)"},
		{"non-string title (int)", map[string]interface{}{"title": 7}, true, "update title"},
		{"non-string title (nil)", map[string]interface{}{"title": nil}, true, "update title"},
		{"no title key", map[string]interface{}{"description": "x"}, false, ""},
		{"short title", map[string]interface{}{"title": "ok"}, false, ""},
		{"exactly 500 ASCII bytes", map[string]interface{}{"title": strings.Repeat("a", 500)}, false, ""},
		{"250 × é, exactly 500 bytes", map[string]interface{}{"title": strings.Repeat("é", 250)}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// No issue_type key, so the config-dependent branch never runs and
			// nil tx proves the title check needs neither config nor database.
			err := ValidateScalarUpdates(context.Background(), nil, tc.updates)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("ValidateScalarUpdates() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, storage.ErrValidation) {
				t.Fatalf("ValidateScalarUpdates() = %v, want ErrValidation", err)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("ValidateScalarUpdates() = %v, want error containing %q", err, tc.wantMsg)
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
