package issueops

import (
	"context"
	"testing"
)

func TestHydrateIssueOperationResultRequiresIssue(t *testing.T) {
	if _, err := HydrateIssueOperationResult(context.Background(), nil, "missing", false); err == nil {
		t.Fatal("HydrateIssueOperationResult() error = nil, want error")
	}
}
