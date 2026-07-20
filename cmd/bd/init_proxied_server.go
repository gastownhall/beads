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
		dbName:    dbName,
		projectID: projectID,
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

	initUOWProvider, err := newProxiedServerUOWProvider(ctx, beadsDir)
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

	err = uow.RunTx(ctx, initUOWProvider, func(ctx context.Context, uw uow.UnitOfWork) (string, error) {
		bootstrapParams := domain.BootstrapProjectParams{
			Prefix:         prefix,
			ProjectID:      projectID,
			BdVersion:      Version,
			LastImportTime: time.Now(),
			RepoID:         repoID,
			CloneID:        cloneID,
		}
		if remoteURL != "" {
			bootstrapParams.RemoteName = "origin"
			bootstrapParams.RemoteURL = remoteURL
		}

		if _, err := uw.BootstrapUseCase().BootstrapProject(ctx, bootstrapParams); err != nil {
			return "", fmt.Errorf("bootstrap project: %w", err)
		}

		return "bd init", nil
	})
	if err != nil {
		return HandleError("%v", err)
	}

	return runInitProxiedServerTail(cmd, ctx, in, runInitTailContext{
		beadsDir:      beadsDir,
		prefix:        prefix,
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

type proxiedMetadataInputs struct {
	dbName    string
	projectID string
}

func composeProxiedServerMetadataJSON(in proxiedMetadataInputs) ([]byte, error) {
	cfg := configfile.DefaultConfig()
	cfg.Backend = configfile.BackendDolt
	cfg.Database = "dolt"
	cfg.DoltDatabase = in.dbName
	cfg.DoltMode = configfile.DoltModeProxiedServer
	cfg.ProjectID = in.projectID

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
			printInitNoDoltRemoteWarning()
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
