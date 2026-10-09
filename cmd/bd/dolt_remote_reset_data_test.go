package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/dolthub/dolt/go/store/blobstore"
	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/execenv"
	"github.com/steveyegge/beads/internal/githooksenv"
)

// TestResetDataRefNamesMatchDolt keeps the locally pinned data-plane ref
// names in sync with the constants dolt's git blobstore actually publishes
// (store/blobstore/git_refs.go). If a dolt bump renames either ref, this
// fails instead of reset-data silently deleting the wrong refs.
func TestResetDataRefNamesMatchDolt(t *testing.T) {
	if gitDoltDataRef != blobstore.DoltDataRef {
		t.Errorf("gitDoltDataRef = %q, dolt publishes %q", gitDoltDataRef, blobstore.DoltDataRef)
	}
	if config.DefaultDoltRemoteInfoBranch != blobstore.DefaultInfoBranch {
		t.Errorf("config.DefaultDoltRemoteInfoBranch = %q, dolt publishes %q", config.DefaultDoltRemoteInfoBranch, blobstore.DefaultInfoBranch)
	}
}

// The info ref reset-data deletes beside the data ref is the one dolt
// publishes: the DOLT_REMOTE_INFO_BRANCH override when one is set, the
// default otherwise, and none when an empty override disables the marker,
// since the default name is then an ordinary branch that may hold data.
func TestDoltInfoRefFollowsOverride(t *testing.T) {
	out := []byte("a\trefs/dolt/units/team-12542\nb\trefs/heads/marker\nc\trefs/heads/__dolt_remote_info__\nd\trefs/dolt/data\n")
	t.Setenv(config.DoltRemoteInfoBranchEnvVar, "marker")
	if got := doltInfoRef(); got != "refs/heads/marker" {
		t.Errorf("override: doltInfoRef() = %q", got)
	}
	if got := parseDoltDataRefs(out, "refs/dolt/units/team-12542"); !slices.Equal(got, []string{"refs/dolt/units/team-12542", "refs/heads/marker"}) {
		t.Errorf("with the override, parseDoltDataRefs kept %q", got)
	}
	t.Setenv(config.DoltRemoteInfoBranchEnvVar, "")
	if got := doltInfoRef(); got != "" {
		t.Errorf("disabled marker: doltInfoRef() = %q, want none", got)
	}
	if got := parseDoltDataRefs(out, "refs/dolt/units/team-12542"); !slices.Equal(got, []string{"refs/dolt/units/team-12542"}) {
		t.Errorf("with the marker disabled, parseDoltDataRefs must keep the data ref alone, got %q", got)
	}
	os.Unsetenv(config.DoltRemoteInfoBranchEnvVar)
	if got, want := doltInfoRef(), "refs/heads/"+config.DefaultDoltRemoteInfoBranch; got != want {
		t.Errorf("no override: doltInfoRef() = %q, want dolt's default %q", got, want)
	}
}

// The confirmation, the non-interactive refusal, and the JSON result name
// the ref of a git-backed remote: on a repository holding several databases
// on their own refs, the URL alone does not say which one is replaced. A
// remote that is not git-backed carries no ref and is named by its URL. The
// JSON ref follows `bd dolt remote list --json`: the recorded ref, empty
// (and left out) for the default.
func TestResetDataTargetNamesTheRef(t *testing.T) {
	const url = "git+ssh://git@host/org/ledgers.git"
	cases := []struct {
		name     string
		kind     resetDataKind
		dataRef  string
		want     string
		wantJSON string
	}{
		{"unit ref", resetDataGitBacked, "refs/dolt/units/team-12542", url + ", ref refs/dolt/units/team-12542", "refs/dolt/units/team-12542"},
		{"branch ref", resetDataGitBacked, "refs/heads/issue-data", url + ", ref refs/heads/issue-data", "refs/heads/issue-data"},
		{"default ref", resetDataGitBacked, "", url + ", ref refs/dolt/data", ""},
		{"explicit default ref", resetDataGitBacked, "refs/dolt/data", url + ", ref refs/dolt/data", ""},
		{"file store", resetDataFileStore, "", url, ""},
		{"absent file target", resetDataFileAbsent, "", url, ""},
		{"unsupported", resetDataUnsupported, "", url, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resetDataTarget(url, tc.kind, tc.dataRef); got != tc.want {
				t.Errorf("resetDataTarget = %q, want %q", got, tc.want)
			}
			if got := resetDataJSONRef(tc.kind, tc.dataRef); got != tc.wantJSON {
				t.Errorf("resetDataJSONRef = %q, want %q", got, tc.wantJSON)
			}
		})
	}
}

func TestEnvWithNoGitHooksPreservesExistingParameters(t *testing.T) {
	const existing = "'user.email=ci@example.com'"
	t.Setenv(githooksenv.ParametersEnv, existing)
	t.Setenv("BEADS_TEST_NO_HOOKS_KEEP", "GIT_CONFIG_PARAMETERS=unrelated-value")

	// Build the expected slice independently of Without/DisabledEnv so a
	// stale entry cannot hide behind Extract's last-wins lookup.
	var want []string
	for _, entry := range os.Environ() {
		if !execenv.KeyEqual(execenv.EntryKey(entry), githooksenv.ParametersEnv) {
			want = append(want, entry)
		}
	}
	want = append(want, githooksenv.ParametersEnv+"="+existing+" "+githooksenv.NoHooksParam)
	if got := envWithNoGitHooks(); !slices.Equal(got, want) {
		t.Fatalf("envWithNoGitHooks() differs from the expected environment: got %d entries, want %d", len(got), len(want))
	}
}

func TestClassifyResetDataRemote(t *testing.T) {
	makeBareGitRepo := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "HEAD"), []byte("ref: refs/heads/main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	makeDoltFileStore := func(t *testing.T) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "manifest"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return dir
	}

	bareRepo := makeBareGitRepo(t)
	fileStore := makeDoltFileStore(t)
	emptyDir := t.TempDir()
	strangeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(strangeDir, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		url     string
		want    resetDataKind
		wantErr bool
	}{
		{"git+https", "git+https://github.com/org/repo.git", resetDataGitBacked, false},
		{"git+ssh", "git+ssh://git@github.com/org/repo.git", resetDataGitBacked, false},
		{"scp-style", "git@github.com:org/repo.git", resetDataGitBacked, false},
		{"git+file", "git+file://" + bareRepo, resetDataGitBacked, false},
		{"file url to bare git repo", "file://" + bareRepo, resetDataGitBacked, false},
		{"bare path to bare git repo", bareRepo, resetDataGitBacked, false},
		{"file url to dolt store", "file://" + fileStore, resetDataFileStore, false},
		{"bare path to dolt store", fileStore, resetDataFileStore, false},
		{"file url to missing dir", "file://" + filepath.Join(emptyDir, "nope"), resetDataFileAbsent, false},
		{"file url to empty dir", "file://" + emptyDir, resetDataFileAbsent, false},
		{"file url to unrecognized dir", "file://" + strangeDir, 0, true},
		{"aws", "aws://[table:bucket]/db", resetDataUnsupported, false},
		{"gs", "gs://bucket/db", resetDataUnsupported, false},
		{"dolthub", "dolthub://org/db", resetDataUnsupported, false},
		{"https hosted", "https://doltremoteapi.dolthub.com/org/db", resetDataUnsupported, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := classifyResetDataRemote(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("classifyResetDataRemote(%q) = %v, want error", tt.url, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("classifyResetDataRemote(%q): %v", tt.url, err)
			}
			if got != tt.want {
				t.Errorf("classifyResetDataRemote(%q) = %v, want %v", tt.url, got, tt.want)
			}
		})
	}
}

func TestResetDataGitURL(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"git+https://github.com/org/repo.git", "https://github.com/org/repo.git"},
		{"git+ssh://git@github.com/org/repo.git", "ssh://git@github.com/org/repo.git"},
		{"git+file:///tmp/remote.git", "file:///tmp/remote.git"},
		{"git@github.com:org/repo.git", "git@github.com:org/repo.git"},
		{"file:///tmp/remote.git", "file:///tmp/remote.git"},
		{"/tmp/remote.git", "file:///tmp/remote.git"},
	}
	for _, tt := range tests {
		if got := resetDataGitURL(tt.url); got != tt.want {
			t.Errorf("resetDataGitURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}
