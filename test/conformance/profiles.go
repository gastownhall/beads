// Package conformance is the end-to-end (real `bd` binary) conformance harness. It
// complements the in-process store suite (internal/storage/conformance): that one
// exercises a store object; this one exercises the whole CLI path — `bd init`, the
// factory, metadata.json, and output formatting — differentially against the Dolt
// reference.
//
// One declarative registry (Profiles) describes how to create and tear down an
// isolated workspace for each backend. Adding a backend is one Profiles entry; the
// runner and scripts/conformance.sh pick it up automatically. The suite is behind
// the `e2e` build tag because it shells out to a freshly built bd.
package conformance

// Workspace is one isolated place a backend can be `bd init`-ed. Dir is the working
// directory; init creates its .beads directory.
type Workspace struct {
	Dir string
}

// BackendProfile declares everything the E2E runner needs to init and isolate a
// backend. Keep it declarative: no test logic here, just recipe + isolation + the
// deferral allowlist.
type BackendProfile struct {
	// Name identifies the backend in output and in XFail keys.
	Name string
	// Reference marks the differential baseline every candidate is compared to.
	// Exactly one profile must set it (dolt-embedded).
	Reference bool
	// Available reports whether this backend can run here (e.g. a required env var
	// is set). Unavailable profiles are skipped, not failed.
	Available func() bool
	// InitArgs returns the extra args appended to `bd init` for a fresh workspace.
	InitArgs func(ws *Workspace) []string
	// Env returns extra environment (KEY=VALUE) for every bd invocation in ws.
	Env func(ws *Workspace) []string
	// XFail lists scenarios this backend is known not to match the reference on,
	// each with a reason. They are reported as XFAIL (visible, never masked); an
	// XFAIL that starts matching is flagged so the list can only shrink.
	XFail map[string]string
}

// Profiles is the backend registry. Add a backend here and every tier covers it.
var Profiles = []BackendProfile{
	{
		Name:      "dolt-embedded",
		Reference: true,
		Available: func() bool { return true },
		InitArgs:  func(*Workspace) []string { return nil },
	},
}

// Reference returns the single reference profile.
func Reference() BackendProfile {
	for _, p := range Profiles {
		if p.Reference {
			return p
		}
	}
	panic("conformance: no reference profile registered")
}

// Candidates returns the non-reference profiles that are available here.
func Candidates() []BackendProfile {
	var out []BackendProfile
	for _, p := range Profiles {
		if !p.Reference && p.Available != nil && p.Available() {
			out = append(out, p)
		}
	}
	return out
}
