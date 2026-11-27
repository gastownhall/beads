package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// renderRemoteMigrateGateJSON returns the exact bytes handleRemoteMigrateGateJSON
// writes to stderr for e, with the JSON envelope and --global pinned off so the
// output depends on e alone.
func renderRemoteMigrateGateJSON(t *testing.T, e *schema.RemoteMigrateGateError) string {
	t.Helper()
	t.Setenv("BD_JSON_ENVELOPE", "")
	restoreGlobal := globalFlag
	globalFlag = false
	t.Cleanup(func() { globalFlag = restoreGlobal })

	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	handleRemoteMigrateGateJSON(e)
	os.Stderr = origStderr
	_ = w.Close()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()
	return buf.String()
}

// bluntServerJSON and bluntEmbeddedJSON are the blunt #4515 refusal's JSON in
// server mode (shared store) and embedded mode (unreadable remote state),
// pinned byte for byte. bd-5hl9dg rewords the proxied-server JSON only; agents
// and Gas City parse these, so the other topologies must not move a byte.
const bluntServerJSON = "{\n  \"error\": \"refusing to auto-apply 8 pending schema migrations to a remote-backed database (v66 -\\u003e v74): migrating clones independently forks the schema (#4259)\",\n  \"hint\": \"Coordination decision required: only ONE clone may migrate a shared remote; a second clone migrating independently forks the schema unrecoverably (#4259). Do NOT auto-run a migration — surface remote_migrate_gate.options to the operator and let them choose.\",\n  \"remote_migrate_gate\": {\n    \"current_version\": 66,\n    \"docs\": \"https://github.com/gastownhall/beads/blob/main/docs/getting-started/upgrading.md#remote-backed-databases-and-multiple-clones\",\n    \"expected\": \"exactly one designated clone migrates and publishes; every other clone adopts the result\",\n    \"fallback_reason\": \"shared-store\",\n    \"human_decision_required\": true,\n    \"latest_version\": 74,\n    \"observed\": \"8 pending schema migration(s) and a configured remote\",\n    \"options\": [\n      {\n        \"commands\": [\n          \"bd migrate --force\",\n          \"bd dolt push\"\n        ],\n        \"id\": \"migrate\",\n        \"risk\": \"if another clone also migrates independently, the schema forks unrecoverably (#4259)\",\n        \"when\": \"you are the single designated migrator (only ONE machine, confirmed with the operator) and no other clone has migrated yet\"\n      },\n      {\n        \"commands\": [\n          \"bd bootstrap\"\n        ],\n        \"id\": \"adopt\",\n        \"risk\": \"re-clones and replaces the local database; push or export unpushed work first or it is lost; on this shared server it also promotes the schema for every co-resident client, and clients still on an older bd will refuse the database until upgraded\",\n        \"when\": \"another machine has already migrated and pushed\"\n      }\n    ],\n    \"pending\": 8,\n    \"severity\": \"blocking\"\n  },\n  \"schema_version\": 1\n}\n"

const bluntEmbeddedJSON = "{\n  \"error\": \"refusing to auto-apply 8 pending schema migrations to a remote-backed database (v66 -\\u003e v74): migrating clones independently forks the schema (#4259)\",\n  \"hint\": \"Coordination decision required: only ONE clone may migrate a shared remote; a second clone migrating independently forks the schema unrecoverably (#4259). Do NOT auto-run a migration — surface remote_migrate_gate.options to the operator and let them choose.\",\n  \"remote_migrate_gate\": {\n    \"current_version\": 66,\n    \"docs\": \"https://github.com/gastownhall/beads/blob/main/docs/getting-started/upgrading.md#remote-backed-databases-and-multiple-clones\",\n    \"expected\": \"exactly one designated clone migrates and publishes; every other clone adopts the result\",\n    \"fallback_reason\": \"unreadable-remote-state\",\n    \"human_decision_required\": true,\n    \"latest_version\": 74,\n    \"observed\": \"8 pending schema migration(s) and a configured remote\",\n    \"options\": [\n      {\n        \"commands\": [\n          \"bd migrate --force\",\n          \"bd dolt push\"\n        ],\n        \"id\": \"migrate\",\n        \"risk\": \"if another clone also migrates independently, the schema forks unrecoverably (#4259)\",\n        \"when\": \"you are the single designated migrator (only ONE machine, confirmed with the operator) and no other clone has migrated yet\"\n      },\n      {\n        \"commands\": [\n          \"bd bootstrap\"\n        ],\n        \"id\": \"adopt\",\n        \"risk\": \"re-clones and replaces the local database; push or export unpushed work first or it is lost\",\n        \"when\": \"another machine has already migrated and pushed\"\n      }\n    ],\n    \"pending\": 8,\n    \"severity\": \"blocking\"\n  },\n  \"schema_version\": 1\n}\n"

// TestBluntRemoteRefusalJSONUnchangedOffProxied passes before and after
// bd-5hl9dg.
func TestBluntRemoteRefusalJSONUnchangedOffProxied(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate *schema.RemoteMigrateGateError
		want string
	}{
		{
			name: "server mode (shared store)",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 74, Pending: 8, Shared: true, FallbackReason: "shared-store"},
			want: bluntServerJSON,
		},
		{
			name: "embedded (unreadable remote state)",
			gate: &schema.RemoteMigrateGateError{CurrentVersion: 66, LatestVersion: 74, Pending: 8, FallbackReason: "unreadable-remote-state"},
			want: bluntEmbeddedJSON,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderRemoteMigrateGateJSON(t, tc.gate); got != tc.want {
				t.Errorf("JSON changed off proxied-server mode.\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestBluntRemoteRefusalJSONProxied covers the JSON half of bd-5hl9dg: agents
// and Gas City act on remote_migrate_gate.options, not on the text, so on a
// proxied-server workspace the options must not hand out `bd migrate --force`,
// `bd dolt push` or `bd bootstrap` (the front door refuses the first two, and
// bootstrap does not adopt the served database from there). The one command
// the workspace can run is the forced two-word verb.
func TestBluntRemoteRefusalJSONProxied(t *testing.T) {
	gate := &schema.RemoteMigrateGateError{
		CurrentVersion: 66, LatestVersion: 74, Pending: 8,
		Shared: true, Proxied: true, FallbackReason: "shared-store",
	}
	out := renderRemoteMigrateGateJSON(t, gate)

	var parsed struct {
		Hint string `json:"hint"`
		Gate struct {
			Options []schema.GateOption `json:"options"`
		} `json:"remote_migrate_gate"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("json.Unmarshal: %v\n%s", err, out)
	}
	if parsed.Hint != gate.AgentDirective() {
		t.Errorf("hint = %q, want the directive", parsed.Hint)
	}

	var all []string
	byID := map[string]schema.GateOption{}
	for _, o := range parsed.Gate.Options {
		all = append(all, o.Commands...)
		byID[o.ID] = o
	}
	for _, c := range all {
		switch c {
		case "bd migrate --force", "bd migrate", "bd dolt push", "bd dolt pull", "bd bootstrap":
			t.Errorf("proxied options hand out %q, which this workspace cannot use: %s", c, out)
		}
	}

	migrate, ok := byID["migrate"]
	if !ok {
		t.Fatalf("proxied options lost the designated-migrator option: %s", out)
	}
	if len(migrate.Commands) != 1 || migrate.Commands[0] != schema.SharedConsentCommandForced {
		t.Errorf("migrate commands = %q, want [%q]", migrate.Commands, schema.SharedConsentCommandForced)
	}
	if got := gate.EscapeHint(); got != schema.SharedConsentCommandForced {
		t.Errorf("EscapeHint = %q, want %q", got, schema.SharedConsentCommandForced)
	}
	for _, want := range []string{"ONE clone of that remote", "library client", "server host"} {
		if !strings.Contains(migrate.When, want) {
			t.Errorf("migrate.when missing %q: %q", want, migrate.When)
		}
	}

	adopt, ok := byID["adopt-on-server-host"]
	if !ok {
		t.Fatalf("proxied options lost the adopt path (want id adopt-on-server-host): %s", out)
	}
	if !strings.Contains(adopt.When, "server host") || !strings.Contains(adopt.When, "do NOT migrate here") {
		t.Errorf("adopt.when does not say where to adopt: %q", adopt.When)
	}
	if _, stale := byID["adopt"]; stale {
		t.Errorf("proxied options still carry the runnable-here adopt option: %s", out)
	}
}
