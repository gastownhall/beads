package domain

import (
	"context"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
)

type recordingConfigRepo struct {
	ConfigSQLRepository
	config        map[string]string
	metadata      map[string]string
	localMetadata map[string]string
}

func newRecordingConfigRepo() *recordingConfigRepo {
	return &recordingConfigRepo{
		config:        map[string]string{},
		metadata:      map[string]string{},
		localMetadata: map[string]string{},
	}
}

func (r *recordingConfigRepo) SetConfig(_ context.Context, key, value string) error {
	r.config[key] = value
	return nil
}

func (r *recordingConfigRepo) SetMetadata(_ context.Context, key, value string) error {
	r.metadata[key] = value
	return nil
}

func (r *recordingConfigRepo) SetLocalMetadata(_ context.Context, key, value string) error {
	r.localMetadata[key] = value
	return nil
}

func TestBootstrapProject_WritesIdentity(t *testing.T) {
	repo := newRecordingConfigRepo()
	uc := NewBootstrapUseCase(repo, nil)

	params := BootstrapProjectParams{
		Prefix:         "gc",
		ProjectID:      "proj-bts",
		BdVersion:      "1.0.0-test",
		LastImportTime: time.Date(2026, 7, 29, 0, 0, 0, 0, time.UTC),
		RepoID:         "repo-1",
		CloneID:        "clone-1",
	}
	if _, err := uc.BootstrapProject(context.Background(), params); err != nil {
		t.Fatalf("BootstrapProject: %v", err)
	}
	if got := repo.config["issue_prefix"]; got != "gc" {
		t.Errorf("issue_prefix = %q, want %q", got, "gc")
	}
	if got := repo.metadata["_project_id"]; got != "proj-bts" {
		t.Errorf("_project_id = %q, want %q", got, "proj-bts")
	}
}

func TestResolveDoltDatabaseName_DerivedFlag(t *testing.T) {
	cases := []struct {
		name        string
		cfgDB       string
		prefix      string
		dbFlag      string
		wantName    string
		wantDerived bool
	}{
		{"flag pins the name", "meta_db", "pfx", "flag_db", "flag_db", false},
		{"metadata pins the name", "meta_db", "pfx", "", "meta_db", false},
		{"prefix guess is derived", "", "my-proj", "", "my_proj", true},
		{"default fallback is derived", "", "", "", "beads", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var fileCfg *configfile.Config
			if tc.cfgDB != "" {
				fileCfg = &configfile.Config{DoltDatabase: tc.cfgDB}
			}
			name, derived := resolveDoltDatabaseName(fileCfg, tc.prefix, tc.dbFlag)
			if name != tc.wantName || derived != tc.wantDerived {
				t.Errorf("resolveDoltDatabaseName() = (%q, %v), want (%q, %v)", name, derived, tc.wantName, tc.wantDerived)
			}
		})
	}
}
