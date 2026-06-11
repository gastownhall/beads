package issueops

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

func TestAddAttachmentInTx(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	createdAt := time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC)
	attachment := &types.Attachment{
		IssueID:          "bd-abc",
		ContentHash:      "abc123",
		OriginalFilename: "body.md",
		MimeType:         "text/markdown",
		ByteSize:         1234,
		StorageRelPath:   "attachments/bd-abc/abc123",
		CreatedBy:        "tester",
		CreatedAt:        createdAt,
	}

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM issues WHERE id = ?)`)).
		WithArgs("bd-abc").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta(`
		INSERT INTO attachments (
			id, issue_id, hash_algorithm, content_hash, original_filename,
			mime_type, byte_size, storage_relpath, created_by, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)).
		WithArgs(sqlmock.AnyArg(), "bd-abc", "sha256", "abc123", "body.md", "text/markdown", int64(1234), "attachments/bd-abc/abc123", "tester", createdAt).
		WillReturnResult(sqlmock.NewResult(1, 1))

	got, err := AddAttachmentInTx(ctx, tx, attachment)
	if err != nil {
		t.Fatalf("AddAttachmentInTx: %v", err)
	}
	if got.ID == "" {
		t.Fatal("AddAttachmentInTx generated empty ID")
	}
	if got.HashAlgorithm != "sha256" {
		t.Fatalf("HashAlgorithm = %q, want sha256", got.HashAlgorithm)
	}
	if got.CreatedAt != createdAt {
		t.Fatalf("CreatedAt = %s, want %s", got.CreatedAt, createdAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestAddAttachmentInTxMissingIssue(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	mock.ExpectQuery(regexp.QuoteMeta(`SELECT EXISTS(SELECT 1 FROM issues WHERE id = ?)`)).
		WithArgs("bd-missing").
		WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	_, err = AddAttachmentInTx(ctx, tx, &types.Attachment{IssueID: "bd-missing"})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("AddAttachmentInTx error = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestListAndCountAttachmentsInTx(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	createdAt := time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, issue_id, hash_algorithm, content_hash, original_filename,
		       mime_type, byte_size, storage_relpath, created_by, created_at
		FROM attachments
		WHERE issue_id = ?
		ORDER BY created_at ASC, id ASC
	`)).
		WithArgs("bd-abc").
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "hash_algorithm", "content_hash", "original_filename", "mime_type", "byte_size", "storage_relpath", "created_by", "created_at"}).
			AddRow("att-1", "bd-abc", "sha256", "abc123", "body.md", "text/markdown", int64(1234), "attachments/bd-abc/abc123", "tester", createdAt))
	mock.ExpectQuery(regexp.QuoteMeta(`SELECT count(*) FROM attachments WHERE issue_id = ?`)).
		WithArgs("bd-abc").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(int64(1)))

	list, err := ListAttachmentsInTx(ctx, tx, "bd-abc")
	if err != nil {
		t.Fatalf("ListAttachmentsInTx: %v", err)
	}
	if len(list) != 1 || list[0].ID != "att-1" || list[0].ByteSize != 1234 {
		t.Fatalf("ListAttachmentsInTx = %+v, want att-1", list)
	}
	count, err := CountAttachmentsInTx(ctx, tx, "bd-abc")
	if err != nil {
		t.Fatalf("CountAttachmentsInTx: %v", err)
	}
	if count != 1 {
		t.Fatalf("CountAttachmentsInTx = %d, want 1", count)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestResolveAttachmentInTx(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	createdAt := time.Date(2026, 6, 7, 11, 0, 0, 0, time.UTC)
	expectResolveQuery(mock, "bd-abc", "abc").
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "hash_algorithm", "content_hash", "original_filename", "mime_type", "byte_size", "storage_relpath", "created_by", "created_at"}).
			AddRow("att-1", "bd-abc", "sha256", "abc123", "body.md", "text/markdown", int64(1234), "attachments/bd-abc/abc123", "tester", createdAt))
	expectResolveQuery(mock, "bd-abc", "body.md").
		WillReturnRows(sqlmock.NewRows([]string{"id", "issue_id", "hash_algorithm", "content_hash", "original_filename", "mime_type", "byte_size", "storage_relpath", "created_by", "created_at"}).
			AddRow("att-1", "bd-abc", "sha256", "abc123", "body.md", "text/markdown", int64(1234), "attachments/bd-abc/abc123", "tester", createdAt).
			AddRow("att-2", "bd-abc", "sha256", "def456", "body.md", "text/markdown", int64(4321), "attachments/bd-abc/def456", "tester", createdAt))

	got, err := ResolveAttachmentInTx(ctx, tx, "bd-abc", "abc")
	if err != nil {
		t.Fatalf("ResolveAttachmentInTx: %v", err)
	}
	if got.ID != "att-1" {
		t.Fatalf("ResolveAttachmentInTx ID = %q, want att-1", got.ID)
	}
	_, err = ResolveAttachmentInTx(ctx, tx, "bd-abc", "body.md")
	if !errors.Is(err, storage.ErrAmbiguous) {
		t.Fatalf("ResolveAttachmentInTx ambiguous error = %v, want ErrAmbiguous", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRemoveAttachmentInTx(t *testing.T) {
	ctx := context.Background()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM attachments WHERE issue_id = ? AND id = ?`)).
		WithArgs("bd-abc", "att-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(`DELETE FROM attachments WHERE issue_id = ? AND id = ?`)).
		WithArgs("bd-abc", "missing").
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := RemoveAttachmentInTx(ctx, tx, "bd-abc", "att-1"); err != nil {
		t.Fatalf("RemoveAttachmentInTx: %v", err)
	}
	err = RemoveAttachmentInTx(ctx, tx, "bd-abc", "missing")
	if !errors.Is(err, storage.ErrNotFound) {
		t.Fatalf("RemoveAttachmentInTx error = %v, want ErrNotFound", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func expectResolveQuery(mock sqlmock.Sqlmock, issueID, selector string) *sqlmock.ExpectedQuery {
	return mock.ExpectQuery(regexp.QuoteMeta(`
		SELECT id, issue_id, hash_algorithm, content_hash, original_filename,
		       mime_type, byte_size, storage_relpath, created_by, created_at
		FROM attachments
		WHERE issue_id = ?
		  AND (id = ? OR content_hash LIKE ? OR original_filename = ?)
		ORDER BY created_at ASC, id ASC
	`)).
		WithArgs(issueID, selector, selector+"%", selector)
}
