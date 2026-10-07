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
func (t Target) String() string {
	if t.BaseURL == nil {
		return ""
	}
	return t.BaseURL.String()
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
	if err := checkCAFileAbsolute(f.CAFile); err != nil {
		return Target{}, err
	}
	return Target{BaseURL: u, ExpectProjectID: f.ExpectProjectID, CAFile: f.CAFile}, nil
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
// a relative CAFile before writing anything, with LoadTarget's own message.
func SaveTarget(beadsDir string, t Target) error {
	if err := checkCAFileAbsolute(t.CAFile); err != nil {
		return err
	}
	f := targetFile{ExpectProjectID: t.ExpectProjectID, API: "v0", CAFile: t.CAFile}
	if t.BaseURL != nil {
		f.URL = t.BaseURL.String()
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling %s: %w", TargetFileName, err)
	}
	if err := os.WriteFile(TargetPath(beadsDir), data, 0o600); err != nil {
		return fmt.Errorf("writing %s: %w", TargetFileName, err)
	}
	return nil
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
