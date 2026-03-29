//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// bdFederation runs "bd federation" with extra args. Returns combined output.
func bdFederation(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"federation"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("bd federation %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// bdFederationFail runs "bd federation" expecting failure.
func bdFederationFail(t *testing.T, bd, dir string, args ...string) string {
	t.Helper()
	fullArgs := append([]string{"federation"}, args...)
	cmd := exec.Command(bd, fullArgs...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("bd federation %s should have failed, got: %s", strings.Join(args, " "), out)
	}
	return string(out)
}

func TestEmbeddedFederation(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt federation tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)

	t.Run("list_peers_empty", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdlst0")

		out := bdFederation(t, bd, dir, "list-peers")
		if !strings.Contains(out, "No federation peers") {
			t.Errorf("expected 'No federation peers', got: %s", out)
		}
	})

	t.Run("list_peers_empty_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdlstj")

		out := bdFederation(t, bd, dir, "list-peers", "--json")
		// Should be valid JSON (empty array or null)
		out = strings.TrimSpace(out)
		if out != "null" && out != "[]" {
			// Try parsing as JSON array
			var result []interface{}
			if err := json.Unmarshal([]byte(out), &result); err != nil {
				t.Fatalf("expected valid JSON array, got: %s", out)
			}
		}
	})

	t.Run("add_peer_simple", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdadd")

		out := bdFederation(t, bd, dir, "add-peer", "test-peer", "file:///tmp/fake-peer")
		if !strings.Contains(out, "test-peer") {
			t.Errorf("expected peer name in output, got: %s", out)
		}

		// Verify it appears in list
		listOut := bdFederation(t, bd, dir, "list-peers")
		if !strings.Contains(listOut, "test-peer") {
			t.Errorf("expected 'test-peer' in list, got: %s", listOut)
		}
	})

	t.Run("add_peer_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdaddj")

		out := bdFederation(t, bd, dir, "add-peer", "json-peer", "file:///tmp/json-peer", "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if added, _ := result["added"].(string); added != "json-peer" {
			t.Errorf("expected added='json-peer', got %q", added)
		}
	})

	t.Run("add_peer_with_credentials", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdcred")

		out := bdFederation(t, bd, dir, "add-peer", "auth-peer", "file:///tmp/auth-peer",
			"--user", "admin", "--password", "secret", "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if hasAuth, _ := result["has_auth"].(bool); !hasAuth {
			t.Error("expected has_auth=true")
		}
	})

	t.Run("add_peer_with_sovereignty", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdsov")

		out := bdFederation(t, bd, dir, "add-peer", "sov-peer", "file:///tmp/sov-peer",
			"--sovereignty", "T2", "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if sov, _ := result["sovereignty"].(string); sov != "T2" {
			t.Errorf("expected sovereignty='T2', got %q", sov)
		}
	})

	t.Run("remove_peer", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdrm")

		bdFederation(t, bd, dir, "add-peer", "removable", "file:///tmp/removable")

		// Verify it exists
		listOut := bdFederation(t, bd, dir, "list-peers")
		if !strings.Contains(listOut, "removable") {
			t.Fatalf("peer should exist before removal, got: %s", listOut)
		}

		// Remove it
		out := bdFederation(t, bd, dir, "remove-peer", "removable")
		if !strings.Contains(out, "removable") {
			t.Errorf("expected peer name in removal output, got: %s", out)
		}

		// Verify it's gone
		listOut = bdFederation(t, bd, dir, "list-peers")
		if strings.Contains(listOut, "removable") {
			t.Errorf("peer should be gone after removal, got: %s", listOut)
		}
	})

	t.Run("remove_peer_json", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdrmj")

		bdFederation(t, bd, dir, "add-peer", "rm-json", "file:///tmp/rm-json")

		out := bdFederation(t, bd, dir, "remove-peer", "rm-json", "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if removed, _ := result["removed"].(string); removed != "rm-json" {
			t.Errorf("expected removed='rm-json', got %q", removed)
		}
	})

	t.Run("status_no_peers", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdst0")

		out := bdFederation(t, bd, dir, "status")
		if !strings.Contains(out, "No federation peers") {
			t.Errorf("expected 'No federation peers', got: %s", out)
		}
	})

	t.Run("status_json_no_peers", func(t *testing.T) {
		dir, _, _ := bdInit(t, bd, "--prefix", "fdstj")

		out := bdFederation(t, bd, dir, "status", "--json")
		var result map[string]interface{}
		if err := json.Unmarshal([]byte(out), &result); err != nil {
			t.Fatalf("failed to parse JSON: %v\n%s", err, out)
		}
		if _, ok := result["peers"]; !ok {
			t.Error("missing 'peers' in status JSON")
		}
	})
}

func TestEmbeddedFederationConcurrent(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt federation tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "fdconc")

	const numWorkers = 10

	type result struct {
		worker int
		out    string
		err    error
	}

	results := make([]result, numWorkers)
	var wg sync.WaitGroup
	wg.Add(numWorkers)

	for w := 0; w < numWorkers; w++ {
		go func(worker int) {
			defer wg.Done()
			peerName := fmt.Sprintf("conc-peer-%d", worker)
			peerURL := fmt.Sprintf("file:///tmp/conc-peer-%d", worker)
			cmd := exec.Command(bd, "federation", "add-peer", peerName, peerURL)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			out, err := cmd.CombinedOutput()
			results[worker] = result{worker: worker, out: string(out), err: err}
		}(w)
	}
	wg.Wait()

	successes := 0
	for _, r := range results {
		if strings.Contains(r.out, "panic") {
			t.Errorf("worker %d panicked:\n%s", r.worker, r.out)
		}
		if r.err == nil {
			successes++
		} else if !strings.Contains(r.out, "one writer at a time") {
			t.Errorf("worker %d failed with unexpected error: %v\n%s", r.worker, r.err, r.out)
		}
	}
	if successes < 1 {
		t.Errorf("expected at least 1 successful add-peer, got %d", successes)
	}
	t.Logf("%d/%d federation workers succeeded", successes, numWorkers)
}
