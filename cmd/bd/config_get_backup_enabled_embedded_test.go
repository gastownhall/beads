//go:build cgo

package main

import "testing"

func TestConfigGetBackupEnabled_EffectiveValue_Embedded(t *testing.T) {
	runConfigGetBackupEnabledEffectiveValueCases(t, []backupEnabledEffectiveValueCase{
		{
			name:         "unset + no remote → off (no git remote)",
			envVal:       "\x00",
			hasRemote:    false,
			wantPrefix:   "false ",
			wantContains: "no git remote",
		},
		{
			name:         "unset + remote → on (git remote)",
			envVal:       "\x00",
			hasRemote:    true,
			wantPrefix:   "true ",
			wantContains: "git remote detected",
		},
	})
}
