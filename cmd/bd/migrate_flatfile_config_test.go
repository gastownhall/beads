package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// failingConfigSetter fails SetConfig for one designated key and records
// every successful write.
type failingConfigSetter struct {
	failKey string
	written map[string]string
}

func (f *failingConfigSetter) SetConfig(_ context.Context, key, value string) error {
	if key == f.failKey {
		return errors.New("disk full")
	}
	f.written[key] = value
	return nil
}

// TestMigrateFlatfileConfigCopyFailureIsFatal reproduces TASKS-qwho: the
// reverse migration demoted SetConfig failures to per-key warnings, flipped
// metadata.json to backend=dolt anyway, printed success, and advised
// 'rm -rf .beads/config_kv.json' — destroying the only copy of issue_prefix
// and bd-remember memories. The forward direction aborts on the identical
// failure, so config copy must return an error (aborting the migration
// before the backend flip), never swallow it.
func TestMigrateFlatfileConfigCopyFailureIsFatal(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("failure_propagates_with_key", func(t *testing.T) {
		dst := &failingConfigSetter{failKey: "issue_prefix", written: map[string]string{}}
		err := copyAllConfig(ctx, dst, map[string]string{
			"issue_prefix": "bd",
			"memory.a":     "insight",
		})
		if err == nil {
			t.Fatal("copyAllConfig swallowed a SetConfig failure; migration would flip backend over missing config")
		}
		if !strings.Contains(err.Error(), "issue_prefix") {
			t.Errorf("error does not name the failed key: %v", err)
		}
	})

	t.Run("success_copies_every_key", func(t *testing.T) {
		dst := &failingConfigSetter{written: map[string]string{}}
		src := map[string]string{"issue_prefix": "bd", "memory.a": "insight", "custom.k": "v"}
		if err := copyAllConfig(ctx, dst, src); err != nil {
			t.Fatalf("copyAllConfig: %v", err)
		}
		if len(dst.written) != len(src) {
			t.Fatalf("copied %d keys, want %d", len(dst.written), len(src))
		}
		for k, v := range src {
			if dst.written[k] != v {
				t.Errorf("key %s = %q, want %q", k, dst.written[k], v)
			}
		}
	})
}
