package remotecache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/steveyegge/beads/internal/config"
)

// GH#6233: federation.allowed-remote-patterns used to be a lint, not a
// boundary — a committed config.yaml reaches Cache.Ensure through routing.*
// (bd create) and repos.additional (bd repo sync), so the cache funnels
// dialed whatever remote the URL named. checkPolicy is their gate.

// checkPolicy enforces federation.allowed-remote-patterns for a cache
// transfer to remoteURL. An empty list is no gate. The URL argument is what
// bd was told to reach; on a warm entry the clone's own recorded origin is
// what dolt pull/push actually dials, so both must pass — a cache populated
// before the list tightened is not grandfathered. A warm entry whose origin
// cannot be read fails closed: an unverifiable destination is not
// authorization to dial it.
func (c *Cache) checkPolicy(remoteURL string) error {
	patterns := config.AllowedRemotePatterns()
	if len(patterns) == 0 {
		return nil
	}
	if err := ValidateRemoteWithPolicy(patterns, "cached remote", remoteURL); err != nil {
		return err
	}
	target := c.cloneTarget(remoteURL)
	if !c.doltExists(target) {
		return nil
	}
	origin, err := c.recordedOriginURL(target)
	if err != nil {
		return &RemotePolicyError{
			Subject: "cached remote",
			Cause:   fmt.Sprintf("its cached origin could not be read to enforce the allowlist: %v", err),
		}
	}
	if origin == "" {
		return &RemotePolicyError{
			Subject: "cached remote",
			Cause:   "its cached clone records no origin URL to enforce the allowlist against",
		}
	}
	return ValidateRemoteWithPolicy(patterns, "cached remote", origin)
}

// recordedOriginURL reads the origin remote's URL from the cached clone's
// .dolt/repo_state.json. doltutil owns the full reader (PersistedRemotes),
// but this package sits below it in the import graph, so the one field
// needed here is parsed inline.
func (c *Cache) recordedOriginURL(target string) (string, error) {
	data, err := os.ReadFile(filepath.Join(target, ".dolt", "repo_state.json")) // #nosec G304 -- cache-local dolt state file
	if err != nil {
		return "", err
	}
	var state struct {
		Remotes map[string]struct {
			URL string `json:"url"`
		} `json:"remotes"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return "", err
	}
	return state.Remotes["origin"].URL, nil
}
