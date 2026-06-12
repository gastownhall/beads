package schema

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestEnsureMetadataIndexes_CreatesWhenMissing(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("information_schema.columns").
		WithArgs("issues", "bd_md_alias").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("ALTER TABLE issues ADD COLUMN bd_md_alias").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery("information_schema.statistics").
		WithArgs("issues", "idx_bd_md_alias").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(0))
	mock.ExpectExec("CREATE INDEX idx_bd_md_alias ON issues").
		WillReturnResult(sqlmock.NewResult(0, 0))

	specs := []MetadataIndexSpec{{Column: "bd_md_alias", Index: "idx_bd_md_alias", JSONPath: "$.alias"}}
	if err := EnsureMetadataIndexes(context.Background(), db, specs); err != nil {
		t.Fatalf("EnsureMetadataIndexes: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestEnsureMetadataIndexes_SkipsWhenPresent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectQuery("information_schema.columns").
		WithArgs("issues", "bd_md_alias").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	mock.ExpectQuery("information_schema.statistics").
		WithArgs("issues", "idx_bd_md_alias").
		WillReturnRows(sqlmock.NewRows([]string{"n"}).AddRow(1))
	// No ALTER/CREATE expected — column and index already exist.

	specs := []MetadataIndexSpec{{Column: "bd_md_alias", Index: "idx_bd_md_alias", JSONPath: "$.alias"}}
	if err := EnsureMetadataIndexes(context.Background(), db, specs); err != nil {
		t.Fatalf("EnsureMetadataIndexes: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
