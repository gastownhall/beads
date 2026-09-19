package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"golang.org/x/term"
)

var resolveBootstrapAuthoritativeMetadata = fix.ResolveAuthoritativeServerMetadata

type bootstrapServerProbeConfig struct {
	host     string
	port     int
	user     string
	pass     string
	database string
	tls      bool
}

type bootstrapServerDBCheck struct {
	Exists    bool
	Reachable bool
	Err       error
}

// bootstrapRetryDelay is the sleep function used between server-DB-not-found
// retries. Injectable for testing.
var bootstrapRetryDelay = func(d time.Duration) { time.Sleep(d) }

// probeGitRemoteDoltData reports whether a git remote carries Dolt data
// (refs/dolt/data). Injectable for testing; the production value is a
// 10s-bounded `git ls-remote` with credential prompts suppressed. A non-nil
// error means UNKNOWN, not "no data".
var probeGitRemoteDoltData = gitRemoteHasDoltDataRefStatus

var checkBootstrapServerDB = func(probeCfg bootstrapServerProbeConfig) bootstrapServerDBCheck {
	host := probeCfg.host
	port := probeCfg.port
	dbName := probeCfg.database
	dsn := doltutil.ServerDSN{
		Host:     host,
		Port:     port,
		User:     probeCfg.user,
		Password: probeCfg.pass,
		TLS:      probeCfg.tls,
	}.String()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return bootstrapServerDBCheck{Reachable: false, Err: err}
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return bootstrapServerDBCheck{Reachable: false, Err: err}
	}

	rows, err := db.QueryContext(ctx, "SHOW DATABASES")
	if err != nil {
		return bootstrapServerDBCheck{Reachable: true, Err: err}
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return bootstrapServerDBCheck{Reachable: true, Err: err}
		}
		if name == dbName {
			return bootstrapServerDBCheck{Exists: true, Reachable: true}
		}
	}
	if err := rows.Err(); err != nil {
		return bootstrapServerDBCheck{Reachable: true, Err: err}
	}

	return bootstrapServerDBCheck{Exists: false, Reachable: true}
}

var bootstrapCmd = &cobra.Command{
	Use:     "bootstrap",
	GroupID: "setup",
	Short:   "Non-destructive database setup for fresh clones and recovery",
	Long: `Bootstrap sets up the beads database without destroying existing data.
Unlike 'bd init --force', bootstrap will never delete existing issues.

Bootstrap auto-detects the right action:
  • If sync.remote is configured: clones from the remote
  • If git origin has Dolt data (refs/dolt/data): clones from git and wires origin for future push/pull
  • If .beads/backup/*.jsonl exists: restores from backup
  • If .beads/issues.jsonl exists: imports from git-tracked JSONL
  • If no database exists: creates a fresh one
  • If database already exists: validates and reports status

If sync.remote points at a git repository, bootstrap verifies refs/dolt/data
before cloning. Bootstrap exits non-zero when it cannot set up a database.

This is the recommended command for:
  • Setting up beads on a fresh clone
  • Recovering after moving to a new machine
  • Repairing a broken database configuration

Non-interactive mode (--non-interactive, --yes/-y, or BD_NON_INTERACTIVE=1):
  Skips the confirmation prompt before executing the bootstrap plan.
  Also auto-detected when stdin is not a terminal or CI=true is set.

Examples:
  bd bootstrap              # Auto-detect and set up
  bd bootstrap --dry-run    # Show what would be done
  bd bootstrap --json       # Output plan as JSON
  bd bootstrap --yes        # Skip confirmation prompt
`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("bootstrap")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		dryRun, _ := cmd.Flags().GetBool("dry-run")
		yesFlag, _ := cmd.Flags().GetBool("yes")
		nonInteractiveFlag, _ := cmd.Flags().GetBool("non-interactive")

		// Resolve non-interactive mode: flag > env var > CI env > terminal detection.
		nonInteractive := isNonInteractiveBootstrap(yesFlag || nonInteractiveFlag)

		// dc-6jaq: "bootstrap" is in noDbCommands, so PersistentPreRunE
		// returns at the skip-store early return before its freeze gate, and
		// bootstrap.go calls neither CheckReadonly nor the gate itself — yet
		// it writes, and the first of those writes (the metadata repair below)
		// lands before its own confirmation prompt and before it takes any
		// workspace gate. --dry-run is exempt because it genuinely does not
		// write; every other invocation is refused while a marker is active.
		if !dryRun {
			if err := migrationFreezeGateFor(cmd, "bootstrap"); err != nil {
				return err
			}
		}

		// Find beads directory
		beadsDir := beads.FindBeadsDir()
		if beadsDir == "" {
			// No .beads directory exists yet. Before giving up, probe the
			// git remote for Dolt data stored in git (refs/dolt/data). This
			// is the "fresh second clone" case: clone1 pushed Beads state
			// to a git remote, and clone2 needs to bootstrap from it.
			// Only applies to git remotes — Dolt-native remotes (DoltHub,
			// S3, etc.) should be configured via sync.remote. (GH#2792)
			//
			// If found, synthesize the theoretical .beads path and fall
			// through to the normal detectBootstrapAction + executeBootstrapPlan
			// flow. Actual directory creation is deferred to executeSyncAction
			// to preserve --dry-run semantics.
			if isGitRepo() && !isBareGitRepo() {
				if originURL, err := gitOriginGetURL(); err == nil && originURL != "" {
					if gitOriginHasDoltDataRef() {
						if fallbackDir := beads.GetWorktreeFallbackBeadsDir(); fallbackDir != "" {
							beadsDir = fallbackDir
						} else {
							cwd, err := os.Getwd()
							if err != nil {
								return HandleError("failed to get working directory: %v", err)
							}
							beadsDir = filepath.Join(cwd, ".beads")
						}
					}
				}
			}
		}

		if beadsDir == "" {
			if jsonOutput {
				if err := outputJSON(noWorkspaceBootstrapPayload()); err != nil {
					return err
				}
				return SilentExit()
			}
			fmt.Fprintf(os.Stderr, "Hint: %s\n", diagHint())
			fmt.Fprintf(os.Stderr, "Bootstrap is for existing projects that need database setup.\n")
			return HandleError("%s", activeWorkspaceNotFoundMessage())
		}

		if err := guardLegacyUpgradeWorkspace(beadsDir); err != nil {
			return HandleError("%v", err)
		}

		// Load config from .beads/metadata.json. When the beadsDir was
		// synthesized (fresh clone or rig with no local .beads), the file
		// won't exist. In that case, walk up parent directories to find a
		// workspace-level metadata.json that contains the correct database
		// name (e.g. dolt_database). Without this, server-mode rigs get the
		// default name "beads" instead of their configured name. (GH#3029)
		cfg, err := configfile.Load(beadsDir)
		if err != nil {
			return HandleError("failed to load %s: %v; no storage database was opened or modified; fix or restore metadata.json and retry", configfile.ConfigPath(beadsDir), err)
		}
		if cfg == nil {
			cfg, err = findParentConfig(beadsDir)
			if err != nil {
				return HandleError("failed to load ancestor storage metadata: %v; no storage database was opened or modified; fix or restore metadata.json and retry", err)
			}
		}
		if cfg == nil {
			cfg = configfile.DefaultConfig()
		}
		if err := requireBootstrapDoltBackend(cfg, beadsDir); err != nil {
			return HandleError("%v", err)
		}

		resolvedCfg, repairMsg, err := applyBootstrapMetadataRepair(beadsDir, cfg, !dryRun)
		if err != nil {
			return HandleError("failed to reconcile shared-server metadata: %v", err)
		}
		if resolvedCfg != nil {
			cfg = resolvedCfg
		}

		// Determine action based on state
		plan := detectBootstrapAction(beadsDir, cfg)

		if jsonOutput {
			if err := outputJSON(plan); err != nil {
				return err
			}
			if plan.Action == "none" || dryRun {
				return bootstrapPlanOutcome(plan, dryRun)
			}
		} else {
			if repairMsg != "" {
				fmt.Fprintf(os.Stderr, "Bootstrap metadata repair: %s\n", repairMsg)
			}
			printBootstrapPlan(plan)
			if plan.Action == "none" || dryRun {
				return bootstrapPlanOutcome(plan, dryRun)
			}
		}

		if err := executeBootstrapPlan(plan, cfg, nonInteractive); err != nil {
			return HandleError("Bootstrap failed: %v", err)
		}
		return nil
	},
}

func applyBootstrapMetadataRepair(beadsDir string, cfg *configfile.Config, apply bool) (*configfile.Config, string, error) {
	if beadsDir == "" {
		return cfg, "", nil
	}
	if _, err := os.Stat(beadsDir); err != nil {
		return cfg, "", nil
	}
	resolved, msg, err := resolveBootstrapAuthoritativeMetadata(filepath.Dir(beadsDir), apply)
	if err != nil {
		return nil, "", err
	}
	if resolved == nil {
		return cfg, msg, nil
	}
	return resolved, msg, nil
}

// BootstrapPlan describes what bootstrap will do.
type BootstrapPlan struct {
	Action   string `json:"action"` // "sync", "restore", "jsonl-import", "init", "none"
	Reason   string `json:"reason"` // Human-readable explanation
	BeadsDir string `json:"beads_dir"`
	Database string `json:"database"`
	// SyncRemote is the remote this workspace syncs with. For Action=="sync"
	// it is the clone source; for the provisioning actions it is the remote to
	// register as Dolt "origin" so the first `bd dolt push` has somewhere to
	// go. Always the routed (doltRemoteURL) form for forge URLs, and empty on
	// a Blocked plan — nothing may be cloned from or pushed to a remote
	// bootstrap could not verify.
	//
	// This is an operational URL, so it keeps whatever credentials the user
	// configured. Run it through redactRemoteURL before printing it or putting
	// it anywhere a human will read. Reason and BlockedRemote are already
	// display-safe.
	SyncRemote string `json:"sync_remote,omitempty"`
	BackupDir  string `json:"backup_dir,omitempty"`
	JSONLFile  string `json:"jsonl_file,omitempty"`
	// Blocked marks a plan that took no action and found no database. It is
	// derived in detectBootstrapAction from Action+HasExisting, never set by a
	// detection branch, and it is the single discriminator the exit code and
	// the printed output both switch on. Additive on purpose: JSON consumers
	// switch on "action", so a new action value would break them, while
	// "none" + "blocked" + a non-zero exit code is backwards compatible.
	Blocked bool `json:"blocked,omitempty"`
	// BlockedRemote is the credential-free remote URL a Blocked plan could not
	// verify, carried only so the diagnostic can name it.
	BlockedRemote string `json:"blocked_remote,omitempty"`
	HasExisting   bool   `json:"has_existing"`
}

// MarshalJSON renders the plan for `bd bootstrap --json` (and `--dry-run --json`)
// with the credential-bearing SyncRemote redacted, mirroring the human path
// (printBootstrapPlan runs it through redactRemoteURL). SyncRemote keeps whatever
// credentials the user configured — see the field comment — and `--json` writes
// to stdout, which CI logs and agent transcripts capture, so it must not be
// emitted verbatim. Redaction is on a copy; executeBootstrapPlan still consumes
// the raw plan.SyncRemote for the clone. Reason and BlockedRemote are already
// built display-safe.
func (p BootstrapPlan) MarshalJSON() ([]byte, error) {
	// The local type sheds BootstrapPlan's method set so json.Marshal does not
	// recurse back into this MarshalJSON.
	type bootstrapPlanJSON BootstrapPlan
	safe := bootstrapPlanJSON(p)
	safe.SyncRemote = redactRemoteURL(p.SyncRemote)
	return json.Marshal(safe)
}

// bootstrapPlanOutcome maps a printed plan to the command's exit status.
// Action=="none" was two outcomes wearing one name — "a database already
// exists, nothing to do" and "bootstrap declined and left you with no
// database" — and both returned nil, so a rejected sync.remote printed a
// success tick and exited 0 with no database (#5743). Blocked is the
// discriminator.
//
// --dry-run always succeeds. bd doctor documents `bd bootstrap --dry-run` as
// the safe inspection step, and a preview that aborts a `set -e` provisioning
// script is a worse contract than a preview that merely reports. The blocked
// state is still printed (and still in the JSON), so the preview does predict
// what a real run would refuse to do.
func bootstrapPlanOutcome(plan BootstrapPlan, dryRun bool) error {
	if dryRun || !plan.Blocked {
		return nil
	}
	// The reason and hints are already on stderr, or the plan JSON (carrying
	// "blocked": true) is already on stdout.
	return SilentExit()
}

func noWorkspaceBootstrapPayload() map[string]interface{} {
	return map[string]interface{}{
		"action":     "none",
		"reason":     activeWorkspaceNotFoundError(),
		"suggestion": diagHint(),
	}
}

func requireBootstrapDoltBackend(cfg *configfile.Config, beadsDir string) error {
	if err := validateConfiguredBackend(cfg, beadsDir); err != nil {
		return err
	}
	// A registered extension backend passes validateConfiguredBackend so its
	// existing workspaces can be opened, but every bd bootstrap action
	// (sync, restore, jsonl-import, init) provisions or imports Dolt. Registered
	// backends are open/discover-only, so reject them here — before
	// detectBootstrapAction or executeBootstrapPlan — mirroring bd init's
	// fail-closed gate. Downstream registrants supply their own workspace
	// provisioning path.
	if cfg != nil && cfg.GetBackend() != configfile.BackendDolt {
		return fmt.Errorf("backend %q cannot be bootstrapped by bd bootstrap; it can only open an existing workspace (bd bootstrap provisions %q, the default)", cfg.GetBackend(), configfile.BackendDolt)
	}
	return nil
}

// detectBootstrapAction builds the plan and then derives its outcome flags in
// exactly one place. Blocked is never hand-set by a detection branch: it is a
// function of the finished plan ("took no action AND found no database"), so no
// branch can leave a stale flag behind for a later branch to inherit.
func detectBootstrapAction(beadsDir string, cfg *configfile.Config) BootstrapPlan {
	plan := detectBootstrapPlan(beadsDir, cfg)
	plan.Blocked = plan.Action == "none" && !plan.HasExisting
	if !plan.Blocked {
		plan.BlockedRemote = ""
	}
	return plan
}

func detectBootstrapPlan(beadsDir string, cfg *configfile.Config) BootstrapPlan {
	plan := BootstrapPlan{
		BeadsDir: beadsDir,
		Database: cfg.GetDoltDatabase(),
	}

	// When bootstrap synthesized a fallback beadsDir for a fresh clone or
	// worktree recovery, the path may not exist yet. In that case we must let
	// sync.remote / refs/dolt/data detection run before treating an existing
	// shared-server database as "nothing to do", otherwise an unrelated default
	// "beads" database can mask the real recovery path.
	info, statErr := os.Stat(beadsDir)
	beadsDirExists := statErr == nil && info.IsDir()

	// Check for existing database (path differs between server and embedded mode).
	// Determine server/shared-server mode from the target workspace itself
	// (metadata.json, env vars, and the target config.yaml when present) rather
	// than unrelated global config loaded from the caller's current repo.
	isSharedServer := bootstrapSharedServerMode(beadsDir)
	isServer := cfg.IsDoltServerMode() || isSharedServer

	// Prefer an existing local database over re-clone (GH#5037). Previously
	// sync.remote returned Action=sync unconditionally, so a second
	// `bd bootstrap` on an already-cloned workspace ran DOLT_CLONE into the
	// existing dir and failed with Error 1007 (database exists) — contradicting
	// help text ("If database already exists: validates and reports status")
	// and the multi-clone upgrade guide. If the local beadsDir does not exist
	// yet, still prefer sync recovery first for Action=="none" so a default
	// shared-server "beads" DB from another project cannot mask a real clone.
	// Held aside rather than merged into plan: a "none" verdict here is only a
	// fallback for when nothing else matches, and copying it into plan leaked
	// its HasExisting/Blocked/Reason into every branch below — including the
	// probe-error branch, which then inherited HasExisting=true and printed the
	// #5743 success tick again.
	var deferredExistingPlan *BootstrapPlan
	if dbAction, ok := existingBootstrapDBPlan(beadsDir, cfg, isServer, isSharedServer); ok {
		if beadsDirExists || dbAction.Action != "none" {
			return dbAction
		}
		deferredExistingPlan = &dbAction
	}

	// A configured sync.remote (primary) or sync.git-remote (deprecated
	// fallback) is authoritative: try it first and, when it cannot be cloned,
	// remember why so an unverifiable remote can become fatal only after local
	// recovery is exhausted. With no sync.remote, auto-detect Dolt data on git
	// origin instead.
	syncRemote := resolveSyncRemote()
	unusableRemoteReason, unusableRemoteURL := "", ""
	if syncRemote != "" {
		done, reason, url := planConfiguredSyncRemote(&plan, syncRemote)
		if done {
			return plan
		}
		// Cloning is off the table; purely local recovery is not.
		unusableRemoteReason, unusableRemoteURL = reason, url
	} else if originPlan, ok := gitOriginSyncPlan(plan); ok {
		return originPlan
	}

	// On-disk recovery sources: a non-empty backup JSONL, then a git-tracked
	// issues.jsonl. When a forge sync.remote had no Dolt data yet, plan already
	// carries its SyncRemote so the restored/imported workspace stays wired to it.
	if recoveryPlan, ok := localFileRecoveryPlan(plan, beadsDir); ok {
		return recoveryPlan
	}

	if deferredExistingPlan != nil {
		return *deferredExistingPlan
	}

	// Only now, with every local recovery source exhausted, does an
	// unverifiable remote become fatal. Restoring a local backup touches no
	// remote, so an offline laptop or a credential-less CI runner must not be
	// denied the recovery that the same machine would get if the probe had
	// merely answered "no data".
	if unusableRemoteReason != "" {
		plan.Action = "none"
		plan.Reason = unusableRemoteReason
		plan.BlockedRemote = unusableRemoteURL
		return plan
	}

	// Fresh setup
	plan.Action = "init"
	plan.Reason = "No existing database, remote, or backup — will create fresh database"
	return plan
}

// planConfiguredSyncRemote resolves a configured sync.remote into plan. done==true
// means the plan is finished — a clone plan for a trusted remote or a forge repo
// the probe confirmed carries Dolt data — and the caller returns plan as-is.
// done==false means keep looking: either the forge repo has no Dolt data yet
// (plan.SyncRemote is still recorded so a fresh workspace gets wired to it) or the
// remote could not be verified, in which case the returned reason/url describe the
// unusable remote so it can become fatal only after local recovery is exhausted.
func planConfiguredSyncRemote(plan *BootstrapPlan, syncRemote string) (done bool, unusableReason, unusableURL string) {
	if !isGitCodeRepoURL(syncRemote) {
		// User-provided sync.remote — trust the URL format as-is.
		// normalizeRemoteURL would convert http:// to git+http://, breaking Dolt
		// remotesapi endpoints (GH#3339). That concern cannot apply here: a
		// remotesapi endpoint never classifies as a git code-repo URL.
		plan.SyncRemote = syncRemote
		plan.Action = "sync"
		plan.Reason = "sync.remote configured — will clone from " + redactRemoteURL(syncRemote)
		return true, "", ""
	}

	// A git-forge URL is a *supported* Dolt remote: `bd init` and `bd dolt
	// remote add` both persist one, and `bd dolt push` stores the database under
	// refs/dolt/data on it. What must never happen is cloning it blind, or in a
	// form that reaches Dolt's remotesapi client. doltRemoteURL owns that routing
	// rule; probe first, and clone only what the probe actually confirmed
	// (#5743, #5663).
	routed := doltRemoteURL(syncRemote)
	hasData, probeErr := probeGitRemoteDoltData(routed)
	switch {
	case probeErr != nil:
		// UNKNOWN, not "no data" — never clone something we could not verify
		// (init's resolveRemoteHasDoltDataProbe convention).
		reason := fmt.Sprintf("could not verify refs/dolt/data on sync.remote %q: %v",
			redactRemoteURL(syncRemote), probeErr)
		// Said here as well as in the final verdict because a local backup may
		// well rescue this run, and then this is the only place the user learns
		// the remote could not be reached.
		fmt.Fprintf(os.Stderr, "note: not cloning — %s; checking local recovery sources\n", reason)
		return false, reason, redactRemoteURL(routed)
	case hasData:
		plan.SyncRemote = routed
		plan.Action = "sync"
		plan.Reason = "sync.remote git repo has Dolt data (refs/dolt/data) — will clone from " + redactRemoteURL(routed)
		return true, "", ""
	default:
		// Definitively no Dolt data: the repo was wired before the first `bd
		// dolt push`. Say so and keep looking, the same way bd init falls back
		// to a fresh local database. Carry the URL so the workspace we create
		// still gets wired to it.
		plan.SyncRemote = routed
		fmt.Fprintf(os.Stderr, "note: sync.remote %q has no Dolt data yet (refs/dolt/data absent); checking other recovery sources\n",
			redactRemoteURL(syncRemote))
		return false, "", ""
	}
}

// gitOriginSyncPlan returns a clone-from-git-origin plan when git origin carries
// Dolt data (refs/dolt/data), or ok==false otherwise. Only git remotes qualify —
// Dolt-native remotes (DoltHub, S3, etc.) must be configured via sync.remote.
//
// The caller invokes this only when no sync.remote is configured. sync.remote is
// the authoritative remote for this workspace, so hydrating from whatever git
// origin happens to be (a fork, an old experiment, the code repo under the
// dedicated-data-repo pattern) is not recovery — and finalizeSyncedBootstrap would
// then rewrite the team's committed sync.remote to point at it. Skipping it also
// spares the second identical ls-remote in the common case where sync.remote IS
// the git origin, which is what bd init persists.
func gitOriginSyncPlan(plan BootstrapPlan) (BootstrapPlan, bool) {
	if !isGitRepo() || isBareGitRepo() {
		return BootstrapPlan{}, false
	}
	originURL, err := gitOriginGetURL()
	if err != nil || originURL == "" {
		return BootstrapPlan{}, false
	}
	if !gitOriginHasDoltDataRef() {
		return BootstrapPlan{}, false
	}
	plan.SyncRemote = normalizeRemoteURL(originURL)
	plan.Action = "sync"
	plan.Reason = "Found Dolt data on git origin (refs/dolt/data) — will clone from " + redactRemoteURL(originURL)
	return plan, true
}

// localFileRecoveryPlan returns a restore or jsonl-import plan built from on-disk
// recovery sources under beadsDir — a non-empty backup/issues.jsonl (restore),
// else a git-tracked issues.jsonl (the portable export format) — or ok==false
// when neither is present. plan is taken by value so its already-populated fields
// (e.g. a forge SyncRemote with no Dolt data yet) ride into the returned plan.
func localFileRecoveryPlan(plan BootstrapPlan, beadsDir string) (BootstrapPlan, bool) {
	// Backup JSONL must be non-empty to be useful.
	backupDir := filepath.Join(beadsDir, "backup")
	issuesFile := filepath.Join(backupDir, "issues.jsonl")
	if info, err := os.Stat(issuesFile); err == nil && info.Size() > 0 {
		plan.BackupDir = backupDir
		plan.Action = "restore"
		plan.Reason = "Backup files found — will restore from " + backupDir
		return plan, true
	}

	gitJSONL := filepath.Join(beadsDir, "issues.jsonl")
	if _, err := os.Stat(gitJSONL); err == nil {
		plan.JSONLFile = gitJSONL
		plan.Action = "jsonl-import"
		plan.Reason = "Git-tracked issues.jsonl found — will import from " + gitJSONL
		return plan, true
	}

	return BootstrapPlan{}, false
}

func existingBootstrapDBPlan(beadsDir string, cfg *configfile.Config, isServer, isSharedServer bool) (BootstrapPlan, bool) {
	plan := BootstrapPlan{
		BeadsDir: beadsDir,
		Database: cfg.GetDoltDatabase(),
	}

	var dbPath string
	if isServer {
		dbPath = bootstrapServerDoltDir(beadsDir, cfg, isSharedServer)
	} else {
		dbPath = filepath.Join(beadsDir, "embeddeddolt")
	}
	if info, err := os.Stat(dbPath); err != nil || !info.IsDir() {
		return BootstrapPlan{}, false
	}

	entries, _ := os.ReadDir(dbPath)
	if len(entries) == 0 {
		return BootstrapPlan{}, false
	}

	if isServer {
		probeCfg := bootstrapServerProbeConfig{
			host:     cfg.GetDoltServerHost(),
			port:     bootstrapServerPort(beadsDir, cfg, isSharedServer),
			user:     cfg.GetDoltServerUser(),
			pass:     cfg.GetDoltServerPassword(),
			database: cfg.GetDoltDatabase(),
			tls:      cfg.GetDoltServerTLS(),
		}
		// When the server is reachable but the DB appears absent, retry with
		// exponential backoff before concluding the DB is genuinely missing.
		// A managed Dolt restart completes in <30 s; three retries over 70 s
		// cover all observed restart windows.
		retryDelays := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second}
		var result bootstrapServerDBCheck
		for attempt := 0; ; attempt++ {
			result = checkBootstrapServerDB(probeCfg)
			if result.Err != nil || result.Exists || !result.Reachable {
				break
			}
			if attempt >= len(retryDelays) {
				break
			}
			fmt.Fprintf(os.Stderr, "Database %s not found on reachable server (attempt %d/%d), retrying in %v (possible transient restart)\n",
				cfg.GetDoltDatabase(), attempt+1, len(retryDelays), retryDelays[attempt])
			bootstrapRetryDelay(retryDelays[attempt])
		}
		if result.Err != nil {
			// Same "declined, and no database was confirmed" class as a
			// failed remote probe: report it and exit non-zero rather than
			// printing a success tick (#5743). Blocked is not hand-set here —
			// detectBootstrapAction derives it from Action=="none" && !HasExisting
			// (HasExisting is false on this branch), keeping one owner for the flag.
			plan.Action = "none"
			plan.Reason = fmt.Sprintf("Could not verify existing server database %s: %v", cfg.GetDoltDatabase(), result.Err)
			return plan, true
		}
		if result.Exists {
			plan.HasExisting = true
			plan.Action = "none"
			plan.Reason = fmt.Sprintf("Database %s already exists on server at %s:%d", probeCfg.database, probeCfg.host, probeCfg.port)
			return plan, true
		}
		return BootstrapPlan{}, false
	}

	plan.HasExisting = true
	plan.Action = "none"
	plan.Reason = "Database already exists at " + dbPath
	return plan, true
}

func bootstrapSharedServerMode(beadsDir string) bool {
	if v := os.Getenv("BEADS_DOLT_SHARED_SERVER"); v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	return strings.EqualFold(config.GetStringFromDir(beadsDir, "dolt.shared-server"), "true")
}

func bootstrapServerDoltDir(beadsDir string, cfg *configfile.Config, isSharedServer bool) string {
	if isSharedServer {
		if dir, err := doltserver.SharedDoltDir(); err == nil {
			return dir
		}
	}

	if d := cfg.GetDoltDataDir(); d != "" {
		if filepath.IsAbs(d) {
			return d
		}
		return filepath.Join(beadsDir, d)
	}

	return filepath.Join(beadsDir, "dolt")
}

func bootstrapServerPort(beadsDir string, cfg *configfile.Config, isSharedServer bool) int {
	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}

	if isSharedServer {
		if sharedDir, err := doltserver.SharedServerDir(); err == nil {
			if port := doltserver.ReadPortFile(sharedDir); port > 0 {
				return port
			}
		}
		return doltserver.DefaultSharedServerPort
	}

	if port := doltserver.ReadPortFile(beadsDir); port > 0 {
		return port
	}

	if p := config.GetStringFromDir(beadsDir, "dolt.port"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}

	if cfg.DoltServerPort > 0 {
		return cfg.DoltServerPort
	}

	return configfile.DefaultDoltServerPort
}

func printBootstrapPlan(plan BootstrapPlan) {
	switch plan.Action {
	case "none":
		if plan.Blocked {
			// Nothing was set up and nothing was found. Never print the
			// success tick here — that is the #5743 false success.
			fmt.Fprintf(os.Stderr, "Bootstrap did not set up a database: %s\n", plan.Reason)
			if plan.BlockedRemote != "" {
				// Deliberately not "delete sync.remote and re-run": the git
				// origin probe on that path swallows its error, so for a
				// credential failure the retry would "succeed" by creating a
				// fresh database that has diverged from the team's history.
				fmt.Fprintf(os.Stderr, "  Reproduce: git ls-remote %s refs/dolt/data\n",
					gitRemoteURLForLsRemote(plan.BlockedRemote))
				fmt.Fprintf(os.Stderr, "  If the remote is right, fix access (credentials, network, VPN) and re-run 'bd bootstrap'.\n")
				fmt.Fprintf(os.Stderr, "  If it is wrong, point sync.remote at the Dolt remote: bd dolt remote add origin <url>\n")
			}
			return
		}
		fmt.Printf("✓ Database already exists: %s\n", plan.BeadsDir)
		if !usesSQLServer() {
			fmt.Printf("  Nothing to do.\n")
		} else {
			fmt.Printf("  Nothing to do. Use 'bd doctor' to check health.\n")
		}
	case "sync":
		fmt.Printf("Bootstrap plan: clone from remote\n")
		fmt.Printf("  Remote: %s\n", redactRemoteURL(plan.SyncRemote))
		fmt.Printf("  Database: %s\n", plan.Database)
	case "restore":
		fmt.Printf("Bootstrap plan: restore from backup\n")
		fmt.Printf("  Backup dir: %s\n", plan.BackupDir)
	case "jsonl-import":
		fmt.Printf("Bootstrap plan: import from git-tracked JSONL\n")
		fmt.Printf("  JSONL file: %s\n", plan.JSONLFile)
		fmt.Printf("  Database: %s\n", plan.Database)
	case "init":
		fmt.Printf("Bootstrap plan: create fresh database\n")
		fmt.Printf("  Database: %s\n", plan.Database)
	}
}

// confirmPrompt asks the user to confirm an action. Returns true if
// nonInteractive is set, stdin is not a terminal, or the user confirms.
func confirmPrompt(message string, nonInteractive bool) bool {
	if nonInteractive {
		return true
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return true
	}
	fmt.Fprintf(os.Stderr, "%s [Y/n] ", message)
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')
	line = strings.TrimSpace(strings.ToLower(line))
	return line == "" || line == "y" || line == "yes"
}

func executeBootstrapPlan(plan BootstrapPlan, cfg *configfile.Config, nonInteractive bool) error {
	if err := requireBootstrapDoltBackend(cfg, plan.BeadsDir); err != nil {
		return err
	}
	if !confirmPrompt("Proceed?", nonInteractive) {
		fmt.Fprintf(os.Stderr, "Aborted.\n")
		return nil
	}

	ctx := context.Background()

	// Workspace operation gate: every bootstrap action below replaces or
	// creates workspace state (sync/restore/jsonl-import/init), and
	// bootstrap is a skip-store command that the PersistentPreRunE
	// chokepoint never gates. This is the single point where the target
	// workspace is known and no state has been touched yet, so acquire the
	// workspace + physical-root gates EXCLUSIVELY here for the rest of the
	// plan execution. Exclusive failures are hard errors: bootstrap
	// refuses to run over live bd activity rather than pretend.
	//
	// The resolved PhysicalRoots for plan.BeadsDir do not necessarily cover
	// the directory the actions below actually open — executeInitAction,
	// executeRestoreAction, and executeJSONLImportAction all open
	// doltserver.ResolveDoltDir(plan.BeadsDir) directly (e.g. .beads/dolt
	// for an embedded-metadata workspace). Pass it as an extraRoot,
	// mirroring how bd init passes its own resolved db path, so the
	// physical directory bootstrap writes is actually gated.
	gateHandle, gateErr := acquireExclusiveWorkspaceGates(ctx, plan.BeadsDir, "bd bootstrap "+plan.Action,
		doltserver.ResolveDoltDir(plan.BeadsDir))
	if gateErr != nil {
		return fmt.Errorf("bd bootstrap refuses to run over live bd activity on this workspace: %w", gateErr)
	}
	defer func() { _ = gateHandle.Release() }()

	switch plan.Action {
	case "sync":
		return executeSyncAction(ctx, plan, cfg)
	case "restore":
		return executeRestoreAction(ctx, plan, cfg)
	case "jsonl-import":
		return executeJSONLImportAction(ctx, plan, cfg)
	case "init":
		return executeInitAction(ctx, plan, cfg)
	}
	return nil
}

func executeInitAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	prefix := inferPrefix(cfg)
	dbName := cfg.GetDoltDatabase()

	s, err := newDoltStore(ctx, &dolt.Config{
		Path:            doltserver.ResolveDoltDir(plan.BeadsDir),
		Database:        dbName,
		CreateIfMissing: true,
		AutoStart:       true,
		BeadsDir:        plan.BeadsDir,
	})
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		return fmt.Errorf("set issue prefix: %w", err)
	}
	if err := s.CommitWithConfig(ctx, "bd bootstrap"); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	wireBootstrapDoltRemote(ctx, s, plan)
	fmt.Fprintf(os.Stderr, "Created fresh database with prefix %q\n", prefix)
	return nil
}

// wireBootstrapDoltRemote registers plan.SyncRemote as Dolt remote "origin" on
// a workspace bootstrap just provisioned locally.
//
// The sync action has always done this (via configureInitDoltRemote after the
// clone) and bd init does it too, but the local actions never did, because a
// configured sync.remote used to short-circuit to "sync" before they could be
// reached. The refs/dolt/data fall-through makes them reachable, and without
// the remote the promise that "the first bd dolt push creates refs/dolt/data
// on that same git remote" breaks: push either refuses to adopt a remote
// without consent, or adopts the git origin — a different repository under the
// dedicated-data-repo pattern — and rewrites the committed sync.remote to
// match. Two machines bootstrapping in the pre-first-push window would then
// mint independent Dolt identities and diverge, which is the exact failure
// bootstrap exists to prevent (#5743).
func wireBootstrapDoltRemote(ctx context.Context, s storage.DoltStorage, plan BootstrapPlan) {
	if plan.SyncRemote == "" {
		return
	}
	configureInitDoltRemote(ctx, s, doltRemoteURL(plan.SyncRemote), false)
}

func executeRestoreAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	prefix := inferPrefix(cfg)
	dbName := cfg.GetDoltDatabase()

	s, err := newDoltStore(ctx, &dolt.Config{
		Path:            doltserver.ResolveDoltDir(plan.BeadsDir),
		Database:        dbName,
		CreateIfMissing: true,
		AutoStart:       true,
		BeadsDir:        plan.BeadsDir,
	})
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		return fmt.Errorf("set issue prefix: %w", err)
	}
	if err := s.CommitWithConfig(ctx, "bd bootstrap: init"); err != nil {
		return fmt.Errorf("commit init: %w", err)
	}

	if err := runBackupRestore(ctx, s, plan.BackupDir, false); err != nil {
		return fmt.Errorf("restore from backup: %w", err)
	}

	wireBootstrapDoltRemote(ctx, s, plan)
	fmt.Fprintf(os.Stderr, "Restored from backup\n")
	return nil
}

func executeJSONLImportAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	prefix := inferPrefix(cfg)
	dbName := cfg.GetDoltDatabase()

	s, err := newDoltStore(ctx, &dolt.Config{
		Path:            doltserver.ResolveDoltDir(plan.BeadsDir),
		Database:        dbName,
		CreateIfMissing: true,
		AutoStart:       true,
		BeadsDir:        plan.BeadsDir,
	})
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		return fmt.Errorf("set issue prefix: %w", err)
	}
	if err := s.CommitWithConfig(ctx, "bd bootstrap: init"); err != nil {
		return fmt.Errorf("commit init: %w", err)
	}

	count, err := importFromLocalJSONL(ctx, s, plan.JSONLFile)
	if err != nil {
		return fmt.Errorf("import from JSONL: %w", err)
	}

	if err := s.Commit(ctx, "bd bootstrap: import from issues.jsonl"); err != nil {
		return fmt.Errorf("commit import: %w", err)
	}

	wireBootstrapDoltRemote(ctx, s, plan)
	fmt.Fprintf(os.Stderr, "Imported %d issues from %s\n", count, plan.JSONLFile)
	return nil
}

func executeSyncAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	// Ensure .beads directory exists — it may not in the "fresh clone"
	// bootstrap path where we detected remote data before .beads was
	// created. Deferred here to preserve --dry-run semantics. (GH#2792)
	if err := os.MkdirAll(plan.BeadsDir, 0o750); err != nil {
		return fmt.Errorf("create beads directory: %w", err)
	}

	dbName := cfg.GetDoltDatabase()
	if err := cloneFromRemote(ctx, plan.BeadsDir, plan.SyncRemote, dbName, cfg); err != nil {
		return err
	}

	// Finalize the bootstrapped workspace so subsequent bd commands can open
	// the cloned database. Without metadata.json and config.yaml,
	// configfile.Load() returns nil, callers fall back to the default
	// dolt_database name, and bd loses track of the cloned database —
	// producing "no beads configuration found" and "Error 1105: no database
	// selected" on bd status / bd dolt push in fresh clones. Every other
	// bootstrap action (init, restore, jsonl-import) writes these files via
	// newDoltStore + createConfigYaml; the sync path historically did not.
	// (GH#3201)
	if err := finalizeSyncedBootstrap(plan.BeadsDir, plan.SyncRemote, cfg, dbName); err != nil {
		return err
	}

	// Open and close the store to ensure dolt_ignore'd wisp tables are
	// created in the working set. Clone does not include these tables
	// (they are never committed), so they must be recreated after clone.
	// Both embedded and server mode handle this in their store init paths.
	warmupStore, err := newDoltStoreFromConfig(ctx, plan.BeadsDir)
	if err != nil {
		// #4259: the cloned remote is behind this binary, so the remote-migrate
		// gate held migration for an explicit operator decision. Surface that
		// now with bootstrap-specific guidance and a non-zero exit. Returning
		// silent success here (as this path once did) sent operators in a
		// loop: the first real command failed with the gate message, whose
		// generic "adopt" remedy is `bd bootstrap` — which re-clones the same
		// behind database and silently "succeeds" again (bd-6dnrw.31).
		var gateErr *schema.RemoteMigrateGateError
		if errors.As(err, &gateErr) {
			if !jsonOutput {
				printBootstrapRemoteBehindGuidance(os.Stderr, gateErr, plan.SyncRemote, "bd bootstrap")
			}
			unit := "migrations"
			if gateErr.Pending == 1 {
				unit = "migration"
			}
			return fmt.Errorf("clone from %s succeeded, but the database needs %d schema %s (v%d -> v%d) that bd will not auto-apply to a remote-backed database (#4259)",
				plan.SyncRemote, gateErr.Pending, unit, gateErr.CurrentVersion, gateErr.LatestVersion)
		}
		// Non-fatal: wisp tables will be created on the next command that
		// opens the store. Warn so the user knows to retry if they hit
		// "table not found: wisp_*" errors.
		fmt.Fprintf(os.Stderr, "Warning: post-clone store init failed (wisp tables may be missing): %v\n", err)
		return nil
	}
	configureInitDoltRemote(ctx, warmupStore, plan.SyncRemote, false)
	_ = warmupStore.Close()

	return nil
}

// printInitJoinGuidance corrects the one thing the gate's shared-store block
// gets wrong when `bd init` is the caller: it says to run `bd migrate schema`,
// but this init failed before writing metadata.json, so there is no workspace
// here to run it from (gastownhall/beads#5920). A client JOINING an existing,
// behind shared server has to be told where the command actually works.
//
// Only the shared-store arm needs this. The remote-backed refusals from init
// are already handled by printBootstrapRemoteBehindGuidance above, and their
// remedies are not workspace-local in the same way.
func printInitJoinGuidance(w io.Writer, e *schema.RemoteMigrateGateError) {
	if e.Decision != "shared-no-remote" {
		return
	}
	fmt.Fprintf(w,
		"\n  This workspace was NOT created — bd init stopped before writing\n"+
			"  .beads/metadata.json, so `%s` cannot be run from here yet.\n"+
			"  Either run it from a workspace already set up against this server, or\n"+
			"  join and migrate in one step once every client is upgraded:\n"+
			"        %s=1 bd init …\n",
		schema.SharedConsentCommand, schema.AllowRemoteMigrateEnv)
}

// printBootstrapRemoteBehindGuidance explains a remote-migrate gate refusal in
// bootstrap terms. The gate's generic remedy ("adopt the migrated database:
// bd bootstrap") is wrong from inside a bootstrap-style clone — the database
// was just cloned from the remote, so the REMOTE is what is behind this binary
// and re-cloning can never help. The way out is exactly one designated machine
// migrating and pushing. rerunCmd is the command the operator just ran ("bd
// bootstrap", "bd init") so the don't-bother-retrying line names it.
func printBootstrapRemoteBehindGuidance(w io.Writer, e *schema.RemoteMigrateGateError, syncRemote, rerunCmd string) {
	unit := "migrations"
	if e.Pending == 1 {
		unit = "migration"
	}
	fmt.Fprintf(w, "\nThe database cloned from %s needs %d schema %s (v%d -> v%d).\n",
		syncRemote, e.Pending, unit, e.CurrentVersion, e.LatestVersion)
	fmt.Fprint(w,
		"  bd will not migrate it automatically: migrating clones independently forks\n"+
			"  the schema so `bd dolt pull` can no longer merge (#4259).\n"+
			"\n"+
			"  Re-running `"+rerunCmd+"` will NOT fix this — the remote itself is behind.\n"+
			"  Choose one:\n"+
			"    • This machine is the designated migrator (exactly ONE machine should be):\n"+
			"        bd migrate --force\n"+
			"        bd dolt push\n"+
			"      then other machines re-run `bd bootstrap` to adopt the migrated database.\n"+
			"    • Another machine is the designated migrator: wait for it to push, then\n"+
			"      re-run `bd bootstrap`, or keep using a bd version that matches the remote.\n\n")
}

// finalizeSyncedBootstrap writes metadata.json and config.yaml after a
// successful sync clone, matching the on-disk layout that bd init produces.
// It is idempotent: re-running over an already-finalized workspace leaves
// existing files intact (createConfigYaml skips if config.yaml exists; the
// metadata.json write is a full rewrite that preserves caller fields).
func finalizeSyncedBootstrap(beadsDir, syncRemote string, cfg *configfile.Config, dbName string) error {
	// Preserve whatever upstream fields were already set in cfg (which may
	// be DefaultConfig when metadata.json was absent, or a parent workspace
	// config propagated by findParentConfig), then fill in the bits
	// required by configfile.Load consumers.
	cfg.Backend = configfile.BackendDolt
	cfg.DoltDatabase = dbName
	switch {
	case cfg.IsDoltProxiedServerMode():
		cfg.DoltMode = configfile.DoltModeProxiedServer
	case cfg.IsDoltServerMode() || doltserver.IsSharedServerMode():
		cfg.DoltMode = configfile.DoltModeServer
	default:
		cfg.DoltMode = configfile.DoltModeEmbedded
	}
	// Mirror init's convention: metadata.json database points at the Dolt
	// directory rather than the legacy "beads.db" placeholder.
	if cfg.Database == "" || cfg.Database == beads.CanonicalDatabaseName {
		cfg.Database = "dolt"
	}

	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("write metadata.json: %w", err)
	}

	if err := createConfigYaml(beadsDir, false, ""); err != nil {
		return fmt.Errorf("create config.yaml: %w", err)
	}
	if err := doctor.EnsureGitignoreForBeadsDir(beadsDir); err != nil {
		return fmt.Errorf("ensure .beads/.gitignore: %w", err)
	}

	// Persist sync.remote so subsequent fresh clones (and bd bootstrap
	// retries) can rediscover the remote without re-probing origin refs.
	if syncRemote != "" {
		if err := config.SetYamlConfigInDir(beadsDir, "sync.remote", syncRemote); err != nil {
			return fmt.Errorf("persist sync.remote to config.yaml: %w", err)
		}
	}

	return nil
}

type remoteCloneMode int

const (
	remoteCloneAuto remoteCloneMode = iota
	remoteCloneEmbedded
	remoteCloneExternalServer
	remoteCloneCLI
)

// cloneFromRemote clones a Dolt database from a remote URL.
// In embedded mode, uses the embedded engine's DOLT_CLONE procedure.
// In external server mode, connects to the running server via MySQL and
// executes DOLT_CLONE so the server places the database in its own data
// directory. In owned-server mode, shells out to dolt clone via
// BootstrapFromRemoteWithDB.
// Shared by bd init and bd bootstrap to keep clone logic in one place.
func cloneFromRemote(ctx context.Context, beadsDir, remoteURL, dbName string, cfg *configfile.Config) error {
	return cloneFromRemoteWithMode(ctx, beadsDir, remoteURL, dbName, cfg, remoteCloneAuto)
}

func cloneFromRemoteWithMode(ctx context.Context, beadsDir, remoteURL, dbName string, cfg *configfile.Config, cloneMode remoteCloneMode) error {
	mode := resolveRemoteCloneMode(beadsDir, cfg, cloneMode)

	switch mode {
	case remoteCloneEmbedded:
		return cloneViaEmbedded(ctx, beadsDir, remoteURL, dbName)

	case remoteCloneExternalServer:
		if cfg == nil {
			// Caller didn't provide config; fall back to loading from disk.
			if loaded, err := configfile.Load(beadsDir); err == nil && loaded != nil {
				cfg = loaded
			}
		}
		if cfg != nil {
			return cloneViaServer(ctx, beadsDir, remoteURL, dbName, cfg)
		}
		// No config available — fall through to CLI clone.
		fmt.Fprintf(os.Stderr, "Warning: server mode detected but no config available, falling back to CLI clone\n")
		return cloneViaCLI(ctx, beadsDir, remoteURL, dbName)

	default:
		return cloneViaCLI(ctx, beadsDir, remoteURL, dbName)
	}
}

func resolveRemoteCloneMode(beadsDir string, cfg *configfile.Config, cloneMode remoteCloneMode) remoteCloneMode {
	if cloneMode != remoteCloneAuto {
		return cloneMode
	}

	if cfg != nil {
		if cfg.IsDoltServerMode() || doltserver.IsSharedServerMode() || os.Getenv("BEADS_DOLT_SERVER_MODE") == "1" {
			return remoteCloneExternalServer
		}
		return remoteCloneEmbedded
	}

	switch doltserver.ResolveServerMode(beadsDir) {
	case doltserver.ServerModeEmbedded:
		return remoteCloneEmbedded
	case doltserver.ServerModeExternal:
		return remoteCloneExternalServer
	default:
		return remoteCloneCLI
	}
}

// cloneViaEmbedded clones using the embedded Dolt engine (CGO required).
func cloneViaEmbedded(ctx context.Context, beadsDir, remoteURL, dbName string) error {
	dataDir := filepath.Join(beadsDir, "embeddeddolt")
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return fmt.Errorf("create embeddeddolt directory: %w", err)
	}
	db, cleanup, err := embeddeddolt.OpenSQL(ctx, dataDir, "", "")
	if err != nil {
		return fmt.Errorf("open embedded engine for clone: %w", err)
	}
	defer func() { _ = cleanup() }()

	if err := versioncontrolops.DoltClone(ctx, db, remoteURL, dbName, os.Getenv("DOLT_REMOTE_USER")); err != nil {
		return fmt.Errorf("clone from remote: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Synced database from %s\n", remoteURL)
	return nil
}

// cloneViaServer clones by connecting to the external Dolt server and
// executing CALL DOLT_CLONE. The server places the database in its own
// data directory, which is the correct behavior for externally managed
// servers where bd does not know the filesystem layout.
func cloneViaServer(ctx context.Context, beadsDir, remoteURL, dbName string, cfg *configfile.Config) error {
	port := serverClonePort(beadsDir, cfg)
	dsn := doltutil.ServerDSN{
		Socket:   cfg.GetDoltServerSocket(),
		Host:     cfg.GetDoltServerHost(),
		Port:     port,
		User:     cfg.GetDoltServerUser(),
		Password: cfg.GetDoltServerPasswordForPort(port),
		TLS:      cfg.GetDoltServerTLS(),
		// No Database — DOLT_CLONE creates the database.
	}.String()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return fmt.Errorf("connect to dolt server for clone: %w", err)
	}
	defer db.Close()

	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if err := db.PingContext(cloneCtx); err != nil {
		return fmt.Errorf("dolt server unreachable at %s:%d (is dolt sql-server running?): %w",
			cfg.GetDoltServerHost(), port, err)
	}

	if err := versioncontrolops.DoltClone(cloneCtx, db, remoteURL, dbName, os.Getenv("DOLT_REMOTE_USER")); err != nil {
		return fmt.Errorf("clone from remote via server: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Synced database from %s (via server at %s:%d)\n",
		remoteURL, cfg.GetDoltServerHost(), port)
	return nil
}

func serverClonePort(beadsDir string, cfg *configfile.Config) int {
	if cfg != nil && cfg.DoltServerPort > 0 {
		return cfg.DoltServerPort
	}
	if p := os.Getenv("BEADS_DOLT_SERVER_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}
	if p := os.Getenv("BEADS_DOLT_PORT"); p != "" {
		if port, err := strconv.Atoi(p); err == nil && port > 0 {
			return port
		}
	}
	if resolved := doltserver.DefaultConfig(beadsDir); resolved.Port > 0 {
		return resolved.Port
	}
	if cfg != nil {
		return cfg.GetDoltServerPort()
	}
	return configfile.DefaultDoltServerPort
}

// cloneViaCLI clones by shelling out to the dolt CLI.
// Used for owned-server mode where bd manages the server lifecycle.
func cloneViaCLI(ctx context.Context, beadsDir, remoteURL, dbName string) error {
	doltDir := doltserver.ResolveDoltDir(beadsDir)
	synced, err := dolt.BootstrapFromRemoteWithDB(ctx, doltDir, remoteURL, dbName)
	if err != nil {
		return fmt.Errorf("sync from remote: %w", err)
	}
	if synced {
		fmt.Fprintf(os.Stderr, "Synced database from %s\n", remoteURL)
	}
	return nil
}

func inferPrefix(cfg *configfile.Config) string {
	db := cfg.GetDoltDatabase()
	if db != "" && db != "beads" {
		return db
	}
	cwd, _ := os.Getwd()
	return filepath.Base(cwd)
}

// isNonInteractiveBootstrap returns true if bootstrap should skip confirmation prompts.
// Precedence: explicit flag > BD_NON_INTERACTIVE env > CI env > terminal detection.
func isNonInteractiveBootstrap(flagValue bool) bool {
	if flagValue {
		return true
	}
	if v := os.Getenv("BD_NON_INTERACTIVE"); v == "1" || v == "true" {
		return true
	}
	if v := os.Getenv("CI"); v == "true" || v == "1" {
		return true
	}
	return !term.IsTerminal(int(os.Stdin.Fd()))
}

// findParentConfig walks up from beadsDir's parent looking for a
// .beads/metadata.json in ancestor directories. This handles the case where a
// rig subdirectory (its own git repo) doesn't have a local .beads but its
// parent workspace does. Returns nil if no parent config is found. A malformed
// or unreadable ancestor metadata file is authoritative and returned as an error;
// bootstrap must not skip it and select a more distant workspace or defaults.
func findParentConfig(beadsDir string) (*configfile.Config, error) {
	// Start from the parent of beadsDir's enclosing directory.
	// beadsDir is typically "<project>/.beads", so we start from <project>'s parent.
	start := filepath.Dir(filepath.Dir(beadsDir))
	homeDir, _ := os.UserHomeDir()

	for dir := start; dir != "/" && dir != "."; {
		candidate := filepath.Join(dir, ".beads")
		cfg, err := configfile.LoadForDiscovery(candidate)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", configfile.ConfigPath(candidate), err)
		}
		if cfg != nil {
			if err := guardLegacyUpgradeWorkspace(candidate); err != nil {
				return nil, err
			}
			return cfg, nil
		}

		// Don't search above $HOME
		if homeDir != "" && dir == homeDir {
			break
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return nil, nil
}

func init() {
	bootstrapCmd.Flags().Bool("dry-run", false, "Show what would be done without doing it")
	bootstrapCmd.Flags().BoolP("yes", "y", false, "Skip confirmation prompts (for CI/automation)")
	bootstrapCmd.Flags().Bool("non-interactive", false, "Alias for --yes")
	rootCmd.AddCommand(bootstrapCmd)
}

// isGitCodeRepoURL reports whether rawURL looks like a git code-repository
// remote rather than a Dolt data remote. Used to block accidental DOLT_CLONE
// against forges like github.com.
//
// Rules (in priority order):
//   - Dolt-native schemes (dolthub, s3, gs, az, file) → always false
//   - .git suffix → always true (Dolt DBs never carry a .git suffix)
//   - Well-known code forges / their subdomains → true
//   - Everything else → false (git+ssh:// to self-hosted Dolt remotes is valid)
func isGitCodeRepoURL(rawURL string) bool {
	// Dolt-native schemes are never code repos.
	for _, prefix := range []string{"dolthub://", "s3://", "gs://", "az://", "file://"} {
		if strings.HasPrefix(rawURL, prefix) {
			return false
		}
	}
	// .git suffix is a universal code-repo signal; Dolt DBs never use it.
	if strings.HasSuffix(strings.ToLower(rawURL), ".git") {
		return true
	}
	// Well-known code-hosting forges.
	host := urlHostname(rawURL)
	switch host {
	case "github.com", "gitlab.com", "bitbucket.org", "codeberg.org":
		return true
	}
	return strings.HasSuffix(host, ".github.com") || strings.HasSuffix(host, ".gitlab.com")
}

// urlHostname extracts the lowercase hostname from a URL without importing
// net/url. Handles scheme://[user@]host[:port]/path and SCP git@host:path.
func urlHostname(rawURL string) string {
	if sep := strings.Index(rawURL, "://"); sep >= 0 {
		rest := rawURL[sep+3:]
		if at := strings.Index(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		if slash := strings.Index(rest, "/"); slash >= 0 {
			rest = rest[:slash]
		}
		if colon := strings.Index(rest, ":"); colon >= 0 {
			rest = rest[:colon]
		}
		return strings.ToLower(rest)
	}
	// SCP-style: git@github.com:org/repo
	if at := strings.Index(rawURL, "@"); at >= 0 {
		rest := rawURL[at+1:]
		if colon := strings.Index(rest, ":"); colon >= 0 {
			return strings.ToLower(rest[:colon])
		}
	}
	return ""
}
