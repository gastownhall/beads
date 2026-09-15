package ownershiphandoffv2

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
)

// commit writes four config.yaml keys and then reads every one of them back
// through the resolver bd itself uses. That readback is the only thing standing
// between "the write succeeded" and "the write landed somewhere a reader can
// see", because config.SetYamlConfigInDir will happily write a dotted key as a
// FLAT `dolt.host:` line that config.GetStringFromDir — which splits on the dot
// and looks for a nested mapping — can never find.
//
// The shapes below are the ones that occur in the wild. A caller-managed
// workspace's config.yaml is written by the caller, not by bd, so bd does not
// get to assume it is nested.
func TestCommitWriteSetKeysAreReadableInEveryConfigShape(t *testing.T) {
	shapes := map[string]string{
		"absent":       "",
		"empty":        "",
		"comment only": "# this workspace's config\n",
		// The shape a Gas City managed city actually has: every dolt key flat,
		// no `dolt:` mapping anywhere. This is what made commit refuse itself.
		"flat dotted keys": "dolt.host: 127.0.0.1\ndolt.port: 3307\ndolt.mode: server\n",
		// Flat, and missing the key most likely to be absent.
		"flat without auto-start": "dolt.host: 127.0.0.1\ndolt.port: 3307\n",
		"nested":                  "dolt:\n    host: 127.0.0.1\n    port: 3307\n",
		// Both shapes at once, which an edited file can easily be.
		"mixed":          "dolt.host: 127.0.0.1\ndolt:\n    port: 3307\n",
		"unrelated keys": "sync:\n    branch: beads-sync\nnode_id: somewhere\n",
	}

	for name, seed := range shapes {
		t.Run(name, func(t *testing.T) {
			beadsDir := canonicalTempDir(t)
			if name != "absent" {
				if err := os.WriteFile(filepath.Join(beadsDir, configYAMLName), []byte(seed), 0o600); err != nil {
					t.Fatalf("seed config.yaml: %v", err)
				}
			}

			target := Target{Host: "127.0.0.1", Port: 45678, PID: 4242}
			if err := writeCommitYAMLKeys(beadsDir, target); err != nil {
				t.Fatalf("write config.yaml keys: %v", err)
			}

			want := map[string]string{
				"dolt.host":       "127.0.0.1",
				"dolt.port":       "45678",
				"dolt.auto-start": "true",
				"dolt.mode":       configfile.DoltModeServer,
			}
			for key, expected := range want {
				if got := config.GetStringFromDir(beadsDir, key); got != expected {
					body, _ := os.ReadFile(filepath.Join(beadsDir, configYAMLName)) //nolint:errcheck // diagnostic
					t.Errorf("config.yaml %s reads back as %q, want %q\nfile:\n%s", key, got, expected, body)
				}
			}
		})
	}
}

// The flat keys a caller left behind must not survive as a second, contradictory
// answer. A reader that happens to prefer the flat spelling would otherwise find
// the caller's old endpoint after bd committed its own.
func TestCommitWriteSetRemovesContradictoryFlatKeys(t *testing.T) {
	beadsDir := canonicalTempDir(t)
	seed := "# managed city\ndolt.host: 10.9.9.9\ndolt.port: 3307\ndolt.auto-start: false\ndolt.mode: server\nsync.branch: keep-me\n"
	if err := os.WriteFile(filepath.Join(beadsDir, configYAMLName), []byte(seed), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := writeCommitYAMLKeys(beadsDir, Target{Host: "127.0.0.1", Port: 45678}); err != nil {
		t.Fatalf("write: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(beadsDir, configYAMLName))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, stale := range []string{"dolt.host: 10.9.9.9", "dolt.port: 3307", "dolt.auto-start: false"} {
		if contains(string(body), stale) {
			t.Errorf("config.yaml still carries the caller's old %q:\n%s", stale, body)
		}
	}
	// A dotted key bd does not own is none of bd's business and stays exactly
	// as the caller wrote it.
	if !contains(string(body), "sync.branch: keep-me") {
		t.Errorf("the write set removed a key it does not own:\n%s", body)
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
