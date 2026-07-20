package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/dolt"
)

// Gateway mode: a configured credential command resolves its token into the
// connection username, marks the config as targeting a gateway server, and
// disables local auto-start (the gateway is externally managed). This is what
// makes bd init connect as the token — never as "root" — and what makes the
// store skip the SHOW/CREATE DATABASE probe (openServerConnection keys that on
// cfg.Gateway). ServerMode is set because gateway init always targets a server.
func TestApplyInitGatewayCredentialAdoptsToken(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "printf tok-init")
	doltCfg := &dolt.Config{ServerMode: true, AutoStart: true}
	if err := applyInitGatewayCredential(context.Background(), t.TempDir(), doltCfg); err != nil {
		t.Fatalf("applyInitGatewayCredential: %v", err)
	}
	if doltCfg.ServerUser != "tok-init" {
		t.Fatalf("ServerUser = %q, want tok-init (never root)", doltCfg.ServerUser)
	}
	if !doltCfg.Gateway {
		t.Fatal("Gateway must be true so the store skips SHOW/CREATE DATABASE")
	}
	if doltCfg.AutoStart {
		t.Fatal("AutoStart must be disabled in gateway mode (server is externally managed)")
	}
}

// Embedded init (no ServerMode) must never run the credential command, even when
// BEADS_DOLT_CREDENTIAL_COMMAND is ambient on the host. This is the FIX-1
// regression guard: the canonical open path gates the command on server mode
// ("a command exported in the environment must not run (or fail) an embedded
// open"), so init must too. The command here (`false`) would error if it ran;
// the helper returning nil with the config untouched proves it did not.
func TestApplyInitGatewayCredentialSkipsEmbeddedMode(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "false")
	doltCfg := &dolt.Config{AutoStart: true} // ServerMode defaults to false
	if err := applyInitGatewayCredential(context.Background(), t.TempDir(), doltCfg); err != nil {
		t.Fatalf("embedded init must not run the credential command: %v", err)
	}
	if doltCfg.Gateway || doltCfg.ServerUser != "" || !doltCfg.AutoStart {
		t.Fatalf("embedded config must be left untouched: %+v", doltCfg)
	}
}

// Server mode, but no command configured: a strict no-op. The hand-built config is
// left exactly as the caller built it.
func TestApplyInitGatewayCredentialNoopWithoutCommand(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "")
	doltCfg := &dolt.Config{ServerMode: true, AutoStart: true}
	if err := applyInitGatewayCredential(context.Background(), t.TempDir(), doltCfg); err != nil {
		t.Fatalf("applyInitGatewayCredential: %v", err)
	}
	if doltCfg.ServerUser != "" || doltCfg.Gateway || !doltCfg.AutoStart {
		t.Fatalf("config must be untouched without a command: %+v", doltCfg)
	}
}

// Fail-closed: in server mode a configured-but-failing command aborts init and
// never leaves a fallback (root) user behind.
func TestApplyInitGatewayCredentialFailsClosed(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "false")
	doltCfg := &dolt.Config{ServerMode: true, AutoStart: true}
	err := applyInitGatewayCredential(context.Background(), t.TempDir(), doltCfg)
	if err == nil {
		t.Fatal("expected an error when the credential command fails")
	}
	if doltCfg.ServerUser != "" || doltCfg.Gateway {
		t.Fatalf("config must be untouched on failure: %+v", doltCfg)
	}
}

// A caller/flag-preset --server-user wins over the credential command (the
// command is not run). Mirrors ApplyGatewayCredential's preset short-circuit.
func TestApplyInitGatewayCredentialPresetWins(t *testing.T) {
	t.Setenv("BEADS_DOLT_CREDENTIAL_COMMAND", "false")
	doltCfg := &dolt.Config{ServerMode: true, ServerUser: "preset", AutoStart: true}
	if err := applyInitGatewayCredential(context.Background(), t.TempDir(), doltCfg); err != nil {
		t.Fatalf("preset should short-circuit before running the command: %v", err)
	}
	if doltCfg.ServerUser != "preset" || doltCfg.Gateway || !doltCfg.AutoStart {
		t.Fatalf("preset user must be preserved untouched: %+v", doltCfg)
	}
}

// issue_prefix resolution.

// Gateway with no server-provisioned issue_prefix is a provisioning-contract
// violation: bd refuses to choose one for a hosted database.
func TestResolveInitIssuePrefixGatewayMissing(t *testing.T) {
	value, write, err := resolveInitIssuePrefix(true, "", "myhosteddb", "fallback", nil)
	if err == nil {
		t.Fatal("expected a provisioning-contract error for a hosted db with no issue_prefix")
	}
	if !strings.Contains(err.Error(), "provisioning-contract violation") ||
		!strings.Contains(err.Error(), "myhosteddb") {
		t.Fatalf("error should name the db and the contract violation, got: %v", err)
	}
	if write || value != "" {
		t.Fatalf("nothing must be written on violation: value=%q write=%v", value, write)
	}
}

// FIX 3: a transient read error in gateway mode is surfaced as that error, NOT as
// a false provisioning-contract violation — the prefix may well be provisioned; we
// simply failed to read it.
func TestResolveInitIssuePrefixGatewayReadError(t *testing.T) {
	readErr := errors.New("dial tcp: connection refused")
	value, write, err := resolveInitIssuePrefix(true, "", "myhosteddb", "fallback", readErr)
	if err == nil {
		t.Fatal("expected the read error to be surfaced")
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("returned error must wrap the read error, got: %v", err)
	}
	if strings.Contains(err.Error(), "provisioning-contract violation") {
		t.Fatalf("a transient read error must not be reported as a contract violation, got: %v", err)
	}
	if write || value != "" {
		t.Fatalf("nothing must be written on a read error: value=%q write=%v", value, write)
	}
}

// Gateway with an already-provisioned issue_prefix: adopt it (no write).
func TestResolveInitIssuePrefixGatewayAdopts(t *testing.T) {
	value, write, err := resolveInitIssuePrefix(true, "hq", "myhosteddb", "fallback", nil)
	if err != nil {
		t.Fatalf("adoption must not error: %v", err)
	}
	if write || value != "" {
		t.Fatalf("adoption must not write: value=%q write=%v", value, write)
	}
}

// Non-gateway with no existing prefix: set the sanitized prefix (dots -> underscores).
// This is the byte-identical legacy behavior — and a read error is ignored here,
// exactly as legacy init ignored it (the guard is gateway-only).
func TestResolveInitIssuePrefixNonGatewaySets(t *testing.T) {
	value, write, err := resolveInitIssuePrefix(false, "", "mydb", "GPUPolynomials.jl", errors.New("ignored"))
	if err != nil {
		t.Fatalf("non-gateway set must not error even with a read error: %v", err)
	}
	if !write || value != "GPUPolynomials_jl" {
		t.Fatalf("value=%q write=%v, want (GPUPolynomials_jl, true)", value, write)
	}
}

// Non-gateway with an existing prefix: no-op (do not clobber a shared db).
func TestResolveInitIssuePrefixNonGatewayExisting(t *testing.T) {
	value, write, err := resolveInitIssuePrefix(false, "existing", "mydb", "prefix", nil)
	if err != nil {
		t.Fatalf("non-gateway existing must not error: %v", err)
	}
	if write || value != "" {
		t.Fatalf("existing prefix must be preserved: value=%q write=%v", value, write)
	}
}

// project identity resolution.

// Gateway with no server-provisioned _project_id is a provisioning-contract
// violation: bd will not mint an identity for a hosted database.
func TestResolveInitProjectIDGatewayMissing(t *testing.T) {
	value, _, err := resolveInitProjectID(true, "", "", "myhosteddb", nil)
	if err == nil {
		t.Fatal("expected a provisioning-contract error for a hosted db with no _project_id")
	}
	if !strings.Contains(err.Error(), "provisioning-contract violation") ||
		!strings.Contains(err.Error(), "_project_id") ||
		!strings.Contains(err.Error(), "myhosteddb") {
		t.Fatalf("error should name the db, _project_id, and the contract, got: %v", err)
	}
	if value != "" {
		t.Fatalf("no identity must be produced: %q", value)
	}
}

// FIX 3: a transient read error in gateway mode is surfaced as that error, NOT as
// a false provisioning-contract violation.
func TestResolveInitProjectIDGatewayReadError(t *testing.T) {
	readErr := errors.New("i/o timeout")
	value, _, err := resolveInitProjectID(true, "", "", "myhosteddb", readErr)
	if err == nil {
		t.Fatal("expected the read error to be surfaced")
	}
	if !errors.Is(err, readErr) {
		t.Fatalf("returned error must wrap the read error, got: %v", err)
	}
	if strings.Contains(err.Error(), "provisioning-contract violation") {
		t.Fatalf("a transient read error must not be reported as a contract violation, got: %v", err)
	}
	if value != "" {
		t.Fatalf("no identity must be produced on a read error: %q", value)
	}
}

// Gateway with a server-provisioned _project_id and no local id yet: adopt it
// verbatim and report the change (fresh adoption).
func TestResolveInitProjectIDGatewayAdopts(t *testing.T) {
	value, changed, err := resolveInitProjectID(true, "", "proj-xyz", "myhosteddb", nil)
	if err != nil {
		t.Fatalf("adoption must not error: %v", err)
	}
	if value != "proj-xyz" {
		t.Fatalf("value = %q, want adopted proj-xyz", value)
	}
	if !changed {
		t.Fatal("adopting a server id over an empty local id must report changed")
	}
}

// The regression this PR revision fixes: gateway re-init with a local
// metadata.json that already carries a project_id. The hosted server is
// authoritative, so a server _project_id that differs from the stale local id is
// adopted (and reported changed), not silently kept — otherwise init opens with
// CreateIfMissing (skipping the identity verifier), saves the stale id as
// success, and every later normal open hard-fails PROJECT IDENTITY MISMATCH.
func TestResolveInitProjectIDGatewayReconcilesStaleLocal(t *testing.T) {
	value, changed, err := resolveInitProjectID(true, "stale-local", "server-authoritative", "myhosteddb", nil)
	if err != nil {
		t.Fatalf("reconciliation must not error: %v", err)
	}
	if value != "server-authoritative" {
		t.Fatalf("value = %q, want the server-authoritative id adopted over the stale local one", value)
	}
	if !changed {
		t.Fatal("a differing server id must report changed so the caller surfaces the reconcile")
	}
}

// Gateway re-init where the local id already matches the server: adopt it but
// report no change, so no false "reconciled" message is printed.
func TestResolveInitProjectIDGatewayLocalMatchesServer(t *testing.T) {
	value, changed, err := resolveInitProjectID(true, "proj-x", "proj-x", "myhosteddb", nil)
	if err != nil {
		t.Fatalf("matching identity must not error: %v", err)
	}
	if value != "proj-x" || changed {
		t.Fatalf("value=%q changed=%v, want (proj-x, false)", value, changed)
	}
}

// A stale local id must not mask a missing server identity in gateway mode: the
// provisioning-contract violation still fires even when localID is set, so init
// fails loudly instead of persisting an id the hosted database does not have.
func TestResolveInitProjectIDGatewayMissingWithLocalSet(t *testing.T) {
	value, changed, err := resolveInitProjectID(true, "stale-local", "", "myhosteddb", nil)
	if err == nil {
		t.Fatal("expected a provisioning-contract error even with a local id set")
	}
	if !strings.Contains(err.Error(), "provisioning-contract violation") {
		t.Fatalf("error should name the contract violation, got: %v", err)
	}
	if value != "" || changed {
		t.Fatalf("no identity must be produced on violation: value=%q changed=%v", value, changed)
	}
}

// Non-gateway with no adopted id: generate a fresh identity (legacy behavior).
// A read error is ignored here, exactly as legacy init ignored it.
func TestResolveInitProjectIDNonGatewayGenerates(t *testing.T) {
	value, _, err := resolveInitProjectID(false, "", "", "mydb", errors.New("ignored"))
	if err != nil {
		t.Fatalf("non-gateway generation must not error even with a read error: %v", err)
	}
	if value == "" {
		t.Fatal("non-gateway must generate a non-empty project id")
	}
}

// Non-gateway with no local id and an adopted id (existing shared/bootstrapped
// db): use it and report the change.
func TestResolveInitProjectIDNonGatewayAdopts(t *testing.T) {
	value, changed, err := resolveInitProjectID(false, "", "adopted-id", "mydb", nil)
	if err != nil {
		t.Fatalf("non-gateway adoption must not error: %v", err)
	}
	if value != "adopted-id" {
		t.Fatalf("value = %q, want adopted-id", value)
	}
	if !changed {
		t.Fatal("adopting over an empty local id must report changed")
	}
}

// Non-gateway keeps the legacy guard: an existing local id is never clobbered,
// even if a database id was somehow read — local wins and reports no change.
func TestResolveInitProjectIDNonGatewayKeepsLocal(t *testing.T) {
	value, changed, err := resolveInitProjectID(false, "local-id", "db-id", "mydb", nil)
	if err != nil {
		t.Fatalf("non-gateway keep must not error: %v", err)
	}
	if value != "local-id" || changed {
		t.Fatalf("value=%q changed=%v, want (local-id, false)", value, changed)
	}
}

// shouldConsultInitProjectID decides when init reads _project_id from the db.
// Gateway always consults (the fix: it must reconcile even when a local id is
// already set — a re-init or preseeded workspace). Non-gateway only consults to
// adopt from a pre-existing shared/bootstrapped database when no local id exists.
func TestShouldConsultInitProjectID(t *testing.T) {
	tests := []struct {
		name                   string
		gateway                bool
		localID                string
		database               string
		bootstrappedFromRemote bool
		want                   bool
	}{
		{"gateway fresh, no local id", true, "", "", false, true},
		{"gateway re-init with local id", true, "local", "", false, true},
		{"gateway preseeded + database", true, "local", "hosteddb", false, true},
		{"non-gateway fresh local-only", false, "", "", false, false},
		{"non-gateway --database, no local id", false, "", "mydb", false, true},
		{"non-gateway bootstrapped, no local id", false, "", "", true, true},
		{"non-gateway --database but local id set", false, "local", "mydb", false, false},
		{"non-gateway bootstrapped but local id set", false, "local", "", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldConsultInitProjectID(tt.gateway, tt.localID, tt.database, tt.bootstrappedFromRemote)
			if got != tt.want {
				t.Fatalf("shouldConsultInitProjectID(%v, %q, %q, %v) = %v, want %v",
					tt.gateway, tt.localID, tt.database, tt.bootstrappedFromRemote, got, tt.want)
			}
		})
	}
}

// The project identity is server-authoritative in gateway mode: bd must not
// write _project_id back to the (possibly read-only) hosted database. Non-gateway
// keeps writing it for cross-project verification.
func TestShouldWriteProjectIDLocally(t *testing.T) {
	if shouldWriteProjectIDLocally(true, "proj-xyz") {
		t.Fatal("gateway mode must not write _project_id back (server-authoritative)")
	}
	if !shouldWriteProjectIDLocally(false, "proj-xyz") {
		t.Fatal("non-gateway must write _project_id for cross-project verification")
	}
	if shouldWriteProjectIDLocally(false, "") {
		t.Fatal("no id means nothing to write")
	}
}

// FIX 2: gateway init must not write clone-local tracking state (bd_version,
// repo_id, clone_id, last_import_time) or issue the initial-state DOLT_COMMIT into
// the shared, server-owned database. Non-gateway keeps doing all of it —
// byte-identical legacy behavior.
func TestShouldWriteInitStateToDB(t *testing.T) {
	if shouldWriteInitStateToDB(true) {
		t.Fatal("gateway mode must not write tracking metadata or commit initial state to the shared db")
	}
	if !shouldWriteInitStateToDB(false) {
		t.Fatal("non-gateway init must write tracking metadata and commit initial state (byte-identical)")
	}
}

// Gateway init must not manage the local shared server or provision beads_global.
// Shared-server mode forces server mode on, which is exactly what makes the gateway
// credential path run, so BEADS_DOLT_SHARED_SERVER and BEADS_DOLT_CREDENTIAL_COMMAND
// can be active together. When they are, the gateway wins: init skips starting a
// local shared server, EnsureGlobalDatabase, and initGlobalDatabaseConfig — which
// otherwise rebuilds its dolt.Config without the Gateway flag and would drive
// create/schema/write operations against the authenticating gateway. Non-gateway
// shared-server behavior (flag or env) is unchanged.
func TestShouldInitSharedGlobalDB(t *testing.T) {
	tests := []struct {
		name             string
		sharedServer     bool
		sharedServerMode bool
		gateway          bool
		want             bool
	}{
		{"shared flag, no gateway", true, false, false, true},
		{"shared env mode, no gateway", false, true, false, true},
		{"shared flag + gateway skips (credential+shared-server)", true, false, true, false},
		{"shared env mode + gateway skips", false, true, true, false},
		{"neither shared nor gateway", false, false, false, false},
		{"gateway only, not shared", false, false, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldInitSharedGlobalDB(tt.sharedServer, tt.sharedServerMode, tt.gateway); got != tt.want {
				t.Fatalf("shouldInitSharedGlobalDB(%v, %v, %v) = %v, want %v",
					tt.sharedServer, tt.sharedServerMode, tt.gateway, got, tt.want)
			}
		})
	}
}

// Gateway init must not write the Dolt "origin" remote (AddRemote =
// DOLT_REMOTE('add', ...)) against the server-owned database, even when a git
// origin would normally wire one. Non-gateway remote-wiring is unchanged and
// still honored (byte-identical to shouldConfigureInitDoltRemote).
func TestShouldWriteInitDoltRemote(t *testing.T) {
	const gitOrigin = "https://example.com/repo.git"
	tests := []struct {
		name                 string
		gateway              bool
		syncURL              string
		syncFromRemote       bool
		syncURLFromConfig    bool
		syncURLFromGitOrigin bool
		localOnly            bool
		want                 bool
	}{
		{"gateway + git origin suppresses the write", true, gitOrigin, false, false, true, false, false},
		{"gateway + explicit sync remote suppresses the write", true, gitOrigin, true, false, false, false, false},
		{"non-gateway + git origin writes", false, gitOrigin, false, false, true, false, true},
		{"non-gateway + explicit sync remote writes", false, gitOrigin, true, false, false, false, true},
		{"non-gateway local-only does not write", false, gitOrigin, false, false, true, true, false},
		{"non-gateway no remote does not write", false, "", false, false, false, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldWriteInitDoltRemote(tt.gateway, tt.syncURL, tt.syncFromRemote, tt.syncURLFromConfig, tt.syncURLFromGitOrigin, tt.localOnly)
			if got != tt.want {
				t.Fatalf("shouldWriteInitDoltRemote(%v, %q, %v, %v, %v, %v) = %v, want %v",
					tt.gateway, tt.syncURL, tt.syncFromRemote, tt.syncURLFromConfig, tt.syncURLFromGitOrigin, tt.localOnly, got, tt.want)
			}
		})
	}
}
