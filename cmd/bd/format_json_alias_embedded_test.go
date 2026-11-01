//go:build cgo

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// bdRunStreams runs one bd process in dir with the hermetic test environment
// and returns stdout, stderr and the exit status separately. A nonzero exit is
// returned, not fatal, so a caller can pin the status itself; only a process
// that could not be started fails the test here.
func bdRunStreams(t *testing.T, bd, dir string, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	cmd := exec.Command(bd, args...)
	cmd.Dir = dir
	cmd.Env = bdEnv(dir)
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &outBuf, &errBuf
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("bd %s did not run: %v", strings.Join(args, " "), err)
		}
		return outBuf.String(), errBuf.String(), ee.ExitCode()
	}
	return outBuf.String(), errBuf.String(), 0
}

// TestEmbeddedFormatJSONAliasMatchesJSON pins the hidden root --format alias
// (GH#2612): on show, list, ready and memories, `--format json` prints exactly
// what `--json` prints, byte for byte, and exits 0 like it.
//
// The alias is a root persistent flag, so any subcommand that registers a
// --format of its own shadows it (the GH#6278 hazard). `bd list` already has
// its own --format and passes `json` through by hand (list_input.go). A
// local --format added to show, ready or memories for another output
// format would turn `bd show ID --format json` into a refusal or into a
// different document. Both are regressions this test catches: it compares
// the whole of stdout, not a parse of it.
//
// Each command is checked in three spellings: `--format json` and
// `--format=json` after the subcommand, and `--format json` before it, where
// the flag is plainly the root's.
func TestEmbeddedFormatJSONAliasMatchesJSON(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "fj")

	id := bdCreateSilent(t, bd, dir, "Format alias parity", "--type", "task", "--description", "read through --format json")
	bdRunOK(t, bd, dir, "remember", "the format alias is --json", "--key", "fj-alias")

	for _, tc := range []struct {
		name string
		args []string
		// mention is a value the --json document must contain, so two empty
		// documents cannot match each other.
		mention string
	}{
		{"show", []string{"show", id}, id},
		{"list", []string{"list"}, id},
		{"ready", []string{"ready"}, id},
		{"memories", []string{"memories"}, "fj-alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wantArgs := slices.Concat(tc.args, []string{"--json"})
			want, wantErr, exit := bdRunStreams(t, bd, dir, wantArgs...)
			if exit != 0 {
				t.Fatalf("bd %s exited %d\nstdout:\n%s\nstderr:\n%s", strings.Join(wantArgs, " "), exit, want, wantErr)
			}
			if !json.Valid([]byte(want)) || !strings.Contains(want, tc.mention) {
				t.Fatalf("bd %s did not print one JSON document naming %s:\n%s", strings.Join(wantArgs, " "), tc.mention, want)
			}

			for _, args := range [][]string{
				slices.Concat(tc.args, []string{"--format", "json"}),
				slices.Concat(tc.args, []string{"--format=json"}),
				slices.Concat([]string{"--format", "json"}, tc.args),
			} {
				got, gotErr, exit := bdRunStreams(t, bd, dir, args...)
				if exit != 0 {
					t.Errorf("bd %s exited %d, want 0 like bd %s\nstdout:\n%s\nstderr:\n%s", strings.Join(args, " "), exit, strings.Join(wantArgs, " "), got, gotErr)
					continue
				}
				if got != want {
					t.Errorf("bd %s printed a different document from bd %s\n--format:\n%s\n--json:\n%s", strings.Join(args, " "), strings.Join(wantArgs, " "), got, want)
				}
			}
		})
	}
}
