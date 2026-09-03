package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// LocalConfigFileName is the untracked sidecar that sits beside a project's
// .beads/config.yaml. Initialize() already merges it LAST, so a value here
// wins over the tracked config.yaml for the same key.
const LocalConfigFileName = "config.local.yaml"

// machineLocalMigrationMarker records that the one-time migration of
// machine-local keys out of the tracked config.yaml has already run for this
// workspace.
//
// It is a YAML COMMENT rather than a config key on purpose: viper merges this
// file into the live settings, so a real key would show up in `bd config list`
// and in every consumer that ranges over settings.
const machineLocalMigrationMarker = "# bd: machine-local keys migrated out of config.yaml (do not remove)"

const localConfigHeader = `# bd machine-local configuration.
#
# Settings here describe THIS machine (which Dolt to talk to, whether this
# host takes backups) rather than the project. bd merges this file last, so a
# value here overrides the same key in the tracked config.yaml.
#
# This file must NOT be committed. bd keeps git from seeing it: repositories
# initialized after bd added the sidecar exclude it in the tracked
# .beads/.gitignore, and in older ones bd adds a per-clone entry to
# .git/info/exclude the first time it writes here.
`

// MachineLocalKeys are config keys whose value is a statement about the
// MACHINE bd is running on, not about the project.
//
// They are written to the untracked config.local.yaml sidecar instead of the
// tracked .beads/config.yaml. Writing them to config.yaml has two costs, and
// the first one is paid by every user of the repository:
//
//  1. The checkout dirties itself. bd rewrites config.yaml as a side effect of
//     ordinary operation, so `git status` reports a modification no one made.
//     Any clean-tree guard — a release script, a pre-commit hook, CI — then
//     refuses for a reason no operator caused and none can fix by committing
//     once, because the next bd run writes the file again.
//  2. One machine's answer propagates to every clone that pulls it. That is
//     the same hazard IsUserGlobalKey exists to prevent for node_id, one axis
//     over: user-global keys are per-machine across ALL workspaces, these are
//     per-machine for ONE workspace. `dolt.mode` is the exemplar — bd's own
//     init code calls a config.yaml dolt.mode "a deliberate statement about
//     this machine" — and it cannot live in the user-global file, because a
//     host may legitimately run one workspace in server mode and another
//     embedded.
//
// Membership is EXACT, never by prefix. Prefix matching is what made
// IsYamlOnlyKey coarse enough to sweep in keys nobody classified; here an
// unrecognized key stays shared, which preserves existing behavior. A
// committed value still works as a shared DEFAULT: reads merge config.yaml
// first and the sidecar second, so a project can ship one and a machine can
// override it.
//
// Deliberately NOT included, as shared project contract:
//   - dolt.auto-start, dolt.disable-event-flush: fleet-wide policy about how
//     the project's store is driven, committed on purpose.
//   - dolt.shared-server: arguably machine-local — it selects a per-machine
//     path under ~/.beads/shared-server/ — but bd's proxied-server migrations
//     record it in config.yaml as workspace state and assert on it there, so
//     it is left shared pending a deliberate decision rather than moved as a
//     side effect of this change.
//   - dolt.max-conns, dolt.pool-read-timeout, dolt.pool-write-timeout: tuning
//     a project ships for all of its clones.
//   - backup.git-push, backup.git-repo: where backups go is arguably a
//     project decision; only whether THIS host takes them is local.
//   - secrets (github.token, *.api_key, ...): already covered by the stricter
//     control in CheckSecretKeyGitSafety, which REFUSES the write rather than
//     relocating it. Routing them here would silently downgrade that refusal.
var MachineLocalKeys = map[string]bool{
	// Which Dolt this host talks to, and how.
	"dolt.mode":     true,
	"dolt.host":     true,
	"dolt.port":     true,
	"dolt.socket":   true,
	"dolt.user":     true,
	"dolt.data-dir": true,
	"dolt.debug":    true,

	// Whether THIS host takes backups, and how often. Backups are written to
	// .beads/backup/, which .beads/.gitignore already excludes as local-only.
	"backup.enabled":  true,
	"backup.interval": true,
}

// sortedMachineLocalKeys returns the registry's keys in a stable order, so any
// pass over it produces byte-identical output from identical input.
func sortedMachineLocalKeys() []string {
	keys := make([]string, 0, len(MachineLocalKeys))
	for key := range MachineLocalKeys {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// IsMachineLocalKey reports whether key describes this machine rather than the
// project, and so must be written to the untracked sidecar. Exact match only —
// see MachineLocalKeys.
func IsMachineLocalKey(key string) bool {
	return MachineLocalKeys[normalizeYamlKey(key)]
}

// LocalConfigPathFor returns the sidecar path beside the given config.yaml.
func LocalConfigPathFor(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), LocalConfigFileName)
}

// setMachineLocalYamlConfig writes a machine-local key to the sidecar beside
// configPath, first migrating any machine-local keys already sitting in the
// tracked config.yaml.
func setMachineLocalYamlConfig(configPath, key, value string) error {
	localPath := LocalConfigPathFor(configPath)
	if err := ensureLocalConfigFile(localPath); err != nil {
		return err
	}
	if err := migrateMachineLocalKeys(configPath, localPath); err != nil {
		return err
	}
	// Written after the migration so the value being set wins over any older
	// value the migration lifted out of config.yaml.
	return setYamlConfigAtPath(localPath, normalizeYamlKey(key), value)
}

// unsetMachineLocalYamlConfig comments a machine-local key out of the sidecar.
// The tracked config.yaml is left alone: a value there is a shared default that
// only an explicit edit should remove.
func unsetMachineLocalYamlConfig(configPath, key string) error {
	localPath := LocalConfigPathFor(configPath)
	// Unset has to migrate first. Before the migration has run, the live value
	// is still the one in config.yaml; clearing only the sidecar would report
	// success while `bd config get` kept returning the old value.
	if err := ensureLocalConfigFile(localPath); err != nil {
		return err
	}
	if err := migrateMachineLocalKeys(configPath, localPath); err != nil {
		return err
	}
	content, err := os.ReadFile(localPath) //nolint:gosec // localPath is derived from a resolved config.yaml path
	if err != nil {
		if os.IsNotExist(err) {
			return nil // nothing set on this machine
		}
		return fmt.Errorf("failed to read %s: %w", LocalConfigFileName, err)
	}
	updated := commentOutYamlKeyAnyForm(string(content), normalizeYamlKey(key))
	if err := os.WriteFile(localPath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", LocalConfigFileName, err)
	}
	return nil
}

// ensureLocalConfigFile creates the sidecar with its header if absent, and
// makes sure git ignores it. The 0600 posture matches every other config
// writer in this package.
func ensureLocalConfigFile(localPath string) error {
	// The exclusion is ensured on every write, not only when the sidecar is
	// created. A workspace that already has the sidecar from an earlier bd
	// would otherwise stay dirty forever, since nothing else on the write path
	// revisits the question.
	if err := ensureSidecarIgnored(filepath.Dir(localPath)); err != nil {
		return err
	}
	if _, err := os.Stat(localPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat %s: %w", LocalConfigFileName, err)
	}
	if err := os.WriteFile(localPath, []byte(localConfigHeader), 0o600); err != nil {
		return fmt.Errorf("failed to create %s: %w", LocalConfigFileName, err)
	}
	return nil
}

// sidecarIgnoreComment introduces the appended entry. bd is not the only
// writer of .git/info/exclude — `bd init --stealth` and `bd doctor --fix` add
// sections there too — so an unexplained bare line would read as cruft.
const sidecarIgnoreComment = "# bd machine-local config sidecar (per-clone; the tracked .beads/.gitignore covers it in repos initialized after bd added it)"

// ensureSidecarIgnored makes git ignore the sidecar in this clone.
//
// Without this the routing change trades one self-dirtying file for another:
// the tracked config.yaml stops being rewritten, but `git status` reports an
// untracked .beads/config.local.yaml instead, and the release script, the
// pre-commit hook and the CI clean-tree step still refuse. The pattern is in
// cmd/bd/doctor's requiredPatterns, but that list is only applied by
// `bd init`, `bd bootstrap` and `bd doctor --fix` — none of which a plain
// `bd config set` runs — so a checkout whose .beads/.gitignore predates the
// sidecar never picks it up.
//
// The entry goes in the clone-local .git/info/exclude, NOT the tracked
// .beads/.gitignore. Appending to the tracked file would leave
// ` M .beads/.gitignore` behind and fail the very clean-tree guards this
// change exists to satisfy — the same sin as rewriting config.yaml, one file
// over. .git/info/exclude is git's own mechanism for a per-clone exclusion, it
// is never committed, and bd already writes there for stealth repos
// (addProjectPatternsToGitExclude). The shared, tracked fix stays where it
// was: `bd doctor --fix` adds config.local.yaml to .beads/.gitignore, and this
// function does nothing when that entry is already present.
func ensureSidecarIgnored(beadsDir string) error {
	// Modern repos: the tracked .beads/.gitignore already covers it, so there
	// is nothing per-clone to add.
	if content, err := os.ReadFile(filepath.Join(beadsDir, ".gitignore")); err == nil { //nolint:gosec // beadsDir is a resolved .beads directory
		if gitignoreListsPattern(string(content), LocalConfigFileName) {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to read .beads/.gitignore: %w", err)
	}

	pattern, excludePath, ok := sidecarExcludeTarget(beadsDir)
	if !ok {
		return nil // not in a git work tree: nothing can be dirty, nothing to do
	}

	content, err := os.ReadFile(excludePath) //nolint:gosec // excludePath is derived from git rev-parse output
	switch {
	case err == nil:
		if gitignoreListsPattern(string(content), pattern) {
			return nil
		}
	case os.IsNotExist(err):
		content = nil
	default:
		return fmt.Errorf("failed to read %s: %w", excludePath, err)
	}

	if err := os.MkdirAll(filepath.Dir(excludePath), 0o750); err != nil {
		return fmt.Errorf("failed to create %s: %w", filepath.Dir(excludePath), err)
	}

	// Additive: an existing exclude keeps its content, its ordering and its
	// trailing-newline shape. Operators and other bd code both write here.
	updated := string(content)
	if updated != "" && !strings.HasSuffix(updated, "\n") {
		updated += "\n"
	}
	if updated != "" {
		updated += "\n"
	}
	updated += sidecarIgnoreComment + "\n" + pattern + "\n"

	if err := os.WriteFile(excludePath, []byte(updated), 0o600); err != nil {
		return fmt.Errorf("failed to update %s: %w", excludePath, err)
	}
	return nil
}

// sidecarExcludeTarget returns the exclude pattern for this workspace's sidecar
// and the path of the exclude file to put it in.
//
// The pattern is anchored to the repository root and names exactly one file, so
// it cannot mask anything else: a .beads directory that is not at the top level
// (BEADS_DIR pointing into a subproject) gets its own entry. The exclude file
// lives in the COMMON git dir, which is what git reads for a linked worktree
// too — a per-worktree .git/info/exclude would be ignored.
//
// Both facts come from ONE rev-parse. gitDirsForRepo would give the common dir
// on its own, but this runs on every machine-local write, and a second process
// spawn to learn the top level is a cost with no payer.
func sidecarExcludeTarget(beadsDir string) (pattern, excludePath string, ok bool) {
	out, err := exec.Command("git", "-C", beadsDir, "rev-parse", "--git-common-dir", "--show-toplevel").Output()
	if err != nil {
		return "", "", false // not a git work tree, or a bare repo
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return "", "", false
	}

	commonDir := gitPathForRepo(beadsDir, strings.TrimSpace(lines[0]))
	topLevel := gitPathForRepo(beadsDir, strings.TrimSpace(lines[1]))
	if commonDir == "" || topLevel == "" {
		return "", "", false
	}

	rel, err := filepath.Rel(topLevel, filepath.Join(gitPathForRepo(beadsDir, beadsDir), LocalConfigFileName))
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", "", false // outside the work tree: git status would never show it
	}

	return "/" + filepath.ToSlash(rel), filepath.Join(commonDir, "info", "exclude"), true
}

// gitignoreListsPattern reports whether content already carries pattern as its
// own entry. Matching is line-exact after trimming, mirroring
// cmd/bd/doctor.containsGitignorePattern so the two agree on what "present"
// means; a commented-out line does not count.
func gitignoreListsPattern(content, pattern string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == pattern {
			return true
		}
	}
	return false
}

// migrationAnchorKey is the scratch top-level key the migration uses to give a
// comments-only sidecar a mapping to write into. It never reaches disk: it is
// removed before the file is written. The name is deliberately not a valid bd
// config key, so a crash between writes leaves something obviously bd's rather
// than something viper would surface in `bd config list`.
const migrationAnchorKey = "bd-migration-anchor"

// hasYamlMapping reports whether content parses to a document with a top-level
// mapping — the condition under which updateYamlKey takes its nested path.
func hasYamlMapping(content string) bool {
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		return false
	}
	return len(root.Content) > 0 && root.Content[0].Kind == yaml.MappingNode
}

// dropYamlLine removes the top-level entry for key. It is textual on purpose:
// re-marshaling to delete a node would be the whole-file reflow this package
// avoids, and the header comments sit above the anchor line, so removing that
// one line leaves them exactly where they are.
func dropYamlLine(content, key string) string {
	prefix := key + ":"
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// migrateMachineLocalKeys performs the ONE-TIME move of machine-local keys out
// of the tracked config.yaml and into the sidecar.
//
// It runs at most once per workspace, gated on a marker comment in the sidecar.
// Running it on every write would re-take a value an operator had deliberately
// re-added to config.yaml as a shared default, which is the same self-dirtying
// churn this change exists to end — just with the sign flipped.
//
// config.yaml is rewritten line-by-line (keys are commented out, matching
// UnsetYamlConfig's convention) rather than re-marshaled, so comments,
// ordering, and formatting of the tracked file survive: the operator gets one
// small reviewable diff to commit, not a reflow of the whole file.
func migrateMachineLocalKeys(configPath, localPath string) error {
	localContent, err := os.ReadFile(localPath) //nolint:gosec // localPath is derived from a resolved config.yaml path
	if err != nil {
		return fmt.Errorf("failed to read %s: %w", LocalConfigFileName, err)
	}
	if strings.Contains(string(localContent), machineLocalMigrationMarker) {
		return nil // already migrated
	}

	trackedRaw, err := os.ReadFile(configPath) //nolint:gosec // configPath is a resolved config.yaml path
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("failed to read config.yaml: %w", err)
		}
		trackedRaw = nil
	}
	tracked := string(trackedRaw)

	// Sorted, not map order. updateYamlKey picks the flat or the nested form
	// from what the document already contains, so the order keys are written
	// in decides the shape of the result: ranging over the map produced a
	// different sidecar on each run, including one carrying a top-level flat
	// `dolt.mode` AND a separate nested `dolt:` block for the same namespace.
	// The tracked-file rewrite below is order-sensitive for the same reason.
	keys := sortedMachineLocalKeys()

	migrated := make(map[string]string)
	for _, key := range keys {
		value, found := yamlValueInContent(tracked, key)
		if !found {
			continue
		}
		// A value already on this machine wins; the tracked one is only a
		// default and must not overwrite it.
		if _, alreadyLocal := yamlValueInContent(string(localContent), key); !alreadyLocal {
			migrated[key] = value
		}
		tracked = commentOutYamlKeyAnyForm(tracked, key)
	}

	newLocal := string(localContent)
	// A freshly created sidecar is header comments and nothing else, and
	// updateYamlKey falls back to the FLAT dotted form on a document with no
	// YAML content: its nested writer round-trips through yaml.Node, and a
	// comment on an empty document has no node to survive on. Left alone that
	// puts the first migrated key in flat form and every later one in nested
	// form, so one namespace ends up represented twice in the same file. A
	// scratch key anchors the mapping so every real key takes the nested path;
	// it is dropped once they are all written.
	anchored := false
	if len(migrated) > 0 && !hasYamlMapping(newLocal) {
		if newLocal, err = updateYamlKey(newLocal, migrationAnchorKey, ""); err != nil {
			return fmt.Errorf("anchoring %s: %w", LocalConfigFileName, err)
		}
		anchored = true
	}
	for _, key := range keys {
		value, ok := migrated[key]
		if !ok {
			continue
		}
		newLocal, err = updateYamlKey(newLocal, key, value)
		if err != nil {
			return fmt.Errorf("migrating %s into %s: %w", key, LocalConfigFileName, err)
		}
	}
	if anchored {
		newLocal = dropYamlLine(newLocal, migrationAnchorKey)
	}
	// Values first, marker last. If the config.yaml rewrite below fails (a
	// read-only checkout, a full disk), a marker already on disk would make
	// this one-time migration skip forever, stranding the keys in the tracked
	// file. Writing the sidecar twice is cheap; it is untracked.
	if err := os.WriteFile(localPath, []byte(newLocal), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", LocalConfigFileName, err)
	}

	// Only touch the tracked file when something actually moved.
	if trackedRaw != nil && tracked != string(trackedRaw) {
		if err := os.WriteFile(configPath, []byte(tracked), 0o600); err != nil {
			return fmt.Errorf("failed to write config.yaml: %w", err)
		}
	}

	if err := os.WriteFile(localPath, []byte(withMigrationMarker(newLocal)), 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", LocalConfigFileName, err)
	}
	return nil
}

// withMigrationMarker records the marker as the FIRST line of the sidecar.
//
// Position matters: subsequent writes to this file go through
// updateNestedYamlKey, which round-trips the document through yaml.Node and
// preserves comments by their attachment to nodes. A head comment at the top
// of the document is the position that survives that round-trip most reliably;
// a trailing comment has no node to attach to. Losing the marker would let the
// one-time migration run a second time and re-take a value the operator had
// deliberately restored to config.yaml as a shared default.
func withMigrationMarker(content string) string {
	if strings.Contains(content, machineLocalMigrationMarker) {
		return content
	}
	if content != "" && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	return machineLocalMigrationMarker + "\n" + content
}

// yamlValueInContent reads a dotted key out of YAML text in either the flat
// dotted form (`dolt.mode: server`) or the nested form (`dolt:\n  mode:
// server`). bd has written both over its lifetime, so a migration that
// understood only one would leave the other behind.
func yamlValueInContent(content, key string) (string, bool) {
	if strings.TrimSpace(content) == "" {
		return "", false
	}
	return yamlValueFromBytes([]byte(content), key)
}

// MachineLocalYamlValue reads a key from the project's config.local.yaml ONLY,
// never the tracked config.yaml.
//
// It exists so the CLI can ATTRIBUTE a value correctly. GetValueSource reports
// SourceConfigFile for anything viper merged, which cannot tell the tracked
// file from the sidecar; labeling a sidecar value "config.yaml" would send an
// operator to edit a file that does not contain it — the same misattribution
// config_show.go already guards against for user-global keys.
func MachineLocalYamlValue(key string) (string, bool) {
	if !IsMachineLocalKey(key) {
		return "", false
	}
	configPath, err := findProjectConfigYaml()
	if err != nil {
		return "", false
	}
	return readYamlValueAtPath(LocalConfigPathFor(configPath), normalizeYamlKey(key))
}
