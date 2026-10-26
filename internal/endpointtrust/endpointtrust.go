package endpointtrust

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/lockfile"
)

const pinsFileName = "trusted-endpoints.json"

var (
	pinsMu   sync.Mutex
	guidance = map[string]struct {
		key  string
		hint string
	}{
		"ai":          {"ai.base_url", "BD_AI_BASE_URL"},
		"github":      {"github.url", "GITHUB_API_URL"},
		"gitlab":      {"gitlab.url", "GITLAB_URL"},
		"ado":         {"ado.url", "AZURE_DEVOPS_URL"},
		"jira":        {"jira.url", "JIRA_URL"},
		"linear":      {"linear.api_endpoint", "LINEAR_API_ENDPOINT"},
		"dolt-server": {"dolt_server_host", "BEADS_DOLT_SERVER_HOST"},
	}
)

type pins struct {
	Version    int                          `json:"version"`
	Workspaces map[string]map[string]string `json:"workspaces"`
}

type MismatchError struct {
	Kind   string
	Key    string
	Pinned string
	Got    string
	Hint   string
}

func (e *MismatchError) Error() string {
	return fmt.Sprintf("%s for this workspace changed from %s to %s. Your %s credential was not sent. If this change is intended, set %s in your environment or .beads/config.local.yaml.",
		e.Key, e.Pinned, e.Got, e.Kind, e.Hint)
}

func Origin(endpoint string) (string, error) {
	if strings.HasPrefix(endpoint, "unix:") {
		path := strings.TrimPrefix(endpoint, "unix:")
		if path == "" {
			return "", errors.New("unix endpoint has no path")
		}
		abs, err := filepath.Abs(path)
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
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("endpoint %q has an invalid port", endpoint)
		}
		return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(n)), nil
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, nil
}

func WorkspaceKey() string {
	key := os.Getenv("BEADS_DIR")
	if key != "" {
		if info, err := os.Stat(key); err != nil || !info.IsDir() {
			key = ""
		}
	}
	if key == "" {
		if path := config.ConfigFileUsed(); path != "" {
			key = filepath.Dir(path)
		}
	}
	if key == "" {
		return ""
	}
	key, _ = cleanWorkspaceKey(key)
	return key
}

func PrivateMatch(key, envVar, value string) bool {
	if value == "" {
		return false
	}
	want, err := Origin(value)
	if err != nil {
		return false
	}
	if envVar != "" {
		if envValue := os.Getenv(envVar); envValue != "" {
			if got, err := Origin(envValue); err == nil && got == want {
				return true
			}
		}
	}
	if configValue := config.GetString(key); configValue != "" && !config.IsFromProjectConfig(key) {
		if got, err := Origin(configValue); err == nil && got == want {
			return true
		}
	}
	return false
}

func Check(workspaceKey, credKind, endpoint string, fromPrivateSource bool) error {
	hint, ok := guidance[credKind]
	if !ok {
		return fmt.Errorf("unsupported credential kind %q", credKind)
	}
	if endpoint == "" {
		return nil
	}
	origin, err := Origin(endpoint)
	if err != nil {
		return err
	}
	if credKind == "dolt-server" && isLocalDoltEndpoint(endpoint) {
		return nil
	}
	key, err := cleanWorkspaceKey(workspaceKey)
	if err != nil {
		return err
	}
	if key == "" {
		if fromPrivateSource {
			return nil
		}
		return errors.New("cannot determine workspace")
	}

	pinsMu.Lock()
	defer pinsMu.Unlock()

	lock, err := acquirePinsLock()
	if err != nil {
		if fromPrivateSource {
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
		if fromPrivateSource {
			_ = writePin(key, credKind, origin, false)
			return nil
		}
		return err
	}
	pinned := current.Workspaces[key][credKind]
	if fromPrivateSource {
		if pinned == origin {
			return nil
		}
		_ = writePin(key, credKind, origin, false)
		return nil
	}
	if pinned == "" {
		if err := writePin(key, credKind, origin, true); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "beads: trusting %s endpoint %s for this workspace\n", credKind, origin)
		return nil
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
