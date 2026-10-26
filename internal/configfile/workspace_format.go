package configfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/remedy"
)

// GraphFormatMarkerFile is the format sentinel graph-preview builds write in
// .beads before metadata.json, so that a graph workspace whose metadata was
// never written, was lost, or was rewritten without graph_mode by a bd that
// does not know the field still declares its format.
const GraphFormatMarkerFile = "graph-preview-format"

// UnsupportedWorkspaceFormatError refuses a workspace whose metadata or format
// marker declares a data model other than the Issue store this build opens.
// Graph-preview builds persist graph_mode "link" plus graph_* binding fields
// beside an ordinary Dolt backend, and the GraphFormatMarkerFile beside them,
// so nothing else tells this build that the database behind it is not an
// Issue store; opening it as one would run Issue migrations into it.
//
// It is defined here, beside the metadata, so that every open path refuses
// with the same type and text: cmd/bd's admission guard and store factories,
// the public beads package, and the Dolt store's metadata-driven constructors.
type UnsupportedWorkspaceFormatError struct {
	// BeadsDir is the workspace whose metadata or marker declared the format.
	BeadsDir string
	// GraphMode is the graph_mode marker exactly as the metadata wrote it.
	GraphMode string
	// FormatMarker reports that GraphFormatMarkerFile, not graph_mode,
	// declared the format.
	FormatMarker bool
}

// BeadsDir can be a routed planning or target repo a cloned repository's
// committed routing config names, so Error names it through the remedy
// printer (remedy.PrintedValue): it is printed beside Hint.
func (e *UnsupportedWorkspaceFormatError) Error() string {
	if e.FormatMarker {
		return fmt.Sprintf("workspace %s has a %s marker, which this bd build does not support; %s",
			remedy.PrintedValue(e.BeadsDir), GraphFormatMarkerFile, BackendNotOpenedGuarantee)
	}
	return fmt.Sprintf("workspace %s uses format graph_mode %q, which this bd build does not support; %s",
		remedy.PrintedValue(e.BeadsDir), e.GraphMode, BackendNotOpenedGuarantee)
}

// Hint names the remedy: a build that supports the format, with the
// workspace's metadata and marker left as they are (or, for a marker without
// graph metadata, the metadata restored first).
func (e *UnsupportedWorkspaceFormatError) Hint() string {
	if e.FormatMarker {
		return fmt.Sprintf("restore metadata.json (graph_mode and the graph_* fields) from version control or a backup, then use a bd build that supports graph workspaces; do not remove .beads/%s", GraphFormatMarkerFile)
	}
	return fmt.Sprintf("use a bd build that supports graph_mode %q workspaces; leave graph_mode and the graph_* fields in metadata.json unchanged", e.GraphMode)
}

// CheckWorkspaceFormat refuses metadata whose graph_mode marker names any
// model but the default, with an *UnsupportedWorkspaceFormatError. An unknown
// marker is refused too: it may describe a format newer than this build, so
// it must never fall back to the Issue store. It also refuses a beadsDir
// holding GraphFormatMarkerFile, whatever the metadata says and whatever the
// file holds. cfg may be nil (no metadata); beadsDir may be "" (no workspace
// directory, so no marker to find).
func CheckWorkspaceFormat(cfg *Config, beadsDir string) error {
	if mode, err := cfg.GetGraphMode(); err != nil || mode != GraphModeDependency {
		return &UnsupportedWorkspaceFormatError{BeadsDir: beadsDir, GraphMode: cfg.GraphMode}
	}
	if HasGraphFormatMarker(beadsDir) {
		return &UnsupportedWorkspaceFormatError{BeadsDir: beadsDir, FormatMarker: true}
	}
	return nil
}

// LoadAndCheckWorkspaceFormat reads beadsDir's discovery metadata without
// migrating it (LoadForDiscovery) and refuses a workspace in a format this
// build cannot open (CheckWorkspaceFormat). cfg is nil when beadsDir has no
// metadata. Metadata that cannot be read beside the graph format marker is
// refused as that format too: the marker alone declares it. Any other load
// failure is returned wrapped.
func LoadAndCheckWorkspaceFormat(beadsDir string) (*Config, error) {
	cfg, err := LoadForDiscovery(beadsDir)
	if err != nil {
		if HasGraphFormatMarker(beadsDir) {
			return nil, &UnsupportedWorkspaceFormatError{BeadsDir: beadsDir, FormatMarker: true}
		}
		return nil, fmt.Errorf("loading config: %w", err)
	}
	if err := CheckWorkspaceFormat(cfg, beadsDir); err != nil {
		return nil, err
	}
	return cfg, nil
}

// HasGraphFormatMarker reports whether anything exists at beadsDir's graph
// format marker path. Callers read beadsDir's metadata first, so a directory
// bd cannot search has already failed there. An empty beadsDir never probes
// the working directory.
func HasGraphFormatMarker(beadsDir string) bool {
	if beadsDir == "" {
		return false
	}
	_, err := os.Lstat(filepath.Join(beadsDir, GraphFormatMarkerFile))
	return err == nil
}

// CheckDiscoveredForOpen is LoadDiscoveredForOpen for a caller that needs
// only its refusal: an open, attach or create that has not yet decided this
// build may touch beadsDir runs it before anything that writes there,
// configfile.Load included, which would otherwise migrate a legacy config.json
// into metadata.json before the caller could refuse the workspace.
func CheckDiscoveredForOpen(beadsDir string) error {
	_, err := LoadDiscoveredForOpen(beadsDir)
	return err
}

// LoadDiscoveredForOpen is the one non-mutating check every path that opens
// or inspects a workspace bd was not launched in runs before it writes
// anything there: a migrating configfile.Load, a metadata.json save, or a
// database, gate file or sidecar it would create. Command admission makes the
// same refusals, in the same order, for the workspace bd runs in; this check
// makes them for every other one (an external project, a routed or
// auto-routed target, a create or planning target, an additional repo, or a
// library open of another path), so such a workspace is never rewritten
// before it is refused, and is refused with the text bd prints when run
// inside it.
//
// It reads beadsDir's metadata as LoadForDiscovery does, which never rewrites
// the workspace, and refuses
//   - a workspace format this build cannot open
//     (*UnsupportedWorkspaceFormatError, LoadAndCheckWorkspaceFormat), and
//   - a historical SQLite workspace (*LegacyWorkspaceError,
//     CheckHistoricalSQLiteWorkspace).
//
// Otherwise it returns the metadata LoadForDiscovery read: nil when there is
// none, or when it does not load and is left to the caller's own load to
// report. A caller that also refuses a backend checks that metadata itself.
func LoadDiscoveredForOpen(beadsDir string) (*Config, error) {
	cfg, err := LoadAndCheckWorkspaceFormat(beadsDir)
	if err != nil {
		var formatErr *UnsupportedWorkspaceFormatError
		if errors.As(err, &formatErr) {
			return nil, err
		}
		return nil, nil
	}
	if err := CheckHistoricalSQLiteWorkspace(beadsDir, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
