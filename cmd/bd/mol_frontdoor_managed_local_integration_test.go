//go:build cgo && unix

package main

import (
	"encoding/json"
	"testing"
	"time"
)

// TestMoleculeFrontDoorManagedLocalPositive exercises the production
// fresh-project topology: bd owns a loopback proxy and a local Dolt child.
// The external-TCP parity test covers the shared server lane; this test keeps
// the managed-local path honest without introducing a second storage API.
func TestMoleculeFrontDoorManagedLocalPositive(t *testing.T) {
	requireManagedLocalProxiedEnv(t)
	bd := buildEmbeddedBD(t)
	p := bdManagedLocalInit(t, bd, "mf", 5*time.Minute)
	r := moleculeFrontDoorRunner{name: "managed-local", dir: p.dir, env: bdProxiedEnv}

	buildMoleculeFrontDoorFixture(t, bd, r)
	showRaw := r.mustRun(t, bd, "mol", "show", "mf-root", "--json")
	assertMoleculeFrontDoorRead(t, "show", showRaw)
	if normalizedMoleculeJSON(t, showRaw) == nil {
		t.Fatal("managed-local mol show returned nil")
	}

	// Positive graph write through the managed proxy.
	r.mustRun(t, bd, "create", "Managed bond A", "--type", "epic", "--id", "mf-local-a")
	r.mustRun(t, bd, "create", "Managed bond B", "--type", "epic", "--id", "mf-local-b")
	var bond struct {
		ResultID   string `json:"result_id"`
		ResultType string `json:"result_type"`
	}
	if err := json.Unmarshal([]byte(r.mustRun(t, bd, "mol", "bond", "mf-local-a", "mf-local-b", "--type", "sequential", "--json")), &bond); err != nil {
		t.Fatalf("parse managed-local mol bond: %v", err)
	}
	if bond.ResultID != "mf-local-a" || bond.ResultType != "compound_molecule" {
		t.Fatalf("managed-local mol bond = %+v, want mf-local-a/compound_molecule", bond)
	}

	f := twoStepFormula("managed-local-write", "Managed local step")
	writeMoleculeFormulaFixture(t, r, f)
	var pour struct {
		Created int    `json:"created"`
		Phase   string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(r.mustRun(t, bd, "mol", "pour", f.Formula, "--json")), &pour); err != nil {
		t.Fatalf("parse managed-local mol pour: %v", err)
	}
	if pour.Created != 2 || pour.Phase != "liquid" {
		t.Fatalf("managed-local mol pour = %+v, want created=2 phase=liquid", pour)
	}
	var wisp struct {
		Created int    `json:"created"`
		Phase   string `json:"phase"`
	}
	if err := json.Unmarshal([]byte(r.mustRun(t, bd, "mol", "wisp", "create", f.Formula, "--json")), &wisp); err != nil {
		t.Fatalf("parse managed-local mol wisp: %v", err)
	}
	if wisp.Created != 2 || wisp.Phase != "vapor" {
		t.Fatalf("managed-local mol wisp = %+v, want created=2 phase=vapor", wisp)
	}
}
