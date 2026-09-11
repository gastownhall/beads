package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/steveyegge/beads/internal/ownershiphandoff"
)

func TestOwnershipHandoffCommandIsExplicitAndSkipsStore(t *testing.T) {
	if ownershipHandoffCmd == nil {
		t.Fatal("ownership-handoff command is not registered")
	}
	if ownershipHandoffCmd.Parent() != migrateCmd {
		t.Fatalf("parent = %v, want migrate", ownershipHandoffCmd.Parent())
	}
	if ownershipHandoffCmd.Annotations[skipStoreAnnotation] != "1" {
		t.Fatal("ownership-handoff must skip ordinary store/provider startup")
	}
	for _, command := range rootCmd.Commands() {
		if command == ownershipHandoffCmd {
			t.Fatal("ownership-handoff must not be an ordinary top-level bd command")
		}
	}
	for _, name := range []string{"city", "root", "database", "workspace", "host", "port", "socket", "journal", "dry-run", "resume", "retry"} {
		if ownershipHandoffCmd.Flags().Lookup(name) == nil {
			t.Errorf("missing --%s flag", name)
		}
	}
}

func TestOwnershipHandoffCommandMissingIdentityUsesTypedJSON(t *testing.T) {
	setOwnershipHandoffFlag(t, "root", "")
	setOwnershipHandoffFlag(t, "city", "")
	setOwnershipHandoffFlag(t, "database", "")
	setOwnershipHandoffFlag(t, "workspace", "")
	setOwnershipHandoffFlag(t, "journal", "")
	setOwnershipHandoffFlag(t, "socket", "")
	setOwnershipHandoffFlag(t, "host", "127.0.0.1")
	setOwnershipHandoffFlag(t, "port", "3307")
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })
	var runErr error
	out := captureStdout(t, func() error {
		runErr = runOwnershipHandoffCommand(ownershipHandoffCmd, nil)
		return nil
	})
	if runErr == nil {
		t.Fatal("missing identity unexpectedly succeeded")
	}
	var got ownershipHandoffOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode strict handoff JSON %q: %v", out, err)
	}
	if got.ErrorCode != "invalid_request" || got.Phase != ownershiphandoff.PhasePrepared {
		t.Fatalf("output=%+v, want typed invalid_request", got)
	}
}

func TestOwnershipHandoffRunRejectsInvalidRequestBeforeProvider(t *testing.T) {
	root := canonicalTempDir(t)
	setOwnershipHandoffFlag(t, "city", root)
	called := 0
	provider := ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		called++
		return ownershiphandoff.Hooks{}, nil
	})
	request := ownershiphandoff.Request{
		CityRoot:  root,
		Root:      root,
		Database:  "beads",
		Workspace: "workspace",
		Endpoint:  ownershiphandoff.Endpoint{Host: "203.0.113.7", Port: 3307},
		Owner:     ownershiphandoff.OwnerLegacyGC,
	}
	result, err := ownershiphandoff.Run(context.Background(), request, filepath.Join(root, "handoff.json"), provider, false)
	if err == nil || result.ErrorCode != "invalid_request" {
		t.Fatalf("result=%+v err=%v, want invalid_request", result, err)
	}
	if called != 0 {
		t.Fatalf("provider opened %d times for invalid request", called)
	}
}

// TestOwnershipHandoffDefaultProviderFailsClosed pins the shipped provider
// seam.  With no explicit GC_BIN adapter configured, a valid request must be
// refused before any provider hooks can run; in particular, the provider must
// not silently return empty hooks and let Run create a handoff journal.
func TestOwnershipHandoffDefaultProviderFailsClosed(t *testing.T) {
	city := canonicalTempDir(t)
	scope := filepath.Join(city, "scope")
	if err := os.Mkdir(scope, 0700); err != nil {
		t.Fatal(err)
	}
	before := handoffDirectorySnapshot(t, city)
	t.Setenv("GC_BIN", "")
	request := ownershiphandoff.Request{
		CityRoot:  city,
		Root:      scope,
		Database:  "beads",
		Workspace: "workspace",
		Endpoint:  ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		Owner:     ownershiphandoff.OwnerLegacyGC,
	}
	_, err := ownershipHandoffProvider.OwnershipHandoffHooks(context.Background(), request)
	if err == nil {
		t.Fatal("default ownership handoff provider unexpectedly returned hooks without GC_BIN")
	}
	var coded interface{ HandoffErrorCode() string }
	if !errors.As(err, &coded) || coded.HandoffErrorCode() != "provider_unavailable" {
		t.Fatalf("provider error = %v, want typed provider_unavailable", err)
	}
	if after := handoffDirectorySnapshot(t, city); !reflect.DeepEqual(after, before) {
		t.Fatalf("default provider mutated its root: before=%v after=%v", before, after)
	}
}

// TestOwnershipHandoffLegacyGuardBypass pins the explicit command-only
// escape hatch.  A metadata-less historical SQLite workspace is refused for
// an ordinary no-store command, while the ownership-handoff front door is
// admitted so it can perform its own canonical identity validation.
func TestOwnershipHandoffLegacyGuardBypass(t *testing.T) {
	repo := canonicalTempDir(t)
	beadsDir := filepath.Join(repo, ".beads")
	if err := os.Mkdir(beadsDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beadsDir, "beads.db"), []byte("SQLite format 3\x00historical"), 0600); err != nil {
		t.Fatal(err)
	}

	if err := guardLegacyNoStoreCommand(ownershipHandoffCmd, beadsDir); err != nil {
		t.Fatalf("ownership-handoff legacy guard = %v, want explicit bypass", err)
	}

	control := &cobra.Command{Use: "ordinary-historical", RunE: func(*cobra.Command, []string) error { return nil }}
	migrateCmd.AddCommand(control)
	t.Cleanup(func() { migrateCmd.RemoveCommand(control) })
	if err := guardLegacyNoStoreCommand(control, beadsDir); !isLegacyUpgradeRefusal(err) {
		t.Fatalf("ordinary command legacy guard = %v, want historical SQLite refusal", err)
	}
}

func TestOwnershipHandoffDryRunDoesNotOpenProvider(t *testing.T) {
	root := canonicalTempDir(t)
	called := 0
	provider := ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		called++
		return ownershiphandoff.Hooks{}, errors.New("must not open in dry-run")
	})
	request := ownershiphandoff.Request{
		CityRoot:  root,
		Root:      root,
		Database:  "beads",
		Workspace: "workspace",
		Endpoint:  ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		Owner:     ownershiphandoff.OwnerLegacyGC,
	}
	result, err := ownershiphandoff.Run(context.Background(), request, filepath.Join(root, "handoff.json"), provider, true)
	if err != nil || result.Phase != ownershiphandoff.PhasePrepared || result.Mutates {
		t.Fatalf("result=%+v err=%v, want prepared non-mutating dry-run", result, err)
	}
	if called != 0 {
		t.Fatalf("provider opened %d times for dry-run", called)
	}
}

func TestOwnershipHandoffResolvesProviderUnderJournalLock(t *testing.T) {
	root := canonicalTempDir(t)
	journal := filepath.Join(root, "handoff.json")
	request := ownershiphandoff.Request{
		CityRoot:  root,
		Root:      root,
		Database:  "beads",
		Workspace: "workspace",
		Endpoint:  ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		Owner:     ownershiphandoff.OwnerLegacyGC,
	}
	provider := ownershiphandoff.ProviderFunc(func(ctx context.Context, got ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		// A nested attempt must see the live lock rather than opening a second
		// provider while the outer call is resolving hooks.
		nested, err := ownershiphandoff.Run(ctx, got, journal, nil, false)
		if err == nil || nested.ErrorCode != "concurrent_handoff" {
			t.Fatalf("nested run result=%+v err=%v, want concurrent_handoff", nested, err)
		}
		return ownershiphandoff.Hooks{
			Snapshot: func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Snapshot, error) {
				return ownershiphandoff.Snapshot{}, nil
			},
			Configure:  func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			StopLegacy: func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			Verify:     func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
			Commit:     func(context.Context, ownershiphandoff.Request, ownershiphandoff.Snapshot) error { return nil },
		}, nil
	})
	result, err := ownershiphandoff.Run(context.Background(), request, journal, provider, false)
	if err != nil || result.Phase != ownershiphandoff.PhaseCommitted {
		t.Fatalf("result=%+v err=%v, want committed", result, err)
	}
}

func TestOwnershipHandoffProviderErrorPreservesJournalState(t *testing.T) {
	root := canonicalTempDir(t)
	journal := filepath.Join(root, "handoff.json")
	request := ownershiphandoff.Request{
		CityRoot: root,
		Root:     root, Database: "beads", Workspace: "workspace",
		Endpoint: ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		Owner:    ownershiphandoff.OwnerLegacyGC,
	}
	seed := ownershiphandoff.Journal{Request: request, Snapshot: ownershiphandoff.Snapshot{Sentinel: "s"}, SnapshotCaptured: true,
		Phase: ownershiphandoff.PhaseOldOwnerStopped, Owner: ownershiphandoff.OwnerLegacyGC}
	b, err := json.Marshal(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, append(b, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	provider := ownershiphandoff.ProviderFunc(func(context.Context, ownershiphandoff.Request) (ownershiphandoff.Hooks, error) {
		return ownershiphandoff.Hooks{}, errors.New("provider unavailable")
	})
	result, err := ownershiphandoff.Run(context.Background(), request, journal, provider, false)
	if err == nil || result.Phase != ownershiphandoff.PhaseOldOwnerStopped || result.Owner != ownershiphandoff.OwnerLegacyGC || !result.Mutates || result.ErrorCode != "provider_unavailable" {
		t.Fatalf("result=%+v err=%v, want preserved old_owner_stopped mutation state", result, err)
	}
}

func TestOwnershipHandoffJSONShapeIsStable(t *testing.T) {
	result := ownershipHandoffOutput{
		Phase:   ownershiphandoff.PhasePrepared,
		Owner:   ownershiphandoff.OwnerLegacyGC,
		Mutates: false,
		Identity: ownershipHandoffIdentity{
			Root:      "/srv/beads",
			Database:  "beads",
			Workspace: "workspace",
			Endpoint:  ownershiphandoff.Endpoint{Host: "127.0.0.1", Port: 3307},
		},
		ErrorCode: "",
	}
	b, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"phase", "owner", "mutates", "identity", "error_code"} {
		if _, ok := fields[field]; !ok {
			t.Errorf("JSON missing required field %q: %s", field, b)
		}
	}
	if _, ok := fields["schema_version"]; ok {
		t.Errorf("handoff JSON must be strict result JSON, got schema wrapper: %s", b)
	}
}

func TestOwnershipHandoffCommandDryRunJSONFrontDoor(t *testing.T) {
	root := canonicalTempDir(t)
	setOwnershipHandoffFlag(t, "city", root)
	setOwnershipHandoffFlag(t, "root", root)
	setOwnershipHandoffFlag(t, "database", "beads")
	setOwnershipHandoffFlag(t, "workspace", "workspace")
	setOwnershipHandoffFlag(t, "host", "127.0.0.1")
	setOwnershipHandoffFlag(t, "port", "3307")
	setOwnershipHandoffFlag(t, "socket", "")
	setOwnershipHandoffFlag(t, "journal", filepath.Join(root, ownershipHandoffJournalName))
	setOwnershipHandoffFlag(t, "dry-run", "true")
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	out := captureStdout(t, func() error {
		return runOwnershipHandoffCommand(ownershipHandoffCmd, nil)
	})
	var got ownershipHandoffOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode strict handoff JSON %q: %v", out, err)
	}
	if got.Phase != ownershiphandoff.PhasePrepared || got.Owner != ownershiphandoff.OwnerLegacyGC || got.ErrorCode != "" {
		t.Fatalf("output=%+v, want prepared legacy-gc success", got)
	}
}

func TestOwnershipHandoffCommandSocketEndpoint(t *testing.T) {
	for _, explicit := range []string{"", "host", "port"} {
		t.Run("explicit_"+explicit, func(t *testing.T) {
			root := canonicalTempDir(t)
			setOwnershipHandoffFlag(t, "city", root)
			setOwnershipHandoffFlag(t, "root", root)
			setOwnershipHandoffFlag(t, "database", "beads")
			setOwnershipHandoffFlag(t, "workspace", "workspace")
			setOwnershipHandoffFlag(t, "journal", "")
			setOwnershipHandoffFlag(t, "socket", filepath.Join(root, "beads.sock"))
			setOwnershipHandoffFlag(t, "dry-run", "true")
			if explicit == "host" {
				setOwnershipHandoffFlag(t, "host", "127.0.0.1")
			}
			if explicit == "port" {
				setOwnershipHandoffFlag(t, "port", "3307")
			}
			oldJSON := jsonOutput
			jsonOutput = true
			t.Cleanup(func() { jsonOutput = oldJSON })
			var runErr error
			out := captureStdout(t, func() error {
				runErr = runOwnershipHandoffCommand(ownershipHandoffCmd, nil)
				return nil
			})
			var got ownershipHandoffOutput
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatalf("decode handoff JSON %q: %v", out, err)
			}
			if explicit == "" {
				if runErr != nil || got.ErrorCode != "" || got.Identity.Endpoint.Host != "" || got.Identity.Endpoint.Port != 0 {
					t.Fatalf("socket-only result=%+v err=%v, want socket without implicit TCP defaults", got, runErr)
				}
			} else if runErr == nil || got.ErrorCode != "invalid_request" {
				t.Fatalf("socket with --%s result=%+v err=%v, want invalid_request", explicit, got, runErr)
			}
		})
	}
}

func TestOwnershipHandoffCommandRejectsAlternateJournal(t *testing.T) {
	root := canonicalTempDir(t)
	setOwnershipHandoffFlag(t, "city", root)
	setOwnershipHandoffFlag(t, "root", root)
	setOwnershipHandoffFlag(t, "database", "beads")
	setOwnershipHandoffFlag(t, "workspace", "workspace")
	setOwnershipHandoffFlag(t, "host", "127.0.0.1")
	setOwnershipHandoffFlag(t, "port", "3307")
	setOwnershipHandoffFlag(t, "socket", "")
	setOwnershipHandoffFlag(t, "journal", filepath.Join(root, "alternate.json"))
	oldJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = oldJSON })

	var runErr error
	out := captureStdout(t, func() error {
		runErr = runOwnershipHandoffCommand(ownershipHandoffCmd, nil)
		return nil // captureStdout treats a command error as a test failure.
	})
	if runErr == nil {
		t.Fatal("alternate journal unexpectedly succeeded")
	}
	var got ownershipHandoffOutput
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode strict handoff JSON %q: %v", out, err)
	}
	if got.ErrorCode != "invalid_journal" {
		t.Fatalf("output=%+v, want invalid_journal", got)
	}
	if _, err := os.Stat(filepath.Join(root, "alternate.json")); !os.IsNotExist(err) {
		t.Fatalf("alternate journal was created: %v", err)
	}
}

func setOwnershipHandoffFlag(t *testing.T, name, value string) {
	t.Helper()
	flag := ownershipHandoffCmd.Flags().Lookup(name)
	if flag == nil {
		t.Fatalf("missing handoff flag --%s", name)
	}
	old := flag.Value.String()
	oldChanged := flag.Changed
	if err := ownershipHandoffCmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s: %v", name, err)
	}
	t.Cleanup(func() {
		_ = flag.Value.Set(old)
		flag.Changed = oldChanged
	})
}

type handoffFileSnapshot struct {
	Mode     os.FileMode
	Contents string
}

func handoffDirectorySnapshot(t *testing.T, root string) map[string]handoffFileSnapshot {
	t.Helper()
	files := make(map[string]handoffFileSnapshot)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot := handoffFileSnapshot{Mode: info.Mode()}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snapshot.Contents = string(data)
		} else if info.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			snapshot.Contents = target
		}
		files[rel] = snapshot
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot handoff root: %v", err)
	}
	return files
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(canonical) || strings.TrimSpace(canonical) == "" {
		t.Fatalf("bad canonical temp dir %q", canonical)
	}
	return canonical
}
