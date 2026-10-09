package uow

import (
	"errors"
	"testing"

	"github.com/steveyegge/beads/issueops"
)

func TestDetailBatchReaderValidatesBeforeOpen(t *testing.T) {
	if reader, err := (*doltSQLProvider)(nil).DetailBatchReader(); reader != nil || err == nil {
		t.Fatalf("nil provider reader=%T error=%v", reader, err)
	}
	provider := &mockUnitOfWorkProvider{newUOWErr: errors.New("opener failed")}
	reader, err := NewDetailBatchReader(provider)
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
	if provider.newUOWCalls != 1 {
		t.Fatalf("opener calls=%d, want 1 (valid only)", provider.newUOWCalls)
	}
}
