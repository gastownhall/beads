//go:build cgo

package main

import (
	"os"
	"os/exec"
	"slices"
	"testing"
)

// TestProxiedServerSearchRoutesAgree is the proxied and remote routes of the
// `bd search` route agreement (search_routes_parity_test.go). Both answer every
// flag set as searchRouteQueries expects, the answers
// TestEmbeddedSearchRoutesAgree holds the direct route to. The remote route is
// `bd serve` over the proxied workspace (bd serve needs a SQL server, so it
// cannot serve an embedded one), so it reads the SAME database as the proxied
// route and must answer it row for row, ids and order included.
func TestProxiedServerSearchRoutesAgree(t *testing.T) {
	if os.Getenv("BEADS_TEST_PROXIED_SERVER") != "1" {
		t.Skip("set BEADS_TEST_PROXIED_SERVER=1 to run proxied server integration tests")
	}
	bd := buildEmbeddedBD(t)
	p := newSharedProxiedProject(t, bd, "sp")
	seedSearchRoutes(searchRouteOps{
		create: func(title string, args []string) string {
			return bdProxiedCreate(t, bd, p.dir, append([]string{title}, args...)...).ID
		},
		close: func(id string) { bdProxiedClose(t, bd, p.dir, id) },
		markTemplate: func(id string) {
			res, err := openProxiedDB(t, p).Exec("UPDATE issues SET is_template = 1 WHERE id = ?", id)
			if err != nil {
				t.Fatalf("mark %s a template: %v", id, err)
			}
			if n, err := res.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("mark %s a template: %d rows affected (err %v), want 1", id, n, err)
			}
		},
		promote: func(id string) {
			if out, err := bdProxiedRun(t, bd, p.dir, "promote", id); err != nil {
				t.Fatalf("bd promote %s: %v\n%s", id, err, out)
			}
		},
	})
	proxied := checkSearchRoute(t, "proxied", bd, p.dir, bdProxiedEnv(p.dir), false)

	sp := startServe(t, bd, p.dir, bdProxiedEnv(p.dir))
	remoteDir := t.TempDir()
	connect := exec.Command(bd, "connect", "http://"+sp.addr, "--json")
	connect.Dir = remoteDir
	connect.Env = bdEnv(remoteDir)
	if out, err := connect.CombinedOutput(); err != nil {
		t.Fatalf("bd connect: %v\n%s", err, out)
	}
	remote := checkSearchRoute(t, "remote", bd, remoteDir, bdEnv(remoteDir), true)
	for _, q := range searchRouteQueries {
		got, gotOK := remote[q.name]
		want, wantOK := proxied[q.name]
		if !gotOK || !wantOK {
			continue // refused or failed on a route: reported above
		}
		if !slices.Equal(got, want) {
			t.Errorf("%s: remote answered %+v, proxied answered %+v", q.name, got, want)
		}
	}
}
