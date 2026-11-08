//go:build cgo

package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dolt"
)

// TestCheckFederationSyncStaleness_OriginTrackingRef_RealDolt runs the Sync
// Staleness check end-to-end against a real Dolt server with a real
// file:// origin remote (GH#6289). The pure evaluator tests above cover the
// aggregation; this one exercises the actual origin iteration and the real
// SyncStatus query, which is how the check shipped green while skipping
// origin entirely. One database walks the states in order:
//
//	remote never pushed     → no cached tracking ref, comparison warns
//	pushed and matching     → local-only all-clear
//	new local commit        → ahead of the cached ref, warns
//	pushed again, recorded
//	failed attempt beside
//	equal cached HEAD       → the recording still warns
//	no-push in the workspace → origin leg identified as skipped
func TestCheckFederationSyncStaleness_OriginTrackingRef_RealDolt(t *testing.T) {
	port := doctorTestServerPort()
	if port == 0 {
		t.Skip("Dolt test server not available, skipping")
	}
	t.Setenv("GT_ROOT", "")
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "0")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")

	dbName := fmt.Sprintf("doctest_syncstale_%d", time.Now().UnixNano())
	t.Cleanup(func() { dropDoctorTestDatabase(dbName, port) })

	tmpDir := t.TempDir()
	beadsDir := filepath.Join(tmpDir, ".beads")
	doltDir := filepath.Join(beadsDir, "dolt")
	if err := os.MkdirAll(doltDir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	store, err := dolt.New(ctx, &dolt.Config{
		Path:            doltDir,
		ServerHost:      "127.0.0.1",
		ServerPort:      port,
		Database:        dbName,
		CommitterName:   "test",
		CommitterEmail:  "test@example.com",
		CreateIfMissing: true,
		MaxOpenConns:    1,
	})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	db := store.DB()
	defer func() { _ = store.Close() }()
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_REMOTE('add', 'origin', ?)", "file:///tmp/"+dbName+"-origin"); err != nil {
		t.Fatalf("add origin remote: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_COMMIT('--allow-empty', '-m', 'test: first commit')"); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	cfg := &configfile.Config{
		Backend:        configfile.BackendDolt,
		DoltMode:       "server",
		DoltServerHost: "127.0.0.1",
		DoltServerPort: port,
		DoltDatabase:   dbName,
	}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(filepath.Join(beadsDir, "metadata.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Origin configured but never pushed: no cached tracking ref exists,
	// so the comparison is unavailable and the check must say so, not report
	// "Sync is up to date" the way the origin-skipping check did.
	got := CheckFederationSyncStaleness(tmpDir)
	if got.Status != StatusWarning || got.Message != "Origin sync staleness detected" {
		t.Fatalf("never pushed: got %s %q (detail %q), want warning %q",
			got.Status, got.Message, got.Detail, "Origin sync staleness detected")
	}
	if !strings.Contains(got.Detail, "comparison with cached remote refs unavailable") {
		t.Errorf("never pushed: detail %q does not name the unavailable comparison", got.Detail)
	}

	// 2. Push: the cached tracking ref appears and matches local history.
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('origin', 'main')"); err != nil {
		t.Fatalf("push to origin: %v", err)
	}
	got = CheckFederationSyncStaleness(tmpDir)
	if got.Status != StatusOK || got.Message != "No local sync staleness detected" {
		t.Fatalf("pushed and matching: got %s %q (detail %q), want OK %q",
			got.Status, got.Message, got.Detail, "No local sync staleness detected")
	}
	if !strings.Contains(got.Detail, "do not verify the remote's current contents") {
		t.Errorf("pushed and matching: detail %q must label the evidence local", got.Detail)
	}

	// 3. A new local commit: local is ahead of the cached origin ref.
	if _, err := db.ExecContext(ctx,
		"CALL DOLT_COMMIT('--allow-empty', '-m', 'test: second commit')"); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	got = CheckFederationSyncStaleness(tmpDir)
	if got.Status != StatusWarning || !strings.Contains(got.Detail, "ahead") {
		t.Fatalf("new local commit: got %s %q (detail %q), want an ahead warning",
			got.Status, got.Message, got.Detail)
	}

	// 4. Push again so the cached ref equals HEAD, then record a failed
	// attempt in push-state.json: the recording is positive evidence a push
	// did not land, so it must warn even beside equal cached refs (the
	// failure at the heart of GH#6289).
	if _, err := db.ExecContext(ctx, "CALL DOLT_PUSH('origin', 'main')"); err != nil {
		t.Fatalf("second push to origin: %v", err)
	}
	recent := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	pushStateJSON := fmt.Sprintf(
		`{"last_push":"2026-01-01T00:00:00Z","last_commit":"abc","last_attempt":%q,"last_error":"dolt push failed: timed out after 30s"}`,
		recent)
	if err := os.WriteFile(filepath.Join(beadsDir, "push-state.json"), []byte(pushStateJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	got = CheckFederationSyncStaleness(tmpDir)
	if got.Status != StatusWarning || got.Message != "Origin sync staleness detected" {
		t.Fatalf("recorded failure with equal cached HEAD: got %s %q (detail %q), want warning",
			got.Status, got.Message, got.Detail)
	}
	if !strings.Contains(got.Detail, "timed out after 30s") || !strings.Contains(got.Detail, "bd dolt push") {
		t.Errorf("recorded failure: detail %q must carry the reason and the retry guidance", got.Detail)
	}

	// 5. no-push in the inspected workspace: the origin leg is skipped and
	// identified as such, not silently green; with no peers there is nothing
	// else to warn about.
	if err := os.WriteFile(filepath.Join(beadsDir, "config.yaml"), []byte("no-push: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = CheckFederationSyncStaleness(tmpDir)
	if got.Status != StatusOK || got.Message != "Origin check skipped (no-push: true); no peer staleness detected" {
		t.Fatalf("no-push: got %s %q (detail %q), want OK %q",
			got.Status, got.Message, got.Detail, "Origin check skipped (no-push: true); no peer staleness detected")
	}
}
