//go:build cgo

package main

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/steveyegge/beads/internal/types"
)

func TestProxiedServerExternalCreate(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	port := testutil.StartIsolatedDoltContainer(t)
	portInt, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("parse mapped port %q: %v", port, err)
	}

	p := bdProxiedInit(t, bd, "ext",
		"--proxied-server-external-host", "127.0.0.1",
		"--proxied-server-external-port", port,
	)

	info, err := configfile.LoadProxiedServerClientInfo(p.beadsDir)
	if err != nil {
		t.Fatalf("LoadProxiedServerClientInfo: %v", err)
	}
	if info == nil || info.External == nil {
		t.Fatalf("expected External block in proxied_server_client_info.json, got %+v", info)
	}
	if info.External.Host != "127.0.0.1" {
		t.Errorf("external host: got %q, want %q", info.External.Host, "127.0.0.1")
	}
	if info.External.Port != portInt {
		t.Errorf("external port: got %d, want %d", info.External.Port, portInt)
	}

	issue := bdProxiedCreate(t, bd, p.dir, "External proxied issue")
	if issue.ID == "" {
		t.Fatal("expected issue ID")
	}
	if issue.Title != "External proxied issue" {
		t.Errorf("title: got %q, want %q", issue.Title, "External proxied issue")
	}
	if issue.Status != types.StatusOpen {
		t.Errorf("status: got %q, want %q", issue.Status, types.StatusOpen)
	}

	directDSN := fmt.Sprintf("root:@tcp(127.0.0.1:%s)/%s?parseTime=true", port, p.database)
	db, err := sql.Open("mysql", directDSN)
	if err != nil {
		t.Fatalf("sql.Open %s: %v", directDSN, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var title string
	if err := db.QueryRowContext(ctx, "SELECT title FROM issues WHERE id = ?", issue.ID).Scan(&title); err != nil {
		t.Fatalf("direct SELECT from external dolt for %s: %v", issue.ID, err)
	}
	if title != "External proxied issue" {
		t.Errorf("title in external dolt: got %q, want %q", title, "External proxied issue")
	}
}
