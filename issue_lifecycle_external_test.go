package beads_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/telemetry"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/issueops"
)

// TestIssueLifecycleLayersHooksOutsideTelemetry pins the decorator order the
// accessor produces against the order cmd/bd assembles the chain in
// (storage_chain.go: telemetry first, hooks outermost). Each decorator layers
// itself onto the inner result, so the lifecycle comes back stacked the same
// way the store is.
func TestIssueLifecycleLayersHooksOutsideTelemetry(t *testing.T) {
	t.Setenv("BD_OTEL_STDOUT", "true")
	instrumented, ok := telemetry.WrapStorage(&dolt.DoltStore{}).(*telemetry.InstrumentedStorage)
	if !ok {
		t.Fatal("WrapStorage() did not create InstrumentedStorage")
	}

	lifecycle, err := storage.NewHookFiringStore(instrumented, nil).IssueLifecycle()
	if err != nil {
		t.Fatalf("IssueLifecycle() error = %v", err)
	}
	if got := reflect.TypeOf(lifecycle).String(); got != "*storage.hookIssueOperations" {
		t.Fatalf("outer layer = %s, want the hook wrapper", got)
	}
}

// TestIssueLifecycleExposesTypedUnsupportedError pins the typed error a backend
// returns when it cannot serve guarded mutations, reachable through the public
// alias without importing internal/storage.
func TestIssueLifecycleExposesTypedUnsupportedError(t *testing.T) {
	lifecycle, err := (*dolt.DoltStore)(nil).IssueLifecycle()
	if lifecycle != nil {
		t.Fatalf("IssueLifecycle() lifecycle = %T, want nil", lifecycle)
	}
	var unsupported *beads.ErrUnsupported
	if !errors.As(err, &unsupported) {
		t.Fatalf("IssueLifecycle() error = %v, want *beads.ErrUnsupported", err)
	}
}

func TestHookDecoratedIssueOperationsCreateFiresReverseDependencyUpdate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("hook script execution is not supported on Windows")
	}
	skipIfNoDoltServer(t)
	ctx := t.Context()
	root := t.TempDir()
	store, err := beads.Open(ctx, filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.SetConfig(ctx, "issue_prefix", "test"); err != nil {
		t.Fatal(err)
	}
	source := &types.Issue{ID: "test-hook-source", Title: "source", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
	if err := store.CreateIssue(ctx, source, "seed"); err != nil {
		t.Fatal(err)
	}
	rawStore, ok := store.(storage.DoltStorage)
	if !ok {
		t.Fatalf("opened store = %T, want DoltStorage", store)
	}
	hooksDir := filepath.Join(root, "hooks")
	if err := os.Mkdir(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "events")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s %%s\\n' \"$1\" \"$2\" >> %q\n", output)
	for _, name := range []string{hooks.HookOnCreate, hooks.HookOnUpdate} {
		if err := os.WriteFile(filepath.Join(hooksDir, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	operations, err := storage.NewHookFiringStore(rawStore, hooks.NewRunner(hooksDir)).IssueLifecycle()
	if err != nil {
		t.Fatal(err)
	}
	result, err := operations.Create(ctx, issueops.CreateRequest{
		Actor:         "writer",
		ForceIDPrefix: true,
		Issue:         &types.Issue{Title: "created", Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask},
		Dependencies:  []issueops.CreateDependency{{TargetID: source.ID, Type: types.DepRelatesTo, Reverse: true, Metadata: `{"key":"value"}`, ThreadID: "thread"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Issue.Dependencies) != 0 {
		t.Fatalf("create result dependencies = %#v, want no outgoing reverse dependency", result.Issue.Dependencies)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, _ := os.ReadFile(output)
		events := string(data)
		if strings.Contains(events, result.Issue.ID+" create") && strings.Contains(events, source.ID+" update") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatalf("read hook events: %v", err)
	}
	t.Fatalf("hook events = %q, want create for %s and update for %s", data, result.Issue.ID, source.ID)
}
