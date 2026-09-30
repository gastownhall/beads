//go:build cgo

package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestPersistentPreRunLogsAndSkipsIdentityCheckForFreshEmbeddedWorkspace is
// the MINOR-criterion regression test for be-0gfcs round 2 (be-3bt2e): a
// brand-new workspace -- metadata.json present, but no embedded database on
// disk yet -- is the one previewErr case the identity check has always
// tolerated. It must still emit a debug-visible log line explaining why it
// was skipped, so the skip is diagnosable under BD_DEBUG/--verbose instead of
// looking identical to "the check silently ran and found nothing wrong."
//
// This exercises a real embedded-Dolt bootstrap (schema init on first open),
// so it is opt-in like the other embedded-dolt integration tests in this
// package: set BEADS_TEST_EMBEDDED_DOLT=1 to run it.
func TestPersistentPreRunLogsAndSkipsIdentityCheckForFreshEmbeddedWorkspace(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	// No .beads/embeddeddolt data dir: newPreviewStoreFromConfig's embedded
	// open fails with a wrapped os.ErrNotExist -- the legitimate first-run
	// condition this check must keep skipping (silently, save for the debug
	// log line under test here).
	writeMetadataConfig(t, beadsDir, configfile.DoltModeEmbedded, "identity_gate_bootstrap_test")

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_SKIP_IDENTITY_CHECK", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	t.Cleanup(func() { store = oldStore })

	// PersistentPreRunE itself calls debug.SetVerbose(verboseFlag) before
	// reaching the identity check (main.go), which would silently overwrite
	// this with the zero-value verboseFlag=false -- this test calls
	// PersistentPreRunE directly, bypassing the cobra flag parsing that
	// would otherwise populate verboseFlag from a real -v/--verbose flag.
	oldVerboseFlag := verboseFlag
	verboseFlag = true
	t.Cleanup(func() { verboseFlag = oldVerboseFlag })
	debug.SetVerbose(true)
	t.Cleanup(func() { debug.SetVerbose(false) })

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}

	// Named "import", not a distinct probe name: beads.FindDatabasePath()
	// legitimately returns "" for this exact on-disk state (metadata.json
	// present, no .beads/embeddeddolt/ yet -- see findDatabaseInBeadsDir),
	// and PersistentPreRunE's earlier, unrelated "no beads database found"
	// gate (main.go) refuses any command whose name isn't "import" or
	// "setup" before it ever reaches the identity check under test here.
	// "import" is also the realistic case: it is the one command documented
	// to auto-initialize a missing database, so it is the command a fresh
	// embedded workspace's first invocation would actually be. rootCmd
	// already has a real import command registered under the same name;
	// that's harmless here since PersistentPreRunE only ever compares
	// cmd.Name() as a string (never looks a sibling up by name), and
	// RemoveCommand below matches this probe by pointer identity, not name.
	probe := &cobra.Command{
		Use:  "import",
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	rootCmd.AddCommand(probe)
	t.Cleanup(func() { rootCmd.RemoveCommand(probe) })

	oldStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	os.Stderr = w

	err := rootCmd.PersistentPreRunE(probe, nil)

	w.Close()
	os.Stderr = oldStderr
	var captured bytes.Buffer
	io.Copy(&captured, r) //nolint:errcheck // best-effort drain of a test pipe

	if err != nil {
		t.Fatalf("PersistentPreRunE on a fresh embedded workspace: %v\nstderr:\n%s", err, captured.String())
	}

	const wantMarker = "workspace identity check: skipping"
	if !strings.Contains(captured.String(), wantMarker) {
		t.Errorf("expected a debug log line containing %q noting the identity check was skipped for a fresh workspace, got stderr:\n%s", wantMarker, captured.String())
	}
}

// TestIdentityGateRendersSchemaSkewFromForwardDriftedWorkspace pins the
// preview→skew WIRE, which TestRefuseUnverifiablePreviewOpenRendersTypedErrors
// deliberately does not: that test hands refuseUnverifiablePreviewOpen a
// hand-built *schema.SchemaSkewError, so it pins the renderer in isolation.
//
// The premise the renderer rests on is that a forward-drifted workspace now
// fails the PREVIEW open (OpenForPreviewCommand runs CheckForwardDrift) rather
// than the real one. Nothing pinned that link: a change to the preview's drift
// check, or to which factory arm the gate calls, would move skew back onto the
// generic arm -- restoring the exact regression this fix removed -- with every
// renderer subtest still green. So drive it end to end instead, from a real
// workspace whose schema cursor is ahead of this binary through
// rootCmd.PersistentPreRunE.
//
// This bootstraps a real embedded-Dolt database, so it is opt-in on the same
// tier as the sibling above: set BEADS_TEST_EMBEDDED_DOLT=1 to run it.
func TestIdentityGateRendersSchemaSkewFromForwardDriftedWorkspace(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}

	const database = "identity_gate_skew_test"
	ctx := t.Context()

	repoDir := t.TempDir()
	beadsDir := filepath.Join(repoDir, ".beads")
	writeTestConfigYAML(t, beadsDir, "")
	writeMetadataConfig(t, beadsDir, configfile.DoltModeEmbedded, database)

	// A real bootstrap, not a fake: Open creates the data dir and runs
	// initSchema, which is what leaves the schema_migrations cursor the drift
	// check reads. Closed again so the gate's own preview open is the only
	// holder.
	bootstrap, err := embeddeddolt.Open(ctx, beadsDir, database, "main")
	if err != nil {
		t.Fatalf("bootstrap embedded workspace: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("close bootstrap store: %v", err)
	}

	// Forward-drift it. CurrentVersion is MAX(version) over schema_migrations,
	// so a single row above the binary's latest reproduces the recurring
	// stale-binary class (#4135/#4137) that made this arm's UX matter.
	ahead := schema.LatestVersion() + 1
	withEmbeddedMigrateSQL(t, beadsDir, database, func(db *sql.DB) error {
		_, err := db.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, content_hash) VALUES (?, ?)",
			ahead, strings.Repeat("f", 64))
		return err
	})

	t.Chdir(repoDir)
	t.Setenv("BEADS_DIR", beadsDir)
	t.Setenv("BEADS_DOLT_SHARED_SERVER", "")
	t.Setenv("BEADS_DOLT_SERVER_DATABASE", "")
	t.Setenv("BEADS_DOLT_SERVER_PORT", "")
	t.Setenv("BEADS_DOLT_AUTO_START", "0")
	t.Setenv("BEADS_SKIP_IDENTITY_CHECK", "")
	// The skew check itself has an escape hatch; an ambient one would turn the
	// refusal under test into a warning and pass vacuously.
	t.Setenv("BD_IGNORE_SCHEMA_SKEW", "")

	config.ResetForTesting()
	t.Cleanup(config.ResetForTesting)
	savePersistentPreRunState(t)

	oldStore := store
	// Zero it: the closing assertion is that the gate refused before opening
	// the real store, which a leftover global would satisfy vacuously.
	store = nil
	t.Cleanup(func() { store = oldStore })

	oldJSON := jsonOutput
	jsonOutput = false
	t.Cleanup(func() { jsonOutput = oldJSON })

	if rootCmd.PersistentPreRunE == nil {
		t.Fatal("rootCmd.PersistentPreRunE must be set")
	}

	probe := &cobra.Command{
		Use:  "identity-gate-skew-probe",
		RunE: func(*cobra.Command, []string) error { return nil },
	}
	rootCmd.AddCommand(probe)
	t.Cleanup(func() { rootCmd.RemoveCommand(probe) })

	var preRunErr error
	out := captureStderr(t, func() { preRunErr = rootCmd.PersistentPreRunE(probe, nil) })

	if preRunErr == nil {
		t.Fatalf("PersistentPreRunE succeeded against a workspace %d migrations ahead of this binary; the gate must refuse\nstderr:\n%s", ahead-schema.LatestVersion(), out)
	}

	// The actionable block SchemaSkewError.UserMessage() owns. Losing this to a
	// generic wrapper was the regression.
	if !strings.Contains(out, "Your bd binary is stale") {
		t.Errorf("stderr lacks the skew rebuild guidance, so the preview's typed error was not rendered as the real-open arm renders it:\n%s", out)
	}
	// Proves the drift came from the real workspace cursor, not a stray
	// SchemaSkewError manufactured somewhere else on the path.
	wantVersions := fmt.Sprintf("database is at v%d, binary knows up to v%d", ahead, schema.LatestVersion())
	if !strings.Contains(out, wantVersions) {
		t.Errorf("stderr = %q, want it to report %q", out, wantVersions)
	}
	if strings.Contains(out, "could not verify workspace identity") {
		t.Errorf("schema skew from the preview open was rendered as the gate's generic identity wrapper:\n%s", out)
	}
	if strings.Contains(out, "BEADS_SKIP_IDENTITY_CHECK") {
		t.Errorf("refusal advertised BEADS_SKIP_IDENTITY_CHECK, which cannot help here: skipping the gate leaves the real open failing on the identical skew:\n%s", out)
	}
	if store != nil {
		t.Error("PersistentPreRunE must not have opened the real store after refusing on a forward-drifted preview")
	}
}
