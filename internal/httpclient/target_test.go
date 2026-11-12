// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/target_test.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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

// sidecarGoldenCAFile is the ca_file the v0 sidecar goldens record. It is
// absolute on every GOOS, as SaveTarget requires (checkCAFileAbsolute):
// path/filepath reads a leading "//" as absolute on Unix and as a UNC share
// on Windows, where "/etc/..." is relative. So each golden is the same bytes
// on every platform. SaveTarget records the path; it never opens it.
const sidecarGoldenCAFile = "//certs.example/beads/serve-ca.pem"

// sidecarGoldenEveryMember is the v0 sidecar with every targetFile member
// set. It is the one golden that fixes those members' order relative to one
// another, and the one TestEverySidecarMemberHasAV0Golden holds to the
// struct.
const sidecarGoldenEveryMember = `{
  "url": "https://bd.example.com:8443/team-a",
  "expect_project_id": "proj-1",
  "api": "v0",
  "ca_file": "//certs.example/beads/serve-ca.pem",
  "previous_backend": "dolt",
  "allow_plaintext": true
}`

// sidecarMembersV0NeverWrites declares, each with its reason, the targetFile
// members SaveTarget never writes for a v0 connection, which
// sidecarGoldenEveryMember therefore cannot show. A member added to
// targetFile goes into the every_member case of
// TestSaveTargetV0SidecarBytesAreFrozen when some v0 connection writes it,
// or here when none does. Empty today: a v0 connection can write every
// member.
var sidecarMembersV0NeverWrites = map[string]string{}

// TestSaveTargetV0SidecarBytesAreFrozen pins, byte for byte, the
// http_target.json SaveTarget writes for v0 connections. The sidecar outlives
// the bd that wrote it: older and newer bds read it back (LoadTarget; see
// TestLoadTargetIgnoresUnknownMembers for the members a bd does not know). A
// member added, dropped, renamed or reordered, an omitempty gained or lost,
// or a value spelled differently is a cross-version change to every v0
// connection, so it must arrive as a reviewed edit of a want below, never as
// a side effect of another change to the encoder.
func TestSaveTargetV0SidecarBytesAreFrozen(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target Target
		want   string
	}{
		{
			// The floor: every optional member omitted; api is always written.
			name:   "url_only",
			target: Target{BaseURL: mustParseURL(t, "https://bd.example.com")},
			want: `{
  "url": "https://bd.example.com",
  "api": "v0"
}`,
		},
		{
			// A connection pinned to its workspace identity, and nothing else.
			name: "plain",
			target: Target{
				BaseURL:         mustParseURL(t, "https://bd.example.com"),
				ExpectProjectID: "proj-1",
			},
			want: `{
  "url": "https://bd.example.com",
  "expect_project_id": "proj-1",
  "api": "v0"
}`,
		},
		{
			// bd connect --ca-file.
			name: "ca_file",
			target: Target{
				BaseURL:         mustParseURL(t, "https://bd.example.com"),
				ExpectProjectID: "proj-1",
				CAFile:          sidecarGoldenCAFile,
			},
			want: `{
  "url": "https://bd.example.com",
  "expect_project_id": "proj-1",
  "api": "v0",
  "ca_file": "//certs.example/beads/serve-ca.pem"
}`,
		},
		{
			// A workspace's first bd connect: Attach records the backend
			// metadata.json selected before (dolt, the default), and the
			// project id is the one the handshake reported.
			name: "previous_backend",
			target: Target{
				BaseURL:         mustParseURL(t, "http://127.0.0.1:8080"),
				ExpectProjectID: "proj-1",
				PreviousBackend: "dolt",
			},
			want: `{
  "url": "http://127.0.0.1:8080",
  "expect_project_id": "proj-1",
  "api": "v0",
  "previous_backend": "dolt"
}`,
		},
		{
			// bd connect --allow-plaintext to a non-loopback http server.
			name: "allow_plaintext",
			target: Target{
				BaseURL:                 mustParseURL(t, "http://bd.internal:8080"),
				ExpectProjectID:         "proj-1",
				AllowInsecureCredential: true,
			},
			want: `{
  "url": "http://bd.internal:8080",
  "expect_project_id": "proj-1",
  "api": "v0",
  "allow_plaintext": true
}`,
		},
		{
			name: "every_member",
			target: Target{
				BaseURL:                 mustParseURL(t, "https://bd.example.com:8443/team-a"),
				ExpectProjectID:         "proj-1",
				CAFile:                  sidecarGoldenCAFile,
				PreviousBackend:         "dolt",
				AllowInsecureCredential: true,
			},
			want: sidecarGoldenEveryMember,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := SaveTarget(dir, tc.target); err != nil {
				t.Fatalf("SaveTarget: %v", err)
			}
			got, err := os.ReadFile(TargetPath(dir))
			if err != nil {
				t.Fatalf("reading the sidecar: %v", err)
			}
			if string(got) != tc.want {
				t.Errorf("SaveTarget wrote a v0 %s other than the frozen one.\n--- want ---\n%s\n--- got ---\n%s\n"+
					"Every bd that opens this workspace reads these bytes. If the change is intended, make the got "+
					"bytes this case's want, and say in the PR how older and newer bds read them.",
					TargetFileName, tc.want, got)
			}
		})
	}
}

// TestEverySidecarMemberHasAV0Golden keeps those goldens complete: every
// member targetFile defines appears in sidecarGoldenEveryMember or is
// declared, with a reason, in sidecarMembersV0NeverWrites. Without it, a
// member added with omitempty and set by no case above would change what
// some v0 connections write while every golden stayed green.
func TestEverySidecarMemberHasAV0Golden(t *testing.T) {
	members := map[string]bool{}
	typ := reflect.TypeOf(targetFile{})
	for i := range typ.NumField() {
		field := typ.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue // encoding/json never writes it
		case "":
			name = field.Name // encoding/json's default spelling
		}
		members[name] = true
		_, declared := sidecarMembersV0NeverWrites[name]
		shown := strings.Contains(sidecarGoldenEveryMember, `"`+name+`": `)
		switch {
		case shown && declared:
			t.Errorf("%q is in sidecarGoldenEveryMember and declared in sidecarMembersV0NeverWrites; it is one or the other", name)
		case !shown && !declared:
			t.Errorf("targetFile member %q is in no v0 sidecar golden: set it in TestSaveTargetV0SidecarBytesAreFrozen's every_member case if a v0 connection can write it, or declare in sidecarMembersV0NeverWrites why none does", name)
		}
	}
	for name, why := range sidecarMembersV0NeverWrites {
		if !members[name] {
			t.Errorf("sidecarMembersV0NeverWrites declares %q, which targetFile does not define; drop it", name)
		}
		if strings.TrimSpace(why) == "" {
			t.Errorf("sidecarMembersV0NeverWrites declares %q without a reason; say why no v0 connection writes it", name)
		}
	}
}

// TestLoadTargetIgnoresUnknownMembers is the other half of the sidecar's
// cross-version contract: LoadTarget reads the members it knows and ignores
// the rest. A workspace whose sidecar a newer bd wrote, with members this bd
// has never heard of, still opens and dials the server it names; a decoder
// that refused unknown members would strand it until someone reconnected it.
func TestLoadTargetIgnoresUnknownMembers(t *testing.T) {
	// One unknown member of each JSON kind, named so no bd will define it.
	const unknown = `
  "x_unknown_string": "v9",
  "x_unknown_number": 7,
  "x_unknown_bool": false,
  "x_unknown_null": null,
  "x_unknown_array": [1, "two", {"three": 3}],
  "x_unknown_object": {"nested": {"deeper": ["a"]}}`

	for _, tc := range []struct {
		name    string
		sidecar string
		want    Target
	}{
		{
			name:    "beside every known member",
			sidecar: strings.TrimSuffix(sidecarGoldenEveryMember, "\n}") + "," + unknown + "\n}",
			want: Target{
				BaseURL:                 mustParseURL(t, "https://bd.example.com:8443/team-a"),
				ExpectProjectID:         "proj-1",
				CAFile:                  sidecarGoldenCAFile,
				PreviousBackend:         "dolt",
				AllowInsecureCredential: true,
			},
		},
		{
			name:    "beside the url alone",
			sidecar: `{"url": "http://127.0.0.1:8080",` + unknown + "\n}",
			want:    Target{BaseURL: mustParseURL(t, "http://127.0.0.1:8080")},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(TargetPath(dir), []byte(tc.sidecar), 0o600); err != nil {
				t.Fatalf("seed sidecar: %v", err)
			}
			got, err := LoadTarget(dir)
			if err != nil {
				t.Fatalf("LoadTarget refused a sidecar carrying members it does not know: %v\n%s", err, tc.sidecar)
			}
			// Field by field rather than the whole Target: a field a later bd
			// adds, and fills with its own default when the sidecar is silent,
			// is not an unknown member being read.
			if got.String() != tc.want.String() {
				t.Errorf("BaseURL = %q, want %q", got.String(), tc.want.String())
			}
			if got.ExpectProjectID != tc.want.ExpectProjectID {
				t.Errorf("ExpectProjectID = %q, want %q", got.ExpectProjectID, tc.want.ExpectProjectID)
			}
			if got.CAFile != tc.want.CAFile {
				t.Errorf("CAFile = %q, want %q", got.CAFile, tc.want.CAFile)
			}
			if got.PreviousBackend != tc.want.PreviousBackend {
				t.Errorf("PreviousBackend = %q, want %q", got.PreviousBackend, tc.want.PreviousBackend)
			}
			if got.AllowInsecureCredential != tc.want.AllowInsecureCredential {
				t.Errorf("AllowInsecureCredential = %t, want %t", got.AllowInsecureCredential, tc.want.AllowInsecureCredential)
			}
		})
	}
}

// TestTargetRefusesUserinfo closes the one way a credential could still reach
// the sidecar: a url with userinfo. SaveTarget refuses it and writes nothing,
// LoadTarget refuses a sidecar holding one with the same message, and neither
// message prints the credential.
func TestTargetRefusesUserinfo(t *testing.T) {
	const secret = "s3cr3t"
	for _, tc := range []struct{ name, url string }{
		{"user and password", "https://ada:" + secret + "@serve.example.com"},
		{"a token as the username", "https://" + secret + "@serve.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			saveErr := SaveTarget(dir, Target{BaseURL: mustParseURL(t, tc.url)})
			if saveErr == nil {
				t.Fatal("SaveTarget accepted a url carrying userinfo")
			}
			if _, err := LoadTarget(dir); !errors.Is(err, ErrNotConnected) {
				t.Errorf("LoadTarget after the refused save = %v, want ErrNotConnected: nothing may be written", err)
			}

			seeded := t.TempDir()
			if err := os.WriteFile(TargetPath(seeded), []byte(`{"url":"`+tc.url+`"}`), 0o600); err != nil {
				t.Fatalf("seed sidecar: %v", err)
			}
			_, loadErr := LoadTarget(seeded)
			if loadErr == nil {
				t.Fatal("LoadTarget accepted a url carrying userinfo")
			}
			if saveErr.Error() != loadErr.Error() {
				t.Errorf("SaveTarget refused with %q; want LoadTarget's own refusal %q", saveErr, loadErr)
			}
			if strings.Contains(loadErr.Error(), secret) {
				t.Fatalf("the refusal printed the credential: %v", loadErr)
			}
			if !strings.Contains(loadErr.Error(), "https://serve.example.com") {
				t.Errorf("error %q does not name the server", loadErr)
			}
		})
	}
}

// TestTargetStringDropsUserinfo: error text renders a target through String, so
// a Target built by hand with userinfo must not print it — the username
// included, which url.URL.Redacted would keep.
func TestTargetStringDropsUserinfo(t *testing.T) {
	for _, raw := range []string{"https://ada:s3cr3t@serve.example.com/v0", "https://s3cr3t@serve.example.com/v0"} {
		u := mustParseURL(t, raw)
		if got, want := (Target{BaseURL: u}).String(), "https://serve.example.com/v0"; got != want {
			t.Errorf("Target{%s}.String() = %q, want %q", raw, got, want)
		}
		if u.User == nil {
			t.Errorf("String stripped userinfo from the caller's url rather than a copy")
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

// TestSaveTargetRoundTripsPreviousBackendAndAllowInsecureCredential pins the
// bee-ghosttrack CHANGES_REQUESTED findings on #7288 (should-fix 1 and 2):
// `bd connect --clear` needs PreviousBackend back to restore metadata.json's
// backend selection, and an ordinary command's dial needs
// AllowInsecureCredential back to honor a plaintext grant made at connect
// time. Neither is useful if the sidecar does not actually carry it through
// a save/load round trip.
func TestSaveTargetRoundTripsPreviousBackendAndAllowInsecureCredential(t *testing.T) {
	dir := t.TempDir()
	target := Target{
		BaseURL:                 mustParseURL(t, "http://127.0.0.1:8080"),
		PreviousBackend:         "dolt",
		AllowInsecureCredential: true,
	}
	if err := SaveTarget(dir, target); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	got, err := LoadTarget(dir)
	if err != nil {
		t.Fatalf("LoadTarget: %v", err)
	}
	if got.PreviousBackend != "dolt" {
		t.Errorf("PreviousBackend = %q, want %q", got.PreviousBackend, "dolt")
	}
	if !got.AllowInsecureCredential {
		t.Error("AllowInsecureCredential = false after a round trip, want true")
	}

	raw, err := os.ReadFile(TargetPath(dir))
	if err != nil {
		t.Fatalf("reading sidecar: %v", err)
	}
	if !strings.Contains(string(raw), `"previous_backend": "dolt"`) {
		t.Errorf("sidecar does not carry previous_backend: %s", raw)
	}
	if !strings.Contains(string(raw), `"allow_plaintext": true`) {
		t.Errorf("sidecar does not carry allow_plaintext: %s", raw)
	}
}

// TestSaveTargetOmitsPreviousBackendAndAllowInsecureCredentialWhenUnset keeps
// the common case's sidecar exactly as small as before this fix: a workspace
// with nothing to restore and no plaintext grant must not gain two new
// always-present keys.
func TestSaveTargetOmitsPreviousBackendAndAllowInsecureCredentialWhenUnset(t *testing.T) {
	dir := t.TempDir()
	if err := SaveTarget(dir, testTarget(t)); err != nil {
		t.Fatalf("SaveTarget: %v", err)
	}
	raw, err := os.ReadFile(TargetPath(dir))
	if err != nil {
		t.Fatalf("reading sidecar: %v", err)
	}
	if strings.Contains(string(raw), "previous_backend") {
		t.Errorf("sidecar carries previous_backend with nothing to restore: %s", raw)
	}
	if strings.Contains(string(raw), "allow_plaintext") {
		t.Errorf("sidecar carries allow_plaintext with no grant: %s", raw)
	}
}
