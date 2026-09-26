package main

import (
	"testing"

	"github.com/steveyegge/beads/internal/config"
)

// TestImportSyncPrefix pins the ONE value runImportRecordsClassic hands to both
// of its issue_prefix writers — the pre-batch seed and the post-batch sync.
//
// It is deliberately a plain unit test: no store, no Dolt binary, no container.
// The classic seed's end-to-end cases (import_missing_config_prefix_test.go,
// import_embedded_test.go) are gated behind a Dolt server or
// BEADS_TEST_EMBEDDED_DOLT and skip under the BEADS_TEST_SKIP=dolt the required
// integration lanes set, so the resolver's rules are asserted here instead,
// where every lane runs them.
func TestImportSyncPrefix(t *testing.T) {
	tests := []struct {
		name   string
		yaml   string
		global bool
		want   string
	}{
		{name: "absent is reconcile nothing", yaml: "", want: ""},
		{name: "plain value passes through", yaml: "bd", want: "bd"},
		{
			// Both writers must see the SAME trimmed value. When the seed
			// stored the trimmed one and the sync compared the raw one, the
			// sync found them unequal and overwrote the seed's value with the
			// padded one.
			name: "padded value is trimmed for both writers",
			yaml: "  bd\t",
			want: "bd",
		},
		{
			// The guard covers the sync too, not just the seed: an invalid
			// prefix reconciles nothing at all.
			name: "invalid value reconciles nothing",
			yaml: "BAD",
			want: "",
		},
		{
			// Trailing hyphens are NOT an invalid prefix: validatePrefix
			// TrimRight's them before matching, which is the same tolerance
			// `bd rename-prefix` has. The resolver defers to that guard rather
			// than inventing a stricter rule of its own, so the value reaches
			// both writers unchanged.
			name: "trailing hyphens are tolerated by the shared guard",
			yaml: "bd--",
			want: "bd--",
		},
		{
			// --global: config.yaml is per-project and must not speak for the
			// shared global store, whatever it says.
			name:   "global mode ignores config.yaml entirely",
			yaml:   "localproj",
			global: true,
			want:   "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Isolate CWD and BEADS_DIR before config.Initialize(): a leaked
			// BEADS_DIR or a cwd inside the repo tree lets Initialize() load a
			// real ambient config.yaml whose issue-prefix would win over the
			// value under test.
			t.Chdir(t.TempDir())
			t.Setenv("BEADS_DIR", "")
			t.Setenv("BEADS_TEST_IGNORE_REPO_CONFIG", "1")

			config.ResetForTesting()
			t.Cleanup(func() { config.ResetForTesting() })
			if err := config.Initialize(); err != nil {
				t.Fatalf("config.Initialize: %v", err)
			}
			config.Set("issue-prefix", tt.yaml)

			origGlobal := globalFlag
			globalFlag = tt.global
			t.Cleanup(func() { globalFlag = origGlobal })

			if got := importSyncPrefix(); got != tt.want {
				t.Errorf("importSyncPrefix() with issue-prefix %q (global=%v) = %q, want %q", tt.yaml, tt.global, got, tt.want)
			}
		})
	}
}
