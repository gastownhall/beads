//go:build cgo

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/httpapi"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/externaldeps"
	"github.com/steveyegge/beads/internal/types"
)

// readyEquivalenceAnswer is what one route answered to the matrix's three
// requests: the unlimited listing, a one-row page with its total, and a claim.
type readyEquivalenceAnswer struct {
	all       []string // ids, --limit 0
	allTotal  int
	page      []string // ids, --limit 1
	pageTotal int
	claimed   string
}

func (a readyEquivalenceAnswer) String() string {
	return fmt.Sprintf("all=%v (total %d) page=%v (total %d) claimed=%q",
		a.all, a.allTotal, a.page, a.pageTotal, a.claimed)
}

// The matrix workspace, identical on every route. Three P0 rows are held back
// by an `external:` edge — unsatisfied (the foreign project provides the
// capability only on an OPEN issue), unresolvable (no such project) and
// malformed (no capability segment) — and are created FIRST, so on a route
// that dropped the policy they would lead the listing and win the claim. One
// P0 row's edge is satisfied (a CLOSED foreign issue provides it), and two
// plain rows follow at lower priorities.
var readyEquivalenceWant = readyEquivalenceAnswer{
	all:       []string{"eq-sat", "eq-free1", "eq-free2"},
	allTotal:  3,
	page:      []string{"eq-sat"},
	pageTotal: 3,
	claimed:   "eq-sat",
}

// seedReadyEquivalenceWorkspace points the workspace in dir at foreignDir as
// external project "foreign" and creates the matrix rows through run.
func seedReadyEquivalenceWorkspace(t *testing.T, dir, foreignDir string, run func(args ...string)) {
	t.Helper()
	config := fmt.Sprintf("external_projects:\n  foreign: %q\n", foreignDir)
	if err := os.WriteFile(filepath.Join(dir, ".beads", "config.local.yaml"), []byte(config), 0o600); err != nil {
		t.Fatalf("write external project config: %v", err)
	}
	run("create", "--silent", "--id", "eq-unsat", "Waits on an unprovided capability", "--type", "task", "--priority", "0", "--deps", "blocked-by:external:foreign:pending")
	run("create", "--silent", "--id", "eq-nowhere", "Waits on an unknown project", "--type", "task", "--priority", "0", "--deps", "blocked-by:external:nowhere:thing")
	run("create", "--silent", "--id", "eq-malformed", "Waits on a malformed reference", "--type", "task", "--priority", "0", "--deps", "blocked-by:external:malformed")
	run("create", "--silent", "--id", "eq-sat", "Waits on a provided capability", "--type", "task", "--priority", "0", "--deps", "blocked-by:external:foreign:ok")
	run("create", "--silent", "--id", "eq-free1", "Plain work one", "--type", "task", "--priority", "1")
	run("create", "--silent", "--id", "eq-free2", "Plain work two", "--type", "task", "--priority", "2")
}

// cliReadyEquivalence asks the three questions through the `bd ready` command
// line; runEnv runs one bd invocation with extra environment.
func cliReadyEquivalence(t *testing.T, runEnv func(env []string, args ...string) (string, string, error)) readyEquivalenceAnswer {
	t.Helper()
	envelope := []string{"BD_JSON_ENVELOPE=1"}
	list := func(limit string) ([]string, int) {
		t.Helper()
		stdout, stderr, err := runEnv(envelope, "ready", "--json", "--limit", limit)
		if err != nil {
			t.Fatalf("bd ready --json --limit %s: %v\n%s\n%s", limit, err, stdout, stderr)
		}
		var body struct {
			Data       []types.IssueWithCounts `json:"data"`
			Pagination *PaginationMeta         `json:"pagination"`
		}
		if err := json.Unmarshal([]byte(stdout), &body); err != nil {
			t.Fatalf("parse bd ready --json --limit %s: %v\n%s", limit, err, stdout)
		}
		ids := make([]string, 0, len(body.Data))
		for _, row := range body.Data {
			ids = append(ids, row.ID)
		}
		total := len(ids)
		if body.Pagination != nil {
			total = body.Pagination.Total
		}
		return ids, total
	}
	var a readyEquivalenceAnswer
	a.all, a.allTotal = list("0")
	a.page, a.pageTotal = list("1")

	stdout, stderr, err := runEnv([]string{"BEADS_ACTOR=eq-claimer"}, "ready", "--claim", "--json")
	if err != nil {
		t.Fatalf("bd ready --claim --json: %v\n%s\n%s", err, stdout, stderr)
	}
	var claimed []types.IssueWithCounts
	if err := json.Unmarshal([]byte(stdout), &claimed); err != nil {
		t.Fatalf("parse bd ready --claim --json: %v\n%s", err, stdout)
	}
	if len(claimed) == 1 {
		a.claimed = claimed[0].ID
	}
	return a
}

// servedReadyEquivalence asks the same three questions of a bd HTTP server at
// baseURL: GET /v0/beads/ready (the page), GET /v0/beads/ready:count (the
// total) and POST /v0/beads/issues:claimNext.
func servedReadyEquivalence(t *testing.T, baseURL string) readyEquivalenceAnswer {
	t.Helper()
	client := &http.Client{Timeout: 60 * time.Second}
	do := func(method, path string, body io.Reader, out any) {
		t.Helper()
		req, err := http.NewRequest(method, baseURL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d: %s", method, path, resp.StatusCode, raw)
		}
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s %s: decode: %v\n%s", method, path, err, raw)
		}
	}
	list := func(limit string) []string {
		t.Helper()
		var page struct {
			Items []types.IssueWithCounts `json:"items"`
		}
		do(http.MethodGet, "/v0/beads/ready?limit="+limit, nil, &page)
		ids := make([]string, 0, len(page.Items))
		for _, row := range page.Items {
			ids = append(ids, row.ID)
		}
		return ids
	}
	var count struct {
		Total int `json:"total"`
	}
	do(http.MethodGet, "/v0/beads/ready:count", nil, &count)

	var a readyEquivalenceAnswer
	a.all, a.allTotal = list("0"), count.Total
	a.page, a.pageTotal = list("1"), count.Total

	var claim struct {
		Claimed *types.IssueWithCounts `json:"claimed"`
	}
	do(http.MethodPost, "/v0/beads/issues:claimNext", strings.NewReader(`{"actor":"eq-claimer"}`), &claim)
	if claim.Claimed != nil {
		a.claimed = claim.Claimed.ID
	}
	return a
}

// TestReadyExternalDependencyEquivalenceMatrix is the upstream equivalence
// matrix for the external-dependency policy: the same ready requests — every
// ready row, a one-row page with its total, and a claim — over identically
// seeded workspaces holding satisfied, unsatisfied, unresolvable and malformed
// `external:` edges, answered by every route that reaches the policy:
//
//   - cli-direct:     `bd ready` on an embedded workspace (the store's roles);
//   - served-store:   httpapi over the roles bd serve's store arm takes
//     (serveIssueRoles) off the embedded store wrapped in the policy;
//   - cli-proxied:    `bd ready --proxied-server` (the provider's roles);
//   - served-provider: a real `bd serve` over a proxied workspace (the provider
//     arm, composed by runServe itself).
//
// Every route must return the same ids, the same total and the same claimed
// id, and that answer must be the policy's. httpstore (a served client) is not
// upstream, so the served side is driven over HTTP directly.
//
// The embedded routes run under BEADS_TEST_EMBEDDED_DOLT=1 and the proxied
// ones under BEADS_TEST_PROXIED_SERVER=1; each lane checks the routes it can
// reach against the one expected answer, so the lanes agree with each other
// through it.
func TestReadyExternalDependencyEquivalenceMatrix(t *testing.T) {
	embedded := os.Getenv("BEADS_TEST_EMBEDDED_DOLT") == "1"
	proxied := os.Getenv("BEADS_TEST_PROXIED_SERVER") == "1"
	if !embedded && !proxied {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 and/or BEADS_TEST_PROXIED_SERVER=1 to run the equivalence matrix")
	}
	bd := buildEmbeddedBD(t)

	// The foreign project the `external:foreign:*` edges resolve against: a
	// CLOSED issue provides "ok", an OPEN one only claims to provide
	// "pending".
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

	answers := map[string]readyEquivalenceAnswer{}

	if embedded {
		t.Run("cli-direct", func(t *testing.T) {
			dir, _, _ := bdInit(t, bd, "--prefix", "eq")
			seedReadyEquivalenceWorkspace(t, dir, foreignDir, func(args ...string) {
				t.Helper()
				if out, err := bdRunWithFlockRetry(t, bd, dir, args...); err != nil {
					t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			})
			answers["cli-direct"] = cliReadyEquivalence(t, func(env []string, args ...string) (string, string, error) {
				cmd := exec.Command(bd, args...)
				cmd.Dir = dir
				cmd.Env = append(bdEnv(dir), env...)
				stdout, stderr, err := runCommandBuffers(t, cmd)
				return stdout.String(), stderr.String(), err
			})
		})

		t.Run("served-store", func(t *testing.T) {
			dir, beadsDir, _ := bdInit(t, bd, "--prefix", "eq")
			seedReadyEquivalenceWorkspace(t, dir, foreignDir, func(args ...string) {
				t.Helper()
				if out, err := bdRunWithFlockRetry(t, bd, dir, args...); err != nil {
					t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			})
			ctx := context.Background()
			raw, err := newDoltStoreFromConfig(ctx, beadsDir)
			if err != nil {
				t.Fatalf("open the embedded workspace: %v", err)
			}
			t.Cleanup(func() { _ = raw.Close() })
			// wireExternalDependencyPolicy's composition, with the locator
			// spelled out: that one reads external_projects through the
			// process-global config, which this test process never loaded for
			// this workspace.
			chain := externaldeps.Wrap(raw,
				func(project externaldeps.ProjectName) (string, bool) {
					return foreignDir, project == "foreign"
				},
				func(ctx context.Context, root string) (storage.DoltStorage, error) {
					return newReadOnlyStoreFromConfig(ctx, filepath.Join(root, ".beads"))
				})
			roles, err := serveIssueRoles(chain, false)
			if err != nil {
				t.Fatalf("serveIssueRoles: %v", err)
			}
			cfg := serveRolesTestConfig(roles)
			if !cfg.ExternalDependencyPolicy {
				t.Fatal("serve's store arm did not report the policy it composed")
			}
			answers["served-store"] = servedReadyEquivalence(t, "http://"+listenForTest(t, cfg))
		})
	}

	if proxied {
		t.Run("cli-proxied", func(t *testing.T) {
			p := newSharedProxiedProject(t, bd, "eq")
			seedReadyEquivalenceWorkspace(t, p.dir, foreignDir, func(args ...string) {
				t.Helper()
				if out, err := bdProxiedRun(t, bd, p.dir, args...); err != nil {
					t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			})
			answers["cli-proxied"] = cliReadyEquivalence(t, func(env []string, args ...string) (string, string, error) {
				return bdProxiedRunBuffersWithEnv(t, bd, p.dir, env, args...)
			})
		})

		t.Run("served-provider", func(t *testing.T) {
			p := newSharedProxiedProject(t, bd, "eq")
			seedReadyEquivalenceWorkspace(t, p.dir, foreignDir, func(args ...string) {
				t.Helper()
				if out, err := bdProxiedRun(t, bd, p.dir, args...); err != nil {
					t.Fatalf("bd %s: %v\n%s", strings.Join(args, " "), err, out)
				}
			})
			sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))
			_, ctxBody, _ := sp.get(t, "/v0/beads/context")
			caps, _ := ctxBody["capabilities"].([]any)
			if !slices.Contains(caps, any(httpapi.CapExternalDependencies)) {
				t.Errorf("bd serve's provider arm does not advertise %s: %v", httpapi.CapExternalDependencies, caps)
			}
			answers["served-provider"] = servedReadyEquivalence(t, "http://"+sp.addr)
			sp.shutdown(t)
		})
	}

	if len(answers) == 0 {
		t.Fatal("no route answered")
	}
	for route, got := range answers {
		if !slices.Equal(got.all, readyEquivalenceWant.all) || got.allTotal != readyEquivalenceWant.allTotal ||
			!slices.Equal(got.page, readyEquivalenceWant.page) || got.pageTotal != readyEquivalenceWant.pageTotal ||
			got.claimed != readyEquivalenceWant.claimed {
			t.Errorf("%s answered\n  %v\nwant\n  %v", route, got, readyEquivalenceWant)
		}
	}
	var summary bytes.Buffer
	for _, route := range []string{"cli-direct", "served-store", "cli-proxied", "served-provider"} {
		if a, ok := answers[route]; ok {
			fmt.Fprintf(&summary, "  %-16s %v\n", route, a)
		}
	}
	t.Logf("equivalence matrix:\n%s", summary.String())
}
