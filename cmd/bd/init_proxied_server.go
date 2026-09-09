package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/domain"
	"github.com/steveyegge/beads/internal/storage/fs"
	"github.com/steveyegge/beads/internal/storage/git"
	"github.com/steveyegge/beads/internal/storage/uow"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/workapi"
	"github.com/steveyegge/beads/issueops"
)

type initProxiedServerInput struct {
	prefix                 string
	database               string
	roleFlag               string
	initRemote             string
	initRemoteChanged      bool
	destroyToken           string
	serverConfigPath       string
	serverLogPath          string
	serverRootPath         string
	serverProxyPort        int
	serverProxyIdleTimeout time.Duration
	externalConfig         *configfile.ExternalDoltConfig
	quiet                  bool
	stealth                bool
	skipHooks              bool
	skipAgents             bool
	reinitLocal            bool
	contributor            bool
	team                   bool
	teamServer             bool
	fromJSONL              bool
	nonInteractive         bool
}

func runInitProxiedServer(cmd *cobra.Command, ctx context.Context, in initProxiedServerInput) error {
	if in.fromJSONL {
		return fmt.Errorf("--from-jsonl is not supported with --proxied-server")
	}
	if in.contributor {
		return fmt.Errorf("--contributor is not supported with --proxied-server")
	}
	if in.team {
		return fmt.Errorf("--team is not supported with --proxied-server")
	}

	// Preflight the external dolt binary before any .beads/ write below
	// (checkExistingBeadsData onward). This only applies to managed
	// proxied-server mode (in.externalConfig == nil): external mode talks
	// to a remote/pre-existing dolt sql-server over the network and never
	// spawns a local dolt binary, so it has nothing to preflight here.
	// Failing here means a missing/broken dolt produces a clean preflight
	// error instead of a half-initialized .beads/ directory from a later
	// failure in newManagedProxiedServerUOWProvider.
	//
	// Shares resolveAndProbeDolt (uow_factory.go) with
	// newManagedProxiedServerUOWProvider, which this same `bd init
	// --proxied-server` invocation goes on to call a few lines below via
	// newProxiedServerUOWProvider: the shared doltVersionWarnOnce means the
	// version advisory prints at most once for the command, not once per
	// call site.
	if in.externalConfig == nil {
		// in.quiet, not the global quietFlag: init's local --quiet shadows
		// the persistent flag, so the global is false under `bd init -q`.
		if _, err := resolveAndProbeDolt(ctx, "bd init --proxied-server", in.quiet || quietFlag); err != nil {
			return err
		}
	}

	if err := config.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize config: %v\n", err)
	}

	if err := checkExistingBeadsData(in.prefix); err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current directory: %v", err)
	}

	fsProvider := fs.NewFileSystemProvider(cwd, newBeadsDirTemplates(), newFileSystemAdapters())
	fsUseCase := fsProvider.BeadsDirFSUseCase()
	gitUC := git.NewGitProvider(cwd).GitUseCase()

	if in.stealth {
		if err := fsUseCase.SetupStealthMode(ctx, !in.quiet); err != nil {
			return fmt.Errorf("setting up stealth mode: %v", err)
		}
		in.skipHooks = true
	}

	prefix, err := resolveInitPrefix(in.prefix)
	if err != nil {
		return err
	}

	proxiedInit, err := fsUseCase.ResolveProxiedInit(ctx, domain.ResolveProxiedInitParams{
		Prefix: prefix,
		DBFlag: in.database,
	})
	if err != nil {
		return fmt.Errorf("resolving proxied init: %v", err)
	}
	beadsDir, hasExplicitBeadsDir := proxiedInit.BeadsDir, proxiedInit.HasExplicit
	dbName, projectID := proxiedInit.DBName, proxiedInit.ProjectID
	beadsDirIsLocal := proxiedInit.IsLocal
	useLocalBeads := !hasExplicitBeadsDir || beadsDirIsLocal

	if in.teamServer && proxiedInit.DBNameDerived {
		return fmt.Errorf(
			"--team-server requires --database (or an existing .beads/metadata.json naming the database): bd cannot guess the name of the bts-provisioned database (guessed %q from the prefix)",
			dbName)
	}

	if strings.Contains(filepath.Clean(cwd), string(filepath.Separator)+".beads"+string(filepath.Separator)) ||
		strings.HasSuffix(filepath.Clean(cwd), string(filepath.Separator)+".beads") {
		return fmt.Errorf("cannot initialize bd inside a .beads directory\nCurrent directory: %s", cwd)
	}

	if !hasExplicitBeadsDir {
		res, err := gitUC.EnsureGitRepo(ctx)
		if err != nil {
			return fmt.Errorf("failed to initialize git repository: %v", err)
		}
		if res.DidInit && !in.quiet {
			fmt.Printf("  %s Initialized git repository\n", ui.RenderPass("✓"))
		}
	}

	metadataBody, err := composeProxiedServerMetadataJSON(proxiedMetadataInputs{
		dbName:     dbName,
		projectID:  projectID,
		teamServer: in.teamServer,
	})
	if err != nil {
		return fmt.Errorf("composing metadata.json: %v", err)
	}
	configYAMLBody := renderInitConfigYAML("", false)

	clientInfo, err := buildProxiedServerClientInfo(in.serverRootPath, in.serverConfigPath, in.serverLogPath, in.serverProxyPort, in.serverProxyIdleTimeout, in.externalConfig)
	if err != nil {
		return err
	}

	fsParams := domain.InitializeBeadsDirParams{
		MetadataJSONBody:        metadataBody,
		ConfigYAMLBody:          configYAMLBody,
		ProxiedServerClientInfo: clientInfo,
		SetNoCOW:                true,
		WriteProjectGitignore:   useLocalBeads && beadsDirIsLocal,
	}
	if useLocalBeads {
		fsParams.LocalVersion = Version
	}

	fsResult, err := fsUseCase.InitializeBeadsDir(ctx, fsParams)
	if err != nil {
		return fmt.Errorf("initializing .beads directory: %v", err)
	}
	if fsResult.NoCOWErr != nil && !in.quiet {
		fmt.Fprintf(os.Stderr, "Warning: failed to set FS_NOCOW_FL on %s: %v\n", beadsDir, fsResult.NoCOWErr)
	}
	if fsResult.LocalVersionErr != nil && !in.quiet {
		fmt.Fprintf(os.Stderr, "Warning: failed to initialize version tracking: %v\n", fsResult.LocalVersionErr)
	}

	// bd init is the one command whose open may create the database (#2189):
	// every other proxied call site opens with create disabled.
	initUOWProvider, err := newProxiedServerUOWProviderAdopting(ctx, beadsDir, "", uow.WithCreateIfMissing(true))
	if err != nil {
		return fmt.Errorf("failed to open uow provider: %v", err)
	}
	defer func() { _ = initUOWProvider.Close(ctx) }()

	remoteURL := resolveProxiedInitRemoteURL(ctx, gitUC, in)

	var repoID, cloneID string
	if id, err := beads.ComputeRepoID(); err == nil {
		repoID = id
	} else if !in.quiet {
		fmt.Fprintf(os.Stderr, "Warning: could not compute repository ID: %v\n", err)
	}
	if id, err := beads.GetCloneID(); err == nil {
		cloneID = id
	} else if !in.quiet {
		fmt.Fprintf(os.Stderr, "Warning: could not compute clone ID: %v\n", err)
	}

	// The order is VERIFY, then bootstrap or adopt: Bootstrapper REFUSES an
	// already-identified substrate, and asking first is how a front door tells a
	// re-init from the collision that guard exists for.
	verifier, err := proxiedInitVerifier(initUOWProvider)
	if err != nil {
		return HandleError("%v", err)
	}

	adoptedPrefix, adoptedProjectID := prefix, projectID
	if in.teamServer {
		// bts owns the shared database: adopt identity and write nothing — no
		// identity, no tracking metadata (repo_id/clone_id are per-clone
		// fingerprints; last-init-wins overwrites feed false cross-project
		// mismatch diagnostics), no Dolt remote.
		adoptedPrefix, adoptedProjectID, err = adoptTeamServerIdentity(ctx, verifier, dbName, prefix, in.prefix != "", projectID)
		if err != nil {
			return HandleError("%v", err)
		}
	} else {
		existing, err := verifier.VerifyIdentity(ctx, issueops.VerifyIdentityRequest{})
		if err != nil {
			return HandleError("reading project identity from database %q: %v", dbName, err)
		}
		switch {
		case existing.Prefix != "" || existing.ProjectID != "":
			// Another rig — or an earlier init — already identified this
			// database. ADOPT it: this route used to rewrite the prefix and the
			// project id every time, which renamed the ids a co-tenant was
			// about to mint.
			adoptedPrefix, adoptedProjectID = existing.Prefix, existing.ProjectID
			if !in.quiet {
				fmt.Printf("  %s Adopted project identity from existing database\n", ui.RenderPass("✓"))
			}
		default:
			bootstrapper, err := proxiedBootstrapper(initUOWProvider)
			if err != nil {
				return HandleError("%v", err)
			}
			result, err := bootstrapper.Bootstrap(ctx, issueops.BootstrapRequest{
				Prefix:    prefix,
				ProjectID: projectID,
			})
			if err != nil {
				return HandleError("bootstrap project: %v", err)
			}
			adoptedPrefix, adoptedProjectID = result.Prefix, result.ProjectID
		}

		// The per-clone tracking state is written on EVERY init, adopt or not,
		// which is why it is not on the role: a fresh clone of an
		// already-identified database needs its own fingerprints precisely
		// because it bootstrapped nothing.
		if err := recordProxiedInitTrackingState(ctx, initUOWProvider, repoID, cloneID); err != nil {
			return HandleError("%v", err)
		}

		// The Dolt remote is configured SEPARATELY from the identity, the way
		// the direct route has always configured it: folding them into one call
		// is what let a remote that could not be created fail a bootstrap that
		// had already succeeded.
		if remoteURL != "" {
			if err := configureProxiedInitDoltRemote(ctx, initUOWProvider, remoteURL); err != nil {
				return HandleError("%v", err)
			}
		}
	}

	// metadata.json was written with a locally-minted project id before any
	// DB connection existed; the adopted id must replace it before the tail
	// git-commits .beads/.
	if in.teamServer && adoptedProjectID != projectID {
		fileCfg, err := configfile.Load(beadsDir)
		if err != nil || fileCfg == nil {
			return HandleError("failed to reload %s to adopt the provisioned project identity: %v", configfile.ConfigFileName, err)
		}
		fileCfg.ProjectID = adoptedProjectID
		if err := fileCfg.Save(beadsDir); err != nil {
			return HandleError("failed to save the provisioned project identity to %s: %v", configfile.ConfigFileName, err)
		}
	}

	return runInitProxiedServerTail(cmd, ctx, in, runInitTailContext{
		beadsDir:      beadsDir,
		prefix:        adoptedPrefix,
		dbName:        dbName,
		useLocalBeads: useLocalBeads,
		remoteURL:     remoteURL,
		fsUseCase:     fsUseCase,
		gitUC:         gitUC,
	})
}

func resolveInitPrefix(flagPrefix string) (string, error) {
	prefix := flagPrefix
	if prefix == "" {
		prefix = config.GetString("issue-prefix")
	}
	if prefix == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("failed to get current directory: %v", err)
		}
		prefix = filepath.Base(cwd)
	}
	prefix = strings.TrimLeft(prefix, ".")
	prefix = strings.TrimRight(prefix, "-")
	prefix = strings.ReplaceAll(prefix, ".", "_")
	if len(prefix) > 0 && !((prefix[0] >= 'a' && prefix[0] <= 'z') || (prefix[0] >= 'A' && prefix[0] <= 'Z') || prefix[0] == '_') {
		prefix = "bd_" + prefix
	}
	return prefix, nil
}

func resolveProxiedInitRemoteURL(ctx context.Context, gitUC domain.GitUseCase, in initProxiedServerInput) string {
	url, source := resolveInitConfiguredSyncRemote(in.initRemote, in.initRemoteChanged, resolveSyncRemote)
	if url != "" {
		return url
	}
	if source != initSyncRemoteNone {
		return ""
	}
	if !in.stealth {
		if originURL, err := gitUC.OriginRemoteURL(ctx); err == nil && originURL != "" {
			return normalizeRemoteURL(originURL)
		}
	}
	return ""
}

// proxiedInitVerifier and proxiedBootstrapper hand back the two identity
// surfaces through the provider's own capability accessors.
//
// They are asked for SEPARATELY, and team-server mode is what that separation
// is for: it holds a verifier and never obtains a bootstrapper, so the path bd
// must not write on cannot reach the write.
func proxiedInitVerifier(provider uow.UnitOfWorkProvider) (issueops.InitVerifier, error) {
	src, ok := provider.(uow.InitVerifierSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the identity-read surface", provider)
	}
	return src.InitVerifier()
}

func proxiedBootstrapper(provider uow.UnitOfWorkProvider) (issueops.Bootstrapper, error) {
	src, ok := provider.(uow.BootstrapperSource)
	if !ok {
		return nil, fmt.Errorf("proxied-server provider %T does not offer the identity-seeding surface", provider)
	}
	return src.Bootstrapper()
}

// recordProxiedInitTrackingState seeds the per-clone bookkeeping: the
// repository and clone fingerprints, the synced-at marker and the recorded
// binary version.
//
// It is separate from the identity because its LIFETIME is: the identity is
// written once and then adopted forever, while these four describe the clone
// running init and are refreshed every time it runs. In the refusable one-time
// write, a re-init on a shared database would silently stop recording them.
func recordProxiedInitTrackingState(ctx context.Context, provider uow.UnitOfWorkProvider, repoID, cloneID string) error {
	return uow.RunTx(ctx, provider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		cfg := uw.ConfigUseCase()
		// An absent fingerprint is recorded as nothing rather than as "": an
		// empty row reads back to cross-project verification as a clone whose
		// fingerprint failed to compute.
		if repoID != "" {
			if err := cfg.SetMetadata(ctx, "repo_id", repoID); err != nil {
				return "", fmt.Errorf("record repo_id: %w", err)
			}
		}
		if cloneID != "" {
			if err := cfg.SetMetadata(ctx, "clone_id", cloneID); err != nil {
				return "", fmt.Errorf("record clone_id: %w", err)
			}
		}
		if err := cfg.SetMetadata(ctx, "last_import_time", time.Now().UTC().Format(time.RFC3339)); err != nil {
			return "", fmt.Errorf("record last_import_time: %w", err)
		}
		if err := cfg.SetLocalMetadata(ctx, workapi.MetadataKeyVersion, Version); err != nil {
			return "", fmt.Errorf("record bd_version: %w", err)
		}
		return "bd init", nil
	})
}

// configureProxiedInitDoltRemote adds the sync remote, skipping a name that is
// already taken.
func configureProxiedInitDoltRemote(ctx context.Context, provider uow.UnitOfWorkProvider, remoteURL string) error {
	return uow.RunTx(ctx, provider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		remotes, err := uw.DoltRemoteUseCase().ListRemotes(ctx)
		if err != nil {
			return "", fmt.Errorf("list remotes: %w", err)
		}
		for _, r := range remotes {
			if r.Name == "origin" {
				return "", nil
			}
		}
		if err := uw.DoltRemoteUseCase().CreateRemote(ctx, "origin", remoteURL); err != nil {
			return "", fmt.Errorf("create remote origin: %w", err)
		}
		return "", nil
	})
}

// adoptTeamServerIdentity reads the bts-provisioned identity out of the shared
// database, following the gateway contract: adopt if present, hard error if
// absent — bd never writes identity in team-server mode.
//
// ABSENT means "unprovisioned, tell them to run bts init" and UNREADABLE means
// "the connection failed, say so"; keeping those apart is the InitVerifier
// role's promise. The two markers arrive as ONE snapshot, so the prefix and the
// project id cannot come from either side of a concurrent write.
func adoptTeamServerIdentity(ctx context.Context, verifier issueops.InitVerifier, dbName, localPrefix string, prefixIsExplicit bool, localProjectID string) (prefix, projectID string, err error) {
	identity, readErr := verifier.VerifyIdentity(ctx, issueops.VerifyIdentityRequest{})
	if _, err := resolveInitIssuePrefix(true, identity.Prefix, dbName, localPrefix, readErr); err != nil {
		if readErr == nil {
			return "", "", fmt.Errorf(
				"database %q has no project identity (config.issue_prefix) — provision it with 'bts init' (or heal an older bts database with 'bts migrate')",
				dbName)
		}
		return "", "", err
	}
	// An explicit --prefix that disagrees must not be silently ignored; a
	// merely derived prefix adopts silently.
	if prefixIsExplicit && identity.Prefix != localPrefix {
		return "", "", fmt.Errorf(
			"--prefix %q conflicts with issue_prefix %q provisioned in database %q; omit --prefix to adopt the provisioned one",
			localPrefix, identity.Prefix, dbName)
	}

	adoptedID, _, err := resolveInitProjectID(true, localProjectID, identity.ProjectID, dbName, readErr)
	if err != nil {
		if readErr == nil {
			return "", "", fmt.Errorf(
				"database %q has no project identity (metadata._project_id) — provision it with 'bts init' (or heal an older bts database with 'bts migrate')",
				dbName)
		}
		return "", "", err
	}
	return identity.Prefix, adoptedID, nil
}

type proxiedMetadataInputs struct {
	dbName     string
	projectID  string
	teamServer bool
}

func composeProxiedServerMetadataJSON(in proxiedMetadataInputs) ([]byte, error) {
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.Database = "dolt"
	cfg.DoltDatabase = in.dbName
	cfg.DoltMode = configfile.DoltModeProxiedServer
	cfg.ProjectID = in.projectID
	cfg.DoltTeamServer = in.teamServer

	if filepath.IsAbs(cfg.DoltDataDir) {
		cfg.DoltDataDir = ""
	}

	return json.MarshalIndent(cfg, "", "  ")
}

func buildProxiedServerClientInfo(rootPath, configPath, logPath string, port int, idleTimeout time.Duration, external *configfile.ExternalDoltConfig) (*configfile.ProxiedServerClientInfo, error) {
	if rootPath == "" && configPath == "" && logPath == "" && port == 0 && idleTimeout == 0 && external == nil {
		return nil, nil
	}
	clean := func(p string) (string, error) {
		if p == "" {
			return "", nil
		}
		if !filepath.IsAbs(p) {
			return "", fmt.Errorf("buildProxiedServerClientInfo: path %q is not absolute", p)
		}
		return filepath.Clean(p), nil
	}
	rootAbs, err := clean(rootPath)
	if err != nil {
		return nil, err
	}
	configAbs, err := clean(configPath)
	if err != nil {
		return nil, err
	}
	logAbs, err := clean(logPath)
	if err != nil {
		return nil, err
	}
	if external != nil {
		if err := external.Validate(); err != nil {
			return nil, fmt.Errorf("buildProxiedServerClientInfo: %w", err)
		}
	}
	return &configfile.ProxiedServerClientInfo{
		RootPath:    rootAbs,
		ConfigPath:  configAbs,
		LogPath:     logAbs,
		Port:        port,
		IdleTimeout: idleTimeout,
		External:    external,
	}, nil
}

type runInitTailContext struct {
	beadsDir      string
	prefix        string
	dbName        string
	useLocalBeads bool
	remoteURL     string
	fsUseCase     domain.BeadsDirFSUseCase
	gitUC         domain.GitUseCase
}

func runInitProxiedServerTail(cmd *cobra.Command, ctx context.Context, in initProxiedServerInput, t runInitTailContext) error {
	isRepo := t.gitUC.IsGitRepo(ctx)

	if isRepo {
		role := in.roleFlag
		if role == "" {
			role = "maintainer"
		}
		_, hasRole, _ := t.gitUC.BeadsRole(ctx)
		if !hasRole || in.roleFlag != "" {
			if err := t.gitUC.SetBeadsRole(ctx, role); err != nil && !in.quiet {
				fmt.Fprintf(os.Stderr, "Warning: failed to set beads.role: %v\n", err)
			}
		}
	}

	setupExclude, _ := cmd.Flags().GetBool("setup-exclude")
	if setupExclude {
		if err := t.fsUseCase.SetupForkExclude(ctx, !in.quiet); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to configure git exclude: %v\n", err)
		}
	} else if !in.stealth && isRepo {
		if isFork, upstreamURL, _ := t.gitUC.DetectFork(ctx); isFork {
			if in.nonInteractive {
				if err := t.fsUseCase.SetupForkExclude(ctx, !in.quiet); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: failed to configure git exclude: %v\n", err)
				}
			} else {
				shouldExclude, err := promptForkExclude(upstreamURL, in.quiet)
				if err != nil && isCanceled(err) {
					fmt.Fprintln(os.Stderr, "Setup canceled.")
					return errCanceled()
				}
				if shouldExclude {
					if err := t.fsUseCase.SetupForkExclude(ctx, !in.quiet); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to configure git exclude: %v\n", err)
					}
				}
			}
		}
	}

	if !in.skipHooks && (!hooksInstalled() || hooksNeedUpdate()) {
		if hooksInstalled() && !in.quiet {
			fmt.Printf("  Updating hooks to version %s...\n", Version)
		}
		isJJ := t.gitUC.IsJujutsuRepo(ctx)
		isColocated := t.gitUC.IsColocatedJJGit(ctx)
		switch {
		case isJJ && !isColocated:
			if !in.quiet {
				printJJAliasInstructions()
			}
		case isColocated:
			if err := t.fsUseCase.InstallJJHooks(ctx); err != nil && !in.quiet {
				fmt.Fprintf(os.Stderr, "\n%s Failed to install jj hooks: %v\n", ui.RenderWarn("⚠"), err)
			} else if !in.quiet {
				fmt.Printf("  Hooks installed (jujutsu mode - no staging)\n")
			}
		default:
			if isRepo {
				hooksParams := domain.HooksInstallParams{
					HookNames:  managedHookNames,
					BeadsHooks: true,
				}
				if err := t.fsUseCase.InstallGitHooks(ctx, hooksParams); err != nil && !in.quiet {
					fmt.Fprintf(os.Stderr, "\n%s Failed to install git hooks to .beads/hooks/: %v\n", ui.RenderWarn("⚠"), err)
				} else if !in.quiet {
					fmt.Printf("  Hooks installed to: .beads/hooks/\n")
				}
			}
		}
	}

	if !in.stealth && !in.skipAgents {
		agentsTemplate, _ := cmd.Flags().GetString("agents-template")
		agentsProfileStr, _ := cmd.Flags().GetString("agents-profile")
		agentsFile, _ := cmd.Flags().GetString("agents-file")
		if agentsFile != "" {
			if err := config.ValidateAgentsFile(agentsFile); err != nil {
				return HandleError("invalid --agents-file: %v", err)
			}
			if err := t.fsUseCase.SetYAMLConfig(ctx, "agents.file", agentsFile); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to persist agents.file to config: %v\n", err)
			}
		}
		resolvedAgentsFile := agentsFile
		if resolvedAgentsFile == "" {
			resolvedAgentsFile = config.SafeAgentsFile()
		}
		isBare := t.gitUC.IsBareGitRepo(ctx)
		if isBare {
			if !in.quiet {
				fmt.Printf("  Skipping %s generation in bare repository\n", resolvedAgentsFile)
			}
		} else {
			_ = t.fsUseCase.AddAgentsInstructions(ctx, domain.AgentsFileParams{
				File:         resolvedAgentsFile,
				Verbose:      !in.quiet,
				TemplatePath: agentsTemplate,
				Profile:      agentsProfileStr,
				HasRemote:    t.remoteURL != "",
				NoPush:       config.GetBool("no-push"),
			})
			if err := t.fsUseCase.InstallClaudeProject(ctx, in.stealth); err != nil && !in.quiet {
				fmt.Fprintf(os.Stderr, "Warning: failed to setup Claude hooks: %v\n", err)
			}
		}
	}

	if !in.stealth && isRepo && t.useLocalBeads {
		commitResult, err := t.gitUC.CommitInitArtifacts(ctx, domain.CommitInitArtifactsParams{
			BeadsDir: ".beads/",
			OptionalPaths: []string{
				config.SafeAgentsFile(),
				filepath.Join(".claude", "settings.json"),
				"CLAUDE.md",
				".gitignore",
			},
			Message:   "bd init: initialize beads issue tracking",
			NoVerify:  true,
			SkipHooks: true,
		})
		switch {
		case err != nil && !in.quiet:
			fmt.Fprintf(os.Stderr, "Warning: failed to commit beads files: %v\n", err)
		case err == nil && commitResult.DidCommit && !in.quiet:
			fmt.Printf("  %s Committed beads files to git\n", ui.RenderPass("✓"))
		}
	}

	if isRepo && !in.quiet {
		if t.gitUC.HasAnyRemotes(ctx) && !t.gitUC.HasUpstream(ctx) {
			fmt.Fprintf(os.Stderr, "\n%s Git upstream not configured\n", ui.RenderWarn("⚠"))
			fmt.Fprintf(os.Stderr, "  For sync workflows, set your upstream with:\n")
			fmt.Fprintf(os.Stderr, "  %s\n\n", ui.RenderAccent("git remote add upstream <repo-url>"))
		}
		if !in.stealth && !in.initRemoteChanged && t.remoteURL == "" {
			printInitNoDoltRemoteWarning(false)
		}
	}

	if in.quiet {
		return nil
	}
	fmt.Printf("\n%s bd initialized successfully!\n\n", ui.RenderPass("✓"))
	fmt.Printf("  Backend: %s\n", ui.RenderAccent(configfile.BackendDolt))
	fmt.Printf("  Mode: %s\n", ui.RenderAccent("proxied-server"))
	fmt.Printf("  Database: %s\n", ui.RenderAccent(t.dbName))
	fmt.Printf("  Issue prefix: %s\n", ui.RenderAccent(t.prefix))
	fmt.Printf("  Issues will be named: %s\n\n", ui.RenderAccent(t.prefix+"-<hash> (e.g., "+t.prefix+"-a3f2dd)"))
	fmt.Printf("Run %s to get started.\n\n", ui.RenderAccent("bd quickstart"))
	return nil
}
