package bdhttp

import "github.com/steveyegge/beads/internal/httpclient"

// Target is the resolved endpoint for one workspace: the server's mount root
// and, optionally, the workspace identity pinned when it was connected. A
// pinned id is compared against the server's own at every handshake, which is
// what turns "some server answered" into "the server that owns this workspace
// answered".
type Target = httpclient.Target

// TargetFileName is the per-user, untracked activation sidecar that names the
// server a workspace is attached to. metadata.json is git-tracked by design
// and selects the backend; the URL and the identity pin are per-user state
// and live beside it.
const TargetFileName = httpclient.TargetFileName

// ErrNotConnected reports a workspace that selects this backend but has no
// activation sidecar, so there is no server to dial. It is what an Open
// through the registry returns for a workspace nobody ran `bd connect` in.
var ErrNotConnected = httpclient.ErrNotConnected

// TargetPath is the sidecar's location for a workspace, given its .beads
// directory.
func TargetPath(beadsDir string) string { return httpclient.TargetPath(beadsDir) }

// LoadTarget reads the activation sidecar. A missing one is ErrNotConnected
// rather than a zero Target: an http workspace with no target cannot be
// opened at all.
func LoadTarget(beadsDir string) (Target, error) { return httpclient.LoadTarget(beadsDir) }

// SaveTarget writes the sidecar 0600, beside metadata.json. It is the encoder
// `bd connect` uses; the connect UX around it — gitignore coverage, identity
// verification, conversion consent — is the CLI's, so an embedder that writes
// a workspace this way owns those decisions itself.
func SaveTarget(beadsDir string, t Target) error { return httpclient.SaveTarget(beadsDir, t) }

// RemoveTarget deletes the activation sidecar, reporting whether one was
// there. Detaching is a per-user act, so it never touches the tracked
// metadata.json that selects the backend.
func RemoveTarget(beadsDir string) (bool, error) { return httpclient.RemoveTarget(beadsDir) }
