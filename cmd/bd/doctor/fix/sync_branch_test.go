package fix

import "testing"

func TestParseGitLsFilesFlag(t *testing.T) {
	tests := map[string]struct {
		flag                byte
		wantHasAnyFlag      bool
		wantHasSkipWorktree bool
	}{
		"normal tracked file (H)": {
			flag:                'H',
			wantHasAnyFlag:      false,
			wantHasSkipWorktree: false,
		},
		"assume-unchanged only (h)": {
			flag:                'h',
			wantHasAnyFlag:      true,
			wantHasSkipWorktree: false,
		},
		"skip-worktree only (S)": {
			flag:                'S',
			wantHasAnyFlag:      true,
			wantHasSkipWorktree: true,
		},
		"both flags set (s)": {
			flag:                's',
			wantHasAnyFlag:      true,
			wantHasSkipWorktree: true,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			gotHasAnyFlag, gotHasSkipWorktree := parseGitLsFilesFlag(tt.flag)

			if gotHasAnyFlag != tt.wantHasAnyFlag {
				t.Errorf("hasAnyFlag = %v, want %v", gotHasAnyFlag, tt.wantHasAnyFlag)
			}

			if gotHasSkipWorktree != tt.wantHasSkipWorktree {
				t.Errorf("hasSkipWorktree = %v, want %v", gotHasSkipWorktree, tt.wantHasSkipWorktree)
			}
		})
	}
}
