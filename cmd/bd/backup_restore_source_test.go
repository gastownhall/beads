package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/git"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
)

// The CLI layer is where the remote-restore capability was unreachable, and it
// is the layer neither store test can see: both of them start at
// RestoreDatabase, below every gate and route decision in this package. None of
// these tests needs a Dolt server, so they are the part of the evidence that
// runs on every lane.

// TestValidateBackupRestoreDirExemptsBackupURLs pins the gate that makes a
// remote restore reachable at all. validateBackupRestoreDir runs before the
// proxied/direct split in backupRestoreCmd, so statting the argument here
// rejected every URL-shaped source on BOTH topologies — with "backup directory
// not found", before any code that understands backup URLs could run.
func TestValidateBackupRestoreDirExemptsBackupURLs(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"s3://bucket/beads",
		"aws://[dynamo-table:bucket]/beads",
		"gs://bucket/beads",
		"https://doltremoteapi.dolthub.com/user/repo",
		"file:///var/backups/beads",
	} {
		if !versioncontrolops.IsBackupURL(source) {
			t.Fatalf("fixture %q is not a backup URL, so the exemption under test would not apply to it", source)
		}
		if err := validateBackupRestoreDir(source); err != nil {
			t.Errorf("validateBackupRestoreDir(%q) = %v, want nil: a backup URL is ResolveBackupSource's to check", source, err)
		}
	}
}

// TestValidateBackupRestoreDirStillRefusesMissingDirectories is the other half:
// exempting URLs must not cost the typo its short answer, and an unrecognized
// scheme is not a backup URL, so it still lands on the stat arm — carrying
// whatever credentials it was given.
func TestValidateBackupRestoreDirStillRefusesMissingDirectories(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no-such-backup")
	err := validateBackupRestoreDir(missing)
	if err == nil {
		t.Fatalf("validateBackupRestoreDir(%q) = nil, want the missing-directory refusal", missing)
	}
	if !strings.Contains(err.Error(), "backup directory not found") {
		t.Errorf("validateBackupRestoreDir(%q) = %v, want the 'backup directory not found' wording", missing, err)
	}

	// IsBackupURL is case-sensitive by design (everything downstream keys on
	// the lowercase scheme), so S3:// is stat'ed as a path and quoted back.
	const credentialed = "S3://AKIAEXAMPLE:wJalrXUtnFEMI/K7MDENG@bucket/beads"
	if versioncontrolops.IsBackupURL(credentialed) {
		t.Fatalf("fixture %q is a recognized backup URL, so it would be exempted rather than stat'ed", credentialed)
	}
	err = validateBackupRestoreDir(credentialed)
	if err == nil {
		t.Fatalf("validateBackupRestoreDir(%q) = nil, want a refusal", credentialed)
	}
	for _, secret := range []string{"AKIAEXAMPLE", "wJalrXUtnFEMI", "K7MDENG"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("validateBackupRestoreDir(%q) = %v, which echoes %q", credentialed, err, secret)
		}
	}

	// A malformed scheme is not exempted either. Redaction must fail closed
	// even though it has no :// delimiter for the normal URL path.
	const malformed = "aws:/AKIAEXAMPLE:hunter2pass@bucket/beads"
	err = validateBackupRestoreDir(malformed)
	if err == nil {
		t.Fatalf("validateBackupRestoreDir(%q) = nil, want a refusal", malformed)
	}
	if !strings.Contains(err.Error(), "aws:[redacted]") {
		t.Errorf("validateBackupRestoreDir(%q) = %v, want fail-closed redaction", malformed, err)
	}
	for _, secret := range []string{"AKIAEXAMPLE", "hunter2pass"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("validateBackupRestoreDir(%q) = %v, which echoes %q", malformed, err, secret)
		}
	}
}

// TestRunBackupRestoreProxiedResolvesThroughResolveBackupSource pins the call
// site itself: the proxied route builds its restore URL with
// ResolveBackupSource, not DirToFileURL.
//
// The discriminator is an error only the new call can produce. DirToFileURL
// absolutizes a missing directory without complaint — it never stats — so
// under the old code this input sailed past the URL build and died later, at
// the server. ResolveBackupSource stats the directory arm, so the route now
// refuses before it touches the topology, with the resolver's own wording.
// That is also what keeps this test fast and serverless: it returns before
// proxiedActiveDatabase.
func TestRunBackupRestoreProxiedResolvesThroughResolveBackupSource(t *testing.T) {
	workspace := t.TempDir()
	beadsDir := filepath.Join(workspace, ".beads")
	if err := os.MkdirAll(beadsDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg, err := json.Marshal(configfile.Config{
		Backend:  configfile.BackendDolt,
		DoltMode: configfile.DoltModeProxiedServer,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, configfile.ConfigPath(beadsDir), string(cfg))

	t.Chdir(workspace)
	beads.ResetCaches()
	git.ResetCaches()
	t.Cleanup(func() {
		beads.ResetCaches()
		git.ResetCaches()
	})

	if got := resolveProxiedTopology(beadsDir); got != ProxyTopologyManagedLocal {
		t.Fatalf("fixture topology = %q, want managed-local; the backup gate would refuse before the URL build", got)
	}

	missing := filepath.Join(workspace, "no-such-backup")
	// The file:// spelling of the same directory is the form bd itself
	// persists and prints. validateBackupRestoreDir exempts it as a backup
	// URL, so on this route the resolver's stat of the directory it names is
	// the only check before the topology is taken down for the restore.
	for _, source := range []string{missing, "file://" + missing} {
		var refusal error
		// HandleErrorRespectJSON renders the message and returns a bare exit
		// code, so the wording is on stderr, not in the error value.
		stderr := captureStderr(t, func() {
			refusal = runBackupRestoreProxied(context.Background(), source, false)
		})
		if refusal == nil {
			t.Fatalf("runBackupRestoreProxied(%q) = nil, want a refusal", source)
		}
		if !strings.Contains(stderr, "backup source does not exist") {
			t.Fatalf("runBackupRestoreProxied(%q) said %q, want ResolveBackupSource's wording: "+
				"DirToFileURL never stats, so this error can only come from the converted call", source, stderr)
		}
	}
}

// TestResolveDoltBackupURLAgreesWithIsBackupURL is the anti-drift pin for the
// scheme tables. registerBackupRemote feeds resolveDoltBackupURL the source of
// a just-completed restore, so a scheme this helper does not recognize is not
// merely unhandled — it gets absolutized, and `bd backup restore s3://b/db`
// would register and persist file://$PWD/s3:/b/db as the default backup
// remote. The biconditional is the property: a recognized backup URL passes
// through untouched, and nothing else does.
func TestResolveDoltBackupURLAgreesWithIsBackupURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"s3://bucket/beads",
		"aws://[dynamo-table:bucket]/beads",
		"aws://key:secret@bucket/beads",
		"gs://bucket/beads",
		"https://doltremoteapi.dolthub.com/user/repo",
		"http://localhost:50051/repo",
		"file:///var/backups/beads",
		"S3://bucket/beads", // unrecognized: case-sensitive by design
		"az://container/beads",
		"/var/backups/beads",
		"relative/backups",
	} {
		passedThrough := resolveDoltBackupURL(raw) == raw
		if want := versioncontrolops.IsBackupURL(raw); passedThrough != want {
			t.Errorf("resolveDoltBackupURL(%q) passed through = %v, IsBackupURL = %v: the two scheme tables have drifted",
				raw, passedThrough, want)
		}
	}
}
