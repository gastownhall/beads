package configfile

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
// the workspace, and refuses a historical SQLite workspace
// (*LegacyWorkspaceError, CheckHistoricalSQLiteWorkspace).
//
// Otherwise it returns the metadata LoadForDiscovery read: nil when there is
// none, or when it does not load and is left to the caller's own load to
// report. A caller that also refuses a backend checks that metadata itself.
func LoadDiscoveredForOpen(beadsDir string) (*Config, error) {
	cfg, err := LoadForDiscovery(beadsDir)
	if err != nil {
		// Left to the caller's own load to report.
		return nil, nil
	}
	if err := CheckHistoricalSQLiteWorkspace(beadsDir, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
