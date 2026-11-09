//go:build cgo

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestEmbeddedTemplateGuardAcrossWriteVerbs runs the template-guard table
// (templateGuardVerbs) on the direct route over embedded Dolt.
func TestEmbeddedTemplateGuardAcrossWriteVerbs(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, beadsDir, _ := bdInit(t, bd, "--prefix", "tgv")
	runTemplateGuardTable(t, templateGuardHarness{
		route: templateGuardDirect,
		run: func(t *testing.T, args ...string) (string, string, int) {
			t.Helper()
			cmd := exec.Command(bd, args...)
			cmd.Dir = dir
			cmd.Env = bdEnv(dir)
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			var exitErr *exec.ExitError
			switch {
			case err == nil:
				return stdout.String(), stderr.String(), 0
			case errors.As(err, &exitErr):
				return stdout.String(), stderr.String(), exitErr.ExitCode()
			}
			t.Fatalf("bd %s: %v", strings.Join(args, " "), err)
			return "", "", -1
		},
		create: func(t *testing.T, title string) string { return bdCreate(t, bd, dir, title, "--type", "task").ID },
		markTemplates: func(t *testing.T, ids []string) {
			for _, id := range ids {
				markIssueTemplate(t, beadsDir, "tgv", id)
			}
		},
	})
}
