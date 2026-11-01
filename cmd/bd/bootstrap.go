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
	"runtime"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/cmd/bd/doctor/fix"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/doltserver"
	"github.com/steveyegge/beads/internal/doltversion"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/remedy"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/dolt"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/storage/versioncontrolops"
	"github.com/steveyegge/beads/internal/utils"
	"golang.org/x/term"
)

var resolveBootstrapAuthoritativeMetadata = fix.ResolveAuthoritativeServerMetadata

// bootstrapServerProbeConfig is the endpoint bd bootstrap dials on a Dolt
// server: the existence probe dials it, and so does the server clone.
type bootstrapServerProbeConfig struct {
	socket   string
	host     string
	port     int
	user     string
	pass     string
	database string
	tls      bool
}

// dsn is the connection string for ep with no database selected: the probe
// lists the server's databases, and DOLT_CLONE creates one.
func (ep bootstrapServerProbeConfig) dsn() string {
	return doltutil.ServerDSN{
		Socket:   ep.socket,
		Host:     ep.host,
		Port:     ep.port,
		User:     ep.user,
		Password: ep.pass,
		TLS:      ep.tls,
	}.String()
}

// serverLocation names the server ep dials for a message
// (doltserver.Endpoint.Location): host:port, else its socket when it has no
// port, never host:0.
func (ep bootstrapServerProbeConfig) serverLocation() string {
	return doltserver.Endpoint{Host: ep.host, Port: ep.port, Socket: ep.socket}.Location()
}

// serverLocation names the server the probe dialed, as
// bootstrapServerProbeConfig.serverLocation does.
func (p serverDBProbe) serverLocation() string {
	return doltserver.Endpoint{Host: p.Host, Port: p.Port, Socket: p.Socket}.Location()
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
	dbName := probeCfg.database
	db, err := sql.Open("mysql", probeCfg.dsn())
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
  • If no database exists: creates a fresh one — unless the workspace was already
    initialized (metadata.json has a project_id) and its server-mode database is
    missing, in which case bootstrap refuses rather than strand the existing data
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
						if explicitDir := beads.ExplicitBeadsDir(); explicitDir != "" {
							beadsDir = explicitDir
						} else if fallbackDir := beads.GetWorktreeFallbackBeadsDir(); fallbackDir != "" {
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
			// Discovery passes over a .beads marked only by a legacy
			// config.json or holding only a SQLite database. One in a format
			// this build cannot open, or a historical SQLite workspace, is
			// refused as that, not reported as no workspace.
			if err := checkUndiscoveredWorkspaceFormat(); err != nil {
				return handleWorkspaceGuardError(err)
			}
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
			return handleWorkspaceGuardError(err)
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
			if isUnsupportedWorkspaceFormat(err) || isLegacyUpgradeRefusal(err) {
				// Admission's refusal of the ancestor, as bd prints it there:
				// its metadata loaded, so there is nothing to fix or restore.
				return handleWorkspaceGuardError(err)
			} else if err != nil {
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
			// The error can carry sync.remote verbatim, credentials and all:
			// sanitizeURL passes through a URL net/url cannot parse (a
			// password with an unencoded "#" or "?"), and Dolt's "invalid
			// remote url:" repeats it. Scrub it where it is printed.
			return HandleError("Bootstrap failed: %s", scrubURLCredentials(err.Error()))
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
	Action   string `json:"action"` // "sync", "restore", "jsonl-import", "init", "none", "refuse"
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
	// the printed output both switch on.
	//
	// "blocked" is the stable, script-facing signal and must be set for EVERY
	// no-database outcome, so a consumer that only understands "none" is never
	// told a run succeeded when it did not. A new "action" value is acceptable
	// only for a state that must not be conflated with an existing one — the
	// refusal is one, because "none" also covers the benign "a database
	// already exists, nothing to do" — and only when it is accompanied by
	// "blocked" and, outside --dry-run, a non-zero exit code. That is the
	// contract to preserve when extending this enum: not "never add an action",
	// but "never add an action that is the only signal of what happened".
	Blocked bool `json:"blocked,omitempty"`
	// BlockedRemote is the credential-free remote URL a Blocked plan could not
	// verify, carried only so the diagnostic can name it.
	BlockedRemote string `json:"blocked_remote,omitempty"`
	HasExisting   bool   `json:"has_existing"`

	// refusalDetail is the full operator-facing explanation for
	// Action=="refuse". It is unexported so the ANSI styling it carries never
	// leaks into --json output; JSON consumers read the plain-text one-liner
	// in Reason instead.
	refusalDetail string
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
	// "refuse" is a no-database outcome too, so it carries the same flag as
	// "none": the action string says WHY bootstrap stopped, Blocked says THAT
	// it stopped, and only the latter is the contract scripts switch on. This
	// changes no exit code — RunE routes Action=="refuse" through
	// executeBootstrapPlan, which errors, and --dry-run is contractually
	// exit 0 either way (see bootstrapPlanOutcome) — it only stops a refusal
	// from being the one bootstrap outcome that produced nothing yet reported
	// "blocked" absent.
	plan.Blocked = (plan.Action == "none" || plan.Action == "refuse") && !plan.HasExisting
	if plan.Blocked {
		// Keep SyncRemote's documented "empty on a Blocked plan" invariant
		// true now that refusals are Blocked: a refusal can be reached with a
		// forge SyncRemote already recorded (planConfiguredSyncRemote keeps it
		// for the workspace it expected to create), and no Blocked plan wires
		// up, clones from, or pushes to a remote. Inert for execution — every
		// consumer of plan.SyncRemote runs on a sync/provisioning action.
		plan.SyncRemote = ""
	} else {
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
	dbAction, dbProbe, hasExistingDB := existingBootstrapDBPlan(beadsDir, cfg, isServer, isSharedServer)
	// The same masking hazard applies to a server-side hit with no local
	// evidence. Probing on ProjectID alone (see existingBootstrapDBPlan) means
	// a shadow-dir-less workspace now reaches the Exists arm, where the only
	// thing matched is the database NAME — nothing checks that the database on
	// the server holds this project's data. Such a workspace previously
	// skipped the probe entirely and always fell through to sync.remote, so
	// settling here would let a stale or unrelated same-named database (including
	// one left behind by the very bug this guard fixes) silently suppress the
	// configured recovery path. Defer it exactly as a missing beadsDir is
	// deferred: sync.remote / refs/dolt/data / local files get their turn, and
	// the name match survives as the fallback verdict rather than becoming the
	// verdict.
	settledOnNameAlone := dbProbe.Ran && !dbProbe.LocalEvidence
	if hasExistingDB {
		if dbAction.Action != "none" || (beadsDirExists && !settledOnNameAlone) {
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

	// Every recovery source has now been ruled out, so the fall-through below
	// is a fresh, empty database. Decide the refusal HERE rather than inside
	// executeInitAction: at plan time the answer reaches --dry-run and --json,
	// printBootstrapPlan never advertises "will create fresh database" for a
	// plan that cannot run, and confirmPrompt never asks the operator to
	// approve one. The probe result is reused, so no second dial is needed.
	if refusal, refused := refuseServerModeInitPlan(plan, cfg, isServer, dbProbe); refused {
		return refusal
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
		plan.Reason = "sync.remote configured — will clone from " + remedy.PrintedValue(redactRemoteURL(syncRemote))
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
		reason := fmt.Sprintf("could not verify refs/dolt/data on sync.remote %q: %s",
			redactRemoteURL(syncRemote), remedy.PrintedMessage(probeErr))
		// Said here as well as in the final verdict because a local backup may
		// well rescue this run, and then this is the only place the user learns
		// the remote could not be reached.
		fmt.Fprintf(os.Stderr, "note: not cloning — %s; checking local recovery sources\n", reason)
		return false, reason, redactRemoteURL(routed)
	case hasData:
		plan.SyncRemote = routed
		plan.Action = "sync"
		plan.Reason = "sync.remote git repo has Dolt data (refs/dolt/data) — will clone from " + remedy.PrintedValue(redactRemoteURL(routed))
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
	plan.Reason = "Found Dolt data on git origin (refs/dolt/data) — will clone from " + remedy.PrintedValue(redactRemoteURL(originURL))
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

// refuseServerModeInitPlan turns a would-be fresh "init" into an explicit
// "refuse" plan when metadata.json proves this workspace was already
// initialized (ProjectID set) and the configured server-mode database could
// not be confirmed to exist. Creating one here would strand the existing issue
// data behind a new, empty database of the same name — the same root cause
// the be-5up5 guards in init.go address for bd init (PR #5791).
//
// Reaching the init fall-through already implies the database was not
// confirmed: existingBootstrapDBPlan returns a settled "none" plan whenever a
// probe found it. The probe is passed in rather than re-run so the refusal
// costs no extra round-trip and can quote why the check failed.
func refuseServerModeInitPlan(plan BootstrapPlan, cfg *configfile.Config, isServer bool, probe serverDBProbe) (BootstrapPlan, bool) {
	if !isServer || cfg.ProjectID == "" || !probe.Ran || probe.Exists {
		return BootstrapPlan{}, false
	}
	plan.Action = "refuse"
	plan.Reason = bootstrapMissingServerDBSummary(plan.Database, probe.serverLocation(), probe.Err)
	plan.refusalDetail = bootstrapMissingServerDBRefusal(plan.Database, probe.serverLocation(), probe.Err).Error()
	return plan, true
}

// serverDBProbe records what a server-mode existence probe concluded, so the
// answer can be reused by the init fall-through instead of dialing the server
// a second time.
type serverDBProbe struct {
	Ran bool // a probe was actually performed
	// Host, Port and Socket are where it dialed: bootstrapServerEndpoint,
	// which the clone dials too.
	Host   string
	Port   int
	Socket string
	Exists bool  // the server confirmed the database exists
	Err    error // the probe could not reach or question the server
	// LocalEvidence records whether anything on this machine ties this
	// workspace to the database the probe asked about — in practice a
	// populated local shadow directory. Without it the probe can only match
	// on the database NAME, which is why the caller refuses to let an
	// Exists verdict settle the plan ahead of the real recovery sources.
	LocalEvidence bool
}

// existingBootstrapDBPlan reports whether an existing database already settles
// the bootstrap plan. The returned serverDBProbe carries the server-mode
// probe result — including on the false path, where the caller needs it to
// decide whether a fall-through to "init" is safe.
func existingBootstrapDBPlan(beadsDir string, cfg *configfile.Config, isServer, isSharedServer bool) (BootstrapPlan, serverDBProbe, bool) {
	plan := BootstrapPlan{
		BeadsDir: beadsDir,
		Database: cfg.GetDoltDatabase(),
	}

	if isServer {
		// Scope note: this is per-workspace evidence in locally-managed server
		// mode only. In shared-server mode bootstrapServerDoltDir resolves to
		// doltserver.SharedDoltDir() — ~/.beads/shared-server/dolt/, one
		// directory for EVERY shared-server database on the machine — so a
		// fresh clone of project B reads a populated directory belonging to
		// projects A and C. Everything below that calls this "local evidence"
		// is therefore only sound for the locally-managed case; the
		// shared-server granularity gap is pre-existing (main probes on the
		// same signal) and is tracked separately rather than narrowed here,
		// because keying on the database-named subdirectory would also change
		// the locally-managed verdict for a data dir holding only .doltcfg.
		dbPath := bootstrapServerDoltDir(beadsDir, cfg, isSharedServer)
		hasLocalShadowDir := false
		if info, err := os.Stat(dbPath); err == nil && info.IsDir() {
			if entries, _ := os.ReadDir(dbPath); len(entries) > 0 {
				hasLocalShadowDir = true
			}
		}
		// A missing/empty local shadow directory normally means no database
		// exists yet. But when metadata.json carries a ProjectID, this
		// workspace was already initialized somewhere — most likely a fresh
		// clone of an already-bootstrapped server-mode project, which has no
		// local shadow directory by design. In that case, skip the local-dir
		// short-circuit and live-probe the server directly rather than
		// concluding "no database" from local-disk state alone.
		if !hasLocalShadowDir && cfg.ProjectID == "" {
			return BootstrapPlan{}, serverDBProbe{}, false
		}

		// Retry the reachable-but-absent window only when the local shadow
		// directory says this database really does live here; see
		// probeBootstrapServerDB for why a fresh clone must not pay 70s.
		ep := bootstrapServerEndpoint(beadsDir, cfg)
		if ep.port == 0 && ep.socket == "" {
			// No port is resolved, so no server is running to ask: there is
			// no 127.0.0.1:0, and no default port later commands dial.
			return unresolvedBootstrapDBPlan(plan, beadsDir, cfg, hasLocalShadowDir)
		}
		result := probeBootstrapServerDB(ep, hasLocalShadowDir)
		probe := serverDBProbe{Ran: true, Host: ep.host, Port: ep.port, Socket: ep.socket, Exists: result.Exists, Err: result.Err, LocalEvidence: hasLocalShadowDir}

		if result.Exists {
			plan.HasExisting = true
			plan.Action = "none"
			plan.Reason = fmt.Sprintf("Database %s already exists on server at %s", remedy.PrintedValue(cfg.GetDoltDatabase()), remedy.PrintedValue(ep.serverLocation()))
			return plan, probe, true
		}
		// An unverifiable probe means UNKNOWN, never "exists". Answering
		// "nothing to do" for it is only defensible with local evidence that
		// the database lives here — a populated shadow directory (in
		// locally-managed server mode; see the scope note above, it is not
		// per-database in shared-server mode), where a down server is a live
		// database we simply cannot see right now.
		// Without that evidence the very same error describes a fresh clone
		// whose server has not been started yet, which is the normal state of
		// every locally-managed server-mode clone before its first sync. Such
		// a clone must stay eligible for sync.remote, refs/dolt/data,
		// .beads/backup/ and issues.jsonl, so report "not found" and let
		// detectBootstrapAction consult them.
		//
		// When it does settle here, Blocked is still not hand-set:
		// detectBootstrapAction derives it from Action=="none" && !HasExisting
		// (HasExisting is false on this branch), keeping one owner for the flag
		// and the #5743 non-zero exit rather than a false success tick.
		if result.Err != nil && hasLocalShadowDir {
			plan.Action = "none"
			plan.Reason = fmt.Sprintf("Could not verify existing server database %s: %v", remedy.PrintedValue(cfg.GetDoltDatabase()), remedy.PrintedError(result.Err))
			return plan, probe, true
		}
		return BootstrapPlan{}, probe, false
	}

	dbPath := filepath.Join(beadsDir, "embeddeddolt")
	if info, err := os.Stat(dbPath); err != nil || !info.IsDir() {
		return BootstrapPlan{}, serverDBProbe{}, false
	}

	entries, _ := os.ReadDir(dbPath)
	if len(entries) == 0 {
		return BootstrapPlan{}, serverDBProbe{}, false
	}

	plan.HasExisting = true
	plan.Action = "none"
	plan.Reason = "Database already exists at " + dbPath
	return plan, serverDBProbe{}, true
}

// errNoOwnedServerDatabase is wrapped by unresolvedBootstrapDBPlan's probe
// error when the data directory of the server bd would start holds no
// database.
var errNoOwnedServerDatabase = errors.New("no Dolt server is running for this workspace, and its data directory holds no database")

// unresolvedBootstrapDBPlan is existingBootstrapDBPlan's verdict for a
// server-mode workspace whose endpoint resolves no port and no socket
// (bootstrapUnresolvedServer). When bd starts the workspace's own server on
// the next open, the database lives in that server's data directory, so a
// database there settles the plan as it does in embedded mode, and its
// absence is a probe that found nothing, which keeps the refusal for an
// already-initialized workspace (refuseServerModeInitPlan). Otherwise the
// probe could not run, and says why.
func unresolvedBootstrapDBPlan(plan BootstrapPlan, beadsDir string, cfg *configfile.Config, hasLocalShadowDir bool) (BootstrapPlan, serverDBProbe, bool) {
	ep := bootstrapEndpoint(beadsDir, cfg)
	probe := serverDBProbe{Ran: true, Host: ep.Host, LocalEvidence: hasLocalShadowDir}
	if err := bootstrapUnresolvedServer(ep); err != nil {
		probe.Err = err
		return BootstrapPlan{}, probe, false
	}
	dbDir := filepath.Join(bootstrapOwnedDataDir(beadsDir, cfg), cfg.GetDoltDatabase())
	if info, err := os.Stat(filepath.Join(dbDir, ".dolt")); err == nil && info.IsDir() {
		probe.Exists = true
		plan.HasExisting = true
		plan.Action = "none"
		plan.Reason = "Database already exists at " + remedy.PrintedValue(dbDir)
		return plan, probe, true
	}
	probe.Err = fmt.Errorf("%w at %s", errNoOwnedServerDatabase, remedy.PrintedValue(dbDir))
	return BootstrapPlan{}, probe, false
}

// probeBootstrapServerDB checks whether the configured database exists on
// the Dolt server at ep (bootstrapServerEndpoint, where the clone would land).
//
// When retryTransient is set, a reachable server that reports the database
// absent is retried with backoff, because a managed Dolt restart can
// transiently look like a missing database for up to ~30s. That budget costs
// 70s, so it is only worth paying when something independent says the
// database really is supposed to be here — in practice, a populated local
// shadow directory. A caller with no such evidence (a fresh clone, which by
// design has no shadow directory) is not watching a database disappear; it is
// simply ahead of its first sync, and must pass retryTransient=false so the
// answer costs one round-trip instead of 70s.
func probeBootstrapServerDB(ep bootstrapServerProbeConfig, retryTransient bool) bootstrapServerDBCheck {
	if !retryTransient {
		return checkBootstrapServerDB(ep)
	}
	retryDelays := []time.Duration{10 * time.Second, 20 * time.Second, 40 * time.Second}
	var result bootstrapServerDBCheck
	for attempt := 0; ; attempt++ {
		result = checkBootstrapServerDB(ep)
		if result.Err != nil || result.Exists || !result.Reachable {
			break
		}
		if attempt >= len(retryDelays) {
			break
		}
		fmt.Fprintf(os.Stderr, "Database %s not found on reachable server (attempt %d/%d), retrying in %v (possible transient restart)\n",
			remedy.PrintedValue(ep.database), attempt+1, len(retryDelays), retryDelays[attempt])
		bootstrapRetryDelay(retryDelays[attempt])
	}
	return result
}

func bootstrapSharedServerMode(beadsDir string) bool {
	if v := os.Getenv("BEADS_DOLT_SHARED_SERVER"); v == "1" || strings.EqualFold(v, "true") {
		return true
	}
	return strings.EqualFold(config.GetStringFromDir(beadsDir, "dolt.shared-server"), "true")
}

// bootstrapOwnedDataDir is the data directory bd's own server will serve
// for beadsDir once metadata.json holds cfg, which bootstrap clones into
// when no port is resolved and the next bd command starts that server
// (bootstrapUnresolvedServer): the rule doltserver.ResolveDoltDir applies to
// the metadata.json that finalizeSyncedBootstrap writes
// (configfile.Config.DatabasePath): BEADS_DOLT_DATA_DIR, else a relative
// dolt_data_dir, else an absolute database path, else beadsDir/dolt.
//
// An absolute dolt_data_dir is not part of it: metadata.json never stores
// one (configfile.Config.Save drops it), so the server never serves it. A
// fresh workspace's cfg can be an enclosing workspace's (findParentConfig),
// whose absolute dolt_data_dir is that workspace's data directory; cloning
// there would put the database among that workspace's and out of reach of
// this one's server.
func bootstrapOwnedDataDir(beadsDir string, cfg *configfile.Config) string {
	saved := *cfg
	if filepath.IsAbs(saved.DoltDataDir) {
		saved.DoltDataDir = ""
	}
	return saved.DatabasePath(beadsDir)
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
				//
				// sync.remote comes from the committed config.yaml, so it goes
				// into the command through the remedy printer: one shell word,
				// or a <url> placeholder with the URL named escaped beside it.
				url, note := remedy.CommandArg(runtime.GOOS, gitRemoteURLForLsRemote(plan.BlockedRemote), "<url>")
				fmt.Fprintf(os.Stderr, "  Reproduce: git ls-remote %s refs/dolt/data%s\n", url, note)
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
		fmt.Printf("  Remote: %s\n", remedy.PrintedValue(redactRemoteURL(plan.SyncRemote)))
		fmt.Printf("  Database: %s\n", remedy.PrintedValue(plan.Database))
	case "restore":
		fmt.Printf("Bootstrap plan: restore from backup\n")
		fmt.Printf("  Backup dir: %s\n", plan.BackupDir)
	case "jsonl-import":
		fmt.Printf("Bootstrap plan: import from git-tracked JSONL\n")
		fmt.Printf("  JSONL file: %s\n", plan.JSONLFile)
		fmt.Printf("  Database: %s\n", remedy.PrintedValue(plan.Database))
	case "init":
		fmt.Printf("Bootstrap plan: create fresh database\n")
		fmt.Printf("  Database: %s\n", remedy.PrintedValue(plan.Database))
	case "refuse":
		// Deliberately the one-line summary, not refusalDetail: the full
		// explanation is the error executeBootstrapPlan returns, and printing
		// both would render it twice.
		//
		// Deliberately stdout, unlike the "none"+Blocked arm above: this is the
		// plan the command was asked to compute, and the one-line Reason rides
		// with it (pinned by TestPrintBootstrapPlan_RefusePrintsSummaryNotDetail)
		// so a stdout-only consumer is never left with a bare "refuse". It is
		// not the #5743 false-success class either — a real refuse run exits
		// non-zero through executeBootstrapPlan, so nothing reads this as
		// success.
		fmt.Printf("Bootstrap plan: refuse — will NOT create a database\n")
		fmt.Printf("  Database: %s\n", remedy.PrintedValue(plan.Database))
		fmt.Printf("  Reason: %s\n", plan.Reason)
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
	// A refusal is a decision, not an action awaiting approval: surface it
	// before confirmPrompt so the operator is never asked to approve it, and
	// before the workspace gates below so no state is touched.
	if plan.Action == "refuse" {
		detail := plan.refusalDetail
		if detail == "" {
			detail = plan.Reason
		}
		return errors.New(detail)
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
	if err := refuseServerModeInitForExistingProject(plan, cfg); err != nil {
		return err
	}

	prefix := inferPrefix(cfg)

	s, heldAt, err := openBootstrapStore(ctx, plan, cfg)
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	if heldAt != "" {
		fmt.Fprintf(os.Stderr, "Database %s already exists on the Dolt server at %s; bd bootstrap did not create another\n",
			remedy.PrintedValue(cfg.GetDoltDatabase()), remedy.PrintedValue(heldAt))
		return nil
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

// refuseServerModeInitForExistingProject is the belt-and-braces half of the
// guard whose real decision is made at plan time by refuseServerModeInitPlan.
// It re-checks, at the last moment before a database would actually be
// created, that this workspace is not an already-initialized project
// (ProjectID set) whose server-mode database cannot be confirmed to exist —
// covering any caller that reaches executeInitAction with a plan that did not
// come from detectBootstrapAction.
//
// It deliberately probes ONCE. The transient-restart backoff belongs to the
// plan-time probe; repeating it here would charge a second 70s to a decision
// that has already been made, and refusing is the safe direction anyway.
func refuseServerModeInitForExistingProject(plan BootstrapPlan, cfg *configfile.Config) error {
	isSharedServer := bootstrapSharedServerMode(plan.BeadsDir)
	if (!cfg.IsDoltServerMode() && !isSharedServer) || cfg.ProjectID == "" {
		return nil
	}
	ep := bootstrapServerEndpoint(plan.BeadsDir, cfg)
	if ep.port == 0 && ep.socket == "" {
		// Nothing to dial (see unresolvedBootstrapDBPlan).
		_, probe, _ := unresolvedBootstrapDBPlan(plan, plan.BeadsDir, cfg, false)
		if probe.Exists {
			return nil
		}
		return bootstrapMissingServerDBRefusal(cfg.GetDoltDatabase(), probe.serverLocation(), probe.Err)
	}
	result := probeBootstrapServerDB(ep, false)
	if result.Exists {
		return nil
	}
	return bootstrapMissingServerDBRefusal(cfg.GetDoltDatabase(), ep.serverLocation(), result.Err)
}

// bootstrapMissingServerDBSummary is the single-line, unstyled form of
// bootstrapMissingServerDBRefusal, used as BootstrapPlan.Reason so --json
// consumers get the explanation without ANSI escapes.
func bootstrapMissingServerDBSummary(dbName, location string, probeErr error) string {
	if probeErr != nil {
		return fmt.Sprintf("Could not verify database %q on Dolt server at %s (%s); workspace already initialized (project_id set) — refusing to create an empty database", dbName, remedy.PrintedValue(location), strings.ReplaceAll(printedProbeError(probeErr).Error(), "\n", " "))
	}
	return fmt.Sprintf("Database %q not found on server at %s; workspace already initialized (project_id set) — refusing to create an empty database", dbName, remedy.PrintedValue(location))
}

// printedProbeError is a serverDBProbe's error as the refusal names it. A
// leaf error from the dial or the driver (checkBootstrapServerDB) can repeat
// a clone-supplied host on its one line, so it is spelled by
// remedy.PrintedError. An error bd composed with its values already spelled
// (Endpoint.UnresolvedError, whose advice spans lines, and
// errNoOwnedServerDatabase) keeps its line breaks: it is spelled by
// remedy.PrintedText instead.
func printedProbeError(err error) error {
	if errors.Is(err, doltserver.ErrUnresolvedPort) || errors.Is(err, errNoOwnedServerDatabase) {
		return remedy.PrintedTextError(err)
	}
	return remedy.PrintedError(err)
}

// bootstrapMissingServerDBRefusal builds the refusal error shown when bd
// bootstrap would otherwise create a fresh, empty server-mode database for a
// workspace that was already initialized elsewhere. Per GH#2363, this must
// never suggest a destructive "recreate" action as an actionable next step.
//
// The equivalent `bd init` refusal is initGuardMissingServerDBMessage, from
// PR #5791. This wording is kept deliberately close to it so the two can be
// folded into one message. location names the server
// (bootstrapServerProbeConfig.serverLocation): host:port, or the socket of a
// socket-only workspace, never host:0.
//
// It is an error's text, printed through bd's error printer, which spells any
// terminal escape in it (remedy.PrintedText), so the warning sign is not
// styled here. The probe error repeats the host, which metadata.json sets.
func bootstrapMissingServerDBRefusal(dbName, location string, probeErr error) error {
	var b strings.Builder
	if probeErr != nil {
		fmt.Fprintf(&b, "\n⚠ Could not verify database %q on Dolt server at %s: %v\n", dbName, remedy.PrintedValue(location), printedProbeError(probeErr))
	} else {
		fmt.Fprintf(&b, "\n⚠ Database %q not found on server at %s.\n", dbName, remedy.PrintedValue(location))
	}
	b.WriteString("This workspace was already initialized (metadata.json has a project_id from a prior\n")
	b.WriteString("bd init/bootstrap), so this looks like a recovery situation, not a fresh clone.\n")

	b.WriteString("\nbd bootstrap will NOT create an empty database here — that would strand any existing\n")
	b.WriteString("issue data behind a new, empty database of the same name.\n")

	b.WriteString("\nDiagnose with:\n")
	b.WriteString("  bd doctor          # check project health\n")
	b.WriteString("  bd dolt status     # inspect Dolt server state\n")

	// Every route here must be followable FROM this state. `bd backup restore`
	// alone is not: it restores a Dolt-native snapshot, not a `bd export`
	// JSONL, and it requires an initialized database — so it silently routes
	// the operator back through `bd init`, the door this refusal exists to
	// keep shut (PR #5791). Name that precondition instead
	// of implying it, and lead with the routes that need no init at all.
	b.WriteString("\nTo recover existing data, repopulate the database rather than creating fresh.\n")
	b.WriteString("Routes that work from this state:\n")
	b.WriteString("  bd dolt push                     # from another clone that still has the data\n")
	b.WriteString("  bd config set sync.remote <url>  # then re-run bd bootstrap to clone from it\n")
	b.WriteString("  bd init && bd backup restore     # ONLY with a Dolt-native backup snapshot:\n")
	b.WriteString("                                   # bd backup restore needs an initialized\n")
	b.WriteString("                                   # database and will not read a bd export JSONL\n")
	b.WriteString("\nbd bootstrap only auto-restores .beads/backup/issues.jsonl, so a timestamped or\n")
	b.WriteString("otherwise-named export under .beads/backup/ is still yours to import by hand.\n")

	b.WriteString("\nAborting.")
	return errors.New(b.String())
}

func executeRestoreAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	prefix := inferPrefix(cfg)

	storeCfg, err := bootstrapStoreConfig(ctx, plan.BeadsDir, cfg)
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	if storeCfg.ServerMode {
		return bootstrapServerRestoreRefusal(plan, cfg, storeCfg)
	}
	s, err := newDoltStore(ctx, storeCfg)
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

	s, heldAt, err := openBootstrapStore(ctx, plan, cfg)
	if err != nil {
		return fmt.Errorf("create database: %w", err)
	}
	if heldAt != "" {
		fmt.Fprintf(os.Stderr, "Database %s already exists on the Dolt server at %s; bd bootstrap did not import %s into it\n",
			remedy.PrintedValue(cfg.GetDoltDatabase()), remedy.PrintedValue(heldAt), remedy.PrintedValue(plan.JSONLFile))
		return nil
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

// bootstrapStoreConfig is the config of the store bd bootstrap's init,
// restore and jsonl-import actions open: beadsDir's database, created when it
// is missing, in the store every later command in beadsDir opens. Where the
// workspace's own metadata selects a Dolt server (bootstrapOpensServer), that
// is the workspace's server, reached as the CLI's own store reaches it
// (resolveDoltServerConnection: its host, the port doltserver.ResolveEndpoint
// resolves, its socket and credentials) under the CLI's auto-start policy
// (dolt.ApplyCLIAutoStart). Elsewhere it is the embedded store, as before.
func bootstrapStoreConfig(ctx context.Context, beadsDir string, cfg *configfile.Config) (*dolt.Config, error) {
	storeCfg := &dolt.Config{
		Path:            doltserver.ResolveDoltDir(beadsDir),
		Database:        cfg.GetDoltDatabase(),
		CreateIfMissing: true,
		AutoStart:       true,
		BeadsDir:        beadsDir,
	}
	if !bootstrapOpensServer(beadsDir) {
		return storeCfg, nil
	}
	storeCfg.ServerMode = true
	if err := resolveDoltServerConnection(ctx, beadsDir, cfg, storeCfg); err != nil {
		return nil, err
	}
	dolt.ApplyCLIAutoStart(beadsDir, storeCfg)
	return storeCfg, nil
}

// bootstrapOpensServer reports whether the store every later command in
// beadsDir opens is a plain Dolt server: whether beadsDir's own metadata.json
// selects one (IsDoltServerMode, the predicate workspace discovery and the
// bootstrap planner use). Shared-server mode named only in a config.yaml
// (dolt.shared-server) while metadata.json does not select server mode keeps
// the embedded actions: discovery does not count that mode, so later
// commands find the workspace only through the .beads/embeddeddolt those
// actions create. A workspace with no metadata.json of its own keeps the
// embedded store, as does a configuration bd bootstrap took from an
// enclosing workspace (findParentConfig): no later command in beadsDir reads
// it. A proxied-server workspace keeps the embedded path too; the factories
// refuse a plain server store there (errProxiedStoreUnrouted) before they
// consult server mode.
func bootstrapOpensServer(beadsDir string) bool {
	own, err := configfile.LoadForDiscovery(beadsDir)
	if err != nil || own == nil || own.IsDoltProxiedServerMode() {
		return false
	}
	return own.IsDoltServerMode()
}

// openBootstrapStore opens the store of bd bootstrap's init and jsonl-import
// actions (bootstrapStoreConfig). On a Dolt server it first asks whether the
// server already holds the database (bootstrapServerHolds); when it does, it
// opens nothing and returns where. The plan can settle on those actions with
// nothing on this machine tying that database to the workspace: a fresh clone
// matches it on the server by name alone, and a workspace with no project_id
// does not look for it there at all. bootstrap never writes a fresh database's
// prefix, or a tracked issues.jsonl, into a database the server already holds.
func openBootstrapStore(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) (s storage.DoltStorage, heldAt string, err error) {
	storeCfg, err := bootstrapStoreConfig(ctx, plan.BeadsDir, cfg)
	if err != nil {
		return nil, "", err
	}
	if storeCfg.ServerMode {
		location, held, err := bootstrapServerHolds(plan.BeadsDir, storeCfg)
		if err != nil {
			return nil, "", err
		}
		if held {
			return nil, location, nil
		}
		// bd bootstrap is a skip-store command, so PersistentPreRunE records
		// no version witness for it (trackBdVersion), and a server workspace
		// whose local Dolt root holds data and no witness is refused as a
		// legacy one by every later command, bd bootstrap included. The open
		// below populates that root when it starts bd's own server there,
		// and it does so first (dolt init), so an open that then fails or is
		// interrupted leaves the root populated too. The witness is recorded
		// before the open, as PersistentPreRunE records it before every other
		// command's; bootstrap's own legacy guard has admitted the workspace.
		recordCloneVersionWitness(plan.BeadsDir)
	}
	s, err = newDoltStore(ctx, storeCfg)
	return s, "", err
}

// bootstrapServerHolds reports whether the Dolt server storeCfg dials
// already holds storeCfg.Database, and names that server. It asks over the
// store's transport (dolt.ResolveSocketTransport), as the user and with the
// password the store presents (resolveDoltServerConnection filled them, a
// credential command's token included), so the answer is about the server and
// the database the action would write. A server that cannot be dialed is not
// known to hold it: the store's open then starts bd's own server or reports
// that server, as every later command's does. One that answers and cannot
// list its databases is an error, as it is for the store's open. With no
// address nothing is dialed, and under BEADS_TEST_MODE=1 neither is a
// production port (dolt.TestModeRefusesPort).
func bootstrapServerHolds(beadsDir string, storeCfg *dolt.Config) (location string, held bool, err error) {
	if storeCfg.ServerPort <= 0 && storeCfg.ServerSocket == "" {
		return "", false, nil
	}
	if err := dolt.TestModeRefusesPort(beadsDir, storeCfg.ServerPort); err != nil {
		return "", false, err
	}
	probe := bootstrapServerProbeConfig{
		socket:   dolt.ResolveSocketTransport(storeCfg.ServerSocket, storeCfg.ServerHost, storeCfg.ServerPort, 500*time.Millisecond),
		host:     storeCfg.ServerHost,
		port:     storeCfg.ServerPort,
		user:     storeCfg.ServerUser,
		pass:     storeCfg.ServerPassword,
		database: storeCfg.Database,
		tls:      storeCfg.ServerTLS,
	}
	location = probe.serverLocation()
	result := checkBootstrapServerDB(probe)
	if result.Reachable && result.Err != nil {
		return location, false, fmt.Errorf("could not check whether the Dolt server at %s holds database %s: %w",
			remedy.PrintedValue(location), remedy.PrintedValue(storeCfg.Database), remedy.PrintedError(result.Err))
	}
	return location, result.Exists, nil
}

// bootstrapServerRestoreRefusal is bd bootstrap's restore action in a
// workspace whose store is a Dolt server (bootstrapOpensServer), storeCfg
// being that store's config. The action creates the database and then
// restores the backup over it without --force, which Dolt refuses ("database
// already exists"), so on a server it would leave an empty database behind,
// where every later command, and every bootstrap rerun, would take it for the
// real one. It refuses before anything is created. When the server already
// holds the database (bootstrapServerHolds), it says so and names no
// --force restore, which would replace that database's current contents.
// Otherwise it names the server as later commands resolve it
// (bootstrapEndpoint) and the restore command to run once the database
// exists there.
func bootstrapServerRestoreRefusal(plan BootstrapPlan, cfg *configfile.Config, storeCfg *dolt.Config) error {
	heldAt, held, err := bootstrapServerHolds(plan.BeadsDir, storeCfg)
	if err != nil {
		return fmt.Errorf("restore from backup: %w", err)
	}
	if held {
		return fmt.Errorf("database %s already exists on the Dolt server at %s; bd bootstrap did not restore %s over it "+
			"(a 'bd backup restore --force' would replace the database's current contents with the backup's)",
			remedy.PrintedValue(cfg.GetDoltDatabase()), remedy.PrintedValue(heldAt), remedy.PrintedValue(plan.BackupDir))
	}
	location := bootstrapEndpoint(plan.BeadsDir, cfg).Location()
	dir, note := remedy.CommandArg(runtime.GOOS, plan.BackupDir, "<dir>")
	msg := fmt.Sprintf("bd bootstrap does not restore a backup into a Dolt server; nothing was created on the Dolt server at %s.\n"+
		"To restore the Dolt backup there, run 'bd backup restore --force %s'%s once database %s exists on that server "+
		"(see init-missing-server-db in docs/recovery/init-safety.md)",
		remedy.PrintedValue(location), dir, note, remedy.PrintedValue(cfg.GetDoltDatabase()))
	tracked := filepath.Join(plan.BeadsDir, "issues.jsonl")
	if _, err := os.Stat(tracked); err == nil {
		msg += fmt.Sprintf("; to import %s instead, move %s aside and re-run 'bd bootstrap'",
			remedy.PrintedValue(tracked), remedy.PrintedValue(plan.BackupDir))
	}
	return errors.New(msg)
}

func executeSyncAction(ctx context.Context, plan BootstrapPlan, cfg *configfile.Config) error {
	// Ensure .beads directory exists — it may not in the "fresh clone"
	// bootstrap path where we detected remote data before .beads was
	// created. Deferred here to preserve --dry-run semantics. (GH#2792)
	if err := os.MkdirAll(plan.BeadsDir, 0o750); err != nil {
		return fmt.Errorf("create beads directory: %w", err)
	}

	dbName := cfg.GetDoltDatabase()
	// Clone and finalize under the configuration the next command in
	// plan.BeadsDir loads (config.WithBeadsDir), as serverClonePort and the
	// warmup below do: the clone's mode (a server or the embedded engine),
	// the server it dials and the dolt_mode finalizeSyncedBootstrap records
	// are then the ones that command uses. Bootstrap can have started with an
	// enclosing workspace's config.yaml loaded, whose dolt.shared-server,
	// dolt.mode and dolt.host a fresh rig's later commands never read.
	var cloneErr error
	_ = config.WithBeadsDir(plan.BeadsDir, func() {
		if cloneErr = cloneFromRemote(ctx, plan.BeadsDir, plan.SyncRemote, dbName, cfg); cloneErr != nil {
			return
		}
		// Finalize the bootstrapped workspace so subsequent bd commands can
		// open the cloned database. Without metadata.json and config.yaml,
		// configfile.Load() returns nil, callers fall back to the default
		// dolt_database name, and bd loses track of the cloned database —
		// producing "no beads configuration found" and "Error 1105: no
		// database selected" on bd status / bd dolt push in fresh clones.
		// Every other bootstrap action (init, restore, jsonl-import) writes
		// these files via newDoltStore + createConfigYaml; the sync path
		// historically did not. (GH#3201)
		cloneErr = finalizeSyncedBootstrap(plan.BeadsDir, plan.SyncRemote, cfg, dbName)
	})
	if cloneErr != nil {
		return cloneErr
	}

	// Creation-consent for the migration-consent gate
	// (schema/migrate_consent.go): this sync action just CLONED the database
	// fresh, so the operator's explicit bootstrap is consent for its schema —
	// the same principle that exempts a fresh `bd init`. The #4259
	// remote-migrate gate below is unaffected (consent is not its
	// designated-migrator override), so the behind-remote refusal keeps its
	// exact bd-6dnrw.31 behavior.
	schema.SetLocalMigrateConsent(true)

	// Open and close the store to ensure dolt_ignore'd wisp tables are
	// created in the working set. Clone does not include these tables
	// (they are never committed), so they must be recreated after clone.
	// Both embedded and server mode handle this in their store init paths.
	if err := warmUpSyncedStore(ctx, plan); err != nil {
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
				remedy.PrintedValue(plan.SyncRemote), gateErr.Pending, unit, gateErr.CurrentVersion, gateErr.LatestVersion)
		}
		// A Dolt below bd's minimum (the server the clone landed on, or the
		// dolt binary the open would start one with) refuses every later
		// command the same way, so this is no missing-wisp-table warning:
		// report it rather than a usable workspace.
		if errors.Is(err, doltversion.ErrBelowRequiredMin) {
			return fmt.Errorf("open the database cloned from %s: %w", remedy.PrintedValue(plan.SyncRemote), err)
		}
		// Non-fatal: wisp tables will be created on the next command that
		// opens the store. Warn so the user knows to retry if they hit
		// "table not found: wisp_*" errors.
		fmt.Fprintf(os.Stderr, "Warning: post-clone store init failed (wisp tables may be missing): %v\n", err)
		return nil
	}

	return nil
}

// openSyncedWarmupStore opens the store warmUpSyncedStore warms up.
// Injectable for testing.
var openSyncedWarmupStore = newDoltStoreFromConfig

// warmUpSyncedStore opens and closes the cloned store, which creates its wisp
// tables, and wires origin to plan.SyncRemote. It opens under the
// configuration the next command in plan.BeadsDir loads (config.WithBeadsDir),
// the one serverClonePort cloned through: bootstrap can have started with an
// enclosing workspace's config.yaml loaded, whose dolt.port names another
// server.
func warmUpSyncedStore(ctx context.Context, plan BootstrapPlan) error {
	var err error
	_ = config.WithBeadsDir(plan.BeadsDir, func() {
		var s storage.DoltStorage
		if s, err = openSyncedWarmupStore(ctx, plan.BeadsDir); err != nil {
			return
		}
		configureInitDoltRemote(ctx, s, plan.SyncRemote, false)
		_ = s.Close()
	})
	return err
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
		remedy.PrintedValue(syncRemote), e.Pending, unit, e.CurrentVersion, e.LatestVersion)
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

// remoteCloneViaEmbedded, remoteCloneViaServer, remoteCloneIntoDataDir and
// remoteCloneViaCLI run the embedded-engine, the server, the owned
// data-directory and the dolt CLI clone cloneFromRemoteWithMode picks.
// Injectable for testing.
var (
	remoteCloneViaEmbedded = cloneViaEmbedded
	remoteCloneViaServer   = cloneViaServer
	remoteCloneIntoDataDir = cloneIntoOwnedDataDir
	remoteCloneViaCLI      = cloneViaCLI
)

func cloneFromRemoteWithMode(ctx context.Context, beadsDir, remoteURL, dbName string, cfg *configfile.Config, cloneMode remoteCloneMode) error {
	mode := resolveRemoteCloneMode(beadsDir, cfg, cloneMode)

	switch mode {
	case remoteCloneEmbedded:
		return remoteCloneViaEmbedded(ctx, beadsDir, remoteURL, dbName)

	case remoteCloneExternalServer:
		if cfg == nil {
			// Caller didn't provide config; fall back to loading from disk.
			if loaded, err := configfile.Load(beadsDir); err == nil && loaded != nil {
				cfg = loaded
			}
		}
		if cfg != nil {
			// bd bootstrap lets the workspace pick the mode and clones into
			// it, so it dials the endpoint every later command dials there.
			// bd init picks the mode itself and opens its own store with the
			// environment first, so its clone keeps that port order.
			if cloneMode == remoteCloneAuto {
				ep := bootstrapServerEndpoint(beadsDir, cfg)
				if ep.port == 0 && ep.socket == "" {
					// No port resolves, the store's external-host default
					// included (serverClonePort): no server publishes one.
					// When bd starts the workspace's own server on the next
					// open, clone into the data directory that server
					// serves; otherwise refuse, as every later open will,
					// rather than dial a default port no command dials.
					if err := bootstrapUnresolvedServer(bootstrapEndpoint(beadsDir, cfg)); err != nil {
						return err
					}
					if err := remoteCloneIntoDataDir(ctx, bootstrapOwnedDataDir(beadsDir, cfg), remoteURL, dbName); err != nil {
						return err
					}
					recordCloneVersionWitness(beadsDir)
					return nil
				}
				return remoteCloneViaServer(ctx, remoteURL, dbName, ep)
			}
			ep, err := initServerCloneEndpoint(beadsDir, cfg)
			if err != nil {
				return err
			}
			return remoteCloneViaServer(ctx, remoteURL, dbName, ep)
		}
		// No config available — fall through to CLI clone.
		fmt.Fprintf(os.Stderr, "Warning: server mode detected but no config available, falling back to CLI clone\n")
		return remoteCloneViaCLI(ctx, beadsDir, remoteURL, dbName)

	default:
		return remoteCloneViaCLI(ctx, beadsDir, remoteURL, dbName)
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

// cloneViaServer clones by connecting to the external Dolt server at ep and
// executing CALL DOLT_CLONE. The server places the database in its own
// data directory, which is the correct behavior for externally managed
// servers where bd does not know the filesystem layout.
func cloneViaServer(ctx context.Context, remoteURL, dbName string, ep bootstrapServerProbeConfig) error {
	db, err := sql.Open("mysql", ep.dsn())
	if err != nil {
		return fmt.Errorf("connect to dolt server for clone: %w", err)
	}
	defer db.Close()

	cloneCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	if err := db.PingContext(cloneCtx); err != nil {
		return fmt.Errorf("dolt server unreachable at %s (is dolt sql-server running?): %w",
			remedy.PrintedValue(ep.serverLocation()), remedy.PrintedError(err))
	}

	// Refuse a server older than bd's minimum before DOLT_CLONE writes a
	// database to it that every later open would refuse, as the store's own
	// open checks before it reads or writes (dolt.requireServerDoltVersion).
	subject := "dolt sql-server at " + remedy.PrintedValue(ep.serverLocation())
	if err := doltversion.CheckServer(cloneCtx, db, subject, func(err error) {
		debug.Logf("bootstrap: could not determine the version of %s; not checking it against the required minimum %s: %v\n",
			subject, doltversion.RequiredMin, remedy.PrintedError(err))
	}); err != nil {
		return err
	}

	if err := versioncontrolops.DoltClone(cloneCtx, db, remoteURL, dbName, os.Getenv("DOLT_REMOTE_USER")); err != nil {
		return fmt.Errorf("clone from remote via server: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Synced database from %s (via server at %s)\n",
		remedy.PrintedValue(remoteURL), remedy.PrintedValue(ep.serverLocation()))
	return nil
}

// bootstrapServerEndpoint is the endpoint bd bootstrap dials on an external
// Dolt server, both to probe for the database and to clone it: serverClonePort's
// port, and the host, socket, user and password the store pairs with it. Like
// the port, they are read under the configuration a later command in beadsDir
// loads (config.WithBeadsDir): for a fresh rig, an enclosing workspace's
// config.yaml dolt.host picks neither the host nor, through it, the
// credentials-file entry the password comes from.
func bootstrapServerEndpoint(beadsDir string, cfg *configfile.Config) bootstrapServerProbeConfig {
	port := serverClonePort(beadsDir, cfg)
	var ep bootstrapServerProbeConfig
	_ = config.WithBeadsDir(beadsDir, func() { ep = serverDialEndpoint(cfg, port) })
	return ep
}

// bootstrapEndpoint is the endpoint later commands in beadsDir resolve for
// cfg (doltserver.ResolveEndpoint), read under the configuration they load
// (config.WithBeadsDir), as bootstrapServerEndpoint reads its fields. Bootstrap
// asks it only when no port is resolved: whether bd starts the workspace's
// own server on the next open (Endpoint.BDStartsServer), and what to say
// when it does not (bootstrapUnresolvedServer).
func bootstrapEndpoint(beadsDir string, cfg *configfile.Config) doltserver.Endpoint {
	var ep doltserver.Endpoint
	// A config file that does not load fails the same way for later
	// commands, which run with what did load.
	_ = config.WithBeadsDir(beadsDir, func() { ep = doltserver.ResolveEndpoint(beadsDir, cfg) })
	return ep
}

// bootstrapUnresolvedServer explains a server-mode workspace whose endpoint
// resolves no port: the error to refuse with, or nil when bd starts the
// workspace's own server on the next open (ep.BDStartsServer), whose data
// directory then holds the database. That server is not running, or it
// would have published its port, so there is nothing to dial; bootstrap
// works on its data directory instead, as the store's auto-start would.
func bootstrapUnresolvedServer(ep doltserver.Endpoint) error {
	if ep.Dialable() || ep.BDStartsServer() {
		return nil
	}
	return ep.UnresolvedError()
}

// serverDialEndpoint is the endpoint cfg names for its server on port, under
// the configuration loaded now. The password is the one for that host and
// port (GetDoltServerPasswordForPort), as the store looks it up.
func serverDialEndpoint(cfg *configfile.Config, port int) bootstrapServerProbeConfig {
	return bootstrapServerProbeConfig{
		socket:   cfg.GetDoltServerSocket(),
		host:     cfg.GetDoltServerHost(),
		port:     port,
		user:     cfg.GetDoltServerUser(),
		pass:     cfg.GetDoltServerPasswordForPort(port),
		database: cfg.GetDoltDatabase(),
		tls:      cfg.GetDoltServerTLS(),
	}
}

// serverClonePort is the port bd bootstrap dials on an external Dolt server,
// both to probe for the database and to clone it: the port the store dials in
// this workspace once metadata.json holds cfg, so the clone lands where every
// later command looks for it and the probe looks there too. That is
// doltserver.DialConfig's port, by the store's rule (#5835, #7767):
// BEADS_DOLT_SERVER_PORT first, and the legacy BEADS_DOLT_PORT only where no
// listener.port, dolt.port or metadata.json dolt_server_port names one.
//
// DialConfig runs under the configuration a later command in beadsDir loads
// (config.WithBeadsDir): beadsDir's own config.yaml, which
// finalizeSyncedBootstrap writes after a fresh clone and which names no port,
// over the user config files, with the BD_* variables on top. Bootstrap can
// have started with another file loaded as the project config: for a fresh
// rig or clone with no .beads yet, whose own git repository bounds discovery
// (GH#3029, GH#2792), an enclosing workspace's config.yaml, or for a
// repository under HOME the legacy ~/.beads/config.yaml, which later commands
// read only as the lowest user layer.
//
// A fresh clone has no metadata.json yet: cfg comes from a parent workspace
// or the defaults, and finalizeSyncedBootstrap writes it after the clone.
// DialConfig cannot see it, so cfg's dolt_server_port takes the rank the
// file's will have, and cfg's dolt_server_host gets the store's external-host
// default: an externally managed server (BEADS_DOLT_SERVER_MODE=1) at a host
// bd does not run, with no port named, is dialed at 3307 once the file names
// that host. A shared server reads no port from the workspace's file.
//
// Otherwise, when nothing names a port it is 0, never the default 3307:
// later commands dial no such port. They start bd's own server on a port of
// its choosing, or refuse until the port is pinned (bootstrapUnresolvedServer).
func serverClonePort(beadsDir string, cfg *configfile.Config) int {
	var dial *doltserver.Config
	// A config file that does not load fails the same way for later
	// commands, which run with what did load.
	_ = config.WithBeadsDir(beadsDir, func() { dial = doltserver.DialConfig(beadsDir) })
	if cfg != nil && cfg.DoltServerPort > 0 && !dial.PortSharedServer && !outranksMetadataPort(dial) {
		if _, err := os.Stat(configfile.ConfigPath(beadsDir)); errors.Is(err, os.ErrNotExist) {
			return cfg.DoltServerPort
		}
	}
	if cfg != nil && dial.Port == 0 && dial.Mode == doltserver.ServerModeExternal {
		if _, err := os.Stat(configfile.ConfigPath(beadsDir)); errors.Is(err, os.ErrNotExist) {
			var host string
			_ = config.WithBeadsDir(beadsDir, func() { host = cfg.GetDoltServerHost() })
			if !doltserver.HostRunsBDServer(host) {
				return configfile.DefaultDoltServerPort
			}
		}
	}
	return dial.Port
}

// outranksMetadataPort reports whether DialConfig took its port from a source
// the store ranks above metadata.json's dolt_server_port. DialConfig reports
// the legacy BEADS_DOLT_PORT as PortSourceEnv too, but it ranks below.
func outranksMetadataPort(dial *doltserver.Config) bool {
	switch dial.PortSource {
	case doltserver.PortSourceEnv:
		return os.Getenv("BEADS_DOLT_SERVER_PORT") != ""
	case doltserver.PortSourcePortFile, doltserver.PortSourceDoltConfigYaml,
		doltserver.PortSourceConfigYaml, doltserver.PortSourceMetadataJSON:
		return true
	}
	return false
}

// initServerClonePort is the port bd init --server --external clones through:
// the port of the store init opens right after the clone, so init's own reads
// and writes find the clone. That store starts from --server-port, or else
// initStoreServerPort (BEADS_DOLT_SERVER_PORT, the port file for a host bd
// runs its server at, the shared default in shared-server mode, and for any
// other host the port every later command in the new workspace resolves under
// that workspace's own configuration; never the dolt.port of the
// configuration init loaded), and the store applies its rule to that
// (dolt.ResolveServerPort): the legacy BEADS_DOLT_PORT replaces a port bd
// chose, and DefaultConfig fills in a port nothing named unless
// initStoreServerPort resolved it.
// When nothing names one it is 0, never 3307: the store opens no such port
// either, and refuses the unresolved port instead (initServerCloneEndpoint).
func initServerClonePort(beadsDir string, cfg *configfile.Config) int {
	port, _ := initServerClonePortAndSource(beadsDir, cfg)
	return port
}

// initServerClonePortAndSource is initServerClonePort with the source the
// store records for that port.
func initServerClonePortAndSource(beadsDir string, cfg *configfile.Config) (int, doltserver.PortSource) {
	host := configfile.DefaultDoltServerHost
	if cfg != nil {
		host = cfg.GetDoltServerHost()
	}
	storeCfg := &dolt.Config{BeadsDir: beadsDir}
	if cfg != nil && cfg.DoltServerPort > 0 {
		storeCfg.ServerPort, storeCfg.ServerPortSource = cfg.DoltServerPort, doltserver.PortSourceCallerExplicit
	} else {
		storeCfg.ServerPort, storeCfg.ServerPortSource, storeCfg.ServerPortSharedServer, storeCfg.ServerPortResolved = initStoreServerPort(beadsDir, host)
	}
	dolt.ResolveServerPort(storeCfg)
	return storeCfg.ServerPort, storeCfg.ServerPortSource
}

// initServerCloneEndpoint is the endpoint bd init clones into when it clones
// through a server: cfg's on initServerClonePort's port, over the transport
// init's store dials there (initCloneSocketTransport): TCP at that port when
// cfg's unix socket does not answer and TCP does, as the store falls back to
// it (a DSN that names the socket dials it alone). With neither a port
// nor a socket there is nothing to dial, and the error wraps
// dolt.ErrUnresolvedServerPort, as init's store open would refuse. Under
// BEADS_TEST_MODE=1 a production port is refused too, before anything is
// probed, as New's test-mode guard would never have let the store dial it
// (dolt.TestModeRefusesPort).
func initServerCloneEndpoint(beadsDir string, cfg *configfile.Config) (bootstrapServerProbeConfig, error) {
	port := initServerClonePort(beadsDir, cfg)
	ep := serverDialEndpoint(cfg, port)
	if port == 0 && ep.socket == "" {
		return ep, fmt.Errorf("%w for the Dolt server at %s to clone into", dolt.ErrUnresolvedServerPort, remedy.PrintedValue(ep.host))
	}
	if err := dolt.TestModeRefusesPort(beadsDir, port); err != nil {
		return ep, err
	}
	ep.socket = initCloneSocketTransport(ep.socket, ep.host, ep.port)
	return ep, nil
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
		fmt.Fprintf(os.Stderr, "Synced database from %s\n", remedy.PrintedValue(remoteURL))
	}
	return nil
}

// recordCloneVersionWitness records this binary in beadsDir's version
// witness (.local_version) when there is none, where bootstrap writes the
// workspace's local Dolt root: after the sync action cloned into it, and
// before the init and jsonl-import actions open a server store, which
// populates it when it starts bd's own server there (openBootstrapStore). A
// server workspace whose .beads/dolt holds data and no witness is refused as
// a legacy (pre-1.0) one by every later command
// (guardLegacyUpgradeWorkspace); this binary writes that root, after the
// guard admitted the workspace, so the witness says what is true. An
// existing witness is left alone: it is the one-shot upgrade signal
// (trackBdVersion).
func recordCloneVersionWitness(beadsDir string) {
	path := filepath.Join(beadsDir, localVersionFile)
	if _, err := os.Stat(path); err == nil {
		return
	}
	_ = writeLocalVersion(path, Version) // best effort, as trackBdVersion
}

// cloneIntoOwnedDataDir clones with the dolt CLI into doltDir, the data
// directory bd's own server will serve for this workspace once a command
// starts it (bootstrapUnresolvedServer). It fails when the clone is skipped
// because doltDir already holds a Dolt database: bootstrap planned a clone
// because none was found, so a skip would report a sync that never happened.
func cloneIntoOwnedDataDir(ctx context.Context, doltDir, remoteURL, dbName string) error {
	synced, err := dolt.BootstrapFromRemoteWithDB(ctx, doltDir, remoteURL, dbName)
	if err != nil {
		return fmt.Errorf("sync from remote: %w", err)
	}
	if !synced {
		return fmt.Errorf("did not clone %s: the Dolt data directory %s already holds a Dolt database; "+
			"bd's server for this workspace serves that directory, so run bd commands against it, or move it aside and re-run bd bootstrap",
			remedy.PrintedValue(remoteURL), remedy.PrintedValue(doltDir))
	}
	fmt.Fprintf(os.Stderr, "Synced database from %s (into %s)\n", remedy.PrintedValue(remoteURL), remedy.PrintedValue(doltDir))
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
// An ancestor holding the graph format marker without metadata it can read is
// a graph workspace, refused like one whose metadata declares graph_mode,
// rather than skipped or reported as a metadata load failure.
//
// The walk ends at the home directory: $HOME, or the account home
// (beads.IsAccountHomeDir) when $HOME points elsewhere. The account home's
// .beads is the user-global directory that workspace discovery passes over
// (GH#4402), not an enclosing workspace, so it is not examined either, and a
// project under the home directory does not inherit its database name or
// server. Only when the home directory is itself a repository root (a
// dotfiles repository) is its .beads that repository's own, and examined.
func findParentConfig(beadsDir string) (*configfile.Config, error) {
	// Start from the parent of beadsDir's enclosing directory.
	// beadsDir is typically "<project>/.beads", so we start from <project>'s parent.
	start := utils.CanonicalizePath(filepath.Dir(filepath.Dir(beadsDir)))
	// Canonicalize the $HOME boundary only when there is one. An unset HOME
	// makes os.UserHomeDir fail, and CanonicalizePath("") resolves to the
	// current working directory, which would silently turn "don't search above
	// $HOME" into "stop at the CWD". Keep "" meaning "no boundary".
	homeDir := ""
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		homeDir = utils.CanonicalizePath(home)
	}
	origin := utils.CanonicalizePath(filepath.Dir(beadsDir))
	walk := beads.NewAncestorDirWalk(start, origin)
	for dir, ok := walk.Next(); ok; dir, ok = walk.Next() {
		// GH#4402: the user-global ~/.beads is no parent workspace; the walk
		// ends without examining it.
		atAccountHome := beads.IsAccountHomeDir(dir)
		if atAccountHome && !isGitWorkTreeRoot(dir) {
			break
		}
		candidate := filepath.Join(dir, ".beads")
		cfg, err := configfile.LoadForDiscovery(candidate)
		if (err != nil || cfg == nil) && hasGraphFormatMarker(candidate) {
			return nil, &configfile.UnsupportedWorkspaceFormatError{BeadsDir: candidate, FormatMarker: true}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", configfile.ConfigPath(candidate), err)
		}
		if cfg != nil {
			// The ancestor is not the workspace admission checked, so it is
			// classified from its own config (admitForeignWorkspace), and its
			// refusal names it.
			if _, err := admitForeignWorkspace(candidate); err != nil {
				return nil, fmt.Errorf("ancestor workspace %s: %w", candidate, err)
			}
			return cfg, nil
		}

		// Don't search above the home directory
		if atAccountHome || (homeDir != "" && dir == homeDir) {
			break
		}

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
//
// The host part is cut out before userinfo is stripped through its last "@",
// so an "@" in a path, query or fragment never selects the host (mc-gcmer).
// For a scheme URL the host part is the authority, which ends at the first
// "/", "?" or "#" — the host net/url, and so Dolt, would dial. For SCP it is
// everything before the first ":", unless a "/" comes first, which makes it a
// local path (the split doltremote.FromGitURL makes).
func urlHostname(rawURL string) string {
	if sep := strings.Index(rawURL, "://"); sep >= 0 {
		rest := rawURL[sep+3:]
		if end := strings.IndexAny(rest, "/?#"); end >= 0 {
			rest = rest[:end]
		}
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			rest = rest[at+1:]
		}
		if colon := strings.Index(rest, ":"); colon >= 0 {
			rest = rest[:colon]
		}
		return strings.ToLower(rest)
	}
	// SCP-style: git@github.com:org/repo
	if colon := strings.Index(rawURL, ":"); colon >= 0 && !strings.Contains(rawURL[:colon], "/") {
		if at := strings.LastIndex(rawURL[:colon], "@"); at >= 0 {
			return strings.ToLower(rawURL[at+1 : colon])
		}
	}
	return ""
}
