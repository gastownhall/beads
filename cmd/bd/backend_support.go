package main

import (
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
)

// validateConfiguredBackend fails closed on metadata that selects a removed or
// unrecognized backend. beadsDir is the workspace whose metadata cfg came from;
// the rejection inspects it (read-only) so a workspace that already holds a Dolt
// database is told to fix the stale "backend" value rather than to export and
// reinitialize. Pass "" only where there is genuinely no workspace directory.
func validateConfiguredBackend(cfg *configfile.Config, beadsDir string) error {
	if cfg == nil {
		return nil
	}
	if backends.Registered(cfg.Backend) {
		return nil
	}
	switch cfg.Backend {
	case configfile.BackendPostgres, configfile.BackendMySQL, configfile.BackendSQLite:
		return configfile.RemovedBackendErrorAt(cfg.Backend, beadsDir, cfg)
	case "", configfile.BackendDolt:
		return nil
	default:
		return configfile.UnknownBackendErrorAt(cfg.Backend, beadsDir, cfg)
	}
}

// registeredBackendWorkspaceIsBeadsDir reports whether metadata selects a
// backend that has no separately discoverable local database.
func registeredBackendWorkspaceIsBeadsDir(cfg *configfile.Config) bool {
	if cfg == nil {
		return false
	}
	return backends.WorkspaceIsBeadsDir(cfg.GetBackend())
}

func requireDoltBackend(cfg *configfile.Config, beadsDir string) error {
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return err
	}
	if cfg != nil && cfg.GetBackend() != configfile.BackendDolt {
		return fmt.Errorf("not using Dolt backend (configured backend %q)", cfg.GetBackend())
	}
	return nil
}

// normalizeLoadedConfig substitutes the default config for an absent
// metadata.json (cfg == nil) so mode inference still runs: a remote host
// supplied via BEADS_DOLT_SERVER_HOST or config.yaml dolt.host (GH#3545)
// must select server mode even when no metadata.json exists — otherwise
// the CLI silently opens the embedded store against a remote-host
// configuration.
func normalizeLoadedConfig(cfg *configfile.Config) *configfile.Config {
	if cfg == nil {
		return configfile.DefaultConfig()
	}
	return cfg
}

// openStoreError frames a failed store-open for the operator. A registered
// remote backend (backends.IsRemote) is a pure network client of a bd serve
// with no local database: "failed to open database" misnames the failure, and
// the "database"-shaped diagnostics the generic path otherwise invites
// (checking .dolt/ files, a wrong data directory, ...) are the wrong advice
// for what is really a dial, auth, or protocol failure against a remote
// server. Every other backend — Dolt, or a registered backend that isn't
// Remote — keeps the original, unqualified wording. Both framings wrap err
// with %w, so errors.Is/errors.As consumers (prime's context.DeadlineExceeded
// check, for one) see the same chain as before: only a Remote backend's
// message text changes, which is what keeps this additive rather than a
// behavior change for any existing workspace.
func openStoreError(backendName string, err error) error {
	if backends.IsRemote(backendName) {
		return fmt.Errorf("failed to reach remote backend %q: %w", backendName, err)
	}
	return fmt.Errorf("failed to open database: %w", err)
}

// backendNameForErrorFraming best-effort loads metadata.json to learn which
// backend a failed open was for, so a caller that does not already have cfg
// in scope (for example direct mode's single-shot store-open) can still give
// openStoreError the name it needs. A failure to even read the config
// degrades to "" — openStoreError's default, Dolt-shaped framing — rather
// than compounding one error with another.
func backendNameForErrorFraming(beadsDir string) string {
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.GetBackend()
}

func loadDoltBackendConfig(beadsDir string) (*configfile.Config, error) {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	if err := requireDoltBackend(cfg, beadsDir); err != nil {
		return nil, err
	}
	return cfg, nil
}

// isRemoteBackendWorkspace reports whether the workspace the current command
// is running against selects a registered Remote backend (backends.IsRemote).
// It re-resolves beadsDir and the configured backend name from disk, through
// the same resolveCommandBeadsDir(dbPath) + backendNameForErrorFraming(beadsDir)
// pair openStoreError's own caller already uses, rather than threading a
// backend name through every RunE signature that needs one: PersistentPreRunE
// does not leave the backend name it resolved in any package-level variable,
// only the global dbPath it computed on the way to opening the store.
func isRemoteBackendWorkspace() bool {
	beadsDir := resolveCommandBeadsDir(dbPath)
	if beadsDir == "" {
		return false
	}
	return backends.IsRemote(backendNameForErrorFraming(beadsDir))
}

// creationActorStamp is the CreatedBy value a newly created issue should
// carry. It is getActorWithGit()'s actor for an ordinary local backend, and
// empty for a registered Remote backend: the http backend's create role
// deliberately refuses a populated CreatedBy on create (see
// backend/http/lifecycle.go's refuseUnwirableCreateIssue and the divergence
// ledger row W-CreateRequest.Issue) because the creation stamp belongs to the
// journal entry the server itself writes, not a value the caller supplies —
// re-dating history is what an import is for. Stamping it here for a remote
// workspace would make every `bd create` against that backend fail instead of
// deferring attribution to the server.
func creationActorStamp() string {
	if isRemoteBackendWorkspace() {
		return ""
	}
	return currentActor()
}
