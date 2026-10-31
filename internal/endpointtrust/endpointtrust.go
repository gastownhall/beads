package endpointtrust

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
)

const (
	pinsFileName = "trusted-endpoints.json"
	// localDoltOrigin is the pin recorded for bd's own loopback Dolt server.
	localDoltOrigin = "local-dolt-server"
)

var (
	pinsMu        sync.Mutex
	currentEUIDFn = os.Geteuid
	guidance      = map[string]struct {
		key           string
		hint          string
		defaultOrigin string
	}{
		"ai":          {"ai.base_url", "BD_AI_BASE_URL", "https://api.anthropic.com:443"},
		"github":      {"github.url", "GITHUB_API_URL", "https://api.github.com:443"},
		"gitlab":      {"gitlab.url", "GITLAB_URL", "https://gitlab.com:443"},
		"ado":         {"ado.url", "AZURE_DEVOPS_URL", "https://dev.azure.com:443"},
		"jira":        {"jira.url", "JIRA_URL", ""},
		"linear":      {"linear.api_endpoint", "LINEAR_API_ENDPOINT", "https://api.linear.app:443/graphql"},
		"dolt-server": {"dolt_server_host", "BEADS_DOLT_SERVER_HOST", ""},
	}
)

type pins struct {
	Version    int                          `json:"version"`
	Workspaces map[string]map[string]string `json:"workspaces"`
}

// MismatchError reports that a shared credential endpoint differs from the workspace's pinned origin.
type MismatchError struct {
	Kind   string
	Key    string
	Pinned string
	Got    string
	Hint   string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("%s for this workspace changed from %s to %s. Your %s credential was not sent. If this change is intended, set %s in your environment or %s in ~/.config/bd/config.yaml.",
		e.Key, e.Pinned, e.Got, e.Kind, e.Hint, e.Key)
}

// FirstUseError reports a shared credential endpoint that has no pin for
// this workspace yet: no private source has confirmed it, so the
// credential is not sent until one does.
type FirstUseError struct {
	Kind string
	Key  string
	Got  string
	Hint string
}

func (e *FirstUseError) Error() string {
	return fmt.Sprintf("%s for this workspace is %s, which no private source has confirmed. Your %s credential was not sent. If this endpoint is intended, set %s in your environment or %s in ~/.config/bd/config.yaml.",
		e.Key, e.Got, e.Kind, e.Hint, e.Key)
}

// Origin normalizes an endpoint to a scheme/host/port[/path] origin or an absolute Unix socket origin.
func Origin(endpoint string) (string, error) {
	if strings.HasPrefix(endpoint, "unix:") {
		sockPath := strings.TrimPrefix(endpoint, "unix:")
		if sockPath == "" {
			return "", errors.New("unix endpoint has no path")
		}
		abs, err := filepath.Abs(sockPath)
		if err != nil {
			return "", fmt.Errorf("resolve unix endpoint %q: %w", endpoint, err)
		}
		return "unix:" + filepath.Clean(abs), nil
	}

	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse endpoint %q: %w", endpoint, err)
	}
	if u.Scheme == "" || u.Hostname() == "" {
		return "", fmt.Errorf("endpoint %q must include a scheme and host", endpoint)
	}
	scheme := strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		}
	}
	var hostPort string
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("endpoint %q has an invalid port", endpoint)
		}
		hostPort = net.JoinHostPort(host, strconv.Itoa(n))
	} else {
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		hostPort = host
	}
	cleanPath := ""
	if rawPath := u.EscapedPath(); rawPath != "" && rawPath != "/" {
		decoded, err := url.PathUnescape(rawPath)
		if err != nil {
			return "", fmt.Errorf("unescape endpoint path %q: %w", endpoint, err)
		}
		cleaned := path.Clean("/" + strings.TrimPrefix(decoded, "/"))
		if cleaned != "/" {
			cleanPath = (&url.URL{Path: cleaned}).EscapedPath()
		}
	}
	return scheme + "://" + hostPort + cleanPath, nil
}

// WorkspaceKey returns the canonical workspace key used for endpoint pins, or empty when unknown.
//
// The workspace bd discovered before applying .beads/.env wins, so a
// BEADS_DIR line in a committed .env cannot move the pins to a fresh key.
// A BEADS_DIR the caller exported comes next, then the project config.yaml
// (never a user-level config file, which would share pins across workspaces).
func WorkspaceKey() string {
	var key string
	for _, candidate := range []string{config.WorkspaceBeadsDir(), config.PrivateGetenv("BEADS_DIR")} {
		if candidate == "" {
			continue
		}
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			key = candidate
			break
		}
	}
	if key == "" {
		if path := config.ProjectConfigFileUsed(); path != "" {
			key = filepath.Dir(path)
		}
	}
	if key == "" {
		return ""
	}
	key, _ = cleanWorkspaceKey(key)
	return key
}

// PrivateMatch reports whether value matches a private environment variable
// (excluding .beads/.env) or user-level global configuration.
func PrivateMatch(key, envVar, value string) bool {
	if value == "" {
		return false
	}
	want, err := Origin(value)
	if err != nil {
		return false
	}
	if envVar != "" {
		if envValue := config.PrivateGetenv(envVar); envValue != "" {
			if got, err := Origin(envValue); err == nil && got == want {
				return true
			}
		}
	}
	if key != "" {
		if configValue := config.PrivateConfigValue(key); configValue != "" {
			if got, err := Origin(configValue); err == nil && got == want {
				return true
			}
		}
	}
	return false
}

// Check enforces workspace-scoped endpoint trust before a credential is sent.
// Private sources can confirm a changed origin; shared changes return a MismatchError.
func Check(workspaceKey, credKind, endpoint string, fromPrivateSource bool) error {
	hint, ok := guidance[credKind]
	if !ok {
		return fmt.Errorf("unsupported credential kind %q", credKind)
	}
	// The vendor default (an empty endpoint, or one naming the default origin)
	// and bd's own loopback Dolt server are code-path values, not shared
	// config, so they are trusted like a private source: they are pinned
	// (re-pinned if needed) so a later shared custom endpoint is a pin change
	// that needs private confirmation instead of a silent first use.
	var origin string
	if endpoint == "" {
		if hint.defaultOrigin == "" {
			return nil
		}
		origin = hint.defaultOrigin
	} else {
		var err error
		origin, err = Origin(endpoint)
		if err != nil {
			return err
		}
		if credKind == "dolt-server" {
			if strings.HasPrefix(origin, "unix:") {
				// A socket is not pinned (it is checked beside its TCP
				// fallback), but a socket another user owns must not
				// receive the credential.
				return checkUnixSocketOwnership(strings.TrimPrefix(origin, "unix:"))
			}
			if isLocalDoltEndpoint(endpoint) {
				origin = localDoltOrigin
			}
		}
	}
	trusted := fromPrivateSource || origin == hint.defaultOrigin || origin == localDoltOrigin
	key, err := cleanWorkspaceKey(workspaceKey)
	if err != nil {
		return err
	}
	if key == "" {
		if trusted {
			return nil
		}
		return errors.New("cannot determine workspace")
	}

	pinsMu.Lock()
	defer pinsMu.Unlock()

	lock, err := acquirePinsLock()
	if err != nil {
		if trusted {
			return nil
		}
		return fmt.Errorf("lock trusted endpoints: %w", err)
	}
	defer func() {
		_ = lockfile.FlockUnlock(lock)
		_ = lock.Close()
	}()

	current, err := readPins()
	if err != nil {
		if trusted {
			_ = writePin(key, credKind, origin, false)
			return nil
		}
		return err
	}
	pinned := current.Workspaces[key][credKind]
	if trusted {
		if pinned == origin {
			return nil
		}
		_ = writePin(key, credKind, origin, false)
		return nil
	}
	if pinned == "" {
		// A shared endpoint that has never been privately confirmed is the
		// scenario this gate exists for: pinning it silently on first use
		// would trust whatever a committed config pointed at.
		return &FirstUseError{Kind: credKind, Key: hint.key, Got: origin, Hint: hint.hint}
	}
	if pinned != origin {
		return &MismatchError{Kind: credKind, Key: hint.key, Pinned: pinned, Got: origin, Hint: hint.hint}
	}
	return nil
}

func acquirePinsLock() (*os.File, error) {
	path, err := pinsPath()
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create trusted endpoints directory: %w", err)
	}
	lockPath := path + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600) // #nosec G304 -- path is under the user's config directory.
	if err != nil {
		return nil, fmt.Errorf("open trusted endpoints lock: %w", err)
	}
	if err := lock.Chmod(0600); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("secure trusted endpoints lock: %w", err)
	}
	if err := lockfile.FlockExclusiveBlocking(lock); err != nil {
		_ = lock.Close()
		return nil, fmt.Errorf("acquire trusted endpoints lock: %w", err)
	}
	return lock, nil
}

func cleanWorkspaceKey(key string) (string, error) {
	if key == "" {
		return "", nil
	}
	abs, err := filepath.Abs(key)
	if err != nil {
		return "", fmt.Errorf("resolve workspace %q: %w", key, err)
	}
	if _, err := os.Stat(abs); err == nil {
		if resolved, err := filepath.EvalSymlinks(abs); err == nil {
			abs = resolved
		}
	}
	return filepath.Clean(abs), nil
}

func checkUnixSocketOwnership(sockPath string) error {
	euid := currentEUIDFn()
	if euid < 0 {
		return nil
	}
	info, err := os.Lstat(sockPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat unix socket %q: %w", sockPath, err)
	}
	sys := reflect.ValueOf(info.Sys())
	if sys.Kind() == reflect.Ptr && !sys.IsNil() {
		uidField := sys.Elem().FieldByName("Uid")
		if uidField.IsValid() && uidField.CanUint() {
			if uid := uidField.Uint(); uid != uint64(euid) {
				return fmt.Errorf("unix socket %q is owned by uid %d, not current uid %d", sockPath, uid, euid)
			}
		}
	}
	return nil
}

func isLocalDoltEndpoint(endpoint string) bool {
	if strings.HasPrefix(endpoint, "unix:") {
		return true
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	switch configfile.ClassifyDoltHost(u.Hostname()) {
	case configfile.DoltHostBdServer, configfile.DoltHostIPv6Loopback, configfile.DoltHostOtherLoopback:
		return true
	default:
		return false
	}
}

func pinsPath() (string, error) {
	if runtime.GOOS == "windows" {
		if appdata := strings.TrimSpace(os.Getenv("APPDATA")); appdata != "" && filepath.IsAbs(appdata) {
			return filepath.Join(filepath.Clean(appdata), "beads", pinsFileName), nil
		}
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(filepath.Clean(xdg), "beads", pinsFileName), nil
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.IsAbs(home) {
		return filepath.Join(filepath.Clean(home), ".config", "beads", pinsFileName), nil
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("find user config directory: %w", err)
	}
	return filepath.Join(configDir, "beads", pinsFileName), nil
}

func readPins() (pins, error) {
	path, err := pinsPath()
	if err != nil {
		return pins{}, err
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is under the user's config directory.
	if errors.Is(err, os.ErrNotExist) {
		return pins{Version: 1, Workspaces: map[string]map[string]string{}}, nil
	}
	if err != nil {
		return pins{}, fmt.Errorf("read trusted endpoints %s: %w", path, err)
	}
	var stored pins
	if err := json.Unmarshal(data, &stored); err != nil {
		return pins{}, fmt.Errorf("parse trusted endpoints %s: %w", path, err)
	}
	if stored.Version != 1 || stored.Workspaces == nil {
		return pins{}, fmt.Errorf("invalid trusted endpoints file %s", path)
	}
	return stored, nil
}

func writePin(workspace, credKind, origin string, onlyIfUnset bool) error {
	path, err := pinsPath()
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create trusted endpoints directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil { // #nosec G302 -- the config directory must be private.
		return fmt.Errorf("secure trusted endpoints directory: %w", err)
	}

	// Re-read immediately before updating so pins added by another invocation
	// are retained and a first-use race cannot silently replace a different pin.
	stored, err := readPins()
	if err != nil {
		return err
	}
	workspacePins := stored.Workspaces[workspace]
	if workspacePins == nil {
		workspacePins = map[string]string{}
		stored.Workspaces[workspace] = workspacePins
	}
	if existing := workspacePins[credKind]; onlyIfUnset && existing != "" {
		if existing != origin {
			hint := guidance[credKind]
			return &MismatchError{Kind: credKind, Key: hint.key, Pinned: existing, Got: origin, Hint: hint.hint}
		}
		return nil
	}
	workspacePins[credKind] = origin
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("encode trusted endpoints: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".trusted-endpoints-*.tmp")
	if err != nil {
		return fmt.Errorf("create trusted endpoints temporary file: %w", err)
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("secure trusted endpoints file: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write trusted endpoints: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync trusted endpoints: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close trusted endpoints: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace trusted endpoints: %w", err)
	}
	if directory, err := os.Open(dir); err == nil { // #nosec G304 -- dir is the user's config directory.
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}
