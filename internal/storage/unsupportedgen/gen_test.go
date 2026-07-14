package main

// Regen-idempotence gate for the typed-unsupported shells.
//
// Every backend's unsupported_gen.go carries two compile-time assertions
// (var _ storage.DoltStorage = unsupportedDoltStorage{}, likewise for
// Transaction) plus a real-store assertion (var _ storage.DoltStorage =
// (*Store)(nil)) in each backend package. Together those catch interface GROWTH,
// a hand-DELETED stub, and a skip-list entry the real store does not actually
// implement — all at compile time. What nothing else catches is a hand-edited
// stub BODY: a typo'd Op string, or a stub changed to return nil instead of the
// typed error. That is the drift class this test pins — for each backend it
// regenerates from the exact flags in the backend's //go:generate directive and
// byte-compares against the committed file.
//
// It is an ordinary (ungated) unit test so plain `go test ./...` runs it; it
// needs no DB, no env, and no cgo — runConfig is pure stdlib go/ast codegen.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// shellBackends are the backends whose typed-unsupported shell this tool
// generates. Each dir is relative to this package (internal/storage/unsupportedgen).
var shellBackends = []string{"postgres", "mysql", "sqlite"}

func TestGeneratedShellsAreUpToDate(t *testing.T) {
	// storage package source, relative to this package dir (one level up).
	const srcDir = ".."

	for _, backend := range shellBackends {
		t.Run(backend, func(t *testing.T) {
			dir := filepath.Join("..", backend)
			cfg := parseBackendDirective(t, filepath.Join(dir, "unsupported.go"))
			cfg.src = srcDir

			committedPath := filepath.Join(dir, cfg.out)
			want, err := os.ReadFile(committedPath)
			if err != nil {
				t.Fatalf("read committed %s: %v", committedPath, err)
			}

			// Regenerate into a temp file so a stale/edited committed copy is
			// never clobbered by the check itself.
			tmp := filepath.Join(t.TempDir(), cfg.out)
			cfg.out = tmp
			if err := runConfig(cfg); err != nil {
				t.Fatalf("runConfig(%s): %v", backend, err)
			}
			got, err := os.ReadFile(tmp)
			if err != nil {
				t.Fatalf("read regenerated %s: %v", backend, err)
			}

			if string(got) != string(want) {
				t.Fatalf("%s is stale or hand-edited: it does not match a fresh `go generate ./...`.\n"+
					"Regenerate it (do not hand-edit the DO-NOT-EDIT file) and commit the result.\n"+
					"committed length=%d, regenerated length=%d", committedPath, len(want), len(got))
			}
		})
	}
}

// parseBackendDirective extracts the generator flags from the single
// `//go:generate go run ../unsupportedgen ...` line in a backend's
// unsupported.go, so the test regenerates with exactly the committed flags
// (and auto-follows any change to the skip list).
func parseBackendDirective(t *testing.T, path string) genConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var directive string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "go:generate") && strings.Contains(line, "unsupportedgen") {
			directive = line
			break
		}
	}
	if directive == "" {
		t.Fatalf("%s: no `//go:generate go run ../unsupportedgen` directive found", path)
	}

	fields := strings.Fields(directive)
	cfg := defaultConfig()
	for i := 0; i < len(fields); i++ {
		switch fields[i] {
		case "-pkg":
			cfg.pkg = directiveValue(t, path, fields, &i)
		case "-out":
			cfg.out = directiveValue(t, path, fields, &i)
		case "-type":
			cfg.types = splitList(directiveValue(t, path, fields, &i))
		case "-skip":
			entries := splitList(directiveValue(t, path, fields, &i))
			cfg.skip = make(map[string]bool, len(entries))
			for _, e := range entries {
				cfg.skip[e] = true
			}
		}
	}
	return cfg
}

func directiveValue(t *testing.T, path string, fields []string, i *int) string {
	t.Helper()
	if *i+1 >= len(fields) {
		t.Fatalf("%s: flag %q missing its value", path, fields[*i])
	}
	*i++
	return fields[*i]
}
