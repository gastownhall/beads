package ownershiphandoffv2

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/doltserver"
)

// The commit write set, named. These strings are journaled at prepare so the
// snapshot can be checked for completeness before anything is touched, and so
// an operator reading a committed journal can see exactly which keys changed
// hands. Nothing outside this list is written by commit.
var commitWriteSetKeys = []string{
	"dolt-server.pid",
	"dolt-server.port",
	"config.yaml:dolt.host",
	"config.yaml:dolt.port",
	"config.yaml:dolt.auto-start",
	"config.yaml:dolt.mode",
	"metadata.json:dolt_mode",
	"metadata.json:dolt_server_port",
}

// configYAMLName is the workspace config file the write set edits. It is
// re-declared rather than imported because the config package resolves it
// through a precedence chain and this code needs the one file in this .beads
// directory, nothing higher up.
const configYAMLName = "config.yaml"

func metadataPath(beadsDir string) string { return configfile.ConfigPath(beadsDir) }
func configYAMLPath(beadsDir string) string {
	return filepath.Join(beadsDir, configYAMLName)
}
func portFilePath(beadsDir string) string {
	return filepath.Join(beadsDir, doltserver.PortFileName)
}
func pidFilePath(beadsDir string) string {
	return filepath.Join(beadsDir, doltserver.PIDFileName)
}

// captureArtifact records a file byte- and mode-exact, or records its absence.
func captureArtifact(path string) (Artifact, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return Artifact{Present: false}, nil
	}
	if err != nil {
		return Artifact{}, err
	}
	if !info.Mode().IsRegular() {
		return Artifact{}, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := os.ReadFile(path) //nolint:gosec // workspace artifact under .beads
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{Present: true, Data: data, Mode: info.Mode().Perm()}, nil
}

// restoreArtifact puts a file back exactly as captured, or removes it if it was
// not there. The write is atomic so a concurrent reader never sees a partial
// file, and the mode is restored explicitly because a temp file does not
// inherit it.
func restoreArtifact(path string, a Artifact) error {
	if !a.Present {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".restore-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op once renamed
	if _, err := tmp.Write(a.Data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	mode := a.Mode
	if mode == 0 {
		mode = 0o600
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return syncDir(dir)
}

// artifactMatches reports whether the file on disk is still byte- and
// mode-identical to what was captured. This is how a restore is proved, not
// assumed.
func artifactMatches(path string, a Artifact) error {
	current, err := captureArtifact(path)
	if err != nil {
		return err
	}
	if current.Present != a.Present {
		return fmt.Errorf("%s presence changed (was present=%v, now present=%v)", path, a.Present, current.Present)
	}
	if !a.Present {
		return nil
	}
	if string(current.Data) != string(a.Data) {
		return fmt.Errorf("%s contents differ from the snapshot", path)
	}
	if current.Mode != a.Mode {
		return fmt.Errorf("%s mode is %v, snapshot has %v", path, current.Mode, a.Mode)
	}
	return nil
}

// captureSnapshot records everything the transfer might have to put back,
// before anything is touched.
func captureSnapshot(root, dataDir string) (Snapshot, error) {
	beadsDir := BeadsDir(root)
	var s Snapshot
	var err error
	if s.Metadata, err = captureArtifact(metadataPath(beadsDir)); err != nil {
		return s, fmt.Errorf("snapshot metadata.json: %w", err)
	}
	if s.Config, err = captureArtifact(configYAMLPath(beadsDir)); err != nil {
		return s, fmt.Errorf("snapshot config.yaml: %w", err)
	}
	if s.PortFile, err = captureArtifact(portFilePath(beadsDir)); err != nil {
		return s, fmt.Errorf("snapshot %s: %w", doltserver.PortFileName, err)
	}
	if s.PIDFile, err = captureArtifact(pidFilePath(beadsDir)); err != nil {
		return s, fmt.Errorf("snapshot %s: %w", doltserver.PIDFileName, err)
	}
	s.DataDirIsDolt = isDoltRoot(dataDir)
	// Read through the same resolver rollback-finish will use, so "the endpoint
	// was there" and "the endpoint is there" are the same question asked twice.
	host, port := configuredEndpoint(beadsDir)
	s.ConfigHadEndpoint = host != "" && port != ""
	s.CommitWriteSet = append([]string(nil), commitWriteSetKeys...)
	if cfg, loadErr := configfile.Load(beadsDir); loadErr == nil && cfg != nil {
		s.MetadataDoltServerPort = cfg.DoltServerPort
	}
	return s, nil
}

// applyCommitWriteSet transfers authority over the scope's own artifacts to
// bd's target: the lifecycle files name the target, config.yaml points at it
// and re-enables auto-start, and metadata.json stops declaring an external
// server.
//
// Clearing dolt_server_port is the load-bearing edit. While it is set,
// ResolveServerMode returns External for this root — "someone else manages this
// server" — and bd would refuse to auto-start the very server it just launched.
func applyCommitWriteSet(root string, target Target) error {
	beadsDir := BeadsDir(root)
	if err := os.WriteFile(pidFilePath(beadsDir), []byte(strconv.Itoa(target.PID)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", doltserver.PIDFileName, err)
	}
	if err := os.WriteFile(portFilePath(beadsDir), []byte(strconv.Itoa(target.Port)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", doltserver.PortFileName, err)
	}
	if err := writeCommitYAMLKeys(beadsDir, target); err != nil {
		return err
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("load metadata.json: %w", err)
	}
	if cfg == nil {
		return fmt.Errorf("metadata.json is missing")
	}
	cfg.DoltMode = configfile.DoltModeServer
	cfg.DoltServerPort = 0
	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("write metadata.json: %w", err)
	}
	return syncDir(beadsDir)
}

// commitYAMLKeys are the config.yaml keys commit owns, and the values it writes.
func commitYAMLKeys(target Target) map[string]string {
	return map[string]string{
		"dolt.host":       target.Host,
		"dolt.port":       strconv.Itoa(target.Port),
		"dolt.auto-start": "true",
		"dolt.mode":       configfile.DoltModeServer,
	}
}

// writeCommitYAMLKeys points the workspace's config.yaml at bd's replacement and
// proves every key is readable afterwards.
//
// The readback is not ceremony. These keys are dotted, and a dotted key that
// lands as a literal `dolt.host:` line is invisible to the resolver bd itself
// reads with — a write that looks like success and is discovered much later, by
// a rollback that cannot prove config.yaml points anywhere. internal/config now
// holds that round-trip property for every file shape (bd-zj95); this asserts it
// at the one moment where getting it wrong is expensive, so a regression refuses
// the commit instead of silently writing where nothing reads.
func writeCommitYAMLKeys(beadsDir string, target Target) error {
	// SetYamlConfigInDir refuses a workspace with no config.yaml, telling the
	// operator to run `bd init`. Right for a person typing a config command,
	// wrong here: a caller-managed workspace need never have had one, and the
	// write set is not optional. The snapshot recorded its absence, so rollback
	// removes the file this creates.
	if err := ensureConfigYAML(beadsDir); err != nil {
		return err
	}
	keys := commitYAMLKeys(target)
	for key, value := range keys {
		if err := config.SetYamlConfigInDir(beadsDir, key, value); err != nil {
			return fmt.Errorf("write config.yaml %s: %w", key, err)
		}
	}
	for key, want := range keys {
		if got := config.GetStringFromDir(beadsDir, key); got != want {
			return fmt.Errorf("config.yaml %s reads back as %q, not %q", key, got, want)
		}
	}
	return nil
}

// ensureConfigYAML creates an empty workspace config.yaml when there is none,
// so the key writes that follow have a file to edit. It deliberately seeds
// nothing: internal/config nests a dotted key into an empty document by itself.
func ensureConfigYAML(beadsDir string) error {
	path := configYAMLPath(beadsDir)
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat config.yaml: %w", err)
	}
	if err := os.WriteFile(path, []byte("# created by bd ownership handoff\n"), 0o600); err != nil {
		return fmt.Errorf("create config.yaml: %w", err)
	}
	return nil
}

// restoreCommitWriteSet puts the four artifacts back exactly as captured. It
// restores whole files rather than unsetting individual keys: a key-by-key undo
// cannot restore a key that was absent versus present-and-empty, and cannot
// restore formatting or comments the config writer rewrote.
func restoreCommitWriteSet(root string, snap Snapshot) error {
	beadsDir := BeadsDir(root)
	for _, item := range []struct {
		path string
		art  Artifact
	}{
		{metadataPath(beadsDir), snap.Metadata},
		{configYAMLPath(beadsDir), snap.Config},
		{portFilePath(beadsDir), snap.PortFile},
		{pidFilePath(beadsDir), snap.PIDFile},
	} {
		if err := restoreArtifact(item.path, item.art); err != nil {
			return fmt.Errorf("restore %s: %w", item.path, err)
		}
	}
	return syncDir(beadsDir)
}

// assertRestored proves the restore, artifact by artifact. R1 runs it as a
// post-condition so "the workspace is back as it was" is a checked fact at the
// moment it is claimed, not an inference from having called the restore.
func assertRestored(root string, snap Snapshot) error {
	beadsDir := BeadsDir(root)
	for _, item := range []struct {
		path string
		art  Artifact
	}{
		{metadataPath(beadsDir), snap.Metadata},
		{configYAMLPath(beadsDir), snap.Config},
		{portFilePath(beadsDir), snap.PortFile},
		{pidFilePath(beadsDir), snap.PIDFile},
	} {
		if err := artifactMatches(item.path, item.art); err != nil {
			return err
		}
	}
	return nil
}
