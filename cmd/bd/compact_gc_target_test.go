package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/hooks"
	"github.com/steveyegge/beads/internal/storage"
)

type compactGCStoreStub struct {
	storage.DoltStorage
	directory string
	err       error
}

func (s *compactGCStoreStub) ExternalGCPath(context.Context) (string, error) {
	return s.directory, s.err
}

func (s *compactGCStoreStub) ActiveDatabaseSize(ctx context.Context) (int64, error) {
	if sizer, ok := s.DoltStorage.(storage.ActiveDatabaseSizer); ok {
		return sizer.ActiveDatabaseSize(ctx)
	}
	return 0, &storage.ErrUnsupported{Op: "ActiveDatabaseSize", Backend: "fixture"}
}

func TestRunCompactDoltTargetsOnlyAuthorizedActiveDatabase(t *testing.T) {
	clearTelemetryEnv(t)
	t.Setenv("BD_JSON_ENVELOPE", "0")
	toolDir := buildCompactGCFixture(t)
	for _, tc := range []struct {
		name, mode, pathKind     string
		shared, dry, unsupported bool
		decorated                bool
		plain, envelope          bool
		calls                    int
		wantErr                  bool
		wantMessage              string
	}{
		{name: "owned active", calls: 1},
		{name: "shared active despite stale project root", shared: true, calls: 1},
		{name: "owned active through decorators", decorated: true, calls: 1},
		{name: "shared active through decorators", decorated: true, shared: true, calls: 1},
		{name: "older CLI fallback", mode: "fallback", calls: 2},
		{name: "real failure is not retried", mode: "failure", calls: 1, wantErr: true},
		{name: "dry run", dry: true},
		{name: "unsupported with plausible local path", unsupported: true, wantErr: true},
		{name: "unsupported text output", unsupported: true, plain: true, wantErr: true},
		{name: "unsupported enveloped output", unsupported: true, envelope: true, wantErr: true},
		{name: "unsupported dry run", unsupported: true, dry: true},
		{name: "unsupported through decorators", decorated: true, unsupported: true, wantErr: true},
		{name: "unsupported dry run through decorators", decorated: true, unsupported: true, dry: true},
		{name: "size capability is insufficient", pathKind: "size-only", wantErr: true},
		{name: "general locator is insufficient", pathKind: "locator-only", wantErr: true},
		{name: "missing declared path", pathKind: "missing", wantErr: true, wantMessage: "active Dolt database directory is unavailable"},
		{name: "missing declared path in dry run", pathKind: "missing", dry: true, wantErr: true, wantMessage: "active Dolt database directory is unavailable"},
		{name: "file is not a database directory", pathKind: "file", wantErr: true, wantMessage: "is not a directory"},
		{name: "empty declared path", pathKind: "empty", wantErr: true, wantMessage: "requires an absolute active database directory"},
		{name: "relative declared path", pathKind: "relative", wantErr: true, wantMessage: "requires an absolute active database directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldStore, oldDry, oldJSON := store, compactDryRun, jsonOutput
			beads.ResetCaches()
			t.Cleanup(func() { store, compactDryRun, jsonOutput = oldStore, oldDry, oldJSON; beads.ResetCaches() })
			project := t.TempDir()
			beadsDir := filepath.Join(project, ".beads")
			stale := filepath.Join(beadsDir, "dolt")
			root := stale
			if tc.shared {
				root = filepath.Join(t.TempDir(), "central", "dolt")
			}
			active := filepath.Join(root, "active")
			sibling := filepath.Join(root, "sibling")
			for _, dir := range []string{stale, active, sibling} {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, "keep"), []byte("unchanged"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("BEADS_DIR", beadsDir)
			t.Setenv("PATH", toolDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			logPath := filepath.Join(t.TempDir(), "calls.jsonl")
			t.Setenv("BEADS_COMPACT_GC_FIXTURE_LOG", logPath)
			t.Setenv("BEADS_COMPACT_GC_FIXTURE_MODE", tc.mode)
			candidate := &compactGCStoreStub{DoltStorage: &gcSizeStoreStub{size: 42}, directory: active}
			if tc.unsupported {
				candidate.err = &storage.ErrUnsupported{Op: "ExternalGCPath", Backend: "external"}
			}
			store = candidate
			switch tc.pathKind {
			case "size-only":
				store = &gcSizeStoreStub{size: 42}
			case "locator-only":
				store = &gcLocatorOnlyStoreStub{path: active}
			case "missing":
				candidate.directory = filepath.Join(root, "missing")
			case "file":
				candidate.directory = filepath.Join(active, "keep")
			case "empty":
				candidate.directory = ""
			case "relative":
				candidate.directory = "relative-database"
			}
			if tc.decorated {
				t.Setenv("BD_OTEL_STDOUT", "true")
				store = wireStorageDecorators(store, hooks.NewRunner(filepath.Join(project, "missing-hooks")), false)
				if storage.UnwrapStore(store) != candidate {
					t.Fatal("production decorator chain lost the active store")
				}
			}
			compactDryRun, jsonOutput = tc.dry, !tc.plain
			if tc.envelope {
				t.Setenv("BD_JSON_ENVELOPE", "1")
			}
			var runErr error
			var output string
			if tc.wantErr {
				output = captureStderr(t, func() { runErr = runCompactDolt(t.Context()) })
			} else {
				output = captureStdout(t, func() error { runErr = runCompactDolt(t.Context()); return nil })
			}
			if (runErr != nil) != tc.wantErr {
				t.Fatalf("runCompactDolt error = %v, wantErr %v", runErr, tc.wantErr)
			}
			if tc.wantErr {
				if code, ok := exitCodeFromError(runErr); !ok || code != 1 {
					t.Fatalf("refusal exit code = %d, recognized %v; error %v", code, ok, runErr)
				}
			}
			if tc.wantErr && tc.mode != "failure" {
				message, hint := output, output
				if tc.plain {
					if !strings.HasPrefix(output, "Error: ") || !strings.Contains(output, "\nHint: ") {
						t.Fatalf("missing text error/hint: %q", output)
					}
				} else {
					var result map[string]interface{}
					if err := json.Unmarshal([]byte(output), &result); err != nil {
						t.Fatalf("error stderr is not JSON: %v: %s", err, output)
					}
					if result["schema_version"] != float64(JSONSchemaVersion) {
						t.Fatalf("missing error schema version: %v", result)
					}
					if tc.envelope {
						var ok bool
						result, ok = result["data"].(map[string]interface{})
						if !ok {
							t.Fatalf("missing error envelope data: %s", output)
						}
					}
					message, _ = result["error"].(string)
					hint, _ = result["hint"].(string)
				}
				wantMessage := tc.wantMessage
				if wantMessage == "" {
					wantMessage = "cannot select a local database for external Dolt garbage collection"
				}
				if !strings.Contains(message, wantMessage) || !strings.Contains(hint, "bd doctor") {
					t.Fatalf("refusal lost its cause or actionable hint: %s", output)
				}
				if tc.unsupported {
					for _, condition := range []string{"BEADS_DOLT_SERVER_PORT", "BEADS_DOLT_PORT", "auto-start", "external", "gateway", "proxied", "socket", "TLS", "server administrator"} {
						if !strings.Contains(hint, condition) {
							t.Errorf("unsupported hint omits %q: %s", condition, hint)
						}
					}
				}
			}
			data, err := os.ReadFile(logPath)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			var calls []struct {
				Dir  string
				Args []string
			}
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				if line == "" {
					continue
				}
				var call struct {
					Dir  string
					Args []string
				}
				if err := json.Unmarshal([]byte(line), &call); err != nil {
					t.Fatal(err)
				}
				calls = append(calls, call)
			}
			if len(calls) != tc.calls {
				t.Fatalf("external GC calls = %v, want %d", calls, tc.calls)
			}
			for i, call := range calls {
				wantArgs := []string{"gc", "--archive-level", "0"}
				if i == 1 {
					wantArgs = []string{"gc"}
				}
				gotInfo, err := os.Stat(call.Dir)
				if err != nil {
					t.Fatal(err)
				}
				wantInfo, err := os.Stat(active)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(gotInfo, wantInfo) || !reflect.DeepEqual(call.Args, wantArgs) {
					t.Fatalf("GC call = %+v, want directory %q, args %v", call, active, wantArgs)
				}
			}
			for _, dir := range []string{stale, active, sibling} {
				data, err := os.ReadFile(filepath.Join(dir, "keep"))
				if err != nil || string(data) != "unchanged" {
					t.Fatalf("changed sentinel in %q: %q, %v", dir, data, err)
				}
				_, err = os.Stat(filepath.Join(dir, "gc-ran"))
				wantMarker := dir == active && tc.calls > 0 && !tc.wantErr
				if (err == nil) != wantMarker {
					t.Fatalf("GC marker in %q: %v, want marker %v", dir, err, wantMarker)
				}
			}
			if !tc.wantErr {
				var result map[string]interface{}
				if err := json.Unmarshal([]byte(output), &result); err != nil {
					t.Fatalf("JSON: %v: %s", err, output)
				}
				if tc.unsupported {
					_, hasDoltPath := result["dolt_path"]
					if result["available"] != false || hasDoltPath {
						t.Fatalf("unsupported target was guessed: %v", result)
					}
				} else if result["dolt_path"] != active || result["size_before"] != float64(42) {
					t.Fatalf("reported scope = %v, want active %q and size42", result, active)
				}
			}
		})
	}
}

func buildCompactGCFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	const source = `package main
import ("encoding/json"; "fmt"; "os")
func main() {
 dir, err := os.Getwd(); if err != nil { panic(err) }
 log, err := os.OpenFile(os.Getenv("BEADS_COMPACT_GC_FIXTURE_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); if err != nil { panic(err) }
 if err := json.NewEncoder(log).Encode(struct { Dir string; Args []string }{dir, os.Args[1:]}); err != nil { panic(err) }; if err := log.Close(); err != nil { panic(err) }
 mode := os.Getenv("BEADS_COMPACT_GC_FIXTURE_MODE")
 if mode == "fallback" && len(os.Args) == 4 { fmt.Fprintln(os.Stderr, "unknown flag: --archive-level"); os.Exit(23) }
 if mode == "failure" { fmt.Fprintln(os.Stderr, "genuine GC failure"); os.Exit(23) }
 if err := os.WriteFile("gc-ran", []byte("collected"), 0600); err != nil { panic(err) }
}
`
	path := filepath.Join(dir, "fixture.go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	name := "dolt"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), path)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build owned GC fixture: %v\n%s", err, output)
	}
	return dir
}
