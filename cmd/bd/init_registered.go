package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/steveyegge/beads/cmd/bd/doctor"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/ui"
)

// registeredInitFlags are the bd init flags the registered-backend route
// honors. --non-interactive, --skip-hooks and --skip-agents are honored by
// construction: the route never prompts and installs neither hooks nor agent
// files. Every other init flag selects something only the Dolt route does, and
// is refused rather than ignored (refuseFlagsRegisteredInitCannotHonor). The
// list is an allow-list so a flag added to bd init later is refused here
// until someone decides this route honors it.
var registeredInitFlags = map[string]bool{
	"backend":         true,
	"prefix":          true,
	"quiet":           true,
	"init-if-missing": true,
	"non-interactive": true,
	"skip-hooks":      true,
	"skip-agents":     true,
}

// registeredInitRefusedGlobals are the root flags that place a Dolt database.
// The route cannot honor them either: a registered backend places its own
// store through its Create hook. The other root flags (--json, --verbose,
// --actor, ...) are process-wide and say nothing about what init provisions.
var registeredInitRefusedGlobals = map[string]bool{
	"db":       true,
	"database": true,
}

// refuseFlagsRegisteredInitCannotHonor fails bd init --backend=<registered>
// when the command line carries a flag the route would silently drop. Before
// registered backends could be provisioned, every such invocation failed on
// the backend name alone; accepting --server, --remote or --stealth now and
// ignoring them would report success for a workspace the caller did not ask
// for.
func refuseFlagsRegisteredInitCannotHonor(cmd *cobra.Command, name string) error {
	for _, legacyFlag := range removedBackendInitFlags {
		if cmd.Flags().Changed(legacyFlag.name) {
			return fmt.Errorf("--%s belonged to %s: %s; backend %q does not take it", legacyFlag.name, legacyFlag.origin, legacyFlag.rationale, name)
		}
	}
	local := cmd.LocalFlags()
	var refused []string
	cmd.Flags().Visit(func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		if local.Lookup(f.Name) == f {
			if !registeredInitFlags[f.Name] {
				refused = append(refused, "--"+f.Name)
			}
			return
		}
		if registeredInitRefusedGlobals[f.Name] {
			refused = append(refused, "--"+f.Name)
		}
	})
	if len(refused) == 0 {
		return nil
	}
	sort.Strings(refused)
	return fmt.Errorf("bd init cannot honor %s with backend %q: a registered backend is provisioned through its own create hook, and bd init applies only --prefix, --quiet and --init-if-missing to it", strings.Join(refused, ", "), name)
}

// registeredInitOptions is what bd init hands the registered-backend route.
type registeredInitOptions struct {
	name          string
	prefix        string
	quiet         bool
	initIfMissing bool
	// keepHomeClean is guardInitInHomeDir's answer: the workspace is one the
	// caller named under an untracked home directory, and init leaves the
	// directory around it alone.
	keepHomeClean bool
}

// runInitRegisteredBackend provisions a workspace on a registered backend
// that can create one (backends.CanCreate). It is the minimal init: the
// guards, the standard .beads/.gitignore, the backend's own store creation,
// the issue prefix and the identity written through the open store, and last
// metadata.json naming the backend. The remote, hook and agent setup of the
// Dolt route is Dolt's and is left out.
//
// metadata.json is the commit point. It is what makes the directory a
// workspace to every later command and to the next bd init, so it is written
// only after the store exists and holds its prefix and identity: a failure
// before that leaves nothing a later init would refuse as "already
// initialized" or --init-if-missing would wave through. A .beads directory
// this init created is removed again on failure; one that was already there
// is left as it was found, plus whatever the backend's Create put in it,
// which the hook's contract requires it to accept on a retry.
func runInitRegisteredBackend(ctx context.Context, opts registeredInitOptions) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cwd, cwdErr := os.Getwd()
	if cwdErr != nil {
		return fmt.Errorf("failed to get current directory: %v", cwdErr)
	}
	if cwdIsInsideBeadsDir(cwd) {
		return fmt.Errorf("cannot initialize bd inside a .beads directory (%s); run bd init from outside it", cwd)
	}
	beadsDir := resolveInitBeadsDir()
	if beadsDir == "" {
		return fmt.Errorf("cannot determine the workspace directory for bd init")
	}
	// The same refusal the Dolt route makes before it looks at anything else:
	// a historical workspace, or metadata naming a removed backend, is never
	// initialized over.
	if guardErr := guardLegacyUpgradeWorkspaceForInit(beadsDir, false); guardErr != nil {
		return handleWorkspaceGuardError(guardErr)
	}
	prefix := opts.prefix
	if prefix == "" {
		prefix = config.GetString("issue-prefix")
	}
	if prefix == "" {
		prefix = filepath.Base(cwd)
	}
	prefix = normalizeIssuePrefix(prefix)

	// An initialized workspace is answered before the gate: --init-if-missing
	// on one is a no-op many callers make at once, and it must not queue for
	// exclusive access behind the workspace's own commands.
	if done, existsErr := registeredInitAnswersExisting(beadsDir, opts); done || existsErr != nil {
		return existsErr
	}

	// bd init creates workspace state, so it holds the workspace gate
	// exclusively from here on, as the Dolt route does: two inits of one
	// directory cannot both pass the check above and both call Create.
	beadsDirAbs, absErr := filepath.Abs(beadsDir)
	if absErr != nil {
		beadsDirAbs = filepath.Clean(beadsDir)
	}
	gate, gateErr := acquireInitMutationGate(ctx, beadsDirAbs, "", nil)
	if gateErr != nil {
		return gateErr
	}
	defer func() { _ = gate.Release() }()
	// Again under the gate: an init that waited for another one finds the
	// workspace that one made.
	if done, existsErr := registeredInitAnswersExisting(beadsDir, opts); done || existsErr != nil {
		return existsErr
	}
	if dataErr := checkExistingBeadsDataAt(beadsDir, prefix); dataErr != nil {
		return dataErr
	}

	_, statErr := os.Stat(beadsDir)
	createdBeadsDir := os.IsNotExist(statErr)
	if mkErr := os.MkdirAll(beadsDir, 0o700); mkErr != nil {
		return fmt.Errorf("failed to create %s: %w", beadsDir, mkErr)
	}
	defer func() {
		if err != nil && createdBeadsDir {
			_ = os.RemoveAll(beadsDir)
		}
	}()

	// The standard .beads/.gitignore keeps the runtime files, locks and
	// credential key every workspace can grow out of git. It knows only the
	// built-in engines' directories: the backend's Create keeps its own store
	// out of version control.
	if ignoreErr := doctor.EnsureGitignoreForBeadsDir(beadsDir); ignoreErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to create/update .gitignore: %v\n", ignoreErr)
	}
	if createErr := backends.Create(ctx, opts.name, beadsDir); createErr != nil {
		return fmt.Errorf("failed to create the %s workspace store: %w", opts.name, createErr)
	}
	projectID := configfile.GenerateProjectID()
	if seedErr := seedRegisteredWorkspace(ctx, opts.name, beadsDir, prefix, projectID); seedErr != nil {
		return seedErr
	}

	cfg := configfile.DefaultConfig()
	cfg.Backend = opts.name
	cfg.ProjectID = projectID
	if saveErr := cfg.Save(beadsDir); saveErr != nil {
		return fmt.Errorf("failed to write %s: %w", configfile.ConfigPath(beadsDir), saveErr)
	}

	// Every command on the workspace takes the workspace gate, whose file
	// sits beside .beads, so the project .gitignore gets the patterns the
	// Dolt route gives it, under the same two conditions: the workspace is
	// under the directory bd init ran in, and that directory is not a home
	// directory init was asked to leave alone.
	if !opts.keepHomeClean && strings.HasPrefix(beadsDirAbs, filepath.Clean(cwd)+string(filepath.Separator)) {
		if ignoreErr := doctor.EnsureProjectGitignore(cwd); ignoreErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to update project .gitignore: %v\n", ignoreErr)
		}
	}

	if !opts.quiet {
		fmt.Printf("  %s Initialized beads workspace at %s\n", ui.RenderPass("✓"), beadsDir)
		fmt.Printf("  %s Backend: %s\n", ui.RenderPass("✓"), opts.name)
		fmt.Printf("  %s Issue prefix: %s\n", ui.RenderPass("✓"), prefix)
	}
	return nil
}

// registeredInitAnswersExisting reports whether beadsDir already holds an
// initialized workspace, and answers for it when it does: --init-if-missing
// returns without touching it, a plain init is refused.
func registeredInitAnswersExisting(beadsDir string, opts registeredInitOptions) (done bool, err error) {
	existing, loadErr := configfile.LoadForDiscovery(beadsDir)
	if loadErr != nil {
		return true, fmt.Errorf("failed to load %s: %w", configfile.ConfigPath(beadsDir), loadErr)
	}
	if existing == nil {
		return false, nil
	}
	if opts.initIfMissing {
		if !opts.quiet {
			fmt.Printf("  %s Workspace already initialized at %s (backend %q)\n", ui.RenderPass("✓"), beadsDir, existing.GetBackend())
		}
		return true, nil
	}
	return true, alreadyInitialized("beads workspace already exists at %s (backend %q); use bd commands normally, or move it aside before initializing again", beadsDir, existing.GetBackend())
}

// seedRegisteredWorkspace writes the issue prefix and the workspace identity
// through the store the backend just created, and closes it. Init opens to
// write, the posture the Dolt route's own open has: a backend that cannot
// journal on a journaling workspace is refused here rather than by every
// later write.
func seedRegisteredWorkspace(ctx context.Context, name, beadsDir, prefix, projectID string) error {
	store, err := newRegisteredBackendStore(ctx, name, beadsDir, false, true)
	if err != nil {
		return openStoreError(name, err)
	}
	if err := store.SetConfig(ctx, "issue_prefix", prefix); err != nil {
		_ = store.Close()
		return fmt.Errorf("failed to set issue prefix: %w", err)
	}
	if err := store.SetMetadata(ctx, "_project_id", projectID); err != nil {
		_ = store.Close()
		return fmt.Errorf("failed to record the workspace identity: %w", err)
	}
	if err := store.Close(); err != nil {
		return fmt.Errorf("failed to close the %s workspace store: %w", name, err)
	}
	return nil
}

// cwdIsInsideBeadsDir reports whether dir is a .beads directory or lies under
// one, where bd init never creates a workspace.
func cwdIsInsideBeadsDir(dir string) bool {
	clean := filepath.Clean(dir)
	sep := string(filepath.Separator)
	return strings.Contains(clean, sep+".beads"+sep) || strings.HasSuffix(clean, sep+".beads")
}
