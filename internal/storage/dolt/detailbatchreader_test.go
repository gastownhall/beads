package dolt

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/issueops"
)

func TestDetailBatchReaderValidatesBeforeOpen(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectClose()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store := &DoltStore{db: db}
	reader, err := store.DetailBatchReader()
	if err != nil {
		t.Fatal(err)
	}
	empty, err := reader.GetBatch(t.Context(), issueops.DetailBatchRequest{})
	if err != nil || empty.Items == nil || len(empty.Items) != 0 {
		t.Fatalf("empty result=%+v error=%v", empty, err)
	}
	invalid, err := reader.GetBatch(t.Context(), issueops.DetailBatchRequest{IDs: []string{" "}})
	if !errors.Is(err, issueops.ErrValidation) || invalid.Items != nil {
		t.Fatalf("blank result=%+v error=%v, want validation", invalid, err)
	}
	_, err = reader.GetBatch(t.Context(), issueops.DetailBatchRequest{IDs: []string{"valid"}})
	if err == nil {
		t.Fatal("valid request did not reach failing opener")
	}
}
