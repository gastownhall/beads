// wp4_helpers_test.go — shared helpers for the errors / memories / versioning
// contract tests (protocol v0 §E, §M, §V).
//
// Two things the base workspace harness does not do:
//
//  1. Run bd with extra environment. The versioning clauses (V2/V3) are gated
//     on env vars (BD_ALLOW_REMOTE_MIGRATE, BD_IGNORE_SCHEMA_SKEW) and the
//     claim error classes (E4) need two distinct actors, so these tests need to
//     append to the workspace's fixed environment.
//
//  2. Reach the store directly. V2 (pending migration) and V3 (database ahead
//     of the binary) are properties of the *database*, not of any bd command —
//     the only way to stage them is to write the schema-cursor table behind
//     bd's back. That is the "store fixture" the conformance corpus explicitly
//     allows (proposal §14): the assertion still runs against the frozen CLI
//     surface, only the precondition is staged out-of-band.
package protocol

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// command builds a bd invocation in the workspace with extra environment
// appended to the workspace's fixed env.
func (w *workspace) command(extraEnv []string, args ...string) *exec.Cmd {
	cmd := exec.Command(w.bd, args...)
	cmd.Dir = w.dir
	cmd.Env = append(w.env(), extraEnv...)
	return cmd
}

// exitCodeOf unwraps the process exit code from an *exec.ExitError.
func exitCodeOf(t *testing.T, err error, args []string) int {
	t.Helper()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("bd %s: unexpected error type: %v", strings.Join(args, " "), err)
	}
	return exitErr.ExitCode()
}

// runEnv runs bd with extra environment appended, expecting success.
func (w *workspace) runEnv(extraEnv []string, args ...string) string {
	w.t.Helper()
	out, err := w.tryRunEnv(extraEnv, args...)
	if err != nil {
		w.t.Fatalf("bd %s (env %v): %v\n%s", strings.Join(args, " "), extraEnv, err, out)
	}
	return out
}

// tryRunEnv runs bd with extra environment appended, returning output and error.
func (w *workspace) tryRunEnv(extraEnv []string, args ...string) (string, error) {
	w.t.Helper()
	cmd := w.command(extraEnv, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// runEnvExpectError runs bd with extra environment appended, expecting a
// non-zero exit. Returns the combined output and the exit code.
func (w *workspace) runEnvExpectError(extraEnv []string, args ...string) (string, int) {
	w.t.Helper()
	out, err := w.tryRunEnv(extraEnv, args...)
	if err == nil {
		w.t.Fatalf("bd %s (env %v): expected non-zero exit, got success\nOutput: %s",
			strings.Join(args, " "), extraEnv, out)
	}
	code := exitCodeOf(w.t, err, args)
	return out, code
}

// ---------------------------------------------------------------------------
// Structured-error helpers (§E5)
// ---------------------------------------------------------------------------

// jsonErrorFrom extracts the structured error object from a failed --json
// command's output. bd emits JSON errors on stderr for some paths and stdout
// for others (proposal OQ-5), and other lines may share the stream, so the
// object is located rather than assumed to be the whole output.
//
// It returns the first JSON object in the output that carries an "error" key,
// unwrapping the BD_JSON_ENVELOPE form ({schema_version, data:{...}}) so callers
// see one shape either way. The second return reports whether an object was
// found.
func jsonErrorFrom(t *testing.T, out string) (map[string]any, bool) {
	t.Helper()
	for i, r := range out {
		if r != '{' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(out[i:]))
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil {
			continue
		}
		if _, ok := obj["error"]; ok {
			return obj, true
		}
		// BD_JSON_ENVELOPE=1 nests the error under "data".
		if data, ok := obj["data"].(map[string]any); ok {
			if _, ok := data["error"]; ok {
				// Lift schema_version from the envelope so the caller can
				// assert one shape.
				if sv, ok := obj["schema_version"]; ok {
					data["schema_version"] = sv
				}
				return data, true
			}
		}
	}
	return nil, false
}

// requireJSONError asserts the output carries a structured error object with a
// non-empty message and schema_version == 1 (§E5), and returns it.
func requireJSONError(t *testing.T, out, context string) map[string]any {
	t.Helper()
	obj, ok := jsonErrorFrom(t, out)
	if !ok {
		t.Fatalf("%s: no JSON error object in output:\n%s", context, out)
	}
	msg, _ := obj["error"].(string)
	if strings.TrimSpace(msg) == "" {
		t.Errorf("%s: JSON error has empty %q field:\n%s", context, "error", out)
	}
	sv, ok := obj["schema_version"].(float64)
	if !ok || int(sv) != 1 {
		t.Errorf("%s: schema_version = %v, want 1 (§E5):\n%s", context, obj["schema_version"], out)
	}
	return obj
}

// errorMessage returns the "error" field of a structured error object.
func errorMessage(obj map[string]any) string {
	msg, _ := obj["error"].(string)
	return msg
}

// ---------------------------------------------------------------------------
// Store fixture: direct SQL against the workspace's database (§V2/§V3)
// ---------------------------------------------------------------------------
//
// The workspace is backed by an embedded Dolt database under
// .beads/embeddeddolt/<db>, so the fixture drives the `dolt` CLI against that
// directory — the same binary newWorkspace already requires, and no new
// dependency. bd holds no lock between commands, so a write here is visible to
// the next bd invocation.

// storeDatabase returns the name of the database backing the workspace, as the
// workspace itself recorded it at init. Read from bd's metadata rather than
// re-derived from the prefix, so a fixture that guesses wrong fails loudly here
// instead of silently testing nothing.
func (w *workspace) storeDatabase(t *testing.T) string {
	t.Helper()
	path := filepath.Join(w.dir, ".beads", "metadata.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var meta struct {
		Database     string `json:"database"`
		DoltDatabase string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if meta.DoltDatabase != "" {
		return meta.DoltDatabase
	}
	if meta.Database == "" {
		t.Fatalf("%s names no database:\n%s", path, data)
	}
	return meta.Database
}

// storeExec runs one SQL statement against the workspace's database, behind
// bd's back. Values are interpolated (the dolt CLI takes no bind parameters),
// so callers must pass only test-controlled literals.
func (w *workspace) storeExec(t *testing.T, query string) {
	t.Helper()
	dbDir := filepath.Join(w.dir, ".beads", "embeddeddolt", w.storeDatabase(t))
	if _, err := os.Stat(dbDir); err != nil {
		t.Fatalf("workspace is not embedded-Dolt backed (%s): %v", dbDir, err)
	}
	cmd := exec.Command("dolt", "sql", "-q", query)
	cmd.Dir = dbDir
	cmd.Env = append(os.Environ(), "DOLT_ROOT_PATH="+t.TempDir())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dolt sql -q %q: %v\n%s", query, err, out)
	}
}
