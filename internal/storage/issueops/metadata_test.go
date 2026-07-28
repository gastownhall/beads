package issueops

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/storage"
)

// expectNonWisp makes IsActiveWispInTx report "not a wisp" (empty result set).
func expectNonWisp(mock sqlmock.Sqlmock, id string) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT 1 FROM wisps WHERE id = ? LIMIT 1")).
		WithArgs(id).WillReturnRows(sqlmock.NewRows([]string{"1"}))
}

// TestMergeMetadataInTx exercises the cheap, deterministic branches of the merge
// primitive hermetically via sqlmock: the invalid-JSON guard (no query) and the
// not-found path (routed SELECT → sql.ErrNoRows → wrapped storage.ErrNotFound).
// The happy path routes through UpdateIssueInTx (event + validation + full
// update SQL) and is covered end-to-end against real Dolt in the dolt package.
func TestMergeMetadataInTx(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid json is rejected before any query", func(t *testing.T) {
		_, _, tx := beginMockTx(t)
		if err := MergeMetadataInTx(ctx, tx, "iss-1", "k", json.RawMessage(`{bad`), "actor"); err == nil {
			t.Fatal("want error for invalid JSON value, got nil")
		}
	})

	t.Run("missing issue returns ErrNotFound", func(t *testing.T) {
		_, mock, tx := beginMockTx(t)
		expectNonWisp(mock, "iss-missing")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT metadata FROM issues WHERE id = ?")).
			WithArgs("iss-missing").WillReturnError(sql.ErrNoRows)

		err := MergeMetadataInTx(ctx, tx, "iss-missing", "k", json.RawMessage(`"v"`), "actor")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("err = %v, want errors.Is(_, storage.ErrNotFound)", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

// TestMergeMetadataInTxSchemaValidation proves the merge routes through the
// configured metadata-schema validation before it writes — the check the old
// SlotSet inherited from the generic update path. With error-mode validation and
// a required field, a merged blob that lacks the required field is rejected and
// UpdateIssueInTx is never reached (no UPDATE/event is mocked or executed).
func TestMergeMetadataInTxSchemaValidation(t *testing.T) {
	// t.Setenv forces non-parallel; ignore the repo's tracked config so only the
	// schema we inject below is in effect.
	t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")
	if err := config.Initialize(); err != nil {
		t.Fatalf("config.Initialize: %v", err)
	}
	defer config.ResetForTesting()
	config.Set("validation.metadata.mode", "error")
	config.Set("validation.metadata.fields", map[string]interface{}{
		"required_field": map[string]interface{}{"type": "string", "required": true},
	})

	_, mock, tx := beginMockTx(t)
	expectNonWisp(mock, "iss-1")
	// Existing metadata has no required_field; the merge adds an unrelated key.
	mock.ExpectQuery(regexp.QuoteMeta("SELECT metadata FROM issues WHERE id = ?")).
		WithArgs("iss-1").WillReturnRows(sqlmock.NewRows([]string{"metadata"}).AddRow(`{}`))

	err := MergeMetadataInTx(context.Background(), tx, "iss-1", "other", json.RawMessage(`"x"`), "actor")
	if err == nil || !strings.Contains(err.Error(), "schema violation") {
		t.Fatalf("err = %v, want a metadata schema violation", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations (validation should have stopped before the update): %v", err)
	}
}

// TestDeleteMetadataInTx covers the clear primitive's cheap branches: a missing
// issue is ErrNotFound, and clearing a key that is absent is a no-op that issues
// no write (and therefore records no event), matching the historical SlotClear.
func TestDeleteMetadataInTx(t *testing.T) {
	ctx := context.Background()

	t.Run("missing issue returns ErrNotFound", func(t *testing.T) {
		_, mock, tx := beginMockTx(t)
		expectNonWisp(mock, "iss-missing")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT metadata FROM issues WHERE id = ?")).
			WithArgs("iss-missing").WillReturnError(sql.ErrNoRows)

		err := DeleteMetadataInTx(ctx, tx, "iss-missing", "k", "actor")
		if !errors.Is(err, storage.ErrNotFound) {
			t.Fatalf("err = %v, want errors.Is(_, storage.ErrNotFound)", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})

	t.Run("clearing an absent key is a no-op with no write", func(t *testing.T) {
		_, mock, tx := beginMockTx(t)
		expectNonWisp(mock, "iss-1")
		mock.ExpectQuery(regexp.QuoteMeta("SELECT metadata FROM issues WHERE id = ?")).
			WithArgs("iss-1").WillReturnRows(sqlmock.NewRows([]string{"metadata"}).AddRow(`{"other":"y"}`))
		// No ExpectExec: the absent-key clear must not issue an UPDATE.

		if err := DeleteMetadataInTx(ctx, tx, "iss-1", "gone", "actor"); err != nil {
			t.Fatalf("DeleteMetadataInTx (absent key): %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}
