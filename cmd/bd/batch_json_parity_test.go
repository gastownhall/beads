//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerBatchJSONGuardedPublicationParity(t *testing.T) {
	requireSharedProxiedServer(t)
	bd := buildEmbeddedBD(t)
	for _, env := range newCrossModeEnvs(t, bd, "jbc", "jbp") {
		testBatchJSONGuardedPublication(t, env)
	}
}

func TestBatchJSONEmbeddedGuardedPublication(t *testing.T) {
	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "jbc")
	testBatchJSONGuardedPublication(t, crossModeEnv{mode: "classic", bd: bd, dir: dir, env: bdEnv(dir)})
}

func TestProxiedServerBatchJSONManagedGuardedPublication(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)
	project := bdProxiedInit(t, bd, "jbm")
	testBatchJSONGuardedPublication(t, crossModeEnv{mode: "managed-proxy", bd: bd, dir: project.dir, env: bdProxiedEnv(project.dir)})
}

func testBatchJSONGuardedPublication(t *testing.T, env crossModeEnv) {
	t.Helper()
	existing := env.create(t, "JSON batch state")
	prefix := existing
	for index, char := range existing {
		if char == '-' {
			prefix = existing[:index]
			break
		}
	}
	pinned := prefix + "-json-pinned"
	plan := fmt.Sprintf(`{"schema_version":"1","request":{"Items":[{"Kind":"create","Create":{"Issue":{"id":%q,"title":"JSON evidence","issue_type":"task","status":"closed","description":"opaque 9007199254740993 \n evidence","metadata":{"bool":true,"number":17}}}},{"Kind":"update","Update":{"Target":{"ID":%q},"ExpectedVersion":42,"Patch":{"Title":{"Set":true,"Value":"changed"}}}}]}}`, pinned, existing)
	stdout, stderr, code := env.runStdin(t, plan, "batch", "--input-format=json", "--json")
	if code == 0 {
		t.Fatalf("[%s] stale guard accepted: %s %s", env.mode, stdout, stderr)
	}
	_, _, code = env.run(t, "show", pinned, "--json")
	if code == 0 {
		t.Fatalf("[%s] partial create survived failed guard", env.mode)
	}
	plan = fmt.Sprintf(`{"schema_version":"1","request":{"Items":[{"Kind":"create","Create":{"Issue":{"id":%q,"title":"JSON evidence","issue_type":"task","status":"closed","description":"opaque 9007199254740993 \n evidence","metadata":{"bool":true,"number":17}}}}]}}`, pinned)
	stdout, stderr, code = env.runStdin(t, plan, "batch", "--input-format=json", "--json")
	if code != 0 {
		t.Fatalf("[%s] publication: %d %s %s", env.mode, code, stdout, stderr)
	}
	row := env.show(t, pinned)
	if row.Status != types.StatusClosed {
		t.Fatalf("[%s] published open row", env.mode)
	}
	if row.Description != "opaque 9007199254740993 \n evidence" {
		t.Fatalf("[%s] opaque evidence changed: %q", env.mode, row.Description)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(row.Metadata, &metadata); err != nil {
		t.Fatal(err)
	}
	if string(metadata["bool"]) != "true" || string(metadata["number"]) != "17" {
		t.Fatalf("[%s] typed metadata changed: %s", env.mode, row.Metadata)
	}
}
