//go:build cgo

package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestProxiedServerDoltDropDatabase is AC-7 of GH#6099 on the proxied-server
// route: the named drop runs over the maintenance connection, drops only
// that database, and leaves it recoverable with DOLT_UNDROP.
func TestProxiedServerDoltDropDatabase(t *testing.T) {
	requireProxiedServerEnv(t)

	bd := buildEmbeddedBD(t)
	p := bdProxiedInit(t, bd, "dropnamed")

	const target, bystander = "dropnamed_target", "dropnamed_keep"
	for _, name := range []string{target, bystander} {
		bdProxiedSQL(t, bd, p.dir, "CREATE DATABASE `"+name+"`")
	}
	exists := func(name string) bool {
		rows := bdProxiedSQLJSON(t, bd, p.dir,
			"SELECT COUNT(*) as count FROM information_schema.schemata WHERE schema_name = '"+name+"'")
		return len(rows) == 1 && sqlValueEquals(rows[0]["count"], 1)
	}

	stdout, stderr, err := bdProxiedRunBuffers(t, bd, p.dir, "dolt", "drop-database", target, "--yes", "--json")
	if err != nil {
		t.Fatalf("bd dolt drop-database failed: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	var got dropDatabaseResult
	if i := strings.Index(stdout, "{"); i < 0 || json.Unmarshal([]byte(stdout[i:]), &got) != nil {
		t.Fatalf("drop-database --json output is not the result object:\n%s", stdout)
	}
	if got != (dropDatabaseResult{Dropped: target, RecoverableViaUndrop: true}) {
		t.Fatalf("result = %+v, want dropped=%s recoverable_via_undrop=true", got, target)
	}
	if exists(target) {
		t.Fatalf("%s still exists after drop-database", target)
	}
	if !exists(bystander) {
		t.Fatalf("drop-database %s also removed %s", target, bystander)
	}
	if _, _, err := bdProxiedRunBuffers(t, bd, p.dir, "sql", fmt.Sprintf("CALL DOLT_UNDROP('%s')", target)); err != nil {
		t.Fatalf("DOLT_UNDROP(%q) failed: the drop was purged or never recoverable: %v", target, err)
	}
}
