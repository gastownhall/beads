package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// rollingDoltLineageExecutor retains one initialized workspace for each
// scenario/family frontier. Advancing a frontier invokes the next historical
// bd binary against that exact workspace, then fingerprints it only through
// the pinned public Dolt SQL collector.
//
// It is intentionally separate from census generation: the caller decides
// which fresh families to retain and when to serialize the resulting edges.
type rollingDoltLineageExecutor struct {
	root        string
	removeRoot  bool
	initialize  func(context.Context, string) error
	environment func(string) ([]string, error)
	run         func(context.Context, string, string, []string, ...string) error
	observe     func(context.Context, string, lineageScenario, []string, doltObservationEndpoint) (family, error)
	withServer  func(context.Context, string, string, lineageScenario, string, []string, func(int, []string) error) error
	frontiers   map[string]*retainedDoltFrontier
	batch       map[*retainedDoltFrontier]struct{}
	nextOrdinal uint64
}

// doltObservationEndpoint is empty for local data-dir observation. Server
// observations must carry the active loopback endpoint explicitly so the
// collector cannot accidentally inspect an idle local directory.
type doltObservationEndpoint struct {
	host string
	port int
}

type rollingDoltLineageConfig struct {
	Root                 string
	DoltBin              string
	InitializeRepository func(context.Context, string) error
	Environment          func(string) ([]string, error)
	Run                  func(context.Context, string, string, []string, ...string) error
	Observe              func(context.Context, string, lineageScenario, []string, doltObservationEndpoint) (family, error)
	WithServer           func(context.Context, string, string, lineageScenario, string, []string, func(int, []string) error) error
}

type rollingDoltSource struct {
	Scenario lineageScenario
	Version  string
	Binary   string
	FamilyID string
}

type rollingDoltTarget struct {
	Version string
	Binary  string
	Runtime lineageScenario
}

type retainedDoltFrontier struct {
	Scenario  lineageScenario
	Version   string
	FamilyID  string
	Workspace string
	key       string
	ordinal   uint64
}

func newRollingDoltLineageExecutor(config rollingDoltLineageConfig) *rollingDoltLineageExecutor {
	root := config.Root
	removeRoot := false
	if root == "" {
		root, _ = os.MkdirTemp("", "beads-rolling-dolt-lineage-")
		removeRoot = root != ""
	}
	executor := &rollingDoltLineageExecutor{
		root: root, removeRoot: removeRoot, frontiers: make(map[string]*retainedDoltFrontier),
		initialize:  config.InitializeRepository,
		environment: config.Environment,
		run:         config.Run,
		observe:     config.Observe,
	}
	if executor.initialize == nil {
		executor.initialize = initializeCensusRepository
	}
	if executor.environment == nil {
		executor.environment = censusEnvironment
	}
	if executor.run == nil {
		executor.run = runRollingDoltCommand
	}
	if executor.observe == nil {
		executor.observe = observeRollingDoltFamily
	}
	executor.withServer = config.WithServer
	if executor.withServer == nil {
		executor.withServer = rollingDoltServerSession(config.DoltBin)
	}
	return executor
}

// Retain initializes a source release once. Later callers for the same
// scenario/family frontier receive the original workspace instead of an
// independently initialized copy.
func (e *rollingDoltLineageExecutor) Retain(ctx context.Context, source rollingDoltSource) (*retainedDoltFrontier, error) {
	if err := validateRollingDoltSource(source); err != nil {
		return nil, err
	}
	key := rollingDoltFrontierKey(source.Scenario.Name, source.FamilyID)
	if existing := e.frontiers[key]; existing != nil {
		return existing, nil
	}
	if e.root == "" {
		return nil, errors.New("rolling Dolt lineage workspace root is empty")
	}
	if err := os.MkdirAll(e.root, 0o700); err != nil {
		return nil, fmt.Errorf("create rolling Dolt lineage root: %w", err)
	}
	workspace, err := os.MkdirTemp(e.root, "frontier-")
	if err != nil {
		return nil, fmt.Errorf("create retained Dolt workspace: %w", err)
	}
	if err := e.initialize(ctx, workspace); err != nil {
		_ = os.RemoveAll(workspace)
		return nil, fmt.Errorf("initialize retained Dolt workspace: %w", err)
	}
	environment, err := e.environment(workspace)
	if err != nil {
		_ = os.RemoveAll(workspace)
		return nil, fmt.Errorf("configure retained Dolt workspace: %w", err)
	}
	if err := e.initializeDolt(ctx, workspace, source, environment); err != nil {
		_ = os.RemoveAll(workspace)
		return nil, err
	}
	observed, err := e.observeRetained(ctx, workspace, source.Scenario, source.Version, source.Binary, environment)
	if err != nil {
		_ = os.RemoveAll(workspace)
		return nil, fmt.Errorf("observe initialized Dolt family %s: %w", source.FamilyID, err)
	}
	if observed.ID != source.FamilyID {
		_ = os.RemoveAll(workspace)
		return nil, fmt.Errorf("initialized Dolt family %s, want %s", observed.ID, source.FamilyID)
	}
	e.nextOrdinal++
	frontier := &retainedDoltFrontier{
		Scenario: source.Scenario, Version: source.Version, FamilyID: source.FamilyID,
		Workspace: workspace, key: key, ordinal: e.nextOrdinal,
	}
	e.frontiers[key] = frontier
	return frontier, nil
}

// Advance runs an actual command from target.Binary in the retained source
// workspace. It returns the observed target family rather than assuming a
// fresh initialization has the same layout.
func (e *rollingDoltLineageExecutor) Advance(ctx context.Context, frontier *retainedDoltFrontier, target rollingDoltTarget) (lineageTransition, family, error) {
	if frontier == nil || (e.batch == nil && e.frontiers[frontier.key] != frontier) {
		return lineageTransition{}, family{}, errors.New("rolling Dolt frontier is not retained by this executor")
	}
	if e.batch != nil {
		if _, retained := e.batch[frontier]; !retained {
			return lineageTransition{}, family{}, errors.New("rolling Dolt frontier is not retained by this executor")
		}
	}
	if err := validateRollingDoltTarget(frontier, target); err != nil {
		return lineageTransition{}, family{}, err
	}
	environment, err := e.environment(frontier.Workspace)
	if err != nil {
		return lineageTransition{}, family{}, fmt.Errorf("configure retained Dolt workspace: %w", err)
	}
	current, err := e.observeRetained(ctx, frontier.Workspace, frontier.Scenario, frontier.Version, target.Binary, environment)
	if err != nil {
		return lineageTransition{}, family{}, fmt.Errorf("re-observe retained Dolt frontier %s: %v", frontier.FamilyID, err)
	}
	if current.ID != frontier.FamilyID {
		return lineageTransition{}, family{}, fmt.Errorf("retained Dolt frontier %s changed before historical target (observed %s)", frontier.FamilyID, current.ID)
	}
	var observed family
	runAndObserve := func(sessionEnvironment []string, endpoint doltObservationEndpoint) error {
		commandErr := e.run(ctx, target.Binary, frontier.Workspace, sessionEnvironment, "list", "--json")
		var observeErr error
		if endpoint.host != "" && endpoint.port > 0 {
			observed, observeErr = e.observe(ctx, frontier.Workspace, frontier.Scenario, sessionEnvironment, endpoint)
		} else {
			observed, observeErr = e.observeRetained(
				ctx, frontier.Workspace, frontier.Scenario, target.Version, target.Binary, sessionEnvironment,
			)
		}
		if observeErr != nil {
			if commandErr != nil {
				return fmt.Errorf("run historical target %s: %v; observe public Dolt state afterward: %w", target.Version, commandErr, observeErr)
			}
			return observeErr
		}
		if commandErr != nil {
			return fmt.Errorf("run historical target %s: %w", target.Version, commandErr)
		}
		return nil
	}
	var advanceErr error
	runtime := target.Runtime
	if runtime.Name == "" {
		runtime = frontier.Scenario
	}
	if runtime.Mode == "dolt-server" && frontier.Scenario.Mode != "dolt-server" {
		var commandErr error
		sessionErr := e.withServer(ctx, frontier.Workspace, target.Binary, runtime, target.Version, environment, func(_ int, sessionEnvironment []string) error {
			commandErr = e.run(ctx, target.Binary, frontier.Workspace, sessionEnvironment, "list", "--json")
			return nil
		})
		if sessionErr != nil {
			return lineageTransition{}, family{}, fmt.Errorf("advance Dolt server frontier: %w", sessionErr)
		}
		// The retained family may still be legacy even though this target
		// requires a server transport. Observe only after the temporary server
		// has stopped, so the legacy collector never opens its data directory
		// concurrently with Dolt.
		observed, err = e.observeRetained(
			ctx, frontier.Workspace, frontier.Scenario, target.Version, target.Binary, environment,
		)
		if err != nil {
			if commandErr != nil {
				return lineageTransition{}, observed, fmt.Errorf("%v; observe public Dolt state afterward: %w", commandErr, err)
			}
			return lineageTransition{}, observed, err
		}
		advanceErr = commandErr
	} else {
		if runtime.Mode == "dolt-server" {
			advanceErr = e.withServer(ctx, frontier.Workspace, target.Binary, runtime, target.Version, environment, func(port int, sessionEnvironment []string) error {
				return runAndObserve(sessionEnvironment, doltObservationEndpoint{host: "127.0.0.1", port: port})
			})
			if advanceErr != nil {
				advanceErr = fmt.Errorf("advance Dolt server frontier: %w", advanceErr)
			}
		} else {
			advanceErr = runAndObserve(environment, doltObservationEndpoint{})
		}
	}
	if observed.ID == "" || !validMode(observed.Mode) {
		if advanceErr != nil {
			return lineageTransition{}, observed, advanceErr
		}
		return lineageTransition{}, observed, fmt.Errorf("target %s observed invalid Dolt family", target.Version)
	}
	// A retained frontier's family ID was produced by this same public SQL
	// collector before the target command ran. Equality therefore proves an
	// unchanged semantic state without inspecting Dolt's private files.
	if advanceErr != nil && observed.ID == frontier.FamilyID {
		if !isHistoricalProcessExit(advanceErr) {
			return lineageTransition{}, family{}, advanceErr
		}
		return lineageTransition{}, observed, advanceErr
	}
	if advanceErr != nil && !isHistoricalProcessExit(advanceErr) {
		return lineageTransition{}, family{}, advanceErr
	}
	transition := e.moveFrontier(frontier, target.Version, runtime.Mode, observed)
	return transition, observed, advanceErr
}

func (e *rollingDoltLineageExecutor) moveFrontier(frontier *retainedDoltFrontier, version, runtimeMode string, observed family) lineageTransition {
	transition := lineageTransition{
		FromFamilyID: frontier.FamilyID, TargetVersion: version,
		Scenario: frontier.Scenario.Name, Mode: frontier.Scenario.Mode, RuntimeMode: runtimeMode, ToFamilyID: observed.ID,
	}
	if e.frontiers[frontier.key] == frontier {
		delete(e.frontiers, frontier.key)
	}
	frontier.Version = version
	frontier.FamilyID = observed.ID
	frontier.key = rollingDoltFrontierKey(frontier.Scenario.Name, observed.ID)
	if e.batch != nil {
		return transition
	}
	if other := e.frontiers[frontier.key]; other != nil && other != frontier {
		_ = os.RemoveAll(frontier.Workspace)
		return transition
	}
	e.frontiers[frontier.key] = frontier
	return transition
}

// BeginBatch retains every start-of-release frontier until all have been
// advanced. Two old workspaces may temporarily share a post-target family;
// they are deduplicated only by EndBatch after each independent attempt ran.
func (e *rollingDoltLineageExecutor) BeginBatch() {
	if e.batch != nil {
		panic("rolling Dolt batch already active")
	}
	e.batch = make(map[*retainedDoltFrontier]struct{}, len(e.frontiers))
	for _, frontier := range e.frontiers {
		e.batch[frontier] = struct{}{}
	}
}

func (e *rollingDoltLineageExecutor) EndBatch() map[string]*retainedDoltFrontier {
	if e.batch == nil {
		return nil
	}
	frontiers := make([]*retainedDoltFrontier, 0, len(e.batch))
	for frontier := range e.batch {
		frontiers = append(frontiers, frontier)
	}
	sort.Slice(frontiers, func(i, j int) bool {
		if frontiers[i].key != frontiers[j].key {
			return frontiers[i].key < frontiers[j].key
		}
		return frontiers[i].ordinal < frontiers[j].ordinal
	})
	next := make(map[string]*retainedDoltFrontier, len(frontiers))
	for _, frontier := range frontiers {
		if existing := next[frontier.key]; existing != nil {
			_ = os.RemoveAll(frontier.Workspace)
			continue
		}
		next[frontier.key] = frontier
	}
	e.frontiers = next
	e.batch = nil
	return next
}

func (e *rollingDoltLineageExecutor) observeRetained(
	ctx context.Context,
	workspace string,
	scenario lineageScenario,
	version string,
	binary string,
	environment []string,
) (family, error) {
	topology, err := recognizeFreshTopology(workspace)
	if (err == nil && topology.Mode != "dolt-server") || (err != nil && scenario.Mode != "dolt-server") {
		return e.observe(ctx, workspace, scenario, environment, doltObservationEndpoint{})
	}
	serverScenario := scenario
	if err == nil {
		serverScenario = lineageScenarioMapMust(rollingServerScenario)
	}
	var observed family
	err = e.withServer(ctx, workspace, binary, serverScenario, version, environment, func(port int, sessionEnvironment []string) error {
		var err error
		observed, err = e.observe(ctx, workspace, scenario, sessionEnvironment, doltObservationEndpoint{host: "127.0.0.1", port: port})
		return err
	})
	return observed, err
}

func (e *rollingDoltLineageExecutor) initializeDolt(ctx context.Context, workspace string, source rollingDoltSource, environment []string) error {
	if source.Scenario.Mode != "dolt-server" {
		args, err := rollingDoltInitArgs(source.Scenario, source.Version, "census", 0)
		if err != nil {
			return err
		}
		if err := e.run(ctx, source.Binary, workspace, environment, args...); err != nil {
			return fmt.Errorf("initialize %s at %s: %w", source.Scenario.Name, source.Version, err)
		}
		return nil
	}
	return e.withServer(ctx, workspace, source.Binary, source.Scenario, source.Version, environment, func(port int, sessionEnvironment []string) error {
		args, err := rollingDoltInitArgs(source.Scenario, source.Version, "census", port)
		if err != nil {
			return err
		}
		if err := e.run(ctx, source.Binary, workspace, sessionEnvironment, args...); err != nil {
			return fmt.Errorf("initialize %s at %s: %w", source.Scenario.Name, source.Version, err)
		}
		return nil
	})
}

func (e *rollingDoltLineageExecutor) WorkspaceCount() int { return len(e.frontiers) }

func (e *rollingDoltLineageExecutor) Close() error {
	var first error
	frontiers := e.frontiers
	if e.batch != nil {
		frontiers = make(map[string]*retainedDoltFrontier, len(e.batch))
		for frontier := range e.batch {
			frontiers[frontier.Workspace] = frontier
		}
	}
	seen := make(map[string]bool, len(frontiers))
	for _, frontier := range frontiers {
		if frontier == nil || seen[frontier.Workspace] {
			continue
		}
		seen[frontier.Workspace] = true
		if err := os.RemoveAll(frontier.Workspace); err != nil && first == nil {
			first = err
		}
	}
	if e.removeRoot && e.root != "" {
		if err := os.RemoveAll(e.root); err != nil && first == nil {
			first = err
		}
	}
	e.frontiers = make(map[string]*retainedDoltFrontier)
	e.batch = nil
	return first
}

func rollingDoltInitArgs(scenario lineageScenario, version, prefix string, serverPort int) ([]string, error) {
	spec, err := rollingDoltScenarioSpec(scenario)
	if err != nil {
		return nil, err
	}
	return spec.initArgs(version, prefix, serverPort)
}

func rollingDoltScenarioSpec(scenario lineageScenario) (scenarioSpec, error) {
	switch scenario.Name {
	case rollingLegacyScenario:
		if scenario.Mode == "dolt-legacy" {
			return freshScenarioByNameMust(freshDoltLegacyScenario), nil
		}
	case rollingServerScenario:
		if scenario.Mode == "dolt-server" {
			return freshScenarioByNameMust(freshDoltServerScenario), nil
		}
	case rollingEmbeddedScenario:
		if scenario.Mode == "dolt-embedded" {
			return freshScenarioByNameMust(freshDoltEmbeddedScenario), nil
		}
	}
	return scenarioSpec{}, fmt.Errorf("unsupported rolling Dolt scenario %q/%q", scenario.Name, scenario.Mode)
}

func freshScenarioByNameMust(name string) scenarioSpec {
	scenario, ok := freshScenarioByName(name)
	if !ok {
		panic("missing fixed fresh scenario " + name)
	}
	return scenario
}

func validateRollingDoltSource(source rollingDoltSource) error {
	if source.Binary == "" || source.FamilyID == "" {
		return errors.New("rolling Dolt source requires a binary and family ID")
	}
	if _, err := parseReleaseVersion(source.Version); err != nil {
		return err
	}
	if !source.Scenario.compatible(source.Version) {
		return fmt.Errorf("source version %s is outside %s", source.Version, source.Scenario.Name)
	}
	_, err := rollingDoltScenarioSpec(source.Scenario)
	return err
}

func validateRollingDoltTarget(frontier *retainedDoltFrontier, target rollingDoltTarget) error {
	if target.Binary == "" {
		return errors.New("rolling Dolt target binary is empty")
	}
	if _, err := parseReleaseVersion(target.Version); err != nil {
		return err
	}
	if !frontier.Scenario.compatible(target.Version) {
		return fmt.Errorf("target version %s is outside %s", target.Version, frontier.Scenario.Name)
	}
	if compareReleaseVersions(target.Version, frontier.Version) <= 0 {
		return fmt.Errorf("target version %s does not advance source %s", target.Version, frontier.Version)
	}
	return nil
}

func rollingDoltFrontierKey(scenario, familyID string) string { return scenario + "\x00" + familyID }

// rollingDoltServerCloseError reports a failed server cleanup without exposing
// a prior action failure through the error chain. A close failure means the
// server session is no longer trustworthy infrastructure, even when the
// action itself ended with a historical process exit.
type rollingDoltServerCloseError struct {
	actionErr error
	closeErr  error
}

func (err rollingDoltServerCloseError) Error() string {
	if err.actionErr != nil {
		return fmt.Sprintf("close pinned external Dolt server: %v; action before close: %v", err.closeErr, err.actionErr)
	}
	return fmt.Sprintf("close pinned external Dolt server: %v", err.closeErr)
}

func (err rollingDoltServerCloseError) Unwrap() error { return err.closeErr }

func combineRollingDoltServerSessionResult(actionErr, closeErr error) error {
	if closeErr == nil {
		return actionErr
	}
	return rollingDoltServerCloseError{actionErr: actionErr, closeErr: closeErr}
}

func rollingDoltServerSession(
	doltBin string,
) func(context.Context, string, string, lineageScenario, string, []string, func(int, []string) error) error {
	return func(ctx context.Context, workspace, historicalBinary string, scenario lineageScenario, version string, environment []string, action func(int, []string) error) (err error) {
		spec, err := rollingDoltScenarioSpec(scenario)
		if err != nil {
			return err
		}
		requestedPort, err := spec.bootstrapServerPort(version)
		if err != nil {
			return err
		}
		server, err := startDoltServerFallback(ctx, workspace, doltBin, environment, requestedPort)
		if err != nil {
			return err
		}
		defer func() {
			err = combineRollingDoltServerSessionResult(err, server.Close())
		}()
		if err := action(server.port, doltFallbackEnv(environment, server.port)); err != nil {
			return err
		}
		return nil
	}
}

func runRollingDoltCommand(ctx context.Context, binary, workspace string, environment []string, args ...string) error {
	commandContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	output, err := runHistoricalCommandIn(commandContext, binary, workspace, environment, args...)
	if err != nil {
		if commandContext.Err() != nil {
			return fmt.Errorf("historical Dolt command context: %w", commandContext.Err())
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return historicalProcessExit(fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output))))
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func observeRollingDoltFamily(
	ctx context.Context,
	workspace string,
	_ lineageScenario,
	environment []string,
	endpoint doltObservationEndpoint,
) (family, error) {
	topology, err := recognizeFreshTopology(workspace)
	if err != nil {
		return family{}, err
	}
	binary, err := resolveDoltRuntime(ctx, "")
	if err != nil {
		return family{}, err
	}
	var layout json.RawMessage
	switch topology.Mode {
	case "dolt-legacy":
		layout, err = collectLegacyDoltLayout(ctx, workspace, binary, environment, topology.Markers, topology.DoltDatabase, topology.MetadataDatabase)
	case "dolt-server":
		if endpoint.host == "" || endpoint.port < 1 {
			return family{}, errors.New("active Dolt server observation has no endpoint")
		}
		database, derr := discoverActiveDoltServerDatabase(ctx, binary, workspace, environment, endpoint.host, endpoint.port)
		if derr != nil {
			return family{}, fmt.Errorf("discover active Dolt server database: %w", derr)
		}
		runner := pinnedDoltServerRunner(binary, workspace, database, environment, endpoint.port)
		runner.host = endpoint.host
		var embeddedRunner func(string) doltSQLRunner
		if hasTopologyMarker(topology.Markers, "directory:.beads/dolt") &&
			hasTopologyMarker(topology.Markers, "directory:.beads/embeddeddolt") {
			embeddedDir := filepath.Join(workspace, ".beads", "embeddeddolt")
			embeddedRunner = func(database string) doltSQLRunner {
				return pinnedDoltRunner{
					binary: binary, workspace: workspace, dataDir: embeddedDir,
					database: database, environment: environment,
				}
			}
		}
		layout, err = collectServerDoltLayout(ctx, topology.Markers, runner, embeddedRunner)
	case "dolt-embedded":
		metadata, merr := readStorageMetadata(filepath.Join(workspace, ".beads", "metadata.json"))
		if merr != nil {
			return family{}, merr
		}
		if metadata.DoltDatabase == "" {
			return family{}, errors.New("embedded Dolt metadata has no database name")
		}
		runner := pinnedDoltRunner{
			binary: binary, workspace: workspace, dataDir: filepath.Join(workspace, ".beads", "embeddeddolt"),
			database: metadata.DoltDatabase, environment: environment,
		}
		fingerprint, ferr := collectDolt(ctx, runner)
		if ferr != nil {
			return family{}, ferr
		}
		layout, err = marshalDoltLayout(topology.Markers, fingerprint)
	default:
		return family{}, fmt.Errorf("retained workspace has non-Dolt topology %q", topology.Mode)
	}
	if err != nil {
		return family{}, err
	}
	layout, err = attachSQLiteEvidenceToDoltLayout(workspace, topology, layout)
	if err != nil {
		return family{}, err
	}
	return finishRollingDoltFamily(topology.Mode, layout)
}

func finishRollingDoltFamily(mode string, layout json.RawMessage) (family, error) {
	canonicalLayout, err := canonicalJSON(layout)
	if err != nil {
		return family{}, err
	}
	id, err := familyID(mode, canonicalLayout)
	if err != nil {
		return family{}, err
	}
	return family{ID: id, Mode: mode, Layout: canonicalLayout}, nil
}

func hasTopologyMarker(markers []string, expected string) bool {
	for _, marker := range markers {
		if marker == expected {
			return true
		}
	}
	return false
}
