//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
)

// servedApplyBatchExternalChecks drives POST /v0/beads/issues:batchApply at
// baseURL over a workspace seeded by seedReadyEquivalenceWorkspace, with
// eq-nowhere already closed by force: a close item or a closing update item on
// externally blocked work refuses the whole request as 409 not_closable and
// writes nothing — including when an earlier item of the same request adds the
// `external:` edge; a satisfied blocker, an unrelated item and a forced item
// land; re-closing already-closed blocked work is the idempotent no-op.
func servedApplyBatchExternalChecks(t *testing.T, baseURL string) {
	t.Helper()
	client := &http.Client{Timeout: 60 * time.Second}
	do := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, baseURL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatalf("%s %s: decode %q: %v", method, path, raw, err)
			}
		}
		return resp.StatusCode, m
	}
	status := func(id string) string {
		t.Helper()
		code, body := do(http.MethodGet, "/v0/beads/issues/"+id, "")
		if code != http.StatusOK {
			t.Fatalf("GET %s: %d %v", id, code, body)
		}
		if issue, ok := body["issue"].(map[string]any); ok {
			body = issue
		}
		s, _ := body["status"].(string)
		return s
	}
	apply := func(items string) (int, map[string]any) {
		t.Helper()
		return do(http.MethodPost, "/v0/beads/issues:batchApply", `{"actor":"ba","items":[`+items+`]}`)
	}
	closeItem := func(id, extra string) string {
		return `{"kind":"close","close":{"target":{"id":"` + id + `"}` + extra + `}}`
	}
	closingUpdate := func(id, extra string) string {
		return `{"kind":"update","update":{"target":{"id":"` + id + `"},"patch":{"status":"closed"}` + extra + `}}`
	}
	refused := func(op string, code int, body map[string]any) {
		t.Helper()
		if code != http.StatusConflict || body["code"] != "not_closable" {
			t.Errorf("%s: %d %v, want 409 not_closable", op, code, body)
		}
	}

	code, body := apply(closeItem("eq-free1", "") + "," + closeItem("eq-unsat", ""))
	refused("close item", code, body)
	code, body = apply(closingUpdate("eq-unsat", ""))
	refused("closing update item", code, body)
	code, body = apply(`{"kind":"dep_add","dep_add":{"source":{"id":"eq-free2"},"target":{"id":"external:foreign:pending"},"type":"blocks"}},` + closeItem("eq-free2", ""))
	refused("edge added earlier in the request", code, body)
	for _, id := range []string{"eq-unsat", "eq-free1", "eq-free2"} {
		if got := status(id); got != "open" {
			t.Errorf("%s is %q after a refused batch, want open (all or nothing)", id, got)
		}
	}

	if code, body := apply(closeItem("eq-sat", "") + "," + closingUpdate("eq-free1", "")); code != http.StatusOK {
		t.Errorf("satisfied + unrelated: %d %v", code, body)
	}
	if code, body := apply(closeItem("eq-nowhere", "") + "," + closingUpdate("eq-nowhere", "")); code != http.StatusOK {
		t.Errorf("re-close of already-closed blocked work: %d %v", code, body)
	}
	if code, body := apply(closingUpdate("eq-malformed", `,"force_close_policy":true`) + "," + closeItem("eq-unsat", `,"force":true`)); code != http.StatusOK {
		t.Errorf("forced: %d %v", code, body)
	}
	for _, id := range []string{"eq-sat", "eq-free1", "eq-nowhere", "eq-malformed", "eq-unsat"} {
		if got := status(id); got != "closed" {
			t.Errorf("%s is %q, want closed", id, got)
		}
	}
}

// TestServedApplyBatchHonorsExternalBlockers runs the batch-apply checks on
// both of bd serve's arms against real Dolt: the store arm (the embedded
// store wrapped in the policy, served through serveIssueRoles) under
// BEADS_TEST_EMBEDDED_DOLT=1, and the provider arm (a real `bd serve` over a
// proxied workspace) under BEADS_TEST_PROXIED_SERVER=1.
func TestServedApplyBatchHonorsExternalBlockers(t *testing.T) {
	embedded := os.Getenv("BEADS_TEST_EMBEDDED_DOLT") == "1"
	proxied := os.Getenv("BEADS_TEST_PROXIED_SERVER") == "1"
	if !embedded && !proxied {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 and/or BEADS_TEST_PROXIED_SERVER=1")
	}
	bd := buildEmbeddedBD(t)
	foreignDir, _, _ := bdInit(t, bd, "--prefix", "fx")
	foreignRun := func(args ...string) {
		t.Helper()
		cmd := exec.Command(bd, args...)
		cmd.Dir = foreignDir
		cmd.Env = bdEnv(foreignDir)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("foreign bd %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	foreignRun("create", "--silent", "--id", "fx-ok", "Provides ok", "--labels", "provides:ok")
	foreignRun("close", "fx-ok")
	foreignRun("create", "--silent", "--id", "fx-pending", "Will provide pending", "--labels", "provides:pending")

	if embedded {
		t.Run("served-store", func(t *testing.T) {
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", "eq")
			run := func(args ...string) {
				t.Helper()
				if out, err := bdRunWithFlockRetry(t, bd, dir, args...); err != nil {
					t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
			seedReadyEquivalenceWorkspace(t, dir, foreignDir, run)
			run("close", "--force", "eq-nowhere")
			raw, err := newDoltStoreFromConfig(context.Background(), beadsDir)
			if err != nil {
				t.Fatalf("open the embedded workspace: %v", err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			chain := externaldeps.Wrap(raw,
				func(project externaldeps.ProjectName) (string, bool) { return foreignDir, project == "foreign" },
				func(ctx context.Context, root string) (storage.DoltStorage, error) {
					return newReadOnlyStoreFromConfig(ctx, filepath.Join(root, ".beads"))
				})
			roles, err := serveIssueRoles(chain, false)
			if err != nil {
				t.Fatalf("serveIssueRoles: %v", err)
			}
			servedApplyBatchExternalChecks(t, "http://"+listenForTest(t, serveRolesTestConfig(roles)))
		})
	}
	if proxied {
		t.Run("served-provider", func(t *testing.T) {
			p := newSharedProxiedProject(t, bd, "eq")
			run := func(args ...string) {
				t.Helper()
				if out, err := bdProxiedRun(t, bd, p.dir, args...); err != nil {
					t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
			seedReadyEquivalenceWorkspace(t, p.dir, foreignDir, run)
			run("close", "--force", "eq-nowhere")
			sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))
			servedApplyBatchExternalChecks(t, "http://"+sp.addr)
			sp.shutdown(t)
		})
	}
}
