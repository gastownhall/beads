package httpclient

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/atomicfile"
)

// PolicyCacheTTLEnv sets how long one bd process may trust what an earlier one
// learned from the handshake about the external-dependency policy
// (ServerEnforcesExternalDependencyPolicy): a Go duration ("5m") or a number
// of seconds ("300"), or "0"/"off" to ask the server every time. An
// unparseable value turns the cache off.
const PolicyCacheTTLEnv = "BEADS_HTTP_POLICY_CACHE_TTL"

// DefaultPolicyCacheTTL bounds how stale a cached answer can be. A server's
// answer changes only when it is restarted with a different composition.
const DefaultPolicyCacheTTL = 5 * time.Minute

// policyCache remembers, per server URL and pinned project, whether that
// server enforces bd's external-dependency policy itself. Without it every
// short-lived `bd ready` against a remote store pays a GET /v0/beads/context
// before its listing only to learn this one bit, which listReadyWork itself
// never needs a handshake for.
//
// It is a file under the user cache directory, keyed by the URL and the
// project the workspace pinned at `bd connect`; a workspace with no pin gets
// no cache. A nil *policyCache is a disabled one.
type policyCache struct {
	path      string
	url       string
	projectID string
	ttl       time.Duration
	now       func() time.Time
}

type policyCacheEntry struct {
	URL                  string `json:"url"`
	ProjectID            string `json:"project_id"`
	ExternalDependencies bool   `json:"external_dependencies"`
	// CrossRigDependencies is the handshake's wire.CapCrossRigDependencies
	// bit. An entry an older bd wrote has none, so loadCrossRig misses on it
	// and the next probe asks the server.
	CrossRigDependencies *bool     `json:"cross_rig_dependencies,omitempty"`
	CheckedAt            time.Time `json:"checked_at"`
}

// newPolicyCache returns the cache for target, or nil when the target has no
// project pin, the TTL is off, or there is no user cache directory.
func newPolicyCache(target Target) *policyCache {
	if target.BaseURL == nil || target.ExpectProjectID == "" {
		return nil
	}
	ttl := policyCacheTTL()
	if ttl <= 0 {
		return nil
	}
	dir, err := os.UserCacheDir()
	if err != nil || dir == "" {
		return nil
	}
	url := target.BaseURL.String()
	sum := sha256.Sum256([]byte(url + "\x00" + target.ExpectProjectID))
	return &policyCache{
		path:      filepath.Join(dir, "beads", "http-policy", hex.EncodeToString(sum[:16])+".json"),
		url:       target.BaseURL.Redacted(),
		projectID: target.ExpectProjectID,
		ttl:       ttl,
		now:       time.Now,
	}
}

func policyCacheTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv(PolicyCacheTTLEnv))
	if raw == "" {
		return DefaultPolicyCacheTTL
	}
	if strings.EqualFold(raw, "off") {
		return 0
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		d, err = time.ParseDuration(raw + "s")
	}
	if err != nil || d < 0 {
		return 0
	}
	return d
}

// load returns the cached answer while it is fresh. An entry written in the
// future (a clock that stepped back) is not trusted.
func (c *policyCache) load() (enforced, ok bool) {
	e, ok := c.fresh()
	if !ok {
		return false, false
	}
	return e.ExternalDependencies, true
}

// loadCrossRig is load for the cross-rig bit. An entry that does not record
// it is a miss.
func (c *policyCache) loadCrossRig() (enforced, ok bool) {
	e, ok := c.fresh()
	if !ok || e.CrossRigDependencies == nil {
		return false, false
	}
	return *e.CrossRigDependencies, true
}

func (c *policyCache) fresh() (policyCacheEntry, bool) {
	if c == nil {
		return policyCacheEntry{}, false
	}
	raw, err := os.ReadFile(c.path)
	if err != nil {
		return policyCacheEntry{}, false
	}
	var e policyCacheEntry
	if json.Unmarshal(raw, &e) != nil || e.URL != c.url || e.ProjectID != c.projectID {
		return policyCacheEntry{}, false
	}
	age := c.now().Sub(e.CheckedAt)
	if age < 0 || age >= c.ttl {
		return policyCacheEntry{}, false
	}
	return e, true
}

// store records an answer the handshake just gave. It is best effort: a
// cache that cannot be written only costs the next process a handshake.
func (c *policyCache) store(enforced bool) {
	c.write(policyCacheEntry{ExternalDependencies: enforced})
}

// storeAnswers records both policy bits of one handshake.
func (c *policyCache) storeAnswers(external, crossRig bool) {
	c.write(policyCacheEntry{ExternalDependencies: external, CrossRigDependencies: &crossRig})
}

func (c *policyCache) write(e policyCacheEntry) {
	if c == nil {
		return
	}
	e.URL, e.ProjectID, e.CheckedAt = c.url, c.projectID, c.now().UTC()
	raw, err := json.Marshal(e)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	_ = atomicfile.WriteFile(c.path, raw, 0o600)
}
