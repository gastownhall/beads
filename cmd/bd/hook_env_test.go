package main

import (
	"runtime"
	"slices"
	"testing"
)

func TestFilterChainedHookEnvUsesHostKeyIdentity(t *testing.T) {
	env := []string{
		"BD_GIT_HOOK=canonical", "bd_git_hook=mixed", "BD_GİT_HOOK=unicode-case",
		"BD_GıT_HOOK=distinct", "KEEP=first", "KEEP=BD_GIT_HOOK=1",
		"BD_GIT_HOOK", `=C:=C:\work`,
	}
	original := slices.Clone(env)
	want := []string{"BD_GıT_HOOK=distinct", "KEEP=first", "KEEP=BD_GIT_HOOK=1", "BD_GIT_HOOK", `=C:=C:\work`}
	if runtime.GOOS != "windows" {
		want = append([]string{"bd_git_hook=mixed", "BD_GİT_HOOK=unicode-case"}, want...)
	}
	if got := filterEnv(env, "BD_GIT_HOOK"); !slices.Equal(got, want) {
		t.Errorf("chained-hook environment = %q, want %q", got, want)
	}
	if !slices.Equal(env, original) {
		t.Errorf("input environment mutated: %q", env)
	}
}

func TestScrubGitHookEnvUsesHostKeyIdentity(t *testing.T) {
	env := []string{
		"GIT_DIR=canonical", "git_dir=mixed", "GİT_DİR=unicode-case", "GIT_DıR=distinct",
		"GIT_CONFIG_COUNT=1", "git_config_parameters=mixed", "gİt_config_count=unicode-case",
		"GIT_CONFIG_UNSET", "git_config_unset", "KEEP=first", "KEEP=GIT_CONFIG_COUNT=1",
		"GIT_AUTHOR_NAME=author", "GIT_OPTIONAL_LOCKS=1", "GIT_DIR", "MALFORMED", `=C:=C:\work`,
	}
	original := slices.Clone(env)
	want := []string{"GIT_DıR=distinct"}
	if runtime.GOOS != "windows" {
		want = append([]string{"git_dir=mixed", "GİT_DİR=unicode-case"}, want...)
		want = append(want, "git_config_parameters=mixed", "gİt_config_count=unicode-case", "git_config_unset")
	}
	want = append(want, "KEEP=first", "KEEP=GIT_CONFIG_COUNT=1", "GIT_AUTHOR_NAME=author", "GIT_OPTIONAL_LOCKS=1", "GIT_DIR", "MALFORMED", `=C:=C:\work`)
	if got := scrubGitHookEnv(env); !slices.Equal(got, want) {
		t.Errorf("Git-hook environment = %q, want %q", got, want)
	}
	if !slices.Equal(env, original) {
		t.Errorf("input environment mutated: %q", env)
	}
}
