package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// retainedSQLiteWorkspace is one in-place historical storage state. It is
// deliberately keyed by its currently observed family, not a release: when
// migrations merge two families, only one representative workspace advances.
type retainedSQLiteWorkspace struct {
	path        string
	environment []string
}

type rollingSQLiteRuntime struct {
	acquire     func(context.Context, catalogEntry, string, string, acquisition) (string, error)
	initialize  func(context.Context, string, catalogEntry, acquisition, family) (*retainedSQLiteWorkspace, family, error)
	read        func(context.Context, string, catalogEntry, *retainedSQLiteWorkspace) error
	snapshot    func(*retainedSQLiteWorkspace) (string, error)
	fingerprint func(*retainedSQLiteWorkspace) (family, error)
	remove      func(*retainedSQLiteWorkspace)
}

// generateRollingSQLiteLineage produces real, in-place SQLite migration
// observations. It intentionally returns rolling-only families separately:
// census currently records one observation per version/scenario, while a
// lineage frontier can produce multiple observations for one target version.
func generateRollingSQLiteLineage(
	ctx context.Context,
	releases catalog,
	fresh census,
	cache string,
) (lineageSet, []family, error) {
	return generateRollingSQLiteLineageWithRuntime(ctx, releases, fresh, cache, defaultRollingSQLiteRuntime())
}

func generateRollingSQLiteLineageWithRuntime(
	ctx context.Context,
	releases catalog,
	fresh census,
	cache string,
	runtime rollingSQLiteRuntime,
) (lineageSet, []family, error) {
	if err := validateRollingSQLiteRuntime(runtime); err != nil {
		return lineageSet{}, nil, err
	}
	scenario, err := rollingSQLiteLineageScenario()
	if err != nil {
		return lineageSet{}, nil, err
	}
	freshByVersion, acquisitions, allFamilies, err := freshSQLiteFamiliesByVersion(fresh)
	if err != nil {
		return lineageSet{}, nil, err
	}
	_, doltAcquisitions, _, err := freshDoltFamiliesByVersion(fresh)
	if err != nil {
		return lineageSet{}, nil, err
	}

	frontier := make(map[string]*retainedSQLiteWorkspace)
	defer func() {
		for _, workspace := range frontier {
			runtime.remove(workspace)
		}
	}()
	rollingOnly := make(map[string]family)
	transitions := make([]lineageTransition, 0)
	outcomes := make([]lineageOutcome, 0)

	for _, entry := range releases.Versions {
		freshFamilies := freshByVersion[entry.Version]
		if !scenario.compatible(entry.Version) {
			continue
		}
		if len(frontier) == 0 && len(freshFamilies) == 0 {
			continue
		}

		acquired, err := rollingSQLiteTargetAcquisition(entry.Version, acquisitions, doltAcquisitions)
		if err != nil {
			return lineageSet{}, nil, err
		}
		binary, err := runtime.acquire(ctx, entry, cache, rollingSQLiteTargetMode(entry.Version, acquisitions), acquired)
		if err != nil {
			return lineageSet{}, nil, fmt.Errorf("%s: acquire authenticated binary: %w", entry.Version, err)
		}

		next, err := advanceSQLiteFamilyFrontier(ctx, runtime, binary, acquired, entry, frontier, &transitions, &outcomes, rollingOnly, allFamilies)
		if err != nil {
			return lineageSet{}, nil, err
		}
		frontier = next

		for _, expected := range freshFamilies {
			if _, exists := frontier[expected.ID]; exists {
				continue
			}
			workspace, observed, err := runtime.initialize(ctx, binary, entry, acquired, expected)
			if err != nil {
				return lineageSet{}, nil, fmt.Errorf("%s: initialize retained SQLite family %s: %w", entry.Version, expected.ID, err)
			}
			if observed.ID != expected.ID || observed.Mode != "sqlite" {
				runtime.remove(workspace)
				return lineageSet{}, nil, fmt.Errorf("%s: initialized SQLite family %s, want %s", entry.Version, observed.ID, expected.ID)
			}
			if existing, duplicate := frontier[observed.ID]; duplicate {
				// The family is already represented by a rolling predecessor.
				// Keep that state and discard this equivalent fresh representative.
				_ = existing
				runtime.remove(workspace)
				continue
			}
			frontier[observed.ID] = workspace
		}

	}

	sortLineageTransitions(transitions)
	sortLineageOutcomes(outcomes)
	families := make([]family, 0, len(rollingOnly))
	for _, candidate := range rollingOnly {
		families = append(families, candidate)
	}
	sort.Slice(families, func(i, j int) bool { return families[i].ID < families[j].ID })
	return lineageSet{SchemaVersion: lineageSchemaVersion, Transitions: transitions, Outcomes: outcomes}, families, nil
}

func rollingSQLiteTargetMode(version string, sqlite map[string]acquisition) string {
	if _, ok := sqlite[version]; ok {
		return "sqlite"
	}
	return "dolt-server"
}

func rollingSQLiteTargetAcquisition(version string, sqlite map[string]acquisition, dolt map[string]map[string]acquisition) (acquisition, error) {
	if acquired, ok := sqlite[version]; ok {
		return acquired, nil
	}
	if acquired, ok := dolt[version][rollingServerScenario]; ok {
		return acquired, nil
	}
	return acquisition{}, fmt.Errorf("%s: no authenticated SQLite target acquisition", version)
}

func validateRollingSQLiteRuntime(runtime rollingSQLiteRuntime) error {
	if runtime.acquire == nil || runtime.initialize == nil || runtime.read == nil || runtime.snapshot == nil || runtime.fingerprint == nil || runtime.remove == nil {
		return fmt.Errorf("rolling SQLite runtime is incomplete")
	}
	return nil
}

func rollingSQLiteLineageScenario() (lineageScenario, error) {
	scenarios, err := lineageScenarioMap()
	if err != nil {
		return lineageScenario{}, err
	}
	scenario, ok := scenarios[rollingSQLiteScenario]
	if !ok || scenario.Mode != "sqlite" {
		return lineageScenario{}, fmt.Errorf("SQLite rolling lineage scenario is unavailable")
	}
	return scenario, nil
}

func freshSQLiteFamiliesByVersion(result census) (map[string][]family, map[string]acquisition, map[string]family, error) {
	families := make(map[string]family, len(result.Families))
	for _, candidate := range result.Families {
		families[candidate.ID] = candidate
	}
	byVersion := make(map[string][]family)
	acquisitions := make(map[string]acquisition)
	seen := make(map[string]bool)
	for _, observed := range result.Observations {
		if observed.Scenario != freshSQLiteScenario {
			continue
		}
		candidate, ok := families[observed.FamilyID]
		if !ok || candidate.Mode != "sqlite" {
			return nil, nil, nil, fmt.Errorf("%s/%s: fresh SQLite observation has no SQLite family", observed.Version, observed.Scenario)
		}
		if prior, exists := acquisitions[observed.Version]; exists && prior != observed.Acquisition {
			return nil, nil, nil, fmt.Errorf("%s: fresh SQLite acquisition differs by observation", observed.Version)
		}
		acquisitions[observed.Version] = observed.Acquisition
		key := observed.Version + "\x00" + candidate.ID
		if seen[key] {
			continue
		}
		seen[key] = true
		byVersion[observed.Version] = append(byVersion[observed.Version], candidate)
	}
	for version := range byVersion {
		sort.Slice(byVersion[version], func(i, j int) bool { return byVersion[version][i].ID < byVersion[version][j].ID })
	}
	return byVersion, acquisitions, families, nil
}

func advanceSQLiteFamilyFrontier(
	ctx context.Context,
	runtime rollingSQLiteRuntime,
	binary string,
	acquired acquisition,
	entry catalogEntry,
	frontier map[string]*retainedSQLiteWorkspace,
	transitions *[]lineageTransition,
	outcomes *[]lineageOutcome,
	rollingOnly map[string]family,
	allFamilies map[string]family,
) (map[string]*retainedSQLiteWorkspace, error) {
	fromFamilies := make([]string, 0, len(frontier))
	for familyID := range frontier {
		fromFamilies = append(fromFamilies, familyID)
	}
	sort.Strings(fromFamilies)
	next := make(map[string]*retainedSQLiteWorkspace, len(frontier))
	for _, fromID := range fromFamilies {
		workspace := frontier[fromID]
		before, err := runtime.snapshot(workspace)
		if err != nil {
			return nil, fmt.Errorf("%s: snapshot SQLite source for family %s: %w", entry.Version, fromID, err)
		}
		observed, err := runtime.fingerprint(workspace)
		if err != nil {
			return nil, fmt.Errorf("%s: re-fingerprint retained SQLite frontier %s: %v", entry.Version, fromID, err)
		}
		if observed.ID != fromID || observed.Mode != "sqlite" {
			return nil, fmt.Errorf("%s: retained SQLite frontier %s changed before historical target (observed %s/%s)", entry.Version, fromID, observed.ID, observed.Mode)
		}
		if err := runtime.read(ctx, binary, entry, workspace); err != nil {
			if !isHistoricalProcessExit(err) {
				return nil, fmt.Errorf("%s: public SQLite read failed for family %s: %w", entry.Version, fromID, err)
			}
			after, snapshotErr := runtime.snapshot(workspace)
			if snapshotErr != nil {
				return nil, fmt.Errorf("%s: public SQLite read failed for family %s (%v); snapshot afterward: %w", entry.Version, fromID, err, snapshotErr)
			}
			if before != after {
				observed, fingerprintErr := runtime.fingerprint(workspace)
				if fingerprintErr != nil {
					return nil, fmt.Errorf("%s: fingerprint partially migrated SQLite family %s: %w", entry.Version, fromID, fingerprintErr)
				}
				if observed.Mode != "sqlite" || observed.ID == "" {
					return nil, fmt.Errorf("%s: partial SQLite migration produced invalid family %q/%q", entry.Version, observed.ID, observed.Mode)
				}
				if observed.ID == fromID {
					*outcomes = append(*outcomes, lineageOutcome{
						FromFamilyID: fromID, TargetVersion: entry.Version, Scenario: rollingSQLiteScenario,
						Mode: "sqlite", RuntimeMode: "sqlite", Outcome: lineageOutcomeUnchangedRefusal, Acquisition: acquired,
					})
					next[fromID] = workspace
					continue
				}
				*outcomes = append(*outcomes, lineageOutcome{
					FromFamilyID: fromID, TargetVersion: entry.Version, Scenario: rollingSQLiteScenario,
					Mode: "sqlite", RuntimeMode: "sqlite", Outcome: lineageOutcomeMutatingFailure, ToFamilyID: observed.ID, Acquisition: acquired,
				})
				if _, isFreshFamily := allFamilies[observed.ID]; !isFreshFamily {
					rollingOnly[observed.ID] = observed
				}
				if _, duplicate := next[observed.ID]; duplicate {
					runtime.remove(workspace)
					continue
				}
				next[observed.ID] = workspace
				continue
			}
			// A historical binary can reject a newer on-disk version before it
			// opens the database. That is a non-edge: retain the untouched
			// family so a later compatible release can still advance it.
			next[fromID] = workspace
			*outcomes = append(*outcomes, lineageOutcome{
				FromFamilyID: fromID, TargetVersion: entry.Version, Scenario: rollingSQLiteScenario,
				Mode: "sqlite", RuntimeMode: "sqlite", Outcome: lineageOutcomeUnchangedRefusal, Acquisition: acquired,
			})
			continue
		}
		observed, err = runtime.fingerprint(workspace)
		if err != nil {
			return nil, fmt.Errorf("%s: fingerprint rolled SQLite family %s: %w", entry.Version, fromID, err)
		}
		if observed.Mode != "sqlite" || observed.ID == "" {
			return nil, fmt.Errorf("%s: public SQLite read produced invalid family %q/%q", entry.Version, observed.ID, observed.Mode)
		}
		*transitions = append(*transitions, lineageTransition{
			FromFamilyID: fromID, TargetVersion: entry.Version, Scenario: rollingSQLiteScenario,
			Mode: "sqlite", RuntimeMode: "sqlite", ToFamilyID: observed.ID, Acquisition: acquired,
		})
		if _, isFreshFamily := allFamilies[observed.ID]; !isFreshFamily {
			rollingOnly[observed.ID] = observed
		}
		if existing, duplicate := next[observed.ID]; duplicate {
			_ = existing
			runtime.remove(workspace)
			continue
		}
		next[observed.ID] = workspace
	}
	return next, nil
}

func defaultRollingSQLiteRuntime() rollingSQLiteRuntime {
	return rollingSQLiteRuntime{
		acquire:    acquireRecordedBinary,
		initialize: initializeRetainedSQLiteWorkspace,
		read:       runRetainedSQLiteRead,
		snapshot:   snapshotRetainedSQLiteSource,
		fingerprint: func(workspace *retainedSQLiteWorkspace) (family, error) {
			return fingerprintRetainedSQLiteWorkspace(workspace)
		},
		remove: func(workspace *retainedSQLiteWorkspace) { _ = os.RemoveAll(workspace.path) },
	}
}

func initializeRetainedSQLiteWorkspace(
	ctx context.Context,
	binary string,
	entry catalogEntry,
	_ acquisition,
	_ family,
) (*retainedSQLiteWorkspace, family, error) {
	workspace, err := os.MkdirTemp("", "beads-schema-census-rolling-sqlite-")
	if err != nil {
		return nil, family{}, err
	}
	failed := true
	defer func() {
		if failed {
			_ = os.RemoveAll(workspace)
		}
	}()
	if err := initializeCensusRepository(ctx, workspace); err != nil {
		return nil, family{}, err
	}
	environment, err := censusEnvironment(workspace)
	if err != nil {
		return nil, family{}, err
	}
	scenario, ok := freshScenarioByName(freshSQLiteScenario)
	if !ok {
		return nil, family{}, fmt.Errorf("fresh SQLite scenario is unavailable")
	}
	args, err := scenario.initArgs(entry.Version, "census", 0)
	if err != nil {
		return nil, family{}, err
	}
	initContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if output, err := runHistoricalCommandIn(initContext, binary, workspace, environment, args...); err != nil {
		return nil, family{}, fmt.Errorf("initialize SQLite workspace: %w: %s", err, strings.TrimSpace(string(output)))
	}
	retained := &retainedSQLiteWorkspace{path: workspace, environment: environment}
	observed, err := fingerprintRetainedSQLiteWorkspace(retained)
	if err != nil {
		return nil, family{}, err
	}
	failed = false
	return retained, observed, nil
}

// runRetainedSQLiteRead uses only the public CLI. It intentionally performs
// no write command; any migration is therefore the historical binary's normal
// open-on-read behavior.
func runRetainedSQLiteRead(ctx context.Context, binary string, _ catalogEntry, workspace *retainedSQLiteWorkspace) error {
	readContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	if output, err := runHistoricalCommandIn(readContext, binary, workspace.path, workspace.environment, "list"); err != nil {
		if readContext.Err() != nil {
			return fmt.Errorf("bd list context: %w", readContext.Err())
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return historicalProcessExit(fmt.Errorf("bd list: %w: %s", err, strings.TrimSpace(string(output))))
		}
		return fmt.Errorf("bd list: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func fingerprintRetainedSQLiteWorkspace(workspace *retainedSQLiteWorkspace) (family, error) {
	topology, err := recognizeFreshTopology(workspace.path)
	if err != nil {
		return family{}, err
	}
	if topology.Mode != "sqlite" {
		return family{}, fmt.Errorf("retained workspace storage mode %q, want sqlite", topology.Mode)
	}
	canonical, err := collectSQLiteLayout(workspace.path, topology)
	if err != nil {
		return family{}, err
	}
	id, err := familyID("sqlite", canonical)
	if err != nil {
		return family{}, err
	}
	return family{ID: id, Mode: "sqlite", Layout: canonical}, nil
}

func snapshotRetainedSQLiteSource(workspace *retainedSQLiteWorkspace) (string, error) {
	root := filepath.Join(workspace.path, ".beads")
	hasher := sha256.New()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		_, _ = io.WriteString(hasher, relative+"\x00"+info.Mode().String()+"\x00")
		if entry.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(hasher, target)
		} else if entry.Type().IsRegular() {
			file, err := os.Open(path) //nolint:gosec // path is confined to the retained temporary workspace.
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(hasher, file)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		}
		_, _ = io.WriteString(hasher, "\x00")
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("snapshot retained SQLite source: %w", err)
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}
