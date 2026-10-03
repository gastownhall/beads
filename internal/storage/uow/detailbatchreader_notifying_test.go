package uow

import (
	publicops "github.com/steveyegge/beads/issueops"
	"testing"
)

func TestNotifyingProviderPreservesDetailBatchReader(t *testing.T) {
	provider := &notifyingProvider{inner: &mockUnitOfWorkProvider{}}
	var source DetailBatchReaderSource = provider
	reader, err := source.DetailBatchReader()
	if err != nil || reader == nil {
		t.Fatalf("DetailBatchReader()=%T, %v", reader, err)
	}
	result, err := reader.GetBatch(t.Context(), publicops.DetailBatchRequest{})
	if err != nil || result.Items == nil || len(result.Items) != 0 {
		t.Fatalf("empty result=%+v, %v", result, err)
	}
}
