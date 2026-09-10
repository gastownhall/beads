//go:build cgo

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/steveyegge/beads/internal/beads"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/storage/backends"
	"github.com/steveyegge/beads/internal/storage/contextinfo"
	"github.com/steveyegge/beads/internal/storage/domain"
)

// TestContextRoutesNameOneWorkspaceTheSameWay.
//
// There are two routes to a workspace snapshot and they are not
// interchangeable: `bd context` reads the config files itself, because it has
// to answer in degraded states where no database opens, while the proxied route
// and GET /v0/beads/context go through the contextinfo provider. Both hand the
// result to domain.PublishedContext, which exists so that the identity an
// automation client is given and the identity printed on the operator's
// terminal beside it are the same values read the same way.
//
// Nothing tested that. Both routes carried their own `Backend:
// configfile.BackendDolt`, so a workspace on a registered backend was described
// as embedded Dolt on database "beads" by both — the same lie twice, which is
// indistinguishable from agreement.
func TestContextRoutesNameOneWorkspaceTheSameWay(t *testing.T) {
	const registered = "context-identity-backend"
	backends.Register(registered, backends.Backend{
		Open: func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errors.ErrUnsupported
		},
		OpenReadOnly: func(context.Context, string) (storage.DoltStorage, error) {
			return nil, errors.ErrUnsupported
		},
		WorkspaceIsBeadsDir: true,
	})
	t.Cleanup(func() { backends.Deregister(registered) })

	for _, tc := range []struct {
		name string
		cfg  *configfile.Config
		want domain.PublishedContextFields
	}{
		{
			// The case that was wrong. This workspace configures no Dolt
			// anything, and both Dolt values default rather than fail.
			name: "a registered backend",
			cfg:  &configfile.Config{Backend: registered, ProjectID: "proj-registered"},
			want: domain.PublishedContextFields{Backend: registered, ProjectID: "proj-registered"},
		},
		{
			name: "embedded dolt",
			cfg: &configfile.Config{
				Backend:   configfile.BackendDolt,
				DoltMode:  configfile.DoltModeEmbedded,
				ProjectID: "proj-dolt",
			},
			want: domain.PublishedContextFields{
				Backend:   configfile.BackendDolt,
				DoltMode:  configfile.DoltModeEmbedded,
				Database:  configfile.DefaultDoltDatabase,
				ProjectID: "proj-dolt",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepoAt(t, dir)
			beadsDir := filepath.Join(dir, ".beads")
			if err := os.MkdirAll(beadsDir, 0o755); err != nil {
				t.Fatalf("mkdir .beads: %v", err)
			}
			if err := tc.cfg.Save(beadsDir); err != nil {
				t.Fatalf("save metadata.json: %v", err)
			}
			t.Chdir(dir)
			t.Setenv("BEADS_DIR", beadsDir)
			beads.ResetCaches()
			t.Cleanup(beads.ResetCaches)

			provider, err := contextinfo.NewContextProvider(dir, Version).ContextUseCase().GetContextInfo(t.Context())
			if err != nil {
				t.Fatalf("provider route: %v", err)
			}
			direct := directContextSnapshot(t, beadsDir)

			// The paths and the version come from the workspace either way and
			// are not what this compares; the identity is.
			want := tc.want
			want.BdVersion, want.BeadsDir, want.RepoRoot = Version, provider.BeadsDir, provider.RepoRoot

			for _, got := range []struct {
				route  string
				fields domain.PublishedContextFields
			}{
				{"the contextinfo provider (GET /v0/beads/context, bd context in proxied mode)", domain.PublishedContext(provider)},
				{"the direct route (bd context)", domain.PublishedContext(direct)},
			} {
				if got.fields != want {
					t.Errorf("%s published\n  %+v\nwant\n  %+v", got.route, got.fields, want)
				}
			}
		})
	}
}

// NOT COVERED: this drives applyContextBackend, not contextCmd's RunE, so it
// pins the shared projection rather than the command's own wiring. A change
// that bypassed applyContextBackend inside RunE would not be caught here.
//
// directContextSnapshot assembles the snapshot `bd context`'s direct route
// answers from, off the same config files the command reads.
func directContextSnapshot(t *testing.T, beadsDir string) domain.ContextInfo {
	t.Helper()
	rc, err := beads.GetRepoContext()
	if err != nil {
		t.Fatalf("repo context: %v", err)
	}
	cfg, err := configfile.Load(beadsDir)
	if err != nil || cfg == nil {
		t.Fatalf("load metadata.json: %v", err)
	}

	snapshot := domain.ContextInfo{
		BdVersion: Version,
		BeadsDir:  rc.BeadsDir,
		RepoRoot:  rc.RepoRoot,
	}
	if err := applyContextBackend(&snapshot, rc.BeadsDir, cfg); err != nil {
		t.Fatalf("applyContextBackend: %v", err)
	}
	return snapshot
}
