package uow

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestInitSchemaPreviewDoesNotMutate is the proxied-server half of the
// preview policy: a --dry-run/--inspect command reaches this open during root
// pre-run, before its own RunE has had any chance to honor the flag, so the
// open must attach to the existing database and nothing more. sqlmock is in
// ordered mode with only USE expected, so a CREATE DATABASE or a migration
// statement fails the test rather than silently succeeding.
func TestInitSchemaPreviewDoesNotMutate(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectExec("USE `beads`").WillReturnResult(sqlmock.NewResult(0, 0))

	p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, preview: true}
	if err := p.initSchema(context.Background(), "beads"); err != nil {
		t.Fatalf("initSchema(preview): %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet or extra statements during preview open: %v", err)
	}
}

// TestInitSchemaPreviewMissingDatabaseFailsClosed pins the other half of the
// contract: a database the preview cannot attach to is reported, never
// created. The message has to say so, because "not found" on a path that
// normally creates the database reads as a bug otherwise.
func TestInitSchemaPreviewMissingDatabaseFailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = db.Close() }()

	mock.ExpectExec("USE `beads`").WillReturnError(errors.New("database not found: beads"))

	p := &doltSQLProvider{defaultBranch: defaultBranch, db: db, preview: true}
	err = p.initSchema(context.Background(), "beads")
	if err == nil {
		t.Fatal("initSchema(preview) on a missing database: got nil, want error")
	}
	if !strings.Contains(err.Error(), "--dry-run") {
		t.Fatalf("preview open error does not explain the preview contract: %v", err)
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet or extra statements during preview open: %v", err)
	}
}

// TestApplyProviderOptions keeps the option plumbing honest: the ordinary
// callers pass nothing and must stay on the mutating open.
func TestApplyProviderOptions(t *testing.T) {
	if got := applyProviderOptions(nil); got.preview {
		t.Fatal("applyProviderOptions(nil).preview = true, want false")
	}
	if got := applyProviderOptions([]ProviderOption{WithPreview()}); !got.preview {
		t.Fatal("applyProviderOptions(WithPreview()).preview = false, want true")
	}
}
