package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/routing"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/embeddeddolt"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

// openMigrationPlanningStore opens the personal planning workspace's store for
// the copy phase below.
//
// A planning repo that has a .beads directory but no metadata.json is first
// given one (initializePlanningRepo). Contributor setup leaves exactly that
// shape: runContributorWizard creates an empty .beads, and
// autoConfigureForkContributor a database with no metadata.json and no
// issue_prefix. migrate-personal's own error text sends users to those setups,
// so their repos must be migratable into. It is initialized by the rule a
// routed bd create applies (ensureBeadsDirForPath, with the project store
// supplying the prefix): an empty .beads gets a database named after the
// prefix, and one that already holds a database keeps it
// (adoptPlanningDatabase), since a new database beside it would hide whatever
// it holds. A repo that already has a metadata.json (a clone not yet
// bootstrapped, say) is left as it is.
//
// The shapes that rule refuses are refused before anything is written
// (refuseUninitializablePlanningRepo, refuseUninitializableTarget): a planning
// repo whose own config selects Dolt server mode, whose server data an
// embedded metadata.json would hide; any workspace bd's own command admission
// refuses there (guardLegacyUpgradeWorkspace), such as a SQLite-era repo,
// whose data a fresh database beside it would hide; and one holding several
// databases, where nothing records which one bd uses. A redirect stub is
// refused whether or not it has a metadata.json of its own: bd run in the
// planning repo follows the redirect, so it never reads a database inside the
// stub.
//
// The planning repo is the contributor auto-route target, so it then opens the
// way a write auto-routed there does (openAutoRoutedStore): its own
// metadata.json picks the backend, the mode and the database, and the database
// is pinned against a launching workspace's BEADS_DOLT_SERVER_DATABASE
// (autoRouteTargetDatabase, openRoutedTargetStore). A metadata.json that names
// no dolt_database, or an embedded planning workspace with no database, is
// refused, not given one. A bare dolt.New here used to dial a SQL server for
// every planning workspace, under the default database name.
//
// When the migration commits its writes in the planning store
// (commitsPlanningWrites), that store's working set must hold nothing else: an
// embedded commit is DOLT_COMMIT('-Am'), which would publish anything already
// uncommitted there under the migration's message. The initialization above
// is committed first, in a commit of its own (settlePlanningWorkingSet), and a
// working set that still holds changes is refused
// (refuseUncommittedPlanningChanges), before anything is copied.
//
// The store it returns is WRITTEN THROUGH: the migration creates every issue in
// the planning workspace and then deletes them from the project. Those are
// ordinary bead mutations in another workspace, so they belong in that
// workspace's events journal if it has one enabled — and the setting is read
// from there, not from the workspace bd was launched in. The same goes for its
// issue-version history, which the store's own settings row decides. The
// routed-write factory (newExistingDoltStoreFromConfig) activates the planning
// workspace's events journal and issue-version history on the store it opens.
func openMigrationPlanningStore(ctx context.Context, planningBeadsDir string, projectStore storage.DoltStorage, actor string) (storage.DoltStorage, error) {
	if err := refuseUninitializablePlanningRepo(planningBeadsDir); err != nil {
		return nil, err
	}
	initialized, err := initializePlanningRepo(ctx, planningBeadsDir, projectStore)
	if err != nil {
		return nil, fmt.Errorf("initialize planning repo: %w", err)
	}
	database, err := autoRouteTargetDatabase(planningBeadsDir, true)
	if err != nil {
		return nil, err
	}
	planningStore, err := openRoutedTargetStore(ctx, planningBeadsDir, database, true)
	if err != nil {
		return nil, err
	}
	if err := settlePlanningWorkingSet(ctx, planningStore, filepath.Dir(planningBeadsDir), initialized, actor); err != nil {
		_ = planningStore.Close()
		return nil, err
	}
	return planningStore, nil
}

// settlePlanningWorkingSet readies the opened planning store for the copy. The
// initialization this run wrote, when initializePlanningRepo reports one, is
// committed first under a message of its own (commitPlanningWrites): the
// database is new, or adoptPlanningDatabase found its working set clean, so the
// commit takes nothing else, and a refusal below or in phase 0 leaves no
// change of this run for the planning repo's next commit to publish. A working
// set that still holds changes is then refused
// (refuseUncommittedPlanningChanges).
func settlePlanningWorkingSet(ctx context.Context, planningStore storage.DoltStorage, planningRepo string, initialized bool, actor string) error {
	if initialized {
		message := formatDoltAutoCommitMessage("migrate-personal: initialize planning repo", actor, nil)
		if err := commitPlanningWrites(ctx, planningStore, message); err != nil {
			return fmt.Errorf("failed to commit the planning repo's initialization: %w", err)
		}
	}
	return refuseUncommittedPlanningChanges(ctx, planningStore, planningRepo, "")
}

// refuseUncommittedPlanningChanges refuses a planning store whose working set
// already holds changes when the migration commits its writes there
// (commitsPlanningWrites). An embedded commit is DOLT_COMMIT('-Am'): it would
// publish them under the migration's message and actor, and bd dolt push would
// carry them, whether they are writes the planning repo's own dolt.auto-commit
// batch or off deferred or changes the user means to discard. Dolt-ignored
// tables, such as wisps, are not in the working-set status, and the commit
// leaves them out too. It writes nothing.
//
// The remedy it names is bd dolt commit in the planning repo. unnamedDatabase
// is set when the store is a database no metadata.json names yet and that is
// not the default database (adoptPlanningDatabase): bd run in the planning
// repo then opens the default database, so bd dolt commit reaches this one
// only once a metadata.json names it, and the remedy says so.
func refuseUncommittedPlanningChanges(ctx context.Context, planningStore storage.DoltStorage, planningRepo, unnamedDatabase string) error {
	commits, err := commitsPlanningWrites(planningStore)
	if err != nil || !commits {
		return err
	}
	tables, err := uncommittedTables(ctx, planningStore)
	if err != nil {
		return fmt.Errorf("failed to read the planning repo's uncommitted changes: %w", err)
	}
	if len(tables) == 0 {
		return nil
	}
	remedy := "commit them there with `bd dolt commit`, then rerun"
	if unnamedDatabase != "" {
		remedy = fmt.Sprintf("bd run there opens the database %[1]q only once a metadata.json names it, so give it a .beads/metadata.json whose dolt_database is %[1]q, commit them there with `bd dolt commit`, then rerun", unnamedDatabase)
	}
	return refusePlanningRepo("planning repo %s has uncommitted changes (%s), which migrate-personal's commit would publish; %s; nothing was moved", planningRepo, strings.Join(tables, ", "), remedy)
}

// uncommittedTables lists the tables with staged or unstaged changes in st's
// working set, sorted, each once. Dolt-ignored tables are not in it.
func uncommittedTables(ctx context.Context, st storage.DoltStorage) ([]string, error) {
	status, err := st.Status(ctx)
	if err != nil {
		return nil, err
	}
	var tables []string
	for _, entry := range slices.Concat(status.Staged, status.Unstaged) {
		tables = append(tables, entry.Table)
	}
	slices.Sort(tables)
	return slices.Compact(tables), nil
}

// refuseUninitializablePlanningRepo refuses a planning .beads that
// openMigrationPlanningStore must not open or initialize: a redirect stub, with
// or without a metadata.json of its own, and, when it has no metadata.json,
// one a routed bd create would not initialize either (refuseUninitializableTarget:
// its own config selects Dolt server mode, bd's own admission refuses it, or
// it holds several databases). Every other metadata-less .beads is left to
// initializePlanningRepo. It writes nothing. A .beads that has a metadata.json
// is left to the open, which refuses what it cannot use. A targetRefusal is
// raised as a planningRepoRefusal naming the planning repo; bd's admission
// refusals are returned as they are, for planningOpenError to render.
func refuseUninitializablePlanningRepo(planningBeadsDir string) error {
	if redirect := beads.ResolveRedirect(planningBeadsDir); utils.CanonicalizePath(redirect.TargetDir) != utils.CanonicalizePath(planningBeadsDir) {
		return redirectedPlanningRepoError(planningBeadsDir, redirect.TargetDir, redirect.SourceDatabase)
	}
	if _, err := os.Stat(filepath.Join(planningBeadsDir, "metadata.json")); err == nil {
		return nil
	}
	err := refuseUninitializableTarget(planningBeadsDir)
	var refusal *targetRefusal
	if errors.As(err, &refusal) {
		// Its message starts with the planning repo's path.
		return refusePlanningRepo("planning repo %s", refusal.msg)
	}
	return err
}

// redirectedPlanningRepoError refuses a planning .beads that redirects to
// target, a .beads other than the project's (refuseSelfPlanningRepo refuses
// that one). bd run in the planning repo opens target under stubDatabase, the
// dolt_database the stub's own metadata.json names, when it names one
// (preserveRedirectSourceDatabase), while a migration into target opens
// target's own (autoRouteTargetDatabase). The refusal therefore points
// routing.contributor at target only when the two agree; otherwise it names
// the database bd uses there, which routing.contributor cannot select
// (stubDatabaseRefusal).
func redirectedPlanningRepoError(planningBeadsDir, target, stubDatabase string) error {
	planningRepo, targetRepo := filepath.Dir(planningBeadsDir), filepath.Dir(target)
	if stubDatabase != "" {
		if targetDatabase, err := autoRouteTargetDatabase(target, true); err != nil || targetDatabase != stubDatabase {
			return stubDatabaseRefusal(planningRepo, targetRepo, stubDatabase)
		}
	}
	return refusePlanningRepo("planning repo %s redirects to %s; set routing.contributor to that repository instead", planningRepo, targetRepo)
}

// stubDatabaseRefusal refuses planningRepo, a redirect stub to targetRepo that
// names stubDatabase, a database other than targetRepo's own: bd run in the
// planning repo uses stubDatabase there, and routing.contributor, set to
// targetRepo, selects targetRepo's own instead.
func stubDatabaseRefusal(planningRepo, targetRepo, stubDatabase string) error {
	return refusePlanningRepo("planning repo %s redirects to %s, where bd uses the database %q that the planning repo names; routing.contributor cannot select that database (set to %s, it selects that repository's own); set routing.contributor to a separate planning repository (e.g. ~/.beads-planning)", planningRepo, targetRepo, stubDatabase, targetRepo)
}

// initializePlanningRepo gives a planning .beads that refuseUninitializablePlanningRepo
// admitted a metadata.json when it has none, and reports whether it had none.
// One that already holds a database keeps it (adoptPlanningDatabase); an empty
// one is initialized the way a routed bd create initializes it
// (ensureBeadsDirForPath), which leaves a .beads that has a metadata.json as it
// is.
func initializePlanningRepo(ctx context.Context, planningBeadsDir string, projectStore storage.DoltStorage) (bool, error) {
	_, statErr := os.Stat(filepath.Join(planningBeadsDir, "metadata.json"))
	if statErr != nil {
		if databases := embeddeddolt.Databases(planningBeadsDir); len(databases) == 1 {
			return true, adoptPlanningDatabase(ctx, planningBeadsDir, databases[0], projectStore)
		}
	}
	return statErr != nil, ensureBeadsDirForPath(ctx, filepath.Dir(planningBeadsDir), projectStore, true)
}

// adoptPlanningDatabase adopts the one embedded database a planning .beads
// with no metadata.json holds, as a routed bd create does (adoptTargetDatabase),
// so the issues it already holds stay visible, to bd run in the planning repo
// and to the migration's own phase 0. A database whose working set already
// holds changes is refused first, with nothing written
// (refuseUncommittedPlanningChanges): the initialization commit
// (settlePlanningWorkingSet) would publish them.
func adoptPlanningDatabase(ctx context.Context, planningBeadsDir, database string, projectStore storage.DoltStorage) error {
	var unnamed string
	if database != configfile.DefaultDoltDatabase {
		unnamed = database
	}
	return adoptTargetDatabase(ctx, planningBeadsDir, database, projectStore, func(planningStore storage.DoltStorage) error {
		return refuseUncommittedPlanningChanges(ctx, planningStore, filepath.Dir(planningBeadsDir), unnamed)
	})
}

// refuseSelfPlanningRepo refuses a planning repo that is the project workspace
// itself (routing.contributor set to the project's path, or to ".", the value
// routing.maintainer and routing.default default to), or a clone of it. The
// migration copies each issue into the planning store and then deletes it from
// the project; when both are the same database the copy is a no-op and the
// delete removes the only copy.
//
// The planning .beads directory (after any redirect) is compared with the
// project's. A planning repo in the same directory is refused whatever
// database it uses there: none of them is a separate planning repository, and
// in embedded mode the planning open would hand back this process's own store
// for that directory (embeddeddolt.Open keeps one per directory). The refusal
// names the database the planning repo uses: the one a redirect stub names
// (beads.ResolveRedirect), else the directory's own dolt_database, which a
// migration opens (autoRouteTargetDatabase), or the default database bd uses
// there when the directory names none. The database the project's store
// uses is the project's own workspace. A stub naming another database is a rig
// beside the project under a shared root, refused as the redirect it is
// (stubDatabaseRefusal). The directory's own database, seen from a rig under
// that shared root, is refused as that database. Each refusal points
// routing.contributor at a separate planning repository, never at the shared
// root, which this check would refuse in turn.
// Two directories whose metadata.json carry the same project_id are one
// project's workspaces, a clone sharing the git-tracked metadata.json: a
// separate planning repository has an identity of its own.
func refuseSelfPlanningRepo(planningPath, planningBeadsDir, projectBeadsDir string) error {
	if projectBeadsDir == "" {
		return nil
	}
	redirect := beads.ResolveRedirect(planningBeadsDir)
	planning := utils.CanonicalizePath(redirect.TargetDir)
	project := utils.CanonicalizePath(beads.FollowRedirect(projectBeadsDir))
	// LoadForDiscovery, not Load: Load migrates a legacy config.json to
	// metadata.json, and this check runs before the confirmation prompt and
	// before any format admission, so it must not rewrite either workspace.
	projectCfg, qerr := configfile.LoadForDiscovery(project)
	if planning == project {
		// The database the project's store opened: BEADS_DOLT_SERVER_DATABASE,
		// which preserveRedirectSourceDatabase sets for a launch through a
		// redirect, outranks the shared metadata.json (GetDoltDatabase).
		// sharedDatabase is the database bd uses in the directory itself, its
		// own dolt_database or, when that is empty, the default; the planning
		// repo uses it unless it is a stub naming one of its own.
		projectDatabase, sharedDatabase := configfile.DefaultConfig().GetDoltDatabase(), ""
		if qerr == nil && projectCfg != nil {
			projectDatabase, sharedDatabase = projectCfg.GetDoltDatabase(), projectCfg.DoltDatabase
			if sharedDatabase == "" {
				sharedDatabase = configfile.DefaultDoltDatabase
			}
		}
		planningDatabase := sharedDatabase
		if redirect.WasRedirected && redirect.SourceDatabase != "" {
			planningDatabase = redirect.SourceDatabase
		}
		// Refused here, before the confirmation prompt, rather than by
		// refuseUninitializablePlanningRepo after it.
		switch {
		case planningDatabase == "" || planningDatabase == projectDatabase:
			return fmt.Errorf("planning repo %s is this project's own workspace; set routing.contributor to a separate planning repository (e.g. ~/.beads-planning)", planningPath)
		case planningDatabase != sharedDatabase:
			return stubDatabaseRefusal(filepath.Dir(planningBeadsDir), filepath.Dir(redirect.TargetDir), planningDatabase)
		}
		where := planningPath
		if redirect.WasRedirected {
			where = fmt.Sprintf("%s redirects to %s, which", planningPath, filepath.Dir(redirect.TargetDir))
		}
		return fmt.Errorf("planning repo %s holds the .beads this project uses, where bd uses the database %q, not this project's %q; a database beside this project's is not a separate planning repository; set routing.contributor to a separate planning repository (e.g. ~/.beads-planning)", where, planningDatabase, projectDatabase)
	}
	planningCfg, perr := configfile.LoadForDiscovery(planning)
	if perr == nil && qerr == nil && planningCfg != nil && projectCfg != nil &&
		planningCfg.ProjectID != "" && planningCfg.ProjectID == projectCfg.ProjectID {
		return fmt.Errorf("planning repo %s has this project's project_id (%s), so it is a clone of this project's workspace, not a separate planning repository; set routing.contributor to a separate planning repository (e.g. ~/.beads-planning)", planningPath, planningCfg.ProjectID)
	}
	return nil
}

var migratePersonalCmd = &cobra.Command{
	Use:   "migrate-personal",
	Short: "Move personal planning issues from the project database to your planning repo",
	Long: `Identify issues you created in the project database and move them to your
personal planning repository (~/.beads-planning by default).

This is a one-time migration for contributors who created personal planning
issues before contributor routing was configured.

The command:
  1. Finds all issues in the project database created by your git identity
  2. Shows you the list and asks for confirmation
  3. Moves them to the planning repo configured in routing.contributor

EXAMPLES:
  bd migrate-personal        # Interactive: show list and prompt
  bd migrate-personal -y     # Non-interactive: skip confirmation`,
	GroupID:       "setup",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runMigratePersonal,
}

var migratePersonalYes bool

func init() {
	migratePersonalCmd.Flags().BoolVarP(&migratePersonalYes, "yes", "y", false, "Skip confirmation prompt")
	rootCmd.AddCommand(migratePersonalCmd)
}

func runMigratePersonal(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("migrate-personal is not supported in proxied-server mode")
	}
	ctx := rootCtx

	identity := getActorWithGit()

	// Find personal issues in project DB
	filter := types.IssueFilter{Limit: 0}
	allIssues, err := store.SearchIssues(ctx, "", filter)
	if err != nil {
		return fmt.Errorf("failed to search issues: %w", err)
	}

	var personal []*types.Issue
	for _, issue := range allIssues {
		if issue.CreatedBy == identity {
			personal = append(personal, issue)
		}
	}

	if len(personal) == 0 {
		fmt.Printf("Nothing to migrate — no issues in the project database were created by %s\n", ui.RenderAccent(identity))
		return nil
	}

	// Show list
	fmt.Printf("\n%s personal issues created by %s found in project database:\n\n",
		ui.RenderBold(fmt.Sprintf("%d", len(personal))), ui.RenderAccent(identity))
	for _, issue := range personal {
		fmt.Printf("  %s  %s\n", ui.RenderAccent(issue.ID), issue.Title)
	}
	fmt.Println()

	// Get planning repo path
	planningRaw := getRoutingConfigValue(ctx, store, "routing.contributor")
	if planningRaw == "" {
		// Backward compat
		planningRaw = getRoutingConfigValue(ctx, store, "contributor.planning_repo")
	}
	if planningRaw == "" {
		return fmt.Errorf("no planning repository configured; run 'bd init --contributor' or 'bd init' in a fork repo first")
	}
	planningPath := routing.ExpandPath(planningRaw)

	// Validate planning DB path before any destructive operation
	planningBeadsDir := filepath.Join(planningPath, ".beads")
	if _, err := os.Stat(planningBeadsDir); os.IsNotExist(err) {
		return fmt.Errorf("planning repo at %s has no .beads directory; initialize it first with 'bd init'", planningPath)
	}
	if err := refuseSelfPlanningRepo(planningPath, planningBeadsDir, resolveCommandBeadsDir(dbPath)); err != nil {
		return err
	}

	fmt.Printf("Move to planning repo: %s\n\n", ui.RenderAccent(planningPath))

	if !migratePersonalYes && !confirmMigratePersonal(ctx, len(personal)) {
		return nil
	}

	// Open planning store (writable). This is a SECOND workspace's store, and
	// the copy below creates and deletes real beads in it, so it takes the
	// planning workspace's own events-journal setting — not the launching
	// workspace's. See openMigrationPlanningStore.
	planningStore, err := openMigrationPlanningStore(ctx, planningBeadsDir, store, identity)
	if err != nil {
		return planningOpenError(planningPath, err)
	}
	defer func() { _ = planningStore.Close() }()

	if err := refuseIssuesAlreadyInPlanning(ctx, planningStore, personal, planningPath); err != nil {
		return err
	}

	// Phase 1: copy everything to the planning DB, and commit it there, before
	// touching the project DB.
	ids, err := copyIssuesToPlanning(ctx, store, planningStore, personal, identity)
	if err != nil {
		return err
	}

	// Phase 2: delete them all from the project DB.
	if err := deleteMovedIssues(ctx, store, ids, planningPath); err != nil {
		return err
	}
	// In embedded mode the post-run auto-commit commits the delete, as the
	// dolt.auto-commit policy allows.
	commandDidWrite.Store(true)

	fmt.Printf("\n%s Moved %d %s to %s\n",
		ui.RenderPass("✓"),
		len(personal),
		pluralIssue(len(personal)),
		ui.RenderAccent(planningPath))
	return nil
}

// confirmMigratePersonal asks whether to move n issues and reports whether to
// go ahead. An empty answer, closed stdin included, takes the default Y; n or
// no, or a canceled read, aborts with nothing moved.
func confirmMigratePersonal(ctx context.Context, n int) bool {
	fmt.Printf("Move %d issues? [Y/n]: ", n)
	reader := bufio.NewReader(os.Stdin)
	response, err := readLineWithContext(ctx, reader, os.Stdin)
	if err != nil {
		if isCanceled(err) {
			fmt.Fprintln(os.Stderr, "Aborted.")
			return false
		}
		response = ""
	}
	response = strings.TrimSpace(strings.ToLower(response))
	if response == "n" || response == "no" {
		fmt.Println("Aborted — no changes made.")
		return false
	}
	return true
}

// planningRepoRefusal is openMigrationPlanningStore refusing the planning repo
// before anything moves: a redirect stub, a metadata-less one a routed bd
// create would not initialize either (its own config selects Dolt server
// mode, or it holds several databases with nothing naming one), or a working
// set the migration's commit would publish. Its message names the planning
// repo and the remedy.
type planningRepoRefusal struct{ msg string }

func (r *planningRepoRefusal) Error() string { return r.msg }

func refusePlanningRepo(format string, args ...any) error {
	return &planningRepoRefusal{msg: fmt.Sprintf(format, args...)}
}

// planningOpenError renders a failed openMigrationPlanningStore. A refusal of
// the planning repo (planningRepoRefusal) is returned as it is: nothing
// failed to open, so it carries no "failed to open planning store" prefix. A
// planning repo bd's own command admission refuses (an unsupported format, a
// legacy store) is rendered as that refusal is, naming the planning repo: the
// Error line, plus the Hint line and exit 16 for a format. migratePersonalCmd
// silences cobra, so neither "Error: exit code N" nor a usage dump follows
// either.
func planningOpenError(planningPath string, err error) error {
	var refusal *planningRepoRefusal
	switch {
	case errors.As(err, &refusal):
		return refusal
	case isUnsupportedWorkspaceFormat(err) || isLegacyUpgradeRefusal(err):
		return handleWorkspaceGuardError(fmt.Errorf("planning repo %s: %w", planningPath, err))
	}
	return fmt.Errorf("failed to open planning store at %s: %w", planningPath, err)
}

// refuseIssuesAlreadyInPlanning is phase 0 of runMigratePersonal: it refuses
// when any issue ID is already in the planning DB. A create can upsert onto an
// existing row, and copyIssuesToPlanning's rollback would then delete a row
// this migration did not create; if the planning store were somehow the
// project's own database (refuseSelfPlanningRepo is the first guard), phase 2
// would delete the only copy.
func refuseIssuesAlreadyInPlanning(ctx context.Context, planningStore storage.DoltStorage, personal []*types.Issue, planningPath string) error {
	for _, issue := range personal {
		_, err := planningStore.GetIssue(ctx, issue.ID)
		switch {
		case err == nil:
			if isPlanningRepoMirror(issue, planningPath) {
				return repoSyncMirrorRefusal(issue, planningPath, runtime.GOOS)
			}
			return fmt.Errorf("issue %s already exists in the planning repo %s; nothing was moved", issue.ID, planningPath)
		case !errors.Is(err, storage.ErrNotFound):
			return fmt.Errorf("failed to check the planning DB for %s: %w", issue.ID, err)
		}
	}
	return nil
}

// isPlanningRepoMirror reports whether issue's source_repo names the planning
// repo itself: a copy bd repo sync hydrated from it, which contributor setup
// arranges by adding the planning repo to repos.additional. Only then does
// the phase-0 refusal name bd repo remove, which deletes every issue carrying
// that source_repo; a copy from any other repository gets the plain refusal.
func isPlanningRepoMirror(issue *types.Issue, planningPath string) bool {
	if issue.SourceRepo == "" || issue.SourceRepo == "." {
		return false
	}
	return utils.CanonicalizePath(routing.ExpandPath(issue.SourceRepo)) == utils.CanonicalizePath(planningPath)
}

// repoSyncMirrorRefusal is the phase-0 refusal for a project copy that bd repo
// sync imported from the planning repo (isPlanningRepoMirror). It names the
// commands that remove those copies and turn their hydration back on, with
// the recorded source_repo quoted for a shell on goos (shellQuotedFor), so
// each runs as printed; such a source_repo prints as itself, and the refusal
// names it as it is. A source_repo no quoting there passes on unchanged is
// left as a <path> placeholder for the path the refusal names escaped
// (escapedValue), so pasting that name in its place runs nothing either. The
// issue ID and planning repo it names beside the commands are spelled by
// printedValue, so none of the values can redraw the commands.
func repoSyncMirrorRefusal(issue *types.Issue, planningPath, goos string) error {
	repo, ok := shellQuotedFor(goos, issue.SourceRepo)
	sourceRepo, pathNote := issue.SourceRepo, ""
	if !ok {
		sourceRepo, repo, pathNote = escapedValue(issue.SourceRepo), "<path>", ", with that path for <path>"
	}
	return fmt.Errorf("issue %s already exists in the planning repo %s; the project copy was imported from %s by `bd repo sync` (remove those copies with `bd repo remove %s`, then rerun, and run `bd repo add %s` again if you want them hydrated%s); nothing was moved",
		printedValue(issue.ID), printedValue(planningPath), sourceRepo, repo, repo, pathNote)
}

// printedValue spells value as a refusal names it beside a remedy command: as
// it is when it prints as itself (printsAsItself), and otherwise escaped
// (escapedValue), which spells each character that does not as an escape, so
// the value cannot redraw, reorder or split the command. Unlike
// ui.SanitizeForTerminal, which drops such characters, it still names the
// value bd recorded.
func printedValue(value string) string {
	if printsAsItself(value) {
		return value
	}
	return escapedValue(value)
}

// escapedValue spells value as a refusal names a value its remedy leaves as a
// placeholder, for the user to quote in its place: Go-quoted, as %q spells it,
// with each character shellQuotedFor refuses on some platform but the
// backslash, which %q already escapes, also spelled as an escape: \x22 for ",
// \x24 for $, \x26 for &, \u201c for “. Between the double quotes is then no
// character a shell shellQuotedFor names expands, ends the quotes at or splits
// the argument at, so a user who pastes the name as it is printed runs
// nothing, and strconv.Unquote still reads it back as the value.
func escapedValue(value string) string {
	shellActive := func(r rune) bool {
		return r != '\\' && strings.ContainsRune(unquotableOnPOSIX+unquotableOnWindows, r)
	}
	var escaped strings.Builder
	escaped.WriteByte('"')
	for value != "" {
		// The run up to the next such character is spelled as %q spells it.
		n := strings.IndexFunc(value, shellActive)
		if n < 0 {
			n = len(value)
		}
		quoted := strconv.Quote(value[:n])
		escaped.WriteString(quoted[1 : len(quoted)-1])
		value = value[n:]
		if value == "" {
			break
		}
		r, size := utf8.DecodeRuneInString(value)
		if r < utf8.RuneSelf {
			fmt.Fprintf(&escaped, `\x%02x`, r)
		} else {
			fmt.Fprintf(&escaped, `\u%04x`, r)
		}
		value = value[size:]
	}
	escaped.WriteByte('"')
	return escaped.String()
}

// printsAsItself reports whether a terminal shows value as the characters it
// holds: it is valid UTF-8, since a stray byte such as 0x9b starts a control
// sequence in a terminal that reads 8-bit controls, and each rune is graphic
// (unicode.IsGraphic): a letter, mark, number, punctuation, symbol or space,
// such as the U+3000 a Japanese IME types. That rules out a control
// character, such as a carriage return or the escape that starts a terminal
// sequence, a format character, such as U+202E, which reverses the text after
// it, and a line or paragraph separator, U+2028 or U+2029.
func printsAsItself(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsFunc(value, func(r rune) bool { return !unicode.IsGraphic(r) })
}

// shellQuotedFor quotes value as one argument of a command printed for a user
// to paste into a shell on goos, and reports whether it can: a remedy prints a
// value in a command only when the shells people run there all pass it to bd
// unchanged, and otherwise names it beside a placeholder.
//
// On every platform it refuses a value that does not print as itself
// (printsAsItself): a line break ends the command in each shell, a carriage
// return or terminal escape sequence can redraw the printed command so the
// one a user copies is not the one bd printed, and a character such as U+202E
// or U+2028 can reorder or split how the command shows.
//
// On POSIX a value goes in single quotes (doltutil.ShellQuote), which sh, bash,
// zsh, fish and PowerShell keep literal, unless it holds:
//   - \, which fish reads as an escape there, so a backslash before a quote
//     ends them;
//   - ', which ends them in each: doltutil.ShellQuote spells it as \' between
//     two quoted parts, which sh, bash, zsh and fish read as a quote, but
//     PowerShell, which takes a backslash literally, does not, and runs the
//     rest of the value as a command;
//   - ‘ ’ ‚ ‛ (U+2018 to U+201B), which end them in PowerShell.
//
// On Windows a value goes in plain double quotes, inside which cmd.exe,
// PowerShell and Git Bash keep spaces, backslashes and ( ) ' # @ literal, as
// Go's argument parsing does, unless it holds:
//   - ", which ends the quotes in each, or “ ” „, which end them in PowerShell;
//   - $ or `, which PowerShell and bash expand there;
//   - % or !, which cmd.exe expands there (! with delayed expansion on), and
//     bash expands ! as history;
//   - & | < > or ^, which cmd.exe acts on when PowerShell hands a .cmd shim,
//     such as npm's bd.cmd, an argument with no space in it unquoted, or ; ,
//     or =, which cmd.exe reads there as separating a batch file's
//     arguments, so a shim that reads them one by one (%1, %2) rather than
//     passing them all on (%*), as npm's does, hands bd a host such as
//     db.example.com;--recreate-missing as two (each refused whether or not
//     the value has a space, so a path such as R&D planning gets a
//     placeholder too);
//   - \\, which bash reads there as one backslash;
//
// or ends in a backslash, which Go's argument parsing reads as escaping the
// closing quote. cmd.exe needs it doubled, and PowerShell, which takes
// backslashes literally, would then pass both on.
func shellQuotedFor(goos, value string) (string, bool) {
	if !printsAsItself(value) {
		return "", false
	}
	if goos != "windows" {
		if strings.ContainsAny(value, unquotableOnPOSIX) {
			return "", false
		}
		return doltutil.ShellQuote(value), true
	}
	if strings.HasSuffix(value, `\`) || strings.Contains(value, `\\`) || strings.ContainsAny(value, unquotableOnWindows) {
		return "", false
	}
	return `"` + value + `"`, true
}

// unquotableOnPOSIX and unquotableOnWindows hold the characters a shell there
// does not keep literal inside the quotes shellQuotedFor puts a value in, or,
// on Windows, in the argument PowerShell hands a .cmd shim unquoted.
// escapedValue spells each of them as an escape.
const (
	unquotableOnPOSIX   = "\\'‘’‚‛"
	unquotableOnWindows = "\"“”„$`%!&|<>^;,="
)

// copyIssuesToPlanning is phase 1 of runMigratePersonal: it copies every
// personal issue from src to the planning store dst and returns their IDs. The
// copy is all-or-error — any failed step rolls back the planning-DB rows it
// created and returns the error before anything is deleted from the project
// DB, so a partial copy can never be mistaken for success and lose data
// (maphew review, be-e2nb). A rollback that fails is reported with the error.
//
// Phase 1a creates every issue row first, so same-prefix dependencies between
// two migrated issues resolve in phase 1b regardless of copy order. Phase 1b
// copies labels, dependency records, and comments for every issue. The copy is
// then committed in the planning repo (commitPlanningWrites): the post-run
// auto-commit commits only the project's store, and the planning history must
// hold the copy before the project's delete is committed. A commit whose
// outcome is unknown (storage.ErrCommitIndeterminate) is not rolled back: it
// may have landed, and an uncommitted delete of its rows would undo it at the
// planning repo's next commit. The copy is left for bd dolt commit to settle.
func copyIssuesToPlanning(ctx context.Context, src, dst storage.DoltStorage, personal []*types.Issue, actor string) ([]string, error) {
	var created []string
	rollback := func(err error) ([]string, error) {
		if len(created) == 0 {
			return nil, err
		}
		if _, rollbackErr := dst.DeleteIssues(ctx, created, false, false, false); rollbackErr != nil {
			return nil, fmt.Errorf("%w; rolling the copy back failed too, so the planning DB still holds %s: %w", err, strings.Join(created, ", "), rollbackErr)
		}
		return nil, err
	}
	for _, issue := range personal {
		if err := dst.CreateIssue(ctx, issue, actor); err != nil {
			return rollback(fmt.Errorf("failed to copy issue %s to planning DB: %w", issue.ID, err))
		}
		created = append(created, issue.ID)
	}
	for _, issue := range personal {
		if err := copyIssueRelations(ctx, src, dst, issue, actor); err != nil {
			return rollback(fmt.Errorf("failed to copy %s to planning DB: %w", issue.ID, err))
		}
	}
	err := commitPlanningWrites(ctx, dst, formatDoltAutoCommitMessage("migrate-personal", actor, created))
	switch {
	case errors.Is(err, storage.ErrCommitIndeterminate):
		return nil, fmt.Errorf("committing the copy in the planning repo may have failed: %w; the copy of %s is left there and nothing was deleted from the project DB, so run `bd dolt commit` in the planning repo, then resolve the duplicates manually", err, strings.Join(created, ", "))
	case err != nil:
		return rollback(fmt.Errorf("failed to commit the copy in the planning repo: %w", err))
	}
	return created, nil
}

// commitsPlanningWrites reports whether the migration commits its writes in
// the planning store dst itself (commitPlanningWrites): dst is embedded, whose
// writes stay in its working set until a commit, and the command's
// dolt.auto-commit policy is on. It asks the planning store for its backend,
// not the command for its mode (commitPendingIfEmbedded): the planning repo
// opens with its own (openMigrationPlanningStore), so a server-mode project
// can move issues into an embedded planning repo. A server-mode planning store
// commits each write itself, and a commit here would sweep another writer's
// working set into it.
func commitsPlanningWrites(dst storage.DoltStorage) (bool, error) {
	if !isEmbeddedStore(dst) {
		return false, nil
	}
	mode, err := getDoltAutoCommitMode()
	return mode == doltAutoCommitOn, err
}

// commitPlanningWrites commits the working set of the planning store dst under
// message when commitsPlanningWrites says the migration commits there.
func commitPlanningWrites(ctx context.Context, dst storage.DoltStorage, message string) error {
	commits, err := commitsPlanningWrites(dst)
	if err != nil || !commits {
		return err
	}
	return maybeAutoCommitStore(ctx, dst, doltAutoCommitParams{MessageOverride: message})
}

// deleteMovedIssues is phase 2 of runMigratePersonal: it deletes the moved
// issues from the project DB in a single transaction, under the command's
// dolt.auto-commit policy (issueOpsContext), as bd delete does. Both halves of
// the move then follow it: with on, a server-mode project commits the delete
// as it runs and phase 1 commits the copy; with batch or off, the delete stays
// in the working set, as phase 1 leaves the copy in an embedded planning store
// (commitsPlanningWrites). The planning copy is complete at this point. If the
// delete fails, the planning repo already holds the good copy and the project
// DB is untouched, so the user is told to resolve the duplicate rather than
// silently losing data.
func deleteMovedIssues(ctx context.Context, projectStore storage.DoltStorage, ids []string, planningPath string) error {
	opsCtx, err := issueOpsContext(ctx)
	if err == nil {
		_, err = projectStore.DeleteIssues(opsCtx, ids, false, false, false)
	}
	if err != nil {
		return fmt.Errorf("issues copied to planning repo %s but deleting them from the project DB failed; resolve the duplicate manually: %w", planningPath, err)
	}
	return nil
}

// copyIssueRelations copies the labels, dependency records, and comments for a
// single issue from src to dst. The issue row itself must already exist in dst
// (created in phase 1a, copyIssuesToPlanning). Every step — including the reads
// from src — is fatal on error so the caller can abort the migration before any
// source data is deleted; a partial copy must never be mistaken for success
// (maphew review, be-e2nb).
func copyIssueRelations(ctx context.Context, src, dst storage.DoltStorage, issue *types.Issue, actor string) error {
	labels, err := src.GetLabels(ctx, issue.ID)
	if err != nil {
		return fmt.Errorf("read labels for %s: %w", issue.ID, err)
	}
	for _, label := range labels {
		if err := dst.AddLabel(ctx, issue.ID, label, actor); err != nil {
			return fmt.Errorf("copy label %q for %s: %w", label, issue.ID, err)
		}
	}

	deps, err := src.GetDependencyRecords(ctx, issue.ID)
	if err != nil {
		return fmt.Errorf("read dependencies for %s: %w", issue.ID, err)
	}
	for _, dep := range deps {
		if err := dst.AddDependency(ctx, dep, actor); err != nil {
			return fmt.Errorf("copy dependency %s→%s: %w", dep.IssueID, dep.DependsOnID, err)
		}
	}

	comments, err := src.GetIssueComments(ctx, issue.ID)
	if err != nil {
		return fmt.Errorf("read comments for %s: %w", issue.ID, err)
	}
	for _, c := range comments {
		// ImportIssueComment writes a structured comment row preserving the
		// original author and created_at. AddComment would instead record an
		// event-style entry that GetIssueComments never returns, silently
		// dropping the comment from the migrated issue (maphew review, be-e2nb).
		if _, err := dst.ImportIssueComment(ctx, issue.ID, c.Author, c.Text, c.CreatedAt); err != nil {
			return fmt.Errorf("copy comment for %s: %w", issue.ID, err)
		}
	}

	return nil
}

func pluralIssue(n int) string {
	if n == 1 {
		return "issue"
	}
	return "issues"
}
