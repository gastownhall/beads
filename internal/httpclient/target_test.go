// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/target_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoveTarget is `bd connect --clear`'s whole write. Detaching is per-user,
// so it must be exactly one file removal — a workspace still selects whatever
// backend its tracked metadata.json says after it.
func TestRemoveTarget(t *testing.T) {
	t.Run("removes the sidecar and nothing else", func(t *testing.T) {
		dir := t.TempDir()
		metadata := filepath.Join(dir, "metadata.json")
		if err := os.WriteFile(metadata, []byte(`{"backend":"http"}`), 0o600); err != nil {
			t.Fatalf("seed metadata.json: %v", err)
		}
		if err := SaveTarget(dir, testTarget(t)); err != nil {
			t.Fatalf("SaveTarget: %v", err)
		}

		removed, err := RemoveTarget(dir)
		if err != nil {
			t.Fatalf("RemoveTarget: %v", err)
		}
		if !removed {
			t.Error("RemoveTarget reported nothing removed, but a sidecar was there")
		}
		if _, err := os.Stat(TargetPath(dir)); !os.IsNotExist(err) {
			t.Errorf("sidecar still present: %v", err)
		}
		body, err := os.ReadFile(metadata)
		if err != nil || string(body) != `{"backend":"http"}` {
			t.Errorf("metadata.json = %q (%v); --clear must not touch the tracked file", body, err)
		}
	})

	t.Run("no sidecar is not an error", func(t *testing.T) {
		removed, err := RemoveTarget(t.TempDir())
		if err != nil {
			t.Fatalf("RemoveTarget on an unconnected workspace: %v", err)
		}
		if removed {
			t.Error("RemoveTarget reported a removal it did not make")
		}
	})
}

// TestSaveTargetHoldsNoToken pins the one thing the sidecar file format
// guarantees: it is per-user, untracked state naming a server, and a credential
// has no home in it. A URL with embedded userinfo would smuggle one in.
func TestSaveTargetHoldsNoToken(t *testing.T) {
	dir := t.TempDir()
	if err := SaveTarget(dir, testTarget(t)); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	body, err := os.ReadFile(TargetPath(dir))
	if err != nil {
		t.Fatalf("read sidecar: %v", err)
	}
	for _, field := range []string{"token", "password", "authorization"} {
		if strings.Contains(strings.ToLower(string(body)), field) {
			t.Errorf("sidecar carries a %q field:\n%s", field, body)
		}
	}
}

// TestLoadTargetRejectsRelativeCAFile is finding 3: a relative ca_file in the
// sidecar must be refused with a clear error rather than resolved against
// whatever directory `bd` happens to be run from, which would silently pick a
// different file depending on the caller's cwd.
func TestLoadTargetRejectsRelativeCAFile(t *testing.T) {
	dir := t.TempDir()
	sidecar := `{"url":"https://example.com","ca_file":"relative/ca.pem"}`
	if err := os.WriteFile(TargetPath(dir), []byte(sidecar), 0o600); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	_, err := LoadTarget(dir)
	if err == nil {
		t.Fatal("LoadTarget accepted a relative ca_file")
	}
	if !strings.Contains(err.Error(), "relative/ca.pem") {
		t.Errorf("error %q does not name the offending path", err)
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error %q does not say the path must be absolute", err)
	}
}

// TestSaveTargetRejectsRelativeCAFile is LoadTarget's refusal on the write
// side: a relative ca_file must never reach the sidecar, where every later
// LoadTarget would refuse it and leave the workspace unopenable. The refusal is
// LoadTarget's own message, and it writes nothing, so the workspace stays
// unconnected rather than holding a sidecar it cannot load.
func TestSaveTargetRejectsRelativeCAFile(t *testing.T) {
	dir := t.TempDir()
	saveErr := SaveTarget(dir, Target{BaseURL: mustParseURL(t, "https://example.com"), CAFile: "relative/ca.pem"})
	if saveErr == nil {
		t.Fatal("SaveTarget accepted a relative ca_file")
	}
	if _, err := LoadTarget(dir); !errors.Is(err, ErrNotConnected) {
		t.Errorf("LoadTarget after the refused save = %v, want ErrNotConnected: nothing may be written", err)
	}

	seeded := t.TempDir()
	sidecar := `{"url":"https://example.com","ca_file":"relative/ca.pem"}`
	if err := os.WriteFile(TargetPath(seeded), []byte(sidecar), 0o600); err != nil {
		t.Fatalf("seed sidecar: %v", err)
	}
	_, loadErr := LoadTarget(seeded)
	if loadErr == nil || saveErr.Error() != loadErr.Error() {
		t.Errorf("SaveTarget refused with %q; want LoadTarget's own refusal %q", saveErr, loadErr)
	}
}

// TestLoadTargetAcceptsAbsoluteCAFile is the control for the above: an
// absolute ca_file round-trips through LoadTarget unchanged.
func TestLoadTargetAcceptsAbsoluteCAFile(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(secureTempDir(t), "ca.pem")
	if err := os.WriteFile(abs, []byte("whatever"), 0o600); err != nil {
		t.Fatalf("seed ca file: %v", err)
	}
	if err := SaveTarget(dir, Target{BaseURL: mustParseURL(t, "https://example.com"), CAFile: abs}); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	got, err := LoadTarget(dir)
	if err != nil {
		t.Fatalf("LoadTarget with an absolute ca_file: %v", err)
	}
	if got.CAFile != abs {
		t.Errorf("CAFile = %q, want %q", got.CAFile, abs)
	}
}
