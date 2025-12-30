// Package molecules handles loading template molecules from molecules.jsonl catalogs.
//
// Template molecules are read-only issue templates that can be instantiated as
// work items. They live in a separate molecules.jsonl file, distinct from work
// items in issues.jsonl.
//
// # Hierarchical Loading
//
// Molecules are loaded from multiple locations in priority order (later overrides earlier):
//  1. Built-in molecules (shipped with bd binary)
//  2. Town-level: $GT_ROOT/.beads/molecules.jsonl (if orchestrator detected via GT_ROOT)
//  3. User-level: ~/.beads/molecules.jsonl
//  4. Project-level: .beads/molecules.jsonl in the current project
//
// # Key Properties
//
//   - Templates are marked with is_template: true
//   - Templates are read-only (mutations are rejected)
//   - bd list excludes templates by default
//   - bd molecule list shows the catalog
package molecules

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/steveyegge/beads/internal/debug"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/sqlite"
	"github.com/steveyegge/beads/internal/types"
)

// MoleculeFileName is the canonical name for molecule catalog files.
const MoleculeFileName = "molecules.jsonl"

// LoadResult contains statistics about the molecule loading operation.
type LoadResult struct {
	Loaded       int      // Number of molecules successfully loaded
	Skipped      int      // Number of molecules skipped (already exist or errors)
	Sources      []string // Paths that were loaded from
	BuiltinCount int      // Number of built-in molecules loaded
}

// Loader handles loading molecule catalogs from hierarchical locations.
type Loader struct {
	store storage.Storage
}

// NewLoader creates a new molecule loader for the given storage.
func NewLoader(store storage.Storage) *Loader {
	return &Loader{store: store}
}

// LoadAll loads molecules from all available catalog locations.
// Molecules are loaded in priority order: built-in < town < user < project.
// Later sources override earlier ones if they have the same ID.
func (l *Loader) LoadAll(ctx context.Context, beadsDir string) (*LoadResult, error) {
	result := &LoadResult{
		Sources: make([]string, 0),
	}

	// 1. Load built-in molecules (embedded in binary)
	builtinMolecules := getBuiltinMolecules()
	if len(builtinMolecules) > 0 {
		count, err := l.loadMolecules(ctx, builtinMolecules)
		if err != nil {
			debug.Logf("warning: failed to load built-in molecules: %v", err)
		} else {
			result.BuiltinCount = count
			result.Loaded += count
			result.Sources = append(result.Sources, "<built-in>")
		}
	}

	// 2. Load town-level molecules ($GT_ROOT/.beads/molecules.jsonl)
	townPath := getTownMoleculesPath()
	if townPath != "" {
		if molecules, err := loadMoleculesFromFile(townPath); err == nil && len(molecules) > 0 {
			count, err := l.loadMolecules(ctx, molecules)
			if err != nil {
				debug.Logf("warning: failed to load town molecules: %v", err)
			} else {
				result.Loaded += count
				result.Sources = append(result.Sources, townPath)
			}
		}
	}

	// 3. Load user-level molecules (~/.beads/molecules.jsonl)
	userPath := getUserMoleculesPath()
	if userPath != "" && userPath != townPath {
		if molecules, err := loadMoleculesFromFile(userPath); err == nil && len(molecules) > 0 {
			count, err := l.loadMolecules(ctx, molecules)
			if err != nil {
				debug.Logf("warning: failed to load user molecules: %v", err)
			} else {
				result.Loaded += count
				result.Sources = append(result.Sources, userPath)
			}
		}
	}

	// 4. Load project-level molecules (.beads/molecules.jsonl)
	if beadsDir != "" {
		projectPath := filepath.Join(beadsDir, MoleculeFileName)
		if molecules, err := loadMoleculesFromFile(projectPath); err == nil && len(molecules) > 0 {
			count, err := l.loadMolecules(ctx, molecules)
			if err != nil {
				debug.Logf("warning: failed to load project molecules: %v", err)
			} else {
				result.Loaded += count
				result.Sources = append(result.Sources, projectPath)
			}
		}
	}

	return result, nil
}

// loadMolecules loads a slice of molecules into the store.
// Each molecule is marked as a template (IsTemplate = true).
// Returns the number of molecules successfully loaded.
func (l *Loader) loadMolecules(ctx context.Context, molecules []*types.Issue) (int, error) {
	// Filter out molecules that already exist
	var newMolecules []*types.Issue
	for _, mol := range molecules {
		// Ensure molecule is marked as a template
		mol.IsTemplate = true

		// Check if molecule already exists
		existing, err := l.store.GetIssue(ctx, mol.ID)
		if err == nil && existing != nil {
			// Already exists - skip (or could update if newer)
			debug.Logf("molecule %s already exists, skipping", mol.ID)
			continue
		}

		newMolecules = append(newMolecules, mol)
	}

	if len(newMolecules) == 0 {
		return 0, nil
	}

	// Use batch creation with prefix validation skipped.
	// Molecules have their own ID namespace (mol-*) independent of project prefix.
	if sqliteStore, ok := l.store.(*sqlite.SQLiteStorage); ok {
		opts := sqlite.BatchCreateOptions{
			SkipPrefixValidation: true, // Molecules use their own prefix
		}
		if err := sqliteStore.CreateIssuesWithFullOptions(ctx, newMolecules, "molecules-loader", opts); err != nil {
			return 0, fmt.Errorf("batch create molecules: %w", err)
		}
		return len(newMolecules), nil
	}

	// Fallback for non-SQLite stores (e.g., memory storage in tests)
	loaded := 0
	for _, mol := range newMolecules {
		if err := l.store.CreateIssue(ctx, mol, "molecules-loader"); err != nil {
			debug.Logf("failed to load molecule %s: %v", mol.ID, err)
			continue
		}
		loaded++
	}

	return loaded, nil
}

// loadMoleculesFromFile loads molecules from a JSONL file.
func loadMoleculesFromFile(path string) ([]*types.Issue, error) {
	// Check if file exists
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil
	}

	// #nosec G304 - path is constructed from known safe locations
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", path, err)
	}
	defer file.Close()

	var molecules []*types.Issue
	scanner := bufio.NewScanner(file)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Skip empty lines
		if strings.TrimSpace(line) == "" {
			continue
		}

		var issue types.Issue
		if err := json.Unmarshal([]byte(line), &issue); err != nil {
			debug.Logf("warning: %s line %d: %v", path, lineNum, err)
			continue
		}

		// Mark as template
		issue.IsTemplate = true
		issue.SetDefaults()

		molecules = append(molecules, &issue)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading %s: %w", path, err)
	}

	return molecules, nil
}

// getTownMoleculesPath returns the path to town-level molecules.jsonl
// if an orchestrator is detected via GT_ROOT environment variable.
func getTownMoleculesPath() string {
	gtRoot := os.Getenv("GT_ROOT")
	if gtRoot == "" {
		return ""
	}

	// Check for orchestrator molecules file
	gtPath := filepath.Join(gtRoot, ".beads", MoleculeFileName)
	if _, err := os.Stat(gtPath); err == nil {
		return gtPath
	}

	return ""
}

// getUserMoleculesPath returns the path to user-level molecules.jsonl
// (~/.beads/molecules.jsonl).
func getUserMoleculesPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	userPath := filepath.Join(homeDir, ".beads", MoleculeFileName)
	if _, err := os.Stat(userPath); err == nil {
		return userPath
	}

	return ""
}

// getBuiltinMolecules returns the built-in molecule templates shipped with bd.
// These provide common workflow patterns out of the box.
func getBuiltinMolecules() []*types.Issue {
	// For now, return an empty slice. Built-in molecules can be added later
	// using Go embed or by defining them inline here.
	//
	// Example built-in molecules:
	// - mol-feature: Standard feature workflow (design, implement, test, docs)
	// - mol-bugfix: Bug fix workflow (reproduce, fix, verify, regression test)
	// - mol-refactor: Refactoring workflow (identify, plan, implement, verify)
	return nil
}
