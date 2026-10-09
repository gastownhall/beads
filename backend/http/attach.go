// Written fresh for OSS beads S6 (no bd-enterprise source copied): closes the
// S6 review's MED-8 finding, which asked for one public helper an embedder
// (or `bd connect`) can call to activate a workspace for this backend,
// instead of each caller hand-assembling the two-file write itself.
package bdhttp

import (
	"context"
	"fmt"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/gitignore"
	"github.com/steveyegge/beads/internal/httpclient"
)

// Attach activates the http backend for the workspace at beadsDir: it writes
// the per-user activation sidecar (SaveTarget) pinning target's server and
// project, then sets the workspace's git-tracked backend selection in
// metadata.json to this backend, preserving every other configured field
// (database, project id, ...) and creating a fresh default metadata.json only
// when none exists yet. Both files are written atomically (temp file plus
// rename, 0600): see SaveTarget and configfile.Config.Save.
//
// Unless the caller already set target.PreviousBackend, Attach fills it in
// from the workspace's PRIOR backend selection, so `bd connect --clear` can
// restore it later; see that field's doc.
//
// Attach does NOT verify target itself — it has no network access and takes
// target on faith. A caller MUST call Handshake (or httpclient.Handshake)
// against target first and only call Attach once that succeeds; attaching an
// unverified target lets a workspace record a server that is wrong, hostile,
// or simply never answers. Connect, below, does both in the right order and
// is the easier door for a caller that does not need them split.
//
// Attach does not create beadsDir itself — MkdirAll is the caller's job, same
// as SaveTarget's own precondition. It DOES ensure both of this backend's
// per-user filenames are covered by beadsDir's .gitignore
// (gitignore.EnsurePatternIgnored): the sidecar, and
// httpclient.LocalMetadataFileName, which the store writes on first use. That
// is the same guarantee `bd connect` makes via doctor.EnsureGitignoreForBeadsDir
// — but through a small, independent implementation here rather than
// importing cmd/bd/doctor, whose package as a whole pulls in dolt and
// git-process dependencies that would break backend/http's promise of a
// minimal dependency footprint for an embedder. The two are complementary and
// idempotent together.
//
// It is the one place that writes BOTH of the files that activate beads/http
// (the sidecar and metadata.json's backend selection; the local metadata file
// is the store's own), so a caller that used to hand-assemble the pair
// (`bd connect`'s own connectCmd.RunE, test/embedder's linkage proof) can call
// this instead and stay correct across a future change to either file's shape.
func Attach(beadsDir string, target Target) error {
	cfg, err := configfile.Load(beadsDir)
	if err != nil {
		return fmt.Errorf("reading %s: %w", configfile.ConfigFileName, err)
	}
	if cfg == nil {
		cfg = configfile.DefaultConfig()
	}
	// target.PreviousBackend (bee-ghosttrack CHANGES_REQUESTED on #7288,
	// should-fix 1) is what `bd connect --clear` restores metadata.json's
	// backend selection to. A caller rarely sets it itself (connect.go
	// doesn't), so fill it in here unless the workspace is ALREADY on this
	// backend — a re-connect (a new URL, or re-pinning the same one) must
	// not overwrite an earlier recorded previous backend with "http" itself,
	// or a chain of reconnects would forget what to restore and --clear
	// would land back on http with no sidecar. Carry the ALREADY-recorded
	// value forward instead; a failed or missing read just leaves it "",
	// the same as a workspace with nothing to restore. The check reads the
	// raw field: in a process that never ran Register, GetBackend() reports an
	// http workspace as the dolt default, and the reconnect would record that.
	if target.PreviousBackend == "" {
		if cfg.Backend == httpclient.Backend {
			if prior, err := httpclient.LoadTarget(beadsDir); err == nil {
				target.PreviousBackend = prior.PreviousBackend
			}
		} else {
			target.PreviousBackend = cfg.GetBackend()
		}
	}
	if err := SaveTarget(beadsDir, target); err != nil {
		return fmt.Errorf("writing %s: %w", httpclient.TargetFileName, err)
	}
	// Both of this backend's per-user files: the sidecar just written, and
	// the local metadata file the store itself writes on first use, which
	// never passes through here and so must be covered in advance.
	for _, name := range []string{httpclient.TargetFileName, httpclient.LocalMetadataFileName} {
		if err := gitignore.EnsurePatternIgnored(beadsDir, name); err != nil {
			return fmt.Errorf("ensuring %s is gitignored: %w", name, err)
		}
	}
	// Only the backend SELECTION changes here. cfg.Database/cfg.ProjectID (if
	// an existing metadata.json carried them for a different, prior backend)
	// are left exactly as loaded — see connect.go's own comment on this same
	// hazard: this backend's identity lives entirely in the sidecar just
	// written above, and clobbering those fields would corrupt a future
	// `bd connect --clear` back to a different backend.
	cfg.Backend = httpclient.Backend
	if err := cfg.Save(beadsDir); err != nil {
		return fmt.Errorf("writing %s: %w", configfile.ConfigFileName, err)
	}
	return nil
}

// Connect is Handshake followed by Attach: it verifies target — the same
// server-identity check `bd connect` performs before it ever writes
// anything — and only then activates the workspace. It is the convenience
// door for a caller that wants Attach's ordering guarantee (never attach a
// workspace to a server this process has not handshaken) without composing
// Handshake and Attach itself.
//
// Mirroring `bd connect`'s own project-id pinning: if target.ExpectProjectID
// is empty, Connect pins it to whatever project the server answered with
// before calling Attach, so the workspace is tied to that project from this
// call on. If it is already set, Handshake itself refuses a mismatch and
// Connect returns that error without calling Attach at all. Connect does not
// replicate `bd connect`'s other CLI-level policy — --force re-pinning an
// already-attached workspace, or refusing to switch a workspace from a
// different backend — those stay the command's own job; Connect's contract
// is exactly "handshake, then attach".
func Connect(ctx context.Context, beadsDir string, target Target, opts Options) (*ServerSnapshot, error) {
	snapshot, err := Handshake(ctx, target, opts)
	if err != nil {
		return nil, err
	}
	if target.ExpectProjectID == "" {
		target.ExpectProjectID = snapshot.ProjectID
	}
	if err := Attach(beadsDir, target); err != nil {
		return nil, err
	}
	return snapshot, nil
}
