// Contributed by gascity from bd-enterprise (internal/enterprise/httpstore/target.go@49d1df2f6)
// to OSS beads under the MIT license.
package httpclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
)

// TargetFileName is the per-user, untracked activation sidecar that names the
// server this workspace is attached to. metadata.json is git-tracked by design
// and selects the backend; the URL and the identity pin are per-user state and
// live beside it, following the proxied_server_client_info.json precedent
// (design D1).
const TargetFileName = "http_target.json"

// ErrNotConnected reports a workspace that selects the http backend but has no
// activation sidecar, so there is no server to dial.
var ErrNotConnected = errors.New("no http target configured for this workspace; run `bd connect <url>`")

// Target is the resolved endpoint for one workspace (design D5).
type Target struct {
	// BaseURL is the server mount root (scheme http or https).
	BaseURL *url.URL
	// ExpectProjectID pins the workspace identity recorded at `bd connect`; ""
	// skips the check. It is compared against ContextResponse.project_id at
	// handshake (D6).
	ExpectProjectID string
	// PreviousBackend records the backend selection metadata.json carried
	// right before THIS connect (bee-ghosttrack CHANGES_REQUESTED on #7288,
	// should-fix 1): `bd connect --clear` reads it back to restore
	// metadata.json's backend selection, so detaching from http returns the
	// workspace to whatever it was attached to before, rather than leaving
	// it pinned to "http" with no sidecar to dial. "" means there was
	// nothing to restore (a workspace's first-ever connect, or a value
	// Attach deliberately left alone — see Attach's own doc).
	PreviousBackend string
	// AllowInsecureCredential records `bd connect --allow-plaintext`'s grant
	// for THIS target (bee-ghosttrack CHANGES_REQUESTED on #7288,
	// should-fix 2): once connected with the flag, every later dial for this
	// workspace — not only connect's own Handshake probe — carries the same
	// opt-in, so an operator who accepted the risk once at connect time does
	// not also need BEADS_HTTP_ALLOW_INSECURE=1 set for every ordinary `bd`
	// command afterward. It is scoped to the sidecar it is saved beside: a
	// `bd connect` to a DIFFERENT url without the flag writes a fresh
	// sidecar with this false, so the grant never silently carries over to a
	// server it was never given for. See guardInsecureCredential.
	AllowInsecureCredential bool
	// CAFile names a PEM file that becomes the ENTIRE trusted root pool for
	// this target — not an addition to the system store — set by
	// `bd connect --ca-file <path>`. "" leaves this target on the system
	// roots. MUST BE ABSOLUTE: `bd connect` resolves the flag's path with
	// filepath.Abs before writing it, and SaveTarget and LoadTarget both
	// refuse a relative one rather than resolving it against whatever
	// directory bd happens to run in. BEADS_HTTP_CA_FILE (see TransportFor / CAFileEnv), when its
	// host-scoped pattern matches this target, overrides this per the
	// documented precedence — env beats sidecar, the same rung order the
	// bearer ladder uses, but the two disagreeing is a refusal, not a silent
	// override. This field exists for a private CA with no name constraints
	// (the Gas City Beads Serve CA) that must never be trusted for any host
	// but this one, so it must never be installed system-wide or exported via
	// SSL_CERT_FILE.
	CAFile string
}

// String renders the target for error text. A zero Target renders empty, which
// is what keeps a backstop refusal from naming a server it never resolved.
//
// Userinfo is dropped rather than masked: url.URL.Redacted hides only a
// password, so a token riding as the username ("https://<token>@host") would
// still print. LoadTarget and SaveTarget refuse userinfo outright; this covers
// a Target an embedder built by hand.
func (t Target) String() string {
	if t.BaseURL == nil {
		return ""
	}
	return withoutUserinfo(t.BaseURL).String()
}

// withoutUserinfo returns u with its userinfo removed, leaving u untouched.
func withoutUserinfo(u *url.URL) *url.URL {
	if u.User == nil {
		return u
	}
	stripped := *u
	stripped.User = nil
	return &stripped
}

// targetFile is the on-disk sidecar. `api` is recorded so a future path major
// can be detected without a handshake; D6 still gates on the server's own
// api_version.
type targetFile struct {
	URL             string `json:"url"`
	ExpectProjectID string `json:"expect_project_id,omitempty"`
	API             string `json:"api,omitempty"`
	// CAFile mirrors Target.CAFile; see that field's doc for the trust model
	// and TransportFor for the env-vs-sidecar precedence.
	CAFile string `json:"ca_file,omitempty"`
	// PreviousBackend mirrors Target.PreviousBackend; see that field's doc.
	PreviousBackend string `json:"previous_backend,omitempty"`
	// AllowPlaintext mirrors Target.AllowInsecureCredential; see that field's
	// doc. Named differently on the wire (matching the CLI flag's own
	// spelling) than the Go field (matching DialOptions.AllowInsecureCredential),
	// deliberately: this is the one record of what a human typed, and the
	// JSON key should read that way on disk.
	AllowPlaintext bool `json:"allow_plaintext,omitempty"`
}

// TargetPath is the sidecar's location for a workspace.
func TargetPath(beadsDir string) string {
	return filepath.Join(beadsDir, TargetFileName)
}

// LoadTarget reads the activation sidecar. It is deliberately the minimal read
// the Open path needs — resolution order (`--server-url`, BEADS_SERVER_URL, the
// provider ladder) and the whole `bd connect` write path are owned elsewhere.
// A missing sidecar is ErrNotConnected, not a nil result: an http workspace
// with no target cannot be opened at all.
func LoadTarget(beadsDir string) (Target, error) {
	path := TargetPath(beadsDir)
	data, err := os.ReadFile(path) // #nosec G304 - controlled path
	if os.IsNotExist(err) {
		return Target{}, ErrNotConnected
	}
	if err != nil {
		return Target{}, fmt.Errorf("reading %s: %w", TargetFileName, err)
	}
	var f targetFile
	if err := json.Unmarshal(data, &f); err != nil {
		return Target{}, fmt.Errorf("parsing %s: %w", TargetFileName, err)
	}
	if f.URL == "" {
		return Target{}, fmt.Errorf("%s names no url; re-run `bd connect <url>`", TargetFileName)
	}
	u, err := url.Parse(f.URL)
	if err != nil {
		return Target{}, fmt.Errorf("parsing url in %s: %w", TargetFileName, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return Target{}, fmt.Errorf("url in %s has scheme %q; want http or https", TargetFileName, u.Scheme)
	}
	if err := checkNoUserinfo(u); err != nil {
		return Target{}, err
	}
	if err := checkCAFileAbsolute(f.CAFile); err != nil {
		return Target{}, err
	}
	return Target{
		BaseURL:                 u,
		ExpectProjectID:         f.ExpectProjectID,
		CAFile:                  f.CAFile,
		PreviousBackend:         f.PreviousBackend,
		AllowInsecureCredential: f.AllowPlaintext,
	}, nil
}

// checkNoUserinfo refuses a url carrying userinfo ("user:secret@host").
// LoadTarget and SaveTarget share it, as they share checkCAFileAbsolute. A
// credential in the url would sit in the sidecar, outside the bearer ladder and
// its host scoping, and would print wherever the url does. The refusal names
// the url without its userinfo, so it does not print the credential either.
func checkNoUserinfo(u *url.URL) error {
	if u == nil || u.User == nil {
		return nil
	}
	return fmt.Errorf(
		"url in %s (%s) carries userinfo; a credential does not belong in the url — re-run `bd connect` with the url alone and supply the token through %s, %s, or the credentials file",
		TargetFileName, withoutUserinfo(u), TokenEnv, TokenCommandEnv)
}

// checkCAFileAbsolute refuses a relative ca_file (see Target.CAFile). LoadTarget
// and SaveTarget share it, so a sidecar can never be written holding a value
// every later load would refuse.
func checkCAFileAbsolute(caFile string) error {
	if caFile != "" && !filepath.IsAbs(caFile) {
		return fmt.Errorf(
			"ca_file in %s is %q, a relative path; it must be absolute, because resolving it against the current directory would pick a different file depending on where bd was run — re-run `bd connect --ca-file` to record an absolute path",
			TargetFileName, caFile)
	}
	return nil
}

// SaveTarget writes the sidecar 0600, beside metadata.json. It exists so tests
// and the connect command share one encoder; the connect UX itself (gitignore
// coverage, identity verification, conversion consent) is not here. It refuses
// a url carrying userinfo and a relative CAFile before writing anything, with
// LoadTarget's own messages. The write is atomic (temp file in the same
// directory, then rename): a reader racing this write — LoadTarget, or
// another process entirely — must never observe a truncated or partial
// sidecar.
func SaveTarget(beadsDir string, t Target) error {
	if err := checkNoUserinfo(t.BaseURL); err != nil {
		return err
	}
	if err := checkCAFileAbsolute(t.CAFile); err != nil {
		return err
	}
	f := targetFile{
		ExpectProjectID: t.ExpectProjectID,
		API:             "v0",
		CAFile:          t.CAFile,
		PreviousBackend: t.PreviousBackend,
		AllowPlaintext:  t.AllowInsecureCredential,
	}
	if t.BaseURL != nil {
		f.URL = t.BaseURL.String()
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", TargetFileName, err)
	}
	if err := writeFileAtomic(TargetPath(beadsDir), data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", TargetFileName, err)
	}
	return nil
}

// writeFileAtomic writes data to a temp file in path's directory and renames
// it over path, so a concurrent reader never sees a truncated or partial
// file. Mirrors internal/configfile's own helper of the same name and shape;
// not shared directly because that one is package-private to configfile.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) //nolint:errcheck // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// RemoveTarget deletes the activation sidecar, reporting whether one was there.
// It is `bd connect --clear`'s whole write: detaching is a per-user act, so it
// must never touch the tracked metadata.json that selects the backend.
func RemoveTarget(beadsDir string) (bool, error) {
	err := os.Remove(TargetPath(beadsDir))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("removing %s: %w", TargetFileName, err)
	}
	return true, nil
}
