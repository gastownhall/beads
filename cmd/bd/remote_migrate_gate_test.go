package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/schema"
)

// TestHandleRemoteMigrateGateJSON_Shape verifies the JSON written to stderr has
// the expected shape: error one-liner, a non-runnable directive hint (NOT the
// escape-hatch command), and a remote_migrate_gate subobject carrying the
// current/latest/pending versions plus the agent-decision fields and options.
func TestHandleRemoteMigrateGateJSON_Shape(t *testing.T) {
	gate := &schema.RemoteMigrateGateError{CurrentVersion: 48, LatestVersion: 50, Pending: 2}

	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()

	handleRemoteMigrateGateJSON(gate)

	_ = w.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, buf.String())
	}

	if got, ok := parsed["error"].(string); !ok || got != gate.Error() {
		t.Errorf("error = %v, want %q", parsed["error"], gate.Error())
	}
	// hint is the non-runnable directive, and must NOT be the runnable escape
	// command — that swap is the whole point of this change (the agent footgun).
	if got, ok := parsed["hint"].(string); !ok || got != gate.AgentDirective() {
		t.Errorf("hint = %v, want directive %q", parsed["hint"], gate.AgentDirective())
	}
	if parsed["hint"] == gate.EscapeHint() {
		t.Errorf("hint must not be the runnable escape command %q (agent footgun)", gate.EscapeHint())
	}

	obj, ok := parsed["remote_migrate_gate"].(map[string]interface{})
	if !ok {
		t.Fatalf("remote_migrate_gate key missing or wrong type: %T", parsed["remote_migrate_gate"])
	}
	if got, ok := obj["current_version"].(float64); !ok || int(got) != 48 {
		t.Errorf("current_version = %v, want 48", obj["current_version"])
	}
	if got, ok := obj["latest_version"].(float64); !ok || int(got) != 50 {
		t.Errorf("latest_version = %v, want 50", obj["latest_version"])
	}
	if got, ok := obj["pending"].(float64); !ok || int(got) != 2 {
		t.Errorf("pending = %v, want 2", obj["pending"])
	}
	if got, ok := obj["human_decision_required"].(bool); !ok || !got {
		t.Errorf("human_decision_required = %v, want true", obj["human_decision_required"])
	}
	if got, ok := obj["severity"].(string); !ok || got != "blocking" {
		t.Errorf("severity = %v, want \"blocking\"", obj["severity"])
	}

	// options must carry both paths, and the runnable migrate command must live
	// ONLY inside the conditional migrate option, never surfaced unconditionally.
	rawOpts, ok := obj["options"].([]interface{})
	if !ok || len(rawOpts) != 2 {
		t.Fatalf("options = %v, want 2 entries", obj["options"])
	}
	ids := map[string]bool{}
	migrateCmdFound := false
	for _, ro := range rawOpts {
		o, ok := ro.(map[string]interface{})
		if !ok {
			t.Fatalf("option wrong type: %T", ro)
		}
		id, _ := o["id"].(string)
		ids[id] = true
		if o["when"] == nil || o["risk"] == nil {
			t.Errorf("option %q missing when/risk: %v", id, o)
		}
		cmds, _ := o["commands"].([]interface{})
		for _, c := range cmds {
			if c == gate.EscapeHint() {
				migrateCmdFound = true
				if id != "migrate" {
					t.Errorf("escape command appears under option %q, want only \"migrate\"", id)
				}
			}
		}
	}
	if !ids["migrate"] || !ids["adopt"] {
		t.Errorf("options ids = %v, want migrate+adopt", ids)
	}
	if !migrateCmdFound {
		t.Errorf("migrate option must contain the escape command %q", gate.EscapeHint())
	}
}

// TestHandleRemoteMigrateGateJSON_FallbackReason verifies the blunt-block JSON
// carries the machine-readable fallback_reason field (gastownhall/beads#4551
// follow-up), and that the smart-tailored adopt/fork-skew decisions never gain
// it — those already explain themselves via "decision".
func TestHandleRemoteMigrateGateJSON_FallbackReason(t *testing.T) {
	captureJSON := func(gate *schema.RemoteMigrateGateError) map[string]interface{} {
		origStderr := os.Stderr
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatal(pipeErr)
		}
		os.Stderr = w
		defer func() { os.Stderr = origStderr }()
		handleRemoteMigrateGateJSON(gate)
		_ = w.Close()

		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err != nil {
			t.Fatal(err)
		}
		_ = r.Close()

		var parsed map[string]interface{}
		if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
			t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, buf.String())
		}
		obj, ok := parsed["remote_migrate_gate"].(map[string]interface{})
		if !ok {
			t.Fatalf("remote_migrate_gate key missing or wrong type: %T", parsed["remote_migrate_gate"])
		}
		return obj
	}

	t.Run("blunt block carries fallback_reason", func(t *testing.T) {
		gate := &schema.RemoteMigrateGateError{
			CurrentVersion: 48, LatestVersion: 50, Pending: 2,
			FallbackReason: "below-convergence-floor",
		}
		obj := captureJSON(gate)
		if got, ok := obj["fallback_reason"].(string); !ok || got != "below-convergence-floor" {
			t.Errorf("fallback_reason = %v, want %q", obj["fallback_reason"], "below-convergence-floor")
		}
		if _, ok := obj["decision"]; ok {
			t.Errorf("blunt block must not carry a decision key, got %v", obj["decision"])
		}
	})

	t.Run("adopt decision never carries fallback_reason", func(t *testing.T) {
		gate := &schema.RemoteMigrateGateError{
			CurrentVersion: 48, LatestVersion: 50, Pending: 2,
			Decision: "adopt",
		}
		obj := captureJSON(gate)
		if _, ok := obj["fallback_reason"]; ok {
			t.Errorf("adopt decision must not carry fallback_reason, got %v", obj["fallback_reason"])
		}
	})
}

// TestHandleRemoteMigrateGateJSON_SharedNoRemote covers the shared-store
// refusal's agent-facing block (gastownhall/beads#5920). It is deliberately
// NOT the migrate-or-adopt shape: with no remote there is nothing to adopt
// from, so the only option is "migrate once the fleet is upgraded", and the
// runnable command stays inside that conditional option.
func TestHandleRemoteMigrateGateJSON_SharedNoRemote(t *testing.T) {
	gate := &schema.RemoteMigrateGateError{
		CurrentVersion: 53, LatestVersion: 66, Pending: 13,
		Decision: "shared-no-remote",
	}

	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()
	handleRemoteMigrateGateJSON(gate)
	_ = w.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, buf.String())
	}
	if parsed["hint"] == gate.EscapeHint() {
		t.Errorf("hint must not be the runnable escape command %q (agent footgun)", gate.EscapeHint())
	}

	obj, ok := parsed["remote_migrate_gate"].(map[string]interface{})
	if !ok {
		t.Fatalf("remote_migrate_gate key missing or wrong type: %T", parsed["remote_migrate_gate"])
	}
	if got, _ := obj["decision"].(string); got != "shared-no-remote" {
		t.Errorf("decision = %v, want \"shared-no-remote\"", obj["decision"])
	}
	if got, _ := obj["observed"].(string); !strings.Contains(got, "shared server database") {
		t.Errorf("observed = %q, want the shared-server framing", got)
	}
	if got, _ := obj["expected"].(string); !strings.Contains(got, "bd migrate schema") {
		t.Errorf("expected = %q, want the consent verb", got)
	}
	if _, ok := obj["fallback_reason"]; ok {
		t.Errorf("a tailored decision must not carry fallback_reason, got %v", obj["fallback_reason"])
	}

	rawOpts, ok := obj["options"].([]interface{})
	if !ok || len(rawOpts) != 1 {
		t.Fatalf("options = %v, want exactly one (no adopt path without a remote)", obj["options"])
	}
	o, _ := rawOpts[0].(map[string]interface{})
	if id, _ := o["id"].(string); id != "migrate-shared" {
		t.Errorf("option id = %v, want \"migrate-shared\"", o["id"])
	}
	if o["when"] == nil || o["risk"] == nil {
		t.Errorf("migrate-shared option missing when/risk: %v", o)
	}
	cmds, _ := o["commands"].([]interface{})
	if len(cmds) != 1 || cmds[0] != "bd migrate schema" {
		t.Errorf("commands = %v, want [\"bd migrate schema\"]", cmds)
	}
}

// TestHandleRemoteMigrateGateJSON_SharedNoRemoteGlobal covers the shared-store
// refusal's agent-facing block when the refused open targeted the global
// database (--global). The consent verb is target-scoped: the project-scoped
// `bd migrate schema` would consent the WRONG database and leave the refusal in
// place, so both the top-level "expected" directive and the runnable
// migrate-shared option command must name `bd migrate schema --global`
// (gastownhall/beads#5920), at parity with the human/text path
// (TestPrintGlobalDatabaseConsentHint). The --json notice self-suppresses, so
// this block is the sole channel for a --json consumer.
func TestHandleRemoteMigrateGateJSON_SharedNoRemoteGlobal(t *testing.T) {
	origGlobal := globalFlag
	globalFlag = true
	defer func() { globalFlag = origGlobal }()

	gate := &schema.RemoteMigrateGateError{
		CurrentVersion: 53, LatestVersion: 66, Pending: 13,
		Decision: "shared-no-remote",
	}

	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatal(pipeErr)
	}
	os.Stderr = w
	defer func() { os.Stderr = origStderr }()
	handleRemoteMigrateGateJSON(gate)
	_ = w.Close()

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	_ = r.Close()

	var parsed map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("json.Unmarshal stderr: %v\nstderr was: %s", err, buf.String())
	}

	obj, ok := parsed["remote_migrate_gate"].(map[string]interface{})
	if !ok {
		t.Fatalf("remote_migrate_gate key missing or wrong type: %T", parsed["remote_migrate_gate"])
	}
	if got, _ := obj["decision"].(string); got != "shared-no-remote" {
		t.Errorf("decision = %v, want \"shared-no-remote\"", obj["decision"])
	}
	// Under --global the top-level directive must name the --global consent verb.
	if got, _ := obj["expected"].(string); !strings.Contains(got, schema.SharedConsentCommandGlobal) {
		t.Errorf("expected = %q, want the --global consent verb %q", got, schema.SharedConsentCommandGlobal)
	}

	rawOpts, ok := obj["options"].([]interface{})
	if !ok || len(rawOpts) != 1 {
		t.Fatalf("options = %v, want exactly one (no adopt path without a remote)", obj["options"])
	}
	o, _ := rawOpts[0].(map[string]interface{})
	if id, _ := o["id"].(string); id != "migrate-shared" {
		t.Errorf("option id = %v, want \"migrate-shared\"", o["id"])
	}
	// The runnable command inside the option must be the --global verb too: an
	// agent extracts the command from here, not from the prose directive.
	cmds, _ := o["commands"].([]interface{})
	if len(cmds) != 1 || cmds[0] != schema.SharedConsentCommandGlobal {
		t.Errorf("commands = %v, want [%q]", cmds, schema.SharedConsentCommandGlobal)
	}
}

// TestRenderTypedOpenError pins the rendering every store-open path depends
// on: the Dolt store open, the proxied unit-of-work provider open, and
// `bd serve`'s startup all call it, and all three must produce the whole
// actionable block rather than a one-line `%v`. A gate refusal at `bd serve`
// startup is the case with no operator watching, so the daemon's log is the
// only place the remediation can appear.
func TestRenderTypedOpenError(t *testing.T) {
	capture := func(fn func() bool) (string, bool) {
		origStderr := os.Stderr
		r, w, pipeErr := os.Pipe()
		if pipeErr != nil {
			t.Fatal(pipeErr)
		}
		os.Stderr = w
		handled := fn()
		_ = w.Close()
		os.Stderr = origStderr

		var buf bytes.Buffer
		if _, err := io.Copy(&buf, r); err != nil {
			t.Fatal(err)
		}
		_ = r.Close()
		return buf.String(), handled
	}

	t.Run("shared-store gate refusal renders the full block", func(t *testing.T) {
		gate := &schema.RemoteMigrateGateError{
			CurrentVersion: 53, LatestVersion: 66, Pending: 13,
			Decision: "shared-no-remote",
		}
		out, handled := capture(func() bool {
			return renderTypedOpenError(fmt.Errorf("uow: init schema: %w", gate))
		})
		if !handled {
			t.Fatal("renderTypedOpenError = false, want true for a wrapped gate refusal")
		}
		if out != gate.UserMessage() {
			t.Errorf("rendered output is not the full UserMessage:\n%s", out)
		}
		for _, want := range []string{"co-resident", "bd migrate schema", "Read commands keep working"} {
			if !strings.Contains(out, want) {
				t.Errorf("output missing %q:\n%s", want, out)
			}
		}
	})

	t.Run("an ordinary error is left to the caller", func(t *testing.T) {
		out, handled := capture(func() bool {
			return renderTypedOpenError(errors.New("connection refused"))
		})
		if handled {
			t.Error("renderTypedOpenError = true, want false for an untyped error")
		}
		if out != "" {
			t.Errorf("nothing should be printed for an untyped error, got:\n%s", out)
		}
	})
}
