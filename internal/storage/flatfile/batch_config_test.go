package flatfile

import (
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

// SQL-reference oracle: issueops.NewBatchContext propagates ReadConfigPrefix
// errors ("failed to get config: ..."), aborting the batch with the real
// cause. A corrupt config_kv.json must therefore surface the parse error,
// not collapse to prefix="" and misdiagnose every explicit ID as a
// prefix-validation failure.
func TestBatchCorruptConfigSurfacesReadError(t *testing.T) {
	s := newTestStore(t)
	if err := os.WriteFile(s.configKVPath, []byte("{truncated"), 0o644); err != nil {
		t.Fatalf("corrupt config: %v", err)
	}

	err := s.CreateIssues(ctx, []*types.Issue{{ID: "test-1", Title: "explicit id", Priority: 2}}, "tester")
	if err == nil {
		t.Fatal("batch over corrupt config_kv.json succeeded")
	}
	if strings.Contains(err.Error(), "prefix validation failed") {
		t.Errorf("corrupt config misdiagnosed as prefix mismatch: %v", err)
	}
	if !strings.Contains(err.Error(), "parse kv") {
		t.Errorf("error = %v, want the config_kv.json parse failure as root cause", err)
	}
}
