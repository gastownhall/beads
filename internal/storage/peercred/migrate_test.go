package peercred

import (
	"strings"
	"testing"
)

func TestPlanMigrationRebindsUnambiguousLegacyBlob(t *testing.T) {
	key := testKey(t)
	legacy := sealLegacy(t, "s3cret", key)
	current := []ObservedBinding{{Blob: legacy, Binding: victimBinding}}

	plan := PlanMigration(key, nil, current, nil)
	if len(plan.Rebind) != 1 || plan.Rebind[0].Binding != victimBinding {
		t.Fatalf("Rebind = %+v, want one entry for the victim binding", plan.Rebind)
	}
	if plan.Rebind[0].Password != "s3cret" {
		t.Fatalf("rebind password = %q, want %q", plan.Rebind[0].Password, "s3cret")
	}
	if !sameBytes(plan.Rebind[0].OldBlob, legacy) {
		t.Fatal("rebind OldBlob is not the scanned legacy blob; the CAS update could overwrite a changed row")
	}
	if len(plan.Skipped) != 0 || len(plan.Unopenable) != 0 {
		t.Fatalf("Skipped = %+v, Unopenable = %+v, want none", plan.Skipped, plan.Unopenable)
	}
}

// A blob that appears under two current rows — the replicated-row copy — is
// never rebound, whichever row is scanned first.
func TestPlanMigrationSkipsDuplicatedBlob(t *testing.T) {
	key := testKey(t)
	legacy := sealLegacy(t, "s3cret", key)
	attacker := Binding{Name: "attacker", URL: "https://attacker.example/db", Username: "victimuser"}
	current := []ObservedBinding{
		{Blob: legacy, Binding: victimBinding},
		{Blob: legacy, Binding: attacker},
	}

	plan := PlanMigration(key, nil, current, nil)
	if len(plan.Rebind) != 0 {
		t.Fatalf("Rebind = %+v, want none: the blob is ambiguous", plan.Rebind)
	}
	if len(plan.Skipped) != 2 {
		t.Fatalf("Skipped = %+v, want both rows", plan.Skipped)
	}
}

// A moved blob whose original row was overwritten is still ambiguous: the
// pre-change binding is reachable history, so rebinding would hand the
// password to the row that holds the blob now.
func TestPlanMigrationSkipsMovedBlobSeenInHistory(t *testing.T) {
	key := testKey(t)
	legacy := sealLegacy(t, "s3cret", key)
	oldBinding := Binding{Name: "victim", URL: "https://old.example/db", Username: "victimuser"}
	current := []ObservedBinding{{Blob: legacy, Binding: victimBinding}}
	history := []ObservedBinding{{Blob: legacy, Binding: oldBinding}}

	plan := PlanMigration(key, nil, current, history)
	if len(plan.Rebind) != 0 {
		t.Fatalf("Rebind = %+v, want none: the blob's provenance spans two bindings", plan.Rebind)
	}
	if len(plan.Skipped) != 1 || plan.Skipped[0] != victimBinding {
		t.Fatalf("Skipped = %+v, want the current binding", plan.Skipped)
	}
}

// The same binding seen at many commits (an untouched row's whole history) is
// not ambiguity: the blob still belongs to exactly one identity.
func TestPlanMigrationAllowsBlobRepeatedUnderOneBinding(t *testing.T) {
	key := testKey(t)
	legacy := sealLegacy(t, "s3cret", key)
	current := []ObservedBinding{{Blob: legacy, Binding: victimBinding}}
	history := []ObservedBinding{
		{Blob: legacy, Binding: victimBinding},
		{Blob: legacy, Binding: victimBinding},
		{Blob: nil, Binding: Binding{Name: "other"}},
	}

	plan := PlanMigration(key, nil, current, history)
	if len(plan.Rebind) != 1 {
		t.Fatalf("Rebind = %+v, want one: repetition under one binding is unambiguous", plan.Rebind)
	}
}

// A blob merged in from a peer branch (a second parent) with a different
// binding is ambiguity evidence, even though the row never changed locally.
func TestPlanMigrationSkipsBlobSeenUnderMergedHistory(t *testing.T) {
	key := testKey(t)
	legacy := sealLegacy(t, "s3cret", key)
	current := []ObservedBinding{{Blob: legacy, Binding: victimBinding}}
	mergedPeer := Binding{Name: "victim", URL: "https://peer.example/db", Username: "victimuser"}
	history := []ObservedBinding{{Blob: legacy, Binding: mergedPeer}}

	plan := PlanMigration(key, nil, current, history)
	if len(plan.Rebind) != 0 || len(plan.Skipped) != 1 {
		t.Fatalf("plan = %+v, want the blob skipped", plan)
	}
}

// Rows this machine's key cannot open (another machine's key, corruption) are
// left untouched — not rebound, and not warned about: they fail later use
// with the existing key-mismatch classification.
func TestPlanMigrationLeavesUnopenableBlobs(t *testing.T) {
	key := testKey(t)
	foreign := sealLegacy(t, "other-machines-secret", testKey(t))
	current := []ObservedBinding{
		{Blob: sealLegacy(t, "s3cret", key), Binding: victimBinding},
		{Blob: foreign, Binding: Binding{Name: "fromelsewhere", URL: "https://else.example/db"}},
	}

	plan := PlanMigration(key, nil, current, nil)
	if len(plan.Rebind) != 1 {
		t.Fatalf("Rebind = %+v, want one (the openable row)", plan.Rebind)
	}
	if len(plan.Unopenable) != 1 || plan.Unopenable[0].Name != "fromelsewhere" {
		t.Fatalf("Unopenable = %+v, want the foreign row", plan.Unopenable)
	}
}

// The server store's historical dbPath-derived key is a second key vintage:
// a leftover blob still encrypted under it is eligible once its provenance is
// unambiguous (the pre-v2 conversion never rebinding it would strand it).
func TestPlanMigrationHandlesLegacyDerivedKey(t *testing.T) {
	key := testKey(t)
	derivedKey := testKey(t)
	current := []ObservedBinding{{Blob: sealLegacy(t, "s3cret", derivedKey), Binding: victimBinding}}

	if plan := PlanMigration(key, nil, current, nil); len(plan.Rebind) != 0 {
		t.Fatalf("without the derived key the blob must be unopenable, got %+v", plan)
	}
	plan := PlanMigration(key, derivedKey, current, nil)
	if len(plan.Rebind) != 1 || plan.Rebind[0].Password != "s3cret" {
		t.Fatalf("Rebind = %+v, want the derived-key blob rebound", plan.Rebind)
	}
}

func TestPlanMigrationIgnoresV2AndEmptyBlobs(t *testing.T) {
	key := testKey(t)
	v2, err := Seal("s3cret", key, victimBinding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	legacy := sealLegacy(t, "old", key)
	current := []ObservedBinding{
		{Blob: v2, Binding: victimBinding},
		{Blob: nil, Binding: Binding{Name: "nopassword", URL: "https://x.example/db"}},
		{Blob: legacy, Binding: Binding{Name: "stilllegacy", URL: "https://y.example/db"}},
	}

	plan := PlanMigration(key, nil, current, nil)
	if len(plan.Rebind) != 1 || plan.Rebind[0].Binding.Name != "stilllegacy" {
		t.Fatalf("Rebind = %+v, want only the legacy row", plan.Rebind)
	}
}

func TestRebindWarnings(t *testing.T) {
	plan := MigrationPlan{Skipped: []Binding{victimBinding}}
	lines := plan.RebindWarnings("linux")
	if len(lines) != 1 {
		t.Fatalf("warnings = %d lines, want 1", len(lines))
	}
	if !strings.Contains(lines[0], `bd federation add-peer victim <url>`) {
		t.Fatalf("warning does not name the re-add command: %s", lines[0])
	}
	if !strings.Contains(lines[0], "rotate the peer's password") {
		t.Fatalf("warning does not recommend rotation: %s", lines[0])
	}
	if strings.Contains(lines[0], "s3cret") || strings.Contains(lines[0], "victim.example") {
		t.Fatalf("warning leaks credential-bearing material: %s", lines[0])
	}
	// A peer name with shell metacharacters is safely quoted by
	// remedy.CommandArg, so the rendered command still names the peer.
	weird := MigrationPlan{Skipped: []Binding{{Name: "a b;c", URL: "https://x.example/db"}}}
	if lines := weird.RebindWarnings("linux"); !strings.Contains(lines[0], `bd federation add-peer 'a b;c' <url>`) {
		t.Fatalf("unsafe peer name is not safely quoted: %s", lines[0])
	}
	if lines := (MigrationPlan{}).RebindWarnings("linux"); lines != nil {
		t.Fatalf("empty plan produced %v", lines)
	}
}

func sameBytes(a, b []byte) bool {
	return string(a) == string(b)
}