package issueops

import (
	"context"
	"testing"
)

func TestEnsureIssueIDAvailableInTxRejectsEmptyID(t *testing.T) {
	if err := EnsureIssueIDAvailableInTx(context.Background(), nil, ""); err == nil {
		t.Fatal("EnsureIssueIDAvailableInTx() error = nil, want error")
	}
}
