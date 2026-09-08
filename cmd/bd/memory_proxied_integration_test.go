//go:build cgo

package main

import (
	"strings"
	"testing"
)

func bdProxiedMem(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, args...)
	if err != nil {
		t.Fatalf("bd %s failed: %v\nstdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), err, stdout, stderr)
	}
	return stdout
}

func bdProxiedMemFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := bdProxiedRunBuffers(t, bd, dir, args...)
	if err == nil {
		t.Fatalf("expected bd %s to fail; got stdout:\n%s\nstderr:\n%s",
			strings.Join(args, " "), stdout, stderr)
	}
	return stdout + stderr
}

func TestProxiedServerMemory(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)

	t.Run("remember_memories_forget_recall_journey", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pmem")

		// Multi-line content must round-trip byte-for-byte through recall.
		content := "Dolt phantom DBs hide in three places:\n  1. the data dir\n  2. the global config\n  3. the server cache"
		out := bdProxiedMem(t, bd, p.dir, "remember", content, "--key", "dolt-phantoms")
		if !strings.Contains(out, "Remembered [dolt-phantoms]") {
			t.Errorf("expected 'Remembered [dolt-phantoms]', got: %s", out)
		}

		// Auto-generated key from prose content.
		out = bdProxiedMem(t, bd, p.dir, "remember", "always run tests with -race flag")
		if !strings.Contains(out, "Remembered [always-run-tests-with-race-flag]") {
			t.Errorf("expected slug key in output, got: %s", out)
		}

		// Memories land under kv.memory.<key> in the config table — the
		// namespace `bd export --all` sweeps — but the settings plane does not
		// serve them. A point read answers exactly as an unset key does, so a
		// caller cannot tell a refusal from an absence. This is the proxied
		// leg of the same firewall the role contract pins; recall below is the
		// route that still reads the content.
		out = bdProxiedMem(t, bd, p.dir, "config", "get", "kv.memory.dolt-phantoms")
		if !strings.Contains(out, "(not set)") {
			t.Errorf("expected kv.memory.dolt-phantoms to read as unset through config get, got: %q", out)
		}
		if strings.Contains(out, content) {
			t.Errorf("config get leaked memory content: %q", out)
		}

		// List all.
		out = bdProxiedMem(t, bd, p.dir, "memories")
		if !strings.Contains(out, "Memories (2):") {
			t.Errorf("expected 'Memories (2):', got: %s", out)
		}
		for _, key := range []string{"dolt-phantoms", "always-run-tests-with-race-flag"} {
			if !strings.Contains(out, key) {
				t.Errorf("expected %q in memories listing: %s", key, out)
			}
		}

		// Keyword search matches content, not just keys.
		out = bdProxiedMem(t, bd, p.dir, "memories", "phantom")
		if !strings.Contains(out, "dolt-phantoms") {
			t.Errorf("expected search hit for 'phantom': %s", out)
		}
		if strings.Contains(out, "always-run-tests-with-race-flag") {
			t.Errorf("search should not match unrelated memory: %s", out)
		}

		// Recall returns the full content byte-for-byte (listing truncates).
		out = bdProxiedMem(t, bd, p.dir, "recall", "dolt-phantoms")
		if out != content+"\n" {
			t.Errorf("recall not byte-preserving:\nwant: %q\ngot:  %q", content+"\n", out)
		}

		// Update in place via explicit --key.
		out = bdProxiedMem(t, bd, p.dir, "remember", "updated insight", "--key", "dolt-phantoms")
		if !strings.Contains(out, "Updated [dolt-phantoms]") {
			t.Errorf("expected 'Updated [dolt-phantoms]', got: %s", out)
		}
		out = bdProxiedMem(t, bd, p.dir, "recall", "dolt-phantoms")
		if out != "updated insight\n" {
			t.Errorf("expected updated content, got: %q", out)
		}

		// Forget removes it.
		out = bdProxiedMem(t, bd, p.dir, "forget", "dolt-phantoms")
		if !strings.Contains(out, "Forgot [dolt-phantoms]") {
			t.Errorf("expected 'Forgot [dolt-phantoms]', got: %s", out)
		}
		out = bdProxiedMem(t, bd, p.dir, "memories")
		if strings.Contains(out, "dolt-phantoms") {
			t.Errorf("expected dolt-phantoms gone after forget: %s", out)
		}

		// Forget of a missing key reports not-found and exits nonzero,
		// same as classic.
		out = bdProxiedMemFail(t, bd, p.dir, "forget", "dolt-phantoms")
		if !strings.Contains(out, "No memory with key") {
			t.Errorf("expected 'No memory with key' on double forget, got: %s", out)
		}

		// Recall of a missing key likewise.
		out = bdProxiedMemFail(t, bd, p.dir, "recall", "dolt-phantoms")
		if !strings.Contains(out, "No memory with key") {
			t.Errorf("expected 'No memory with key' on missing recall, got: %s", out)
		}
	})

	t.Run("remember_bare_key_desire_path", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pmemb")

		bdProxiedMem(t, bd, p.dir, "remember", "the auth module uses JWT not sessions", "--key", "auth-jwt")

		// A bare existing key READS instead of writing (= bd recall).
		stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "remember", "auth-jwt")
		if err != nil {
			t.Fatalf("bare-existing-key remember should recall, got error: %v\n%s%s", err, stdout, stderr)
		}
		if stdout != "the auth module uses JWT not sessions\n" {
			t.Errorf("expected recalled content on stdout, got: %q", stdout)
		}
		if !strings.Contains(stderr, "a bare existing key READS") {
			t.Errorf("expected desire-path notice on stderr, got: %q", stderr)
		}

		// A bare key naming nothing is refused, not stored.
		out := bdProxiedMemFail(t, bd, p.dir, "remember", "no-such-memory-key")
		if !strings.Contains(out, "refusing to store a bare key-like token") {
			t.Errorf("expected bare-key refusal, got: %s", out)
		}
		out = bdProxiedMem(t, bd, p.dir, "memories")
		if strings.Contains(out, "no-such-memory-key") {
			t.Errorf("refused bare key must not be stored: %s", out)
		}

		// A bare bd command name is caught by the command guard.
		out = bdProxiedMemFail(t, bd, p.dir, "remember", "list")
		if !strings.Contains(out, "looks like a command") {
			t.Errorf("expected command-word guard, got: %s", out)
		}

		// The two refusals either side of the desire path fire the same way
		// here as on the direct route — one branch, one derivation, both
		// routes. DeriveKey("") is "", so an insight nothing derives from
		// would satisfy the bare-slug test and be READ instead of refused if
		// the branch did not exclude the empty key.
		for _, tc := range []struct{ insight, want string }{
			{"", "memory content cannot be empty"},
			{"   ", "memory content cannot be empty"},
			{"!!!", "could not generate key from content"},
		} {
			out = bdProxiedMemFail(t, bd, p.dir, "remember", tc.insight)
			if !strings.Contains(out, tc.want) {
				t.Errorf("bd remember %q: expected %q, got: %s", tc.insight, tc.want, out)
			}
		}
	})

	t.Run("remember_creates_dolt_commit", func(t *testing.T) {
		t.Parallel()
		p := newSharedProxiedProject(t, bd, "pmemc")
		db := openProxiedDB(t, p)
		before := proxiedDoltHead(t, db)

		bdProxiedMem(t, bd, p.dir, "remember", "one write is one commit", "--key", "tx-shape")

		after := proxiedDoltHead(t, db)
		if after == before {
			t.Errorf("HEAD did not advance on remember: before=%s after=%s", before, after)
		}
		if n := proxiedDoltCommitCountSince(t, db, before); n != 1 {
			t.Errorf("expected exactly 1 Dolt commit for remember, got %d", n)
		}
	})
}
