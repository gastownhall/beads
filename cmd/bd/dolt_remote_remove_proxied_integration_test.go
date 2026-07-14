//go:build cgo

package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestProxiedServerDoltRemoteRemove(t *testing.T) {
	requireProxiedServerEnv(t)
	bd := buildEmbeddedBD(t)

	seedRemote := func(t *testing.T, p proxiedProject, name, url string) {
		t.Helper()
		db := openProxiedDB(t, p)
		if _, err := db.ExecContext(context.Background(),
			"CALL DOLT_REMOTE('add', ?, ?)", name, url); err != nil {
			t.Fatalf("seed remote %s: %v", name, err)
		}
	}

	remoteCount := func(t *testing.T, p proxiedProject, name string) int {
		t.Helper()
		db := openProxiedDB(t, p)
		var count int
		if err := db.QueryRowContext(context.Background(),
			"SELECT COUNT(*) FROM dolt_remotes WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatalf("query dolt_remotes: %v", err)
		}
		return count
	}

	t.Run("removes_existing_remote", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "prr1")
		seedRemote(t, p, "backup", "https://doltremoteapi.dolthub.com/org/backup")

		out, err := bdProxiedRun(t, bd, p.dir, "dolt", "remote", "remove", "backup")
		if err != nil {
			t.Fatalf("remote remove failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), `Removed remote "backup"`) {
			t.Errorf("unexpected output: %s", out)
		}
		if c := remoteCount(t, p, "backup"); c != 0 {
			t.Errorf("remote 'backup' still present after remove (count=%d)", c)
		}
	})

	t.Run("json_output", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "prr2")
		seedRemote(t, p, "backup", "https://doltremoteapi.dolthub.com/org/backup")

		out, err := bdProxiedRun(t, bd, p.dir, "dolt", "remote", "remove", "--json", "backup")
		if err != nil {
			t.Fatalf("remote remove --json failed: %v\n%s", err, out)
		}
		s := string(out)
		start := strings.Index(s, "{")
		if start < 0 {
			t.Fatalf("no JSON object in output: %s", s)
		}
		var res struct {
			Name    string `json:"name"`
			Removed bool   `json:"removed"`
		}
		if err := json.Unmarshal([]byte(s[start:]), &res); err != nil {
			t.Fatalf("parse JSON: %v\n%s", err, s)
		}
		if res.Name != "backup" || !res.Removed {
			t.Errorf("unexpected JSON result: %+v", res)
		}
		if c := remoteCount(t, p, "backup"); c != 0 {
			t.Errorf("remote 'backup' still present after remove (count=%d)", c)
		}
	})

	t.Run("nonexistent_remote_errors", func(t *testing.T) {
		p := bdProxiedInit(t, bd, "prr3")
		out, err := bdProxiedRun(t, bd, p.dir, "dolt", "remote", "remove", "ghost")
		if err == nil {
			t.Fatalf("expected error removing nonexistent remote, got:\n%s", out)
		}
		s := string(out)
		if !strings.Contains(s, "removing remote") || !strings.Contains(s, "ghost") {
			t.Errorf("expected a remove-failure error naming %q, got:\n%s", "ghost", s)
		}
	})
}
