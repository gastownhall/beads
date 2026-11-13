package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/steveyegge/beads/internal/atomicfile"
	"github.com/steveyegge/beads/internal/ceiling"
	"github.com/steveyegge/beads/internal/gitenv"
	"github.com/steveyegge/beads/internal/remedy"
	"gopkg.in/yaml.v3"
)

// ErrNoProjectConfigYaml reports that the workspace has no project config.yaml
// for a YAML-only write to edit - an external BEADS_DIR, or a database-only
// workspace. It is a sentinel because the answer is caller-dependent: a caller
// whose whole job is the YAML edit has failed and must say so, while a caller
// mirroring a database write into the YAML layer has nothing to clear and has
// not.
var ErrNoProjectConfigYaml = errors.New("no project config.yaml in this workspace")

// CommandSettingKeys names configuration keys whose values bd executes. They
// must be supplied from user-global YAML or shell environment variables, never
// from project .beads/config.yaml or replicated database settings.
var CommandSettingKeys = []string{
	"mail.delegate",
}

// IsCommandSettingKey reports whether key names an executable command setting
// (matched case-insensitively and normalizing underscores/hyphens to dots).
func IsCommandSettingKey(key string) bool {
	replacer := strings.NewReplacer("_", ".", "-", ".")
	norm := replacer.Replace(strings.ToLower(strings.TrimSpace(key)))
	for _, k := range CommandSettingKeys {
		if norm == replacer.Replace(strings.ToLower(k)) {
			return true
		}
	}
	return false
}

// YamlOnlyKeys are configuration keys that must be stored in config.yaml
// rather than the database. These are "startup" settings that are
// read before the database is opened.
//
// This fixes GH#536: users were confused when `bd config set no-db true`
// appeared to succeed but had no effect (because no-db is read from yaml
// at startup, not from the database).
var YamlOnlyKeys = map[string]bool{
	"mail.delegate": true,
	// Bootstrap flags (affect how bd starts)
	"no-db": true,
	"json":  true,
	// Events journal: read through viper during root pre-run, before the store
	// is open, so a DB-backed write would be silently unread — the GH#536 class
	// this map exists to prevent. Without these four entries
	// `bd config set events-journal true` reports success and changes nothing.
	"events-journal":             true,
	"events-journal-retain-days": true,
	"events-journal-retain-rows": true,
	"events-journal-auto-prune":  true,

	// Database and identity
	"db":       true,
	"actor":    true,
	"identity": true,
	// Replica identity: config.NodeID() reads this through viper (yaml/env)
	// only, so a DB-backed write would be silently unread — exactly the
	// GH#536 class this map exists to prevent.
	"node_id": true,

	// Git settings
	"git.author":      true,
	"git.no-gpg-sign": true,
	"no-push":         true,
	"no-git-ops":      true, // Disable git ops in bd prime session close protocol (GH#593)
	"agent.profile":   true, // Explicit policy profile for bd prime's close protocol (GH#3423)

	// Sync settings
	"sync.remote":     true, // Primary: any Dolt-compatible remote URL
	"sync.git-remote": true, // Deprecated: falls back from sync.remote
	"sync.require_confirmation_on_mass_delete": true,

	// Routing settings
	"routing.mode":        true,
	"routing.default":     true,
	"routing.maintainer":  true,
	"routing.contributor": true,

	// Create command settings
	"create.require-description": true,

	// Prime memory-injection caps (read at session start, possibly before
	// the database is reachable, so they must live in yaml)
	"prime.max-memories":     true,
	"prime.max-memory-chars": true,
	// Turns bd prime off for sessions whose host briefs the agent itself;
	// read at session start like the caps, usually as BD_PRIME_DISABLED.
	"prime.disabled": true,

	// Memory-corpus budget enforced by `bd remember` (0 = off). Yaml for the
	// same reason the prime caps are: it is read through viper (config.GetInt),
	// so a DB-backed write would be set successfully and then never read.
	"memories.budget-chars": true,

	// Validation settings (bd-t7jq)
	// Values: "warn" | "error" | "none"
	"validation.on-create": true,
	"validation.on-close":  true,
	"validation.on-sync":   true,

	// Hierarchy settings (GH#995)
	"hierarchy.max-depth": true,

	// Backup settings (must be in yaml so GetValueSource can detect overrides)
	"backup.enabled":            true,
	"backup.interval":           true,
	"backup.git-push":           true,
	"backup.git-repo":           true,
	"backup.size-cap-mb":        true,
	"backup.size-warn-interval": true,

	// Import settings
	"import.auto": true,
	"import.path": true,

	// Dolt server settings
	"dolt.shared-server":   true, // Shared Dolt server at ~/.beads/shared-server/ (GH#2377)
	"dolt.remotesapi-port": true, // Machine-global shared-server remotesapi listener (0 disables)

	"dolt.max-conns":          true, // Connection pool size override (default 10, GH#3140)
	"dolt.pool-read-timeout":  true, // Pool per-I/O read deadline override (default 10s, bd-vz0y9)
	"dolt.pool-write-timeout": true, // Pool per-I/O write deadline override (default 10s, bd-vz0y9)
	"dolt.debug":              true, // Debug-mode dolt sql-server: --loglevel=debug + --prof cpu

	// Secrets: tokens and API keys must NOT be stored in the Dolt database
	// because that data is pushed to remotes, triggering secret-scanning
	// blocks on GitHub. Store them in local config.yaml instead.
	"github.token":               true,
	"linear.api_key":             true,
	"linear.oauth_client_id":     true,
	"linear.oauth_client_secret": true,
	"jira.api_token":             true,
	"gitlab.token":               true,
	"ado.pat":                    true,
	"notion.token":               true,
}

// IsYamlOnlyKey returns true if the given key should be stored in config.yaml
// rather than the Dolt database.
func IsYamlOnlyKey(key string) bool {
	// Check exact match
	if YamlOnlyKeys[key] {
		return true
	}

	// Check prefix matches for nested keys
	prefixes := []string{"routing.", "sync.", "git.", "directory.", "repos.", "external_projects.", "validation.", "lint.", "hierarchy.", "ai.", "backup.", "export.", "dolt.", "federation.", "metrics.", "list.", "audit.", "storage-class."}
	for _, prefix := range prefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}

	return false
}

// secretKeyPatterns are substrings that identify a key as carrying sensitive
// material. Matched anywhere in the key, so they must be long enough that a
// substring hit is never an accident.
var secretKeyPatterns = []string{
	"api_key", "api-key", "apikey", "secret", "token", "password", "passwd",
	"credential", "private_key", "privatekey", "privkey",
}

// secretKeySegments are whole segments — split on `.`, `_` and `-` — that mark
// a key as sensitive.
//
// They are matched as SEGMENTS rather than as substrings because every one of
// them is a prefix of an ordinary word: as a substring, "pat" would redact
// `issue.path` and `export.pattern`, "auth" would redact `commit.author`, and
// "key" would redact `sort.keyword`. As a segment, `github.pat` and
// `commit.author` are told apart correctly.
var secretKeySegments = map[string]bool{
	"key": true, "keys": true, "apikey": true, "pwd": true, "pat": true,
	"auth": true, "bearer": true, "cert": true, "credential": true,
	"credentials": true, "secret": true, "token": true, "password": true,
}

// IsSecretKey reports whether a config key holds sensitive material.
//
// IT IS A SECURITY CONTROL, not only a lint. Two callers depend on it: the
// `bd config set` guard that refuses to write a credential into a git-tracked
// file, and — since the settings surface went on the wire — the redaction in
// internal/httpapi that decides whether GET /v0/beads/config publishes a
// value. Redaction is the whole control there: a `bd serve` bearer is optional
// and, where configured, shared and surface-wide, so it cannot withhold one
// value from one caller — and there is no TLS either. A spelling missing from
// this predicate is a credential served in cleartext.
//
// It errs toward over-redacting for that reason: a key wrongly withheld is an
// operator asking why, and a key wrongly published cannot be recalled. The
// decision is about the KEY alone; no value is ever inspected.
func IsSecretKey(key string) bool {
	lower := strings.ToLower(key)
	for _, pattern := range secretKeyPatterns {
		if strings.Contains(lower, pattern) {
			return true
		}
	}
	for _, segment := range strings.FieldsFunc(lower, func(r rune) bool {
		return r == '.' || r == '_' || r == '-'
	}) {
		if secretKeySegments[segment] {
			return true
		}
	}
	return false
}

// isGitTracked reports whether either the containing repository or the
// inherited Git context tracks the file at path (i.e., it has been git-added).
// Uses `git ls-files --error-unmatch`. If neither probe succeeds, errors count
// as untracked — the secret guard only blocks writes it can prove are unsafe.
//
// The containing repository is probed first so inherited routing cannot hide a
// tracked file. Exit 1 is git's "repository reached, path is not tracked"
// answer and is final; any other failure means the scrubbed environment
// reached no repository at all — as for a bare repository whose work tree is
// named only by GIT_DIR/GIT_WORK_TREE — so the inherited context is consulted
// before declaring the file untracked. Mirrors isGitTrackedFile in cmd/bd.
func isGitTracked(path string) bool {
	inherited := os.Environ()
	return isGitTrackedWithEnv(path, gitenv.ScrubRouting(inherited), inherited)
}

// isGitTrackedWithEnv probes path under env, then under each fallback in
// order, stopping at the first that reports it as tracked. Callers pass the
// scrubbed environment as env so inherited routing cannot hide a tracked
// config file, and the inherited one as the fallback: that preserves bare work
// trees and trusted config, where the repository is reachable only through the
// inherited context. Either probe reporting the path as tracked is sufficient
// to refuse the write. This mirrors the hook guard in cmd/bd/hooks.go.
//
// The first environment is a separate parameter rather than part of the
// variadic so that a zero-probe call, which would fall through to "untracked"
// and write the secret, cannot be spelled. The tail stays variadic because a
// test needs to probe one environment alone; the twin's fixed (clean,
// inherited) arity cannot express that, since exec treats a nil Env as
// "inherit the current process" rather than as a disabled probe.
//
// Every probe failing counts as untracked, so the guard only blocks writes it
// can prove are unsafe.
func isGitTrackedWithEnv(path string, env []string, fallbacks ...[]string) bool {
	dir := filepath.Dir(path)
	for _, probe := range append([][]string{env}, fallbacks...) {
		cmd := exec.Command("git", "ls-files", "--error-unmatch", path)
		cmd.Dir = dir
		cmd.Env = probe
		cmd.Stdout = nil
		cmd.Stderr = nil
		err := cmd.Run()
		if err == nil {
			return true
		}
		// Exit 1 is git's "repository reached, path is not tracked" answer and
		// is final, so a later probe runs only when this one failed for a
		// configuration reason (no reachable repository).
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return false
		}
	}
	return false
}

var secretKeyEnvVarHints = map[string]string{ //nolint:gosec // Values are environment variable names, not credentials.
	// Single var name only: this value is interpolated into an
	// `export %s="..."` shell template, where "A or B" would silently
	// assign to B alone. ANTHROPIC_API_KEY is the primary; MiniMax users
	// can export MINIMAX_API_KEY instead (same resolution chain).
	"ai.api_key":     "ANTHROPIC_API_KEY",
	"github.token":   "GITHUB_TOKEN",
	"linear.api_key": "LINEAR_API_KEY",
	"notion.token":   "NOTION_TOKEN",
}

// secretKeyEnvVarHint returns a suggested environment variable name for a
// secret config key, e.g. "linear.api_key" -> "LINEAR_API_KEY".
func secretKeyEnvVarHint(key string) string {
	if envVar, ok := secretKeyEnvVarHints[key]; ok {
		return envVar
	}
	return "BD_" + strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(key, "-", "_"), ".", "_"))
}

// CheckSecretKeyGitSafety checks whether writing key to the project's
// config.yaml would expose a secret in git history. Returns a descriptive
// error with remediation steps if so; nil otherwise. Non-secret keys always
// return nil.
func CheckSecretKeyGitSafety(key string) error {
	configPath, err := findProjectConfigYaml()
	if err != nil {
		return nil // can't resolve path; let the write fail with its own error
	}
	return checkSecretGitTracked(configPath, key)
}

func checkSecretGitTracked(configPath, key string) error {
	if !IsYamlOnlyKey(key) {
		return nil
	}
	if !IsSecretKey(key) {
		return nil
	}
	if !isGitTracked(configPath) {
		return nil
	}
	envVar := secretKeyEnvVarHint(key)
	// The config path goes into the git rm and echo commands as one shell
	// word each: bare, a workspace path with a space in it would untrack the
	// wrong file and append to another.
	configArg, configNote := remedy.CommandArg(runtime.GOOS, configPath, "<config>")
	ignoreArg, ignoreNote := remedy.CommandArg(runtime.GOOS, filepath.Join(filepath.Dir(configPath), ".gitignore"), "<gitignore>")
	return fmt.Errorf(
		"refusing to write secret key %q to git-tracked config file %s\n\n"+
			"This would expose your secret in git history. Instead:\n"+
			"  export %s=\"your-key-here\"    # add to ~/.secrets or ~/.zshrc\n\n"+
			"Or move config.yaml out of git tracking:\n"+
			"  git rm --cached %s%s\n"+
			"  echo \"config.yaml\" >> %s%s\n\n"+
			"To override this check (e.g., for testing):\n"+
			"  bd config set --force-git-tracked %s \"value\"",
		key, remedy.PrintedValue(configPath),
		envVar,
		configArg, configNote,
		ignoreArg, ignoreNote,
		key,
	)
}

// SetYamlConfig sets a configuration value in the project's config.yaml file.
// It handles both adding new keys and updating existing (possibly commented) keys.
func SetYamlConfig(key, value string) error {
	// Validate specific keys (GH#995)
	if err := validateYamlConfigValue(key, value); err != nil {
		return err
	}

	configPath, err := findProjectConfigYaml()
	if err != nil {
		return err
	}

	return setYamlConfigAtPath(configPath, key, value)
}

// SetYamlConfigInDir sets a configuration value in the config.yaml located in
// the provided beadsDir, bypassing CWD/worktree discovery. Use this when the
// caller has already resolved the authoritative workspace and needs to avoid
// local worktree stubs shadowing the real shared config location.
func SetYamlConfigInDir(beadsDir, key, value string) error {
	// Validate specific keys (GH#995)
	if err := validateYamlConfigValue(key, value); err != nil {
		return err
	}

	configPath := filepath.Join(beadsDir, "config.yaml")
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("no config.yaml found in %s (run 'bd init' first)", beadsDir)
		}
		return fmt.Errorf("failed to stat config.yaml: %w", err)
	}

	return setYamlConfigAtPath(configPath, key, value)
}

var userGlobalKeyPrefixes = []string{"metrics."}

// userGlobalExactKeys are per-MACHINE settings that must never be written to
// the project .beads/config.yaml, which is a git-TRACKED file (see
// cmd/bd/doctor/gitignore.go: nothing in .beads/.gitignore excludes it). A
// committed value propagates one machine's answer to every clone that pulls
// it, which for these keys is worse than having no value at all.
//
// node_id is the exemplar: it names the beads STORE that grants leases here,
// and the reclaim guard (issueops.ReclaimExpiredLeasesInTx) compares it
// against each lease's granted_node. Commit "node_id: mini" and every replica
// reads "mini", so every comparison matches and the guard is simultaneously
// fully ARMED and fully INERT — laptop reaps mini's leases exactly as if they
// were local, which is the precise hazard the guard exists to close, now
// happening while the operator believes they are protected. Routing the write
// to ~/.config/bd/config.yaml keeps it per-machine; viper still merges that
// file, so config.NodeID() reads it back.
var userGlobalExactKeys = map[string]bool{
	"mail.delegate":        true,
	"dolt.remotesapi-port": true,
	"node_id":              true,
}

func IsUserGlobalKey(key string) bool {
	if userGlobalExactKeys[key] {
		return true
	}
	for _, prefix := range userGlobalKeyPrefixes {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

// readUserGlobalYamlValue reads a single dotted key from the user-global
// config.yaml ONLY, never project or BEADS_DIR config. It accepts both the
// nested form (metrics:\n  disabled: true) and the flat dotted form
// (metrics.disabled: true). It returns the raw scalar string and whether the
// key was present.
//
// Consent-bearing settings (metrics enablement and endpoint) are resolved
// through this rather than merged viper so a repository's .beads/config.yaml can
// never re-enable metrics for a user who opted out, nor redirect where metrics
// are sent. See MetricsDisabledByUserConfig / UserMetricsEndpoint.
func readUserGlobalYamlValue(key string) (string, bool) {
	configPath, err := UserConfigYamlPath()
	if err != nil {
		return "", false
	}
	return readYamlValueAtPath(configPath, key)
}

// WorkspaceYamlValue reads a single dotted key out of ONE workspace's
// config.yaml, named by its .beads directory, returning ("", false) when the
// file or the key is absent.
//
// It exists for the cross-workspace opens — routed creates, remote-cache
// hydration, `bd serve` against another workspace — where the process-wide
// merged config answers for the directory bd was LAUNCHED from, not for the
// workspace about to be written. A setting that governs what gets recorded in a
// target workspace has to be read from that target.
func WorkspaceYamlValue(beadsDir, key string) (string, bool) {
	if beadsDir == "" {
		return "", false
	}
	return readYamlValueAtPath(filepath.Join(beadsDir, "config.yaml"), key)
}

// WorkspaceYamlValueStrict reads one dotted key from a workspace config.yaml
// without conflating a malformed or unreadable file with an absent key. A
// missing file or key returns present=false and no error; all other I/O and
// YAML shape errors are returned to the caller.
//
// It reads config.yaml ONLY. Callers that must agree with the merged reader
// (config.GetBool and friends) want WorkspaceYamlValueStrictWithLocal below,
// which also honors the machine-local override layer Initialize merges.
func WorkspaceYamlValueStrict(beadsDir, key string) (value string, present bool, err error) {
	if beadsDir == "" {
		return "", false, nil
	}
	return strictYamlValueAtPath(filepath.Join(beadsDir, ProjectConfigFileName), key)
}

// WorkspaceYamlValueStrictWithLocal reads one dotted key from ONE workspace's
// project-level config the way Initialize's merged reader resolves it: a
// config.local.yaml beside config.yaml is merged LAST there (the file-loading
// block at the end of Initialize), i.e. it has the highest priority of the two,
// so it is consulted first here.
//
// "Beside config.yaml" is a condition, not scenery. Initialize hangs its local
// merge off the project config.yaml it actually found, so a .beads holding
// config.local.yaml ALONE is never merged and config.GetBool answers from
// defaults. This reader mirrors that and ignores a lone local file, because
// honoring one would put it in the same position the layered read exists to
// fix — the single resolver in the tree that answers from a file nothing else
// reads — just pointed the other way.
//
// That layer is the documented way to hold machine-specific state out of the
// git-tracked config.yaml (docs/reference/configuration.md), which makes it
// load-bearing for exactly the settings a cross-workspace open has to resolve
// against the target — a workspace can track `dolt.shared-server: true` for the
// team and opt one machine out locally. A workspace-scoped reader that saw only
// config.yaml would answer differently from every merged-config consumer in the
// tree for that ordinary shape.
//
// Each layer keeps WorkspaceYamlValueStrict's strict contract: an absent file or
// an absent key falls through to the next layer, while an unreadable file, an
// unparseable one, or a non-scalar value at the key is returned as an error
// rather than silently treated as absent. Initialize is equally strict — it
// fails outright on a config.local.yaml it cannot merge.
func WorkspaceYamlValueStrictWithLocal(beadsDir, key string) (value string, present bool, err error) {
	return workspaceYamlValueWithLocal(beadsDir, key, false)
}

// WorkspaceYamlSettingWithLocal is WorkspaceYamlValueStrictWithLocal for a
// caller asking what a workspace's config sets, the way bd's merged reader
// answers it there: a YAML null at the key or at any parent of it reads as
// absent in that layer, as it does for config.GetYamlConfig, instead of as an
// error. That is the shape a user leaves by commenting out every child of a
// section ("dolt:\n  # mode: server\n"), and bd itself runs in that
// workspace as if the key were unset. Every other error is kept: an
// unreadable or unparseable file, a non-null non-map parent, and a non-scalar
// value.
func WorkspaceYamlSettingWithLocal(beadsDir, key string) (value string, present bool, err error) {
	return workspaceYamlValueWithLocal(beadsDir, key, true)
}

func workspaceYamlValueWithLocal(beadsDir, key string, nullIsUnset bool) (value string, present bool, err error) {
	if beadsDir == "" {
		return "", false, nil
	}
	names := []string{LocalConfigFileName, ProjectConfigFileName}
	if _, statErr := os.Stat(filepath.Join(beadsDir, ProjectConfigFileName)); statErr != nil {
		// No project config.yaml here, so Initialize would have nothing to hang
		// the local merge off: drop the local layer to stay in step with it. A
		// stat error other than "not exist" drops it too, matching Initialize,
		// whose own os.Stat probes for a project config.yaml fail the same way;
		// the read below then surfaces that error rather than swallowing it.
		names = names[1:]
	}
	for _, name := range names {
		value, present, err = yamlValueAtPath(filepath.Join(beadsDir, name), key, nullIsUnset)
		if err != nil || present {
			return value, present, err
		}
	}
	return "", false, nil
}

func strictYamlValueAtPath(path, key string) (value string, present bool, err error) {
	return yamlValueAtPath(path, key, false)
}

// yamlValueAtPath reads key from the YAML file at path with the strict
// contract of WorkspaceYamlValueStrict; nullIsUnset reads a null at the key or
// at a parent of it as absent instead of as an error.
func yamlValueAtPath(path, key string, nullIsUnset bool) (value string, present bool, err error) {
	data, err := os.ReadFile(path) //nolint:gosec // path is caller-resolved workspace state
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("reading workspace %s: %w", filepath.Base(path), err)
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return "", false, fmt.Errorf("parsing workspace %s: %w", filepath.Base(path), err)
	}
	if root == nil {
		return "", false, nil
	}
	if raw, ok := root[key]; ok && (raw != nil || !nullIsUnset) {
		value, err := strictYamlScalarString(raw)
		if err != nil {
			return "", true, err
		}
		return value, true, nil
	}
	var node interface{} = root
	parts := strings.Split(key, ".")
	for i, part := range parts {
		m, ok := node.(map[string]interface{})
		if !ok {
			return "", true, fmt.Errorf("workspace config key %q has a non-map parent", key)
		}
		node, ok = m[part]
		if !ok || (node == nil && nullIsUnset) {
			return "", false, nil
		}
		if i == len(parts)-1 {
			value, err := strictYamlScalarString(node)
			if err != nil {
				return "", true, err
			}
			return value, true, nil
		}
	}
	return "", false, nil
}

func strictYamlScalarString(v interface{}) (string, error) {
	if v == nil {
		return "", fmt.Errorf("workspace config value is null")
	}
	switch v.(type) {
	case map[string]interface{}, []interface{}:
		return "", fmt.Errorf("workspace config value must be a scalar")
	default:
		return fmt.Sprintf("%v", v), nil
	}
}

func readYamlValueAtPath(path, key string) (string, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // path is a resolved config.yaml path, not user input
	if err != nil {
		return "", false
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		return "", false
	}
	if raw, ok := root[key]; ok { // flat dotted form
		return yamlScalarString(raw)
	}
	var node interface{} = root // nested form
	for _, part := range strings.Split(key, ".") {
		m, ok := node.(map[string]interface{})
		if !ok {
			return "", false
		}
		node, ok = m[part]
		if !ok {
			return "", false
		}
	}
	return yamlScalarString(node)
}

func yamlScalarString(v interface{}) (string, bool) {
	switch s := v.(type) {
	case nil:
		return "", false
	case string:
		return s, true
	default:
		return fmt.Sprintf("%v", s), true
	}
}

// GetUserYamlConfig reads a single dotted key from the user-global config.yaml
// ONLY, never project/BEADS_DIR config, returning "" if unset. It is the read
// counterpart of SetUserYamlConfig/UnsetUserYamlConfig and the generic form of
// per-key consent helpers below. Keys selected by IsUserGlobalKey (for example
// metrics.*, node_id, and the shared-server remotesapi port) must be read
// through this so `bd config get` reports the value that actually governs
// runtime behavior, not a merged project value the machine ignores.
func GetUserYamlConfig(key string) string {
	raw, _ := readUserGlobalYamlValue(key)
	return strings.TrimSpace(raw)
}

// MetricsDisabledByUserConfig reports whether the user-global config.yaml sets
// metrics.disabled: true. Project/BEADS_DIR config is intentionally ignored so a
// repository can never re-enable metrics for a user who opted out globally.
// Absent or unparseable values read as "not disabled" (the default).
func MetricsDisabledByUserConfig() bool {
	raw, ok := readUserGlobalYamlValue("metrics.disabled")
	if !ok {
		return false
	}
	disabled, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return disabled
}

// UserMetricsEndpoint returns the metrics endpoint configured in the user-global
// config.yaml, or "" if unset. Project/BEADS_DIR config is intentionally ignored
// so a repository can never redirect a user's metrics endpoint. Callers fall
// back to the built-in default when this is empty.
func UserMetricsEndpoint() string {
	raw, _ := readUserGlobalYamlValue("metrics.endpoint")
	return strings.TrimSpace(raw)
}

// MetricsNoticeShownByUserConfig reports whether the user-global config.yaml
// records that the first-run metrics disclosure was already shown. Like consent
// and endpoint, it is resolved from the user-global config ONLY: a repository's
// .beads/config.yaml must not be able to set metrics.notice_shown: true and
// suppress the one-time disclosure for a user who has never actually seen it.
// Absent or unparseable values read as "not shown" (the default).
func MetricsNoticeShownByUserConfig() bool {
	raw, ok := readUserGlobalYamlValue("metrics.notice_shown")
	if !ok {
		return false
	}
	shown, err := strconv.ParseBool(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	return shown
}

// UnsetUserYamlConfig comments out key in the user-global config.yaml. The bool
// reports whether the file was actually changed, on the same terms as
// UnsetYamlConfig: an absent file or an absent key is (false, nil), not an
// error.
func UnsetUserYamlConfig(key string) (bool, error) {
	configPath, err := UserConfigYamlPath()
	if err != nil {
		return false, err
	}
	content, err := os.ReadFile(configPath) //nolint:gosec // configPath is a validated absolute user config path
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to read user config.yaml: %w", err)
	}

	// Normalize before the call, not after it. commentOutYamlKey rewrites CRLF
	// to LF as its first statement, so comparing its result against the raw
	// bytes asks whether the bytes moved rather than whether the key was
	// commented out: on any CRLF file those always differ, and an unset of an
	// absent key reported "Unset <key> (in config.yaml)" — the false claim this
	// path exists to remove — while rewriting the whole file to LF. Comparing
	// against the same normalized form the callee answered from keeps the bool
	// meaning "the key was there", and skipping the write on false leaves a
	// CRLF file byte-identical.
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	newContent, err := commentOutYamlKey(normalized, key)
	if err != nil {
		return false, err
	}
	if newContent == normalized {
		return false, nil
	}
	if err := collateralUnsetRefusal(normalized, newContent, key); err != nil {
		return false, err
	}

	// replaceConfigYaml keeps the file's own mode, so the owner-private 0600
	// posture every user-global writer creates it with (SetUserYamlConfig, the
	// metrics bootstrap) survives the rewrite.
	if err := replaceConfigYaml(configPath, []byte(newContent)); err != nil {
		return false, fmt.Errorf("failed to write user config.yaml: %w", err)
	}

	return true, nil
}

func SetUserYamlConfig(key, value string) error {
	if err := validateYamlConfigValue(key, value); err != nil {
		return err
	}
	configPath, err := UserConfigYamlPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return fmt.Errorf("failed to create user config directory: %w", err)
	}
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.WriteFile(configPath, []byte{}, 0o600); err != nil {
			return fmt.Errorf("failed to create user config.yaml: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to stat user config.yaml: %w", err)
	}
	return setYamlConfigAtPath(configPath, key, value)
}

func setYamlConfigAtPath(configPath, key, value string) error {
	// Read existing config
	content, err := os.ReadFile(configPath) //nolint:gosec // configPath is from findProjectConfigYaml
	if err != nil {
		return fmt.Errorf("failed to read config.yaml: %w", err)
	}

	// Update or add the key
	newContent, err := updateYamlKey(string(content), key, value)
	if err != nil {
		return err
	}

	if err := replaceConfigYaml(configPath, []byte(newContent)); err != nil {
		return fmt.Errorf("failed to write config.yaml: %w", err)
	}

	return nil
}

// replaceConfigYaml replaces an existing config.yaml's content in one step:
// temp file, then rename. Other bd processes read this file while bd writes
// it: every command loads it at startup and reads events-journal activation
// from it when it opens its store, and `bd serve` resolves that activation
// once, at startup, for the life of the server. An in-place rewrite (truncate,
// then write) hands a reader that lands in between an empty or partial file,
// which parses as "key absent" and resolves to the key's default.
//
// On POSIX a reader sees the whole old file or the whole new one. Windows
// refuses to rename over a file another handle holds open, so there the
// replace waits for the reader to close it (internal/atomicfile retries for
// about two seconds) and otherwise fails with the old file intact.
//
// A symlinked config.yaml is written through to its target rather than
// replaced by a regular file, and the file keeps its own permissions. A
// rename needs write permission on the directory only, so the file's own is
// checked first: a config.yaml its owner made read-only is refused, as the
// in-place write refused it, rather than rewritten.
func replaceConfigYaml(configPath string, content []byte) error {
	target, err := filepath.EvalSymlinks(configPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(target)
	if err != nil {
		return err
	}
	// Opened without O_TRUNC, so the probe changes nothing.
	f, err := os.OpenFile(target, os.O_WRONLY, 0) //nolint:gosec // target is the resolved config.yaml path
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return atomicfile.WriteFile(target, content, info.Mode().Perm())
}

// GetYamlConfig gets a configuration value from config.yaml.
// Returns empty string if key is not found or is commented out.
func GetYamlConfig(key string) string {
	if v == nil {
		return ""
	}
	return v.GetString(key)
}

// UnsetYamlConfig removes a configuration value from the project's config.yaml
// file. The key line is commented out (prefixed with "# ") to preserve it as
// documentation.
//
// The bool reports whether the file was actually changed, so a caller can name
// where the unset landed rather than asserting a write it did not make. It is
// false, with a nil error, when the key is not present in the file: "the key is
// not in config.yaml" is the correct answer to give there, not a failure.
//
// A workspace with no project config.yaml at all is reported separately, as
// ErrNoProjectConfigYaml, because the callers need different answers for it and
// only they can tell which applies. The two `bd config unset` routes need
// opposite ones — the yaml-only route reports it, the database-backed route
// tolerates it via errors.Is, since there the database write is the whole unset.
// The two `sync.remote` cleanup sites (cmd/bd/dolt.go and
// cmd/bd/dolt_remote_proxied_server.go) take a third: they deliberately keep
// their base warn-on-any-error behavior. Resolution stays here so none of them
// re-derives it: pre-checking in the caller is what produced the false
// "(in config.yaml)" claims this whole path exists to remove.
func UnsetYamlConfig(key string) (bool, error) {
	configPath, err := findProjectConfigYaml()
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrNoProjectConfigYaml, err)
	}

	content, err := os.ReadFile(configPath) //nolint:gosec // configPath is from findProjectConfigYaml
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("failed to read config.yaml: %w", err)
	}

	// Normalize before the call so the comparison below answers "was the key
	// commented out", not "did the bytes move" — see UnsetUserYamlConfig for
	// the CRLF false-positive this avoids.
	normalized := strings.ReplaceAll(string(content), "\r\n", "\n")
	newContent, err := commentOutYamlKey(normalized, key)
	if err != nil {
		return false, err
	}
	if newContent == normalized {
		return false, nil
	}
	if err := collateralUnsetRefusal(normalized, newContent, key); err != nil {
		return false, err
	}

	if err := replaceConfigYaml(configPath, []byte(newContent)); err != nil {
		return false, fmt.Errorf("failed to write config.yaml: %w", err)
	}

	return true, nil
}

// findProjectConfigYaml finds the active config.yaml path for YAML-only config writes.
//
// Resolution order:
//  1. BEADS_DIR/config.yaml (when BEADS_DIR is set)
//  2. Walk up from CWD to find .beads/config.yaml
//
// This keeps YAML-only config behavior aligned with runtime resolution when
// BEADS_DIR points to an external runtime directory.
func findProjectConfigYaml() (string, error) {
	return findProjectConfigYamlWithFinder(findProjectBeadsDir)
}

func findProjectConfigYamlWithFinder(findBeadsDir func() string) (string, error) {
	// Respect BEADS_DIR first when set.
	if beadsDir := os.Getenv("BEADS_DIR"); beadsDir != "" {
		configPath := filepath.Join(beadsDir, "config.yaml")
		if _, err := os.Stat(configPath); err == nil {
			return configPath, nil
		}
		return "", fmt.Errorf("no config.yaml found in BEADS_DIR (%s) (run 'bd init' first)", beadsDir)
	}

	if configPath := projectConfigPathFromLoadedState(); configPath != "" {
		return configPath, nil
	}

	if findBeadsDir != nil {
		if beadsDir := findBeadsDir(); beadsDir != "" {
			configPath := filepath.Join(beadsDir, "config.yaml")
			if _, err := os.Stat(configPath); err == nil {
				return configPath, nil
			}
		}
	}

	return "", fmt.Errorf("no .beads/config.yaml found (run 'bd init' first)")
}

func projectConfigPathFromLoadedState() string {
	configPath := ConfigFileUsed()
	if configPath == "" {
		return ""
	}
	if filepath.Base(configPath) != "config.yaml" {
		return ""
	}
	if filepath.Base(filepath.Dir(configPath)) != ".beads" {
		return ""
	}
	if _, err := os.Stat(configPath); err != nil {
		return ""
	}
	return configPath
}

func findProjectBeadsDir() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}

	bound := ceiling.For(cwd)
	for dir := cwd; dir != filepath.Dir(dir) && !bound.Excludes(dir); dir = filepath.Dir(dir) {
		beadsDir := filepath.Join(dir, ".beads")
		if info, err := os.Stat(beadsDir); err == nil && info.IsDir() {
			return beadsDir
		}
	}

	configPath := worktreeFallbackConfigPath(cwd)
	if configPath == "" {
		return ""
	}

	return filepath.Dir(configPath)
}

// updateYamlKey updates a key in yaml content, handling commented-out keys.
// If the key exists (commented or not), it updates it in place.
// If the key doesn't exist, it appends it at the end.
//
// A dotted key is written by updateNestedYamlKey, and its result is returned
// as is (TestUpdateYamlKeyLeavesTheNestedPathAlone pins that boundary). A
// single-segment key is edited line by line: a CRLF document comes back LF,
// and the end of the file is kept. An edit in place leaves the trailing
// newline run as it was; an appended key is the new last line, terminated
// only if the file was.
func updateYamlKey(content, key, value string) (string, error) {
	if strings.Contains(key, ".") {
		if updated, ok, err := updateNestedYamlKey(content, key, value); err != nil {
			return "", err
		} else if ok {
			return updated, nil
		}
	}

	// Normalize CRLF the way commentOutYamlKey does. The scan below already
	// drops each line's "\r"; this makes the trailing run re-attached at the
	// return the LF run too, so a CRLF file ending in blank lines keeps them.
	// It sits below the nested dispatch on purpose: some updateNestedYamlKey
	// branches carry "\r" through on untouched lines, and this fix leaves them
	// alone.
	content = strings.ReplaceAll(content, "\r\n", "\n")

	formattedValue := formatYamlValue(value)
	newLine := fmt.Sprintf("%s: %s", key, formattedValue)

	// Build regex to match the key (commented or not)
	// Matches: "key: value" or "# key: value" with optional leading whitespace
	keyPattern := regexp.MustCompile(`^(\s*)(#\s*)?` + regexp.QuoteMeta(key) + `\s*:`)

	found := false
	var result []string

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		if keyPattern.MatchString(line) {
			// Found the key - replace with new value (uncommented)
			// Preserve leading whitespace
			matches := keyPattern.FindStringSubmatch(line)
			indent := ""
			if len(matches) > 1 {
				indent = matches[1]
			}
			result = append(result, indent+newLine)
			found = true
		} else {
			result = append(result, line)
		}
	}
	// A line of bufio.MaxScanTokenSize bytes or more ends the scan early;
	// returning the lines read so far would truncate config.yaml on write.
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("cannot set %q: scanning config content: %w", key, err)
	}

	if !found {
		// Key not found - append at end
		// Add blank line before if content doesn't end with one
		if len(result) > 0 && result[len(result)-1] != "" {
			result = append(result, "")
		}
		result = append(result, newLine)

		// The file's trailing blank lines were scanned as empty tokens, so they
		// already sit ahead of the new line and separate it, and the join is one
		// newline short: the terminator bufio.Scanner dropped. Re-attaching
		// content's whole run, as the return below does for an edit in place,
		// would repeat them after the key: "x\n\n" would come back
		// "x\n\nnewkey: \"v\"\n\n", not "x\n\nnewkey: \"v\"\n". A document
		// without a trailing newline still does not acquire one.
		out := strings.Join(result, "\n")
		if strings.HasSuffix(content, "\n") {
			out += "\n"
		}
		return out, nil
	}

	// Preserve the document's trailing newlines. bufio.Scanner drops the final
	// terminator, so the join is one newline short of any terminated input. Same
	// rule as commentOutYamlKey's return: re-attach content's own run, so a
	// document without a trailing newline does not acquire one.
	out := strings.Join(result, "\n")
	return strings.TrimRight(out, "\n") + content[len(strings.TrimRight(content, "\n")):], nil
}

func updateNestedYamlKey(content, key, value string) (string, bool, error) {
	parts := strings.Split(key, ".")
	if len(parts) < 2 {
		return "", false, nil
	}

	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil {
		return "", false, err
	}
	if len(root.Content) == 0 {
		// An empty or comment-only document parses to no nodes at all: yaml.v3
		// keeps no trace of its text, not even the comments. So there is nothing
		// to nest into AND nothing to marshal back — fabricating a mapping here
		// would emit the new key and silently delete everything else in the
		// file, and `bd init`'s default template is exactly this shape, comments
		// and nothing else. Append the rendered key to the text instead, which
		// leaves the document byte for byte intact. Falling through to the flat
		// writer is not an option either: that is what produced a key literally
		// named "dolt.host", which GetStringFromDir — splitting on the dot and
		// looking for a nested mapping — can never read back.
		appended, err := appendNestedYamlKey(content, parts, value)
		if err != nil {
			return "", false, err
		}
		return appended, true, nil
	}
	mapping := root.Content[0]
	if mapping.Kind != yaml.MappingNode {
		return "", false, fmt.Errorf("cannot set %q: the top level of this config file is not a mapping", key)
	}

	// Preserve the spelling already chosen by the file's writer. config.yaml is
	// shared with integrations that intentionally use literal dotted top-level
	// keys, so migrating that node would make it invisible to those readers.
	//
	// Rewrite the one line the key is on rather than marshaling the document
	// back: this branch exists to leave a file other writers share alone, and a
	// whole-document marshal reformats every unrelated section of it — dropping
	// blank separators, re-indenting nested blocks from two spaces to four,
	// collapsing comment alignment. config.yaml is git-tracked, so a one-key set
	// would show up as a whole-file diff nobody asked for.
	if idx := findMappingChild(mapping, key); idx != -1 {
		keyNode, valueNode := mapping.Content[idx], mapping.Content[idx+1]
		if updated, ok := replaceFlatKeyLine(content, keyNode, valueNode, value); ok {
			return updated, true, nil
		}
		// The entry does not fit on its own line, so there is no single line to
		// swap. Marshal it back, reformatting and all: a correct value in a
		// reformatted file beats a value written somewhere no reader looks.
		valueNode.Kind = yaml.ScalarNode
		valueNode.Tag = ""
		valueNode.Style = scalarStyleFor(value)
		valueNode.Value = value
		out, err := yaml.Marshal(&root)
		if err != nil {
			return "", false, err
		}
		return string(out), true, nil
	}

	leaf, err := findOrCreateNestedScalar(mapping, parts)
	if err != nil {
		return "", false, err
	}

	leaf.Kind = yaml.ScalarNode
	leaf.Tag = ""
	leaf.Style = scalarStyleFor(value)
	leaf.Value = value

	out, err := yaml.Marshal(&root)
	if err != nil {
		return "", false, err
	}
	return string(out), true, nil
}

// replaceFlatKeyLine swaps the value on the single line a top-level key and its
// scalar value share, keeping the key exactly as the file spells it — quoting,
// indentation and all — and leaving every other line byte for byte alone. The
// rewritten line keeps its own trailing comment too, so the only thing that
// changes anywhere in the file is the value that was asked for.
//
// Reports false when the entry is not that shape. The line is only safe to
// replace when it is the whole entry: a value that continues onto later lines (a
// block scalar, a nested mapping, a quoted string wrapped across lines) would
// have its body left behind. Parsing the one line on its own settles that, since
// a top-level entry that ends on its line is a complete document.
func replaceFlatKeyLine(content string, keyNode, valueNode *yaml.Node, value string) (string, bool) {
	lines := strings.Split(content, "\n")
	i := keyNode.Line - 1
	if i < 0 || i >= len(lines) {
		return "", false
	}

	var probe map[string]string
	if err := yaml.Unmarshal([]byte(lines[i]), &probe); err != nil {
		return "", false
	}
	if len(probe) != 1 || probe[keyNode.Value] != valueNode.Value {
		return "", false
	}
	head, _, found := strings.Cut(lines[i], ":")
	if !found || strings.Trim(strings.TrimSpace(head), `"'`) != keyNode.Value {
		return "", false
	}

	tail := trailingCommentOn(lines[i], keyNode, valueNode)
	lines[i] = head + ": " + formatYamlValue(value) + tail
	return strings.Join(lines, "\n"), true
}

// trailingCommentOn returns the entry's end-of-line comment together with the
// whitespace separating it from the value, exactly as line spells both, or ""
// when the line carries no comment.
//
// The line is rebuilt from the key and the new value, so an operator's note on
// the one line this branch rewrites would otherwise be the single thing a
// "rewrite only this line" edit silently dropped. Matching yaml.v3's already
// parsed comment text back from the right is what keeps a "#" inside a quoted
// value (`host: 'a # b'  # note`) from being mistaken for the comment. yaml.v3
// hangs the comment on the value node, except when the value is empty and there
// is no value node line to hang it on, so both are consulted.
func trailingCommentOn(line string, keyNode, valueNode *yaml.Node) string {
	comment := valueNode.LineComment
	if comment == "" {
		comment = keyNode.LineComment
	}
	if comment == "" {
		return ""
	}
	start := strings.LastIndex(line, comment)
	if start < 0 {
		return " " + comment
	}
	for start > 0 && (line[start-1] == ' ' || line[start-1] == '\t') {
		start--
	}
	return line[start:]
}

func findOrCreateNestedScalar(mapping *yaml.Node, parts []string) (*yaml.Node, error) {
	current := mapping
	for i, part := range parts {
		idx := findMappingChild(current, part)
		isLeaf := i == len(parts)-1
		if idx == -1 {
			keyNode := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: part}
			var valNode *yaml.Node
			if isLeaf {
				valNode = &yaml.Node{Kind: yaml.ScalarNode}
			} else {
				valNode = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			}
			current.Content = append(current.Content, keyNode, valNode)
			if isLeaf {
				return valNode, nil
			}
			current = valNode
			continue
		}
		child := current.Content[idx+1]
		if isLeaf {
			return child, nil
		}
		if child.Tag == "!!null" {
			// A section with nothing left under it — `sync:` and no more, which
			// is exactly what an unset leaves behind once it has commented the
			// last leaf out. It holds no value to lose, so treat it as the empty
			// mapping it looks like. Refusing here sent the caller to the flat
			// writer, so set -> unset -> set put the unreadable `sync.remote:`
			// spelling back into the file this whole fix exists to keep out.
			child.Kind = yaml.MappingNode
			child.Tag = "!!map"
			child.Value = ""
			child.Style = 0
		}
		if child.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("cannot set %q: %q already holds a value, so there is no section to nest under it",
				strings.Join(parts, "."), strings.Join(parts[:i+1], "."))
		}
		current = child
	}
	// Unreachable: every path through the loop returns on the last part.
	return nil, fmt.Errorf("cannot set %q: no key to write", strings.Join(parts, "."))
}

// appendNestedYamlKey renders just the key being written and appends it to the
// document text. Used when the document has no nodes to walk, where the text is
// the only copy of the file's contents that exists.
func appendNestedYamlKey(content string, parts []string, value string) (string, error) {
	node := &yaml.Node{Kind: yaml.ScalarNode, Style: scalarStyleFor(value), Value: value}
	for i := len(parts) - 1; i >= 0; i-- {
		node = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
			{Kind: yaml.ScalarNode, Tag: "!!str", Value: parts[i]},
			node,
		}}
	}
	rendered, err := yaml.Marshal(node)
	if err != nil {
		return "", err
	}

	// Same spacing the flat writer uses: a blank line between whatever was
	// already in the file and the key being added.
	existing := strings.TrimRight(content, "\n")
	if existing == "" {
		return string(rendered), nil
	}
	return existing + "\n\n" + string(rendered), nil
}

func findMappingChild(mapping *yaml.Node, name string) int {
	for i := 0; i < len(mapping.Content); i += 2 {
		k := mapping.Content[i]
		if k.Kind == yaml.ScalarNode && k.Value == name {
			return i
		}
	}
	return -1
}

func scalarStyleFor(value string) yaml.Style {
	if value == "" {
		return yaml.DoubleQuotedStyle
	}
	if _, err := strconv.ParseBool(value); err == nil {
		return 0
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil {
		return 0
	}
	switch value {
	case "null", "Null", "NULL", "~", "yes", "no", "on", "off":
		return yaml.DoubleQuotedStyle
	}
	if strings.ContainsAny(value, ":#\n\"'") || strings.HasPrefix(value, " ") || strings.HasSuffix(value, " ") {
		return yaml.DoubleQuotedStyle
	}
	return 0
}

func commentOutYamlKey(content, key string) (string, error) {
	// Normalize CRLF up front. The old implementation scanned with
	// bufio.Scanner, whose ScanLines drops a trailing "\r", so a CRLF file came
	// back LF-only; splitting on "\n" instead (below, to keep the file's final
	// newline) would otherwise carry every "\r" through and change that
	// long-standing behavior.
	content = strings.ReplaceAll(content, "\r\n", "\n")

	// A key no document holds is not in the file, whatever its lines look
	// like. The matcher below reads lines, so a line inside a quoted value or
	// a list item can match the key. Commenting that line out edits a value
	// nobody named, and refusing it fails the unset of a key that is not
	// there, which has always been a successful no-op. So the file is left as
	// it is, and both unset callers report that nothing changed.
	if !yamlHoldsKey(content, key) {
		return content, nil
	}

	if err := unsupportedUnsetShape(content, key); err != nil {
		return "", err
	}

	// The flat spelling first — a key literally named "sync.remote", which
	// older files carry and which this function has always handled.
	flatPattern := regexp.MustCompile(`^(\s*)` + regexp.QuoteMeta(key) + `\s*:`)
	// And the nested one, which is what the writer produces. Missing this half
	// made an unset silently do nothing once the writer started nesting: the
	// value stayed live, so bd kept a setting the operator had asked it to
	// forget. Unset is the other direction of the same round-trip property as
	// set and get, and all three have to agree on the shape.
	walk := newNestedKeyWalk(key)

	var result []string
	// blockIndent is the indentation of the key that opened a literal or folded
	// block scalar, or -1 outside one. Everything indented past that key is the
	// value the user typed, not structure: `notes: |` with an indented
	// `remote: keep-this` inside it is prose, and commenting it out edits their
	// data. Matching by line has to skip those lines to stay honest.
	blockIndent := -1
	// Split rather than scan so the file's trailing newline survives: a
	// scanner drops it, so every unset rewrote the file without its final
	// newline, and the unset callers could not tell "nothing matched" from a
	// write by comparing content.
	lines := strings.Split(content, "\n")
	// A flat key is a TOP-LEVEL key whose name contains the dots, so it only
	// matches at the document's top-level indentation. Without this, a
	// single-segment key such as `enabled` matched a nested `  enabled:` line
	// under some other section and commented out a key the caller never named.
	// The top level is usually column 0, but yaml.v3 accepts a document whose
	// whole mapping is indented, so it is read from the first key line.
	topIndent := yamlTopLevelIndent(lines)
	for i, line := range lines {
		if blockIndent >= 0 {
			if strings.TrimSpace(line) == "" || lineIndent(line) > blockIndent {
				result = append(result, line)
				continue
			}
			blockIndent = -1
		}
		blockIndent = blockScalarIndent(line)

		if matches := flatPattern.FindStringSubmatch(line); matches != nil && len(matches[1]) == topIndent {
			if err := mappingValueUnsetRefusal(lines, i, len(matches[1]), key); err != nil {
				return "", err
			}
			result = append(result, matches[1]+"# "+strings.TrimLeft(line, " \t"))
			continue
		}

		name, indent, ok := yamlKeyOnLine(line)
		if !ok {
			walk.stepOver(line)
		} else if walk.step(name, indent) {
			if err := mappingValueUnsetRefusal(lines, i, indent, key); err != nil {
				return "", err
			}
			result = append(result, strings.Repeat(" ", indent)+"# "+strings.TrimLeft(line, " \t"))
			continue
		}
		result = append(result, line)
	}

	// Preserve the document's trailing newlines. The scan above reads with
	// bufio.Scanner, which yields one empty token per blank line but drops the
	// final terminator, so the join is always EXACTLY ONE newline short
	// whenever content ends in "\n" -- "x\n" joins to "x", "x\n\n" to "x\n",
	// "x\n\n\n" to "x\n\n". An unset therefore also wrote an end-of-file change
	// on top of the line it meant to comment out, and did so even for a key the
	// document does not contain, i.e. when nothing was edited at all. A
	// config.yaml is git-tracked, so that is a spurious line in someone's
	// review.
	//
	// Re-attach content's own run rather than appending a single "\n" under a
	// HasSuffix guard. The count was never the problem; the GUARD was. For a
	// file ending "\n\n" the join ends "\n" -- the blank line's own newline --
	// so !HasSuffix(out, "\n") was already false and the one missing newline
	// was never restored. Trimming both ends and re-attaching does not depend
	// on the count at all. The rule is PRESERVE, not always-append: a document
	// that genuinely has no trailing newline does not acquire one, so this
	// cannot rewrite the end of a file that was already written that way.
	out := strings.Join(result, "\n")
	return strings.TrimRight(out, "\n") + content[len(strings.TrimRight(content, "\n")):], nil
}

// mappingValueUnsetRefusal refuses a matched key line whose value continues on
// the lines beneath it rather than sitting on the key's own line: a nested
// mapping, a list, or a plain scalar continued on the next line. Commenting
// that one line out would orphan those lines, leaving a config.yaml that no
// longer parses. The value is judged by the first following line that carries
// content, so a key with an empty value and nothing under it is still
// commented as before.
//
// Nothing on the key's own line but a comment, an anchor or a tag (`b:  #
// note`, `base: &b`, `m: !!map`) still leaves the value to the lines beneath.
// A list may sit at the key's OWN indentation (`types.custom:` then `- step`),
// which YAML allows for a sequence value, so a `-` item at that indentation
// belongs to the key too.
func mappingValueUnsetRefusal(lines []string, idx, indent int, key string) error {
	_, rest, _ := strings.Cut(strings.TrimLeft(lines[idx], " \t"), ":")
	if !valueContinuesBelow(rest) {
		return nil
	}
	for _, next := range lines[idx+1:] {
		// TrimSpace, not TrimLeft: a CRLF file's blank line is a lone "\r",
		// which must not read as content at column 0 and end the value early.
		trimmed := strings.TrimSpace(next)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		nextIndent := lineIndent(next)
		if nextIndent < indent {
			return nil
		}
		if nextIndent == indent && trimmed != "-" && !strings.HasPrefix(trimmed, "- ") {
			return nil
		}
		return fmt.Errorf("cannot unset %q: its value continues on the lines beneath it, and commenting the key out would orphan them; remove it by hand, or unset its keys individually if it is a mapping", key)
	}
	return nil
}

// valueContinuesBelow reports whether text leaves the value to the following
// lines: it holds only anchors and tags before an optional comment. The text is
// what follows a key's colon, or a whole line that declares no key (stepOver).
//
// A "#" at the start of a line or after whitespace starts a comment, so the
// comment begins at the first token that starts with "#". Cutting at the first
// " #" instead read the comment line `# see #12` as the content `# see`:
// stepOver skipped the target beneath it as a subtree, and unset reported the
// key as never set while it stayed live. The cut also missed a comment set off
// by a tab, and it read `sub:  # note #2` as a key with a value of its own, so
// unset commented that line out and orphaned the mapping beneath it.
func valueContinuesBelow(text string) bool {
	for _, tok := range strings.Fields(text) {
		if strings.HasPrefix(tok, "#") {
			return true
		}
		if !strings.HasPrefix(tok, "&") && !strings.HasPrefix(tok, "!") {
			return false
		}
	}
	return true
}

// yamlTopLevelIndent is the indentation of the document's first key line, which
// every top-level key shares, or 0 when the document declares no key.
func yamlTopLevelIndent(lines []string) int {
	for _, line := range lines {
		if _, indent, ok := yamlKeyOnLine(line); ok {
			return indent
		}
	}
	return 0
}

// nestedKeyWalk tracks how much of a dotted key a line-by-line scan has matched
// so far, so a leaf is commented out only when it is nested under its OWN
// parents rather than under some other section that happens to repeat a name.
type nestedKeyWalk struct {
	segments []string
	// depth is how many segments have been matched, and indents holds the
	// indentation each was matched at.
	depth   int
	indents []int
	// opaque is the indentation of the last key line that could not continue
	// the match, or -1. Everything nested under such a key belongs to some
	// other path, so the walk steps over that whole subtree instead of
	// descending into it and matching a leaf name that repeats there.
	opaque int
}

func newNestedKeyWalk(key string) *nestedKeyWalk {
	return &nestedKeyWalk{segments: strings.Split(key, "."), opaque: -1}
}

// step advances the walk by one mapping line and reports whether that line is
// the key being looked for.
func (w *nestedKeyWalk) step(name string, indent int) bool {
	if len(w.segments) < 2 {
		// A single-segment key has no nesting to walk; the flat pattern owns it.
		return false
	}
	if w.opaque >= 0 && indent > w.opaque {
		return false
	}
	w.opaque = -1
	// Leaving a block: drop every segment matched at an indent at or deeper
	// than this line's.
	for w.depth > 0 && indent <= w.indents[w.depth-1] {
		w.depth--
		w.indents = w.indents[:w.depth]
	}
	// Both halves of the anchor matter, and missing either one made an unset
	// edit keys it does not own. Segment 0 is only itself at the TOP level:
	// without that, a `sync:` section nested under an unrelated key seeded the
	// walk, so unsetting `dolt.host` commented out `other.dolt.host` too. And
	// every later segment has to be a DIRECT child of the one before it:
	// without that, an interposed section was stepped over rather than ending
	// the match, so unsetting `sync.remote` on
	//
	//	sync:
	//	    sub:
	//	        remote: keep
	//	    remote: target
	//
	// commented out `sync.sub.remote` and left the real `sync.remote` live,
	// reporting success — the silent divergence this whole fix exists to end.
	if w.depth < len(w.segments) && name == w.segments[w.depth] && (w.depth > 0 || indent == 0) {
		if w.depth == len(w.segments)-1 {
			w.depth, w.indents = 0, nil
			return true
		}
		w.indents = append(w.indents, indent)
		w.depth++
		return false
	}
	w.opaque = indent
	return false
}

// stepOver advances the walk by one line that has content but declares no key
// yamlKeyOnLine can read, a list item above all. A dotted key names mappings
// only, so nothing nested under such a line can continue the match, and the
// walk skips that subtree as it does one under a key that does not match. These
// lines used to bypass the walk entirely, so unsetting `dolt.host` on
//
//	dolt:
//	  - port: 3307
//	    host: 10.0.0.1
//
// read the item's `host:` as a direct child of `dolt:` and commented out a key
// the file never set, reporting success (mc-2nfr1).
//
// Blank lines and comments are not content, and neither is a line holding only
// node properties (`&a`, `!!map`): those decorate the node beneath them, which
// under a matched parent is the parent's own mapping, so the match continues
// into it. yaml.v3 accepts that mapping indented past the properties, and
// skipping it as a subtree left `dolt.host` live on
//
//	dolt:
//	  &a
//	    host: 10.0.0.1
//
// while the unset reported the key as never set. valueContinuesBelow draws the
// same line for the text after a key's colon (`base: &b`), so it decides here
// too.
//
// The skip is armed only while none is. Narrowing an armed one would end the
// skip over a section the key does not own partway through it, and widening
// one would trust a line that need not end anything: yaml.v3 accepts a quoted
// value continued at column 0, and the target can follow it.
func (w *nestedKeyWalk) stepOver(line string) {
	if w.opaque >= 0 || valueContinuesBelow(line) {
		return
	}
	w.opaque = lineIndent(line)
}

// unsupportedUnsetShape reports the shapes named in the changelog — a key under
// a flow-style mapping, a key whose value is a block scalar, and a key whose
// value wraps past its own line — in whichever spelling the file uses. All
// three used to be silent: the flow-style one reported success while the value
// stayed live, and the other two had their key line commented out while the
// rest of the value stayed behind, to be re-read as something else or to stop
// the file from parsing. The set direction already refuses what it cannot write
// correctly, so unset says so too.
//
// The first two are named shapes with diagnoses of their own. The third is
// decided by where the entry ends rather than by the value's style
// (spansLinesUnsetRefusal), so the same value written on one line still
// comments out cleanly. A mapping-valued key is refused by the line matcher
// itself (mappingValueUnsetRefusal), which also covers single-segment keys this
// function never walks. An edit none of these names is still judged by its
// effect once the line matcher has made it (collateralUnsetRefusal).
//
// This function refuses only a key that is actually present: unsetting a key
// that was never there has always succeeded, and staying silent about a shape
// nobody asked to touch is the point.
func unsupportedUnsetShape(content, key string) error {
	// Single-segment keys are checked too. The database-backed unset clears
	// the config.yaml layer for every key, so `notes: |` with an indented body
	// reaches this path, and commenting its key line out orphans the body
	// exactly as it does for a dotted key.
	segments := strings.Split(key, ".")
	var root yaml.Node
	if err := yaml.Unmarshal([]byte(content), &root); err != nil || len(root.Content) == 0 {
		// Nothing to walk. A file yaml.v3 cannot parse is not this function's
		// to diagnose, and the line matcher has always been best-effort on it.
		return nil
	}

	// The flat spelling first, and by the same order the line matcher uses: it
	// comments a literal `sync.remote:` line out without ever walking the nested
	// path, so the body left behind orphans at the TOP level, where it stops the
	// whole document from parsing rather than just its own section. A
	// single-segment key is its own flat spelling, and the walk below would only
	// find the same entry again.
	if err := flatKeyUnsetRefusal(content, root.Content[0], key); err != nil || len(segments) == 1 {
		return err
	}

	node := root.Content[0]
	var keyNode *yaml.Node
	parents := make([]*yaml.Node, 0, len(segments))
	for _, segment := range segments {
		if node.Kind != yaml.MappingNode {
			return nil
		}
		idx := findMappingChild(node, segment)
		if idx == -1 {
			return nil
		}
		parents = append(parents, node)
		keyNode, node = node.Content[idx], node.Content[idx+1]
	}

	for i, parent := range parents {
		if parent.Style&yaml.FlowStyle != 0 {
			return flowStyleUnsetRefusal(key, segments[:i])
		}
	}
	if err := blockScalarUnsetRefusal(node, key); err != nil {
		return err
	}
	return spansLinesUnsetRefusal(content, keyNode, node, key)
}

// flatKeyUnsetRefusal runs unsupportedUnsetShape's checks on the flat spelling
// of key: a top-level entry whose name is the whole key, dots included.
//
// Under a flow-style top level that entry is a key inside a flow-style mapping
// like any other, and the line matcher cannot edit it either. On the opening
// line (`{sync.remote: a, node_id: b}`) it is never matched at all, so the
// value stays live while the unset reports success. On a line of its own it
// can be matched, but that line may close the braces as well, or hold only the
// start of a value that wraps. So it gets the flow-style refusal, as a
// single-segment key there always has. The span probe would not hold here
// anyway: cut inside the mapping's own braces, the document fails to parse
// whatever the value is.
func flatKeyUnsetRefusal(content string, top *yaml.Node, key string) error {
	if top.Kind != yaml.MappingNode {
		return nil
	}
	idx := findMappingChild(top, key)
	if idx == -1 {
		return nil
	}
	if top.Style&yaml.FlowStyle != 0 {
		return flowStyleUnsetRefusal(key, nil)
	}
	if err := blockScalarUnsetRefusal(top.Content[idx+1], key); err != nil {
		return err
	}
	return spansLinesUnsetRefusal(content, top.Content[idx], top.Content[idx+1], key)
}

// flowStyleUnsetRefusal refuses a key inside a flow-style mapping, which the
// line matcher cannot edit. parent is the path of that mapping, empty for the
// top level.
func flowStyleUnsetRefusal(key string, parent []string) error {
	where := "the top level of this config file"
	if len(parent) > 0 {
		where = fmt.Sprintf("%q", strings.Join(parent, "."))
	}
	return fmt.Errorf("cannot unset %q: %s is written in flow style ({...}), which bd cannot edit; remove the key by hand", key, where)
}

// blockScalarUnsetRefusal refuses a key whose value is written as a block
// scalar, in whichever spelling the caller resolved it: commenting the key line
// out leaves the indented body behind, to be re-read as a value of whatever
// encloses it.
func blockScalarUnsetRefusal(valueNode *yaml.Node, key string) error {
	if valueNode.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 {
		return nil
	}
	return fmt.Errorf("cannot unset %q: its value is a block scalar (| or >), and commenting the key out would leave the body behind as a value of its own; remove the key by hand", key)
}

// spansLinesUnsetRefusal refuses a key of a block mapping whose value starts on
// the key's own line and does not end there: a flow collection, a quoted string
// or a plain scalar wrapped onto the next line (`remote: {url: a,` then
// `branch: b}`). The line matcher comments out only the key's line, so the rest
// of the value would stay behind with its opening bracket or quote gone: as a
// line that re-reads as a key nobody set (`branch: b}`) or as the value of the
// enclosing key (`sync: b`), or one that stops the file from parsing.
//
// It is decided by where the entry ends, not by the value's style: the same
// collection written on one line comments out cleanly. Everything above an
// entry of a block mapping is either a finished entry or an enclosing block
// mapping, and those end with the document, so the document cut off after the
// key's line parses whenever the entry ends on that line, and fails while a
// flow collection or quoted string opened on it is still open. The cut counts
// lines the way yaml.v3 numbers them (yamlLineEnd), not by "\n" alone: a
// carriage return or a Unicode line separator above the key would otherwise
// move the cut to another line, refusing a value that ends on its own line
// and missing one that wraps. Cutting the document rather than parsing the one
// line on its own, as replaceFlatKeyLine does, keeps anchors in scope:
// `remote: *r` is a whole entry, but its alias resolves only against an anchor
// defined above it.
//
// A plain scalar continued onto a deeper line (`remote: file:///a`, then `b`)
// needs one more look, because it parses when cut after any of its lines, as a
// shorter value. The cut holds the key's line and nothing after it, so the
// key's value is the last node in it, and a value that started on the key's
// line and goes on reads shorter there than in the whole file. An alias or a
// collection is not compared: a cut that parses has already closed it.
func spansLinesUnsetRefusal(content string, keyNode, valueNode *yaml.Node, key string) error {
	end, ok := yamlLineEnd(content, keyNode.Line)
	if !ok {
		// Nothing follows the key's line for its value to wrap onto.
		return nil
	}
	var cut yaml.Node
	if yaml.Unmarshal([]byte(content[:end]), &cut) == nil && !plainScalarGoesOn(&cut, keyNode, valueNode) {
		return nil
	}
	return fmt.Errorf("cannot unset %q: its value spans more than one line, and commenting the key out would leave the rest of it behind; remove the key by hand", key)
}

// plainScalarGoesOn reports whether a scalar value that starts on the key's
// line reads differently in cut, the document cut off after that line, than
// in the whole file: a plain scalar continued onto the lines below.
func plainScalarGoesOn(cut, keyNode, valueNode *yaml.Node) bool {
	if valueNode.Kind != yaml.ScalarNode || valueNode.Line != keyNode.Line {
		return false
	}
	last := cut
	for len(last.Content) > 0 {
		last = last.Content[len(last.Content)-1]
	}
	return last.Value != valueNode.Value
}

// yamlLineEnd reports the offset of the line break that ends the given 1-based
// line of content, counting breaks as yaml.v3 does when it numbers node lines:
// LF, CR, CRLF as one break, NEL (U+0085) and the Unicode line and paragraph
// separators (U+2028, U+2029). ok is false when no break ends that line,
// because it is the last line or out of range.
func yamlLineEnd(content string, line int) (end int, ok bool) {
	breaks := 0
	for i, r := range content {
		switch {
		case r == '\n' && i > 0 && content[i-1] == '\r':
			continue // the LF of a CRLF, already counted at its CR
		case r == '\n', r == '\r', r == '\u0085', r == '\u2028', r == '\u2029':
			breaks++
			if breaks == line {
				return i, true
			}
		}
	}
	return 0, false
}

// collateralUnsetRefusal refuses an unset whose edit changed more of the file
// than the key. Both unset callers run it on commentOutYamlKey's result before
// they write. The refusals before the edit name the shapes they know, judged
// from the first document's parse; this one judges the edit by its effect, so
// it also catches the shapes they cannot see: a later duplicate of the key
// whose value wraps, the key in a later document, an anchor on the key's line
// that an alias further down still uses, or a line inside another value that
// only looks like the key, beside the key itself. A file that does not hold
// the key at all never gets here: commentOutYamlKey leaves it as it is.
//
// Read back, after must hold what before held, document by document, less
// some of the key's own entries in either spelling: nothing added, and nothing
// else removed or changed. Documents are compared by position because bd reads
// only the first one: commenting out all of a first document that no `---`
// opens would move the second one into its place.
//
// A later document that does not parse leaves the ones before it readable, the
// first of them being what bd reads, so those are compared all the same, and
// the edit must leave every one of them parsing. The document that does not
// parse, and any after it, has no reading to compare, before the edit or after
// it. A file whose first document does not parse is left to the line matcher's
// best effort, as it always has been. A file that nests deeper than
// maxYamlLeafDepth is refused: it costs too much to compare.
func collateralUnsetRefusal(before, after, key string) error {
	was, wasErr := yamlDocuments(before)
	if wasErr != nil && len(was) == 0 {
		return nil
	}
	is, isErr := yamlDocuments(after)
	if wasErr != nil && len(is) >= len(was) {
		is, isErr = is[:len(was)], nil
	}
	wasLeaves, ok := yamlLeaves(was)
	if !ok {
		return fmt.Errorf("cannot unset %q: this file nests more than %d levels deep, too deep for bd to check what else commenting out the key would change; remove the key by hand", key, maxYamlLeafDepth)
	}
	isLeaves, ok := yamlLeaves(is)
	if ok && isErr == nil && onlyKeyLeavesRemoved(wasLeaves, isLeaves, key) {
		return nil
	}
	return fmt.Errorf("cannot unset %q: commenting out the lines that match it would change more of this file than the key itself; remove the key by hand", key)
}

// yamlLeaf is one scalar of a YAML stream: the document it is in, the keys and
// list indexes that lead to it, and its value. An alias is a leaf of its own,
// naming the anchor it resolves to rather than expanding it, so the list never
// grows past the size of the file, however the aliases nest.
type yamlLeaf struct {
	doc   int
	path  []string
	value string
}

func (l yamlLeaf) id() string {
	return fmt.Sprintf("%d %q %q", l.doc, l.path, l.value)
}

// maxYamlLeafDepth is how many keys and list items deep yamlLeaves reads. A
// leaf carries the whole path to it, so listing a document's leaves costs
// their number times their depth, and yaml.v3 lets a document nest 10000
// deep: one unset of a 45 KB config.yaml nested 9000 deep allocated 1.7 GB.
// No config bd reads comes near this depth.
const maxYamlLeafDepth = 64

// yamlLeaves lists the leaves of docs. A null scalar or an empty collection
// holds no leaf, so a section left holding nothing reads the same as one that
// is gone. ok is false, and no leaves come back, when a document nests more
// than maxYamlLeafDepth keys and list items deep.
func yamlLeaves(docs []*yaml.Node) (leaves []yamlLeaf, ok bool) {
	for doc, root := range docs {
		if yamlNestsDeeper(root, maxYamlLeafDepth) {
			return nil, false
		}
		leaves = appendYamlLeaves(leaves, doc, nil, root)
	}
	return leaves, true
}

// yamlNestsDeeper reports whether some node under n has more than depth
// mappings and sequences above it, counting n. Under a document, those are the
// keys and list items on the node's path. It looks no deeper than that.
func yamlNestsDeeper(n *yaml.Node, depth int) bool {
	if n.Kind == yaml.MappingNode || n.Kind == yaml.SequenceNode {
		depth--
	}
	for _, child := range n.Content {
		if depth < 0 || yamlNestsDeeper(child, depth) {
			return true
		}
	}
	return false
}

// yamlDocuments decodes the documents of content, in order. When one does not
// parse, the documents decoded before it come back with the error.
func yamlDocuments(content string) ([]*yaml.Node, error) {
	dec := yaml.NewDecoder(strings.NewReader(content))
	var docs []*yaml.Node
	for {
		var root yaml.Node
		err := dec.Decode(&root)
		if errors.Is(err, io.EOF) {
			return docs, nil
		}
		if err != nil {
			return docs, err
		}
		docs = append(docs, &root)
	}
}

// appendYamlLeaves appends the leaves of n, which path leads to, to leaves.
func appendYamlLeaves(leaves []yamlLeaf, doc int, path []string, n *yaml.Node) []yamlLeaf {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, child := range n.Content {
			leaves = appendYamlLeaves(leaves, doc, path, child)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			leaves = appendYamlLeaves(leaves, doc, append(path[:len(path):len(path)], n.Content[i].Value), n.Content[i+1])
		}
	case yaml.SequenceNode:
		for i, item := range n.Content {
			leaves = appendYamlLeaves(leaves, doc, append(path[:len(path):len(path)], strconv.Itoa(i)), item)
		}
	case yaml.AliasNode:
		// By where its anchor is, not by name alone: YAML lets a later anchor
		// reuse a name, and with the later one's line commented out the alias
		// would resolve to the earlier one, reading the same while its value
		// changed. Commenting a line out moves no other node, so an alias
		// whose anchor is untouched reads the same.
		leaves = append(leaves, yamlLeaf{doc: doc, path: path, value: fmt.Sprintf("*%s@%d:%d", n.Value, n.Alias.Line, n.Alias.Column)})
	case yaml.ScalarNode:
		if n.ShortTag() != "!!null" {
			leaves = append(leaves, yamlLeaf{doc: doc, path: path, value: n.Value})
		}
	}
	return leaves
}

// onlyKeyLeavesRemoved reports whether is holds exactly was's leaves less some
// that belong to key: under the top-level entry named key, dots and all, or
// under its nested path, the two spellings the line matcher comments out.
func onlyKeyLeavesRemoved(was, is []yamlLeaf, key string) bool {
	left := make(map[string]int, len(was))
	for _, leaf := range was {
		left[leaf.id()]++
	}
	for _, leaf := range is {
		id := leaf.id()
		if left[id] == 0 {
			return false
		}
		left[id]--
	}
	segments := strings.Split(key, ".")
	for _, leaf := range was {
		p := leaf.path
		ofKey := (len(p) > 0 && p[0] == key) || (len(p) >= len(segments) && slices.Equal(p[:len(segments)], segments))
		if left[leaf.id()] > 0 && !ofKey {
			return false
		}
	}
	return true
}

// yamlHoldsKey reports whether some document of content has an entry for key
// in either spelling the line matcher comments out: a top-level entry named
// key, dots and all, or its nested path. An entry whose value is null holds
// the key too, and so does one under either copy of a section the file writes
// twice, since the matcher comments out both.
//
// When a later document does not parse, the documents before it answer: bd
// reads the first of them, and an edit to them is checked like any other
// (collateralUnsetRefusal). The document that does not parse, and any after
// it, is read by nobody, so a key there is not set. A file whose first
// document does not parse gives no answer, so it is taken to hold the key,
// and the line matcher keeps its best effort there, as it always has.
func yamlHoldsKey(content, key string) bool {
	docs, err := yamlDocuments(content)
	if err != nil && len(docs) == 0 {
		return true
	}
	segments := strings.Split(key, ".")
	for _, doc := range docs {
		for _, top := range doc.Content {
			if mappingHoldsPath(top, []string{key}) || mappingHoldsPath(top, segments) {
				return true
			}
		}
	}
	return false
}

// mappingHoldsPath reports whether n leads down path through mappings, one key
// per step. Every entry of a name that repeats is followed.
func mappingHoldsPath(n *yaml.Node, path []string) bool {
	level := []*yaml.Node{n}
	for _, name := range path {
		var next []*yaml.Node
		for _, m := range level {
			if m.Kind != yaml.MappingNode {
				continue
			}
			for i := 0; i+1 < len(m.Content); i += 2 {
				if k := m.Content[i]; k.Kind == yaml.ScalarNode && k.Value == name {
					next = append(next, m.Content[i+1])
				}
			}
		}
		level = next
	}
	return len(level) > 0
}

// blockScalarIndent reports the indentation of a key whose value is a literal
// or folded block scalar (`notes: |`), or -1 when the line opens no block.
func blockScalarIndent(line string) int {
	_, indent, ok := yamlKeyOnLine(line)
	if !ok {
		return -1
	}
	_, rest, _ := strings.Cut(strings.TrimLeft(line, " \t"), ":")
	rest = strings.TrimSpace(rest)
	// "|" and ">" are only ever block indicators in value position; a plain
	// scalar cannot start with either.
	if rest == "" || (rest[0] != '|' && rest[0] != '>') {
		return -1
	}
	return indent
}

func lineIndent(line string) int {
	return len(line) - len(strings.TrimLeft(line, " \t"))
}

// yamlKeyOnLine reports the key a mapping line declares and its indentation.
// Comments, list items and blank lines declare nothing.
func yamlKeyOnLine(line string) (name string, indent int, ok bool) {
	trimmed := strings.TrimLeft(line, " \t")
	if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "- ") {
		return "", 0, false
	}
	key, _, found := strings.Cut(trimmed, ":")
	if !found {
		return "", 0, false
	}
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, " \t") {
		return "", 0, false
	}
	return key, lineIndent(line), true
}

// formatYamlValue formats a value appropriately for YAML.
func formatYamlValue(value string) string {
	// Boolean values
	lower := strings.ToLower(value)
	if lower == "true" || lower == "false" {
		return lower
	}

	// Numeric values - return as-is
	if isNumeric(value) {
		return value
	}

	// Duration values (like "30s", "5m") - return as-is
	if isDuration(value) {
		return value
	}

	// For all other string-like values, quote to preserve YAML string semantics
	return fmt.Sprintf("%q", value)
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range s {
		if c == '-' && i == 0 {
			continue
		}
		if c == '.' {
			continue
		}
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isDuration(s string) bool {
	if len(s) < 2 {
		return false
	}
	suffix := s[len(s)-1]
	if suffix != 's' && suffix != 'm' && suffix != 'h' {
		return false
	}
	return isNumeric(s[:len(s)-1])
}

// validateYamlConfigValue validates a configuration value before setting.
// Returns an error if the value is invalid for the given key.
func validateYamlConfigValue(key, value string) error {
	switch key {
	case "hierarchy.max-depth":
		// Must be a positive integer >= 1 (GH#995)
		depth, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("hierarchy.max-depth must be a positive integer, got %q", value)
		}
		if depth < 1 {
			return fmt.Errorf("hierarchy.max-depth must be at least 1, got %d", depth)
		}
	case "dolt.shared-server":
		lower := strings.ToLower(value)
		if lower != "true" && lower != "false" {
			return fmt.Errorf("dolt.shared-server must be \"true\" or \"false\", got %q", value)
		}
	case "dolt.remotesapi-port":
		port, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || port < 0 || port > 65535 {
			return fmt.Errorf("dolt.remotesapi-port must be 0 (disabled) or a valid port number (1-65535), got %q", value)
		}
	case "dolt.debug":
		lower := strings.ToLower(value)
		if lower != "true" && lower != "false" {
			return fmt.Errorf("dolt.debug must be \"true\" or \"false\", got %q", value)
		}
	case "dolt.mode":
		lower := strings.ToLower(value)
		if lower != "server" && lower != "embedded" {
			return fmt.Errorf("dolt.mode must be \"server\" or \"embedded\", got %q", value)
		}
	case "prime.max-memories":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("prime.max-memories must be a non-negative integer (0 = unlimited), got %q", value)
		}
		if n < 0 {
			return fmt.Errorf("prime.max-memories must be a non-negative integer (0 = unlimited), got %q", value)
		}
	case "prime.max-memory-chars":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("prime.max-memory-chars must be a non-negative integer (0 = unlimited), got %q", value)
		}
		if n < 0 {
			return fmt.Errorf("prime.max-memory-chars must be a non-negative integer (0 = unlimited), got %q", value)
		}
	case "prime.disabled":
		// The set viper's GetBool reads, so a stored value means what it says.
		if _, err := strconv.ParseBool(value); err != nil {
			return fmt.Errorf("prime.disabled must be true or false, got %q", value)
		}
	case "memories.budget-chars":
		n, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("memories.budget-chars must be a non-negative integer (0 = off), got %q", value)
		}
		if n < 0 {
			return fmt.Errorf("memories.budget-chars must be a non-negative integer (0 = off), got %q", value)
		}
	}
	return nil
}
