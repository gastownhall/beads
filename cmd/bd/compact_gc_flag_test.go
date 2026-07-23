package main

import "testing"

// TestIsUnknownArchiveLevelFlagError covers the classifier that decides
// whether runCompactDolt should retry `dolt gc` without --archive-level
// (gastownhall/beads#4986). It must recognize the real unknown-flag
// rejection from an older external dolt, and must NOT misclassify genuine
// GC failures — those should keep failing compact rather than being
// silently swallowed by a retry that then also fails.
func TestIsUnknownArchiveLevelFlagError(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   bool
	}{
		{
			name:   "real dolt argparser unknown-option message",
			output: "error: unknown option `archive-level'\n\nNAME\n\tdolt gc - Cleans up unreferenced data from the repository.\n",
			want:   true,
		},
		{
			name:   "uppercase / mixed case tolerant",
			output: "ERROR: Unknown Option `archive-level'",
			want:   true,
		},
		{
			name:   "cobra-pflag style unknown flag message",
			output: "unknown flag: --archive-level",
			want:   true,
		},
		{
			name:   "go flag package style message",
			output: "flag provided but not defined: -archive-level",
			want:   true,
		},
		{
			name:   "underscore spelling still matches",
			output: "error: unknown option `archive_level'",
			want:   true,
		},
		{
			name:   "genuine GC failure must not be swallowed",
			output: "error: could not acquire lock on repository; another process may be using it",
			want:   false,
		},
		{
			name:   "unrelated unknown-option error for a different flag",
			output: "error: unknown option `full'",
			want:   false,
		},
		{
			name:   "mentions archive-level but is not an unknown-flag error",
			output: "Specify the archive compression level garbage collection results. Default is 1, Disable with 0",
			want:   false,
		},
		{
			name:   "empty output",
			output: "",
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUnknownArchiveLevelFlagError(tc.output); got != tc.want {
				t.Errorf("isUnknownArchiveLevelFlagError(%q) = %v, want %v", tc.output, got, tc.want)
			}
		})
	}
}
