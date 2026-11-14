//go:build cgo

package main

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// TestProxiedServerTemplateGuardAcrossWriteVerbs runs the template-guard table
// (templateGuardVerbs) on the --proxied-server route.
func TestProxiedServerTemplateGuardAcrossWriteVerbs(t *testing.T) {
	requireSharedProxiedServer(t)
	t.Parallel()
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "tgp")
	runTemplateGuardTable(t, templateGuardHarness{
		route: templateGuardProxied,
		run: func(t *testing.T, args ...string) (string, string, int) {
			t.Helper()
			stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, args...)
			var exitErr *exec.ExitError
			switch {
			case err == nil:
				return stdout, stderr, 0
			case errors.As(err, &exitErr):
				return stdout, stderr, exitErr.ExitCode()
			}
			t.Fatalf("bd %s: %v", strings.Join(args, " "), err)
			return "", "", -1
		},
		create: func(t *testing.T, title string) string {
			return bdProxiedCreate(t, bd, p.dir, title, "--type", "task").ID
		},
		markTemplates: func(t *testing.T, ids []string) {
			db := openProxiedDB(t, p)
			for _, id := range ids {
				res, err := db.Exec("UPDATE issues SET is_template = 1 WHERE id = ?", id)
				if err != nil {
					t.Fatalf("mark %s a template: %v", id, err)
				}
				if n, err := res.RowsAffected(); err != nil || n != 1 {
					t.Fatalf("mark %s a template: %d rows affected (err %v), want 1", id, n, err)
				}
			}
		},
	})
}
