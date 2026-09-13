package ownershiphandoffv2

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

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
	// SetYamlConfigInDir refuses a workspace with no config.yaml, telling the
	// operator to run `bd init`. That is right for a person typing a config
	// command and wrong here: a caller-managed workspace need never have had
	// one, and the write set is not optional. The snapshot recorded its absence,
	// so rollback removes the file this creates.
	if err := ensureConfigYAML(beadsDir); err != nil {
		return err
	}
	yamlKeys := map[string]string{
		"dolt.host":       target.Host,
		"dolt.port":       strconv.Itoa(target.Port),
		"dolt.auto-start": "true",
		"dolt.mode":       "server",
	}
	for key, value := range yamlKeys {
		if err := config.SetYamlConfigInDir(beadsDir, key, value); err != nil {
			return fmt.Errorf("write config.yaml %s: %w", key, err)
		}
	}
	// Read every key back through the resolver bd will actually use. A write
	// that lands somewhere the reader cannot see it is worse than a failed
	// write: it looks like success and is discovered later, by a rollback that
	// cannot prove config.yaml points anywhere.
	for key, want := range yamlKeys {
		if got := config.GetStringFromDir(beadsDir, key); got != want {
			return fmt.Errorf("config.yaml %s reads back as %q, not %q", key, got, want)
		}
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

// ensureConfigYAML makes beadsDir's config.yaml ready for the dotted-key writes
// that follow: the file exists, and it already has a non-empty `dolt:` mapping.
//
// Both halves work around the same defect in config.SetYamlConfigInDir. Writing
// `dolt.host` into a file with no `dolt:` mapping appends a FLAT
// `dolt.host: 127.0.0.1` line — a key literally named "dolt.host" — and only
// the next write creates the real `dolt:` block. config.GetStringFromDir splits
// on the dot and looks for a nested map, so it never sees the flat key: bd
// would write the host and be unable to read it back, and R2 would later refuse
// with "config.yaml resolves to :3307". An empty `dolt:` is no better, since it
// parses to nil rather than to a mapping.
//
// Seeding one real key gives every later write a mapping to nest into. `mode`
// is the seed because commit sets it to this value anyway, so nothing here is
// a value the write set would not have written.
//
// This works around the defect rather than fixing it: SetYamlConfigInDir is
// used across bd, and changing how it creates sections belongs in its own
// change with its own tests. The readback assertion in applyCommitWriteSet is
// what makes the workaround safe — if it ever stops working, commit refuses
// instead of silently writing somewhere nothing reads.
func ensureConfigYAML(beadsDir string) error {
	path := configYAMLPath(beadsDir)
	content, err := os.ReadFile(path) //nolint:gosec // workspace config under .beads
	switch {
	case os.IsNotExist(err):
		content = []byte("# created by bd ownership handoff\n")
	case err != nil:
		return fmt.Errorf("read config.yaml: %w", err)
	}
	if hasDoltMapping(content) {
		return nil
	}
	if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
		content = append(content, '\n')
	}
	content = append(content, []byte("dolt:\n    mode: "+configfile.DoltModeServer+"\n")...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return fmt.Errorf("write config.yaml: %w", err)
	}
	return nil
}

// hasDoltMapping reports whether content already has a `dolt:` key whose value
// is a non-empty mapping — the shape SetYamlConfigInDir needs to nest into.
func hasDoltMapping(content []byte) bool {
	var root map[string]any
	if err := yaml.Unmarshal(content, &root); err != nil {
		return false
	}
	section, ok := root["dolt"].(map[string]any)
	return ok && len(section) > 0
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
